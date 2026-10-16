package teams

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	"github.com/golang-jwt/jwt/v5"
)

func mustJSON(a activity) []byte {
	b, err := json.Marshal(a)
	if err != nil {
		panic(err)
	}
	return b
}

// collector captures dispatched messages for assertions.
func collector() (core.MessageHandler, *[]*core.Message) {
	var got []*core.Message
	h := func(_ core.Platform, m *core.Message) { got = append(got, m) }
	return h, &got
}

func teamsPlatform(scope string) *Platform {
	return &Platform{
		cfg:     config{appID: "bot-1", sessionScope: scope},
		engaged: newEngagement(""),
	}
}

func messageActivity(conv, user, text string, mention bool) []byte {
	a := activity{
		Type:         "message",
		ID:           "act-1",
		Text:         text,
		ServiceURL:   "https://smba.example/",
		From:         channelAccount{ID: user, Name: "User"},
		Recipient:    channelAccount{ID: "bot-1"},
		Conversation: conversationAccount{ID: conv},
	}
	if mention {
		a.Entities = []entity{{Type: "mention", Text: "<at>bot</at>", Mentioned: channelAccount{ID: "bot-1"}}}
		a.Text = "<at>bot</at> " + text
	}
	b := mustJSON(a)
	return b
}

func TestEngagement_PersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "teams", "proj-engaged.json")

	// First "process": engage a conversation.
	e1 := newEngagement(path)
	e1.engage("conv-A")
	if !e1.isEngaged("conv-A") {
		t.Fatal("conv-A should be engaged in first instance")
	}

	// Second "process": a fresh engagement loading the same path resumes state.
	e2 := newEngagement(path)
	if !e2.isEngaged("conv-A") {
		t.Fatal("engagement should survive restart (loaded from disk)")
	}
	if e2.isEngaged("conv-B") {
		t.Fatal("unengaged conversation must not be engaged after load")
	}
}

func TestEngagement_AlreadyEngagedDoesNotRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engaged.json")
	e := newEngagement(path)
	e.engage("conv-A")

	// Tamper with the file; re-engaging the same key must NOT rewrite it.
	if err := os.WriteFile(path, []byte("SENTINEL"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.engage("conv-A")
	data, _ := os.ReadFile(path)
	if string(data) != "SENTINEL" {
		t.Error("re-engaging an already-engaged conversation should not rewrite the store")
	}

	// A new key DOES rewrite.
	e.engage("conv-B")
	data, _ = os.ReadFile(path)
	if string(data) == "SENTINEL" {
		t.Error("engaging a new conversation should rewrite the store")
	}
}

func TestEngagementPath_SanitizesProject(t *testing.T) {
	got := engagementPath("/data", "evil/../proj")
	if filepath.Base(filepath.Dir(got)) != "teams" {
		t.Fatalf("project segment escaped the teams dir: %q", got)
	}
	if got != "/data/teams/evil_.._proj-engaged.json" {
		t.Errorf("path = %q", got)
	}
}

func TestEngagement_InMemoryWhenNoPath(t *testing.T) {
	e := newEngagement("")
	e.engage("conv-A") // must not panic / must not write anywhere
	if !e.isEngaged("conv-A") {
		t.Fatal("in-memory engagement should still work")
	}
}

func TestEngagement_CorruptStoreIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engaged.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := newEngagement(path) // must not fail; starts empty
	if e.isEngaged("anything") {
		t.Fatal("corrupt store should yield an empty engaged set")
	}
	// And it should recover by persisting fresh state.
	e.engage("conv-A")
	if !newEngagement(path).isEngaged("conv-A") {
		t.Fatal("should overwrite corrupt store with valid state")
	}
}

func TestEngagementPath(t *testing.T) {
	if got := engagementPath("", "p"); got != "" {
		t.Errorf("empty dataDir should disable persistence, got %q", got)
	}
	if got := engagementPath("/data", ""); got != "" {
		t.Errorf("empty project should disable persistence, got %q", got)
	}
	if got := engagementPath("/data", "mybot"); got != "/data/teams/mybot-engaged.json" {
		t.Errorf("path = %q", got)
	}
}

