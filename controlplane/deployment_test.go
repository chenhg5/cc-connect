package controlplane

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/containerhost"
	"github.com/chenhg5/cc-connect/controlstore"
	"github.com/chenhg5/cc-connect/releasecontract"
	"github.com/chenhg5/cc-connect/releaseinstall"
	"github.com/chenhg5/cc-connect/runtimeprotocol"
)

type deploymentTestSupervisor struct {
	mu      sync.Mutex
	running bool
}

func (s *deploymentTestSupervisor) RuntimeActivity(context.Context) (ServerRuntimeActivity, error) {
	return ServerRuntimeActivity{}, nil
}
func (s *deploymentTestSupervisor) Start(context.Context) error {
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	return nil
}
func (s *deploymentTestSupervisor) Stop(context.Context) error {
	s.mu.Lock()
	s.running = false
	s.mu.Unlock()
	return nil
}
func (s *deploymentTestSupervisor) Restart(context.Context) error {
	return s.Start(context.Background())
}

type deploymentTestBroker struct{}

func (deploymentTestBroker) Devices(context.Context) ([]DeviceStatus, error) { return nil, nil }
func (deploymentTestBroker) Call(context.Context, string, runtimeprotocol.Method, runtimeprotocol.Resource, json.RawMessage) (json.RawMessage, error) {
	return nil, nil
}

type deploymentConfirmBroker struct {
	devices []DeviceStatus
	failID  string
	calls   []string
}

type deploymentContainerHost struct {
	mu          sync.Mutex
	status      containerhost.Status
	release     releaseinstall.Release
	preparation containerhost.Preparation
	statusErr   error
}

func (h *deploymentContainerHost) LatestTag(context.Context) (string, error) {
	return h.release.Manifest.Tag, nil
}
func (h *deploymentContainerHost) Prepare(_ context.Context, tag string) (releaseinstall.Release, containerhost.Preparation, error) {
	if tag != h.release.Manifest.Tag {
		return releaseinstall.Release{}, containerhost.Preparation{}, errors.New("unexpected tag")
	}
	return h.release, h.preparation, nil
}
func (h *deploymentContainerHost) Status(context.Context) (containerhost.Status, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.status, h.statusErr
}
func (h *deploymentContainerHost) Activate(_ context.Context, request containerhost.ActivateRequest) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.status.Pending = &containerhost.PendingOperation{RunID: request.RunID, Kind: request.Kind, TargetTag: request.TargetTag,
		TargetImage: request.TargetImage, PreviousTag: h.status.CurrentTag, PreviousImage: h.status.CurrentImage,
		BackupName: request.BackupName, Deadline: time.Now().Add(time.Minute)}
	return nil
}
func (h *deploymentContainerHost) Commit(_ context.Context, runID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.status.Pending == nil || h.status.Pending.RunID != runID {
		return errors.New("pending activation mismatch")
	}
	h.status.Pending.Committed = true
	return nil
}
func (h *deploymentContainerHost) Cancel(_ context.Context, runID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.status.Pending != nil && h.status.Pending.RunID == runID {
		h.status.Pending = nil
	}
	return nil
}
func (h *deploymentContainerHost) Confirm(_ context.Context, runID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.status.Pending == nil || h.status.Pending.RunID != runID || !h.status.Pending.Committed {
		return errors.New("pending confirmation mismatch")
	}
	pending := h.status.Pending
	h.status.CurrentTag, h.status.CurrentImage = pending.TargetTag, pending.TargetImage
	h.status.PreviousTag, h.status.PreviousImage = pending.PreviousTag, pending.PreviousImage
	h.status.Pending = nil
	h.status.LastRunID, h.status.LastOutcome = runID, "succeeded"
	return nil
}

func (b *deploymentConfirmBroker) Devices(context.Context) ([]DeviceStatus, error) {
	return b.devices, nil
}
func (b *deploymentConfirmBroker) Call(_ context.Context, deviceID string, method runtimeprotocol.Method, _ runtimeprotocol.Resource, _ json.RawMessage) (json.RawMessage, error) {
	b.calls = append(b.calls, deviceID+":"+string(method))
	if method == runtimeprotocol.MethodUpdateConfirm && deviceID == b.failID {
		return nil, errors.New("confirm failed")
	}
	return nil, nil
}

