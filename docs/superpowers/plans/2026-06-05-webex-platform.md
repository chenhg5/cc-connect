# Webex Platform Adapter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Cisco Webex as a native Go platform adapter in cc-connect, driven by the Webex Device WebSocket API, with email-allowlist auth, text + file/image support, and group @mention gating.

**Architecture:** A self-contained `platform/webex/` package implementing `core.Platform`. `Start()` fetches bot identity, registers a Webex "device" to obtain a WebSocket URL, and runs a reconnecting read loop. Inbound WebSocket events carry only a message ID (Webex compliance), so the adapter fetches full message bodies via REST, gates them, and dispatches `core.Message` to the engine. Outbound `Reply`/`Send` POST markdown to `/v1/messages`. All Webex REST/WS calls go through a small `webexClient` interface so tests can stub them.

**Tech Stack:** Go 1.25, `github.com/gorilla/websocket` (already a dependency), stdlib `net/http`, cc-connect `core` package.

---

## Reference Material

Study these before starting — they are the canonical patterns to mirror:

- `platform/telegram/telegram.go` — `Platform` struct, `New()`, `Start()`, `connectLoop` reconnection, `dispatchMessage`, allowlist gating, mention stripping.
- `platform/telegram/telegram_reply.go` — reply enrichment pattern.
- `core/interfaces.go` — `Platform`, `MessageHandler`, optional interfaces (`ImageSender`, `FileSender`, `ReplyContextReconstructor`, `FormattingInstructionProvider`, `AsyncRecoverablePlatform`).
- `core/message.go` — `Message`, `ImageAttachment`, `FileAttachment`, helpers `AllowList(allowFrom, userID)`, `CheckAllowFrom(platform, allowFrom)`, `RedactToken(text, token)`.
- `cmd/cc-connect/plugin_platform_telegram.go` — the one-line build-tag plugin file.
- `Makefile` line 37 — `ALL_PLATFORMS`.

### Webex API facts (verify against live API during Task 2 if uncertain)

