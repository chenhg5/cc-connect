package cursorsdk

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const maxCursorSDKImageDimension = 768

type session struct {
	client      *sidecarClient
	workDir     string
	sessionKey  string
	model       string
	mode        string
	apiKey      string
	turnTimeout time.Duration
	idleTTL     time.Duration

	events chan core.Event
	alive  atomic.Bool
	sendMu sync.Mutex

	sessionID atomic.Value // string
}

func newSession(client *sidecarClient, workDir, sessionKey, model, mode, apiKey, sessionID string, timeout, idleTTL time.Duration) *session {
	s := &session{
		client:      client,
		workDir:     workDir,
		sessionKey:  sessionKey,
		model:       model,
		mode:        mode,
		apiKey:      apiKey,
		turnTimeout: timeout,
		idleTTL:     idleTTL,
		events:      make(chan core.Event, 128),
	}
	s.alive.Store(true)
	if sessionID != "" && sessionID != core.ContinueSession {
		s.sessionID.Store(sessionID)
	}
	return s
}

func (s *session) Send(prompt string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if !s.alive.Load() {
		return fmt.Errorf("session is closed")
	}
	if len(files) > 0 {
		prompt = core.AppendFileRefs(prompt, core.SaveFilesToDisk(s.workDir, files))
	}
	if len(images) > 0 {
		paths, err := saveImagesToDisk(s.workDir, images)
		if err != nil {
			return err
		}
		prompt = core.AppendFileRefs(prompt, paths)
	}

	s.sendMu.Lock()
	defer s.sendMu.Unlock()

	req := map[string]any{
		"op":         "send",
		"cwd":        s.workDir,
		"sessionKey": s.sessionKey,
		"model":      s.model,
		"mode":       s.mode,
		"prompt":     prompt,
		"sessionId":  s.CurrentSessionID(),
	}
	if s.apiKey != "" {
		req["apiKey"] = s.apiKey
	}
	if s.idleTTL > 0 {
		req["idleTtlMs"] = int(s.idleTTL / time.Millisecond)
	}
	ch, err := s.client.call(req)
	if err != nil {
		return err
	}
	go s.forward(ch)
	return nil
}

func saveImagesToDisk(workDir string, images []core.ImageAttachment) ([]string, error) {
	if len(images) == 0 {
		return nil, nil
	}
	dir := filepath.Join(workDir, ".cc-connect", "images")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("cursor_sdk: create image dir: %w", err)
	}
	paths := make([]string, 0, len(images))
	for i, img := range images {
		ext := imageExt(img.MimeType)
		name := img.FileName
		if name == "" {
			name = fmt.Sprintf("img_%d_%d%s", time.Now().UnixMilli(), i, ext)
		} else if filepath.Ext(name) == "" {
			name += ext
		}
		data, ext := normalizeImageForSDK(img.Data)
		if ext != "" {
			name = strings.TrimSuffix(name, filepath.Ext(name)) + ext
		}
		path := filepath.Join(dir, filepath.Base(name))
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return nil, fmt.Errorf("cursor_sdk: save image: %w", err)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func normalizeImageForSDK(data []byte) ([]byte, string) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return data, ""
	}
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	newW, newH := scaledSize(width, height, maxCursorSDKImageDimension)
	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	for y := 0; y < newH; y++ {
		srcY := bounds.Min.Y + y*height/newH
		for x := 0; x < newW; x++ {
			srcX := bounds.Min.X + x*width/newW
			c := color.RGBAModel.Convert(img.At(srcX, srcY)).(color.RGBA)
			dst.SetRGBA(x, y, c)
		}
	}

	var out bytes.Buffer
	if err := jpeg.Encode(&out, flattenOnWhite(dst), &jpeg.Options{Quality: 82}); err != nil {
		return data, ""
	}
	return out.Bytes(), ".jpg"
}

func flattenOnWhite(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	draw.Draw(dst, dst.Bounds(), &image.Uniform{C: white}, image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Over)
	return dst
}

func scaledSize(width, height, maxDim int) (int, int) {
	if width <= 0 || height <= 0 || maxDim <= 0 {
		return width, height
	}
	if width >= height {
		return maxDim, max(1, height*maxDim/width)
	}
	return max(1, width*maxDim/height), maxDim
}

func imageExt(mime string) string {
	switch strings.ToLower(mime) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".png"
	}
}

func (s *session) forward(ch <-chan sidecarMessage) {
	var timer <-chan time.Time
	if s.turnTimeout > 0 {
		t := time.NewTimer(s.turnTimeout)
		defer t.Stop()
		timer = t.C
	}
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if msg.SessionID != "" {
				s.sessionID.Store(msg.SessionID)
			}
			switch msg.Event {
			case "session", "run":
				if msg.SessionID != "" {
					s.emit(core.Event{Type: core.EventText, SessionID: msg.SessionID})
				}
			case "text":
				if msg.Text != "" {
					s.emit(core.Event{Type: core.EventText, Content: msg.Text, SessionID: msg.SessionID})
				}
			case "tool":
				s.emit(core.Event{Type: core.EventToolUse, ToolName: msg.ToolName, ToolInput: msg.ToolInput, SessionID: msg.SessionID})
			case "result":
				s.emit(core.Event{
					Type:         core.EventResult,
					Content:      msg.Text,
					SessionID:    s.CurrentSessionID(),
					Done:         true,
					InputTokens:  msg.InputTokens,
					OutputTokens: msg.OutputTokens,
				})
				return
			case "error":
				s.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("%s", msg.Error), SessionID: s.CurrentSessionID()})
				return
			}
		case <-timer:
			s.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("cursor_sdk turn timed out after %s", s.turnTimeout), SessionID: s.CurrentSessionID()})
			return
		}
	}
}

func (s *session) emit(evt core.Event) {
	defer func() { recover() }() //nolint:errcheck // guard against send on closed channel (race with Close)
	select {
	case s.events <- evt:
	default:
		// Preserve the sidecar reader over perfect progress fidelity.
	}
}

func (s *session) RespondPermission(_ string, _ core.PermissionResult) error {
	return nil
}

func (s *session) Events() <-chan core.Event {
	return s.events
}

func (s *session) CurrentSessionID() string {
	v, _ := s.sessionID.Load().(string)
	return v
}

func (s *session) Alive() bool {
	return s.alive.Load() && s.client.alive.Load()
}

func (s *session) Close() error {
	if !s.alive.Swap(false) {
		return nil
	}
	if sid := s.CurrentSessionID(); sid != "" && s.client.alive.Load() {
		if ch, err := s.client.call(map[string]any{"op": "close", "sessionId": sid}); err == nil {
			select {
			case <-ch:
			case <-time.After(2 * time.Second):
			}
		}
	}
	close(s.events)
	return nil
}
