package grokbot

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// replyMsg is one finished Grok Bot reply, or an error the routine reported.
type replyMsg struct {
	text   string
	images []core.ImageAttachment
	files  []core.FileAttachment
	err    error
}

func (a *Agent) listen() error {
	ln, err := net.Listen("tcp", a.callbackAddr)
	if err != nil {
		return fmt.Errorf("grokbot: callback listen %s: %w", a.callbackAddr, err)
	}
	host, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		_ = ln.Close()
		return fmt.Errorf("grokbot: callback address: %w", err)
	}
	if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() {
		slog.Warn("grokbot: callback is not bound to loopback; keep it off the public internet", "addr", ln.Addr().String())
	}
	if host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	a.callbackBase = "http://" + net.JoinHostPort(host, port)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", a.handleHealth)
	mux.HandleFunc("POST /outbox/{id}", a.handleOutbox)
	a.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := a.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("grokbot: callback server", "error", err)
		}
	}()
	return nil
}

func (a *Agent) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *Agent) handleOutbox(w http.ResponseWriter, r *http.Request) {
	if !a.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id := r.PathValue("id")
	if !validID(id) {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	if _, dup := a.done[id]; dup {
		a.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "duplicate": true})
		return
	}
	_, waiting := a.waiters[id]
	a.mu.Unlock()
	if !waiting {
		http.Error(w, "unknown id", http.StatusNotFound)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxReplyBytes))
	if err != nil {
		http.Error(w, "body too large or unreadable", http.StatusRequestEntityTooLarge)
		return
	}
	msg, err := parseReplyBody(body)
	if err != nil {
		slog.Warn("grokbot: rejected reply body", "error", err)
		http.Error(w, "invalid reply", http.StatusBadRequest)
		return
	}
	if !a.deliver(id, msg) {
		http.Error(w, "unknown id", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *Agent) register(id string) chan replyMsg {
	ch := make(chan replyMsg, 1)
	a.mu.Lock()
	a.waiters[id] = ch
	a.mu.Unlock()
	return ch
}

func (a *Agent) unregister(id string) {
	a.mu.Lock()
	delete(a.waiters, id)
	a.mu.Unlock()
}

func (a *Agent) deliver(id string, msg replyMsg) bool {
	a.mu.Lock()
	ch, ok := a.waiters[id]
	if ok {
		delete(a.waiters, id)
		a.done[id] = time.Now()
		a.pruneDoneLocked()
	}
	dir := a.outboxDirLocked()
	a.mu.Unlock()
	if dir != "" {
		_ = os.Remove(filepath.Join(dir, id+".json"))
	}
	if !ok {
		return false
	}
	select {
	case ch <- msg:
	default:
	}
	return true
}

func (a *Agent) pruneDoneLocked() {
	cutoff := time.Now().Add(-30 * time.Minute)
	for id, at := range a.done {
		if at.Before(cutoff) {
			delete(a.done, id)
		}
	}
}

func (a *Agent) waitRegistered(ctx context.Context, id string, ch <-chan replyMsg, timeout time.Duration) (replyMsg, error) {
	if msg, ok := a.consumeOutbox(id); ok {
		return unwrapReply(msg)
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	var poll <-chan time.Time
	if a.outboxEnabled {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		poll = ticker.C
	}
	for {
		select {
		case <-ctx.Done():
			return replyMsg{}, ctx.Err()
		case msg := <-ch:
			return unwrapReply(msg)
		case <-poll:
			if msg, ok := a.consumeOutbox(id); ok {
				return unwrapReply(msg)
			}
		case <-timer.C:
			return replyMsg{}, fmt.Errorf("grokbot: timed out after %s waiting for a reply", timeout)
		}
	}
}

func unwrapReply(msg replyMsg) (replyMsg, error) {
	if msg.err != nil {
		return replyMsg{}, msg.err
	}
	return msg, nil
}

func (a *Agent) consumeOutbox(id string) (replyMsg, bool) {
	dir := a.outboxDir()
	if dir == "" || !validID(id) {
		return replyMsg{}, false
	}
	path := filepath.Join(dir, id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return replyMsg{}, false
	}
	msg, err := parseReplyBody(data)
	if err != nil {
		// The writer may still be flushing a non-atomic write. Keep waiting.
		return replyMsg{}, false
	}
	_ = os.Remove(path)
	a.mu.Lock()
	a.done[id] = time.Now()
	a.pruneDoneLocked()
	a.mu.Unlock()
	return msg, true
}

func parseReplyBody(raw []byte) (replyMsg, error) {
	var wire struct {
		Text    string          `json:"text"`
		Content string          `json:"content"`
		Error   string          `json:"error"`
		Images  json.RawMessage `json:"images"`
		Files   json.RawMessage `json:"files"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return replyMsg{}, fmt.Errorf("grokbot: reply is not JSON: %w", err)
	}
	if msg := strings.TrimSpace(wire.Error); msg != "" {
		return replyMsg{err: fmt.Errorf("grokbot: %s", msg)}, nil
	}
	text := strings.TrimSpace(wire.Text)
	if text == "" {
		text = strings.TrimSpace(wire.Content)
	}
	images, imgErr := decodeImages(wire.Images)
	files, fileErr := decodeFiles(wire.Files)
	if text == "" && len(images) == 0 && len(files) == 0 && (imgErr != nil || fileErr != nil) {
		err := imgErr
		if err == nil {
			err = fileErr
		}
		return replyMsg{err: err}, nil
	}
	if imgErr != nil {
		slog.Warn("grokbot: skipped a reply image", "error", imgErr)
	}
	if fileErr != nil {
		slog.Warn("grokbot: skipped a reply file", "error", fileErr)
	}
	return replyMsg{text: text, images: images, files: files}, nil
}

func decodeImages(raw json.RawMessage) ([]core.ImageAttachment, error) {
	elems, err := rawElems(raw)
	if err != nil || len(elems) == 0 {
		return nil, err
	}
	var out []core.ImageAttachment
	var firstErr error
	for _, el := range elems {
		img, err := decodeOneImage(el)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		out = append(out, img)
	}
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

func decodeFiles(raw json.RawMessage) ([]core.FileAttachment, error) {
	elems, err := rawElems(raw)
	if err != nil || len(elems) == 0 {
		return nil, err
	}
	var out []core.FileAttachment
	var firstErr error
	for _, el := range elems {
		f, err := decodeOneFile(el)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		out = append(out, f)
	}
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

func rawElems(raw json.RawMessage) ([]json.RawMessage, error) {
	if len(bytesTrim(raw)) == 0 || string(bytesTrim(raw)) == "null" {
		return nil, nil
	}
	var elems []json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil {
		return nil, fmt.Errorf("grokbot: attachments must be an array: %w", err)
	}
	return elems, nil
}

func bytesTrim(raw []byte) []byte {
	return []byte(strings.TrimSpace(string(raw)))
}

func decodeOneImage(el json.RawMessage) (core.ImageAttachment, error) {
	if path, ok := rawString(el); ok {
		return imageFromPath(path)
	}
	var obj struct {
		Path       string `json:"path"`
		Name       string `json:"name"`
		FileName   string `json:"file_name"`
		Mime       string `json:"mime"`
		MimeType   string `json:"mime_type"`
		DataBase64 string `json:"data_base64"`
	}
	if err := json.Unmarshal(el, &obj); err != nil {
		return core.ImageAttachment{}, err
	}
	name := safeName(firstNonEmpty(obj.Name, obj.FileName), "")
	mimeType := firstNonEmpty(obj.Mime, obj.MimeType)
	if strings.TrimSpace(obj.DataBase64) != "" {
		data, err := decodeBase64(obj.DataBase64)
		if err != nil {
			return core.ImageAttachment{}, fmt.Errorf("grokbot: bad image base64")
		}
		if int64(len(data)) > maxMediaBytes {
			return core.ImageAttachment{}, fmt.Errorf("grokbot: reply image exceeds size limit")
		}
		if name == "" {
			name = "image" + extFromMime(mimeType)
		}
		if mimeType == "" {
			mimeType = mime.TypeByExtension(filepath.Ext(name))
		}
		if mimeType == "" {
			mimeType = "image/png"
		}
		return core.ImageAttachment{MimeType: mimeType, Data: data, FileName: name}, nil
	}
	if strings.TrimSpace(obj.Path) == "" {
		return core.ImageAttachment{}, fmt.Errorf("grokbot: reply image missing path and data_base64")
	}
	img, err := imageFromPath(obj.Path)
	if err != nil {
		return core.ImageAttachment{}, err
	}
	if name != "" {
		img.FileName = name
	}
	if mimeType != "" {
		img.MimeType = mimeType
	}
	return img, nil
}

func decodeOneFile(el json.RawMessage) (core.FileAttachment, error) {
	if path, ok := rawString(el); ok {
		return fileFromPath(path)
	}
	var obj struct {
		Path       string `json:"path"`
		Name       string `json:"name"`
		FileName   string `json:"file_name"`
		Mime       string `json:"mime"`
		MimeType   string `json:"mime_type"`
		DataBase64 string `json:"data_base64"`
	}
	if err := json.Unmarshal(el, &obj); err != nil {
		return core.FileAttachment{}, err
	}
	name := safeName(firstNonEmpty(obj.Name, obj.FileName), "")
	mimeType := firstNonEmpty(obj.Mime, obj.MimeType)
	if strings.TrimSpace(obj.DataBase64) != "" {
		data, err := decodeBase64(obj.DataBase64)
		if err != nil {
			return core.FileAttachment{}, fmt.Errorf("grokbot: bad file base64")
		}
		if int64(len(data)) > maxMediaBytes {
			return core.FileAttachment{}, fmt.Errorf("grokbot: reply file exceeds size limit")
		}
		if name == "" {
			name = "file"
		}
		if mimeType == "" {
			mimeType = mime.TypeByExtension(filepath.Ext(name))
		}
		return core.FileAttachment{MimeType: mimeType, Data: data, FileName: name}, nil
	}
	if strings.TrimSpace(obj.Path) == "" {
		return core.FileAttachment{}, fmt.Errorf("grokbot: reply file missing path and data_base64")
	}
	f, err := fileFromPath(obj.Path)
	if err != nil {
		return core.FileAttachment{}, err
	}
	if name != "" {
		f.FileName = name
	}
	if mimeType != "" {
		f.MimeType = mimeType
	}
	return f, nil
}

func imageFromPath(p string) (core.ImageAttachment, error) {
	data, name, err := readLocal(p)
	if err != nil {
		return core.ImageAttachment{}, err
	}
	mt := mime.TypeByExtension(filepath.Ext(name))
	if mt == "" {
		mt = "image/png"
	}
	return core.ImageAttachment{MimeType: mt, Data: data, FileName: name}, nil
}

func fileFromPath(p string) (core.FileAttachment, error) {
	data, name, err := readLocal(p)
	if err != nil {
		return core.FileAttachment{}, err
	}
	return core.FileAttachment{
		MimeType: mime.TypeByExtension(filepath.Ext(name)),
		Data:     data,
		FileName: name,
	}, nil
}

func readLocal(p string) ([]byte, string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return nil, "", fmt.Errorf("grokbot: empty attachment path")
	}
	st, err := os.Stat(p)
	if err != nil {
		return nil, "", err
	}
	if st.Size() > maxMediaBytes {
		return nil, "", fmt.Errorf("grokbot: %s exceeds size limit", filepath.Base(p))
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, "", err
	}
	return data, filepath.Base(p), nil
}

func rawString(el json.RawMessage) (string, bool) {
	var s string
	if err := json.Unmarshal(el, &s); err != nil {
		return "", false
	}
	return s, true
}

func decodeBase64(s string) ([]byte, error) {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', ' ', '\t':
			return -1
		default:
			return r
		}
	}, s)
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}

func safeName(name, fallback string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return fallback
	}
	name = filepath.Base(name)
	if name == "." || name == string(filepath.Separator) || name == "" {
		return fallback
	}
	return name
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func extFromMime(mt string) string {
	if mt == "" {
		return ".png"
	}
	return core.ExtFromMime(mt)
}

func validID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "encode", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(data)
}
