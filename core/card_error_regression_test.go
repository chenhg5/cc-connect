package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

type refusedRawCardPlatform struct {
	stubRawCardPlatform
	code int
}

func (p *refusedRawCardPlatform) SendRawCard(context.Context, any, json.RawMessage, string) (string, error) {
	return "", fmt.Errorf("feishu: send failed code=%d msg=card refused", p.code)
}

func TestCardSendRefusalPreservesPlatformCode(t *testing.T) {
	for _, code := range []int{230099, 230025} {
		p := &refusedRawCardPlatform{code: code}
		p.n = "feishu"
		e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
		t.Cleanup(e.cancel)
		e.interactiveStates["feishu:oc:ou"] = &interactiveState{platform: p, replyCtx: "ctx"}
		api := &APIServer{engines: map[string]*Engine{"test": e}}
		rec := postJSON(t, api.handleSend, "/send", `{"session_key":"feishu:oc:ou","card":{}}`)
		if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), fmt.Sprintf("code=%d", code)) {
			t.Fatalf("refusal lost: status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
}
