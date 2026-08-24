package runtimeclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/releasecontract"
	"github.com/chenhg5/cc-connect/releaseinstall"
)

const runtimeActivationVersion = 1

type runtimeActivationRecord struct {
	Version           int       `json:"version"`
	TargetTag         string    `json:"target_tag"`
	TargetDirectory   string    `json:"target_directory"`
	PreviousDirectory string    `json:"previous_directory"`
	CreatedAt         time.Time `json:"created_at"`
}

type UpdateManagerConfig struct {
	StateDirectory  string
	ReleaseClient   *releaseinstall.Client
	RollbackTimeout time.Duration
}

type UpdateManager struct {
	state    string
	releases *releaseinstall.Client

	mu                 sync.Mutex
	restart            chan struct{}
	restarted          bool
	startedWithPending bool
	rollbackTimer      *time.Timer
}

func NewUpdateManager(config UpdateManagerConfig) (*UpdateManager, error) {
	if strings.TrimSpace(config.StateDirectory) == "" || config.ReleaseClient == nil {
		return nil, errors.New("runtime updater: state directory and release client are required")
	}
	state, err := filepath.Abs(config.StateDirectory)
	if err != nil {
		return nil, fmt.Errorf("runtime updater: resolve state directory: %w", err)
	}
	if config.RollbackTimeout <= 0 {
		config.RollbackTimeout = 45 * time.Second
	}
	manager := &UpdateManager{state: state, releases: config.ReleaseClient, restart: make(chan struct{})}
	pending, err := manager.readActivation()
	if err != nil {
		return nil, err
	}
	if pending != nil {
		manager.startedWithPending = true
		manager.rollbackTimer = time.AfterFunc(config.RollbackTimeout, func() {
			_ = manager.rollbackPending()
			manager.requestRestart()
		})
	}
	return manager, nil
}

func (m *UpdateManager) Stage(ctx context.Context, tag string) error {
	release, err := m.releases.Fetch(ctx, tag)
	if err != nil {
		return err
	}
	artifact, ok := release.Manifest.Artifact("runtime", "darwin", runtime.GOARCH)
	if !ok {
		return fmt.Errorf("runtime updater: release %s has no darwin/%s artifact", tag, runtime.GOARCH)
	}
	releasesDirectory := filepath.Join(m.state, "releases")
	if err := os.MkdirAll(releasesDirectory, 0o700); err != nil {
		return fmt.Errorf("runtime updater: create releases directory: %w", err)
	}
	finalDirectory := filepath.Join(releasesDirectory, tag)
	if _, statErr := os.Stat(finalDirectory); statErr == nil {
		return validateInstalledRuntimeSlot(finalDirectory, release)
	}
	temporaryDirectory, err := os.MkdirTemp(releasesDirectory, ".candidate-*")
	if err != nil {
		return err
	}
	remove := true
	defer func() {
		if remove {
			_ = os.RemoveAll(temporaryDirectory)
		}
	}()
	archivePath := filepath.Join(temporaryDirectory, artifact.Name)
	if err := m.releases.DownloadArtifact(ctx, release, artifact, archivePath); err != nil {
		return err
	}
	if err := releaseinstall.ExtractBinary(archivePath, filepath.Join(temporaryDirectory, "cc-connect-runtime"), "cc-connect-runtime"); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(temporaryDirectory, "manifest.json"), release.ManifestRaw, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(temporaryDirectory, "manifest.bundle"), release.BundleRaw, 0o600); err != nil {
		return err
	}
	if err := os.Remove(archivePath); err != nil {
		return err
	}
	if err := os.Rename(temporaryDirectory, finalDirectory); err != nil {
		if _, statErr := os.Stat(filepath.Join(finalDirectory, "cc-connect-runtime")); statErr == nil {
			return nil
		}
		return fmt.Errorf("runtime updater: install release slot: %w", err)
	}
	remove = false
	return nil
}

func (m *UpdateManager) Activate(_ context.Context, tag string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	target := filepath.Join(m.state, "releases", tag)
	if err := validateRuntimeSlot(target, tag); err != nil {
		return fmt.Errorf("runtime updater: staged release %s is unavailable", tag)
	}
	current, err := filepath.EvalSymlinks(filepath.Join(m.state, "current"))
	if err != nil {
		return fmt.Errorf("runtime updater: resolve current release: %w", err)
	}
	if sameRuntimeFile(current, target) {
		return errors.New("runtime updater: target release is already active")
	}
	record := runtimeActivationRecord{Version: runtimeActivationVersion, TargetTag: tag, TargetDirectory: target, PreviousDirectory: current, CreatedAt: time.Now().UTC()}
	if err := m.writeActivation(record); err != nil {
		return err
	}
	link := filepath.Join(m.state, "current")
	temporary := filepath.Join(m.state, ".current-new")
	_ = os.Remove(temporary)
	if err := os.Symlink(target, temporary); err != nil {
		_ = os.Remove(m.activationPath())
		return fmt.Errorf("runtime updater: create activation link: %w", err)
	}
	if err := os.Rename(temporary, link); err != nil {
		_ = os.Remove(temporary)
		_ = os.Remove(m.activationPath())
		return fmt.Errorf("runtime updater: activate release: %w", err)
	}
	m.requestRestartLocked()
	return nil
}

