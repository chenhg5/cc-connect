package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/containerhost"
	"github.com/chenhg5/cc-connect/controlstore"
	"github.com/chenhg5/cc-connect/releasecontract"
	"github.com/chenhg5/cc-connect/releaseinstall"
	"github.com/chenhg5/cc-connect/runtimeprotocol"
	"golang.org/x/sys/unix"
)

type DeploymentConfig struct {
	Owner             string
	RunningVersion    string
	ReleasesDirectory string
	CurrentLink       string
	ControlDatabase   string
	ActivationPath    string
	ReleaseClient     *releaseinstall.Client
	ContainerHost     containerDeploymentHost
	RestartControl    func()
}

const (
	DeploymentOwnerSystemd   = "systemd"
	DeploymentOwnerContainer = "container"
)

type DeploymentCapabilities struct {
	Owner     string `json:"owner"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	Detail    string `json:"detail,omitempty"`
	Update    bool   `json:"update"`
	Rollback  bool   `json:"rollback"`
	Restart   bool   `json:"restart"`
}

type DeploymentManager struct {
	config     DeploymentConfig
	store      *controlstore.Store
	broker     deploymentBroker
	supervisor deploymentSupervisor

	mu         sync.Mutex
	operations map[string]context.CancelFunc
}

type deploymentBroker interface {
	Devices(context.Context) ([]DeviceStatus, error)
	Call(context.Context, string, runtimeprotocol.Method, runtimeprotocol.Resource, json.RawMessage) (json.RawMessage, error)
}

type deploymentSupervisor interface {
	RuntimeActivity(context.Context) (ServerRuntimeActivity, error)
	Start(context.Context) error
	Stop(context.Context) error
	Restart(context.Context) error
}

type containerDeploymentHost interface {
	LatestTag(context.Context) (string, error)
	Prepare(context.Context, string) (releaseinstall.Release, containerhost.Preparation, error)
	Status(context.Context) (containerhost.Status, error)
	Activate(context.Context, containerhost.ActivateRequest) error
	Commit(context.Context, string) error
	Cancel(context.Context, string) error
	Confirm(context.Context, string) error
}

func NewDeploymentManager(config DeploymentConfig, store *controlstore.Store, broker deploymentBroker, supervisor deploymentSupervisor) (*DeploymentManager, error) {
	if store == nil || broker == nil || supervisor == nil {
		return nil, errors.New("deployment manager: store, broker and supervisor are required")
	}
	if config.Owner == "" {
		config.Owner = DeploymentOwnerSystemd
	}
	switch config.Owner {
	case DeploymentOwnerSystemd:
		if config.ReleaseClient == nil || config.RestartControl == nil {
			return nil, errors.New("deployment manager: systemd owner requires release client and restart callback")
		}
		for _, path := range []string{config.ReleasesDirectory, config.CurrentLink, config.ControlDatabase, config.ActivationPath} {
			if !filepath.IsAbs(strings.TrimSpace(path)) {
				return nil, errors.New("deployment manager: absolute release, activation and database paths are required")
			}
		}
	case DeploymentOwnerContainer:
		if config.ContainerHost == nil || strings.TrimSpace(config.RunningVersion) == "" ||
			!filepath.IsAbs(strings.TrimSpace(config.ControlDatabase)) || !filepath.IsAbs(strings.TrimSpace(config.ActivationPath)) {
			return nil, errors.New("deployment manager: container owner requires running version, host executor and absolute activation and database paths")
		}
	default:
		return nil, fmt.Errorf("deployment manager: unsupported owner %q", config.Owner)
	}
	return &DeploymentManager{config: config, store: store, broker: broker, supervisor: supervisor, operations: make(map[string]context.CancelFunc)}, nil
}

func (m *DeploymentManager) Capabilities(ctx context.Context) DeploymentCapabilities {
	capabilities := DeploymentCapabilities{Owner: m.config.Owner, Available: true, Update: true, Rollback: true, Restart: true}
	if m.config.Owner == DeploymentOwnerContainer {
		statusCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, err := m.config.ContainerHost.Status(statusCtx)
		cancel()
		if err != nil {
			capabilities.Available = false
			capabilities.Update = false
			capabilities.Rollback = false
			capabilities.Reason = "container_host_unavailable"
			capabilities.Detail = err.Error()
		}
	}
	return capabilities
}

func (m *DeploymentManager) Start(ctx context.Context, kind, targetTag string) (controlstore.DeployRun, error) {
	if kind != "update" && kind != "rollback" {
		return controlstore.DeployRun{}, errors.New("deployment manager: kind must be update or rollback")
	}
	lockedTag, err := m.resolveTarget(ctx, kind, targetTag)
	if err != nil {
		return controlstore.DeployRun{}, err
	}
	run, err := m.store.AcquireExecutionSlot(ctx, kind, lockedTag, "")
	if err != nil {
		return controlstore.DeployRun{}, err
	}
	operationCtx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.operations[run.ID] = cancel
	m.mu.Unlock()
	go m.execute(operationCtx, run)
	return run, nil
}

func (m *DeploymentManager) Restart(ctx context.Context) (controlstore.DeployRun, error) {
	run, err := m.store.AcquireExecutionSlot(ctx, "restart", "", "")
	if err != nil {
		return controlstore.DeployRun{}, err
	}
	operationCtx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.operations[run.ID] = cancel
	m.mu.Unlock()
	go m.execute(operationCtx, run)
	return run, nil
}

func (m *DeploymentManager) Cancel(runID string) bool {
	m.mu.Lock()
	cancel := m.operations[runID]
	m.mu.Unlock()
	if cancel != nil {
		cancel()
		return true
	}
	return false
}

func (m *DeploymentManager) resolveTarget(ctx context.Context, kind, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if kind == "update" {
		if requested != "" {
			return requested, nil
		}
		if m.config.Owner == DeploymentOwnerContainer {
			return m.config.ContainerHost.LatestTag(ctx)
		}
		return m.config.ReleaseClient.LatestTag(ctx)
	}
	if m.config.Owner == DeploymentOwnerContainer {
		status, err := m.config.ContainerHost.Status(ctx)
		if err != nil {
			return "", err
		}
		if status.PreviousTag == "" || (requested != "" && requested != status.PreviousTag) {
			return "", errors.New("deployment manager: rollback is limited to the previous successful release")
		}
		return status.PreviousTag, nil
	}
	current, err := filepath.EvalSymlinks(m.config.CurrentLink)
	if err != nil {
		return "", fmt.Errorf("deployment manager: resolve current release: %w", err)
	}
	slots, err := m.store.ReleaseSlots(ctx, "succeeded")
	if err != nil {
		return "", err
	}
	for _, slot := range slots {
		if sameFile(slot.Directory, current) {
			continue
		}
		if requested == "" || requested == slot.Tag {
			return slot.Tag, nil
		}
	}
	return "", errors.New("deployment manager: rollback is limited to the previous successful release")
}

func (m *DeploymentManager) execute(ctx context.Context, run controlstore.DeployRun) {
	committed := false
	var runErr error
	defer func() {
		m.mu.Lock()
		delete(m.operations, run.ID)
		m.mu.Unlock()
		if committed {
			return
		}
		status := "failed"
		if errors.Is(runErr, context.Canceled) {
			status = "cancelled"
		}
		message := ""
		if runErr != nil {
			message = runErr.Error()
		}
		_ = m.store.FinishExecution(context.Background(), run.ID, status, message)
	}()
	log := func(line string) {
		_, _ = m.store.AppendRunLog(context.Background(), run.ID, "control", line)
	}
	fail := func(err error) {
		runErr = err
		log("失败: " + err.Error())
	}
	if run.Kind == "restart" {
		log("正在重启业务进程")
		runErr = m.supervisor.Restart(ctx)
		if runErr != nil {
			fail(runErr)
			return
		}
		log("业务进程重启完成")
		_ = m.store.FinishExecution(context.Background(), run.ID, "succeeded", "")
		committed = true
		return
	}
	if m.config.Owner == DeploymentOwnerContainer {
		m.executeContainer(ctx, run, log, fail, &committed)
		return
	}

	log("锁定 Release " + run.TargetTag)
	release, err := m.config.ReleaseClient.Fetch(ctx, run.TargetTag)
	if err != nil {
		fail(err)
		return
	}
	if run.Kind == "rollback" && release.Manifest.ControlSchema < controlstore.SchemaVersion {
		fail(fmt.Errorf("deployment manager: target control schema %d cannot read current schema %d", release.Manifest.ControlSchema, controlstore.SchemaVersion))
		return
	}
	activity, err := m.supervisor.RuntimeActivity(ctx)
	if err != nil {
		fail(err)
		return
	}
	if activity.Busy() {
		fail(fmt.Errorf("deployment manager: active turns=%d interactions=%d realtime=%d block activation", activity.ActiveTurns, activity.PendingInteractions, activity.RealtimeSessions))
		return
	}
	if err := m.checkDisk(release.Manifest); err != nil {
		fail(err)
		return
	}
	log("签名和运行前检查通过")

	targetDirectory, err := m.installServerSlot(ctx, release)
	if err != nil {
		fail(err)
		return
	}
	previousDirectory, err := filepath.EvalSymlinks(m.config.CurrentLink)
	if err != nil {
		fail(err)
		return
	}
	previousTag := filepath.Base(previousDirectory)
	if sameFile(targetDirectory, previousDirectory) {
		fail(errors.New("deployment manager: target release is already active"))
		return
	}

	online, err := m.stageRuntimes(ctx, run.TargetTag)
	if err != nil {
		fail(err)
		return
	}
	log(fmt.Sprintf("Runtime 暂存完成：在线 %d 台，离线设备保留待更新状态", len(online)))

	backup := filepath.Join(filepath.Dir(m.config.ControlDatabase), "backups", "control-"+run.ID+".db")
	if err := m.store.Backup(ctx, backup); err != nil {
		fail(err)
		return
	}
	record := ActivationRecord{RunID: run.ID, Kind: run.Kind, TargetTag: run.TargetTag, TargetDirectory: targetDirectory,
		PreviousTag: previousTag, PreviousDirectory: previousDirectory, DatabasePath: m.config.ControlDatabase, DatabaseBackup: backup, SkipNextRecovery: true}
	record.RuntimeDeviceIDs = append([]string(nil), online...)
	if err := m.store.SaveReleaseSlot(ctx, controlstore.ReleaseSlot{Tag: run.TargetTag, CommitSHA: release.Manifest.CommitSHA, Directory: targetDirectory, Manifest: release.ManifestRaw, Status: "candidate"}); err != nil {
		fail(err)
		return
	}
	if err := writeActivation(m.config.ActivationPath, record); err != nil {
		fail(err)
		return
	}
	if err := ctx.Err(); err != nil {
		_ = clearActivation(m.config.ActivationPath)
		fail(err)
		return
	}
	// 从停止业务进程开始进入不可取消提交阶段，避免留下已停止但未切换的半状态。
	m.mu.Lock()
	delete(m.operations, run.ID)
	m.mu.Unlock()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
	err = m.supervisor.Stop(stopCtx)
	stopCancel()
	if err != nil {
		_ = clearActivation(m.config.ActivationPath)
		fail(err)
		return
	}
	if err := atomicSymlink(targetDirectory, m.config.CurrentLink); err != nil {
		_ = clearActivation(m.config.ActivationPath)
		_ = m.supervisor.Start(context.Background())
		fail(err)
		return
	}

	activated := make([]string, 0, len(online))
	for _, deviceID := range online {
		payload, _ := runtimeprotocol.MarshalPayload(runtimeprotocol.RuntimeUpdateRequest{Tag: run.TargetTag})
		activateCtx, activateCancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, activateErr := m.broker.Call(activateCtx, deviceID, runtimeprotocol.MethodUpdateActivate, runtimeprotocol.Resource{}, payload)
		activateCancel()
		if activateErr != nil {
			_ = atomicSymlink(previousDirectory, m.config.CurrentLink)
			for _, rollbackDevice := range activated {
				rollbackPayload, _ := runtimeprotocol.MarshalPayload(runtimeprotocol.RuntimeUpdateRequest{Tag: previousTag})
				rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 30*time.Second)
				if _, rollbackErr := m.broker.Call(rollbackCtx, rollbackDevice, runtimeprotocol.MethodUpdateActivate, runtimeprotocol.Resource{}, rollbackPayload); rollbackErr == nil {
					_, _ = m.broker.Call(rollbackCtx, rollbackDevice, runtimeprotocol.MethodUpdateConfirm, runtimeprotocol.Resource{}, rollbackPayload)
				}
				rollbackCancel()
			}
			_ = clearActivation(m.config.ActivationPath)
			_ = m.supervisor.Start(context.Background())
			fail(fmt.Errorf("deployment manager: activate runtime %s: %w", deviceID, activateErr))
			return
		}
		activated = append(activated, deviceID)
		_ = m.store.SaveRuntimeUpdate(context.Background(), controlstore.RuntimeUpdate{DeviceID: deviceID, TargetTag: run.TargetTag, Status: "activating"})
	}
	log("候选槽已切换，等待 systemd 启动候选 control 完成确认")
	committed = true
	m.config.RestartControl()
}

func (m *DeploymentManager) ConfirmPending(ctx context.Context) (resultErr error) {
	record, err := ReadActivation(m.config.ActivationPath)
	if err != nil || record == nil {
		return err
	}
	current, err := filepath.EvalSymlinks(m.config.CurrentLink)
	if err != nil || !sameFile(current, record.TargetDirectory) {
		return errors.New("deployment manager: candidate control did not start from the pending target slot")
	}
	activity, err := m.supervisor.RuntimeActivity(ctx)
	if err != nil {
		return fmt.Errorf("deployment manager: candidate server health check failed: %w", err)
	}
	if activity.Busy() {
		return errors.New("deployment manager: candidate server unexpectedly restored active operations during activation")
	}
	if err := m.confirmRuntimes(ctx, record); err != nil {
		return err
	}
	runtimesConfirmed := true
	defer func() {
		if resultErr != nil && runtimesConfirmed {
			resultErr = errors.Join(resultErr, m.rollbackRuntimes(record.RuntimeDeviceIDs, record.PreviousTag))
		}
	}()
	raw, err := os.ReadFile(filepath.Join(record.TargetDirectory, "manifest.json"))
	if err != nil {
		return err
	}
	manifest, err := releasecontract.Decode(raw)
	if err != nil {
		return err
	}
	if err := m.store.SaveReleaseSlot(ctx, controlstore.ReleaseSlot{Tag: manifest.Tag, CommitSHA: manifest.CommitSHA, Directory: record.TargetDirectory, Manifest: raw, Status: "succeeded"}); err != nil {
		return err
	}
	if err := m.store.PutSetting(ctx, "current_release_tag", manifest.Tag); err != nil {
		return err
	}
	if err := m.store.FinishExecution(ctx, record.RunID, "succeeded", ""); err != nil {
		return err
	}
	if err := clearActivation(m.config.ActivationPath); err != nil {
		return err
	}
	runtimesConfirmed = false
	_ = os.Remove(record.DatabaseBackup)
	_, _ = m.store.AppendRunLog(ctx, record.RunID, "control", "候选 control、server 和 Runtime 协议健康检查通过")
	if err := m.prune(ctx, record.TargetDirectory); err != nil {
		_, _ = m.store.AppendRunLog(ctx, record.RunID, "control", "版本槽清理失败: "+err.Error())
		slog.Warn("deployment release slot pruning failed", "error", err)
	}
	return nil
}

func (m *DeploymentManager) RegisterCurrent(ctx context.Context) error {
	if m.config.Owner == DeploymentOwnerContainer {
		status, err := m.config.ContainerHost.Status(ctx)
		if err != nil {
			return err
		}
		if status.CurrentTag != m.config.RunningVersion {
			return fmt.Errorf("deployment manager: running control version %q does not match host current release %q", m.config.RunningVersion, status.CurrentTag)
		}
		return m.store.PutSetting(ctx, "current_release_tag", status.CurrentTag)
	}
	directory, err := filepath.EvalSymlinks(m.config.CurrentLink)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		return err
	}
	manifest, err := releasecontract.Decode(raw)
	if err != nil {
		return err
	}
	if err := m.store.SaveReleaseSlot(ctx, controlstore.ReleaseSlot{Tag: manifest.Tag, CommitSHA: manifest.CommitSHA, Directory: directory, Manifest: raw, Status: "succeeded"}); err != nil {
		return err
	}
	return m.store.PutSetting(ctx, "current_release_tag", manifest.Tag)
}

func (m *DeploymentManager) installServerSlot(ctx context.Context, release releaseinstall.Release) (string, error) {
	final := filepath.Join(m.config.ReleasesDirectory, release.Manifest.Tag)
	if _, err := os.Stat(final); err == nil {
		if err := validateInstalledServerSlot(final, release); err != nil {
			return "", err
		}
		return final, nil
	}
	if err := os.MkdirAll(m.config.ReleasesDirectory, 0o755); err != nil {
		return "", err
	}
	temporary, err := os.MkdirTemp(m.config.ReleasesDirectory, ".candidate-*")
	if err != nil {
		return "", err
	}
	remove := true
	defer func() {
		if remove {
			_ = os.RemoveAll(temporary)
		}
	}()
	for _, component := range []string{"control", "server"} {
		artifact, ok := release.Manifest.Artifact(component, "linux", runtime.GOARCH)
		if !ok {
			return "", fmt.Errorf("deployment manager: release has no linux/%s %s artifact", runtime.GOARCH, component)
		}
		archive := filepath.Join(temporary, artifact.Name)
		if err := m.config.ReleaseClient.DownloadArtifact(ctx, release, artifact, archive); err != nil {
			return "", err
		}
		binary := "cc-connect-" + component
		if err := releaseinstall.ExtractBinary(archive, filepath.Join(temporary, binary), binary); err != nil {
			return "", err
		}
		if err := os.Remove(archive); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(filepath.Join(temporary, "manifest.json"), release.ManifestRaw, 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(temporary, "manifest.bundle"), release.BundleRaw, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(temporary, final); err != nil {
		return "", fmt.Errorf("deployment manager: install server release slot: %w", err)
	}
	remove = false
	return final, nil
}

func validateInstalledServerSlot(directory string, release releaseinstall.Release) error {
	for _, binary := range []string{"cc-connect-control", "cc-connect-server"} {
		info, err := os.Stat(filepath.Join(directory, binary))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			return fmt.Errorf("deployment manager: installed release slot %s is incomplete", directory)
		}
	}
	raw, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		return fmt.Errorf("deployment manager: read installed manifest: %w", err)
	}
	manifest, err := releasecontract.Decode(raw)
	if err != nil || manifest.Tag != release.Manifest.Tag || manifest.CommitSHA != release.Manifest.CommitSHA || manifest.RuntimeContractHash != release.Manifest.RuntimeContractHash {
		return errors.New("deployment manager: installed release slot manifest does not match the signed release")
	}
	if info, err := os.Stat(filepath.Join(directory, "manifest.bundle")); err != nil || !info.Mode().IsRegular() {
		return errors.New("deployment manager: installed release slot signature bundle is unavailable")
	}
	return nil
}

func (m *DeploymentManager) confirmRuntimes(ctx context.Context, record *ActivationRecord) error {
	remaining := make(map[string]struct{}, len(record.RuntimeDeviceIDs))
	for _, deviceID := range record.RuntimeDeviceIDs {
		if strings.TrimSpace(deviceID) == "" {
			return errors.New("deployment manager: activation contains an empty runtime device id")
		}
		remaining[deviceID] = struct{}{}
	}
	for len(remaining) > 0 {
		devices, err := m.broker.Devices(ctx)
		if err != nil {
			return fmt.Errorf("deployment manager: inspect candidate runtime connections: %w", err)
		}
		for _, device := range devices {
			if device.Online {
				delete(remaining, device.ID)
			}
		}
		if len(remaining) == 0 {
			break
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("deployment manager: wait for activated runtimes: %w", ctx.Err())
		case <-timer.C:
		}
	}
	confirmed := make([]string, 0, len(record.RuntimeDeviceIDs))
	for _, deviceID := range record.RuntimeDeviceIDs {
		payload, _ := runtimeprotocol.MarshalPayload(runtimeprotocol.RuntimeUpdateRequest{Tag: record.TargetTag})
		if _, err := m.broker.Call(ctx, deviceID, runtimeprotocol.MethodUpdateConfirm, runtimeprotocol.Resource{}, payload); err != nil {
			return errors.Join(fmt.Errorf("deployment manager: confirm runtime %s: %w", deviceID, err), m.rollbackRuntimes(confirmed, record.PreviousTag))
		}
		confirmed = append(confirmed, deviceID)
		if err := m.store.SaveRuntimeUpdate(ctx, controlstore.RuntimeUpdate{DeviceID: deviceID, TargetTag: record.TargetTag, Status: "succeeded"}); err != nil {
			return errors.Join(fmt.Errorf("deployment manager: persist runtime confirmation: %w", err), m.rollbackRuntimes(confirmed, record.PreviousTag))
		}
	}
	return nil
}

func (m *DeploymentManager) rollbackRuntimes(deviceIDs []string, tag string) error {
	var result error
	for _, deviceID := range deviceIDs {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		payload, _ := runtimeprotocol.MarshalPayload(runtimeprotocol.RuntimeUpdateRequest{Tag: tag})
		if _, err := m.broker.Call(ctx, deviceID, runtimeprotocol.MethodUpdateActivate, runtimeprotocol.Resource{}, payload); err != nil {
			result = errors.Join(result, fmt.Errorf("rollback runtime %s: %w", deviceID, err))
			cancel()
			continue
		}
		if _, err := m.broker.Call(ctx, deviceID, runtimeprotocol.MethodUpdateConfirm, runtimeprotocol.Resource{}, payload); err != nil {
			result = errors.Join(result, fmt.Errorf("confirm runtime rollback %s: %w", deviceID, err))
		}
		cancel()
	}
	return result
}

func (m *DeploymentManager) checkDisk(manifest releasecontract.Manifest) error {
	var stats unix.Statfs_t
	if err := unix.Statfs(m.config.ReleasesDirectory, &stats); err != nil {
		return fmt.Errorf("deployment manager: inspect disk: %w", err)
	}
	var artifactBytes int64
	for _, artifact := range manifest.Artifacts {
		artifactBytes += artifact.Size
	}
	database, _ := os.Stat(m.config.ControlDatabase)
	required := artifactBytes*2 + 64<<20
	if database != nil {
		required += database.Size() * 2
	}
	available := int64(stats.Bavail) * int64(stats.Bsize)
	if available < required {
		return fmt.Errorf("deployment manager: insufficient disk space: available=%d required=%d", available, required)
	}
	return nil
}

func (m *DeploymentManager) prune(ctx context.Context, current string) error {
	slots, err := m.store.ReleaseSlots(ctx, "succeeded")
	if err != nil {
		return err
	}
	kept := make(map[string]struct{})
	for _, slot := range slots {
		if sameFile(slot.Directory, current) {
			kept[slot.Directory] = struct{}{}
			break
		}
	}
	for _, slot := range slots {
		if len(kept) >= 3 {
			break
		}
		kept[slot.Directory] = struct{}{}
	}
	for _, slot := range slots {
		if _, ok := kept[slot.Directory]; ok {
			continue
		}
		if filepath.Dir(slot.Directory) != m.config.ReleasesDirectory {
			return errors.New("deployment manager: release slot escaped releases directory")
		}
		if err := os.RemoveAll(slot.Directory); err != nil {
			return err
		}
		if err := m.store.DeleteReleaseSlot(ctx, slot.Tag); err != nil {
			return err
		}
	}
	return nil
}

func sameFile(left, right string) bool {
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo)
}