func TestDispatch_PersonalChatNoMentionForwarded(t *testing.T) {
	p := teamsPlatform("user")
	h, got := collector()
	p.handler = h

	a := activity{
		Type:         "message",
		ID:           "act-1",
		Text:         "hello bot",
		ServiceURL:   "https://smba.example/",
		From:         channelAccount{ID: "user-1"},
		Recipient:    channelAccount{ID: "bot-1"},
		Conversation: conversationAccount{ID: "dm-1", ConversationType: "personal"},
	}
	p.dispatch(nil, mustJSON(a))
	if len(*got) != 1 {
		t.Fatalf("personal chat message must be forwarded without a mention, got %d", len(*got))
	}
}

func TestDispatch_AllowListMatchesAADObjectID(t *testing.T) {
	p := teamsPlatform("thread")
	p.cfg.allowFrom = "aad-stable"
	h, got := collector()
	p.handler = h

	a := activity{
		Type:         "message",
		ID:           "act-1",
		ServiceURL:   "https://smba.example/",
		From:         channelAccount{ID: "chan-id", AADObjectID: "aad-stable"},
		Recipient:    channelAccount{ID: "bot-1"},
		Conversation: conversationAccount{ID: "conv-A", ConversationType: "personal"},
		Text:         "hi",
	}
	p.dispatch(nil, mustJSON(a))
	if len(*got) != 1 {
		t.Fatalf("allow_from should match the AAD object id, got %d", len(*got))
	}
	if (*got)[0].UserID != "aad-stable" {
		t.Errorf("UserID = %q, want aad-stable (AAD preferred)", (*got)[0].UserID)
	}

	// The channel id must NOT match when an AAD id is present.
	p2 := teamsPlatform("thread")
	p2.cfg.allowFrom = "chan-id"
	h2, got2 := collector()
	p2.handler = h2
	p2.dispatch(nil, mustJSON(a))
	if len(*got2) != 0 {
		t.Fatalf("channel id must not satisfy allow_from when AAD id present, got %d", len(*got2))
	}
}

func TestDispatch_ServiceURLClaimMismatchDropped(t *testing.T) {
	p := teamsPlatform("personal")
	h, got := collector()
	p.handler = h

	claims := jwt.MapClaims{"serviceurl": "https://legit.example/"}
	a := activity{
		Type:         "message",
		ID:           "act-1",
		ServiceURL:   "https://attacker.example/", // body disagrees with token claim
		From:         channelAccount{ID: "user-1"},
		Recipient:    channelAccount{ID: "bot-1"},
		Conversation: conversationAccount{ID: "conv-A", ConversationType: "personal"},
		Text:         "hi",
	}
	p.dispatch(claims, mustJSON(a))
	if len(*got) != 0 {
		t.Fatalf("activity with mismatched serviceUrl claim must be dropped, got %d", len(*got))
	}

	// Matching claim passes.
	claims["serviceurl"] = "https://attacker.example/"
	p.dispatch(claims, mustJSON(a))
	if len(*got) != 1 {
		t.Fatalf("matching serviceUrl claim should pass, got %d", len(*got))
	}
}

func TestDispatch_CrossUserEngagement(t *testing.T) {
	p := teamsPlatform("thread")
	h, got := collector()
	p.handler = h

	// user-1 engages conv-A via mention.
	p.dispatch(nil, messageActivity("conv-A", "user-1", "hey bot", true))
	// user-2 (no mention) in the same engaged conversation is now followed.
	p.dispatch(nil, messageActivity("conv-A", "user-2", "and me", false))
	if len(*got) != 2 {
		t.Fatalf("engaged conversation should follow other users too, got %d", len(*got))
	}
}

