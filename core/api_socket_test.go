package core

import (
	"bufio"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestAPISocketRejectsLiveOwner(t *testing.T) {
	dir := t.TempDir()
	first, err := NewAPIServer(dir)
	if err != nil {
		t.Fatal(err)
	}
	first.Start()
	t.Cleanup(first.Stop)
	before, err := os.Lstat(first.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewAPIServer(dir)
	if err == nil {
		second.Stop()
		t.Fatal("another server replaced the live owner's socket")
	}
	after, err := os.Lstat(first.SocketPath())
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("live socket changed: %v", err)
	}
	conn, err := net.DialTimeout("unix", first.SocketPath(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("GET /sessions HTTP/1.0\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("original server status = %d", response.StatusCode)
	}
}

func TestAPISocketStopPreservesReplacement(t *testing.T) {
	api, err := NewAPIServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	api.Start()
	t.Cleanup(api.Stop)
	if err := os.Remove(api.SocketPath()); err != nil {
		t.Fatal(err)
	}
	replacement, err := net.ListenUnix("unix", &net.UnixAddr{Name: api.SocketPath(), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replacement.Close() }()
	before, err := os.Lstat(api.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	api.Stop()
	after, err := os.Lstat(api.SocketPath())
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("stopping the old owner removed the replacement: %v", err)
	}
}

func TestAPISocketStopBeforeStartAndRepeatedStop(t *testing.T) {
	dir := t.TempDir()
	first, err := NewAPIServer(dir)
	if err != nil {
		t.Fatal(err)
	}
	first.Stop()
	if _, err := os.Lstat(first.SocketPath()); !os.IsNotExist(err) {
		t.Fatalf("stopped socket remains: %v", err)
	}
	if conn, err := net.DialTimeout("unix", first.SocketPath(), time.Second); err == nil {
		_ = conn.Close()
		t.Fatal("Stop before Start left its listener open")
	}
	second, err := NewAPIServer(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Stop()
	first.Stop()
	if _, err := os.Lstat(second.SocketPath()); err != nil {
		t.Fatalf("repeated Stop removed a new owner's socket: %v", err)
	}
}

func TestAPISocketRecoversStaleSocket(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run", "api.sock")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	api, err := NewAPIServer(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer api.Stop()
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
}

func TestAPISocketRejectsOwnerWithoutLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run", "api.sock")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	api, err := NewAPIServer(dir)
	if err == nil {
		api.Stop()
		t.Fatal("replaced a live listener that predates API socket locking")
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("unmanaged live socket changed: %v", err)
	}
}

func TestAPISocketPreservesNonSocket(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run", "api.sock")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	api, err := NewAPIServer(dir)
	if err == nil {
		api.Stop()
		t.Fatal("replaced a non-socket file")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "keep me" {
		t.Fatalf("non-socket file changed: %q, %v", data, err)
	}
}

func TestAPISocketConcurrentOwners(t *testing.T) {
	dir := t.TempDir()
	start := make(chan struct{})
	owners := make(chan *APIServer, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if api, err := NewAPIServer(dir); err == nil {
				owners <- api
			}
		}()
	}
	close(start)
	wg.Wait()
	close(owners)
	count := 0
	for api := range owners {
		count++
		api.Stop()
	}
	if count != 1 {
		t.Fatalf("concurrent owners = %d, want 1", count)
	}
}
