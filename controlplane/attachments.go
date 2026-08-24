package controlplane

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/runtimeprotocol"
)

const (
	attachmentTTL     = 10 * time.Minute
	maxAttachmentSize = 50 << 20
)

type stagedAttachment struct {
	ref               string
	deviceID          string
	localWorkspaceRef string
	typeName          string
	mimeType          string
	fileName          string
	path              string
	expiresAt         time.Time
}

// AttachmentStore 是 Linux 暂存附件的唯一生命周期所有者。引用只能读取一次，
// 并在过期、设备断线或 control 关闭时删除对应文件。
type AttachmentStore struct {
	directory string
	now       func() time.Time

	mu      sync.Mutex
	entries map[string]stagedAttachment
	closed  bool
	stop    chan struct{}
	done    chan struct{}
}

func NewAttachmentStore(directory string) (*AttachmentStore, error) {
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return nil, errors.New("control attachments: directory is required")
	}
	if err := os.RemoveAll(directory); err != nil {
		return nil, fmt.Errorf("control attachments: clear stale directory: %w", err)
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("control attachments: create directory: %w", err)
	}
	store := &AttachmentStore{
		directory: directory, now: time.Now, entries: make(map[string]stagedAttachment),
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	go store.expiryLoop()
	return store, nil
}

func (s *AttachmentStore) Stage(deviceID, localWorkspaceRef string, uploads []runtimeprotocol.AttachmentUpload) ([]runtimeprotocol.AttachmentReference, error) {
	deviceID = strings.TrimSpace(deviceID)
	localWorkspaceRef = strings.TrimSpace(localWorkspaceRef)
	if deviceID == "" || localWorkspaceRef == "" || len(uploads) == 0 {
		return nil, errors.New("control attachments: device, workspace and attachments are required")
	}
	staged := make([]stagedAttachment, 0, len(uploads))
	rollback := func() {
		for _, entry := range staged {
			_ = os.Remove(entry.path)
		}
	}
	for _, upload := range uploads {
		typeName := strings.ToLower(strings.TrimSpace(upload.Type))
		if typeName != "image" && typeName != "file" && typeName != "audio" {
			rollback()
			return nil, fmt.Errorf("control attachments: unsupported type %q", upload.Type)
		}
		if len(upload.Data) == 0 || len(upload.Data) > maxAttachmentSize {
			rollback()
			return nil, errors.New("control attachments: attachment size must be between 1 byte and 50 MiB")
		}
		refToken, err := secureToken(24)
		if err != nil {
			rollback()
			return nil, err
		}
		file, err := os.CreateTemp(s.directory, "payload-*")
		if err != nil {
			rollback()
			return nil, fmt.Errorf("control attachments: create private file: %w", err)
		}
		path := file.Name()
		persistErr := file.Chmod(0o600)
		if persistErr == nil {
			_, persistErr = file.Write(upload.Data)
		}
		if persistErr == nil {
			persistErr = file.Sync()
		}
		closeErr := file.Close()
		if persistErr == nil {
			persistErr = closeErr
		}
		if persistErr != nil {
			_ = os.Remove(path)
			rollback()
			return nil, fmt.Errorf("control attachments: persist private file: %w", persistErr)
		}
		staged = append(staged, stagedAttachment{
			ref: "att_" + refToken, deviceID: deviceID, localWorkspaceRef: localWorkspaceRef,
			typeName: typeName, mimeType: strings.TrimSpace(upload.MimeType), fileName: filepath.Base(strings.TrimSpace(upload.FileName)),
			path: path, expiresAt: s.now().Add(attachmentTTL),
		})
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		rollback()
		return nil, errors.New("control attachments: store is closed")
	}
	for _, entry := range staged {
		s.entries[entry.ref] = entry
	}
	s.mu.Unlock()

	result := make([]runtimeprotocol.AttachmentReference, 0, len(staged))
	for _, entry := range staged {
		result = append(result, runtimeprotocol.AttachmentReference{
			Ref: entry.ref, Type: entry.typeName, MimeType: entry.mimeType, FileName: entry.fileName,
		})
	}
	return result, nil
}

func (s *AttachmentStore) Take(deviceID, ref string) (runtimeprotocol.AttachmentContent, error) {
	now := s.now()
	s.mu.Lock()
	entry, ok := s.entries[ref]
	if ok && (entry.deviceID != deviceID || !entry.expiresAt.After(now)) {
		ok = false
	}
	if ok {
		delete(s.entries, ref)
	}
	s.mu.Unlock()
	if !ok {
		return runtimeprotocol.AttachmentContent{}, errors.New("control attachments: reference is invalid, expired, or belongs to another device")
	}
	defer func() { _ = os.Remove(entry.path) }()
	data, err := os.ReadFile(entry.path)
	if err != nil {
		return runtimeprotocol.AttachmentContent{}, fmt.Errorf("control attachments: read private file: %w", err)
	}
	return runtimeprotocol.AttachmentContent{
		WorkspaceRef: entry.localWorkspaceRef, Type: entry.typeName, MimeType: entry.mimeType,
		FileName: entry.fileName, Data: data,
	}, nil
}

func (s *AttachmentStore) CleanupDevice(deviceID string) {
	s.cleanup(func(entry stagedAttachment) bool { return entry.deviceID == deviceID })
}

func (s *AttachmentStore) cleanupExpired() {
	now := s.now()
	s.cleanup(func(entry stagedAttachment) bool { return !entry.expiresAt.After(now) })
}

func (s *AttachmentStore) cleanup(matches func(stagedAttachment) bool) {
	s.mu.Lock()
	var paths []string
	for ref, entry := range s.entries {
		if matches(entry) {
			paths = append(paths, entry.path)
			delete(s.entries, ref)
		}
	}
	s.mu.Unlock()
	for _, path := range paths {
		_ = os.Remove(path)
	}
}

func (s *AttachmentStore) expiryLoop() {
	defer close(s.done)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.cleanupExpired()
		case <-s.stop:
			return
		}
	}
}

func (s *AttachmentStore) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.stop)
	s.mu.Unlock()
	<-s.done
	s.cleanup(func(stagedAttachment) bool { return true })
	return os.RemoveAll(s.directory)
}
