package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

type panelRequest struct{ method, path, content string }

func panelFixture(t *testing.T) (*interactivePlatform, <-chan panelRequest, *atomic.Bool) {
	t.Helper()
	requests := make(chan panelRequest, 32)
	failPatch := &atomic.Bool{}
	var schemaMu sync.Mutex
	schema2 := make(map[string]bool)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			fmt.Fprint(w, `{"code":0,"tenant_access_token":"test-token","expire":7200}`)
			return
		}
		var body struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requests <- panelRequest{r.Method, r.URL.Path, body.Content}
		if r.Method == http.MethodPatch && failPatch.Load() {
			fmt.Fprint(w, `{"code":230011,"msg":"message cannot be updated"}`)
			return
		}
		// Feishu permits upgrading an old message to 2.0, but rejects returning
		// from a 2.0 form to a 1.0 menu/result. Model that API constraint.
		var card map[string]any
		_ = json.Unmarshal([]byte(body.Content), &card)
		id := "om_panel"
		if r.Method == http.MethodPatch {
			id = filepath.Base(r.URL.Path)
		}
		schemaMu.Lock()
		downgrade := r.Method == http.MethodPatch && schema2[id] && card["schema"] != "2.0"
		if !downgrade {
			schema2[id] = card["schema"] == "2.0"
		}
		schemaMu.Unlock()
		if downgrade {
			fmt.Fprint(w, `{"code":230099,"msg":"ErrCode: 200830; schemaV2 card can not change schemaV1"}`)
			return
		}
		fmt.Fprint(w, `{"code":0,"msg":"success","data":{"message_id":"om_panel"}}`)
	}))
	t.Cleanup(server.Close)
	p, err := New(map[string]any{"app_id": "cli_" + t.Name(), "app_secret": "test", "enable_feishu_card": true})
	if err != nil {
		t.Fatal(err)
	}
	ip := p.(*interactivePlatform)
	ip.client = lark.NewClient(ip.appID, "test", lark.WithOpenBaseUrl(server.URL), lark.WithHttpClient(server.Client()))
	ip.userNameCache.Store("owner", "Owner")
	ip.chatNameCache.Store("oc_private", "Private")
	return ip, requests, failPatch
}

func awaitPanel(t *testing.T, requests <-chan panelRequest, method, id, want string) panelRequest {
	t.Helper()
	select {
	case got := <-requests:
		if got.method != method || (id != "" && !strings.HasSuffix(got.path, "/"+id)) || !strings.Contains(got.content, want) {
			t.Fatalf("unexpected panel request: %+v; want %s %s %q", got, method, id, want)
		}
		return got
	case <-time.After(3 * time.Second):
		t.Fatalf("missing panel %s %s %q", method, id, want)
		return panelRequest{}
	}
}

func panelClick(id, action string) *callback.CardActionTriggerEvent {
	return &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: "owner"},
		Context:  &callback.Context{OpenChatID: "oc_private", OpenMessageID: id},
		Action:   &callback.CallBackAction{Value: map[string]any{"action": action, "reuse_card": "true"}},
	}}
}

type panelAgent struct{}

func (*panelAgent) Name() string { return "stub" }
func (*panelAgent) StartSession(context.Context, string) (core.AgentSession, error) {
	return nil, fmt.Errorf("unexpected agent invocation")
}
func (*panelAgent) ListSessions(context.Context) ([]core.AgentSessionInfo, error) { return nil, nil }
func (*panelAgent) Stop() error                                                   { return nil }