func TestDispatch_MentionStartsSessionThenAutoFollows(t *testing.T) {
	p := teamsPlatform("thread")
	h, got := collector()
	p.handler = h

	// 1) First message without mention in a fresh conversation -> ignored.
	p.dispatch(nil, messageActivity("conv-A", "user-1", "hello", false))
	if len(*got) != 0 {
		t.Fatalf("non-engaged non-mention should be ignored, got %d", len(*got))
	}

	// 2) Mention engages the conversation and is forwarded.
	p.dispatch(nil, messageActivity("conv-A", "user-1", "are you there", true))
	if len(*got) != 1 {
		t.Fatalf("mention should be forwarded, got %d", len(*got))
	}
	if (*got)[0].Content != "are you there" {
		t.Errorf("content = %q, want mention stripped", (*got)[0].Content)
	}

	// 3) Follow-up without mention now auto-follows.
	p.dispatch(nil, messageActivity("conv-A", "user-1", "thanks", false))
	if len(*got) != 2 {
		t.Fatalf("engaged follow-up should be forwarded, got %d", len(*got))
	}

	// 4) A different conversation is still gated.
	p.dispatch(nil, messageActivity("conv-B", "user-2", "hi", false))
	if len(*got) != 2 {
		t.Fatalf("other conversation must stay gated, got %d", len(*got))
	}
}

func TestDispatch_CardActionAlwaysForwarded(t *testing.T) {
	p := teamsPlatform("thread")
	h, got := collector()
	p.handler = h

	a := activity{
		Type:         "message",
		ID:           "act-2",
		From:         channelAccount{ID: "user-1"},
		Recipient:    channelAccount{ID: "bot-1"},
		Conversation: conversationAccount{ID: "conv-C"},
		Value:        []byte(`{"action":"allow"}`),
	}
	b := mustJSON(a)
	p.dispatch(nil, b)

	if len(*got) != 1 {
		t.Fatalf("card action should be forwarded even when not engaged, got %d", len(*got))
	}
	if (*got)[0].Content != "allow" || !(*got)[0].IsPermissionResponse {
		t.Errorf("card action message = %+v, want content=allow IsPermissionResponse=true", (*got)[0])
	}
}

func TestDispatch_NonMessageIgnored(t *testing.T) {
	p := teamsPlatform("thread")
	h, got := collector()
	p.handler = h
	p.dispatch(nil, []byte(`{"type":"typing"}`))
	if len(*got) != 0 {
		t.Fatalf("non-message activity should be ignored, got %d", len(*got))
	}
}

func TestDispatch_AllowListBlocks(t *testing.T) {
	p := teamsPlatform("thread")
	p.cfg.allowFrom = "approved-user"
	h, got := collector()
	p.handler = h
	// engaged via mention but user not on allow list -> blocked
	p.dispatch(nil, messageActivity("conv-D", "random-user", "hi", true))
	if len(*got) != 0 {
		t.Fatalf("allow-list should block unauthorized user, got %d", len(*got))
	}
}

// TestDispatch_ServiceURLAllowlist covers the optional host allowlist: an activity
// whose serviceURL host is not on the list is dropped before any outbound call.
func TestDispatch_ServiceURLAllowlist(t *testing.T) {
	// messageActivity sets serviceURL "https://smba.example/" (host smba.example).
	allowed := teamsPlatform("thread")
	allowed.cfg.serviceURLAllowlist = []string{"smba.example"}
	h, got := collector()
	allowed.handler = h
	allowed.dispatch(nil, messageActivity("conv-A", "user-1", "hi bot", true))
	if len(*got) != 1 {
		t.Fatalf("allowlisted serviceURL host should be handled, got %d", len(*got))
	}

	blocked := teamsPlatform("thread")
	blocked.cfg.serviceURLAllowlist = []string{"smba.trafficmanager.net"}
	h2, got2 := collector()
	blocked.handler = h2
	blocked.dispatch(nil, messageActivity("conv-B", "user-1", "hi bot", true))
	if len(*got2) != 0 {
		t.Fatalf("serviceURL host off the allowlist must be dropped, got %d", len(*got2))
	}
}

// fileDL builds a FileDownloadInfo attachment with the given name + downloadUrl.
func fileDL(name, downloadURL string) inboundAttachment {
	content, _ := json.Marshal(fileDownloadInfo{DownloadURL: downloadURL, FileType: "docx"})
	return inboundAttachment{ContentType: fileDownloadInfoContentType, Name: name, Content: content}
}

