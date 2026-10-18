package discord

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// maomao fork: aux_channels — machine chatter leaves the main channel.

type postedMessage struct {
	channelID string
	body      map[string]any
}

// newAuxTestPlatform serves every "create message" call and records which
// channel each one went to.
func newAuxTestPlatform(t *testing.T, aux map[string]string) (*Platform, func() []postedMessage) {
	t.Helper()
	var mu sync.Mutex
	var posted []postedMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /api/v10/channels/<id>/messages
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) < 4 || parts[2] != "channels" || parts[len(parts)-1] != "messages" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusBadRequest)
			return
		}
		channelID := parts[3]
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if ct := r.Header.Get("Content-Type"); strings.HasPrefix(ct, "application/json") {
			_ = json.Unmarshal(raw, &body)
		}
		mu.Lock()
		posted = append(posted, postedMessage{channelID: channelID, body: body})
		n := len(posted)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"m`+string(rune('0'+n))+`","channel_id":"`+channelID+`"}`)
	}))
	t.Cleanup(server.Close)
	p := &Platform{session: newTestDiscordSession(t, server), auxChannels: aux}
	p.self = p
	return p, func() []postedMessage {
		mu.Lock()
		defer mu.Unlock()
		return append([]postedMessage(nil), posted...)
	}
}

func channelsOf(posted []postedMessage) []string {
	out := make([]string, 0, len(posted))
	for _, m := range posted {
		out = append(out, m.channelID)
	}
	return out
}

func progressCard() string {
	return core.BuildProgressCardPayloadV2([]core.ProgressCardEntry{{Kind: core.ProgressEntryToolUse, Text: "Read x.go"}}, false, "claude", core.LangChinese, core.ProgressCardStateRunning)
}

func subTaskTable() string {
	return core.BuildSubTaskTablePayload(core.LangChinese, []core.SubTask{{ID: "t1", Description: "查代码", Status: core.SubTaskRunning, StartedAt: time.Now()}})
}

func TestAuxChannels_ProgressCardAndSubTaskTableGoToLogChannel(t *testing.T) {
	p, posted := newAuxTestPlatform(t, map[string]string{"main": "log"})
	main := replyContext{channelID: "main", messageID: "u1"}

	h, err := p.SendPreviewStart(context.Background(), p.MarkAgentReply(main), progressCard())
	if err != nil {
		t.Fatalf("SendPreviewStart(progress) error = %v", err)
	}
	if got := h.(*discordPreviewHandle).channelID; got != "log" {
		t.Fatalf("progress card handle channel = %q, want log (edits must follow the card)", got)
	}
	if _, err := p.SendPreviewStart(context.Background(), p.MarkAgentReply(main), subTaskTable()); err != nil {
		t.Fatalf("SendPreviewStart(subtasks) error = %v", err)
	}
	// A streamed text preview is the reply itself: it stays in the main channel.
	if _, err := p.SendPreviewStart(context.Background(), p.MarkAgentReply(main), "正在写结论……"); err != nil {
		t.Fatalf("SendPreviewStart(text) error = %v", err)
	}
	if got := channelsOf(posted()); strings.Join(got, ",") != "log,log,main" {
		t.Fatalf("preview channels = %v, want [log log main]", got)
	}
}

func TestAuxChannels_NoticesGoToLogChannel_RepliesStay(t *testing.T) {
	p, posted := newAuxTestPlatform(t, map[string]string{"main": "log"})
	main := replyContext{channelID: "main", messageID: "u1"}
	ctx := context.Background()

	if err := p.Send(ctx, p.MarkSystemNotice(main), "♻️ 空闲 3 小时：开新会话"); err != nil {
		t.Fatal(err)
	}
	if err := p.Reply(ctx, p.MarkSystemNotice(main), "⏰ 橘量化·接着"); err != nil {
		t.Fatal(err)
	}
	if err := p.Send(ctx, p.MarkSystemNotice(main), "⏳ 模型满载冷却中，这条 10 分钟后再处理。"); err != nil {
		t.Fatal(err)
	}
	if err := p.Reply(ctx, p.MarkFinalReply(main), "【结论】成了。"); err != nil {
		t.Fatal(err)
	}
	if err := p.Send(ctx, p.MarkAgentReply(main), "⏰ ✅ `run.sh`\n\n结果正文"); err != nil {
		t.Fatal(err)
	}
	if err := p.Reply(ctx, main, "❌ 出错了"); err != nil { // unmarked: errors, command replies
		t.Fatal(err)
	}
	got := posted()
	if c := strings.Join(channelsOf(got), ","); c != "log,log,log,main,main,main" {
		t.Fatalf("channels = %v, want notices in log, everything else in main", channelsOf(got))
	}
	// The ⏰ notice was a Reply, but its trigger message is in the main channel:
	// in the log channel it must be a plain post, not a reply to a foreign message.
	if _, hasRef := got[1].body["message_reference"]; hasRef {
		t.Fatalf("notice in log channel carries a message_reference: %v", got[1].body)
	}
	if _, hasRef := got[3].body["message_reference"]; !hasRef {
		t.Fatalf("the reply in the main channel lost its message_reference: %v", got[3].body)
	}
	// Notices are not agent output: backfill must still show them to the agent.
	if p.turnSent.has("m1") || p.turnSent.has("m2") || p.turnSent.has("m3") {
		t.Fatal("a notice was recorded as agent output")
	}
	if !p.turnSent.has("m4") || !p.turnSent.has("m5") {
		t.Fatal("agent output in the main channel must still be recorded")
	}
}

func TestAuxChannels_UnlistedChannelAndNoTableUnchanged(t *testing.T) {
	for name, aux := range map[string]map[string]string{"unlisted": {"main": "log"}, "no table": nil} {
		t.Run(name, func(t *testing.T) {
			p, posted := newAuxTestPlatform(t, aux)
			other := replyContext{channelID: "other", messageID: "u1"}
			ctx := context.Background()
			if _, err := p.SendPreviewStart(ctx, p.MarkAgentReply(other), progressCard()); err != nil {
				t.Fatal(err)
			}
			if _, err := p.SendPreviewStart(ctx, p.MarkAgentReply(other), subTaskTable()); err != nil {
				t.Fatal(err)
			}
			if err := p.Send(ctx, p.MarkSystemNotice(other), "♻️ 开新会话"); err != nil {
				t.Fatal(err)
			}
			if err := p.Reply(ctx, p.MarkSystemNotice(other), "⏰ 定时任务"); err != nil {
				t.Fatal(err)
			}
			if err := p.Reply(ctx, p.MarkFinalReply(other), "正文"); err != nil {
				t.Fatal(err)
			}
			got := posted()
			for i, m := range got {
				if m.channelID != "other" {
					t.Fatalf("message %d went to %q, want other (channel not in aux_channels must be unchanged)", i, m.channelID)
				}
			}
			if _, hasRef := got[3].body["message_reference"]; !hasRef {
				t.Fatalf("⏰ reply lost its message_reference without a table: %v", got[3].body)
			}
		})
	}
}

func TestAuxChannels_ThreadTargetIsNotRedirected(t *testing.T) {
	// The table is keyed by the channel a message is posted to; a thread under a
	// listed channel is not listed, so it keeps everything.
	p, posted := newAuxTestPlatform(t, map[string]string{"main": "log"})
	thread := replyContext{channelID: "main", threadID: "thread-1"}
	if _, err := p.SendPreviewStart(context.Background(), thread, progressCard()); err != nil {
		t.Fatal(err)
	}
	if err := p.Send(context.Background(), p.MarkSystemNotice(thread), "♻️"); err != nil {
		t.Fatal(err)
	}
	if got := channelsOf(posted()); strings.Join(got, ",") != "thread-1,thread-1" {
		t.Fatalf("channels = %v, want both in the thread", got)
	}
}

func TestMarkSystemNotice(t *testing.T) {
	p := &Platform{}
	rc := p.MarkSystemNotice(replyContext{channelID: "c", messageID: "m"}).(replyContext)
	if !rc.notice || rc.agent || rc.final {
		t.Fatalf("MarkSystemNotice = %+v, want notice only", rc)
	}
	ictx := &interactionReplyCtx{channelID: "c"}
	if got := p.MarkSystemNotice(ictx); got != ictx {
		t.Fatal("an interaction reply context is passed through unchanged")
	}
	if p.redirectNotice(rc) != rc {
		t.Fatal("without a table redirectNotice must return the context unchanged")
	}
}

func TestNew_AuxChannelsOption(t *testing.T) {
	pAny, err := New(map[string]any{
		"token":          "discord-token",
		"progress_style": "card",
		"aux_channels":   map[string]any{"1559487673729687685": "1561419538556260422"},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	p := pAny.(*progressPlatform).Platform
	if aux, ok := p.auxChannelFor("1559487673729687685"); !ok || aux != "1561419538556260422" {
		t.Fatalf("auxChannelFor = %q, %v", aux, ok)
	}
	if _, ok := p.auxChannelFor("1559559262529790063"); ok {
		t.Fatal("unlisted channel must have no log channel")
	}

	if _, err := New(map[string]any{"token": "t", "aux_channels": map[string]any{"1": 2}}); err == nil {
		t.Fatal("non-string log channel ID must be rejected")
	}
	if _, err := New(map[string]any{"token": "t", "aux_channels": "1=2"}); err == nil {
		t.Fatal("a non-table aux_channels must be rejected")
	}
	if _, err := New(map[string]any{"token": "t", "aux_channels": map[string]any{"1": ""}}); err == nil {
		t.Fatal("an empty log channel ID must be rejected")
	}
	pAny, err = New(map[string]any{"token": "t"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pAny.(*progressPlatform).auxChannelFor("x"); ok {
		t.Fatal("no option → no table")
	}
}