// The real engine and Feishu callback/HTTP paths navigate one message through
// Help, roster, form, Back, submission, and a native command response.
func TestCardPanel_HelpMembersFormBackAndSubmitReuseOneMessage(t *testing.T) {
	ip, requests, _ := panelFixture(t)
	e := core.NewEngine("manager", &panelAgent{}, []core.Platform{ip}, filepath.Join(t.TempDir(), "sessions.json"), core.LangEnglish)
	t.Cleanup(func() { e.Stop() })
	e.SetAdminFrom("owner")
	e.SetTeamProjects(core.NewTeamProjectStore(filepath.Join(t.TempDir(), "projects.json")), func(ctx context.Context, p core.Platform, msg *core.Message, args []string) (string, error) {
		if len(args) == 1 {
			return "", p.(core.CardSender).ReplyCard(ctx, msg.ReplyCtx, core.NewCard().Title("Members", "blue").ListItem("Reviewer", "Modify", "cmd:/team members dev").Build())
		}
		if msg.FormValues != nil {
			return "Saved: " + msg.FormValues["role"][0], nil
		}
		card := core.NewCard().Title("Modify bot", "blue").Build()
		card.Form = &core.CardForm{Command: "/team members dev", Language: core.LangEnglish, SubmitText: "Modify", BackAction: "cmd:/team members", Fields: []core.CardFormField{
			{Name: "role", Label: "Role", Kind: "select", Required: true, Options: []core.CardSelectOption{{Text: "Reviewer", Value: "reviewer"}}},
		}}
		return "", p.(core.FormCardSender).ReplyForm(ctx, msg, card)
	})
	e.OnPlatformReady(ip)
	ip.handler = e.ReceiveMessage
	ip.rememberCardChatType(replyContext{chatID: "oc_private", chatType: "p2p", senderType: "user"})
	e.ReceiveMessage(ip, &core.Message{UserID: "owner", SessionKey: "feishu:oc_private:owner", Content: "/help", ReplyCtx: replyContext{chatID: "oc_private", messageID: "om_user", sessionKey: "feishu:oc_private:owner"}})
	first := awaitPanel(t, requests, http.MethodPost, "", "cc-connect")
	if !strings.Contains(first.content, `"update_multi":true`) || !strings.Contains(first.content, `"reuse_card":"true"`) {
		t.Fatal("Help must allow shared message updates and mark its navigation callbacks")
	}
	response, err := ip.onCardAction(panelClick("om_panel", "nav:/help team"))
	if err != nil || response == nil || response.Card == nil {
		t.Fatalf("native Help navigation: %v %#v", err, response)
	}
	assertPanelSchema2(t, response.Card.Data)
	if _, err := ip.onCardAction(panelClick("om_panel", "cmd:/team members")); err != nil {
		t.Fatal(err)
	}
	awaitPanel(t, requests, http.MethodPatch, "om_panel", "Reviewer")
	open := func() string {
		t.Helper()
		if _, err := ip.onCardAction(panelClick("om_panel", "cmd:/team members dev")); err != nil {
			t.Fatal(err)
		}
		got := awaitPanel(t, requests, http.MethodPatch, "om_panel", `"schema":"2.0"`)
		var card map[string]any
		if err := json.Unmarshal([]byte(got.content), &card); err != nil {
			t.Fatal(err)
		}
		body := card["body"].(map[string]any)["elements"].([]any)
		back := body[len(body)-1].(map[string]any)
		value := back["behaviors"].([]any)[0].(map[string]any)["value"].(map[string]any)
		if value["action"] != "cmd:/team members" || value["session_key"] != "feishu:oc_private:owner" || back["form_action_type"] != nil {
			t.Fatal("Back must preserve the session and bypass required input validation")
		}
		return value["form_ref"].(string)
	}
	oldID := open()
	back := panelClick("om_panel", "cmd:/team members")
	back.Event.Action.Value["form_ref"] = oldID
	if _, err := ip.onCardAction(back); err != nil {
		t.Fatal(err)
	}
	returned := awaitPanel(t, requests, http.MethodPatch, "om_panel", "Reviewer")
	if !strings.Contains(returned.content, `"schema":"2.0"`) {
		t.Fatal("Back downgraded the form to schema 1.0, which creates a replacement message")
	}
	response, err = ip.onCardAction(formEvent(oldID, "owner", "oc_private", map[string]any{"role": "reviewer"}))
	if err != nil || response == nil || response.Toast.Type != "error" {
		t.Fatal("left form was still submittable")
	}
	id := open()
	submit := formEvent(id, "owner", "oc_private", map[string]any{"role": "reviewer"})
	submit.Event.Context.OpenMessageID = "om_panel"
	if _, err := ip.onCardAction(submit); err != nil {
		t.Fatal(err)
	}
	awaitPanel(t, requests, http.MethodPatch, "om_panel", "Saved: reviewer")
	if _, err := ip.onCardAction(panelClick("om_panel", "cmd:/shell")); err != nil {
		t.Fatal(err)
	}
	native := awaitPanel(t, requests, http.MethodPatch, "om_panel", "/shell")
	if !strings.Contains(native.content, "nav:/help tools") {
		t.Fatal("native command must return to its Help section")
	}
	// Typing the same command is still an ordinary new reply.
	e.ReceiveMessage(ip, &core.Message{UserID: "owner", SessionKey: "feishu:oc_private:owner", Content: "/shell", ReplyCtx: replyContext{chatID: "oc_private", messageID: "om_user"}})
	awaitPanel(t, requests, http.MethodPost, "", "/shell")
	// Unrelated command buttons retain their native new-message behavior.
	legacy := panelClick("om_other", "cmd:/shell")
	delete(legacy.Event.Action.Value, "reuse_card")
	if _, err := ip.onCardAction(legacy); err != nil {
		t.Fatal(err)
	}
	awaitPanel(t, requests, http.MethodPost, "", "/shell")
}

