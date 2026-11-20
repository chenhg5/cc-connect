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
	"runtime"
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

func TestWindowsDescriptorRejectsBroadOrNullDACL(t *testing.T) {
	for _, policy := range []string{"broad", "null"} {
		t.Run(policy, func(t *testing.T) {
			listener, err := Listen(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				setStateDACL(t, listener.path, listener.security.sddl)
				_ = listener.Close()
			})
			sddl := ""
			if policy == "broad" {
				sddl = listener.security.sddl + "(A;;GRGW;;;BU)"
			}
			setStateDACL(t, listener.path, sddl)
			if _, err := readDescriptor(listener.path, listener.security); err == nil {
				t.Fatal("accepted an owned descriptor writable or readable by another identity")
			}
		})
	}
}

func setStateDACL(t *testing.T, path, sddl string) {
	t.Helper()
	var sd *windows.SECURITY_DESCRIPTOR
	var dacl *windows.ACL
	var err error
	if sddl != "" {
		sd, err = windows.SecurityDescriptorFromString(sddl)
		if err != nil {
			t.Fatal(err)
		}
		dacl, _, err = sd.DACL()
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	runtime.KeepAlive(sd)
}

func TestWindowsPrivateDACLRejectsUnknownGrant(t *testing.T) {
	security, err := currentSecurity()
	if err != nil {
		t.Fatal(err)
	}
	// Windows normalizes an object ACE without a GUID to a plain allow ACE.
	// A GUID preserves the unsupported kind; zero rights grant no access.
	sd, err := windows.SecurityDescriptorFromString("O:" + security.sid.String() + "D:P(OA;;0x0;11111111-2222-3333-4444-555555555555;;" + security.sid.String() + ")")
	if err != nil {
		t.Fatal(err)
	}
	if !sd.IsValid() {
		t.Fatal("fixture is not a valid Windows security descriptor")
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil || dacl.AceCount != 1 {
		t.Fatalf("fixture DACL = %v, %v", dacl, err)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		t.Fatal(err)
	}
	if ace == nil {
		t.Fatal("fixture has no ACE")
	}
	t.Logf("fixture ACE type=%d mask=%#x", ace.Header.AceType, ace.Mask)
	if ace.Header.AceType != 5 || ace.Mask != 0 { // ACCESS_ALLOWED_OBJECT_ACE_TYPE
		t.Fatal("fixture did not preserve a zero-rights object ACE")
	}
	if err := security.checkPrivateDescriptor(sd); err == nil {
		t.Fatal("accepted an unsupported object-allow ACE")
	}
}

func TestWindowsPipeRejectsForgedDescriptorPID(t *testing.T) {
	listener, err := Listen(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	endpoint, err := readDescriptor(listener.path, listener.security)
	if err != nil {
		t.Fatal(err)
	}
	endpoint.PID ^= 1 // Guaranteed to differ from this process, without opening it.
	if _, err := publishDescriptor(listener.path, endpoint, listener.security); err != nil {
		t.Fatal(err)
	}
	read := make(chan int, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			read <- -1
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		var first [1]byte
		n, _ := conn.Read(first[:])
		read <- n
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := DialContext(ctx, listener.path)
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil || conn != nil {
		t.Fatal("returned an unverified pipe connection to the caller")
	}
	select {
	case n := <-read:
		if n != 0 {
			t.Fatalf("unverified server received %d bytes, want EOF before any request", n)
		}
	case <-ctx.Done():
		t.Fatal("identity rejection did not close the pipe")
	}
}

func TestWindowsPipeRejectsUnexpectedServerSID(t *testing.T) {
	listener, err := Listen(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		if conn, err := listener.Accept(); err == nil {
			defer func() { _ = conn.Close() }()
			_, _ = io.Copy(io.Discard, conn)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := DialContext(ctx, listener.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	foreign := listener.security
	foreign.sid, err = windows.CreateWellKnownSid(windows.WinBuiltinUsersSid)
	if err != nil {
		t.Fatal(err)
	}
	if err := foreign.checkPipeServer(conn, uint32(os.Getpid())); err == nil {
		t.Fatal("accepted a server token SID different from the expected owner")
	}
}
