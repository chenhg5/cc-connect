package localapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// Path names the descriptor a Windows local API client should read.
func Path(dataDir string) string {
	return filepath.Join(dataDir, "run", "api.endpoint.json")
}

func readDescriptor(path string, security privateSecurity) (descriptor, error) {
	absolute, err := localFilePath(path)
	if err != nil {
		return descriptor{}, err
	}
	if err := rejectReparsePath(absolute); err != nil {
		return descriptor{}, err
	}
	file, err := security.openFile(absolute, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.OPEN_EXISTING)
	if err != nil {
		return descriptor{}, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, descriptorLimit+1))
	if err != nil {
		return descriptor{}, err
	}
	return decodeDescriptor(data)
}

// DialContext opens only the current user's locally discovered named pipe.
func DialContext(ctx context.Context, endpointPath string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	security, err := currentSecurity()
	if err != nil {
		return nil, err
	}
	endpoint, err := readDescriptor(endpointPath, security)
	if err != nil {
		return nil, err
	}
	conn, err := winio.DialPipeContext(ctx, endpoint.Address)
	if err != nil {
		return nil, err
	}
	if err := security.checkPipeServer(conn, endpoint.PID); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// Listener owns one named-pipe listener, its descriptor and its state lock.
type Listener struct {
	net.Listener
	path      string
	instance  string
	info      os.FileInfo
	lock      *os.File
	security  privateSecurity
	closeOnce sync.Once
	closeErr  error
}

func (l *Listener) EndpointPath() string { return l.path }

func (l *Listener) removeDescriptor() error {
	info, err := os.Lstat(l.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !os.SameFile(l.info, info) {
		return nil
	}
	endpoint, err := readDescriptor(l.path, l.security)
	if err != nil {
		return fmt.Errorf("preserve unreadable local API descriptor during cleanup: %w", err)
	}
	if endpoint.InstanceID != l.instance {
		return nil
	}
	return os.Remove(l.path)
}

func (l *Listener) Close() error {
	l.closeOnce.Do(func() {
		l.closeErr = errors.Join(l.Listener.Close(), l.removeDescriptor(), l.lock.Close())
	})
	return l.closeErr
}

func publishDescriptor(path string, endpoint descriptor, security privateSecurity) (os.FileInfo, error) {
	staged := path + "." + endpoint.InstanceID + ".tmp"
	file, err := security.openFile(staged, windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ, windows.CREATE_NEW)
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(staged) }()
	data, err := json.Marshal(endpoint)
	if err == nil {
		_, err = file.Write(append(data, '\n'))
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}
	if err := os.Rename(staged, path); err != nil {
		return nil, err
	}
	return os.Lstat(path)
}

// Listen creates a per-user byte-stream pipe and publishes its endpoint only
// after binding. Existing live endpoints, foreign files and failures to prove a
// stale endpoint are preserved, just as with the Unix socket ownership checks.
func Listen(dataDir string) (*Listener, error) {
	path, err := localFilePath(Path(dataDir))
	if err != nil {
		return nil, err
	}
	security, err := currentSecurity()
	if err != nil {
		return nil, err
	}
	if err := security.ensureRunDirectory(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("prepare private local API directory: %w", err)
	}
	lock, err := security.openFile(path+".lock", windows.GENERIC_READ|windows.GENERIC_WRITE|windows.WRITE_DAC,
		windows.FILE_SHARE_READ, windows.OPEN_ALWAYS)
	if err != nil {
		return nil, fmt.Errorf("lock local API endpoint: %w", err)
	}
	owned := false
	defer func() {
		if !owned {
			_ = lock.Close()
		}
	}()
	if err := security.protectHandle(windows.Handle(lock.Fd())); err != nil {
		return nil, fmt.Errorf("protect local API lock: %w", err)
	}
	if _, err := os.Lstat(path); err == nil {
		previous, err := readDescriptor(path, security)
		if err != nil {
			return nil, fmt.Errorf("preserve existing local API descriptor: %w", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		conn, dialErr := winio.DialPipeContext(ctx, previous.Address)
		cancel()
		if dialErr == nil {
			_ = conn.Close()
			return nil, fmt.Errorf("local API endpoint is already listening")
		}
		if !errors.Is(dialErr, os.ErrNotExist) {
			return nil, fmt.Errorf("cannot prove existing local API endpoint is stale: %w", dialErr)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("create local API instance ID: %w", err)
	}
	instance := hex.EncodeToString(nonce[:])
	endpoint := descriptor{SchemaVersion: 1, Transport: "named_pipe", Address: pipePrefix + instance, InstanceID: instance, PID: uint32(os.Getpid())}
	pipe, err := winio.ListenPipe(endpoint.Address, &winio.PipeConfig{SecurityDescriptor: security.sddl})
	if err != nil {
		return nil, fmt.Errorf("listen on private local API pipe: %w", err)
	}
	info, err := publishDescriptor(path, endpoint, security)
	if err != nil {
		_ = pipe.Close()
		return nil, fmt.Errorf("publish local API descriptor: %w", err)
	}
	owned = true
	return &Listener{Listener: pipe, path: path, instance: instance, info: info, lock: lock, security: security}, nil
}
