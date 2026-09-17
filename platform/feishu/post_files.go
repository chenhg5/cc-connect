package feishu

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"

	"github.com/chenhg5/cc-connect/core"
)

type postFile struct {
	FileKey  string `json:"file_key"`
	FileName string `json:"file_name"`
	IsFolder bool   `json:"is_folder"`
}

// decodePostBody uses the same locale selection for text, images and files.
// A files-only post need not contain a content array.
func decodePostBody(raw string) *postLang {
	var flat postLang
	if json.Unmarshal([]byte(raw), &flat) == nil && (flat.Content != nil || flat.Files != nil || flat.Title != "") {
		return &flat
	}
	var locales map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &locales) != nil {
		return nil
	}
	keys := make([]string, 0, len(locales))
	for key := range locales {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		var post postLang
		if json.Unmarshal(locales[key], &post) == nil && (post.Content != nil || post.Files != nil || post.Title != "") {
			return &post
		}
	}
	return nil
}

func postFiles(raw string) []postFile {
	post := decodePostBody(raw)
	if post == nil {
		return nil
	}
	var files []postFile
	seen := make(map[string]bool)
	for _, file := range post.Files {
		if file.FileKey != "" {
			if seen[file.FileKey] {
				continue
			}
			seen[file.FileKey] = true
		}
		files = append(files, file)
	}
	return files
}

// Failures remain visible to the agent instead of turning into "no attachment".
// Successful siblings still reach the normal core file-saving pipeline.
func (p *Platform) downloadPostFiles(messageID, raw, text string) ([]core.FileAttachment, []string) {
	var files []core.FileAttachment
	var notices []string
	i18n := core.NewI18n(core.DetectLanguage(text))
	for _, file := range postFiles(raw) {
		name := file.FileName
		if name == "" {
			name = "attachment"
		}
		if file.FileKey == "" || file.IsFolder {
			notices = append(notices, i18n.Tf(core.MsgAttachmentUnavailable, name))
			slog.Warn(p.tag()+": invalid post file metadata", "message_id", messageID, "is_folder", file.IsFolder)
			continue
		}
		data, err := p.downloadResource(messageID, file.FileKey, "file")
		if err != nil {
			slog.Error(p.tag()+": download post file failed", "message_id", messageID, "error", err)
			notices = append(notices, i18n.Tf(core.MsgAttachmentUnavailable, name))
			continue
		}
		files = append(files, core.FileAttachment{FileName: name, Data: data, MimeType: http.DetectContentType(data)})
	}
	return files, notices
}
