package controlplane

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const activationRecordVersion = 1

type ActivationRecord struct {
	Version           int       `json:"version"`
	RunID             string    `json:"run_id"`
	Kind              string    `json:"kind"`
	TargetTag         string    `json:"target_tag"`
	TargetDirectory   string    `json:"target_directory"`
	PreviousTag       string    `json:"previous_tag"`
	PreviousDirectory string    `json:"previous_directory"`
	DatabasePath      string    `json:"database_path"`
	DatabaseBackup    string    `json:"database_backup"`
	RuntimeDeviceIDs  []string  `json:"runtime_device_ids"`
	SkipNextRecovery  bool      `json:"skip_next_recovery"`
	CreatedAt         time.Time `json:"created_at"`
}

func ReadActivation(path string) (*ActivationRecord, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("control activation: read record: %w", err)
	}
	var record ActivationRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return nil, fmt.Errorf("control activation: decode record: %w", err)
	}
	if record.Version != activationRecordVersion || strings.TrimSpace(record.RunID) == "" || strings.TrimSpace(record.TargetTag) == "" ||
		!filepath.IsAbs(record.TargetDirectory) || !filepath.IsAbs(record.PreviousDirectory) || !filepath.IsAbs(record.DatabasePath) || !filepath.IsAbs(record.DatabaseBackup) {
		return nil, errors.New("control activation: record is incomplete")
	}
	return &record, nil
}

func writeActivation(path string, record ActivationRecord) error {
	record.Version = activationRecordVersion
	record.CreatedAt = time.Now().UTC()
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return replacePrivateFile(path, append(raw, '\n'))
}

func clearActivation(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("control activation: remove record: %w", err)
	}
	return nil
}

// RestoreActivation 由 systemd ExecStopPost 的稳定 helper 调用。它只接受
// 启动参数声明的精确目录和数据库路径，拒绝 activation 文件扩张删除范围。
func RestoreActivation(recordPath, releasesDirectory, currentLink, databasePath string) error {
	record, err := ReadActivation(recordPath)
	if err != nil || record == nil {
		return err
	}
	if record.SkipNextRecovery {
		record.SkipNextRecovery = false
		return writeActivation(recordPath, *record)
	}
	releasesDirectory, err = filepath.Abs(releasesDirectory)
	if err != nil {
		return err
	}
	databasePath, err = filepath.Abs(databasePath)
	if err != nil {
		return err
	}
	previous, err := filepath.Abs(record.PreviousDirectory)
	if err != nil || filepath.Dir(previous) != releasesDirectory || record.DatabasePath != databasePath {
		return errors.New("control activation: recovery target is outside configured paths")
	}
	if info, err := os.Stat(filepath.Join(previous, "cc-connect-control")); err != nil || !info.Mode().IsRegular() {
		return errors.New("control activation: previous control slot is unavailable")
	}
	if info, err := os.Stat(record.DatabaseBackup); err != nil || !info.Mode().IsRegular() {
		return errors.New("control activation: database backup is unavailable")
	}
	if err := atomicSymlink(previous, currentLink); err != nil {
		return err
	}
	raw, err := os.ReadFile(record.DatabaseBackup)
	if err != nil {
		return err
	}
	for _, sidecar := range []string{databasePath + "-wal", databasePath + "-shm"} {
		if err := os.Remove(sidecar); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := replacePrivateFile(databasePath, raw); err != nil {
		return fmt.Errorf("control activation: restore database: %w", err)
	}
	return clearActivation(recordPath)
}

func atomicSymlink(target, link string) error {
	if !filepath.IsAbs(target) || !filepath.IsAbs(link) {
		return errors.New("control activation: absolute symlink paths are required")
	}
	temporary := filepath.Join(filepath.Dir(link), "."+filepath.Base(link)+"-new")
	_ = os.Remove(temporary)
	if err := os.Symlink(target, temporary); err != nil {
		return fmt.Errorf("control activation: create current link: %w", err)
	}
	if err := os.Rename(temporary, link); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("control activation: switch current link: %w", err)
	}
	return nil
}