// personalActivity builds a 1:1 message with optional attachments.
func personalActivity(text string, atts ...inboundAttachment) []byte {
	return mustJSON(activity{
		Type:         "message",
		ID:           "act-1",
		Text:         text,
		ServiceURL:   "https://smba.example/",
		From:         channelAccount{ID: "user-1", Name: "User"},
		Recipient:    channelAccount{ID: "bot-1"},
		Conversation: conversationAccount{ID: "dm-1", ConversationType: "personal"},
		Attachments:  atts,
	})
}

func personalPlatform(fetch fetchResult) (*Platform, *[]*core.Message, *fakeSender) {
	p := teamsPlatform("user")
	fs := &fakeSender{fetchDefault: fetch}
	p.conn = fs
	h, got := collector()
	p.handler = h
	return p, got, fs
}

func TestDispatch_PersonalFileAttachmentDelivered(t *testing.T) {
	p, got, fs := personalPlatform(fetchResult{data: []byte("DOCX-BYTES"), outcome: fetchOK})
	p.dispatch(nil, personalActivity("", fileDL("report.docx", "https://files.example/dl")))

	if len(*got) != 1 {
		t.Fatalf("file-only 1:1 message must still dispatch, got %d", len(*got))
	}
	m := (*got)[0]
	if len(m.Files) != 1 || len(m.Images) != 0 {
		t.Fatalf("want 1 file, 0 images; got files=%d images=%d", len(m.Files), len(m.Images))
	}
	f := m.Files[0]
	if f.FileName != "report.docx" || string(f.Data) != "DOCX-BYTES" || f.MimeType == "" {
		t.Errorf("file attachment = %+v", f)
	}
	// A pre-authed file downloadUrl must NOT carry the bot token.
	if len(fs.fetchwithToken) != 1 || fs.fetchwithToken[0] {
		t.Errorf("file download must not attach the bot token, withToken=%v", fs.fetchwithToken)
	}
}

func TestDispatch_PersonalImageDelivered(t *testing.T) {
	p, got, fs := personalPlatform(fetchResult{data: []byte("PNG"), outcome: fetchOK})
	p.dispatch(nil, personalActivity("", inboundAttachment{ContentType: "image/png", ContentURL: "https://smba.example/v3/attachments/x"}))

	if len(*got) != 1 {
		t.Fatalf("image 1:1 message must dispatch, got %d", len(*got))
	}
	m := (*got)[0]
	if len(m.Images) != 1 || len(m.Files) != 0 {
		t.Fatalf("want 1 image, 0 files; got images=%d files=%d", len(m.Images), len(m.Files))
	}
	if m.Images[0].MimeType != "image/png" || string(m.Images[0].Data) != "PNG" {
		t.Errorf("image attachment = %+v", m.Images[0])
	}
	// contentUrl host == serviceURL host -> the bot token is attached.
	if len(fs.fetchwithToken) != 1 || !fs.fetchwithToken[0] {
		t.Errorf("same-host image should carry the bot token, withToken=%v", fs.fetchwithToken)
	}
}

func TestDispatch_ForeignHostImageOmitsToken(t *testing.T) {
	p, _, fs := personalPlatform(fetchResult{data: []byte("PNG"), outcome: fetchOK})
	p.dispatch(nil, personalActivity("", inboundAttachment{ContentType: "image/jpeg", ContentURL: "https://cdn.foreign.example/img.jpg"}))
	if len(fs.fetchwithToken) != 1 || fs.fetchwithToken[0] {
		t.Errorf("a foreign-host image URL must NOT receive the bot token, withToken=%v", fs.fetchwithToken)
	}
}

func TestDispatch_TextAndAttachmentSameTurn(t *testing.T) {
	p, got, _ := personalPlatform(fetchResult{data: []byte("X"), outcome: fetchOK})
	p.dispatch(nil, personalActivity("review this", fileDL("a.docx", "https://files.example/dl")))
	if len(*got) != 1 {
		t.Fatalf("got %d messages", len(*got))
	}
	m := (*got)[0]
	if m.Content != "review this" || len(m.Files) != 1 {
		t.Errorf("text+attachment should share one turn: content=%q files=%d", m.Content, len(m.Files))
	}
}

