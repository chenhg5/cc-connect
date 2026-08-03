package dingtalk

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestTruncateLastMessage(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty falls back to processing", in: "  ", want: lastMsgProcessingZH},
		{name: "collapse whitespace", in: "hello\n\n  world", want: "hello world"},
		{name: "short chinese kept", in: "处理完成", want: "处理完成"},
		{
			name: "long truncated with ellipsis",
			in:   strings.Repeat("测", lastMsgMaxRunes+5),
			want: strings.Repeat("测", lastMsgMaxRunes) + "…",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncateLastMessage(tt.in); got != tt.want {
				t.Fatalf("truncateLastMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCreateAICard_SetsLastMessageI18n(t *testing.T) {
	var createBody map[string]any
	rt := &cardAPIRoundTrip{
		onCreate: func(body map[string]any) {
			createBody = body
		},
	}
	p := &Platform{
		clientID:        "cid",
		clientSecret:    "csec",
		robotCode:       "robot-1",
		cardTemplateID:  "tmpl.schema",
		cardTemplateKey: "content",
		cardThrottleMs:  50,
		httpClient:      &http.Client{Transport: rt},
	}

	card, err := p.createAICard(context.Background(), replyContext{
		conversationId: "conv-1",
		senderStaffId:  "user-1",
		isGroup:        false,
	})
	if err != nil {
		t.Fatalf("createAICard: %v", err)
	}
	if card == nil || card.outTrackId == "" {
		t.Fatal("expected card with outTrackId")
	}

	space, ok := createBody["imRobotOpenSpaceModel"].(map[string]any)
	if !ok {
		t.Fatalf("imRobotOpenSpaceModel missing: %#v", createBody["imRobotOpenSpaceModel"])
	}
	last, ok := space["lastMessageI18n"].(map[string]any)
	if !ok {
		t.Fatalf("lastMessageI18n missing: %#v", space)
	}
	if last["ZH_CN"] != lastMsgProcessingZH {
		t.Errorf("ZH_CN = %v, want %q", last["ZH_CN"], lastMsgProcessingZH)
	}
	if last["EN_US"] != lastMsgProcessingEN {
		t.Errorf("EN_US = %v, want %q", last["EN_US"], lastMsgProcessingEN)
	}

	cardData, _ := createBody["cardData"].(map[string]any)
	paramMap, _ := cardData["cardParamMap"].(map[string]any)
	sysRaw, _ := paramMap["sys_lastMessageI18n"].(string)
	if sysRaw == "" {
		t.Fatal("sys_lastMessageI18n missing from cardParamMap")
	}
	var sys map[string]string
	if err := json.Unmarshal([]byte(sysRaw), &sys); err != nil {
		t.Fatalf("sys_lastMessageI18n json: %v", err)
	}
	if sys["zh_CN"] != lastMsgProcessingZH {
		t.Errorf("sys zh_CN = %q, want %q", sys["zh_CN"], lastMsgProcessingZH)
	}
}

func TestFinalize_UpdatesSpaceLastMessage(t *testing.T) {
	var spacesBody, cardBody map[string]any
	rt := &cardAPIRoundTrip{
		onSpaces: func(body map[string]any) {
			spacesBody = body
		},
		onCardUpdate: func(body map[string]any) {
			cardBody = body
		},
	}
	p := &Platform{
		clientID:        "cid",
		clientSecret:    "csec",
		robotCode:       "robot-1",
		cardTemplateID:  "tmpl.schema",
		cardTemplateKey: "content",
		cardThrottleMs:  1,
		httpClient:      &http.Client{Transport: rt},
		accessToken:     "tok-cached",
		tokenExpiry:     time.Now().Add(time.Hour),
	}
	card := &aiCard{
		outTrackId:  "card_track_1",
		templateKey: "content",
		platform:    p,
		isGroup:     false,
		state:       "processing",
		done:        make(chan struct{}),
	}

	if err := card.Finalize(context.Background(), "最终答复：已修好 lastMessageI18n"); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	if spacesBody == nil {
		t.Fatal("expected PUT /card/instances/spaces call")
	}
	if spacesBody["outTrackId"] != "card_track_1" {
		t.Errorf("outTrackId = %v", spacesBody["outTrackId"])
	}
	for _, key := range []string{"imRobotOpenSpaceModel", "imGroupOpenSpaceModel"} {
		space, ok := spacesBody[key].(map[string]any)
		if !ok {
			t.Fatalf("%s missing: %#v", key, spacesBody)
		}
		last, ok := space["lastMessageI18n"].(map[string]any)
		if !ok {
			t.Fatalf("%s.lastMessageI18n missing: %#v", key, space)
		}
		if !strings.Contains(fmtString(last["ZH_CN"]), "最终答复") {
			t.Errorf("%s preview = %v, want final reply snippet", key, last["ZH_CN"])
		}
	}

	if cardBody == nil {
		t.Fatal("expected PUT /card/instances call for sys_lastMessageI18n")
	}
	cardData, _ := cardBody["cardData"].(map[string]any)
	paramMap, _ := cardData["cardParamMap"].(map[string]any)
	sysRaw, _ := paramMap["sys_lastMessageI18n"].(string)
	if !strings.Contains(sysRaw, "最终答复") {
		t.Errorf("sys_lastMessageI18n = %q, want final reply", sysRaw)
	}
	opts, _ := cardBody["cardUpdateOptions"].(map[string]any)
	if opts["updateCardDataByKey"] != true {
		t.Errorf("updateCardDataByKey = %v, want true", opts["updateCardDataByKey"])
	}
}

func fmtString(v any) string {
	s, _ := v.(string)
	return s
}

// cardAPIRoundTrip mocks DingTalk card create / stream / spaces endpoints.
type cardAPIRoundTrip struct {
	onCreate     func(map[string]any)
	onSpaces     func(map[string]any)
	onCardUpdate func(map[string]any)
}

func (f *cardAPIRoundTrip) RoundTrip(req *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	_ = req.Body.Close()

	switch {
	case req.URL.Path == "/v1.0/oauth2/accessToken":
		return jsonResp(req, http.StatusOK, `{"accessToken":"tok-card","expireIn":7200}`)
	case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/card/instances/createAndDeliver"):
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		if f.onCreate != nil {
			f.onCreate(payload)
		}
		outTrack, _ := payload["outTrackId"].(string)
		resp := fmt.Sprintf(`{"result":{"cardInstanceId":"inst-1","outTrackId":%q,"deliverResults":[{"success":true}]}}`, outTrack)
		return jsonResp(req, http.StatusOK, resp)
	case req.Method == http.MethodPut && strings.HasSuffix(req.URL.Path, "/card/streaming"):
		return jsonResp(req, http.StatusOK, `{}`)
	case req.Method == http.MethodPut && strings.HasSuffix(req.URL.Path, "/card/instances/spaces"):
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		if f.onSpaces != nil {
			f.onSpaces(payload)
		}
		return jsonResp(req, http.StatusOK, `{}`)
	case req.Method == http.MethodPut && req.URL.Path == "/v1.0/card/instances":
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		if f.onCardUpdate != nil {
			f.onCardUpdate(payload)
		}
		return jsonResp(req, http.StatusOK, `{"success":true}`)
	default:
		return jsonResp(req, http.StatusNotFound, `{"message":"unexpected path `+req.URL.Path+`"}`)
	}
}

func jsonResp(req *http.Request, status int, body string) (*http.Response, error) {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}
