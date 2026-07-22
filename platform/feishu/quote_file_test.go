package feishu

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// TestFilterQuotedFilesForUser covers the two gating rules for issue #1560
// in isolation: only @-bot-triggers forward files, and only the same IM user
// is allowed to forward them.
func TestFilterQuotedFilesForUser(t *testing.T) {
	const botOpenID = "ou_bot"
	const currentUser = "ou_alice"
	const otherUser = "ou_bob"

	buildMetas := func() []quotedFileMeta {
		return []quotedFileMeta{
			{fileKey: "k1", fileName: "doc.txt", messageID: "om1", senderID: currentUser},
			{fileKey: "k2", fileName: "doc2.txt", messageID: "om2", senderID: otherUser},
		}
	}
	botMention := []*larkim.MentionEvent{
		{Key: strPtr("@bot"), Id: &larkim.UserId{OpenId: strPtr(botOpenID)}, Name: strPtr("Bot")},
	}

	tests := []struct {
		name     string
		mentions []*larkim.MentionEvent
		userID   string
		metas    []quotedFileMeta
		wantLen  int
		wantFile string // if wantLen==1, expected fileName
	}{
		{
			name:     "bot mentioned and same-user file is kept",
			mentions: botMention,
			userID:   currentUser,
			metas:    buildMetas(),
			wantLen:  1,
			wantFile: "doc.txt",
		},
		{
			name:     "bot mentioned but no metas returns empty",
			mentions: botMention,
			userID:   currentUser,
			metas:    nil,
			wantLen:  0,
		},
		{
			name:     "quote without bot mention returns empty (privacy gate)",
			mentions: nil, // no @bot
			userID:   currentUser,
			metas:    buildMetas(),
			wantLen:  0,
		},
		{
			name:     "empty user id returns empty (defensive)",
			mentions: botMention,
			userID:   "",
			metas:    buildMetas(),
			wantLen:  0,
		},
		{
			name:     "bot mentioned but files belong to other user returns empty",
			mentions: botMention,
			userID:   currentUser,
			metas: []quotedFileMeta{
				{fileKey: "sk", fileName: "secret.txt", messageID: "om_s", senderID: otherUser},
			},
			wantLen: 0,
		},
		{
			name:     "bot mentioned but multiple files include foreign users drops the foreign ones",
			mentions: botMention,
			userID:   currentUser,
			metas: []quotedFileMeta{
				{fileKey: "k_mine", fileName: "mine.txt", messageID: "om_m", senderID: currentUser},
				{fileKey: "k_theirs", fileName: "theirs.txt", messageID: "om_t", senderID: otherUser},
			},
			wantLen:  1,
			wantFile: "mine.txt",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &Platform{platformName: "feishu", botOpenID: botOpenID}
			got := p.filterQuotedFilesForUser(tc.metas, tc.mentions, tc.userID)
			if len(got) != tc.wantLen {
				t.Fatalf("len = %d, want %d", len(got), tc.wantLen)
			}
			if tc.wantLen == 1 && got[0].fileName != tc.wantFile {
				t.Fatalf("fileName = %q, want %q", got[0].fileName, tc.wantFile)
			}
		})
	}
}