func TestCardPanel_CapturedMessageAndLateReplies(t *testing.T) {
	ip, requests, _ := panelFixture(t)
	got := make(chan *core.Message, 4)
	ip.handler = func(_ core.Platform, m *core.Message) { got <- m }
	click := func(id string) *core.Message {
		t.Helper()
		if _, err := ip.onCardAction(panelClick(id, "cmd:/team members")); err != nil {
			t.Fatal(err)
		}
		select {
		case msg := <-got:
			return msg
		case <-time.After(time.Second):
			t.Fatal("missing dispatch")
			return nil
		}
	}
	one, two := click("om_one"), click("om_two")
	card := core.NewCard().Title("Members", "blue").Build()
	if err := ip.ReplyCard(context.Background(), one.ReplyCtx, card); err != nil {
		t.Fatal(err)
	}
	awaitPanel(t, requests, http.MethodPatch, "om_one", "Members")
	if err := ip.ReplyCard(context.Background(), two.ReplyCtx, card); err != nil {
		t.Fatal(err)
	}
	awaitPanel(t, requests, http.MethodPatch, "om_two", "Members")
	newer := click("om_one")
	if err := ip.ReplyCard(context.Background(), newer.ReplyCtx, core.NewCard().Markdown("new page").Build()); err != nil {
		t.Fatal(err)
	}
	awaitPanel(t, requests, http.MethodPatch, "om_one", "new page")
	if err := ip.ReplyCard(context.Background(), one.ReplyCtx, card); err != nil {
		t.Fatal(err)
	}
	if err := ip.Reply(context.Background(), one.ReplyCtx, "late text"); err != nil {
		t.Fatal(err)
	}
	if err := ip.Send(context.Background(), one.ReplyCtx, "late send"); err != nil {
		t.Fatal(err)
	}
	if err := ip.SendCard(context.Background(), one.ReplyCtx, card); err != nil {
		t.Fatal(err)
	}
	if err := ip.SendWithStatusFooter(context.Background(), one.ReplyCtx, "late footer", "footer"); err != nil {
		t.Fatal(err)
	}
	select {
	case req := <-requests:
		t.Fatalf("late page was sent: %+v", req)
	default:
	}
}