func TestDeploymentManagerConfirmsReconnectedRuntimesAndRollsBackPartialFailure(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	database := filepath.Join(directory, "control.db")
	store, err := controlstore.Open(database, "setup")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	deviceIDs := make([]string, 0, 2)
	for _, name := range []string{"Mac 1", "Mac 2"} {
		publicKey, _, keyErr := ed25519.GenerateKey(nil)
		if keyErr != nil {
			t.Fatal(keyErr)
		}
		code, codeErr := store.CreatePairingCode(ctx)
		if codeErr != nil {
			t.Fatal(codeErr)
		}
		device, pairErr := store.PairDevice(ctx, code.Code, name, publicKey)
		if pairErr != nil {
			t.Fatal(pairErr)
		}
		deviceIDs = append(deviceIDs, device.ID)
	}
	broker := &deploymentConfirmBroker{failID: deviceIDs[1], devices: []DeviceStatus{
		{Device: controlstore.Device{ID: deviceIDs[0]}, Online: true},
		{Device: controlstore.Device{ID: deviceIDs[1]}, Online: true},
	}}
	manager := &DeploymentManager{store: store, broker: broker}
	err = manager.confirmRuntimes(ctx, &ActivationRecord{TargetTag: "v0.1.0", PreviousTag: "v0.0.9", RuntimeDeviceIDs: deviceIDs})
	if err == nil || !strings.Contains(err.Error(), "confirm runtime") {
		t.Fatalf("confirmRuntimes() error = %v", err)
	}
	want := []string{
		deviceIDs[0] + ":" + string(runtimeprotocol.MethodUpdateConfirm),
		deviceIDs[1] + ":" + string(runtimeprotocol.MethodUpdateConfirm),
		deviceIDs[0] + ":" + string(runtimeprotocol.MethodUpdateActivate),
		deviceIDs[0] + ":" + string(runtimeprotocol.MethodUpdateConfirm),
	}
	if strings.Join(broker.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("runtime calls = %#v, want %#v", broker.calls, want)
	}
}