// TestDispatchMessageQuotedFileAcceptance verifies the three scenarios from
// the issue #1560 acceptance criteria:
//  1. quote + @bot mention -> on-demand file fetch + attachment forward
//  2. quote without @bot   -> no fetch (file resource endpoint not called)
//  3. ordinary message     -> no fetch
//
// The mock httptest server records all incoming paths so we can assert that
// the resource endpoint is hit exactly once across the three cases.
func TestDispatchMessageQuotedFileAcceptance(t *testing.T) {
	const appID = "cli_quote_file"
	const appSecret = "secret-quote-file"
	const botOpenID = "ou_bot"
	const currentUser = "ou_alice"
	const parentMessageID = "om_parent_file"
	const fileKey = "file_v1"
	const fileName = "report.txt"

	// Fake file payload. Bytes happen to start with "%PDF" so detectMimeType
	// classifies it as application/pdf — works for both build and runtime.
	fileData := []byte("%PDF-1.4\nfake pdf body\n")

	type hit struct {
		path string
		q    string
	}
	hits := make(chan hit, 32)

	// servedOnce tracks whether the file resource endpoint was ever hit.
	// The test asserts this flag matches the scenario expectations.
	var fileResourceCalls int
	// Endpoint at which we expect to see the file resource call.
	const fileResourcePath = "/open-apis/im/v1/messages/" + parentMessageID + "/resources/" + fileKey

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits <- hit{path: r.URL.Path, q: r.URL.Query().Get("type")}
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			w.Header().Set("Content-Type", "application/json")
			writeJSON(t, w, map[string]any{
				"code":                0,
				"msg":                 "success",
				"expire":              7200,
				"tenant_access_token": "tenant-token",
			})
		case r.URL.Path == "/open-apis/im/v1/messages/"+parentMessageID:
			w.Header().Set("Content-Type", "application/json")
			// Return a quoted file message whose sender matches the current
			// user. The acceptance flow is "user A uploads, user A re-quotes
			// with @bot" — same-user privacy rule must allow it through.
			writeJSON(t, w, map[string]any{
				"code": 0,
				"msg":  "success",
				"data": map[string]any{
					"items": []map[string]any{
						{
							"msg_type":  "file",
							"parent_id": "",
							"sender": map[string]any{
								"id":          currentUser,
								"sender_type": "user",
							},
							"body": map[string]any{
								"content": `{"file_key":"` + fileKey + `","file_name":"` + fileName + `"}`,
							},
						},
					},
				},
			})
		case strings.HasSuffix(r.URL.Path, "/contact/v3/users/"+currentUser):
			w.Header().Set("Content-Type", "application/json")
			writeJSON(t, w, map[string]any{
				"code": 0,
				"msg":  "success",
				"data": map[string]any{
					"user": map[string]any{
						"name": "Alice",
					},
				},
			})
		case strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/chats/"):
			w.Header().Set("Content-Type", "application/json")
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success"})
		case r.URL.Path == fileResourcePath:
			fileResourceCalls++
			if r.URL.Query().Get("type") != "file" {
				t.Errorf("file resource call type = %q, want file", r.URL.Query().Get("type"))
			}
			w.Header().Set("Content-Type", "application/pdf")
			if _, err := w.Write(fileData); err != nil {
				t.Fatalf("write file: %v", err)
			}
		default:
			// Unexpected path: count but don't fail the test here — the
			// per-scenario assertions below cover expected behaviour.
			t.Logf("unexpected mock path: %s", r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success"})
		}
	}))
	defer srv.Close()
	// Drain hits asynchronously so the handler never blocks.
	go func() {
		for range hits {
		}
	}()

	newClient := func(handler func(_ core.Platform, msg *core.Message)) *Platform {
		return &Platform{
			platformName: "feishu",
			domain:       srv.URL,
			appID:        appID,
			appSecret:    appSecret,
			botOpenID:    botOpenID,
			handler:      handler,
			client: lark.NewClient(appID, appSecret,
				lark.WithOpenBaseUrl(srv.URL),
				lark.WithHttpClient(srv.Client()),
			),
		}
	}

	botMentionEvents := []*larkim.MentionEvent{
		{Key: strPtr("@bot"), Id: &larkim.UserId{OpenId: strPtr(botOpenID)}, Name: strPtr("Bot")},
	}

	type scenario struct {
		name             string
		parentID         string
		mentions         []*larkim.MentionEvent
		expectFiles      int
		expectFileNameIs string // matcher: "report.txt" or "" (none)
	}

	scenarios := []scenario{
		{
			name:             "mention+quote fetches and forwards file",
			parentID:         parentMessageID,
			mentions:         botMentionEvents,
			expectFiles:      1,
			expectFileNameIs: fileName,
		},
		{
			name:             "quote without mention does not fetch",
			parentID:         parentMessageID,
			mentions:         nil, // no @bot
			expectFiles:      0,
			expectFileNameIs: "",
		},
		{
			name:             "ordinary message does not fetch",
			parentID:         "", // no quote at all
			mentions:         botMentionEvents,
			expectFiles:      0,
			expectFileNameIs: "",
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			// Reset the per-scenario counter: the acceptance rules need to
			// be matched across the scenarios as a set.
			before := fileResourceCalls

			got := make(chan *core.Message, 1)
			p := newClient(func(_ core.Platform, msg *core.Message) {
				got <- msg
			})

			p.dispatchMessage(
				context.Background(),
				"text",
				`{"text":"@bot 请帮我分析文件"}`,
				sc.mentions,
				"om_child_"+sc.name,
				"feishu:oc_chat:ou_alice",
				currentUser,
				"oc_chat",
				replyContext{messageID: "om_child_" + sc.name, sessionKey: "feishu:oc_chat:ou_alice"},
				sc.parentID,
				0,
			)

			select {
			case msg := <-got:
				if len(msg.Files) != sc.expectFiles {
					t.Fatalf("len(Files) = %d, want %d (Files=%+v)", len(msg.Files), sc.expectFiles, msg.Files)
				}
				if sc.expectFiles == 1 && msg.Files[0].FileName != sc.expectFileNameIs {
					t.Fatalf("Files[0].FileName = %q, want %q", msg.Files[0].FileName, sc.expectFileNameIs)
				}
				if sc.expectFiles == 1 && string(msg.Files[0].Data) != string(fileData) {
					t.Fatalf("Files[0].Data bytes differ from mocked file body")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("timed out waiting for dispatched message")
			}

			after := fileResourceCalls
			scenariosCalled := after - before
			// Per-scenario assertion: the file resource endpoint must only
			// be hit when the scenario expects it. This is the strongest
			// proof of "on-demand only" — the server-side counter is the
			// ground truth.
			wantCalls := 0
			if sc.expectFiles > 0 {
				wantCalls = 1
			}
			if scenariosCalled != wantCalls {
				t.Fatalf("scenario %q: file resource calls = %d, want %d", sc.name, scenariosCalled, wantCalls)
			}
		})
	}
}

