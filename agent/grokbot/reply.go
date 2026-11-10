package grokbot

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
		if !a.callbackAuth && !a.callbackInsecureOK {
			_ = ln.Close()
			return fmt.Errorf("grokbot: callback_auth=false cannot listen on %s; use a loopback callback_addr or set callback_insecure_ok = true", ln.Addr().String())
		}
		slog.Warn("grokbot: callback is not bound to loopback; keep it off the public internet", "addr", ln.Addr().String())
	}
	adv := host
	if a.callbackAdvertiseHost != "" {
		adv = a.callbackAdvertiseHost
	} else if isWildcardBindHost(host) {
		_ = ln.Close()
		return fmt.Errorf("grokbot: callback_addr %s listens on every interface; set callback_advertise_host to the host or IP Grok Bot can reach", ln.Addr().String())
	}
	a.callbackBase = "http://" + net.JoinHostPort(adv, port)

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
	// Validate the body before checking whether the turn is still waiting.
	// A reply that arrives as the timeout fires must not be reported as an
	// unknown id, and an invalid body is a 400 even if the turn already ended.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxReplyBytes))
	if err != nil {
		http.Error(w, "body too large or unreadable", http.StatusRequestEntityTooLarge)
		return
	}
	msg, err := a.parseReply(body)
	if err != nil {
		slog.Warn("grokbot: rejected reply body", "error", err)
		http.Error(w, "invalid reply", http.StatusBadRequest)
		return
	}
	if a.deliver(id, msg) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	a.mu.Lock()
	_, dup := a.done[id]
	a.mu.Unlock()
	if dup {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "duplicate": true})
		return
	}
	writeJSON(w, http.StatusConflict, map[string]any{
		"ok":    false,
		"error": "turn finished before the reply was accepted",
	})
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

const (
	// outboxBadRetries and outboxBadGrace bound how long a non-atomic or
	// corrupt outbox file is tolerated before the turn fails. A finished
	// JSON file is renamed into place, so a few attempts are enough for a
	// writer that is still flushing.
	outboxBadRetries = 5
	outboxBadGrace   = time.Second
)

type outboxKind int

const (
	outboxMissing outboxKind = iota
	outboxReady
	outboxInvalid
	outboxFatal
)

func (a *Agent) waitRegistered(ctx context.Context, id string, ch <-chan replyMsg, timeout time.Duration) (replyMsg, error) {
	var badSince time.Time
	badCount := 0
	var badErr error

	takeFile := func() (replyMsg, bool, error) {
		msg, kind, err := a.consumeOutbox(id)
		switch kind {
		case outboxReady:
			return msg, true, nil
		case outboxFatal:
			return replyMsg{}, false, err
		case outboxInvalid:
			if badSince.IsZero() {
				badSince = time.Now()
				slog.Warn("grokbot: outbox file is not valid JSON", "id", id, "error", err)
			}
			badCount++
			badErr = err
			if badCount >= outboxBadRetries && time.Since(badSince) >= outboxBadGrace {
				slog.Warn("grokbot: giving up on invalid outbox file", "id", id, "attempts", badCount, "error", badErr)
				return replyMsg{}, false, fmt.Errorf("grokbot: outbox reply %s.json is not valid JSON: %w", id, badErr)
			}
			return replyMsg{}, false, nil
		default:
			return replyMsg{}, false, nil
		}
	}

	if msg, ok, err := takeFile(); err != nil || ok {
		if err != nil {
			return replyMsg{}, err
		}
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
			if msg, ok, err := takeFile(); err != nil || ok {
				if err != nil {
					return replyMsg{}, err
				}
				return unwrapReply(msg)
			}
		case <-timer.C:
			// The HTTP handler may have validated a body and queued it in
			// the same instant the timer fired. Take that reply instead of
			// reporting a timeout.
			select {
			case msg := <-ch:
				return unwrapReply(msg)
			default:
			}
			if msg, ok, err := takeFile(); err != nil || ok {
				if err != nil {
					return replyMsg{}, err
				}
				return unwrapReply(msg)
			}
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

func (a *Agent) consumeOutbox(id string) (replyMsg, outboxKind, error) {
	dir := a.outboxDir()
	if dir == "" || !validID(id) {
		return replyMsg{}, outboxMissing, nil
	}
	path := filepath.Join(dir, id+".json")
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return replyMsg{}, outboxMissing, nil
		}
		return replyMsg{}, outboxFatal, fmt.Errorf("grokbot: outbox reply %s.json: %w", id, err)
	}
	if problem := replyFileProblem(id, fi); problem != nil {
		return replyMsg{}, outboxFatal, problem
	}
	// Open the inode we will actually read. O_NOFOLLOW|O_NONBLOCK (unix) plus
	// fstat close the gap where the path is swapped for a symlink, device, or
	// FIFO between the Lstat above and the read.
	f, err := openReplyFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return replyMsg{}, outboxMissing, nil
		}
		if errors.Is(err, errReplySymlink) {
			return replyMsg{}, outboxFatal, fmt.Errorf("grokbot: outbox reply %s.json is a symlink", id)
		}
		return replyMsg{}, outboxFatal, fmt.Errorf("grokbot: outbox reply %s.json: %w", id, err)
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return replyMsg{}, outboxFatal, fmt.Errorf("grokbot: outbox reply %s.json: %w", id, err)
	}
	if problem := replyFileProblem(id, st); problem != nil {
		return replyMsg{}, outboxFatal, problem
	}
	data, err := io.ReadAll(io.LimitReader(f, maxReplyBytes+1))
	if err != nil {
		return replyMsg{}, outboxFatal, fmt.Errorf("grokbot: outbox reply %s.json: %w", id, err)
	}
	if int64(len(data)) > maxReplyBytes {
		return replyMsg{}, outboxFatal, fmt.Errorf("grokbot: outbox reply %s.json exceeds %d bytes", id, maxReplyBytes)
	}
	msg, err := a.parseReply(data)
	if err != nil {
		// Leave the file in place so a writer can replace it during the grace
		// window. A still-invalid file fails the turn from waitRegistered.
		return replyMsg{}, outboxInvalid, err
	}
	_ = os.Remove(path)
	a.mu.Lock()
	a.done[id] = time.Now()
	a.pruneDoneLocked()
	a.mu.Unlock()
	return msg, outboxReady, nil
}

