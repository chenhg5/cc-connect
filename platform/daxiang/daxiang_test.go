package daxiang

import (
	"errors"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestNew_RequiresMandatoryFields(t *testing.T) {
	tests := []struct {
		name string
		opts map[string]any
	}{
		{
			name: "missing app_id",
			opts: map[string]any{
				"app_secret":       "secret",
				"bot_id":           float64(123456),
				"audience":         "xm-xai",
				"callback_addr":    ":9090",
				"card_template_id": float64(1001),
			},
		},
		{
			name: "missing app_secret",
			opts: map[string]any{
				"app_id":           "cli_xxx",
				"bot_id":           float64(123456),
				"audience":         "xm-xai",
				"callback_addr":    ":9090",
				"card_template_id": float64(1001),
			},
		},
		{
			name: "missing bot_id",
			opts: map[string]any{
				"app_id":           "cli_xxx",
				"app_secret":       "secret",
				"audience":         "xm-xai",
				"callback_addr":    ":9090",
				"card_template_id": float64(1001),
			},
		},
		{
			name: "missing callback_addr",
			opts: map[string]any{
				"app_id":           "cli_xxx",
				"app_secret":       "secret",
				"bot_id":           float64(123456),
				"audience":         "xm-xai",
				"card_template_id": float64(1001),
			},
		},
		{
			name: "missing card_template_id",
			opts: map[string]any{
				"app_id":        "cli_xxx",
				"app_secret":    "secret",
				"bot_id":        float64(123456),
				"audience":      "xm-xai",
				"callback_addr": ":9090",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.opts)
			if err == nil {
				t.Fatal("New() error = nil, want validation error")
			}
		})
	}
}

func TestNew_ImplementsStreamingCapabilities(t *testing.T) {
	pAny, err := New(map[string]any{
		"app_id":           "cli_xxx",
		"app_secret":       "secret",
		"bot_id":           float64(123456),
		"audience":         "xm-xai",
		"callback_addr":    ":9090",
		"card_template_id": float64(1001),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, ok := pAny.(core.PreviewStarter); !ok {
		t.Fatalf("platform type %T does not implement core.PreviewStarter", pAny)
	}
	if _, ok := pAny.(core.MessageUpdater); !ok {
		t.Fatalf("platform type %T does not implement core.MessageUpdater", pAny)
	}
}

func TestHandleCallbackEvent_IgnoresDuplicateMsgID(t *testing.T) {
	p := &Platform{appID: "cli_xxx", botID: 123456}
	calls := 0
	p.handler = func(_ core.Platform, _ *core.Message) { calls++ }

	evt := callbackEvent{
		AppID:         "cli_xxx",
		BotID:         123456,
		EventTypeEnum: robotSingleChatMessage,
		Data: callbackMessageData{
			CTS:            time.Now().UnixMilli(),
			FromName:       "alice",
			FromUID:        10001,
			MsgID:          1212661773582049280,
			Message:        `{"text":"hello"}`,
			ChatID:         20002,
			ConversationID: "dx-single-20002",
			Type:           1,
		},
	}

	if err := p.handleCallbackEvent(evt); err != nil {
		t.Fatalf("first handleCallbackEvent() error = %v", err)
	}
	if err := p.handleCallbackEvent(evt); err != nil {
		t.Fatalf("second handleCallbackEvent() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestHandleCallbackEvent_RejectsWrongBotOrApp(t *testing.T) {
	p := &Platform{appID: "cli_xxx", botID: 123456}
	evt := callbackEvent{AppID: "other", BotID: 999, EventTypeEnum: robotSingleChatMessage}

	err := p.handleCallbackEvent(evt)
	if err == nil {
		t.Fatal("handleCallbackEvent() error = nil, want validation error")
	}
}

func TestHandleCallbackEvent_RejectsSenderOutsideAllowFrom(t *testing.T) {
	p := &Platform{appID: "cli_xxx", botID: 123456, allowFrom: "10002"}
	called := false
	p.handler = func(_ core.Platform, _ *core.Message) { called = true }

	evt := callbackEvent{
		AppID:         "cli_xxx",
		BotID:         123456,
		EventTypeEnum: robotSingleChatMessage,
		Data: callbackMessageData{
			CTS:            time.Now().UnixMilli(),
			FromName:       "alice",
			FromUID:        10001,
			MsgID:          1212661773582049280,
			Message:        `{"text":"hello"}`,
			ChatID:         20002,
			ConversationID: "dx-single-20002",
			Type:           1,
		},
	}

	if err := p.handleCallbackEvent(evt); err != nil {
		t.Fatalf("handleCallbackEvent() error = %v", err)
	}
	if called {
		t.Fatal("handler called for sender outside allow_from")
	}
}

func TestStart_StartsCallbackServer(t *testing.T) {
	p := &Platform{
		appID:        "cli_xxx",
		appSecret:    "secret",
		botID:        123456,
		callbackAddr: "127.0.0.1:0",
	}
	handler := func(_ core.Platform, _ *core.Message) {}
	if err := p.Start(handler); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { _ = p.Stop() })

	if p.thriftServer == nil {
		t.Fatal("Start() did not initialize thriftServer")
	}
	if p.callbackListenAddr == "" {
		t.Fatal("Start() did not record callbackListenAddr")
	}
}

func TestStop_ClearsCallbackServer(t *testing.T) {
	p := &Platform{
		appID:        "cli_xxx",
		appSecret:    "secret",
		botID:        123456,
		callbackAddr: "127.0.0.1:0",
	}
	if err := p.Start(func(_ core.Platform, _ *core.Message) {}); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := p.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if p.thriftServer != nil {
		t.Fatal("Stop() did not clear thriftServer")
	}
	if p.callbackListenAddr != "" {
		t.Fatal("Stop() did not clear callbackListenAddr")
	}
}

type stopErrServer struct{}

func (stopErrServer) Stop() error { return errors.New("stop failed") }

func TestStop_ClearsStateEvenWhenServerStopFails(t *testing.T) {
	p := &Platform{
		thriftServer:       stopErrServer{},
		callbackListenAddr: "127.0.0.1:9000",
	}

	err := p.Stop()
	if err == nil {
		t.Fatal("Stop() error = nil, want stop error")
	}
	if p.thriftServer != nil {
		t.Fatal("Stop() did not clear thriftServer after error")
	}
	if p.callbackListenAddr != "" {
		t.Fatal("Stop() did not clear callbackListenAddr after error")
	}
}

func TestHandleCallbackEvent_IgnoresOldMessage(t *testing.T) {
	oldStart := core.StartTime
	core.StartTime = time.Now()
	t.Cleanup(func() { core.StartTime = oldStart })

	p := &Platform{appID: "cli_xxx", botID: 123456}
	called := false
	p.handler = func(_ core.Platform, _ *core.Message) { called = true }

	evt := callbackEvent{
		AppID:         "cli_xxx",
		BotID:         123456,
		EventTypeEnum: robotSingleChatMessage,
		Data: callbackMessageData{
			CTS:            time.Now().Add(-10 * time.Second).UnixMilli(),
			FromName:       "alice",
			FromUID:        10001,
			MsgID:          1212661773582049280,
			Message:        `{"text":"hello"}`,
			ChatID:         20002,
			ConversationID: "dx-single-20002",
			Type:           1,
		},
	}

	if err := p.handleCallbackEvent(evt); err != nil {
		t.Fatalf("handleCallbackEvent() error = %v", err)
	}
	if called {
		t.Fatal("handler called for old message")
	}
}

