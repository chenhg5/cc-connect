package webex

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
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
	me          *person
	dev         *device
	msg         *message
	file        *downloadedFile
	posted      []postedMsg
	postedFiles []string
	deletedURL  string
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
	joined := strings.ReplaceAll(strings.Join(chunks, ""), "\n", "")
	if !strings.Contains(joined, "aaaa") || !strings.Contains(joined, "cccc") {
		t.Fatalf("content lost in chunking: %v", chunks)
	}
}

func TestIsAllowedWildcard(t *testing.T) {
	p := &Platform{allowFrom: parseAllowFrom("*")}
	if !p.isAllowed("anyone@x.com") {
		t.Fatal("wildcard should allow any email")
	}
}

func TestIsAllowedEmptyAllowsAll(t *testing.T) {
	p := &Platform{}
	if !p.isAllowed("anyone@x.com") {
		t.Fatal("empty allowlist should allow all")
	}
}

func TestChunkMultibyteNoSplit(t *testing.T) {
	// 10 CJK chars (3 bytes each = 30 bytes), limit 8 bytes, no newlines
	text := strings.Repeat("世", 10)
	chunks := chunkMarkdown(text, 8)
	for _, c := range chunks {
		if !utf8.ValidString(c) {
			t.Fatalf("chunk is not valid UTF-8: %q", c)
		}
	}
	if strings.Join(chunks, "") != text {
		t.Fatalf("content lost: %q", strings.Join(chunks, ""))
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

func TestActivityIDToMessageID(t *testing.T) {
	got := activityIDToMessageID("766bc5a0-6b31-11f1-9a28-4325af6c06a3")
	want := "Y2lzY29zcGFyazovL3VzL01FU1NBR0UvNzY2YmM1YTAtNmIzMS0xMWYxLTlhMjgtNDMyNWFmNmMwNmEz"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if activityIDToMessageID("") != "" {
		t.Fatal("empty activity id should yield empty message id")
	}
}

func TestHandleFrameIgnoresNonActivity(t *testing.T) {
	// A conversation.highlight frame must not trigger a GetMessage call.
	stub := &stubClient{}
	p := &Platform{client: stub, selfEmail: "bot@webex.bot"}
	p.handler = func(_ core.Platform, _ *core.Message) { t.Fatal("should not dispatch") }
	p.handleFrame(context.Background(), []byte(`{"data":{"eventType":"conversation.highlight"}}`))
	// no panic, no dispatch = pass
}

// activityFrame builds a minimal conversation.activity frame JSON for the given verb.
func activityFrame(verb string) []byte {
	return []byte(`{"data":{"eventType":"conversation.activity","activity":{` +
		`"id":"766bc5a0-6b31-11f1-9a28-4325af6c06a3","verb":"` + verb + `",` +
		`"actor":{"emailAddress":"user@x.com"}}}}`)
}

func TestHandleFramePostDispatches(t *testing.T) {
	stub := &stubClient{msg: &message{ID: "m", RoomID: "r", RoomType: "direct", Text: "hi", PersonID: "p", PersonEmail: "user@x.com"}}
	p := &Platform{client: stub, selfEmail: "bot@webex.bot"}
	dispatched := false
	p.handler = func(_ core.Platform, _ *core.Message) { dispatched = true }
	p.handleFrame(context.Background(), activityFrame("post"))
	if !dispatched {
		t.Fatal("post verb should dispatch")
	}
}

func TestHandleFrameShareDispatches(t *testing.T) {
	stub := &stubClient{msg: &message{ID: "m", RoomID: "r", RoomType: "direct", Text: "", PersonID: "p", PersonEmail: "user@x.com"}}
	p := &Platform{client: stub, selfEmail: "bot@webex.bot"}
	dispatched := false
	p.handler = func(_ core.Platform, _ *core.Message) { dispatched = true }
	p.handleFrame(context.Background(), activityFrame("share"))
	if !dispatched {
		t.Fatal("share verb (file upload) should dispatch")
	}
}

func TestHandleFrameUpdateIgnored(t *testing.T) {
	// "update" is a re-notification (e.g. malware scan complete) and must not dispatch.
	stub := &stubClient{msg: &message{ID: "m", RoomID: "r", RoomType: "direct", PersonEmail: "user@x.com"}}
	p := &Platform{client: stub, selfEmail: "bot@webex.bot"}
	p.handler = func(_ core.Platform, _ *core.Message) { t.Fatal("update verb should not dispatch") }
	p.handleFrame(context.Background(), activityFrame("update"))
}