func (m *UpdateManager) Confirm(_ context.Context, tag string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, err := m.readActivation()
	if err != nil {
		return err
	}
	if record == nil || record.TargetTag != strings.TrimSpace(tag) {
		return errors.New("runtime updater: matching pending activation is required")
	}
	current, err := filepath.EvalSymlinks(filepath.Join(m.state, "current"))
	if err != nil || !sameRuntimeFile(current, record.TargetDirectory) {
		return errors.New("runtime updater: current release does not match pending activation")
	}
	if err := os.Remove(m.activationPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("runtime updater: confirm activation: %w", err)
	}
	if m.rollbackTimer != nil {
		m.rollbackTimer.Stop()
		m.rollbackTimer = nil
	}
	m.startedWithPending = false
	return nil
}

func (m *UpdateManager) RollbackStartupFailure() error {
	m.mu.Lock()
	startedWithPending := m.startedWithPending
	m.mu.Unlock()
	if !startedWithPending {
		return nil
	}
	return m.rollbackPending()
}

func (m *UpdateManager) rollbackPending() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, err := m.readActivation()
	if err != nil || record == nil {
		return err
	}
	if err := validateRuntimeDirectory(m.state, record.PreviousDirectory); err != nil {
		return err
	}
	link := filepath.Join(m.state, "current")
	temporary := filepath.Join(m.state, ".current-rollback")
	_ = os.Remove(temporary)
	if err := os.Symlink(record.PreviousDirectory, temporary); err != nil {
		return fmt.Errorf("runtime updater: create rollback link: %w", err)
	}
	if err := os.Rename(temporary, link); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("runtime updater: restore previous release: %w", err)
	}
	if err := os.Remove(m.activationPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	m.startedWithPending = false
	return nil
}

func (m *UpdateManager) requestRestart() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requestRestartLocked()
}

func (m *UpdateManager) requestRestartLocked() {
	if !m.restarted {
		m.restarted = true
		time.AfterFunc(500*time.Millisecond, func() { close(m.restart) })
	}
}

func (m *UpdateManager) activationPath() string {
	return filepath.Join(m.state, "pending-activation.json")
}

func (m *UpdateManager) readActivation() (*runtimeActivationRecord, error) {
	raw, err := os.ReadFile(m.activationPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("runtime updater: read pending activation: %w", err)
	}
	var record runtimeActivationRecord
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return nil, fmt.Errorf("runtime updater: decode pending activation: %w", err)
	}
	if record.Version != runtimeActivationVersion || strings.TrimSpace(record.TargetTag) == "" {
		return nil, errors.New("runtime updater: pending activation is incomplete")
	}
	if err := validateRuntimeDirectory(m.state, record.TargetDirectory); err != nil {
		return nil, err
	}
	if err := validateRuntimeDirectory(m.state, record.PreviousDirectory); err != nil {
		return nil, err
	}
	return &record, nil
}

func (m *UpdateManager) writeActivation(record runtimeActivationRecord) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.state, 0o700); err != nil {
		return err
	}
	temporary := m.activationPath() + ".tmp"
	if err := os.WriteFile(temporary, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, m.activationPath()); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("runtime updater: persist pending activation: %w", err)
	}
	return nil
}

func validateInstalledRuntimeSlot(directory string, release releaseinstall.Release) error {
	if err := validateRuntimeSlot(directory, release.Manifest.Tag); err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		return err
	}
	manifest, err := releasecontract.Decode(raw)
	if err != nil || manifest.CommitSHA != release.Manifest.CommitSHA || manifest.RuntimeContractHash != release.Manifest.RuntimeContractHash {
		return errors.New("runtime updater: installed slot manifest does not match signed release")
	}
	return nil
}

func validateRuntimeSlot(directory, tag string) error {
	if err := validateRuntimeDirectory(filepath.Dir(filepath.Dir(directory)), directory); err != nil {
		return err
	}
	info, err := os.Stat(filepath.Join(directory, "cc-connect-runtime"))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return errors.New("runtime updater: runtime binary is unavailable")
	}
	raw, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		return errors.New("runtime updater: signed manifest is unavailable")
	}
	manifest, err := releasecontract.Decode(raw)
	if err != nil || manifest.Tag != strings.TrimSpace(tag) {
		return errors.New("runtime updater: staged manifest tag does not match release slot")
	}
	if info, err := os.Stat(filepath.Join(directory, "manifest.bundle")); err != nil || !info.Mode().IsRegular() {
		return errors.New("runtime updater: signature bundle is unavailable")
	}
	return nil
}

func validateRuntimeDirectory(state, directory string) error {
	if !filepath.IsAbs(directory) {
		return errors.New("runtime updater: release directory escaped runtime state")
	}
	releases, err := filepath.EvalSymlinks(filepath.Join(state, "releases"))
	if err != nil {
		return fmt.Errorf("runtime updater: resolve releases directory: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return fmt.Errorf("runtime updater: resolve release directory: %w", err)
	}
	relative, err := filepath.Rel(releases, resolved)
	if err != nil || relative == "." || filepath.Base(relative) != relative || strings.HasPrefix(relative, "..") {
		return errors.New("runtime updater: release directory escaped runtime state")
	}
	return nil
}

func sameRuntimeFile(left, right string) bool {
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo)
}

func (m *UpdateManager) RestartRequested() <-chan struct{} { return m.restart }

var _ RuntimeUpdater = (*UpdateManager)(nil)
