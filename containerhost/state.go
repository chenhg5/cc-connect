package containerhost

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const stateVersion = 1

type persistedState struct {
	Version int `json:"version"`
	Status
}

func readState(path string) (persistedState, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return persistedState{}, nil
	}
	if err != nil {
		return persistedState{}, fmt.Errorf("container host: read state: %w", err)
	}
	var state persistedState
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return persistedState{}, fmt.Errorf("container host: decode state: %w", err)
	}
	if state.Version != stateVersion || !validTag(state.CurrentTag) || !validDigestImage(state.CurrentImage) {
		return persistedState{}, errors.New("container host: persisted state is invalid")
	}
	if (state.PreviousTag == "") != (state.PreviousImage == "") ||
		(state.PreviousTag != "" && (!validTag(state.PreviousTag) || !validDigestImage(state.PreviousImage))) {
		return persistedState{}, errors.New("container host: persisted previous release is invalid")
	}
	if state.Pending != nil {
		if err := validatePending(*state.Pending); err != nil {
			return persistedState{}, err
		}
	}
	return state, nil
}

func writeState(path string, state persistedState) error {
	state.Version = stateVersion
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return replaceFile(path, append(raw, '\n'), 0o600)
}

func writeEnvironment(path, tag, image string) error {
	if !validTag(tag) || !validDigestImage(image) {
		return errors.New("container host: refusing invalid deployment environment")
	}
	raw := []byte("CC_CONNECT_IMAGE=" + image + "\nCC_CONNECT_VERSION=" + tag + "\n")
	return replaceFile(path, raw, 0o600)
}

func replaceFile(path string, raw []byte, mode os.FileMode) error {
	if !filepath.IsAbs(path) {
		return errors.New("container host: absolute state path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
		return err
	}
	remove := true
	defer func() {
		_ = temporary.Close()
		if remove {
			_ = os.Remove(temporary.Name())
		}
	}()
	if _, err := temporary.Write(raw); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		return err
	}
	remove = false
	return nil
}

func validatePending(pending PendingOperation) error {
	if !validRunID(pending.RunID) || (pending.Kind != "update" && pending.Kind != "rollback") ||
		!validTag(pending.TargetTag) || !validDigestImage(pending.TargetImage) ||
		!validTag(pending.PreviousTag) || !validDigestImage(pending.PreviousImage) ||
		pending.BackupName != "control-"+pending.RunID+".db" || pending.Deadline.IsZero() {
		return errors.New("container host: pending operation is invalid")
	}
	if (pending.PriorPreviousTag == "") != (pending.PriorPreviousImage == "") ||
		(pending.PriorPreviousTag != "" && (!validTag(pending.PriorPreviousTag) || !validDigestImage(pending.PriorPreviousImage))) {
		return errors.New("container host: pending prior release is invalid")
	}
	return nil
}

func validRunID(value string) bool {
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
