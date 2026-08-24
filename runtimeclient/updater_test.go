package runtimeclient

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/releasecontract"
	"github.com/chenhg5/cc-connect/releaseinstall"
)

func TestUpdateManagerStagesSignedRuntimeAndAtomicallyActivates(t *testing.T) {
	archive := runtimeArchive(t)
	digest := sha256.Sum256(archive)
	manifest := releasecontract.Manifest{Version: 1, Repository: releasecontract.Repository, Workflow: releasecontract.Workflow,
		Tag: "v0.1.0", CommitSHA: strings.Repeat("a", 40), RuntimeContractHash: "next", ControlSchema: 4,
		WorkspaceChatSchema: 3, GeneratedAt: time.Now().UTC()}
	for _, target := range [][3]string{{"control", "linux", "amd64"}, {"control", "linux", "arm64"}, {"server", "linux", "amd64"}, {"server", "linux", "arm64"}, {"deployhost", "linux", "amd64"}, {"deployhost", "linux", "arm64"}, {"runtime", "darwin", "amd64"}, {"runtime", "darwin", "arm64"}} {
		manifest.Artifacts = append(manifest.Artifacts, releasecontract.Artifact{Name: "cc-connect-" + strings.Join(target[:], "-") + ".tar.gz",
			Component: target[0], OS: target[1], Arch: target[2], SHA256: hex.EncodeToString(digest[:]), Size: int64(len(archive))})
	}
	manifestRaw, _ := json.Marshal(manifest)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "manifest.json"):
			_, _ = w.Write(manifestRaw)
		case strings.HasSuffix(r.URL.Path, "manifest.bundle"):
			_, _ = w.Write([]byte("bundle"))
		default:
			_, _ = w.Write(archive)
		}
	}))
	defer server.Close()
	releases, err := releaseinstall.New(releaseinstall.Config{HTTPClient: server.Client(), ReleaseBase: server.URL,
		Verify: func(context.Context, string, []byte, []byte) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	installRuntimeTestSlot(t, state, manifest, manifestRaw, "v0.0.9")
	manager, err := NewUpdateManager(UpdateManagerConfig{StateDirectory: state, ReleaseClient: releases})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Stage(context.Background(), "v0.1.0"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Activate(context.Background(), "v0.1.0"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-manager.RestartRequested():
	case <-time.After(2 * time.Second):
		t.Fatal("activation did not request launchd restart")
	}
	candidate, err := NewUpdateManager(UpdateManagerConfig{StateDirectory: state, ReleaseClient: releases, RollbackTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := candidate.Confirm(context.Background(), "v0.1.0"); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(state, "current"))
	if err != nil || filepath.Base(resolved) != "v0.1.0" {
		t.Fatalf("current = %q, %v", resolved, err)
	}
	if runtime.GOOS != "darwin" {
		t.Logf("runtime updater test exercises darwin artifact selection on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
}

func TestUpdateManagerRollsBackRuntimeWhenCandidateIsNotConfirmed(t *testing.T) {
	archive := runtimeArchive(t)
	digest := sha256.Sum256(archive)
	manifest := releasecontract.Manifest{Version: 1, Repository: releasecontract.Repository, Workflow: releasecontract.Workflow,
		Tag: "v0.1.0", CommitSHA: strings.Repeat("a", 40), RuntimeContractHash: "next", ControlSchema: 4,
		WorkspaceChatSchema: 3, GeneratedAt: time.Now().UTC()}
	for _, target := range [][3]string{{"control", "linux", "amd64"}, {"control", "linux", "arm64"}, {"server", "linux", "amd64"}, {"server", "linux", "arm64"}, {"deployhost", "linux", "amd64"}, {"deployhost", "linux", "arm64"}, {"runtime", "darwin", "amd64"}, {"runtime", "darwin", "arm64"}} {
		manifest.Artifacts = append(manifest.Artifacts, releasecontract.Artifact{Name: "cc-connect-" + strings.Join(target[:], "-") + ".tar.gz",
			Component: target[0], OS: target[1], Arch: target[2], SHA256: hex.EncodeToString(digest[:]), Size: int64(len(archive))})
	}
	manifestRaw, _ := json.Marshal(manifest)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "manifest.json"):
			_, _ = w.Write(manifestRaw)
		case strings.HasSuffix(r.URL.Path, "manifest.bundle"):
			_, _ = w.Write([]byte("bundle"))
		default:
			_, _ = w.Write(archive)
		}
	}))
	defer server.Close()
	releases, err := releaseinstall.New(releaseinstall.Config{HTTPClient: server.Client(), ReleaseBase: server.URL,
		Verify: func(context.Context, string, []byte, []byte) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	installRuntimeTestSlot(t, state, manifest, manifestRaw, "v0.0.9")
	manager, err := NewUpdateManager(UpdateManagerConfig{StateDirectory: state, ReleaseClient: releases})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Stage(context.Background(), "v0.1.0"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Activate(context.Background(), "v0.1.0"); err != nil {
		t.Fatal(err)
	}
	candidate, err := NewUpdateManager(UpdateManagerConfig{StateDirectory: state, ReleaseClient: releases, RollbackTimeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-candidate.RestartRequested():
	case <-time.After(2 * time.Second):
		t.Fatal("unconfirmed candidate did not request rollback restart")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(state, "current"))
	if err != nil || filepath.Base(resolved) != "v0.0.9" {
		t.Fatalf("current after rollback = %q, %v", resolved, err)
	}
}

func installRuntimeTestSlot(t *testing.T, state string, manifest releasecontract.Manifest, manifestRaw []byte, tag string) {
	t.Helper()
	slot := filepath.Join(state, "releases", tag)
	if err := os.MkdirAll(slot, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest.Tag = tag
	raw := manifestRaw
	if tag != "v0.1.0" {
		raw, _ = json.Marshal(manifest)
	}
	for name, contents := range map[string][]byte{
		"cc-connect-runtime": []byte("runtime"), "manifest.json": raw, "manifest.bundle": []byte("bundle"),
	} {
		mode := os.FileMode(0o600)
		if name == "cc-connect-runtime" {
			mode = 0o700
		}
		if err := os.WriteFile(filepath.Join(slot, name), contents, mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(slot, filepath.Join(state, "current")); err != nil {
		t.Fatal(err)
	}
}

func runtimeArchive(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(gz)
	raw := []byte("runtime")
	if err := writer.WriteHeader(&tar.Header{Name: "cc-connect-runtime", Mode: 0o755, Size: int64(len(raw)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	_, _ = writer.Write(raw)
	_ = writer.Close()
	_ = gz.Close()
	return buffer.Bytes()
}
