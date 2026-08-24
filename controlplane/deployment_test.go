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
	store, err := controlstore.Open(filepath.Join(t.TempDir(), "control.db"), "setup")
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
		"control": deploymentArchive(t, "cc-connect-control", []byte("new-control")),
		"server":  deploymentArchive(t, "cc-connect-server", []byte("new-server")),
		"runtime": deploymentArchive(t, "cc-connect-runtime", []byte("new-runtime")),
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

func deploymentManifest(t *testing.T, tag string, archives map[string][]byte) releasecontract.Manifest {
	t.Helper()
	manifest := releasecontract.Manifest{Version: 1, Repository: releasecontract.Repository, Workflow: releasecontract.Workflow,
		Tag: tag, CommitSHA: strings.Repeat("a", 40), RuntimeContractHash: runtimeprotocol.ContractHash,
		ControlSchema: controlstore.SchemaVersion, WorkspaceChatSchema: 3, GeneratedAt: time.Now().UTC()}
	for _, target := range [][3]string{{"control", "linux", "amd64"}, {"control", "linux", "arm64"}, {"server", "linux", "amd64"}, {"server", "linux", "arm64"}, {"runtime", "darwin", "amd64"}, {"runtime", "darwin", "arm64"}} {
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