func TestDispatch_ChannelAttachmentIgnored(t *testing.T) {
	p := teamsPlatform("thread")
	fs := &fakeSender{fetchDefault: fetchResult{data: []byte("X"), outcome: fetchOK}}
	p.conn = fs
	h, got := collector()
	p.handler = h

	// Channel message that @mentions the bot (so it passes the engagement gate)
	// and carries a file attachment. The attachment must be ignored (no Graph),
	// and the text path proceeds unchanged.
	a := activity{
		Type:         "message",
		ID:           "act-1",
		Text:         "<at>bot</at> look",
		ServiceURL:   "https://smba.example/",
		From:         channelAccount{ID: "user-1"},
		Recipient:    channelAccount{ID: "bot-1"},
		Conversation: conversationAccount{ID: "conv-A", ConversationType: "channel"},
		Entities:     []entity{{Type: "mention", Text: "<at>bot</at>", Mentioned: channelAccount{ID: "bot-1"}}},
		Attachments:  []inboundAttachment{fileDL("secret.docx", "https://files.example/dl")},
	}
	p.dispatch(nil, mustJSON(a))

	if len(*got) != 1 {
		t.Fatalf("channel text should still dispatch, got %d", len(*got))
	}
	if len((*got)[0].Files) != 0 || len((*got)[0].Images) != 0 {
		t.Errorf("channel attachment must be ignored, got files=%d images=%d", len((*got)[0].Files), len((*got)[0].Images))
	}
	if len(fs.fetchedURLs) != 0 {
		t.Errorf("channel attachment must not trigger a download, fetched=%v", fs.fetchedURLs)
	}
}

func TestDispatch_NoticeOnFailedDownload(t *testing.T) {
	p, got, fs := personalPlatform(fetchResult{outcome: fetchFailed})
	p.dispatch(nil, personalActivity("review this", fileDL("a.docx", "https://files.example/dl")))

	if len(*got) != 1 {
		t.Fatalf("turn should still dispatch its text, got %d", len(*got))
	}
	if (*got)[0].Content != "review this" {
		t.Errorf("text should survive a failed attachment, content=%q", (*got)[0].Content)
	}
	if len((*got)[0].Files) != 0 {
		t.Errorf("failed download must not attach a file, got %d", len((*got)[0].Files))
	}
	if len(fs.replied) != 1 || !strings.Contains(fs.replied[0].Text, "couldn't read") {
		t.Errorf("a failed download should send a user notice, replied=%+v", fs.replied)
	}
}

func TestDispatch_NoticeOnOversizeDownload(t *testing.T) {
	p, _, fs := personalPlatform(fetchResult{outcome: fetchOversize})
	p.dispatch(nil, personalActivity("", fileDL("big.docx", "https://files.example/dl")))
	if len(fs.replied) != 1 {
		t.Fatalf("an oversize download should send a user notice, replied=%d", len(fs.replied))
	}
}

func TestDispatch_MalformedFileAttachmentNotifies(t *testing.T) {
	// A file-download attachment with an empty downloadUrl can't be fetched. It
	// must be counted as a failure (notice sent), not silently dropped into an
	// empty turn.
	p, got, fs := personalPlatform(fetchResult{outcome: fetchOK})
	bad := inboundAttachment{ContentType: fileDownloadInfoContentType, Name: "x.docx", Content: []byte(`{"downloadUrl":""}`)}
	p.dispatch(nil, personalActivity("", bad))

	if len(fs.fetchedURLs) != 0 {
		t.Errorf("an empty downloadUrl must not be fetched, got %v", fs.fetchedURLs)
	}
	if len(fs.replied) != 1 {
		t.Fatalf("a malformed file attachment should send a notice, replied=%d", len(fs.replied))
	}
	if len(*got) != 1 || len((*got)[0].Files) != 0 {
		t.Errorf("no file should be attached for a malformed attachment")
	}
}