// errReplySymlink is returned by openReplyFile when the path is a symlink.
var errReplySymlink = errors.New("grokbot: reply file is a symlink")

func replyFileProblem(id string, fi os.FileInfo) error {
	if fi == nil {
		return fmt.Errorf("grokbot: outbox reply %s.json: missing file info", id)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("grokbot: outbox reply %s.json is a symlink", id)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("grokbot: outbox reply %s.json is not a regular file", id)
	}
	if fi.Size() > maxReplyBytes {
		return fmt.Errorf("grokbot: outbox reply %s.json exceeds %d bytes", id, maxReplyBytes)
	}
	return nil
}

func (a *Agent) parseReply(raw []byte) (replyMsg, error) {
	return parseReplyBody(raw, a.allowedRoots())
}

func parseReplyBody(raw []byte, roots []string) (replyMsg, error) {
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
	images, imgErr := decodeImages(wire.Images, roots)
	files, fileErr := decodeFiles(wire.Files, roots)
	if denied := firstDenied(imgErr, fileErr); denied != nil {
		return replyMsg{err: denied}, nil
	}
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

func decodeImages(raw json.RawMessage, roots []string) ([]core.ImageAttachment, error) {
	elems, err := rawElems(raw)
	if err != nil || len(elems) == 0 {
		return nil, err
	}
	var out []core.ImageAttachment
	var firstErr error
	for _, el := range elems {
		img, err := decodeOneImage(el, roots)
		if err != nil {
			if firstDenied(err) != nil {
				return nil, err
			}
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

func decodeFiles(raw json.RawMessage, roots []string) ([]core.FileAttachment, error) {
	elems, err := rawElems(raw)
	if err != nil || len(elems) == 0 {
		return nil, err
	}
	var out []core.FileAttachment
	var firstErr error
	for _, el := range elems {
		f, err := decodeOneFile(el, roots)
		if err != nil {
			if firstDenied(err) != nil {
				return nil, err
			}
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

func firstDenied(errs ...error) error {
	for _, err := range errs {
		var denied *pathDeniedError
		if errors.As(err, &denied) {
			return denied
		}
	}
	return nil
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

func decodeOneImage(el json.RawMessage, roots []string) (core.ImageAttachment, error) {
	if path, ok := rawString(el); ok {
		return imageFromPath(path, roots)
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
			name = "image"
		}
		if mimeType == "" {
			mimeType = mediaType(name, data)
		}
		if filepath.Ext(name) == "" {
			name += extFromMime(mimeType)
		}
		return core.ImageAttachment{MimeType: mimeType, Data: data, FileName: name}, nil
	}
	if strings.TrimSpace(obj.Path) == "" {
		return core.ImageAttachment{}, fmt.Errorf("grokbot: reply image missing path and data_base64")
	}
	img, err := imageFromPath(obj.Path, roots)
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

func decodeOneFile(el json.RawMessage, roots []string) (core.FileAttachment, error) {
	if path, ok := rawString(el); ok {
		return fileFromPath(path, roots)
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
			mimeType = mediaType(name, data)
		}
		return core.FileAttachment{MimeType: mimeType, Data: data, FileName: name}, nil
	}
	if strings.TrimSpace(obj.Path) == "" {
		return core.FileAttachment{}, fmt.Errorf("grokbot: reply file missing path and data_base64")
	}
	f, err := fileFromPath(obj.Path, roots)
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

func imageFromPath(p string, roots []string) (core.ImageAttachment, error) {
	data, name, err := readLocal(p, roots)
	if err != nil {
		return core.ImageAttachment{}, err
	}
	return core.ImageAttachment{MimeType: mediaType(name, data), Data: data, FileName: name}, nil
}

func fileFromPath(p string, roots []string) (core.FileAttachment, error) {
	data, name, err := readLocal(p, roots)
	if err != nil {
		return core.FileAttachment{}, err
	}
	return core.FileAttachment{MimeType: mediaType(name, data), Data: data, FileName: name}, nil
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
