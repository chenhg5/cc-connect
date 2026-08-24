package containerhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/controlstore"
	"github.com/chenhg5/cc-connect/releasecontract"
	"github.com/chenhg5/cc-connect/releaseinstall"
	"github.com/chenhg5/cc-connect/runtimeprotocol"
)

type fakeRunner struct {
	mu        sync.Mutex
	upCalls   int
	stopCalls int
	failUp    map[int]error
}

func (r *fakeRunner) PrepareImage(_ context.Context, tag string) (string, error) {
	return ImageRepository + "@sha256:" + strings.Repeat(tagDigestCharacter(tag), 64), nil
}

func (r *fakeRunner) ComposeUp(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.upCalls++
	return r.failUp[r.upCalls]
}

func (r *fakeRunner) ComposeStop(context.Context) error {
	r.mu.Lock()
	r.stopCalls++
	r.mu.Unlock()
	return nil
}

func tagDigestCharacter(tag string) string {
	if strings.Contains(tag, "2") {
		return "2"
	}
	return "1"
}

func TestServerClientActivationCommitAndConfirm(t *testing.T) {
	server, client, runner, paths := startTestServer(t, 5*time.Second)
	defer closeTestServer(t, server)

	status, err := client.Status(context.Background())
	if err != nil || status.CurrentTag != "v0.1.0" || runner.upCalls != 1 {
		t.Fatalf("initial status = %#v, up=%d, err=%v", status, runner.upCalls, err)
	}
	release, preparation, err := client.Prepare(context.Background(), "v0.2.0")
	if err != nil || release.Manifest.Tag != "v0.2.0" || !strings.Contains(preparation.Image, "@sha256:") {
		t.Fatalf("prepare = %#v %#v, %v", release.Manifest, preparation, err)
	}
	runID := "run-confirm"
	backupName := "control-" + runID + ".db"
	writeTestFile(t, filepath.Join(filepath.Dir(paths.database), "backups", backupName), []byte("old-db"))
	if err := client.Activate(context.Background(), ActivateRequest{RunID: runID, Kind: "update", TargetTag: "v0.2.0", TargetImage: preparation.Image, BackupName: backupName}); err != nil {
		t.Fatal(err)
	}
	if err := client.Commit(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		status, _ := client.Status(context.Background())
		return status.Pending != nil && status.Pending.Committed && runner.upCalls >= 2
	})
	if err := client.Confirm(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	status, err = client.Status(context.Background())
	if err != nil || status.CurrentTag != "v0.2.0" || status.PreviousTag != "v0.1.0" || status.Pending != nil || status.LastOutcome != "succeeded" {
		t.Fatalf("confirmed status = %#v, %v", status, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(paths.database), "backups", backupName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("confirmed backup was not removed: %v", err)
	}
}

func TestServerRejectsUnknownInputAndUnpreparedActivation(t *testing.T) {
	server, _, _, _ := startTestServer(t, time.Second)
	defer closeTestServer(t, server)

	mismatch := httptest.NewRecorder()
	server.routes().ServeHTTP(mismatch, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	if mismatch.Code != http.StatusUpgradeRequired || !strings.Contains(mismatch.Body.String(), "update_required") {
		t.Fatalf("contract mismatch status=%d body=%s", mismatch.Code, mismatch.Body.String())
	}

	request := httptest.NewRequest(http.MethodPost, "/v1/prepare", strings.NewReader(`{"tag":"v0.2.0","cwd":"/tmp"}`))
	request.Header.Set(contractHeader, ContractHash)
	response := httptest.NewRecorder()
	server.routes().ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "unknown field") {
		t.Fatalf("unknown input status=%d body=%s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/v1/activate", strings.NewReader(`{"run_id":"run","kind":"update","target_tag":"v0.2.0","target_image":"ghcr.io/shusfun/cc-connect@sha256:2222222222222222222222222222222222222222222222222222222222222222","backup_name":"control-run.db"}`))
	request.Header.Set(contractHeader, ContractHash)
	response = httptest.NewRecorder()
	server.routes().ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "not prepared") {
		t.Fatalf("unprepared status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestServerComposeFailureRollsBackDatabaseAndImage(t *testing.T) {
	server, client, runner, paths := startTestServer(t, 5*time.Second)
	defer closeTestServer(t, server)
	runner.failUp[2] = errors.New("candidate failed")
	_, preparation, err := client.Prepare(context.Background(), "v0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	runID := "run-rollback"
	backupName := "control-" + runID + ".db"
	writeTestFile(t, filepath.Join(filepath.Dir(paths.database), "backups", backupName), []byte("old-db"))
	writeTestFile(t, paths.database, []byte("new-db"))
	if err := client.Activate(context.Background(), ActivateRequest{RunID: runID, Kind: "update", TargetTag: "v0.2.0", TargetImage: preparation.Image, BackupName: backupName}); err != nil {
		t.Fatal(err)
	}
	if err := client.Commit(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		status, _ := client.Status(context.Background())
		return status.Pending == nil && status.LastRunID == runID
	})
	status, _ := client.Status(context.Background())
	if status.CurrentTag != "v0.1.0" || status.LastOutcome != "failed" || runner.stopCalls != 1 || runner.upCalls != 3 {
		t.Fatalf("rollback status=%#v runner=%#v", status, runner)
	}
	if raw, err := os.ReadFile(paths.database); err != nil || string(raw) != "old-db" {
		t.Fatalf("database restore = %q, %v", raw, err)
	}
	if raw, err := os.ReadFile(paths.environment); err != nil || !strings.Contains(string(raw), "CC_CONNECT_VERSION=v0.1.0") {
		t.Fatalf("environment restore = %q, %v", raw, err)
	}
}

func TestServerDeadlineRollsBackAndCorruptStateFailsClosed(t *testing.T) {
	server, client, _, paths := startTestServer(t, 40*time.Millisecond)
	_, preparation, err := client.Prepare(context.Background(), "v0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	runID := "run-timeout"
	backupName := "control-" + runID + ".db"
	writeTestFile(t, filepath.Join(filepath.Dir(paths.database), "backups", backupName), []byte("restored"))
	if err := client.Activate(context.Background(), ActivateRequest{RunID: runID, Kind: "update", TargetTag: "v0.2.0", TargetImage: preparation.Image, BackupName: backupName}); err != nil {
		t.Fatal(err)
	}
	if err := client.Commit(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		status, _ := client.Status(context.Background())
		return status.Pending == nil && status.LastRunID == runID
	})
	closeTestServer(t, server)
	writeTestFile(t, paths.state, []byte(`{"version":99}`))
	if _, err := NewServer(testServerConfig(t, paths, &fakeRunner{})); err == nil || !strings.Contains(err.Error(), "persisted state is invalid") {
		t.Fatalf("corrupt state error = %v", err)
	}
}

func TestServerRestartMakesUncommittedActivationReconciliable(t *testing.T) {
	server, client, _, paths := startTestServer(t, 5*time.Second)
	_, preparation, err := client.Prepare(context.Background(), "v0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	runID := "run-restart"
	backupName := "control-" + runID + ".db"
	writeTestFile(t, filepath.Join(filepath.Dir(paths.database), "backups", backupName), []byte("old"))
	if err := client.Activate(context.Background(), ActivateRequest{RunID: runID, Kind: "update", TargetTag: "v0.2.0", TargetImage: preparation.Image, BackupName: backupName}); err != nil {
		t.Fatal(err)
	}
	closeTestServer(t, server)

	restartedRunner := &fakeRunner{failUp: make(map[int]error)}
	config := testServerConfig(t, paths, restartedRunner)
	config.ActivationTimeout = 5 * time.Second
	restarted, err := NewServer(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, restarted)
	restartedClient, err := NewClient(paths.socket)
	if err != nil {
		t.Fatal(err)
	}
	status, err := restartedClient.Status(context.Background())
	if err != nil || status.Pending != nil || status.CurrentTag != "v0.1.0" || status.LastRunID != runID || status.LastOutcome != "failed" {
		t.Fatalf("restarted status = %#v, %v", status, err)
	}
}

func TestServerDatabaseRestoreFailureKeepsContainerStoppedAcrossRestart(t *testing.T) {
	server, client, runner, paths := startTestServer(t, 5*time.Second)
	defer closeTestServer(t, server)
	runner.failUp[2] = errors.New("candidate failed")
	_, preparation, err := client.Prepare(context.Background(), "v0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	runID := "run-restore-failure"
	backupName := "control-" + runID + ".db"
	backup := filepath.Join(filepath.Dir(paths.database), "backups", backupName)
	writeTestFile(t, backup, []byte("old"))
	if err := client.Activate(context.Background(), ActivateRequest{RunID: runID, Kind: "update", TargetTag: "v0.2.0", TargetImage: preparation.Image, BackupName: backupName}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(backup); err != nil {
		t.Fatal(err)
	}
	if err := client.Commit(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		status, _ := client.Status(context.Background())
		return status.Pending != nil && status.LastRunID == runID && status.LastOutcome == "failed"
	})
	if runner.upCalls != 2 || runner.stopCalls != 1 {
		t.Fatalf("restore failure restarted a container: up=%d stop=%d", runner.upCalls, runner.stopCalls)
	}
	restarted, err := NewServer(testServerConfig(t, paths, &fakeRunner{failUp: make(map[int]error)}))
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "operator recovery") {
		t.Fatalf("restart after restore failure error = %v", err)
	}
}

type testPaths struct{ socket, state, environment, database string }

func startTestServer(t *testing.T, timeout time.Duration) (*Server, *Client, *fakeRunner, testPaths) {
	t.Helper()
	directory := t.TempDir()
	socketDirectory, err := os.MkdirTemp("/tmp", "cc-host-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDirectory) })
	paths := testPaths{socket: filepath.Join(socketDirectory, "host.sock"), state: filepath.Join(directory, "state.json"), environment: filepath.Join(directory, "deployment.env"), database: filepath.Join(directory, "control", "control.db")}
	runner := &fakeRunner{failUp: make(map[int]error)}
	config := testServerConfig(t, paths, runner)
	config.ActivationTimeout = timeout
	server, err := NewServer(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(paths.socket)
	if err != nil {
		t.Fatal(err)
	}
	return server, client, runner, paths
}

func testServerConfig(t *testing.T, paths testPaths, runner Runner) ServerConfig {
	t.Helper()
	releaseServer := testReleaseServer(t)
	client, err := releaseinstall.New(releaseinstall.Config{HTTPClient: releaseServer.Client(), ReleaseBase: releaseServer.URL, ReleaseAPI: releaseServer.URL + "/latest", Verify: func(context.Context, string, []byte, []byte) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	return ServerConfig{SocketPath: paths.socket, StatePath: paths.state, EnvironmentPath: paths.environment,
		ControlDatabase: paths.database, InitialTag: "v0.1.0", ClientUID: os.Getuid(), ClientGID: -1, ReleaseClient: client, Runner: runner}
}

func testReleaseServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest" {
			_, _ = w.Write([]byte(`{"tag_name":"v0.2.0"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/manifest.bundle") {
			_, _ = w.Write([]byte("bundle"))
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/manifest.json") {
			http.NotFound(w, r)
			return
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		tag := parts[len(parts)-2]
		manifest := releasecontract.Manifest{Version: 1, Repository: releasecontract.Repository, Workflow: releasecontract.Workflow,
			Tag: tag, CommitSHA: strings.Repeat("a", 40), RuntimeContractHash: runtimeprotocol.ContractHash,
			ControlSchema: controlstore.SchemaVersion, WorkspaceChatSchema: 3, GeneratedAt: time.Now().UTC()}
		for _, target := range [][3]string{{"control", "linux", "amd64"}, {"control", "linux", "arm64"}, {"server", "linux", "amd64"}, {"server", "linux", "arm64"}, {"deployhost", "linux", "amd64"}, {"deployhost", "linux", "arm64"}, {"runtime", "darwin", "amd64"}, {"runtime", "darwin", "arm64"}} {
			hash := sha256.Sum256([]byte(strings.Join(target[:], "/")))
			manifest.Artifacts = append(manifest.Artifacts, releasecontract.Artifact{Name: "cc-connect-" + strings.Join(target[:], "-") + ".tar.gz", Component: target[0], OS: target[1], Arch: target[2], SHA256: hex.EncodeToString(hash[:]), Size: 1})
		}
		_ = json.NewEncoder(w).Encode(manifest)
	}))
	t.Cleanup(server.Close)
	return server
}

func closeTestServer(t *testing.T, server *Server) {
	t.Helper()
	if server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func writeTestFile(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not reached")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