func TestDispatch_UnhandledOnlyAttachmentNotDispatched(t *testing.T) {
	// A 1:1 message with empty text whose only attachment is neither a file
	// download nor an image (e.g. a link preview) must NOT dispatch an empty turn.
	p, got, fs := personalPlatform(fetchResult{outcome: fetchOK})
	p.dispatch(nil, personalActivity("", inboundAttachment{ContentType: "application/vnd.microsoft.card.thumbnail"}))

	if len(*got) != 0 {
		t.Fatalf("empty-text message with only an unhandled attachment must be dropped, got %d", len(*got))
	}
	if len(fs.fetchedURLs) != 0 {
		t.Errorf("no download should be attempted for an unhandled attachment, got %v", fs.fetchedURLs)
	}
}

func TestDispatch_NoNoticeOnSuccess(t *testing.T) {
	p, got, fs := personalPlatform(fetchResult{data: []byte("OK"), outcome: fetchOK})
	p.dispatch(nil, personalActivity("", fileDL("a.docx", "https://files.example/dl")))
	if len((*got)[0].Files) != 1 {
		t.Fatalf("successful download should attach the file")
	}
	if len(fs.replied) != 0 {
		t.Errorf("a successful download must not send a notice, replied=%+v", fs.replied)
	}
}

// TestSessionKey_ScopeVariants covers AE8: the three scopes produce distinct
// keys, and channel scope collapses sibling threads to the channel root.
func TestSessionKey_ScopeVariants(t *testing.T) {
	const thread = "19:abc@thread.tacv2;messageid=1700000000000"
	const root = "19:abc@thread.tacv2"
	a := &activity{From: channelAccount{ID: "user-1"}, Conversation: conversationAccount{ID: thread}}

	if got := teamsPlatform("thread").sessionKey(a); got != "teams:"+thread {
		t.Errorf("thread scope = %q, want teams:%s", got, thread)
	}
	if got := teamsPlatform("channel").sessionKey(a); got != "teams:"+root {
		t.Errorf("channel scope = %q, want teams:%s (messageid stripped)", got, root)
	}
	if got := teamsPlatform("user").sessionKey(a); got != "teams:"+thread+":user-1" {
		t.Errorf("user scope = %q, want per-user key", got)
	}

	// Two reply threads of one channel collapse to the same channel-scope key.
	sibling := &activity{From: channelAccount{ID: "user-1"}, Conversation: conversationAccount{ID: "19:abc@thread.tacv2;messageid=999"}}
	if teamsPlatform("channel").sessionKey(a) != teamsPlatform("channel").sessionKey(sibling) {
		t.Error("channel scope should collapse sibling threads to one key")
	}
	// A 1:1 id has no ;messageid= suffix, so channel scope leaves it unchanged.
	dm := &activity{From: channelAccount{ID: "u"}, Conversation: conversationAccount{ID: "a:1to1"}}
	if got := teamsPlatform("channel").sessionKey(dm); got != "teams:a:1to1" {
		t.Errorf("channel scope on a suffixless id = %q, want unchanged", got)
	}
}

// messageMentioning builds a channel message that @mentions the given ids (by
// Mentioned.ID) without mentioning the bot unless bot-1 is in the list.
func messageMentioning(conv, from string, mentionedIDs ...string) []byte {
	a := activity{
		Type:         "message",
		ID:           "act-m",
		Text:         "mentioning others",
		ServiceURL:   "https://smba.example/",
		From:         channelAccount{ID: from},
		Recipient:    channelAccount{ID: "bot-1"},
		Conversation: conversationAccount{ID: conv},
	}
	for _, id := range mentionedIDs {
		a.Entities = append(a.Entities, entity{Type: "mention", Text: "<at>x</at>", Mentioned: channelAccount{ID: id}})
	}
	return mustJSON(a)
}

