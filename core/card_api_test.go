package core

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type rawCardCall struct {
	replyCtx  any
	messageID string
	card      string
	uuid      string
}

// stubRawCardPlatform sends and patches native cards.
type stubRawCardPlatform struct {
	stubPlatformEngine
	cardMu  sync.Mutex
	sends   []rawCardCall
	updates []rawCardCall
}

func (p *stubRawCardPlatform) SendRawCard(_ context.Context, replyCtx any, card json.RawMessage, uuid string) (string, error) {
	p.cardMu.Lock()
	defer p.cardMu.Unlock()
	p.sends = append(p.sends, rawCardCall{replyCtx: replyCtx, card: string(card), uuid: uuid})
	return "om_card_1", nil
}

func (p *stubRawCardPlatform) UpdateRawCard(_ context.Context, messageID string, card json.RawMessage) error {
	p.cardMu.Lock()
	defer p.cardMu.Unlock()
	p.updates = append(p.updates, rawCardCall{messageID: messageID, card: string(card)})
	return nil
}

func postJSON(t *testing.T, handler http.HandlerFunc, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return rec
}

const testCard = `{"schema":"2.0","header":{"title":{"tag":"plain_text","content":"Receipt"}},"body":{"elements":[]}}`

// A card goes out as it is and the answer names its message.
func TestHandleSend_CardReturnsMessageID(t *testing.T) {
	p := &stubRawCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	defer e.cancel()
	e.interactiveStates["feishu:oc_p2p:ou_owner"] = &interactiveState{platform: p, replyCtx: "reply-ctx"}
	api := &APIServer{engines: map[string]*Engine{"test": e}}

	rec := postJSON(t, api.handleSend, "/send", `{"session_key":"feishu:oc_p2p:ou_owner","card":`+testCard+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp["message_id"] != "om_card_1" || resp["status"] != "ok" {
		t.Fatalf("response = %s (%v)", rec.Body.String(), err)
	}
	if len(p.sends) != 1 || p.sends[0].replyCtx != "reply-ctx" || p.sends[0].uuid != "" {
		t.Fatalf("sends = %+v", p.sends)
	}
	var sent, want any
	_ = json.Unmarshal([]byte(p.sends[0].card), &sent)
	_ = json.Unmarshal([]byte(testCard), &want)
	if !jsonEqual(sent, want) {
		t.Fatalf("card changed on the way: %s", p.sends[0].card)
	}
	if got := p.getSent(); len(got) != 0 {
		t.Fatalf("a card send also sent text %q", got)
	}
}

// A card's optional uuid reaches the platform unchanged, so a retried send
// with the same uuid is deduplicated there.
func TestHandleSend_CardCarriesUUID(t *testing.T) {
	p := &stubRawCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	defer e.cancel()
	e.interactiveStates["feishu:oc_p2p:ou_owner"] = &interactiveState{platform: p, replyCtx: "reply-ctx"}
	api := &APIServer{engines: map[string]*Engine{"test": e}}
	uuid := "asst-" + strings.Repeat("a", 40)
	rec := postJSON(t, api.handleSend, "/send", `{"session_key":"feishu:oc_p2p:ou_owner","uuid":"`+uuid+`","card":`+testCard+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if len(p.sends) != 1 || p.sends[0].uuid != uuid {
		t.Fatalf("sends = %+v", p.sends)
	}
}

// A session that has gone idle is still reachable by its key.
func TestHandleSend_CardToIdleSession(t *testing.T) {
	p := &stubRawCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	reconstructing := &stubCardReconstructPlatform{stubRawCardPlatform: p}
	e := NewEngine("test", &stubAgent{}, []Platform{reconstructing}, "", LangEnglish)
	defer e.cancel()
	api := &APIServer{engines: map[string]*Engine{"test": e}}
	rec := postJSON(t, api.handleSend, "/send", `{"session_key":"feishu:oc_p2p:ou_owner","card":`+testCard+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if len(p.sends) != 1 || p.sends[0].replyCtx != "rebuilt:feishu:oc_p2p:ou_owner" {
		t.Fatalf("sends = %+v", p.sends)
	}
}

type stubCardReconstructPlatform struct {
	*stubRawCardPlatform
}

func (p *stubCardReconstructPlatform) ReconstructReplyCtx(sessionKey string) (any, error) {
	return "rebuilt:" + sessionKey, nil
}

func TestHandleSend_CardRejections(t *testing.T) {
	cards := &stubRawCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	plain := &stubPlatformEngine{n: "slack"}
	e := NewEngine("test", &stubAgent{}, []Platform{cards, plain}, "", LangEnglish)
	defer e.cancel()
	e.interactiveStates["feishu:oc:ou"] = &interactiveState{platform: cards, replyCtx: "c"}
	e.interactiveStates["slack:C1:U1"] = &interactiveState{platform: plain, replyCtx: "s"}
	api := &APIServer{engines: map[string]*Engine{"test": e}}

	for name, tc := range map[string]struct {
		body string
		code int
	}{
		"not an object":         {`{"session_key":"feishu:oc:ou","card":"hello"}`, http.StatusBadRequest},
		"with a message":        {`{"session_key":"feishu:oc:ou","card":` + testCard + `,"message":"hi"}`, http.StatusBadRequest},
		"platform without card": {`{"session_key":"slack:C1:U1","card":` + testCard + `}`, http.StatusNotImplemented},
		"uuid over 50":          {`{"session_key":"feishu:oc:ou","uuid":"` + strings.Repeat("u", 51) + `","card":` + testCard + `}`, http.StatusBadRequest},
		"uuid of 51 runes":      {`{"session_key":"feishu:oc:ou","uuid":"` + strings.Repeat("键", 51) + `","card":` + testCard + `}`, http.StatusBadRequest},
		"uuid without card":     {`{"session_key":"feishu:oc:ou","uuid":"asst-1","message":"hi"}`, http.StatusBadRequest},
		"with at_users":         {`{"session_key":"feishu:oc:ou","card":` + testCard + `,"at_users":["u1"]}`, http.StatusBadRequest},
		"with at_all":           {`{"session_key":"feishu:oc:ou","card":` + testCard + `,"at_all":true}`, http.StatusBadRequest},
	} {
		if rec := postJSON(t, api.handleSend, "/send", tc.body); rec.Code != tc.code {
			t.Errorf("%s: status = %d, want %d, body=%s", name, rec.Code, tc.code, rec.Body.String())
		}
	}
	if len(cards.sends) != 0 || len(plain.getSent()) != 0 {
		t.Fatalf("rejected requests sent something: cards=%+v text=%q", cards.sends, plain.getSent())
	}
}

// A plain text send keeps its old answer.
func TestHandleSend_TextKeepsStatusOK(t *testing.T) {
	p := &stubRawCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	defer e.cancel()
	e.interactiveStates["feishu:oc:ou"] = &interactiveState{platform: p, replyCtx: "c"}
	api := &APIServer{engines: map[string]*Engine{"test": e}}
	rec := postJSON(t, api.handleSend, "/send", `{"session_key":"feishu:oc:ou","message":"hi"}`)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"status":"ok"}` {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleCardsUpdate(t *testing.T) {
	cards := &stubRawCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	e := NewEngine("test", &stubAgent{}, []Platform{cards}, "", LangEnglish)
	defer e.cancel()
	api := &APIServer{engines: map[string]*Engine{"test": e}}

	rec := postJSON(t, api.handleCardsUpdate, "/cards/update", `{"project":"test","message_id":"om_card_1","card":`+testCard+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if len(cards.updates) != 1 || cards.updates[0].messageID != "om_card_1" || !strings.Contains(cards.updates[0].card, `"Receipt"`) {
		t.Fatalf("updates = %+v", cards.updates)
	}

	for name, tc := range map[string]struct {
		body string
		code int
	}{
		"missing message id": {`{"card":` + testCard + `}`, http.StatusBadRequest},
		"missing card":       {`{"message_id":"om_1"}`, http.StatusBadRequest},
		"card not an object": {`{"message_id":"om_1","card":[1]}`, http.StatusBadRequest},
		"unknown project":    {`{"project":"nope","message_id":"om_1","card":` + testCard + `}`, http.StatusNotFound},
	} {
		if rec := postJSON(t, api.handleCardsUpdate, "/cards/update", tc.body); rec.Code != tc.code {
			t.Errorf("%s: status = %d, want %d, body=%s", name, rec.Code, tc.code, rec.Body.String())
		}
	}
	if rec := httptest.NewRecorder(); true {
		api.handleCardsUpdate(rec, httptest.NewRequest(http.MethodGet, "/cards/update", bytes.NewReader(nil)))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET: status = %d", rec.Code)
		}
	}

	plainEngine := NewEngine("plain", &stubAgent{}, []Platform{&stubPlatformEngine{n: "slack"}}, "", LangEnglish)
	defer plainEngine.cancel()
	plainAPI := &APIServer{engines: map[string]*Engine{"plain": plainEngine}}
	if rec := postJSON(t, plainAPI.handleCardsUpdate, "/cards/update", `{"message_id":"om_1","card":`+testCard+`}`); rec.Code != http.StatusNotImplemented {
		t.Errorf("platform without cards: status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// platformForSessionKey resolves a plain key by its first segment and a
// workspace-prefixed key (multi-workspace mode) by the ":name:" it contains.
func TestPlatformForSessionKey(t *testing.T) {
	feishu := &stubPlatformEngine{n: "feishu"}
	slack := &stubPlatformEngine{n: "slack"}
	e := NewEngine("test", &stubAgent{}, []Platform{feishu, slack}, "", LangEnglish)
	defer e.cancel()
	for _, tc := range []struct {
		key, platform, stripped string
	}{
		{"feishu:oc_1:u_1", "feishu", "feishu:oc_1:u_1"},
		{"slack:C1:U1", "slack", "slack:C1:U1"},
		{"/root/work:feishu:oc_1:u_1", "feishu", "feishu:oc_1:u_1"},
		{`C:\work:slack:C1:U1`, "slack", "slack:C1:U1"},
		{"telegram:1:2", "", "telegram:1:2"},
		{"", "", ""},
	} {
		p, stripped := e.platformForSessionKey(tc.key)
		got := ""
		if p != nil {
			got = p.Name()
		}
		if got != tc.platform || stripped != tc.stripped {
			t.Errorf("platformForSessionKey(%q) = (%q, %q), want (%q, %q)", tc.key, got, stripped, tc.platform, tc.stripped)
		}
	}
}

// A workspace-prefixed session key that /send accepts for a card also picks
// the platform for /cards/update.
func TestCards_WorkspacePrefixedSessionKeyOnBothEndpoints(t *testing.T) {
	p := &stubRawCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	reconstructing := &stubCardReconstructPlatform{stubRawCardPlatform: p}
	plain := &stubPlatformEngine{n: "slack"}
	e := NewEngine("test", &stubAgent{}, []Platform{plain, reconstructing}, "", LangEnglish)
	defer e.cancel()
	api := &APIServer{engines: map[string]*Engine{"test": e}}
	const key = "/root/work:feishu:oc_1:u_1"

	rec := postJSON(t, api.handleSend, "/send", `{"session_key":"`+key+`","card":`+testCard+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("send: status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if len(p.sends) != 1 || p.sends[0].replyCtx != "rebuilt:feishu:oc_1:u_1" {
		t.Fatalf("sends = %+v", p.sends)
	}
	rec = postJSON(t, api.handleCardsUpdate, "/cards/update", `{"session_key":"`+key+`","message_id":"om_card_1","card":`+testCard+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if len(p.updates) != 1 || p.updates[0].messageID != "om_card_1" {
		t.Fatalf("updates = %+v", p.updates)
	}

	// A key that names no platform is a 400 on update, not a guess.
	rec = postJSON(t, api.handleCardsUpdate, "/cards/update", `{"session_key":"/root/work:telegram:1:2","message_id":"om_card_1","card":`+testCard+`}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown platform: status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if len(p.updates) != 1 {
		t.Fatalf("an unresolvable key updated a card: %+v", p.updates)
	}
}

// Several card platforms: a session key picks one by name; platforms that
// share a name cannot be told apart, and the error says so.
func TestUpdateCard_SeveralCardPlatforms(t *testing.T) {
	feishu := &stubRawCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	lark := &stubRawCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "lark"}}
	e := NewEngine("test", &stubAgent{}, []Platform{feishu, lark}, "", LangEnglish)
	defer e.cancel()

	if err := e.UpdateCard("", "om_1", json.RawMessage(testCard)); err == nil || !strings.Contains(err.Error(), "pass session_key") {
		t.Fatalf("no session key: err = %v", err)
	}
	if err := e.UpdateCard("/root/work:lark:oc_1:u_1", "om_1", json.RawMessage(testCard)); err != nil {
		t.Fatalf("lark key: %v", err)
	}
	if len(lark.updates) != 1 || len(feishu.updates) != 0 {
		t.Fatalf("updates: lark=%+v feishu=%+v", lark.updates, feishu.updates)
	}

	twin := &stubRawCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	e2 := NewEngine("twins", &stubAgent{}, []Platform{feishu, twin}, "", LangEnglish)
	defer e2.cancel()
	err := e2.UpdateCard("feishu:oc_1:u_1", "om_1", json.RawMessage(testCard))
	if err == nil || strings.Contains(err.Error(), "pass session_key") || !strings.Contains(err.Error(), `named "feishu"`) {
		t.Fatalf("same-name platforms: err = %v", err)
	}
	if rec := httptest.NewRecorder(); true {
		http.Error(rec, err.Error(), cardErrorStatus(err))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("same-name platforms: status = %d", rec.Code)
		}
	}
	if len(feishu.updates) != 0 || len(twin.updates) != 0 {
		t.Fatalf("an ambiguous update reached a platform: %+v %+v", feishu.updates, twin.updates)
	}
}

// A card update waits for the outgoing rate limiter like a send does.
func TestUpdateCard_WaitsForOutgoingRateLimit(t *testing.T) {
	cards := &stubRawCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	e := NewEngine("test", &stubAgent{}, []Platform{cards}, "", LangEnglish)
	e.SetOutgoingRateLimitCfg(OutgoingRateLimitCfg{MaxPerSecond: 0.001, Burst: 1}, nil)
	if err := e.UpdateCard("", "om_1", json.RawMessage(testCard)); err != nil {
		t.Fatalf("first update: %v", err)
	}
	// The bucket is empty now; with the engine stopped the wait gives up
	// instead of reaching the platform.
	e.cancel()
	if err := e.UpdateCard("", "om_1", json.RawMessage(testCard)); err == nil {
		t.Fatal("second update: expected the rate-limit wait to fail on a stopped engine")
	}
	if len(cards.updates) != 1 {
		t.Fatalf("updates = %d, want 1 (the throttled update must not reach the platform)", len(cards.updates))
	}
}

// /cards/update accepts any body /send accepts: a card above the old fixed
// 4 MiB cap still goes through.
func TestHandleCardsUpdate_UsesSendBodyLimit(t *testing.T) {
	cards := &stubRawCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	e := NewEngine("test", &stubAgent{}, []Platform{cards}, "", LangEnglish)
	defer e.cancel()
	api := &APIServer{engines: map[string]*Engine{"test": e}}
	big := `{"body":{"text":"` + strings.Repeat("x", 5<<20) + `"}}`
	if int64(len(big)) >= api.sendBodyLimit() {
		t.Fatalf("test card (%d bytes) must fit the /send body limit (%d)", len(big), api.sendBodyLimit())
	}
	rec := postJSON(t, api.handleCardsUpdate, "/cards/update", `{"message_id":"om_1","card":`+big+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%.200s", rec.Code, rec.Body.String())
	}
	if len(cards.updates) != 1 {
		t.Fatalf("updates = %d", len(cards.updates))
	}
}

// /send and /cards/update pick the project the same way.
func TestCardEndpoints_ResolveProjectAlike(t *testing.T) {
	newEngine := func(name string) *Engine {
		e := NewEngine(name, &stubAgent{}, []Platform{&stubRawCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}}, "", LangEnglish)
		t.Cleanup(e.cancel)
		return e
	}
	api := &APIServer{engines: map[string]*Engine{"a": newEngine("a"), "b": newEngine("b")}}
	for name, tc := range map[string]struct {
		project string
		code    int
	}{
		"no project, two configured": {"", http.StatusBadRequest},
		"unknown project":            {`"project":"nope",`, http.StatusNotFound},
	} {
		cardBody := `{` + tc.project + `"session_key":"feishu:oc:ou","card":` + testCard + `,"message_id":"om_1"}`
		for _, c := range []struct {
			what    string
			handler http.HandlerFunc
			path    string
			body    string
		}{
			{"send text", api.handleSend, "/send", `{` + tc.project + `"session_key":"feishu:oc:ou","message":"hi"}`},
			{"send card", api.handleSend, "/send", cardBody},
			{"update card", api.handleCardsUpdate, "/cards/update", cardBody},
		} {
			if rec := postJSON(t, c.handler, c.path, c.body); rec.Code != tc.code {
				t.Errorf("%s, %s: status = %d, want %d, body=%s", name, c.what, rec.Code, tc.code, rec.Body.String())
			}
		}
	}
}
