package localapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func TestWindowsPipeEndpointRoundTripAndPrivateState(t *testing.T) {
	dir := t.TempDir()
	listener, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "local only")
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return DialContext(ctx, Path(dir))
		},
	}}
	t.Cleanup(client.CloseIdleConnections)
	response, err := client.Get("http://local-api/")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || string(data) != "local only" {
		t.Fatalf("pipe response = %q, %v", data, err)
	}
	for _, path := range []string{filepath.Dir(listener.path), listener.path, listener.path + ".lock"} {
		assertPrivateWindowsACL(t, path, listener.security.sid)
	}
	if second, err := Listen(dir); err == nil {
		_ = second.Close()
		t.Fatal("a second owner acquired the same endpoint")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(listener.path); !os.IsNotExist(err) {
		t.Fatalf("descriptor survived Close: %v", err)
	}
	second, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	_ = listener.Close()
	if _, err := os.Stat(second.path); err != nil {
		t.Fatalf("repeated old Close removed the new descriptor: %v", err)
	}
}

func assertPrivateWindowsACL(t *testing.T, path string, sid *windows.SID) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.Equals(sid) {
		t.Fatalf("unexpected state owner: %v", err)
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("state inherits a permissive parent ACL: %v", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil || dacl.AceCount != 2 {
		t.Fatalf("unexpected private DACL: %v", err)
	}
	seen := make(map[string]bool)
	for i := uint16(0); i < dacl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(i), &ace); err != nil {
			t.Fatal(err)
		}
		identity := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || (identity != sid.String() && identity != "S-1-5-18") {
			t.Fatalf("unexpected principal or ACE type: %s/%d", identity, ace.Header.AceType)
		}
		seen[identity] = true
	}
	if !seen[sid.String()] || !seen["S-1-5-18"] {
		t.Fatal("private state does not grant its user and SYSTEM")
	}
}

func fixtureDescriptor(t *testing.T) descriptor {
	t.Helper()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	id := hex.EncodeToString(nonce[:])
	return descriptor{SchemaVersion: 1, Transport: "named_pipe", Address: pipePrefix + id, InstanceID: id, PID: uint32(os.Getpid())}
}

func TestWindowsPipeRecoversOnlyProvablyStaleEndpoints(t *testing.T) {
	for _, live := range []bool{false, true} {
		name := "stale"
		if live {
			name = "live without a lock"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			security, err := currentSecurity()
			if err != nil {
				t.Fatal(err)
			}
			path, err := localFilePath(Path(dir))
			if err != nil {
				t.Fatal(err)
			}
			if err := security.ensureRunDirectory(filepath.Dir(path)); err != nil {
				t.Fatal(err)
			}
			previous := fixtureDescriptor(t)
			if live {
				pipe, err := winio.ListenPipe(previous.Address, &winio.PipeConfig{SecurityDescriptor: security.sddl})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = pipe.Close() })
				go func() {
					if conn, err := pipe.Accept(); err == nil {
						_ = conn.Close()
					}
				}()
			}
			if _, err := publishDescriptor(path, previous, security); err != nil {
				t.Fatal(err)
			}
			listener, err := Listen(dir)
			if live {
				if err == nil {
					_ = listener.Close()
					t.Fatal("replaced a live endpoint whose owner predates locking")
				}
				got, readErr := readDescriptor(path, security)
				if readErr != nil || got.InstanceID != previous.InstanceID {
					t.Fatalf("live descriptor changed: %v", readErr)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = listener.Close() })
				if listener.instance == previous.InstanceID {
					t.Fatal("stale descriptor did not receive a fresh generation")
				}
			}
		})
	}
}

func TestWindowsPipeClosePreservesReplacementDescriptor(t *testing.T) {
	listener, err := Listen(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	replacement := fixtureDescriptor(t)
	if _, err := publishDescriptor(listener.path, replacement, listener.security); err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := readDescriptor(listener.path, listener.security)
	if err != nil || got.InstanceID != replacement.InstanceID {
		t.Fatalf("old owner removed replacement: %v", err)
	}
}

func TestWindowsPipeDialHonorsCancellation(t *testing.T) {
	listener, err := Listen(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	// The listener is bound but has no Accept loop, so a client must not wait
	// forever for a busy pipe to become available.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	conn, err := DialContext(ctx, listener.path)
	if conn != nil {
		_ = conn.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("busy pipe dial = %v, want context deadline", err)
	}
}

func TestWindowsPipePreservesForeignDescriptor(t *testing.T) {
	dir := t.TempDir()
	security, err := currentSecurity()
	if err != nil {
		t.Fatal(err)
	}
	path, err := localFilePath(Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := security.ensureRunDirectory(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	file, err := security.openFile(path, windows.GENERIC_WRITE, 0, windows.CREATE_NEW)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString("keep me")
	_ = file.Close()
	if listener, err := Listen(dir); err == nil {
		_ = listener.Close()
		t.Fatal("overwrote a foreign descriptor file")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "keep me" {
		t.Fatalf("foreign file changed: %q, %v", data, err)
	}
}

func TestWindowsPipeConcurrentOwners(t *testing.T) {
	dir := t.TempDir()
	start := make(chan struct{})
	owners := make(chan *Listener, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if owner, err := Listen(dir); err == nil {
				owners <- owner
			}
		}()
	}
	close(start)
	wg.Wait()
	close(owners)
	count := 0
	for owner := range owners {
		count++
		_ = owner.Close()
	}
	if count != 1 {
		t.Fatalf("concurrent owners = %d, want 1", count)
	}
}
