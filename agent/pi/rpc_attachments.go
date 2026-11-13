package pi

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// prepareModernRPCPrompt completes all local preparation before touching the
// RPC transport. Each input owns a fresh directory, including repeated or empty
// platform IDs, so an active run can keep reading earlier attachments.
// strictImages certifies whole-input rejection for steering; ordinary sends
// skip images that cannot be represented by the native RPC payload.
func (s *piSession) prepareModernRPCPrompt(text string, images []core.ImageAttachment, files []core.FileAttachment, strictImages bool) (map[string]any, error) {
	cmd := map[string]any{"type": "prompt", "message": text, "streamingBehavior": "steer"}
	if len(images) > 0 {
		native := make([]map[string]string, 0, len(images))
		for i, image := range images {
			if len(image.Data) == 0 || !strings.HasPrefix(image.MimeType, "image/") {
				if strictImages {
					return nil, &core.SteerNotAcceptedError{Err: fmt.Errorf("pi: image attachment requires data and image MIME type")}
				}
				slog.Warn("pi: skipping image attachment without data or image MIME type", "image_index", i+1, "mime_type", image.MimeType, "data_size", len(image.Data))
				continue
			}
			native = append(native, map[string]string{"type": "image", "data": base64.StdEncoding.EncodeToString(image.Data), "mimeType": image.MimeType})
		}
		if len(native) > 0 {
			cmd["images"] = native
		}
	}
	if len(files) == 0 {
		return cmd, nil
	}
	root, err := filepath.Abs(s.attachDir)
	if err == nil {
		err = os.MkdirAll(root, 0700)
	}
	if err != nil {
		return nil, &core.SteerNotAcceptedError{Err: fmt.Errorf("pi: prepare attachments directory: %w", err)}
	}
	dir, err := os.MkdirTemp(root, "input-")
	if err != nil {
		return nil, &core.SteerNotAcceptedError{Err: fmt.Errorf("pi: create input directory: %w", err)}
	}
	// Remove only this never-sent input on failure, never another live input.
	prepared := false
	defer func() {
		if !prepared {
			cleanAttachments(dir)
		}
	}()
	paths := make([]string, 0, len(files))
	for i, file := range files {
		// Per-file directories preserve original basenames even for duplicate names.
		subdir := filepath.Join(dir, fmt.Sprintf("%d", i))
		if err = os.Mkdir(subdir, 0700); err != nil {
			return nil, &core.SteerNotAcceptedError{Err: fmt.Errorf("pi: prepare file directory: %w", err)}
		}
		name := sanitizePiAttachmentName(file.FileName)
		if name == "" {
			name = "attachment"
		}
		path := filepath.Join(subdir, name)
		if err = os.WriteFile(path, file.Data, 0600); err != nil {
			return nil, &core.SteerNotAcceptedError{Err: fmt.Errorf("pi: save file attachment: %w", err)}
		}
		paths = append(paths, path)
	}
	cmd["message"] = promptWithFileRefs(text, paths)
	prepared = true
	return cmd, nil
}