- Base REST URL: `https://webexapis.com/v1`
- Auth header on every call: `Authorization: Bearer <token>`
- `GET /v1/people/me` → `{ "id": "...", "emails": ["bot@..."], "displayName": "..." }` (bot's own identity; `id` is `selfID`).
- `POST /v1/devices` → returns `{ "webSocketUrl": "wss://...", "url": "https://.../devices/{id}" }`. Body can be a minimal device descriptor (name/model). The `url` is used for `DELETE` on shutdown.
- WebSocket frames are JSON. A message event looks like:
  ```json
  { "data": { "eventType": "conversation.activity",
              "activity": { "verb": "post", "id": "...", ... } } }
  ```
  But the **Messages API** webhook-style envelope (preferred, simpler) is:
  ```json
  { "id": "<webhook-event-id>",
    "data": { "id": "<messageId>", "roomId": "...", "roomType": "direct|group",
              "personId": "...", "personEmail": "user@x.com" },
    "resource": "messages", "event": "created" }
  ```
  Use the `resource == "messages" && event == "created"` envelope.
- `GET /v1/messages/{id}` → full message: `{ "id", "roomId", "roomType", "text", "markdown", "personId", "personEmail", "mentionedPeople": ["id1"], "files": ["https://.../contents/..."] }`.
- File download: `GET <fileUrl>` with the Bearer header → raw bytes; `Content-Type` header gives MIME, `Content-Disposition` gives filename.
- `POST /v1/messages` JSON body: `{ "roomId", "parentId" (optional), "markdown" }`.
- File upload: `POST /v1/messages` as `multipart/form-data` with `roomId` and `files` parts.
- Message body cap: 7439 bytes. Split on paragraph boundaries.
- The `<spark-mention>...</spark-mention>` HTML tag wraps @mentions in `text`; strip it for group messages.

---

## File Structure

| File | Responsibility |
|---|---|
| `platform/webex/client.go` | `webexClient` interface + real HTTP implementation (`httpClient`). All REST + file I/O. |
| `platform/webex/webex.go` | `Platform` struct, `New()`, `Start()`, `Stop()`, device registration, WebSocket connect/read loop, reconnection, message gating + dispatch. |
| `platform/webex/webex_reply.go` | `Reply()`, `Send()`, markdown chunking, `ImageSender`/`FileSender`, `ReplyContextReconstructor`, `FormattingInstructionProvider`. |
| `platform/webex/types.go` | JSON structs for Webex API responses (person, device, message, ws event). |
| `platform/webex/webex_test.go` | Unit tests with a stub `webexClient`. |
| `cmd/cc-connect/plugin_platform_webex.go` | Build-tag import. |
| `Makefile` | Add `webex` to `ALL_PLATFORMS`. |
| `config.example.toml` | Webex config block. |

---

## Task 1: Package skeleton + types + client interface

**Files:**
- Create: `platform/webex/types.go`
- Create: `platform/webex/client.go`

- [ ] **Step 1: Create `types.go` with Webex API response structs**

```go
package webex

// person is the subset of GET /v1/people/me we use.
type person struct {
	ID          string   `json:"id"`
	Emails      []string `json:"emails"`
	DisplayName string   `json:"displayName"`
}

// device is the subset of POST /v1/devices response we use.
type device struct {
	URL          string `json:"url"`          // for DELETE on shutdown
	WebSocketURL string `json:"webSocketUrl"` // wss:// endpoint
}

// message is the subset of GET /v1/messages/{id} we use.
type message struct {
	ID              string   `json:"id"`
	RoomID          string   `json:"roomId"`
	RoomType        string   `json:"roomType"` // "direct" | "group"
	Text            string   `json:"text"`
	Markdown        string   `json:"markdown"`
	PersonID        string   `json:"personId"`
	PersonEmail     string   `json:"personEmail"`
	MentionedPeople []string `json:"mentionedPeople"`
	Files           []string `json:"files"`
}

// wsEvent is the message envelope delivered over the WebSocket.
type wsEvent struct {
	Resource string `json:"resource"` // "messages"
	Event    string `json:"event"`    // "created"
	Data     struct {
		ID          string `json:"id"`       // message ID
		RoomID      string `json:"roomId"`
		RoomType    string `json:"roomType"`
		PersonID    string `json:"personId"` // actor
		PersonEmail string `json:"personEmail"`
	} `json:"data"`
}

// downloadedFile is a fetched attachment with metadata.
type downloadedFile struct {
	Data     []byte
	MimeType string
	FileName string
}
```

- [ ] **Step 2: Create `client.go` with the `webexClient` interface and HTTP implementation**

```go
package webex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

const webexBaseURL = "https://webexapis.com/v1"

// webexClient abstracts the Webex REST API so tests can stub it.
type webexClient interface {
	GetMe(ctx context.Context) (*person, error)
	CreateDevice(ctx context.Context) (*device, error)
	DeleteDevice(ctx context.Context, deviceURL string) error
	GetMessage(ctx context.Context, id string) (*message, error)
	DownloadFile(ctx context.Context, url string) (*downloadedFile, error)
	PostMessage(ctx context.Context, roomID, parentID, markdown string) error
	PostFile(ctx context.Context, roomID string, f *downloadedFile) error
}

// httpClient is the real webexClient backed by net/http.
type httpClient struct {
	token string
	hc    *http.Client
}

func newHTTPClient(token string) *httpClient {
	return &httpClient{token: token, hc: &http.Client{Timeout: 60 * time.Second}}
}

func (c *httpClient) do(ctx context.Context, method, url string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return c.hc.Do(req)
}

func (c *httpClient) GetMe(ctx context.Context) (*person, error) {
	resp, err := c.do(ctx, http.MethodGet, webexBaseURL+"/people/me", nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("webex: getMe status %d", resp.StatusCode)
	}
	var p person
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (c *httpClient) CreateDevice(ctx context.Context) (*device, error) {
	payload := strings.NewReader(`{"deviceName":"cc-connect","deviceType":"DESKTOP","name":"cc-connect","systemName":"cc-connect","systemVersion":"1.0"}`)
	resp, err := c.do(ctx, http.MethodPost, "https://wdm-a.wbx2.com/wdm/api/v1/devices", payload, "application/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("webex: createDevice status %d", resp.StatusCode)
	}
	var d device
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (c *httpClient) DeleteDevice(ctx context.Context, deviceURL string) error {
	if deviceURL == "" {
		return nil
	}
	resp, err := c.do(ctx, http.MethodDelete, deviceURL, nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func (c *httpClient) GetMessage(ctx context.Context, id string) (*message, error) {
	resp, err := c.do(ctx, http.MethodGet, webexBaseURL+"/messages/"+id, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("webex: getMessage status %d", resp.StatusCode)
	}
	var m message
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (c *httpClient) DownloadFile(ctx context.Context, url string) (*downloadedFile, error) {
	resp, err := c.do(ctx, http.MethodGet, url, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("webex: downloadFile status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	f := &downloadedFile{Data: data, MimeType: resp.Header.Get("Content-Type")}
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if _, params, err := mime.ParseMediaType(cd); err == nil {
			f.FileName = params["filename"]
		}
	}
	return f, nil
}

func (c *httpClient) PostMessage(ctx context.Context, roomID, parentID, markdown string) error {
	body := map[string]string{"roomId": roomID, "markdown": markdown}
	if parentID != "" {
		body["parentId"] = parentID
	}
	buf, _ := json.Marshal(body)
	resp, err := c.do(ctx, http.MethodPost, webexBaseURL+"/messages", bytes.NewReader(buf), "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("webex: postMessage status %d", resp.StatusCode)
	}
	return nil
}

func (c *httpClient) PostFile(ctx context.Context, roomID string, f *downloadedFile) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("roomId", roomID)
	name := f.FileName
	if name == "" {
		name = "attachment"
	}
	part, err := w.CreateFormFile("files", name)
	if err != nil {
		return err
	}
	if _, err := part.Write(f.Data); err != nil {
		return err
	}
	w.Close()
	resp, err := c.do(ctx, http.MethodPost, webexBaseURL+"/messages", &buf, w.FormDataContentType())
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("webex: postFile status %d", resp.StatusCode)
	}
	return nil
}
```

- [ ] **Step 3: Verify it compiles**

Run: `cd ~/Documents/personal/cc-connect && go build ./platform/webex/`
Expected: builds with no output (no unused-import errors).

- [ ] **Step 4: Commit**

```bash
git add platform/webex/types.go platform/webex/client.go
git commit -m "feat(webex): add API types and REST client"
```

---

## Task 2: Platform struct, New(), and helper functions (with tests)

**Files:**
- Create: `platform/webex/webex.go`
- Create: `platform/webex/webex_test.go`

- [ ] **Step 1: Write the failing test for `New()` and `parseAllowFrom`**

```go
package webex

import (
	"context"
	"testing"
)

func TestNewRequiresToken(t *testing.T) {
	if _, err := New(map[string]any{}); err == nil {
		t.Fatal("expected error when token is missing")
	}
}

func TestNewParsesAllowFrom(t *testing.T) {
	p, err := New(map[string]any{
		"token":      "abc",
		"allow_from": "A@x.com, b@x.com",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wp := p.(*Platform)
	if len(wp.allowFrom) != 2 {
		t.Fatalf("expected 2 allowed emails, got %d", len(wp.allowFrom))
	}
}

// stubClient implements webexClient for tests.
type stubClient struct {
	me           *person
	dev          *device
	msg          *message
	file         *downloadedFile
	posted       []postedMsg
	postedFiles  []string
	deletedURL   string
}

type postedMsg struct {
	roomID, parentID, markdown string
}

func (s *stubClient) GetMe(context.Context) (*person, error)        { return s.me, nil }
func (s *stubClient) CreateDevice(context.Context) (*device, error) { return s.dev, nil }
func (s *stubClient) DeleteDevice(_ context.Context, url string) error {
	s.deletedURL = url
	return nil
}
func (s *stubClient) GetMessage(context.Context, string) (*message, error) { return s.msg, nil }
func (s *stubClient) DownloadFile(context.Context, string) (*downloadedFile, error) {
	return s.file, nil
}
func (s *stubClient) PostMessage(_ context.Context, roomID, parentID, markdown string) error {
	s.posted = append(s.posted, postedMsg{roomID, parentID, markdown})
	return nil
}
func (s *stubClient) PostFile(_ context.Context, roomID string, f *downloadedFile) error {
	s.postedFiles = append(s.postedFiles, roomID)
	return nil
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./platform/webex/ -run TestNew -v`
Expected: FAIL — `New` and `Platform` undefined.

- [ ] **Step 3: Write `webex.go` with the struct, `New()`, and accessors**

```go
package webex

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterPlatform("webex", New)
}

// replyContext carries what Reply/Send need to target a Webex room.
type replyContext struct {
	roomID    string
	messageID string
	personID  string
}

// Platform is the Webex adapter implementing core.Platform.
type Platform struct {
	token     string
	allowFrom []string // lowercased email allowlist; empty = allow all

	client webexClient

	mu               sync.RWMutex
	handler          core.MessageHandler
	lifecycleHandler core.PlatformLifecycleHandler
	cancel           context.CancelFunc
	stopping         bool
	selfID           string // bot's own personId
	deviceURL        string // for cleanup on Stop()
}

// New constructs a Webex platform from config options.
func New(opts map[string]any) (core.Platform, error) {
	token, _ := opts["token"].(string)
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("webex: token is required")
	}
	rawAllow, _ := opts["allow_from"].(string)
	core.CheckAllowFrom("webex", rawAllow)

	return &Platform{
		token:     token,
		allowFrom: parseAllowFrom(rawAllow),
		client:    newHTTPClient(token),
	}, nil
}

func (p *Platform) Name() string { return "webex" }

// parseAllowFrom splits and lowercases a comma-separated email list.
func parseAllowFrom(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []string
	for _, e := range strings.Split(raw, ",") {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, strings.ToLower(e))
		}
	}
	return out
}

// isAllowed reports whether an email may use the bot.
// Empty allowlist permits everyone (a startup warning was already logged).
func (p *Platform) isAllowed(email string) bool {
	if len(p.allowFrom) == 0 {
		return true
	}
	email = strings.ToLower(strings.TrimSpace(email))
	for _, a := range p.allowFrom {
		if a == email {
			return true
		}
	}
	return false
}

func (p *Platform) messageHandler() core.MessageHandler {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.handler
}

func (p *Platform) isStopping() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.stopping
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./platform/webex/ -run TestNew -v`
Expected: PASS (both `TestNewRequiresToken` and `TestNewParsesAllowFrom`).

- [ ] **Step 5: Commit**

```bash
git add platform/webex/webex.go platform/webex/webex_test.go
git commit -m "feat(webex): add Platform struct, New, and allowlist parsing"
```

---

## Task 3: Message gating and parsing (with tests)

**Files:**
- Modify: `platform/webex/webex.go`
- Modify: `platform/webex/webex_test.go`

- [ ] **Step 1: Write failing tests for mention stripping and the gate**

Add to `webex_test.go`:

```go
func TestStripMention(t *testing.T) {
	in := `<spark-mention data-object-type="person" data-object-id="123">bot</spark-mention> hello there`
	got := stripMention(in)
	if got != "hello there" {
		t.Fatalf("got %q, want %q", got, "hello there")
	}
}

func TestStripMentionNoTag(t *testing.T) {
	if got := stripMention("plain text"); got != "plain text" {
		t.Fatalf("got %q", got)
	}
}

func TestShouldProcessGroupRequiresMention(t *testing.T) {
	p := &Platform{selfID: "bot-id"}
	// group message that does NOT mention the bot
	m := &message{RoomType: "group", PersonEmail: "u@x.com", MentionedPeople: []string{"someone-else"}}
	if p.shouldProcess(m) {
		t.Fatal("group message without bot mention should be skipped")
	}
	// group message that DOES mention the bot
	m.MentionedPeople = []string{"bot-id"}
	if !p.shouldProcess(m) {
		t.Fatal("group message mentioning bot should be processed")
	}
}

func TestShouldProcessDirectAlwaysOK(t *testing.T) {
	p := &Platform{selfID: "bot-id"}
	m := &message{RoomType: "direct", PersonEmail: "u@x.com"}
	if !p.shouldProcess(m) {
		t.Fatal("direct message should be processed")
	}
}

func TestShouldProcessDeniedEmail(t *testing.T) {
	p := &Platform{selfID: "bot-id", allowFrom: []string{"allowed@x.com"}}
	m := &message{RoomType: "direct", PersonEmail: "stranger@x.com"}
	if p.shouldProcess(m) {
		t.Fatal("message from non-allowlisted email should be skipped")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./platform/webex/ -run TestStripMention -v && go test ./platform/webex/ -run TestShouldProcess -v`
Expected: FAIL — `stripMention` and `shouldProcess` undefined.

- [ ] **Step 3: Implement `stripMention`, `shouldProcess`, and `isMentioned` in `webex.go`**

```go
import "regexp" // add to existing import block

var sparkMentionRe = regexp.MustCompile(`(?s)<spark-mention[^>]*>.*?</spark-mention>`)

// stripMention removes Webex <spark-mention> tags and trims the result.
func stripMention(text string) string {
	return strings.TrimSpace(sparkMentionRe.ReplaceAllString(text, ""))
}

// isMentioned reports whether the bot's selfID appears in mentionedPeople.
func (p *Platform) isMentioned(m *message) bool {
	for _, id := range m.MentionedPeople {
		if id == p.selfID {
			return true
		}
	}
	return false
}

// shouldProcess applies the gate: allowlist + group-mention requirement.
func (p *Platform) shouldProcess(m *message) bool {
	if !p.isAllowed(m.PersonEmail) {
		return false
	}
	if m.RoomType == "group" && !p.isMentioned(m) {
		return false
	}
	return true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./platform/webex/ -v`
Expected: PASS (all tests so far).

- [ ] **Step 5: Commit**

```bash
git add platform/webex/webex.go platform/webex/webex_test.go
git commit -m "feat(webex): add message gating and mention stripping"
```

---

## Task 4: Build core.Message from a Webex message (with tests)

**Files:**
- Modify: `platform/webex/webex.go`
- Modify: `platform/webex/webex_test.go`

- [ ] **Step 1: Write the failing test for `buildMessage`**

Add to `webex_test.go`:

```go
import "strings" // ensure present in test imports

func TestBuildMessageText(t *testing.T) {
	p := &Platform{selfID: "bot-id", client: &stubClient{}}
	m := &message{
		ID: "msg1", RoomID: "room1", RoomType: "direct",
		Text: "hello", PersonID: "p1", PersonEmail: "u@x.com",
	}
	cm := p.buildMessage(context.Background(), m)
	if cm.Content != "hello" {
		t.Fatalf("content = %q", cm.Content)
	}
	if cm.Platform != "webex" {
		t.Fatalf("platform = %q", cm.Platform)
	}
	if cm.SessionKey != "webex:room1:p1" {
		t.Fatalf("sessionKey = %q", cm.SessionKey)
	}
	rc, ok := cm.ReplyCtx.(replyContext)
	if !ok || rc.roomID != "room1" || rc.messageID != "msg1" {
		t.Fatalf("replyCtx = %+v", cm.ReplyCtx)
	}
}

func TestBuildMessageGroupStripsMention(t *testing.T) {
	p := &Platform{selfID: "bot-id", client: &stubClient{}}
	m := &message{
		ID: "m", RoomID: "r", RoomType: "group",
		Text:     `<spark-mention data-object-id="bot-id">bot</spark-mention> do the thing`,
		PersonID: "p1", PersonEmail: "u@x.com",
		MentionedPeople: []string{"bot-id"},
	}
	cm := p.buildMessage(context.Background(), m)
	if cm.Content != "do the thing" {
		t.Fatalf("content = %q", cm.Content)
	}
}

func TestBuildMessageImageAttachment(t *testing.T) {
	stub := &stubClient{file: &downloadedFile{Data: []byte{1, 2, 3}, MimeType: "image/png", FileName: "a.png"}}
	p := &Platform{selfID: "bot-id", client: stub}
	m := &message{
		ID: "m", RoomID: "r", RoomType: "direct",
		Text: "look", PersonID: "p1", PersonEmail: "u@x.com",
		Files: []string{"https://webex/contents/1"},
	}
	cm := p.buildMessage(context.Background(), m)
	if len(cm.Images) != 1 || cm.Images[0].MimeType != "image/png" {
		t.Fatalf("images = %+v", cm.Images)
	}
	if len(cm.Files) != 0 {
		t.Fatalf("expected no non-image files, got %d", len(cm.Files))
	}
}

func TestBuildMessageNonImageFile(t *testing.T) {
	stub := &stubClient{file: &downloadedFile{Data: []byte{1}, MimeType: "application/pdf", FileName: "r.pdf"}}
	p := &Platform{selfID: "bot-id", client: stub}
	m := &message{
		ID: "m", RoomID: "r", RoomType: "direct",
		PersonID: "p1", PersonEmail: "u@x.com",
		Files: []string{"https://webex/contents/1"},
	}
	cm := p.buildMessage(context.Background(), m)
	if len(cm.Files) != 1 || cm.Files[0].FileName != "r.pdf" {
		t.Fatalf("files = %+v", cm.Files)
	}
	if len(cm.Images) != 0 {
		t.Fatalf("expected no images, got %d", len(cm.Images))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./platform/webex/ -run TestBuildMessage -v`
Expected: FAIL — `buildMessage` undefined.

- [ ] **Step 3: Implement `buildMessage` in `webex.go`**

```go
import "log/slog" // add to existing import block

// buildMessage converts a fetched Webex message into a core.Message,
// downloading any attachments and stripping group @mentions.
func (p *Platform) buildMessage(ctx context.Context, m *message) *core.Message {
	content := m.Text
	if m.RoomType == "group" {
		content = stripMention(content)
	}

	cm := &core.Message{
		SessionKey: fmt.Sprintf("webex:%s:%s", m.RoomID, m.PersonID),
		Platform:   "webex",
		MessageID:  m.ID,
		ChannelID:  m.RoomID,
		ChannelKey: m.RoomID,
		UserID:     m.PersonEmail,
		UserName:   m.PersonEmail,
		Content:    content,
		ReplyCtx:   replyContext{roomID: m.RoomID, messageID: m.ID, personID: m.PersonID},
	}

	for _, url := range m.Files {
		f, err := p.client.DownloadFile(ctx, url)
		if err != nil {
			slog.Error("webex: download file failed", "error", err)
			continue
		}
		if strings.HasPrefix(f.MimeType, "image/") {
			cm.Images = append(cm.Images, core.ImageAttachment{
				MimeType: f.MimeType, Data: f.Data, FileName: f.FileName,
			})
		} else {
			cm.Files = append(cm.Files, core.FileAttachment{
				MimeType: f.MimeType, Data: f.Data, FileName: f.FileName,
			})
		}
	}
	return cm
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./platform/webex/ -v`
Expected: PASS (all tests).

- [ ] **Step 5: Commit**

```bash
git add platform/webex/webex.go platform/webex/webex_test.go
git commit -m "feat(webex): build core.Message with attachment handling"
```

---

## Task 5: Reply, Send, chunking, and optional sender interfaces (with tests)

**Files:**
- Create: `platform/webex/webex_reply.go`
- Modify: `platform/webex/webex_test.go`

- [ ] **Step 1: Write failing tests for chunking, Reply, and Send**

Add to `webex_test.go`:

```go
func TestChunkUnderLimit(t *testing.T) {
	chunks := chunkMarkdown("short", 100)
	if len(chunks) != 1 || chunks[0] != "short" {
		t.Fatalf("chunks = %v", chunks)
	}
}

func TestChunkSplitsOnParagraph(t *testing.T) {
	text := "aaaa\n\nbbbb\n\ncccc"
	chunks := chunkMarkdown(text, 6) // forces splits
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d: %v", len(chunks), chunks)
	}
	// reassembly preserves all non-whitespace content
	joined := strings.ReplaceAll(strings.Join(chunks, ""), "\n", "")
	if !strings.Contains(joined, "aaaa") || !strings.Contains(joined, "cccc") {
		t.Fatalf("content lost in chunking: %v", chunks)
	}
}

func TestReplyPostsWithParent(t *testing.T) {
	stub := &stubClient{}
	p := &Platform{client: stub}
	rc := replyContext{roomID: "r1", messageID: "m1"}
	if err := p.Reply(context.Background(), rc, "hi"); err != nil {
		t.Fatalf("Reply err: %v", err)
	}
	if len(stub.posted) != 1 || stub.posted[0].roomID != "r1" || stub.posted[0].parentID != "m1" {
		t.Fatalf("posted = %+v", stub.posted)
	}
}

func TestSendPostsWithoutParent(t *testing.T) {
	stub := &stubClient{}
	p := &Platform{client: stub}
	rc := replyContext{roomID: "r1", messageID: "m1"}
	if err := p.Send(context.Background(), rc, "yo"); err != nil {
		t.Fatalf("Send err: %v", err)
	}
	if len(stub.posted) != 1 || stub.posted[0].parentID != "" {
		t.Fatalf("posted = %+v", stub.posted)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./platform/webex/ -run 'TestChunk|TestReply|TestSend' -v`
Expected: FAIL — `chunkMarkdown`, `Reply`, `Send` undefined.

- [ ] **Step 3: Implement `webex_reply.go`**

```go
package webex

import (
	"context"
	"fmt"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// webexMaxBytes is Webex's per-message body cap.
const webexMaxBytes = 7439

// asReplyContext recovers a replyContext from the engine's any-typed value.
func asReplyContext(replyCtx any) (replyContext, error) {
	rc, ok := replyCtx.(replyContext)
	if !ok {
		return replyContext{}, fmt.Errorf("webex: invalid reply context %T", replyCtx)
	}
	return rc, nil
}

// Reply posts a threaded response to the originating message.
func (p *Platform) Reply(ctx context.Context, replyCtx any, content string) error {
	rc, err := asReplyContext(replyCtx)
	if err != nil {
		return err
	}
	return p.post(ctx, rc.roomID, rc.messageID, content)
}

// Send posts a non-threaded (proactive) message to the room.
func (p *Platform) Send(ctx context.Context, replyCtx any, content string) error {
	rc, err := asReplyContext(replyCtx)
	if err != nil {
		return err
	}
	return p.post(ctx, rc.roomID, "", content)
}

// post chunks content and posts each chunk; only the first chunk threads.
func (p *Platform) post(ctx context.Context, roomID, parentID, content string) error {
	chunks := chunkMarkdown(content, webexMaxBytes)
	for i, chunk := range chunks {
		pid := ""
		if i == 0 {
			pid = parentID
		}
		if err := p.client.PostMessage(ctx, roomID, pid, chunk); err != nil {
			return fmt.Errorf("webex: post chunk %d/%d: %w", i+1, len(chunks), err)
		}
	}
	return nil
}

// chunkMarkdown splits text to fit within limit bytes, preferring paragraph
// (\n\n), then line (\n), then a hard cut.
func chunkMarkdown(text string, limit int) []string {
	if len(text) <= limit {
		return []string{text}
	}
	var out []string
	rest := text
	for len(rest) > limit {
		cut := strings.LastIndex(rest[:limit], "\n\n")
		if cut <= 0 {
			cut = strings.LastIndex(rest[:limit], "\n")
		}
		if cut <= 0 {
			cut = limit
		}
		out = append(out, rest[:cut])
		rest = strings.TrimLeft(rest[cut:], "\n")
	}
	if rest != "" {
		out = append(out, rest)
	}
	return out
}

// SendImage implements core.ImageSender.
func (p *Platform) SendImage(ctx context.Context, replyCtx any, img core.ImageAttachment) error {
	rc, err := asReplyContext(replyCtx)
	if err != nil {
		return err
	}
	return p.client.PostFile(ctx, rc.roomID, &downloadedFile{
		Data: img.Data, MimeType: img.MimeType, FileName: img.FileName,
	})
}

// SendFile implements core.FileSender.
func (p *Platform) SendFile(ctx context.Context, replyCtx any, file core.FileAttachment) error {
	rc, err := asReplyContext(replyCtx)
	if err != nil {
		return err
	}
	return p.client.PostFile(ctx, rc.roomID, &downloadedFile{
		Data: file.Data, MimeType: file.MimeType, FileName: file.FileName,
	})
}

// ReconstructReplyCtx implements core.ReplyContextReconstructor for cron jobs.
// Session key format is "webex:{roomID}:{personID}".
func (p *Platform) ReconstructReplyCtx(sessionKey string) (any, error) {
	parts := strings.SplitN(sessionKey, ":", 3)
	if len(parts) < 2 || parts[0] != "webex" {
		return nil, fmt.Errorf("webex: cannot reconstruct reply ctx from %q", sessionKey)
	}
	rc := replyContext{roomID: parts[1]}
	if len(parts) == 3 {
		rc.personID = parts[2]
	}
	return rc, nil
}

// FormattingInstructions implements core.FormattingInstructionProvider.
func (p *Platform) FormattingInstructions() string {
	return "Webex supports standard Markdown (bold, italic, lists, code blocks, links). Use it freely."
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./platform/webex/ -v`
Expected: PASS (all tests).

- [ ] **Step 5: Commit**

```bash
git add platform/webex/webex_reply.go platform/webex/webex_test.go
git commit -m "feat(webex): add Reply, Send, chunking, and file senders"
```

---

## Task 6: Start, Stop, device registration, and WebSocket read loop

**Files:**
- Modify: `platform/webex/webex.go`

This task wires the live connection. The WebSocket loop is not unit-tested
(it needs a live server); the gating/parsing it calls is already covered. The
verification here is a compile + `go vet`.

- [ ] **Step 1: Add `Start`, `Stop`, the connect loop, and event handling to `webex.go`**

```go
import (
	"encoding/json"  // add to import block
	"time"           // add to import block

	"github.com/gorilla/websocket" // add to import block
)

const (
	initialBackoff = time.Second
	maxBackoff     = 30 * time.Second
)

// Start fetches the bot identity, registers a device, and launches the
// reconnecting WebSocket read loop in the background.
func (p *Platform) Start(handler core.MessageHandler) error {
	p.mu.Lock()
	if p.stopping {
		p.mu.Unlock()
		return fmt.Errorf("webex: platform stopped")
	}
	p.handler = handler
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.mu.Unlock()

	me, err := p.client.GetMe(ctx)
	if err != nil {
		return fmt.Errorf("webex: getMe: %w", err)
	}
	p.mu.Lock()
	p.selfID = me.ID
	p.mu.Unlock()
	slog.Info("webex: authenticated", "bot", me.DisplayName)

	go p.connectLoop(ctx)
	return nil
}

// Stop cancels the read loop and deletes the registered device.
func (p *Platform) Stop() error {
	p.mu.Lock()
	p.stopping = true
	cancel := p.cancel
	deviceURL := p.deviceURL
	p.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if deviceURL != "" {
		ctx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		if err := p.client.DeleteDevice(ctx, deviceURL); err != nil {
			slog.Warn("webex: delete device failed", "error", err)
		}
	}
	return nil
}

// SetLifecycleHandler implements core.AsyncRecoverablePlatform.
func (p *Platform) SetLifecycleHandler(h core.PlatformLifecycleHandler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lifecycleHandler = h
}

// connectLoop registers a device, opens the WebSocket, and reconnects with
// exponential backoff until the context is cancelled.
func (p *Platform) connectLoop(ctx context.Context) {
	backoff := initialBackoff
	for {
		if ctx.Err() != nil || p.isStopping() {
			return
		}
		started := time.Now()
		err := p.runConnection(ctx)
		if ctx.Err() != nil || p.isStopping() {
			return
		}
		if err != nil {
			slog.Warn("webex: connection ended", "error", err, "backoff", backoff)
			if h := p.lifecycle(); h != nil {
				h.OnPlatformUnavailable(p, err)
			}
		}
		// Reset backoff after a stable connection.
		if time.Since(started) >= 10*time.Second {
			backoff = initialBackoff
		} else if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

func (p *Platform) lifecycle() core.PlatformLifecycleHandler {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.lifecycleHandler
}

// runConnection registers a device, dials the WebSocket, and reads until the
// connection drops or the context is cancelled.
func (p *Platform) runConnection(ctx context.Context) error {
	dev, err := p.client.CreateDevice(ctx)
	if err != nil {
		return fmt.Errorf("create device: %w", err)
	}
	p.mu.Lock()
	p.deviceURL = dev.URL
	p.mu.Unlock()

	header := map[string][]string{"Authorization": {"Bearer " + p.token}}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, dev.WebSocketURL, header)
	if err != nil {
		return fmt.Errorf("dial websocket: %w", err)
	}
	defer conn.Close()

	slog.Info("webex: websocket connected")
	if h := p.lifecycle(); h != nil {
		h.OnPlatformReady(p)
	}

	// Close the conn when ctx is cancelled so ReadMessage unblocks.
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read websocket: %w", err)
		}
		p.handleFrame(ctx, data)
	}
}

// handleFrame parses one WebSocket frame and dispatches qualifying messages.
func (p *Platform) handleFrame(ctx context.Context, data []byte) {
	var ev wsEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		slog.Debug("webex: non-JSON frame", "error", err)
		return
	}
	if ev.Resource != "messages" || ev.Event != "created" {
		return
	}
	// Ignore our own messages.
	if ev.Data.PersonID == p.selfID {
		return
	}
	m, err := p.client.GetMessage(ctx, ev.Data.ID)
	if err != nil {
		slog.Error("webex: fetch message failed", "error", err)
		return
	}
	if !p.shouldProcess(m) {
		slog.Debug("webex: message gated out", "room_type", m.RoomType, "from", m.PersonEmail)
		return
	}
	handler := p.messageHandler()
	if handler == nil {
		return
	}
	handler(p, p.buildMessage(ctx, m))
}
```

- [ ] **Step 2: Verify it compiles and vets clean**

Run: `go build ./platform/webex/ && go vet ./platform/webex/`
Expected: no output (success).

- [ ] **Step 3: Run the full package test suite**

Run: `go test ./platform/webex/ -v`
Expected: PASS (all prior tests still green).

- [ ] **Step 4: Commit**

```bash
git add platform/webex/webex.go
git commit -m "feat(webex): add Start/Stop and reconnecting WebSocket loop"
```

---

## Task 7: Wire into the build (plugin file, Makefile, config example)

**Files:**
- Create: `cmd/cc-connect/plugin_platform_webex.go`
- Modify: `Makefile` (line 37, `ALL_PLATFORMS`)
- Modify: `config.example.toml`

- [ ] **Step 1: Create the build-tag plugin file**

`cmd/cc-connect/plugin_platform_webex.go`:

```go
//go:build !no_webex

package main

import _ "github.com/chenhg5/cc-connect/platform/webex"
```

- [ ] **Step 2: Add `webex` to `ALL_PLATFORMS` in the Makefile**

Change line 37 from:
```make
ALL_PLATFORMS := feishu telegram discord slack dingtalk wecom weixin qq qqbot line weibo max
```
to:
```make
ALL_PLATFORMS := feishu telegram discord slack dingtalk wecom weixin qq qqbot line weibo max webex
```

- [ ] **Step 3: Add a Webex config example to `config.example.toml`**

Find the Telegram platform example block in `config.example.toml` and add this block after it (match the file's existing comment style):

```toml
# -----------------------------------------------------------------------------
# Webex (Cisco) — WebSocket, no public IP required
# -----------------------------------------------------------------------------
# [[projects]]
# name = "webex-project"
# work_dir = "~/code/myproject"
# agent = "claudecode"
# platform = "webex"
#
# [projects.platform.options]
# token = "YOUR_WEBEX_BOT_ACCESS_TOKEN"   # from developer.webex.com (Bot)
# allow_from = "you@cisco.com"            # comma-separated email allowlist
```

- [ ] **Step 4: Verify the whole binary builds with webex included**

Run: `go build ./...`
Expected: no output (success).

- [ ] **Step 5: Verify exclusion build tag works**

Run: `go build -tags 'no_webex' ./cmd/cc-connect`
Expected: no output (success — webex compiled out cleanly).

- [ ] **Step 6: Commit**

```bash
git add cmd/cc-connect/plugin_platform_webex.go Makefile config.example.toml
git commit -m "feat(webex): wire platform into build, Makefile, and config example"
```

---

## Task 8: Full validation and final checks

**Files:** none (validation only)

- [ ] **Step 1: Run the full test suite with the race detector**

Run: `go test -race ./platform/webex/ ./core/`
Expected: PASS, no race warnings.

- [ ] **Step 2: Confirm the full project still builds and vets**

Run: `go build ./... && go vet ./platform/webex/`
Expected: no output.

- [ ] **Step 3: Confirm interface conformance (compile-time assertions)**

Add to the bottom of `platform/webex/webex_reply.go`:

```go
// Compile-time interface conformance checks.
var (
	_ core.Platform                    = (*Platform)(nil)
	_ core.ImageSender                 = (*Platform)(nil)
	_ core.FileSender                  = (*Platform)(nil)
	_ core.ReplyContextReconstructor   = (*Platform)(nil)
	_ core.FormattingInstructionProvider = (*Platform)(nil)
	_ core.AsyncRecoverablePlatform    = (*Platform)(nil)
)
```

Run: `go build ./platform/webex/`
Expected: no output. If any assertion fails, the build error names the missing method — implement it.

- [ ] **Step 4: Commit the conformance assertions**

```bash
git add platform/webex/webex_reply.go
git commit -m "test(webex): add compile-time interface conformance checks"
```

- [ ] **Step 5: Push the branch to the fork**

```bash
git push -u fork add-webex-platform
```
Expected: branch pushed; GitHub prints a PR-creation URL.

---

## Post-Implementation (manual, outside this plan)

- Manual integration test: create a Webex bot at developer.webex.com, set `token`
  + `allow_from`, run `cc-connect`, DM the bot, and verify a Claude session responds.
  Test a group space with an @mention. Test sending/receiving a file.
- Open the PR from `bryantbarzola:add-webex-platform` → `chenhg5:main`. Consider
  opening a tracking issue first to gauge maintainer interest.
- The design spec at `docs/superpowers/specs/2026-06-05-webex-platform-design.md`
  may be excluded from the PR (it's process documentation) — decide at PR time.

## Risks / Things to Verify Against the Live API

- **Device registration endpoint:** The plan uses the Webex WDM endpoint
  (`wdm-a.wbx2.com/wdm/api/v1/devices`) which is what real-time clients use.
  If that proves unavailable for bot tokens, fall back to the documented
  `POST https://webexapis.com/v1/devices` shape. Confirm the response contains
  `webSocketUrl`. This is the single biggest unknown — verify first during
  integration testing.
- **WebSocket frame shape:** Confirm the `resource`/`event`/`data.id` envelope.
  If Webex delivers the WDM `conversation.activity` shape instead, adjust
  `wsEvent` and `handleFrame` accordingly (the gating/parsing logic downstream
  is unaffected).
- **Self-message field:** Confirm the actor ID field name on the event
  (`data.personId`) matches `selfID` from `/people/me`.
