package controlplane

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const containerActivationVersion = 1

type ContainerActivationRecord struct {
	Version          int             `json:"version"`
	RunID            string          `json:"run_id"`
	Kind             string          `json:"kind"`
	TargetTag        string          `json:"target_tag"`
	TargetImage      string          `json:"target_image"`
	PreviousTag      string          `json:"previous_tag"`
	BackupName       string          `json:"backup_name"`
	RuntimeDeviceIDs []string        `json:"runtime_device_ids"`
	Manifest         json.RawMessage `json:"manifest"`
	CreatedAt        time.Time       `json:"created_at"`
}

func ReadContainerActivation(path string) (*ContainerActivationRecord, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("container activation: read record: %w", err)
	}
	var record ContainerActivationRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return nil, fmt.Errorf("container activation: decode record: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("container activation: trailing JSON is not allowed")
	}
	if record.Version != containerActivationVersion || !validContainerRunID(record.RunID) ||
		(record.Kind != "update" && record.Kind != "rollback") || strings.TrimSpace(record.TargetTag) == "" ||
		strings.TrimSpace(record.TargetImage) == "" || strings.TrimSpace(record.PreviousTag) == "" ||
		record.BackupName != "control-"+record.RunID+".db" || len(record.Manifest) == 0 || record.CreatedAt.IsZero() {
		return nil, errors.New("container activation: record is incomplete")
	}
	for _, deviceID := range record.RuntimeDeviceIDs {
		if strings.TrimSpace(deviceID) == "" {
			return nil, errors.New("container activation: runtime device id is empty")
		}
	}
	return &record, nil
}

func validContainerRunID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func writeContainerActivation(path string, record ContainerActivationRecord) error {
	record.Version = containerActivationVersion
	record.CreatedAt = time.Now().UTC()
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return replacePrivateFile(path, append(raw, '\n'))
}

func clearContainerActivation(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("container activation: remove record: %w", err)
	}
	return nil
}