// TestDispatchMessageQuotedFileForeignUserDropped verifies the same-user
// privacy guard: when a quoted file was uploaded by a different IM user,
// the file MUST be forwarded only as a [file] marker — the binary payload
// is not attached to the dispatched core.Message.
func TestDispatchMessageQuotedFileForeignUserDropped(t *testing.T) {
	const appID = "cli_quote_file_foreign"
	const appSecret = "secret-quote-file-foreign"
	const botOpenID = "ou_bot"
	const currentUser = "ou_alice"
	const foreignUser = "ou_bob"
	const parentMessageID = "om_parent_foreign"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			w.Header().Set("Content-Type", "application/json")
			writeJSON(t, w, map[string]any{
				"code":                0,
				"msg":                 "success",
				"expire":              7200,
				"tenant_access_token": "tenant-token",
			})
		case r.URL.Path == "/open-apis/im/v1/messages/"+parentMessageID:
			w.Header().Set("Content-Type", "application/json")
			// Quoted file is uploaded by `foreignUser`, NOT by currentUser.
			writeJSON(t, w, map[string]any{
				"code": 0, "msg": "success",
				"data": map[string]any{
					"items": []map[string]any{
						{
							"msg_type":  "file",
							"parent_id": "",
							"sender": map[string]any{
								"id":          foreignUser,
								"sender_type": "user",
							},
							"body": map[string]any{
								"content": `{"file_key":"file_foreign","file_name":"secret.txt"}`,
							},
						},
					},
				},
			})
		case strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/messages/"+parentMessageID+"/resources/"):
			// Even if the resource API succeeds, the same-user guard must
			// keep the file out of the dispatched payload. We return the
			// bytes anyway; the assertion is purely about the *outcome*.
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("should not be forwarded"))
		case strings.HasPrefix(r.URL.Path, "/open-apis/contact/v3/users/"):
			w.Header().Set("Content-Type", "application/json")
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success"})
		case strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/chats/"):
			w.Header().Set("Content-Type", "application/json")
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	got := make(chan *core.Message, 1)
	p := &Platform{
		platformName: "feishu",
		domain:       srv.URL,
		appID:        appID,
		appSecret:    appSecret,
		botOpenID:    botOpenID,
		client: lark.NewClient(appID, appSecret,
			lark.WithOpenBaseUrl(srv.URL),
			lark.WithHttpClient(srv.Client()),
		),
		handler: func(_ core.Platform, msg *core.Message) { got <- msg },
	}

	p.dispatchMessage(
		context.Background(),
		"text",
		`{"text":"@bot 看看这个文件"}`,
		[]*larkim.MentionEvent{
			{Key: strPtr("@bot"), Id: &larkim.UserId{OpenId: strPtr(botOpenID)}, Name: strPtr("Bot")},
		},
		"om_child_foreign",
		"feishu:oc_chat:ou_alice",
		currentUser,
		"oc_chat",
		replyContext{messageID: "om_child_foreign", sessionKey: "feishu:oc_chat:ou_alice"},
		parentMessageID,
		0,
	)

	select {
	case msg := <-got:
		if len(msg.Files) != 0 {
			t.Fatalf("foreign-user quoted file was forwarded (len(Files)=%d) — same-user guard broken", len(msg.Files))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for dispatched foreign-user message")
	}
}