// TestDispatch_IgnoresOtherMentionInEngagedThread covers AE2 (R7 filter): once a
// thread is engaged, a message @mentioning only other people is human-to-human
// side chatter and is ignored, while a message mentioning others AND the bot is
// still forwarded.
func TestDispatch_IgnoresOtherMentionInEngagedThread(t *testing.T) {
	p := teamsPlatform("thread")
	h, got := collector()
	p.handler = h

	p.dispatch(nil, messageActivity("conv-A", "user-1", "hey bot", true)) // engage
	if len(*got) != 1 {
		t.Fatalf("mention should engage and forward, got %d", len(*got))
	}
	// Mentions user-2 only (not the bot) -> ignored despite the thread being engaged.
	p.dispatch(nil, messageMentioning("conv-A", "user-1", "user-2"))
	if len(*got) != 1 {
		t.Fatalf("message mentioning only another user must be ignored, got %d", len(*got))
	}
	// Mentions user-2 AND the bot -> forwarded (enters the session).
	p.dispatch(nil, messageMentioning("conv-A", "user-1", "user-2", "bot-1"))
	if len(*got) != 2 {
		t.Fatalf("message mentioning another user and the bot must be forwarded, got %d", len(*got))
	}
}

// TestDispatch_SelfMessageIgnored covers the R7 self-message guard: an activity
// authored by the bot itself never triggers a turn, even in an engaged thread.
func TestDispatch_SelfMessageIgnored(t *testing.T) {
	p := teamsPlatform("thread")
	h, got := collector()
	p.handler = h

	p.dispatch(nil, messageActivity("conv-A", "user-1", "hey bot", true)) // engage
	self := activity{
		Type:         "message",
		ID:           "act-self",
		Text:         "echo",
		ServiceURL:   "https://smba.example/",
		From:         channelAccount{ID: "28:bot-1"}, // a bot's channelAccount ID is the "28:<appId>" form
		Recipient:    channelAccount{ID: "28:bot-1"},
		Conversation: conversationAccount{ID: "conv-A"},
	}
	p.dispatch(nil, mustJSON(self))
	if len(*got) != 1 {
		t.Fatalf("bot self-message must be ignored, got %d", len(*got))
	}
}

func TestDispatch_CardActionMapsToResolverInput(t *testing.T) {
	for _, tc := range []struct{ action, want string }{
		{"perm:allow", "allow"},
		{"perm:deny", "deny"},
		{"perm:allow_all", "allow all"}, // must NOT collapse to a one-time allow
		{"askq:0:1", "askq:0:1"},        // AskUserQuestion answer forwarded verbatim
	} {
		t.Run(tc.action, func(t *testing.T) {
			p := teamsPlatform("user")
			h, got := collector()
			p.handler = h
			a := activity{
				Type:         "message",
				ID:           "act-1",
				ServiceURL:   "https://smba.example/",
				From:         channelAccount{ID: "user-1"},
				Recipient:    channelAccount{ID: "bot-1"},
				Conversation: conversationAccount{ID: "dm-1", ConversationType: "personal"},
				Value:        json.RawMessage(`{"action":"` + tc.action + `"}`),
			}
			p.dispatch(nil, mustJSON(a))
			if len(*got) != 1 {
				t.Fatalf("card action should dispatch one message, got %d", len(*got))
			}
			m := (*got)[0]
			if m.Content != tc.want {
				t.Errorf("content = %q, want %q", m.Content, tc.want)
			}
			if !m.IsPermissionResponse {
				t.Error("card action must set IsPermissionResponse")
			}
		})
	}
}

func TestDispatch_MalformedCardValueFallsThroughToText(t *testing.T) {
	p := teamsPlatform("user")
	h, got := collector()
	p.handler = h
	a := activity{
		Type:         "message",
		ID:           "act-1",
		Text:         "hello",
		ServiceURL:   "https://smba.example/",
		From:         channelAccount{ID: "user-1"},
		Recipient:    channelAccount{ID: "bot-1"},
		Conversation: conversationAccount{ID: "dm-1", ConversationType: "personal"},
		Value:        json.RawMessage(`{"foo":"bar"}`), // no action key -> not a card action
	}
	p.dispatch(nil, mustJSON(a))
	if len(*got) != 1 {
		t.Fatalf("text with a non-action value should dispatch as text, got %d", len(*got))
	}
	if m := (*got)[0]; m.Content != "hello" || m.IsPermissionResponse {
		t.Errorf("expected plain text dispatch, got content=%q isPerm=%v", m.Content, m.IsPermissionResponse)
	}
}
