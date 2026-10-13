package feishu

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func executionCallbackID(event *callback.CardActionTriggerEvent) string {
	if event == nil || event.Event == nil || event.Event.Action == nil {
		return ""
	}
	v := event.Event.Action.Value
	if id, _ := v["execution_card"].(string); id != "" {
		return id
	}
	if action, _ := v["action"].(string); strings.HasPrefix(action, "exec:") {
		if a := strings.Split(action, ":"); len(a) == 5 {
			return a[1]
		}
	}
	return ""
}

// Feishu callbacks have a short acknowledgement deadline. Execute on the
// platform worker, independent of the agent and of an in-flight card write.
func (p *Platform) onExecutionCardAction(event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
	type result struct {
		response *callback.CardActionTriggerResponse
		err      error
	}
	done := make(chan result, 1)
	go func() { response, err := p.handleCardAction(event); done <- result{response, err} }()
	select {
	case r := <-done:
		if r.response == nil && r.err == nil {
			r.response = &callback.CardActionTriggerResponse{}
		}
		return r.response, r.err
	case <-time.After(400 * time.Millisecond):
		go func() {
			r := <-done
			if r.err != nil {
				slog.Warn("execution card callback failed", "error", r.err)
			}
			if r.response != nil && r.response.Toast != nil && r.response.Toast.Type == "warning" {
				p.showExecutionCallbackNotice(event, r.response.Toast.Content)
			}
		}()
		return &callback.CardActionTriggerResponse{Toast: &callback.Toast{Type: "info", Content: p.cardI18n().T(core.MsgStarting)}}, nil
	}
}

func (p *Platform) showExecutionCallbackNotice(event *callback.CardActionTriggerEvent, text string) {
	c, err := p.loadQueryCard(executionCallbackID(event))
	if err != nil || event.Event.Context == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	h, ok := c.handle.(*feishuPreviewHandle)
	if !ok || c.rc.chatID != event.Event.Context.OpenChatID || h.messageID != event.Event.Context.OpenMessageID {
		return
	}
	c.notice = text
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.writeLocked(ctx); err != nil {
		slog.Warn("execution callback notice failed", "error", err)
	}
}
