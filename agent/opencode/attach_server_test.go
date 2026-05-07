package opencode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestAgentSessionEnvSkipsAttachServerRestartWhenUnchanged(t *testing.T) {
	a := &Agent{sessionEnv: []string{"CC_PROJECT=p1", "CC_SESSION_KEY=s1"}}
	server, stopped := newAttachServerStopProbe()
	a.server = server

	a.SetSessionEnv([]string{"CC_PROJECT=p1", "CC_SESSION_KEY=s1"})
	if stopped.Load() != 0 {
		t.Fatal("SetSessionEnv stopped attach server for unchanged env")
	}
	if a.server != server {
		t.Fatal("SetSessionEnv replaced attach server for unchanged env")
	}

	a.SetSessionEnv([]string{"CC_PROJECT=p1", "CC_SESSION_KEY=s2"})
	if stopped.Load() != 1 {
		t.Fatalf("SetSessionEnv changed env stopped server %d times, want 1", stopped.Load())
	}
	if a.server != nil {
		t.Fatal("SetSessionEnv changed env did not clear attach server")
	}
}

func TestAgentProvidersSkipAttachServerRestartWhenUnchanged(t *testing.T) {
	providers := []core.ProviderConfig{{
		Name:   "anthropic",
		APIKey: "secret",
		Model:  "anthropic/model",
		Env:    map[string]string{"CUSTOM_ENV": "1"},
	}}
	a := &Agent{providers: providers, activeIdx: 0}
	server, stopped := newAttachServerStopProbe()
	a.server = server

	a.SetProviders([]core.ProviderConfig{{
		Name:   "anthropic",
		APIKey: "secret",
		Model:  "anthropic/model",
		Env:    map[string]string{"CUSTOM_ENV": "1"},
	}})
	if stopped.Load() != 0 {
		t.Fatal("SetProviders stopped attach server for unchanged providers")
	}
	if a.server != server {
		t.Fatal("SetProviders replaced attach server for unchanged providers")
	}

	a.SetProviders([]core.ProviderConfig{{
		Name:   "anthropic",
		APIKey: "secret",
		Model:  "anthropic/other-model",
		Env:    map[string]string{"CUSTOM_ENV": "1"},
	}})
	if stopped.Load() != 1 {
		t.Fatalf("SetProviders changed providers stopped server %d times, want 1", stopped.Load())
	}
	if a.server != nil {
		t.Fatal("SetProviders changed providers did not clear attach server")
	}
}

func TestAgentActiveProviderSkipsAttachServerRestartWhenUnchanged(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{Name: "provider-a", Model: "a/model"},
			{Name: "provider-b", Model: "b/model"},
		},
		activeIdx: 0,
	}
	server, stopped := newAttachServerStopProbe()
	a.server = server

	if !a.SetActiveProvider("provider-a") {
		t.Fatal("SetActiveProvider(provider-a) = false, want true")
	}
	if stopped.Load() != 0 {
		t.Fatal("SetActiveProvider stopped attach server for unchanged provider")
	}
	if a.server != server {
		t.Fatal("SetActiveProvider replaced attach server for unchanged provider")
	}

	if !a.SetActiveProvider("provider-b") {
		t.Fatal("SetActiveProvider(provider-b) = false, want true")
	}
	if stopped.Load() != 1 {
		t.Fatalf("SetActiveProvider changed provider stopped server %d times, want 1", stopped.Load())
	}
	if a.server != nil {
		t.Fatal("SetActiveProvider changed provider did not clear attach server")
	}
}

func TestEnsureAttachServerReusesRunningServer(t *testing.T) {
	fake := buildFakeOpencodeServer(t)
	a := &Agent{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer a.stopAttachServer()

	url1, err := a.ensureAttachServer(ctx, fake, t.TempDir(), nil, 0)
	if err != nil {
		t.Fatalf("ensureAttachServer first start: %v", err)
	}
	if !strings.HasPrefix(url1, "http://127.0.0.1:") {
		t.Fatalf("ensureAttachServer url = %q", url1)
	}
	server := a.server

	url2, err := a.ensureAttachServer(ctx, fake, t.TempDir(), nil, 0)
	if err != nil {
		t.Fatalf("ensureAttachServer reuse: %v", err)
	}
	if url2 != url1 {
		t.Fatalf("ensureAttachServer reused url = %q, want %q", url2, url1)
	}
	if a.server != server {
		t.Fatal("ensureAttachServer did not reuse running server")
	}
}

func TestStopAttachServerIsIdempotent(t *testing.T) {
	fake := buildFakeOpencodeServer(t)
	a := &Agent{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := a.ensureAttachServer(ctx, fake, t.TempDir(), nil, 0); err != nil {
		t.Fatalf("ensureAttachServer: %v", err)
	}

	a.stopAttachServer()
	if a.server != nil {
		t.Fatal("stopAttachServer did not clear server")
	}
	a.stopAttachServer()
}

func TestStartOpencodeServerReportsStartupExit(t *testing.T) {
	fake := buildFakeOpencodeServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := startOpencodeServer(ctx, fake, t.TempDir(), []string{"FAKE_OPENCODE_MODE=exit"}, 0)
	if err == nil {
		t.Fatal("startOpencodeServer succeeded, want startup error")
	}
	if !strings.Contains(err.Error(), "headless server exited during startup") {
		t.Fatalf("startOpencodeServer error = %v", err)
	}
}

func newAttachServerStopProbe() (*opencodeServer, *atomic.Int32) {
	done := make(chan struct{})
	var stopped atomic.Int32
	server := &opencodeServer{
		url:  "http://127.0.0.1:4097",
		done: done,
		cancel: func() {
			if stopped.CompareAndSwap(0, 1) {
				close(done)
			}
		},
	}
	return server, &stopped
}

func buildFakeOpencodeServer(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	src := filepath.Join(dir, "fake-opencode.go")
	exe := filepath.Join(dir, "fake-opencode")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}

	const program = `package main

import (
	"fmt"
	"net"
	"os"
	"time"
)

func main() {
	if os.Getenv("FAKE_OPENCODE_MODE") == "exit" {
		os.Exit(7)
	}
	args := os.Args[1:]
	if len(args) == 0 || args[0] != "serve" {
		os.Exit(2)
	}
	host := "127.0.0.1"
	port := "0"
	for i := 0; i < len(args)-1; i++ {
		switch args[i] {
		case "--hostname":
			host = args[i+1]
		case "--port":
			port = args[i+1]
		}
	}
	listener, err := net.Listen("tcp", host+":"+port)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	defer listener.Close()
	addr := listener.Addr().String()
	_, actualPort, err := net.SplitHostPort(addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(4)
	}
	fmt.Println("opencode server listening on http://" + host + ":" + actualPort)
	for {
		time.Sleep(time.Second)
	}
}
`
	if err := os.WriteFile(src, []byte(program), 0o644); err != nil {
		t.Fatalf("write fake opencode source: %v", err)
	}
	cmd := exec.Command("go", "build", "-o", exe, src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fake opencode: %v\n%s", err, out)
	}
	return exe
}
