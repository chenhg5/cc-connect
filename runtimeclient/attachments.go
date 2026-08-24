package runtimeclient

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chenhg5/cc-connect/core"
	"github.com/chenhg5/cc-connect/runtimeprotocol"
)

func (h *Handler) SetAttachmentFetcher(fetcher func(context.Context, string) (runtimeprotocol.AttachmentContent, error)) {
	h.mu.Lock()
	h.fetchAttachment = fetcher
	h.mu.Unlock()
}

func (h *Handler) materializeInputs(ctx context.Context, workspace core.Workspace, inputs []core.NativeUserInput) ([]core.NativeUserInput, []string, error) {
	h.mu.Lock()
	fetcher := h.fetchAttachment
	h.mu.Unlock()
	result := append([]core.NativeUserInput(nil), inputs...)
	var directory string
	cleanup := func() {
		if directory != "" {
			_ = os.RemoveAll(directory)
		}
	}
	for index := range result {
		item := &result[index]
		typeName := strings.ToLower(strings.TrimSpace(item.Type))
		if typeName == "text" {
			if item.AttachmentRef != "" {
				cleanup()
				return nil, nil, fmt.Errorf("runtime handler: text input %d contains an attachment reference", index)
			}
			continue
		}
		if typeName != "image" && typeName != "file" && typeName != "audio" {
			cleanup()
			return nil, nil, fmt.Errorf("runtime handler: unsupported input type %q", item.Type)
		}
		if fetcher == nil || strings.TrimSpace(item.AttachmentRef) == "" {
			cleanup()
			return nil, nil, errors.New("runtime handler: verified attachment fetcher and reference are required")
		}
		content, err := fetcher(ctx, item.AttachmentRef)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("runtime handler: fetch attachment %d: %w", index, err)
		}
		if content.WorkspaceRef != workspace.Ref || strings.ToLower(strings.TrimSpace(content.Type)) != typeName || len(content.Data) == 0 {
			cleanup()
			return nil, nil, errors.New("runtime handler: attachment does not belong to the requested workspace or input type")
		}
		if directory == "" {
			directory, err = os.MkdirTemp("", "cc-connect-runtime-attachments-*")
			if err != nil {
				return nil, nil, fmt.Errorf("runtime handler: create private attachment directory: %w", err)
			}
			if err := os.Chmod(directory, 0o700); err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("runtime handler: protect attachment directory: %w", err)
			}
		}
		extension := safeAttachmentExtension(content.FileName)
		file, err := os.CreateTemp(directory, "attachment-*"+extension)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("runtime handler: create private attachment file: %w", err)
		}
		path := file.Name()
		persistErr := file.Chmod(0o600)
		if persistErr == nil {
			_, persistErr = file.Write(content.Data)
		}
		if persistErr == nil {
			persistErr = file.Sync()
		}
		closeErr := file.Close()
		if persistErr == nil {
			persistErr = closeErr
		}
		if persistErr != nil {
			cleanup()
			return nil, nil, fmt.Errorf("runtime handler: persist private attachment: %w", persistErr)
		}
		name := strings.TrimSpace(content.FileName)
		if name == "" {
			name = filepath.Base(path)
		}
		if typeName == "image" {
			*item = core.NativeUserInput{
				Type: "image", LocalPath: path, MimeType: content.MimeType, FileName: name, Detail: item.Detail,
			}
		} else {
			*item = core.NativeUserInput{
				Type: "text", Text: fmt.Sprintf("Verified platform attachment %q is available at %s", name, path),
			}
		}
	}
	if directory == "" {
		return result, nil, nil
	}
	return result, []string{directory}, nil
}

func safeAttachmentExtension(name string) string {
	extension := strings.ToLower(filepath.Ext(filepath.Base(strings.TrimSpace(name))))
	if len(extension) < 2 || len(extension) > 16 {
		return ""
	}
	for _, char := range extension[1:] {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') {
			return ""
		}
	}
	return extension
}

func turnArtifactKey(workspaceRef, threadID, turnID string) string {
	return workspaceRef + "\x00" + threadID + "\x00" + turnID
}

func (h *Handler) bindTurnArtifacts(workspaceRef, threadID, turnID string, directories []string) {
	if len(directories) == 0 {
		return
	}
	if strings.TrimSpace(turnID) == "" {
		removeArtifactDirectories(directories)
		return
	}
	key := turnArtifactKey(workspaceRef, threadID, turnID)
	h.mu.Lock()
	if _, terminal := h.terminalTurns[key]; terminal {
		delete(h.terminalTurns, key)
		h.mu.Unlock()
		removeArtifactDirectories(directories)
		return
	}
	h.turnArtifacts[key] = append(h.turnArtifacts[key], directories...)
	h.mu.Unlock()
}

func (h *Handler) completeTurnArtifacts(workspaceRef, threadID, turnID, method string) {
	if strings.TrimSpace(turnID) == "" || !strings.EqualFold(strings.TrimSpace(method), "turn/completed") {
		return
	}
	key := turnArtifactKey(workspaceRef, threadID, turnID)
	h.mu.Lock()
	directories := h.turnArtifacts[key]
	delete(h.turnArtifacts, key)
	if len(directories) == 0 {
		h.terminalTurns[key] = struct{}{}
	}
	h.mu.Unlock()
	removeArtifactDirectories(directories)
}

func (h *Handler) ReleaseConnectionArtifacts() {
	h.mu.Lock()
	var directories []string
	for key, current := range h.turnArtifacts {
		directories = append(directories, current...)
		delete(h.turnArtifacts, key)
	}
	h.terminalTurns = make(map[string]struct{})
	h.mu.Unlock()
	removeArtifactDirectories(directories)
}

func removeArtifactDirectories(directories []string) {
	for _, directory := range directories {
		if directory != "" {
			_ = os.RemoveAll(directory)
		}
	}
}
