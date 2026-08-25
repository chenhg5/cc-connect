package controlplane

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chenhg5/cc-connect/containerhost"
	"github.com/chenhg5/cc-connect/controlstore"
	"github.com/chenhg5/cc-connect/releasecontract"
	"github.com/chenhg5/cc-connect/runtimeprotocol"
)

func (m *DeploymentManager) executeContainer(
	ctx context.Context,
	run controlstore.DeployRun,
	log func(string),
	fail func(error),
	committed *bool,
) {
	log("正在由宿主部署执行器锁定并验证 Release " + run.TargetTag)
	release, preparation, err := m.config.ContainerHost.Prepare(ctx, run.TargetTag)
	if err != nil {
		fail(err)
		return
	}
	if run.Kind == "rollback" && release.Manifest.ControlSchema < controlstore.SchemaVersion {
		fail(fmt.Errorf("deployment manager: target control schema %d cannot read current schema %d", release.Manifest.ControlSchema, controlstore.SchemaVersion))
		return
	}
	status, err := m.config.ContainerHost.Status(ctx)
	if err != nil {
		fail(err)
		return
	}
	if status.Pending != nil {
		fail(errors.New("deployment manager: host executor already has a pending activation"))
		return
	}
	if status.CurrentTag == run.TargetTag {
		fail(errors.New("deployment manager: target release is already active"))
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
	online, err := m.stageRuntimes(ctx, run.TargetTag)
	if err != nil {
		fail(err)
		return
	}
	log(fmt.Sprintf("Runtime 暂存完成：在线 %d 台，离线设备保留待更新状态", len(online)))

	backupName := "control-" + run.ID + ".db"
	backup := filepath.Join(filepath.Dir(m.config.ControlDatabase), "backups", backupName)
	if err := m.store.Backup(ctx, backup); err != nil {
		fail(err)
		return
	}
	record := ContainerActivationRecord{
		RunID: run.ID, Kind: run.Kind, TargetTag: run.TargetTag, TargetImage: preparation.Image,
		PreviousTag: status.CurrentTag, BackupName: backupName, RuntimeDeviceIDs: append([]string(nil), online...),
		Manifest: append([]byte(nil), release.ManifestRaw...),
	}
	if err := writeContainerActivation(m.config.ActivationPath, record); err != nil {
		fail(err)
		return
	}
	activated := make([]string, 0, len(online))
	cancelPending := func() error {
		if err := m.config.ContainerHost.Cancel(context.Background(), run.ID); err != nil {
			return err
		}
		removeErr := os.Remove(backup)
		if errors.Is(removeErr, os.ErrNotExist) {
			removeErr = nil
		}
		return errors.Join(clearContainerActivation(m.config.ActivationPath), removeErr)
	}
	if err := m.config.ContainerHost.Activate(ctx, containerhost.ActivateRequest{
		RunID: run.ID, Kind: run.Kind, TargetTag: run.TargetTag, TargetImage: preparation.Image, BackupName: backupName,
	}); err != nil {
		_ = clearContainerActivation(m.config.ActivationPath)
		_ = os.Remove(backup)
		fail(err)
		return
	}
	if err := ctx.Err(); err != nil {
		fail(errors.Join(err, cancelPending()))
		return
	}
	for _, deviceID := range online {
		if err := ctx.Err(); err != nil {
			_ = m.rollbackRuntimes(activated, status.CurrentTag)
			fail(errors.Join(err, cancelPending()))
			return
		}
		payload, _ := runtimeprotocol.MarshalPayload(runtimeprotocol.RuntimeUpdateRequest{Tag: run.TargetTag})
		activateCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, activateErr := m.broker.Call(activateCtx, deviceID, runtimeprotocol.MethodUpdateActivate, runtimeprotocol.Resource{}, payload)
		cancel()
		if activateErr != nil {
			_ = m.rollbackRuntimes(activated, status.CurrentTag)
			fail(errors.Join(fmt.Errorf("deployment manager: activate runtime %s: %w", deviceID, activateErr), cancelPending()))
			return
		}
		activated = append(activated, deviceID)
		_ = m.store.SaveRuntimeUpdate(context.Background(), controlstore.RuntimeUpdate{DeviceID: deviceID, TargetTag: run.TargetTag, Status: "activating"})
	}

	// commit 返回后宿主执行器会替换 control 容器；从这里开始由激活记录和宿主 watchdog 接管。
	m.mu.Lock()
	delete(m.operations, run.ID)
	m.mu.Unlock()
	if err := m.config.ContainerHost.Commit(context.Background(), run.ID); err != nil {
		resolved, statusErr := m.config.ContainerHost.Status(context.Background())
		if statusErr != nil || resolved.Pending == nil || resolved.Pending.RunID != run.ID || !resolved.Pending.Committed {
			_ = m.rollbackRuntimes(activated, status.CurrentTag)
			fail(errors.Join(err, statusErr, cancelPending()))
			return
		}
	}
	log("宿主执行器已开始替换 control 容器，等待候选版本健康确认")
	*committed = true
}

func (m *DeploymentManager) stageRuntimes(ctx context.Context, targetTag string) ([]string, error) {
	devices, err := m.broker.Devices(ctx)
	if err != nil {
		return nil, err
	}
	online := make([]string, 0, len(devices))
	for _, device := range devices {
		if device.RevokedAt != nil {
			continue
		}
		if !device.Online {
			if err := m.store.SaveRuntimeUpdate(ctx, controlstore.RuntimeUpdate{DeviceID: device.ID, TargetTag: targetTag, Status: "pending"}); err != nil {
				return nil, err
			}
			continue
		}
		payload, _ := runtimeprotocol.MarshalPayload(runtimeprotocol.RuntimeUpdateRequest{Tag: targetTag})
		if _, err := m.broker.Call(ctx, device.ID, runtimeprotocol.MethodUpdateStage, runtimeprotocol.Resource{}, payload); err != nil {
			return nil, fmt.Errorf("deployment manager: stage runtime %s: %w", device.Name, err)
		}
		online = append(online, device.ID)
		if err := m.store.SaveRuntimeUpdate(ctx, controlstore.RuntimeUpdate{DeviceID: device.ID, TargetTag: targetTag, Status: "staged"}); err != nil {
			return nil, err
		}
	}
	return online, nil
}

func (m *DeploymentManager) ConfirmContainerPending(ctx context.Context) error {
	record, err := ReadContainerActivation(m.config.ActivationPath)
	if err != nil || record == nil {
		return err
	}
	status, err := m.config.ContainerHost.Status(ctx)
	if err != nil {
		return err
	}
	manifest, err := releasecontract.Decode(record.Manifest)
	if err != nil || manifest.Tag != record.TargetTag {
		return errors.New("deployment manager: container activation manifest does not match target")
	}

	if status.Pending != nil && status.Pending.RunID == record.RunID && status.Pending.Committed &&
		status.Pending.TargetTag == record.TargetTag && status.Pending.TargetImage == record.TargetImage {
		if m.config.RunningVersion != record.TargetTag {
			return fmt.Errorf("deployment manager: running control version %q is not pending target %q", m.config.RunningVersion, record.TargetTag)
		}
		activity, healthErr := m.waitForRuntimeActivity(ctx)
		if healthErr != nil {
			return fmt.Errorf("deployment manager: candidate server health check failed: %w", healthErr)
		}
		if activity.Busy() {
			return errors.New("deployment manager: candidate server unexpectedly restored active operations during activation")
		}
		if err := m.confirmContainerRuntimes(ctx, record); err != nil {
			return err
		}
		if err := m.config.ContainerHost.Confirm(ctx, record.RunID); err != nil {
			return err
		}
		status.CurrentTag = record.TargetTag
		status.LastRunID = record.RunID
		status.LastOutcome = "succeeded"
		status.Pending = nil
	}

	switch {
	case status.Pending == nil && status.CurrentTag == record.TargetTag && status.LastRunID == record.RunID && status.LastOutcome == "succeeded":
		if m.config.RunningVersion != record.TargetTag {
			return fmt.Errorf("deployment manager: running control version %q is not confirmed target %q", m.config.RunningVersion, record.TargetTag)
		}
		if err := m.store.PutSetting(ctx, "current_release_tag", record.TargetTag); err != nil {
			return err
		}
		if err := m.store.FinishExecution(ctx, record.RunID, "succeeded", ""); err != nil {
			return err
		}
		if err := clearContainerActivation(m.config.ActivationPath); err != nil {
			return err
		}
		_, _ = m.store.AppendRunLog(ctx, record.RunID, "control", "候选 control、server、Runtime 与宿主部署状态健康检查通过")
		return nil
	case status.Pending == nil && status.CurrentTag == record.PreviousTag && status.LastRunID == record.RunID && status.LastOutcome == "failed":
		if m.config.RunningVersion != record.PreviousTag {
			return fmt.Errorf("deployment manager: running control version %q is not rolled back release %q", m.config.RunningVersion, record.PreviousTag)
		}
		rollbackErr := m.rollbackRuntimes(record.RuntimeDeviceIDs, record.PreviousTag)
		if err := m.store.PutSetting(ctx, "current_release_tag", record.PreviousTag); err != nil {
			return errors.Join(err, rollbackErr)
		}
		if err := m.store.FinishExecution(ctx, record.RunID, "failed", status.LastError); err != nil {
			return errors.Join(err, rollbackErr)
		}
		if err := clearContainerActivation(m.config.ActivationPath); err != nil {
			return errors.Join(err, rollbackErr)
		}
		return rollbackErr
	default:
		return errors.New("deployment manager: container host state cannot be reconciled with pending activation")
	}
}

func (m *DeploymentManager) confirmContainerRuntimes(ctx context.Context, record *ContainerActivationRecord) error {
	legacy := &ActivationRecord{TargetTag: record.TargetTag, PreviousTag: record.PreviousTag, RuntimeDeviceIDs: record.RuntimeDeviceIDs}
	return m.confirmRuntimes(ctx, legacy)
}