func TestDeploymentManagerUpdateSurvivesControlHandoffAndConfirmsCandidate(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	releasesDirectory := filepath.Join(directory, "releases")
	if err := os.MkdirAll(releasesDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	artifacts := map[string][]byte{
		"control":    deploymentArchive(t, "cc-connect-control", []byte("new-control")),
		"server":     deploymentArchive(t, "cc-connect-server", []byte("new-server")),
		"deployhost": deploymentArchive(t, "cc-connect-deploy-host", []byte("new-deploy-host")),
		"runtime":    deploymentArchive(t, "cc-connect-runtime", []byte("new-runtime")),
	}
	manifest := deploymentManifest(t, "v0.1.0", artifacts)
	manifestRaw, _ := json.Marshal(manifest)
	releaseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/manifest.json"):
			_, _ = w.Write(manifestRaw)
		case strings.HasSuffix(r.URL.Path, "/manifest.bundle"):
			_, _ = w.Write([]byte("bundle"))
		case strings.Contains(r.URL.Path, "-control-"):
			_, _ = w.Write(artifacts["control"])
		case strings.Contains(r.URL.Path, "-server-"):
			_, _ = w.Write(artifacts["server"])
		default:
			_, _ = w.Write(artifacts["runtime"])
		}
	}))
	defer releaseServer.Close()
	releaseClient, err := releaseinstall.New(releaseinstall.Config{HTTPClient: releaseServer.Client(), ReleaseBase: releaseServer.URL,
		Verify: func(context.Context, string, []byte, []byte) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	initial := filepath.Join(releasesDirectory, "v0.0.9")
	if err := os.MkdirAll(initial, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, binary := range []string{"cc-connect-control", "cc-connect-server"} {
		if err := os.WriteFile(filepath.Join(initial, binary), []byte("old"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	initialManifest := deploymentManifest(t, "v0.0.9", artifacts)
	initialRaw, _ := json.Marshal(initialManifest)
	if err := os.WriteFile(filepath.Join(initial, "manifest.json"), initialRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(directory, "current")
	if err := os.Symlink(initial, current); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(directory, "control.db")
	store, err := controlstore.Open(database, "setup")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	restart := make(chan struct{}, 1)
	supervisor := &deploymentTestSupervisor{running: true}
	manager, err := NewDeploymentManager(DeploymentConfig{ReleasesDirectory: releasesDirectory, CurrentLink: current,
		ControlDatabase: database, ActivationPath: filepath.Join(directory, "activation.json"), ReleaseClient: releaseClient,
		RestartControl: func() { restart <- struct{}{} }}, store, deploymentTestBroker{}, supervisor)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.RegisterCurrent(ctx); err != nil {
		t.Fatal(err)
	}
	run, err := manager.Start(ctx, "update", "v0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-restart:
	case <-time.After(5 * time.Second):
		t.Fatal("deployment did not request control handoff")
	}
	pending, err := store.DeployRun(ctx, run.ID)
	if err != nil || pending.Status != "running" {
		t.Fatalf("pending run = %#v, %v", pending, err)
	}
	if err := manager.ConfirmPending(ctx); err != nil {
		t.Fatal(err)
	}
	completed, err := store.DeployRun(ctx, run.ID)
	if err != nil || completed.Status != "succeeded" {
		t.Fatalf("completed run = %#v, %v", completed, err)
	}
	resolved, _ := filepath.EvalSymlinks(current)
	if filepath.Base(resolved) != "v0.1.0" {
		t.Fatalf("current release = %s", resolved)
	}
}

func TestDeploymentManagerContainerOwnerExposesCapabilitiesAndKeepsRestart(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	database := filepath.Join(directory, "control.db")
	store, err := controlstore.Open(database, "setup")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	supervisor := &deploymentTestSupervisor{running: true}
	manifest := deploymentManifest(t, "v0.1.2", map[string][]byte{
		"control": []byte("control"), "server": []byte("server"), "deployhost": []byte("host"), "runtime": []byte("runtime"),
	})
	manifestRaw, _ := json.Marshal(manifest)
	host := &deploymentContainerHost{
		status:      containerhost.Status{CurrentTag: "v0.1.1", CurrentImage: "ghcr.io/shusfun/cc-connect@sha256:" + strings.Repeat("1", 64)},
		release:     releaseinstall.Release{Manifest: manifest, ManifestRaw: manifestRaw},
		preparation: containerhost.Preparation{Tag: "v0.1.2", Image: "ghcr.io/shusfun/cc-connect@sha256:" + strings.Repeat("2", 64), Manifest: manifestRaw},
	}
	manager, err := NewDeploymentManager(DeploymentConfig{
		Owner: DeploymentOwnerContainer, RunningVersion: "v0.1.1", ContainerHost: host,
		ControlDatabase: database, ActivationPath: filepath.Join(directory, "container-activation.json"),
	}, store, deploymentTestBroker{}, supervisor)
	if err != nil {
		t.Fatal(err)
	}
	capabilities := manager.Capabilities(ctx)
	if capabilities.Owner != DeploymentOwnerContainer || !capabilities.Available || !capabilities.Update || !capabilities.Rollback || !capabilities.Restart {
		t.Fatalf("container capabilities = %#v", capabilities)
	}
	if err := manager.RegisterCurrent(ctx); err != nil {
		t.Fatal(err)
	}
	if current, ok, err := store.Setting(ctx, "current_release_tag"); err != nil || !ok || current != "v0.1.1" {
		t.Fatalf("current release = %q, %v, %v", current, ok, err)
	}
	update, err := manager.Start(ctx, "update", "v0.1.2")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		status, _ := host.Status(ctx)
		if status.Pending != nil && status.Pending.Committed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("container update was not committed to host")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := manager.ConfirmContainerPending(ctx); err == nil || !strings.Contains(err.Error(), "running control version") {
		t.Fatalf("old control confirmed candidate: %v", err)
	}
	manager.config.RunningVersion = "v0.1.2"
	if err := manager.ConfirmContainerPending(ctx); err != nil {
		t.Fatal(err)
	}
	completed, err := store.DeployRun(ctx, update.ID)
	if err != nil || completed.Status != "succeeded" {
		t.Fatalf("container update run = %#v, %v", completed, err)
	}
	run, err := manager.Restart(ctx)
	if err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		stored, readErr := store.DeployRun(ctx, run.ID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if stored.Status == "succeeded" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("container restart status = %q", stored.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	host.mu.Lock()
	host.statusErr = errors.New("socket unavailable")
	host.mu.Unlock()
	unavailable := manager.Capabilities(ctx)
	if unavailable.Available || unavailable.Update || unavailable.Rollback || unavailable.Reason == "" || !unavailable.Restart {
		t.Fatalf("unavailable container capabilities = %#v", unavailable)
	}
}

func TestDeploymentManagerReconcilesHostRollbackAfterControlRestart(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	database := filepath.Join(directory, "control.db")
	store, err := controlstore.Open(database, "setup")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	run, err := store.AcquireExecutionSlot(ctx, "update", "v0.2.0", "")
	if err != nil {
		t.Fatal(err)
	}
	manifest := deploymentManifest(t, "v0.2.0", map[string][]byte{
		"control": []byte("control"), "server": []byte("server"), "deployhost": []byte("host"), "runtime": []byte("runtime"),
	})
	manifestRaw, _ := json.Marshal(manifest)
	activationPath := filepath.Join(directory, "container-activation.json")
	if err := writeContainerActivation(activationPath, ContainerActivationRecord{
		RunID: run.ID, Kind: "update", TargetTag: "v0.2.0",
		TargetImage: "ghcr.io/shusfun/cc-connect@sha256:" + strings.Repeat("2", 64), PreviousTag: "v0.1.0",
		BackupName: "control-" + run.ID + ".db", Manifest: manifestRaw,
	}); err != nil {
		t.Fatal(err)
	}
	host := &deploymentContainerHost{status: containerhost.Status{
		CurrentTag: "v0.1.0", CurrentImage: "ghcr.io/shusfun/cc-connect@sha256:" + strings.Repeat("1", 64),
		LastRunID: run.ID, LastOutcome: "failed", LastError: "candidate timeout",
	}}
	manager, err := NewDeploymentManager(DeploymentConfig{Owner: DeploymentOwnerContainer, RunningVersion: "v0.1.0", ContainerHost: host,
		ControlDatabase: database, ActivationPath: activationPath}, store, deploymentTestBroker{}, &deploymentTestSupervisor{running: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.ConfirmContainerPending(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err := store.DeployRun(ctx, run.ID)
	if err != nil || stored.Status != "failed" || !strings.Contains(stored.Error, "candidate timeout") {
		t.Fatalf("reconciled run = %#v, %v", stored, err)
	}
	if record, err := ReadContainerActivation(activationPath); err != nil || record != nil {
		t.Fatalf("activation record = %#v, %v", record, err)
	}
}

func deploymentManifest(t *testing.T, tag string, archives map[string][]byte) releasecontract.Manifest {
	t.Helper()
	manifest := releasecontract.Manifest{Version: 1, Repository: releasecontract.Repository, Workflow: releasecontract.Workflow,
		Tag: tag, CommitSHA: strings.Repeat("a", 40), RuntimeContractHash: runtimeprotocol.ContractHash,
		ControlSchema: controlstore.SchemaVersion, WorkspaceChatSchema: 3, GeneratedAt: time.Now().UTC()}
	for _, target := range [][3]string{{"control", "linux", "amd64"}, {"control", "linux", "arm64"}, {"server", "linux", "amd64"}, {"server", "linux", "arm64"}, {"deployhost", "linux", "amd64"}, {"deployhost", "linux", "arm64"}, {"runtime", "darwin", "amd64"}, {"runtime", "darwin", "arm64"}} {
		raw := archives[target[0]]
		digest := sha256.Sum256(raw)
		manifest.Artifacts = append(manifest.Artifacts, releasecontract.Artifact{Name: "cc-connect-" + strings.Join(target[:], "-") + ".tar.gz",
			Component: target[0], OS: target[1], Arch: target[2], SHA256: hex.EncodeToString(digest[:]), Size: int64(len(raw))})
	}
	if _, ok := manifest.Artifact("control", "linux", runtime.GOARCH); !ok {
		t.Fatalf("test manifest does not cover %s", runtime.GOARCH)
	}
	return manifest
}

func deploymentArchive(t *testing.T, name string, raw []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(gz)
	if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(raw)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	_, _ = writer.Write(raw)
	_ = writer.Close()
	_ = gz.Close()
	return buffer.Bytes()
}