func TestCardPanel_SendPathsReuseFormMessage(t *testing.T) {
	for _, path := range []string{"send", "send-card", "status-footer", "refresh"} {
		t.Run(path, func(t *testing.T) {
			ip, requests, _ := panelFixture(t)
			ip.noReplyToTrigger = true
			rc := replyContext{chatID: "oc_private", messageID: "om_panel", sessionKey: "feishu:oc_private:owner", cardUpdate: ip.beginCardUpdate("om_panel", "/team members")}
			card := core.NewCard().Title("Edit member", "blue").Build()
			card.Form = &core.CardForm{ID: "form", Command: "/team members dev", SubmitText: "Save", Fields: []core.CardFormField{{Name: "name", Label: "Name"}}}
			if err := ip.ReplyCard(context.Background(), rc, card); err != nil {
				t.Fatal(err)
			}
			awaitPanel(t, requests, http.MethodPatch, "om_panel", `"schema":"2.0"`)
			var err error
			switch path {
			case "send":
				err = ip.Send(context.Background(), rc, "Saved")
			case "send-card":
				err = ip.SendCard(context.Background(), rc, core.NewCard().Markdown("Saved").Build())
			case "status-footer":
				err = ip.SendWithStatusFooter(context.Background(), rc, "Saved", "Footer")
			case "refresh":
				ip.cardActionMsgIDs = map[string]string{rc.sessionKey: "om_panel"}
				err = ip.RefreshCard(context.Background(), rc.sessionKey, core.NewCard().Markdown("Saved").Build())
			}
			if err != nil {
				t.Fatal(err)
			}
			got := awaitPanel(t, requests, http.MethodPatch, "om_panel", "Saved")
			if !strings.Contains(got.content, `"schema":"2.0"`) {
				t.Fatal("result downgraded the form message")
			}
			if path == "status-footer" && !strings.Contains(got.content, "Footer") {
				t.Fatal("lost footer")
			}
		})
	}
}

func TestCardPanel_CallbackAcknowledgementsStaySchema2(t *testing.T) {
	for _, action := range []string{"perm:allow", "askq:0:1", "cmd:/test"} {
		t.Run(action, func(t *testing.T) {
			ip, _, _ := panelFixture(t)
			dispatched := make(chan struct{}, 1)
			ip.handler = func(core.Platform, *core.Message) { dispatched <- struct{}{} }
			event := panelClick("om_panel", action)
			if strings.HasPrefix(action, "cmd:") {
				event.Event.Action.Value["after_click"] = map[string]any{"title": "Saved"}
			}
			response, err := ip.onCardAction(event)
			if err != nil || response == nil || response.Card == nil {
				t.Fatalf("missing acknowledgement: %v %#v", err, response)
			}
			assertPanelSchema2(t, response.Card.Data)
			select {
			case <-dispatched:
			case <-time.After(time.Second):
				t.Fatal("missing command dispatch")
			}
		})
	}
}

func TestCardPanel_UpdateFailureSendsUsableReplacement(t *testing.T) {
	ip, requests, fail := panelFixture(t)
	fail.Store(true)
	rc := replyContext{chatID: "oc_private", messageID: "om_panel", cardUpdate: ip.beginCardUpdate("om_panel", "/team members")}
	if err := ip.ReplyCard(context.Background(), rc, core.NewCard().Title("Members", "blue").Build()); err != nil {
		t.Fatal(err)
	}
	awaitPanel(t, requests, http.MethodPatch, "om_panel", "Members")
	awaitPanel(t, requests, http.MethodPost, "", "nav:/help team")
}

func TestCardPanel_SlowNativeNavigationUpdatesClickedMessage(t *testing.T) {
	ip, requests, _ := panelFixture(t)
	release := make(chan struct{})
	ip.cardNavHandler = func(action, _ string) *core.Card {
		if action == "nav:/slow" {
			<-release
			return core.NewCard().Markdown("slow result").Build()
		}
		return core.NewCard().Markdown("other card").Build()
	}
	response, err := ip.onCardAction(panelClick("om_slow", "nav:/slow"))
	if err != nil || response == nil || response.Toast == nil {
		close(release)
		t.Fatalf("slow navigation should acknowledge before the callback expires: %v %#v", err, response)
	}
	// This overwrites the legacy per-session message pointer while the first
	// page is loading. Its completion must still patch the first card.
	_, err = ip.onCardAction(panelClick("om_other", "nav:/help"))
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	awaitPanel(t, requests, http.MethodPatch, "om_slow", "slow result")
}

