package main

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRelaySendAsyncUsesAsyncEndpoint(t *testing.T) {
	dataDir, err := os.MkdirTemp("/tmp", "cc-connect-relay-")
	if err != nil {
		t.Fatalf("mkdir temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dataDir) })
	runDir := filepath.Join(dataDir, "run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	sockPath := filepath.Join(runDir, "api.sock")

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	defer func() { _ = ln.Close() }()

	gotPath := make(chan string, 1)
	gotBody := make(chan string, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotPath <- r.URL.Path
		gotBody <- string(body)
		if r.URL.Path != "/relay/send-async" {
			http.Error(w, "wrong endpoint", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"status":"queued","job":"relay-123"}`)
	})}
	go func() {
		_ = srv.Serve(ln)
	}()
	defer func() { _ = srv.Close() }()

	oldStdout := os.Stdout
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}
	os.Stdout = wOut
	defer func() { os.Stdout = oldStdout }()

	outDone := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, rOut)
		outDone <- buf.String()
	}()

	runRelaySend([]string{
		"--data-dir", dataDir,
		"--from", "source",
		"--to", "target",
		"--session-key", "feishu:chat:user",
		"--message", "please work in the background",
		"--async",
	})

	_ = wOut.Close()
	out := <-outDone

	if got := <-gotPath; got != "/relay/send-async" {
		t.Fatalf("path = %q, want /relay/send-async", got)
	}
	if got := <-gotBody; !strings.Contains(got, `"message":"please work in the background"`) {
		t.Fatalf("body = %q, want relay payload", got)
	}
	if out != "queued relay job=relay-123\n" {
		t.Fatalf("stdout = %q, want queued job output", out)
	}
}
