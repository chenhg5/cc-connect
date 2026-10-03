package teams

import (
	"encoding/json"
	"testing"
)

func TestCleanText_StripsMention(t *testing.T) {
	a := &activity{
		Text: "<at>mybot</at> hello there",
		Entities: []entity{
			{Type: "mention", Text: "<at>mybot</at>", Mentioned: channelAccount{ID: "bot-1"}},
		},
	}
	if got := a.cleanText(); got != "hello there" {
		t.Fatalf("cleanText = %q, want %q", got, "hello there")
	}
}

func TestMentionsBot(t *testing.T) {
	a := &activity{
		Recipient: channelAccount{ID: "bot-1"},
		Entities: []entity{
			{Type: "mention", Mentioned: channelAccount{ID: "bot-1"}},
		},
	}
	if !a.mentionsBot("bot-1") {
		t.Error("expected mentionsBot true when bot id mentioned")
	}

	// Recipient fallback: a mention whose Mentioned.ID matches the activity
	// recipient counts even if the passed botID differs.
	if !a.mentionsBot("some-other-id") {
		t.Error("expected mentionsBot true via recipient fallback")
	}

	// No recipient, mention is another user → not a bot mention.
	b := &activity{Entities: []entity{{Type: "mention", Mentioned: channelAccount{ID: "someone"}}}}
	if b.mentionsBot("bot-1") {
		t.Error("expected mentionsBot false when only another user is mentioned")
	}
}

func TestCleanText_MultipleMentionsAndNonMention(t *testing.T) {
	a := &activity{
		Text: "<at>bot</at> ping <at>alice</at> please",
		Entities: []entity{
			{Type: "mention", Text: "<at>bot</at>", Mentioned: channelAccount{ID: "bot-1"}},
			{Type: "mention", Text: "<at>alice</at>", Mentioned: channelAccount{ID: "alice"}},
			{Type: "clientInfo"}, // non-mention entity must be ignored, not panic
		},
	}
	if got := a.cleanText(); got != "ping  please" {
		t.Fatalf("cleanText = %q, want %q", got, "ping  please")
	}
}

func TestCardAction_Variants(t *testing.T) {
	cases := map[string]string{
		`{"action":"act:/x"}`: "act:/x",
		`{"cmd":"pause"}`:     "pause",
		`{"action":123}`:      "", // non-string value ignored
		`{"action":{}}`:       "",
		`not json`:            "",
		`{}`:                  "",
	}
	for raw, want := range cases {
		a := &activity{Value: []byte(raw)}
		if got := a.cardAction(); got != want {
			t.Errorf("cardAction(%s) = %q, want %q", raw, got, want)
		}
	}
}

func TestCardAction(t *testing.T) {
	a := &activity{Value: []byte(`{"action":"act:/heartbeat pause"}`)}
	if got := a.cardAction(); got != "act:/heartbeat pause" {
		t.Fatalf("cardAction = %q", got)
	}
	none := &activity{}
	if got := none.cardAction(); got != "" {
		t.Fatalf("cardAction = %q, want empty", got)
	}
}

func TestActivityAttachments_ParseFileDownloadInfo(t *testing.T) {
	body := []byte(`{
		"type":"message",
		"conversation":{"conversationType":"personal"},
		"attachments":[{
			"contentType":"application/vnd.microsoft.teams.file.download.info",
			"name":"report.docx",
			"content":{"downloadUrl":"https://onedrive.example/pre-authed","fileType":"docx","uniqueId":"u-1"}
		}]
	}`)
	a, err := parseActivity(body)
	if err != nil {
		t.Fatalf("parseActivity: %v", err)
	}
	if len(a.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(a.Attachments))
	}
	att := a.Attachments[0]
	if !att.isFileDownload() {
		t.Error("expected isFileDownload true")
	}
	if att.Name != "report.docx" {
		t.Errorf("name = %q", att.Name)
	}
	info, ok := att.downloadInfo()
	if !ok {
		t.Fatal("downloadInfo not parsed")
	}
	if info.DownloadURL != "https://onedrive.example/pre-authed" || info.FileType != "docx" {
		t.Errorf("downloadInfo = %+v", info)
	}
}

func TestActivityAttachments_ParseImage(t *testing.T) {
	body := []byte(`{
		"type":"message",
		"attachments":[{"contentType":"image/png","contentUrl":"https://smba.example/v3/attachments/x"}]
	}`)
	a, err := parseActivity(body)
	if err != nil {
		t.Fatalf("parseActivity: %v", err)
	}
	att := a.Attachments[0]
	if !att.isImage() {
		t.Error("expected isImage true for image/png")
	}
	if att.isFileDownload() {
		t.Error("image must not classify as file download")
	}
	if att.ContentURL != "https://smba.example/v3/attachments/x" {
		t.Errorf("contentUrl = %q", att.ContentURL)
	}
	if _, ok := att.downloadInfo(); ok {
		t.Error("downloadInfo must be false for a non-file attachment")
	}
}

func TestActivityAttachments_NoneWhenAbsent(t *testing.T) {
	a, err := parseActivity([]byte(`{"type":"message","text":"hi"}`))
	if err != nil {
		t.Fatalf("parseActivity: %v", err)
	}
	if len(a.Attachments) != 0 {
		t.Errorf("attachments = %d, want 0", len(a.Attachments))
	}
}

func TestActivityAttachments_CorruptFileContentSkips(t *testing.T) {
	att := inboundAttachment{
		ContentType: fileDownloadInfoContentType,
		Content:     json.RawMessage(`not-json`),
	}
	if _, ok := att.downloadInfo(); ok {
		t.Error("corrupt file content should not parse")
	}
}

func TestActivity_IsPersonal(t *testing.T) {
	cases := map[string]bool{
		"personal":  true,
		"Personal":  true,
		"channel":   false,
		"groupChat": false,
		"":          false,
	}
	for convType, want := range cases {
		a := &activity{Conversation: conversationAccount{ConversationType: convType}}
		if got := a.isPersonal(); got != want {
			t.Errorf("isPersonal(%q) = %v, want %v", convType, got, want)
		}
	}
}

func TestSessionKey_Scopes(t *testing.T) {
	a := &activity{Conversation: conversationAccount{ID: "conv-9"}, From: channelAccount{ID: "user-7"}}
	cases := map[string]string{
		"user":    "teams:conv-9:user-7",
		"thread":  "teams:conv-9",
		"channel": "teams:conv-9",
	}
	for scope, want := range cases {
		p := &Platform{cfg: config{sessionScope: scope}}
		if got := p.sessionKey(a); got != want {
			t.Errorf("scope %q: sessionKey = %q, want %q", scope, got, want)
		}
	}
}
