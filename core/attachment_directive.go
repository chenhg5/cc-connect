package core

import (
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type outboundAttachmentDirective struct {
	kind string
	path string
}

type outboundAttachmentData struct {
	kind  string
	image ImageAttachment
	file  FileAttachment
}

const (
	attachmentDirectiveFence = "```cc-connect-attachments"
	attachmentFenceClose     = "```"
	attachmentKindImage      = "image"
	attachmentKindFile       = "file"
)

func extractAttachmentDirectives(text string) (string, []outboundAttachmentDirective, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	cleanLines := make([]string, 0, len(lines))
	var attachments []outboundAttachmentDirective
	var block []string
	inBlock := false
	found := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if inBlock {
			if trimmed == attachmentFenceClose {
				dirs, err := parseAttachmentDirectiveBlock(block)
				if err != nil {
					return text, nil, err
				}
				attachments = append(attachments, dirs...)
				block = nil
				inBlock = false
				found = true
				continue
			}
			block = append(block, line)
			continue
		}
		if trimmed == attachmentDirectiveFence {
			inBlock = true
			block = nil
			continue
		}
		cleanLines = append(cleanLines, line)
	}
	if inBlock {
		return text, nil, fmt.Errorf("unterminated cc-connect-attachments block")
	}
	if !found {
		return text, nil, nil
	}
	clean := strings.TrimSpace(strings.Join(cleanLines, "\n"))
	return collapseExtraBlankLines(clean), attachments, nil
}

func collapseExtraBlankLines(text string) string {
	for strings.Contains(text, "\n\n\n") {
		text = strings.ReplaceAll(text, "\n\n\n", "\n\n")
	}
	return text
}

func parseAttachmentDirectiveBlock(lines []string) ([]outboundAttachmentDirective, error) {
	var attachments []outboundAttachmentDirective
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("invalid attachment directive line %q", line)
		}
		kind := strings.ToLower(strings.TrimSpace(key))
		switch kind {
		case attachmentKindImage, attachmentKindFile:
		default:
			return nil, fmt.Errorf("unsupported attachment directive kind %q", kind)
		}
		path := strings.Trim(strings.TrimSpace(value), `"'`)
		if path == "" {
			return nil, fmt.Errorf("empty attachment path for %s", kind)
		}
		attachments = append(attachments, outboundAttachmentDirective{kind: kind, path: path})
	}
	return attachments, nil
}

func buildAttachmentFromDirective(dir outboundAttachmentDirective) (outboundAttachmentData, error) {
	if !filepath.IsAbs(dir.path) {
		return outboundAttachmentData{}, fmt.Errorf("attachment path must be absolute: %s", dir.path)
	}
	info, err := os.Stat(dir.path)
	if err != nil {
		return outboundAttachmentData{}, fmt.Errorf("stat attachment %s: %w", dir.path, err)
	}
	if info.IsDir() {
		return outboundAttachmentData{}, fmt.Errorf("attachment path is a directory: %s", dir.path)
	}
	data, err := os.ReadFile(dir.path)
	if err != nil {
		return outboundAttachmentData{}, fmt.Errorf("read attachment %s: %w", dir.path, err)
	}
	name := filepath.Base(dir.path)
	mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	switch dir.kind {
	case attachmentKindImage:
		if !strings.HasPrefix(mimeType, "image/") {
			return outboundAttachmentData{}, fmt.Errorf("attachment is not an image: %s", dir.path)
		}
		return outboundAttachmentData{
			kind: attachmentKindImage,
			image: ImageAttachment{
				MimeType: mimeType,
				Data:     data,
				FileName: name,
			},
		}, nil
	case attachmentKindFile:
		return outboundAttachmentData{
			kind: attachmentKindFile,
			file: FileAttachment{
				MimeType: mimeType,
				Data:     data,
				FileName: name,
			},
		}, nil
	default:
		return outboundAttachmentData{}, fmt.Errorf("unsupported attachment directive kind %q", dir.kind)
	}
}
