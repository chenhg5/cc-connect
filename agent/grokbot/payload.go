package grokbot

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

const replyHint = "POST JSON {\"text\",\"images\",\"files\"} to reply.http with header Authorization: Bearer <reply.token> (or X-Grokbot-Token). Alternatively write the same JSON to reply.file. images/files entries may be absolute paths or objects with path or data_base64."

type inboundImage struct {
	Path       string `json:"path"`
	Name       string `json:"name,omitempty"`
	Mime       string `json:"mime,omitempty"`
	DataBase64 string `json:"data_base64,omitempty"`
}

type inboundFile struct {
	Path string `json:"path"`
	Name string `json:"name,omitempty"`
	Mime string `json:"mime,omitempty"`
}

type replyTarget struct {
	HTTP  string `json:"http,omitempty"`
	File  string `json:"file,omitempty"`
	Token string `json:"token,omitempty"`
	Hint  string `json:"hint,omitempty"`
}

type webhookBody struct {
	Source    string         `json:"source"`
	ID        string         `json:"id"`
	SessionID string         `json:"session_id"`
	MessageID string         `json:"message_id,omitempty"`
	Text      string         `json:"text"`
	Images    []inboundImage `json:"images,omitempty"`
	Files     []inboundFile  `json:"files,omitempty"`
	Reply     replyTarget    `json:"reply"`
}

func (a *Agent) buildBody(id, sessionID, messageID, text string, images []inboundImage, files []inboundFile) webhookBody {
	return webhookBody{
		Source:    "cc-connect",
		ID:        id,
		SessionID: sessionID,
		MessageID: messageID,
		Text:      text,
		Images:    images,
		Files:     files,
		Reply:     a.replyTarget(id),
	}
}

func (a *Agent) replyTarget(id string) replyTarget {
	rt := replyTarget{Hint: replyHint}
	a.mu.Lock()
	callback := a.callbackEnabled
	base := a.callbackBase
	auth := a.callbackAuth
	token := a.replyToken
	a.mu.Unlock()
	if callback && base != "" {
		rt.HTTP = base + "/outbox/" + id
		if auth {
			rt.Token = token
		}
	}
	if dir := a.outboxDir(); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			slog.Warn("grokbot: create outbox dir", "error", err)
		} else {
			rt.File = filepath.Join(dir, id+".json")
		}
	}
	return rt
}

func (a *Agent) stageImages(workDir, messageID, prompt string, images []core.ImageAttachment) (string, []inboundImage) {
	if len(images) == 0 {
		return prompt, nil
	}
	asFiles := make([]core.FileAttachment, 0, len(images))
	for i, im := range images {
		name := im.FileName
		if strings.TrimSpace(name) == "" {
			name = fmt.Sprintf("img_%d%s", i, core.ExtFromMime(im.MimeType))
		}
		asFiles = append(asFiles, core.FileAttachment{
			MimeType: im.MimeType,
			Data:     im.Data,
			FileName: name,
		})
	}
	paths := core.SaveFilesToDisk(workDir, messageID, asFiles)
	prompt = core.AppendImageRefs(prompt, paths)
	out := make([]inboundImage, 0, len(paths))
	matched := len(paths) == len(images)
	for i, p := range paths {
		item := inboundImage{Path: p, Name: filepath.Base(p)}
		var raw []byte
		if matched {
			item.Mime = images[i].MimeType
			raw = images[i].Data
		}
		if item.Mime == "" {
			item.Mime = mime.TypeByExtension(filepath.Ext(p))
		}
		if a.embedImages && a.maxEmbedBytes > 0 && len(raw) > 0 && len(raw) <= a.maxEmbedBytes {
			item.DataBase64 = base64.StdEncoding.EncodeToString(raw)
		} else if a.embedImages && len(raw) > a.maxEmbedBytes {
			slog.Warn("grokbot: image exceeds max_embed_bytes; sending path only",
				"name", item.Name, "bytes", len(raw), "max", a.maxEmbedBytes)
		}
		out = append(out, item)
	}
	if !matched {
		slog.Warn("grokbot: not every image was saved; webhook lists the files that landed on disk",
			"attached", len(images), "saved", len(paths))
	}
	return prompt, out
}

func (a *Agent) stageFiles(workDir, messageID, prompt string, files []core.FileAttachment) (string, []inboundFile) {
	if len(files) == 0 {
		return prompt, nil
	}
	paths := core.SaveFilesToDisk(workDir, messageID, files)
	prompt = core.AppendFileRefs(prompt, paths)
	out := make([]inboundFile, 0, len(paths))
	for _, p := range paths {
		out = append(out, inboundFile{
			Path: p,
			Name: filepath.Base(p),
			Mime: mime.TypeByExtension(filepath.Ext(p)),
		})
	}
	return prompt, out
}

func (a *Agent) postWebhook(ctx context.Context, body webhookBody) (string, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("grokbot: encode webhook: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.webhookURL, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("grokbot: webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", a.authHeader)

	resp, err := a.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("grokbot: webhook request failed: %s", a.redact(err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	redacted := truncate(a.redact(strings.TrimSpace(string(respBody))), 300)

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return "", fmt.Errorf("grokbot: webhook unauthorized (HTTP 401); check %s", a.keyEnv)
	case http.StatusBadRequest:
		if redacted == "" {
			redacted = "bad request"
		}
		return "", fmt.Errorf("grokbot: webhook rejected the run (HTTP 400): %s", redacted)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("grokbot: webhook returned HTTP %d: %s", resp.StatusCode, redacted)
	}

	runID, err := parseWebhookAccepted(respBody)
	if err != nil {
		return "", fmt.Errorf("grokbot: %s", a.redact(err.Error()))
	}
	if runID != "" {
		slog.Info("grokbot: run started", "run_uuid", runID)
	}
	return runID, nil
}

func parseWebhookAccepted(body []byte) (string, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return "", nil
	}
	var parsed struct {
		Success *bool  `json:"success"`
		RunUUID string `json:"runUuid"`
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(trimmed, &parsed); err != nil {
		// A 2xx with a non-JSON body still means the run was accepted.
		return "", nil
	}
	if parsed.Success != nil && !*parsed.Success {
		msg := strings.TrimSpace(parsed.Error)
		if msg == "" {
			msg = strings.TrimSpace(parsed.Message)
		}
		if msg == "" {
			msg = "success=false"
		}
		return "", fmt.Errorf("webhook reported failure: %s", msg)
	}
	return parsed.RunUUID, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
