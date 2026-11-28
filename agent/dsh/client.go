// Package dsh bridges cc-connect to a running DeepSeek Harness (dsh) web
// server. It implements the core.Agent interface by forwarding prompts to
// the server's typed RPC API (POST /api/session.prompt) and consuming the
// aggregated event stream (GET /api/events.mux) for streaming output,
// tool calls, permission approvals, and AskUserQuestion interactions.
//
// The dsh server must be running on the same machine (default
// http://127.0.0.1:3080, i.e. `dsh web`). Sessions are created with the
// project's work_dir as cwd, so each cc-connect project maps to its own
// dsh session; resuming reuses the persisted session on the server side.
package dsh

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// ── Wire types (mirror packages/host/apiproxy/src/api/*) ──────────

// clientRequest is the POST /api/<method> request body.
type clientRequest struct {
	Type    string `json:"type"` // "client-request"
	RPCID   string `json:"rpcId"`
	Method  string `json:"method"`
	Payload any    `json:"payload"`
}

// serverResponse is the HTTP response body of a unary RPC call.
type serverResponse struct {
	Type   string          `json:"type"` // "server-response"
	RPCID  string          `json:"rpcId"`
	Result rpcResult       `json:"result"`
	Raw    json.RawMessage `json:"-"`
}

type rpcResult struct {
	OK    bool            `json:"ok"`
	Value json.RawMessage `json:"value,omitempty"`
	Error *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Details is intentionally ignored: cc-connect only needs code + message.
}

// serverRequest is the envelope of every SSE frame on /api/events.mux.
type serverRequest struct {
	Type    string          `json:"type"` // "server-request"
	RPCID   string          `json:"rpcId"`
	Method  string          `json:"method"`
	Payload json.RawMessage `json:"payload"`
}

// muxFrame is one frame of the aggregated event stream, discriminated by Type.
// RPCID is the ServerRequest envelope's rpcId — the answerable id used to
// respond to approval/question frames via /api/respond. Unknown frame types
// are ignored by the reader (forward compatibility).
type muxFrame struct {
	RPCID     string          `json:"-"`
	Type      string          `json:"type"`
	SessionID string          `json:"sessionId"`
	Event     json.RawMessage `json:"event,omitempty"` // session/event payload
	View      json.RawMessage `json:"view,omitempty"`
	// approval/requested
	ApprovalID string `json:"approvalId,omitempty"`
	ToolName   string `json:"toolName,omitempty"`
	CallID     string `json:"callId,omitempty"`
	Reason     string `json:"reason,omitempty"`
	// question/requested
	Questions []wireQuestion `json:"questions,omitempty"`
	// stream/error
	Error *rpcError `json:"error,omitempty"`
}

// wireQuestion mirrors AskUserQuestionItem.
type wireQuestion struct {
	ID          string `json:"id"`
	Question    string `json:"question"`
	Detail      string `json:"detail,omitempty"`
	Header      string `json:"header,omitempty"`
	MultiSelect bool   `json:"multiSelect,omitempty"`
	Options     []struct {
		Label       string `json:"label"`
		Description string `json:"description,omitempty"`
	} `json:"options,omitempty"`
}

// sessionEvent is a raw SessionEvent (packages/session/.../types.ts). Only
// the fields cc-connect consumes are decoded; everything else is ignored.
type sessionEvent struct {
	Type string          `json:"type"`
	Seq  int64           `json:"seq"`
	Data json.RawMessage `json:"data"`
}

// sessionEventData unions the data payloads of the events we consume.
type sessionEventData struct {
	Turn    int             `json:"turn,omitempty"`
	Step    int             `json:"step,omitempty"`
	Reason  json.RawMessage `json:"reason,omitempty"`  // turn/end reason
	Chunk   *streamChunk    `json:"chunk,omitempty"`   // assistant/chunk
	Message json.RawMessage `json:"message,omitempty"` // assistant/message
	Usage   *tokenUsage     `json:"usage,omitempty"`
	Name    string          `json:"name,omitempty"`      // tool/call
	Args    string          `json:"arguments,omitempty"` // tool/call
	CallID  string          `json:"callId,omitempty"`    // tool/call + tool/result
	Error   *struct {
		Name    string `json:"name"`
		Code    string `json:"code"`
		Message string `json:"message,omitempty"`
	} `json:"error,omitempty"` // tool/result error / turn failure
}