func TestCardPanel_RetryErrorPreservesFormAndLocalizedBack(t *testing.T) {
	ip, requests, _ := panelFixture(t)
	ip.SetCardLanguageProvider(func() core.Language { return core.LangChinese })
	rc := replyContext{chatID: "oc_private", messageID: "om_panel", sessionKey: "feishu:oc_private:owner", cardUpdate: ip.beginCardUpdate("om_panel", "/project create")}
	card := core.NewCard().Title("创建项目", "blue").Build()
	card.Form = &core.CardForm{Command: "/project create", Language: core.LangChinese, SubmitText: "创建", BackAction: "nav:/help project", Fields: []core.CardFormField{
		{Name: "project", Label: "项目", Required: true, Defaults: []string{"existing-name"}},
	}}
	msg := &core.Message{UserID: "owner", SessionKey: rc.sessionKey, ReplyCtx: rc}
	if err := ip.ReplyForm(context.Background(), msg, card); err != nil {
		t.Fatal(err)
	}
	awaitPanel(t, requests, http.MethodPatch, "om_panel", "existing-name")
	if err := ip.Reply(context.Background(), rc, "创建失败，请重试"); err != nil {
		t.Fatal(err)
	}
	got := awaitPanel(t, requests, http.MethodPatch, "om_panel", "创建失败，请重试")
	for _, want := range []string{"existing-name", "cc_form_submit_", "← 返回", `"form_action_type":"submit"`} {
		if !strings.Contains(got.content, want) {
			t.Fatalf("retry lost %q: %s", want, got.content)
		}
	}
}

func TestProjectDashboardRefreshUpdatesClickedCardAndReplacesDeletedCard(t *testing.T) {
	ip, requests, failPatch := panelFixture(t)
	store := core.NewTeamProjectStore(filepath.Join(t.TempDir(), "projects.json"))
	if err := store.Put(core.TeamProject{ID: "panel-project", State: "ready", Platform: "feishu", ChatID: "oc_private", Manager: "manager", Creator: "owner", Bots: []string{"manager"}, Directories: []string{t.TempDir()}, Goal: "Current work"}); err != nil {
		t.Fatal(err)
	}
	e := core.NewEngine("manager", &panelAgent{}, []core.Platform{ip}, filepath.Join(t.TempDir(), "sessions.json"), core.LangEnglish)
	t.Cleanup(func() { e.Stop() })
	e.SetAdminFrom("owner")
	e.SetTeamProjects(store, nil)
	ip.handler = e.ReceiveMessage
	ip.rememberCardChatType(replyContext{chatID: "oc_private", chatType: "group", senderType: "user"})
	for i := 0; i < 2; i++ {
		e.ReceiveMessage(ip, &core.Message{UserID: "owner", SessionKey: "feishu:oc_private:owner", Content: "/project dashboard", ReplyCtx: replyContext{chatID: "oc_private", messageID: "om_user", sessionKey: "feishu:oc_private:owner"}})
		awaitPanel(t, requests, http.MethodPost, "", "Current work")
	}
	click := panelClick("om_panel", "cmd:/project dashboard")
	click.Event.Action.Value["session_key"] = "feishu:oc_private:owner"
	if _, err := ip.onCardAction(click); err != nil {
		t.Fatal(err)
	}
	awaitPanel(t, requests, http.MethodPatch, "om_panel", "Refresh panel")
	failPatch.Store(true)
	if _, err := ip.onCardAction(click); err != nil {
		t.Fatal(err)
	}
	awaitPanel(t, requests, http.MethodPatch, "om_panel", "Current work")
	awaitPanel(t, requests, http.MethodPost, "", "Current work")
}
