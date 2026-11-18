package platform

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chenhg5/cc-connect/core"
)

// Remote delivers injected user input to a cc-connect process instead of an
// in-process engine. The bridge client below is the only implementation.
type Remote interface {
	SendMessage(msg RemoteMessage) error
	SendCardAction(sessionKey, action string) error
}

// RemoteMessage is one user message sent through a Remote.
type RemoteMessage struct {
	SessionKey string
	UserID     string
	UserName   string
	Content    string
	Images     []core.ImageAttachment
	Files      []core.FileAttachment
}

// BridgeClient is a bridge-protocol adapter (docs/bridge-protocol.md) that
// connects a MockPlatform to a running cc-connect binary. Everything the
// binary sends back is recorded on the MockPlatform, so the same assertion
// helpers work for the in-process and the binary backend.
type BridgeClient struct {
	mp      *MockPlatform
	conn    *websocket.Conn
	project string

	writeMu sync.Mutex
	done    chan struct{}
	once    sync.Once
	readErr error
	seq     int
	seqMu   sync.Mutex
}

// ConnectBridge dials wsURL, registers as mp's platform name with the
// capabilities of profile, and starts recording replies on mp. project binds
// the adapter to one cc-connect project ("" = default project).
func ConnectBridge(mp *MockPlatform, wsURL, token, project string, profile Profile) (*BridgeClient, error) {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, resp, err := dialer.Dial(wsURL, header)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("bridge dial %s: %w (HTTP %d)", redactURL(wsURL), err, resp.StatusCode)
		}
		return nil, fmt.Errorf("bridge dial %s: %w", redactURL(wsURL), err)
	}

	c := &BridgeClient{mp: mp, conn: conn, project: project, done: make(chan struct{})}
	reg := map[string]any{
		"type":         "register",
		"platform":     mp.Name(),
		"capabilities": profile.BridgeCapabilities(),
		"metadata":     map[string]any{"version": "blackbox", "description": "cc-connect blackbox test adapter"},
	}
	if project != "" {
		reg["project"] = project
	}
	if err := c.write(reg); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("bridge register: %w", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var ack struct {
		Type  string `json:"type"`
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := conn.ReadJSON(&ack); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("bridge register_ack: %w", err)
	}
	if ack.Type != "register_ack" || !ack.OK {
		_ = conn.Close()
		return nil, fmt.Errorf("bridge register rejected: type=%q error=%q", ack.Type, ack.Error)
	}
	_ = conn.SetReadDeadline(time.Time{})

	mp.mu.Lock()
	mp.remote = c
	mp.mu.Unlock()

	go c.readLoop()
	go c.pingLoop()
	return c, nil
}

// Close disconnects the adapter. Safe to call more than once.
func (c *BridgeClient) Close() {
	c.once.Do(func() {
		close(c.done)
		c.writeMu.Lock()
		_ = c.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		c.writeMu.Unlock()
		_ = c.conn.Close()
	})
}

// Err returns the error that ended the read loop, if any.
func (c *BridgeClient) Err() error {
	c.seqMu.Lock()
	defer c.seqMu.Unlock()
	return c.readErr
}

// SendMessage implements Remote.
func (c *BridgeClient) SendMessage(m RemoteMessage) error {
	c.seqMu.Lock()
	c.seq++
	id := fmt.Sprintf("bb-%d-%d", time.Now().UnixNano(), c.seq)
	c.seqMu.Unlock()

	msg := map[string]any{
		"type":        "message",
		"msg_id":      id,
		"session_key": m.SessionKey,
		"user_id":     m.UserID,
		"user_name":   m.UserName,
		"content":     m.Content,
		"reply_ctx":   m.SessionKey,
	}
	if c.project != "" {
		msg["project"] = c.project
	}
	var imgs []map[string]string
	for _, img := range m.Images {
		imgs = append(imgs, map[string]string{
			"mime_type": img.MimeType, "data": base64.StdEncoding.EncodeToString(img.Data), "file_name": img.FileName,
		})
	}
	if len(imgs) > 0 {
		msg["images"] = imgs
	}
	var files []map[string]string
	for _, f := range m.Files {
		files = append(files, map[string]string{
			"mime_type": f.MimeType, "data": base64.StdEncoding.EncodeToString(f.Data), "file_name": f.FileName,
		})
	}
	if len(files) > 0 {
		msg["files"] = files
	}
	return c.write(msg)
}

// SendCardAction implements Remote.
func (c *BridgeClient) SendCardAction(sessionKey, action string) error {
	msg := map[string]any{
		"type":        "card_action",
		"session_key": sessionKey,
		"action":      action,
		"reply_ctx":   sessionKey,
	}
	if c.project != "" {
		msg["project"] = c.project
	}
	return c.write(msg)
}

func (c *BridgeClient) write(v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return c.conn.WriteJSON(v)
}