// streamChunk mirrors StreamChunk (packages/llm/llm/src/types.ts).
type streamChunk struct {
	Type     string          `json:"type"`
	Index    int             `json:"index,omitempty"`
	Text     string          `json:"text,omitempty"`
	ID       string          `json:"id,omitempty"`
	Name     string          `json:"name,omitempty"`
	ArgsDelt string          `json:"argumentsDelta,omitempty"`
	Block    json.RawMessage `json:"block,omitempty"`
	Usage    *tokenUsage     `json:"usage,omitempty"`
}

// tokenUsage mirrors TokenUsage.
type tokenUsage struct {
	InputTokens      int `json:"inputTokens"`
	OutputTokens     int `json:"outputTokens"`
	CacheReadTokens  int `json:"cacheReadTokens"`
	CacheWriteTokens int `json:"cacheWriteTokens"`
}

// sessionSummary mirrors SessionSummary from session.list. UpdatedAt is a
// float64 on the wire (millisecond epoch, possibly fractional) — int64
// unmarshalling fails on values like 1786465628680.5632.
type sessionSummary struct {
	SessionID   string  `json:"sessionId"`
	UpdatedAt   float64 `json:"updatedAt"`
	Running     bool    `json:"running"`
	Blank       bool    `json:"blank"`
	Cwd         string  `json:"cwd,omitempty"`
	AgentPreset string  `json:"agentPreset,omitempty"`
	Projections *struct {
		Values struct {
			Title *string `json:"title"`
		} `json:"values"`
	} `json:"projections,omitempty"`
}

// sessionModels mirrors SessionModels from session.models.
type sessionModels struct {
	Current struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	} `json:"current"`
	Routable bool `json:"routable"`
	Groups   []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Models []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description,omitempty"`
			Reasoning   struct {
				Efforts []struct {
					ID          string `json:"id"`
					Name        string `json:"name"`
					Description string `json:"description,omitempty"`
				} `json:"efforts"`
				DefaultEffort string `json:"defaultEffort,omitempty"`
			} `json:"reasoning,omitempty"`
		} `json:"models"`
	} `json:"groups"`
}

// rpcClient is a minimal client for the dsh typed RPC API.
type rpcClient struct {
	baseURL    string
	httpClient *http.Client
}

func newRPCClient(baseURL string) *rpcClient {
	return &rpcClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			// No overall timeout: unary RPCs may legitimately take a while
			// (e.g. session.create mounts a fresh agent). The SSE stream is
			// never subject to this client anyway (it has its own reader).
			Timeout: 0,
			Transport: &http.Transport{
				ResponseHeaderTimeout: 30 * time.Second,
			},
		},
	}
}

func mintRPCID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("cc-connect-%d", time.Now().UnixNano())
	}
	return "cc-connect-" + hex.EncodeToString(b[:])
}

// base64Encode wraps encoding/base64 for prompt image parts.
func base64Encode(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

// call performs one unary RPC (POST /api/<method>). It returns the ok value
// decoded into out, or a descriptive error (including the RPC error code).
func (c *rpcClient) call(ctx context.Context, method string, payload any, out any) error {
	reqBody, err := json.Marshal(clientRequest{
		Type:    "client-request",
		RPCID:   mintRPCID(),
		Method:  method,
		Payload: payload,
	})
	if err != nil {
		return fmt.Errorf("dsh: marshal %s: %w", method, err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/"+method, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("dsh: create %s request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("dsh: %s: %w (is the dsh web server running? start it with `dsh web`)", method, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Debug("dsh: close response body", "method", method, "error", err)
		}
	}()

	// Carrier-layer errors: non-JSON / non-2xx responses are transport failures.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("dsh: read %s response: %w", method, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 512 {
			msg = msg[:512]
		}
		return fmt.Errorf("dsh: %s returned HTTP %d: %s", method, resp.StatusCode, msg)
	}

	var sr serverResponse
	if err := json.Unmarshal(body, &sr); err != nil {
		return fmt.Errorf("dsh: %s: invalid server-response: %w", method, err)
	}
	if !sr.Result.OK {
		if sr.Result.Error != nil {
			return &rpcCallError{Method: method, Code: sr.Result.Error.Code, Message: sr.Result.Error.Message}
		}
		return fmt.Errorf("dsh: %s: server returned ok=false without error details", method)
	}
	if out != nil {
		if err := json.Unmarshal(sr.Result.Value, out); err != nil {
			return fmt.Errorf("dsh: %s: decode value: %w", method, err)
		}
	}
	return nil
}

// rpcCallError carries the structured RPC error so callers can branch on code.
type rpcCallError struct {
	Method  string
	Code    string
	Message string
}

func (e *rpcCallError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("dsh: %s: %s (%s)", e.Method, e.Message, e.Code)
	}
	return fmt.Sprintf("dsh: %s: %s", e.Method, e.Code)
}

