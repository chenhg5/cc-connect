package runtimeidentity

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/runtimeprotocol"
)

type Identity struct {
	ServerURL  string
	DeviceID   string
	PrivateKey ed25519.PrivateKey
}

type metadata struct {
	ServerURL string `json:"server_url"`
	DeviceID  string `json:"device_id"`
}

type Store struct {
	directory string
	keychain  platformKeychain
	mu        sync.Mutex
}

type PendingEvent struct {
	ConnectionGeneration uint64                   `json:"connection_generation"`
	Sequence             uint64                   `json:"sequence"`
	Method               runtimeprotocol.Method   `json:"method"`
	Resource             runtimeprotocol.Resource `json:"resource,omitempty"`
	PayloadSHA256        string                   `json:"payload_sha256"`
	RecordedAt           time.Time                `json:"recorded_at"`
}

type RuntimeState struct {
	ConfirmedGeneration uint64         `json:"confirmed_generation"`
	ConfirmedSequence   uint64         `json:"confirmed_sequence"`
	PendingEvents       []PendingEvent `json:"pending_events"`
}

func New(directory string) (*Store, error) {
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return nil, errors.New("runtime identity: state directory is required")
	}
	keychain, err := newPlatformKeychain(directory)
	if err != nil {
		return nil, err
	}
	return &Store{directory: directory, keychain: keychain}, nil
}

func (s *Store) LoadOrCreateKey() (ed25519.PrivateKey, error) {
	key, err := s.keychain.Load()
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, errKeyNotFound) {
		return nil, err
	}
	_, key, err = ed25519.GenerateKey(nil)
	if err != nil {
		return nil, fmt.Errorf("runtime identity: generate Ed25519 key: %w", err)
	}
	if err := s.keychain.Save(key); err != nil {
		return nil, err
	}
	return key, nil
}

func (s *Store) Load() (Identity, error) {
	raw, err := os.ReadFile(filepath.Join(s.directory, "identity.json"))
	if err != nil {
		return Identity{}, fmt.Errorf("runtime identity: read metadata: %w", err)
	}
	var value metadata
	decoderErr := json.Unmarshal(raw, &value)
	if decoderErr != nil || strings.TrimSpace(value.ServerURL) == "" || strings.TrimSpace(value.DeviceID) == "" {
		return Identity{}, errors.New("runtime identity: metadata is invalid")
	}
	key, err := s.keychain.Load()
	if err != nil {
		return Identity{}, err
	}
	return Identity{ServerURL: value.ServerURL, DeviceID: value.DeviceID, PrivateKey: key}, nil
}

func (s *Store) SaveMetadata(serverURL, deviceID string) error {
	if strings.TrimSpace(serverURL) == "" || strings.TrimSpace(deviceID) == "" {
		return errors.New("runtime identity: server URL and device ID are required")
	}
	if err := os.MkdirAll(s.directory, 0o700); err != nil {
		return fmt.Errorf("runtime identity: create state directory: %w", err)
	}
	raw, err := json.Marshal(metadata{ServerURL: strings.TrimSuffix(serverURL, "/"), DeviceID: deviceID})
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(s.directory, ".identity-*.tmp")
	if err != nil {
		return fmt.Errorf("runtime identity: create metadata temp file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, filepath.Join(s.directory, "identity.json")); err != nil {
		return fmt.Errorf("runtime identity: replace metadata: %w", err)
	}
	return nil
}

func (s *Store) RecordUnconfirmed(generation, sequence uint64, method runtimeprotocol.Method, resource runtimeprotocol.Resource, payload []byte) error {
	if generation == 0 || sequence == 0 {
		return errors.New("runtime identity: valid event generation and sequence are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadRuntimeState()
	if err != nil {
		return err
	}
	if len(state.PendingEvents) >= 4096 {
		return errors.New("runtime identity: unconfirmed event journal is full")
	}
	digest := sha256.Sum256(payload)
	state.PendingEvents = append(state.PendingEvents, PendingEvent{
		ConnectionGeneration: generation, Sequence: sequence, Method: method, Resource: resource,
		PayloadSHA256: fmt.Sprintf("%x", digest[:]), RecordedAt: time.Now().UTC(),
	})
	return s.saveRuntimeState(state)
}

func (s *Store) Confirm(generation, sequence uint64) error {
	if generation == 0 || sequence == 0 {
		return errors.New("runtime identity: valid acknowledgement is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadRuntimeState()
	if err != nil {
		return err
	}
	if generation < state.ConfirmedGeneration || (generation == state.ConfirmedGeneration && sequence <= state.ConfirmedSequence) {
		return nil
	}
	state.ConfirmedGeneration = generation
	state.ConfirmedSequence = sequence
	pending := state.PendingEvents[:0]
	for _, event := range state.PendingEvents {
		if event.ConnectionGeneration == generation && event.Sequence <= sequence {
			continue
		}
		pending = append(pending, event)
	}
	state.PendingEvents = pending
	return s.saveRuntimeState(state)
}

func (s *Store) State() (RuntimeState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadRuntimeState()
}

func (s *Store) loadRuntimeState() (RuntimeState, error) {
	raw, err := os.ReadFile(filepath.Join(s.directory, "runtime-state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return RuntimeState{}, nil
	}
	if err != nil {
		return RuntimeState{}, fmt.Errorf("runtime identity: read runtime state: %w", err)
	}
	var state RuntimeState
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return RuntimeState{}, fmt.Errorf("runtime identity: decode runtime state: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return RuntimeState{}, errors.New("runtime identity: runtime state contains trailing data")
	}
	return state, nil
}

func (s *Store) saveRuntimeState(state RuntimeState) error {
	if err := os.MkdirAll(s.directory, 0o700); err != nil {
		return fmt.Errorf("runtime identity: create state directory: %w", err)
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("runtime identity: encode runtime state: %w", err)
	}
	return writePrivateFileAtomically(s.directory, "runtime-state.json", raw)
}

func writePrivateFileAtomically(directory, name string, raw []byte) error {
	temporary, err := os.CreateTemp(directory, ".runtime-state-*.tmp")
	if err != nil {
		return fmt.Errorf("runtime identity: create state temp file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, filepath.Join(directory, name)); err != nil {
		return fmt.Errorf("runtime identity: replace state: %w", err)
	}
	return nil
}

var errKeyNotFound = errors.New("runtime identity: key not found")

type platformKeychain interface {
	Load() (ed25519.PrivateKey, error)
	Save(ed25519.PrivateKey) error
}