func (c *BridgeClient) pingLoop() {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
			_ = c.write(map[string]any{"type": "ping", "ts": time.Now().UnixMilli()})
		}
	}
}

// bridgeFrame is the union of the cc-connect → adapter frames we record.
type bridgeFrame struct {
	Type          string                `json:"type"`
	RefID         string                `json:"ref_id"`
	SessionKey    string                `json:"session_key"`
	ReplyCtx      string                `json:"reply_ctx"`
	PreviewHandle string                `json:"preview_handle"`
	Content       string                `json:"content"`
	Card          map[string]any        `json:"card"`
	Buttons       [][]core.ButtonOption `json:"buttons"`
	FileName      string                `json:"file_name"`
	Message       string                `json:"message"`
	Error         string                `json:"error"`
}

func (c *BridgeClient) readLoop() {
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			select {
			case <-c.done:
			default:
				c.seqMu.Lock()
				c.readErr = err
				c.seqMu.Unlock()
			}
			return
		}
		var f bridgeFrame
		if err := json.Unmarshal(data, &f); err != nil {
			continue
		}
		c.handle(f)
	}
}

func (c *BridgeClient) handle(f bridgeFrame) {
	now := time.Now()
	switch f.Type {
	case "reply", "reply_stream":
		c.mp.record(&SentMessage{Kind: KindReply, Content: f.Content, ReplyCtx: f.ReplyCtx, At: now})
	case "buttons":
		c.mp.record(&SentMessage{Kind: KindButtons, Content: f.Content, Buttons: f.Buttons, ReplyCtx: f.ReplyCtx, At: now})
	case "card":
		c.mp.record(&SentMessage{Kind: KindCard, Content: renderCardJSON(f.Card), CardJSON: f.Card, ReplyCtx: f.ReplyCtx, At: now})
	case "preview_start":
		// The handle we return is the ref_id; cc-connect echoes it back as
		// preview_handle on update_message / delete_message.
		c.mp.record(&SentMessage{Kind: KindPreview, Content: f.Content, ReplyCtx: f.ReplyCtx, Handle: f.RefID, At: now})
		_ = c.write(map[string]any{"type": "preview_ack", "ref_id": f.RefID, "preview_handle": f.RefID})
	case "update_message":
		_ = c.mp.UpdatePreview(f.PreviewHandle, f.Content)
	case "delete_message":
		_ = c.mp.DeletePreview(f.PreviewHandle)
	case "image":
		c.mp.record(&SentMessage{Kind: KindImage, Content: "[image]", ReplyCtx: f.ReplyCtx, At: now})
	case "file":
		c.mp.record(&SentMessage{Kind: KindFile, Content: fmt.Sprintf("[file: %s]", f.FileName), ReplyCtx: f.ReplyCtx, At: now})
	case "error":
		c.mp.record(&SentMessage{Kind: KindReply, Content: "[bridge error] " + firstNonEmpty(f.Message, f.Error), At: now})
	}
	// pong, typing_start/stop, audio, video: nothing to assert on yet.
}

// renderCardJSON flattens a serialized bridge card into text, approximating
// core.Card.RenderText so text assertions behave the same on both backends.
func renderCardJSON(card map[string]any) string {
	var parts []string
	if h, ok := card["header"].(map[string]any); ok {
		if t, _ := h["title"].(string); t != "" {
			parts = append(parts, t)
		}
	}
	elems, _ := card["elements"].([]any)
	for _, raw := range elems {
		e, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch e["type"] {
		case "markdown":
			if s, _ := e["content"].(string); s != "" {
				parts = append(parts, s)
			}
		case "note", "list_item":
			if s, _ := e["text"].(string); s != "" {
				parts = append(parts, s)
			}
		case "actions":
			btns, _ := e["buttons"].([]any)
			var labels []string
			for _, b := range btns {
				if bm, ok := b.(map[string]any); ok {
					if s, _ := bm["text"].(string); s != "" {
						labels = append(labels, "["+s+"]")
					}
				}
			}
			if len(labels) > 0 {
				parts = append(parts, strings.Join(labels, " "))
			}
		}
	}
	return strings.Join(parts, "\n")
}

// cardJSONButtonValues returns the callback values of buttons in a
// serialized bridge card.
func cardJSONButtonValues(card map[string]any) []string {
	if card == nil {
		return nil
	}
	var out []string
	elems, _ := card["elements"].([]any)
	for _, raw := range elems {
		e, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch e["type"] {
		case "actions":
			btns, _ := e["buttons"].([]any)
			for _, b := range btns {
				if bm, ok := b.(map[string]any); ok {
					if v, _ := bm["value"].(string); v != "" {
						out = append(out, v)
					}
				}
			}
		case "list_item":
			if v, _ := e["btn_value"].(string); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<invalid url>"
	}
	q := u.Query()
	if q.Has("token") {
		q.Set("token", "REDACTED")
		u.RawQuery = q.Encode()
	}
	return u.String()
}