// respond sends one client-response to POST /api/respond (approval or
// question answers). The server replies with a receipt, which is checked for
// shape but otherwise ignored.
func (c *rpcClient) respond(ctx context.Context, rpcID string, value any) error {
	body := map[string]any{
		"type":   "client-response",
		"rpcId":  rpcID,
		"result": map[string]any{"ok": true, "value": value},
	}
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("dsh: marshal respond: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/respond", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("dsh: create respond request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("dsh: respond: %w", err)
	}
	defer resp.Body.Close()

	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("dsh: respond returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(rb)))
	}
	var receipt struct {
		Accepted bool   `json:"accepted"`
		Reason   string `json:"reason,omitempty"`
	}
	if err := json.Unmarshal(rb, &receipt); err == nil && !receipt.Accepted {
		return fmt.Errorf("dsh: respond not accepted: %s", receipt.Reason)
	}
	return nil
}

// ── Event stream (WebSocket) ────────────────────────────────────

// openMux connects to /api/events.mux over WebSocket (the dsh webserver
// requires an HTTP upgrade for the two event streams) and returns a channel
// of parsed frames for targetSessionID. The stream stays open until ctx is
// cancelled or the server closes it; the returned channel is closed when the
// reader finishes.
//
// The mux stream aggregates EVERY session on the server (including Web GUI
// sessions), so frames are filtered to the target session at the socket
// reader. Frames that must never be lost (turn/end, approval/question
// requests, stream errors) are enqueued with priority when the channel is
// full; other frames are dropped rather than stalling the socket reader.
func (c *rpcClient) openMux(ctx context.Context, targetSessionID string) (<-chan muxFrame, error) {
	u, err := url.Parse(c.baseURL + "/api/events.mux")
	if err != nil {
		return nil, fmt.Errorf("dsh: parse mux url: %w", err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	}

	dialer := websocket.Dialer{HandshakeTimeout: 30 * time.Second}
	conn, resp, err := dialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		if resp != nil {
			rb, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			return nil, fmt.Errorf("dsh: events.mux websocket: %w (HTTP %d: %s)", err, resp.StatusCode, strings.TrimSpace(string(rb)))
		}
		return nil, fmt.Errorf("dsh: events.mux websocket: %w", err)
	}

	frames := make(chan muxFrame, 1024)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer close(frames)
		defer conn.Close()
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			_, data, err := conn.ReadMessage()
			if err != nil {
				return // connection closed or server gone
			}
			var sr serverRequest
			if err := json.Unmarshal(data, &sr); err != nil {
				slog.Debug("dsh: dropping malformed mux frame", "error", err)
				continue
			}
			var frame muxFrame
			if err := json.Unmarshal(sr.Payload, &frame); err != nil {
				slog.Debug("dsh: dropping unparseable mux frame", "type", frame.Type, "error", err)
				continue
			}
			frame.RPCID = sr.RPCID

			// Drop frames belonging to other sessions early: the mux stream
			// carries every session on the server, and a busy Web GUI would
			// otherwise flood this session's channel (lost turn/end frames
			// were the root cause of stuck turns after permission answers).
			if frame.Type != "stream/error" && frame.SessionID != "" && frame.SessionID != targetSessionID {
				continue
			}

			if criticalFrame(frame) {
				// Never lose turn/end, approval/question requests, or
				// stream errors — block until the consumer catches up.
				select {
				case frames <- frame:
				case <-ctx.Done():
					return
				}
				continue
			}
			select {
			case frames <- frame:
			default:
				// Never block the socket reader; a slow engine consumer drops
				// non-critical frames (turn/end is protected above, and the
				// engine re-aggregates text from its own accumulators).
				slog.Warn("dsh: mux frame channel full, dropping frame", "type", frame.Type)
			}
		}
	}()
	// ReadMessage blocks on the socket and does not observe ctx cancellation;
	// closing the connection from here unblocks it.
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-readerDone:
		}
	}()
	return frames, nil
}

// parseBaseURL normalizes a configured base URL.
func parseBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "http://127.0.0.1:3080"
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("dsh: invalid base_url %q: %w", raw, err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("dsh: invalid base_url %q: missing host", raw)
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// criticalFrame reports whether a frame must survive channel overflow:
// turn/end (the engine's turn-completion signal), approval/question requests
// (answerable interactions), and stream errors.
func criticalFrame(f muxFrame) bool {
	switch f.Type {
	case "approval/requested", "question/requested", "stream/error":
		return true
	case "session/event":
		var ev sessionEvent
		if json.Unmarshal(f.Event, &ev) == nil && ev.Type == "turn/end" {
			return true
		}
	}
	return false
}
