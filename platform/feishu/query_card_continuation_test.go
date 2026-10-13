package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestQueryCard_OverflowContinuesWithoutPagination(t *testing.T) {
	for _, entity := range []bool{true, false} {
		t.Run(fmt.Sprint(entity), func(t *testing.T) {
			p, f := newSplitCardFixture(t, entity)
			ctx := context.Background()
			sc, err := p.CreateStreamingCard(ctx, replyContext{chatID: "oc_test", sessionKey: "feishu:oc_test:owner"})
			if err != nil {
				t.Fatal(err)
			}
			c := sc.(core.InteractiveStreamingCard)
			process := "PROCESS_FIRST\n" + strings.Repeat("process line\n", 500) + "PROCESS_LAST"
			if err := c.Update(ctx, process); err != nil {
				t.Fatal(err)
			}
			prompt := core.NewCard().Title("Approve", "orange").Markdown("APPROVAL_PROMPT").Build()
			prompt.Elements = append(prompt.Elements, core.CardActions{Buttons: []core.CardButton{{Text: "Allow", Value: "perm:allow", Extra: map[string]string{"query_card": "true", "permission_token": "request-one"}}}})
			if err := c.SetPrompt(ctx, prompt); err != nil {
				t.Fatal(err)
			}
			pages := core.SplitMessageCodeFenceAware(process, queryPageRunes)
			f.mu.Lock()
			count := len(f.messages)
			f.mu.Unlock()
			if count != len(pages) {
				t.Fatalf("overflow must create continuation cards: got %d, want %d", count, len(pages))
			}
			for i := range pages {
				text := f.shown(fmt.Sprintf("om_%d", i+1))
				if strings.Contains(text, "query_page:") {
					t.Fatal("new card still requires pagination")
				}
				if strings.Contains(text, "APPROVAL_PROMPT") != (i == len(pages)-1) {
					t.Fatal("approval must appear only on latest card")
				}
			}
			if !strings.Contains(f.shown("om_1"), "PROCESS_FIRST") || !strings.Contains(f.shown(fmt.Sprintf("om_%d", count)), "PROCESS_LAST") {
				t.Fatal("history or tail lost")
			}
			if err := c.SetNotice(ctx, "Working"); err != nil {
				t.Fatal(err)
			}
			if err := c.SetPrompt(ctx, nil); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			unchangedCount := len(f.messages)
			f.mu.Unlock()
			if unchangedCount != count {
				t.Fatal("ordinary updates created more messages")
			}
			answer := "ANSWER_FIRST\n" + strings.Repeat("answer line\n", 500) + "ANSWER_LAST"
			if err := sc.(core.FinalAnswerCard).FinalizeWithAnswer(ctx, process, answer); err != nil {
				t.Fatal(err)
			}
			answerPages := core.SplitMessageCodeFenceAware(answer, queryPageRunes)
			f.mu.Lock()
			finalCount := len(f.messages)
			f.mu.Unlock()
			if finalCount != count+len(answerPages) {
				t.Fatalf("long answer not continued separately: %d", finalCount)
			}
			for i := 1; i <= finalCount; i++ {
				text := f.shown(fmt.Sprintf("om_%d", i))
				if strings.Contains(text, "query_page:") || strings.Contains(text, "perm:allow") {
					t.Fatal("finished cards contain pagination or approval")
				}
				if i > count && (!strings.Contains(text, "结论") || strings.Contains(text, "PROCESS_")) {
					t.Fatal("answer mixed with process")
				}
			}
			if !strings.Contains(f.shown(fmt.Sprintf("om_%d", count+1)), "ANSWER_FIRST") || !strings.Contains(f.shown(fmt.Sprintf("om_%d", finalCount)), "ANSWER_LAST") {
				t.Fatal("answer truncated")
			}
			if err := sc.(core.FinalAnswerCard).FinalizeWithAnswer(ctx, "wrong", "duplicate"); err != nil {
				t.Fatal(err)
			}
			if err := c.Update(ctx, "late"); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.messages) != finalCount {
				t.Fatal("duplicate completion created cards")
			}
		})
	}
}

func TestQueryCard_ContinuationRetriesReuseSentParts(t *testing.T) {
	for _, entity := range []bool{true, false} {
		t.Run(fmt.Sprint(entity), func(t *testing.T) {
			p, f := newSplitCardFixture(t, entity)
			ctx := context.Background()
			sc, err := p.CreateStreamingCard(ctx, replyContext{chatID: "oc_test"})
			if err != nil {
				t.Fatal(err)
			}
			c := sc.(core.InteractiveStreamingCard)
			text := "FIRST\n" + strings.Repeat("中🙂line\n", 1400) + "LAST"
			if err := c.Update(ctx, text); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			f.failCreateAt = 3
			f.mu.Unlock()
			if err := c.SetNotice(ctx, "Working"); err == nil {
				t.Fatal("expected failure creating third part")
			}
			first, second := f.shown("om_1"), f.shown("om_2")
			if first == "" || second == "" {
				t.Fatal("partial delivery lost")
			}
			if err := c.SetNotice(ctx, "Working"); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			count := len(f.messages)
			f.mu.Unlock()
			if count != len(core.SplitMessageCodeFenceAware(text, queryPageRunes)) || f.shown("om_1") != first || f.shown("om_2") != second {
				t.Fatal("retry duplicated or changed delivered parts")
			}
			f.mu.Lock()
			updates := f.updates
			f.mu.Unlock()
			if err := c.SetNotice(ctx, "Working"); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			afterUpdates := f.updates
			f.mu.Unlock()
			if afterUpdates != updates {
				t.Fatal("unchanged chunks were needlessly rewritten")
			}
			if err := c.Finish(ctx, "", core.CardStatusError); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			afterCount := len(f.messages)
			f.mu.Unlock()
			if afterCount != count {
				t.Fatal("error completion created new messages")
			}
		})
	}
}

func TestQueryCard_ContinuationPreservesCodeAndHistory(t *testing.T) {
	p, f := newSplitCardFixture(t, true)
	ctx := context.Background()
	sc, err := p.CreateStreamingCard(ctx, replyContext{chatID: "oc_test"})
	if err != nil {
		t.Fatal(err)
	}
	c := sc.(core.InteractiveStreamingCard)
	text := "FIRST\n```go\n" + strings.Repeat("fmt.Println(\"中🙂\")\n", 550) + "```\nLAST"
	if err := c.Update(ctx, text); err != nil {
		t.Fatal(err)
	}
	if err := c.SetNotice(ctx, "Running"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	count := len(f.messages)
	f.mu.Unlock()
	chunks := core.SplitMessageCodeFenceAware(text, queryPageRunes)
	if count != len(chunks) {
		t.Fatal("code block continuation missing")
	}
	for i, chunk := range chunks {
		elements := foldElements(t, f.shown(fmt.Sprintf("om_%d", i+1)))
		var content string
		for _, el := range elements {
			if el["tag"] == "collapsible_panel" {
				for _, nested := range el["elements"].([]any) {
					if detail := nested.(map[string]any); detail["tag"] == "markdown" {
						content += detail["content"].(string)
					}
				}
			}
		}
		if content != sanitizeMarkdownURLs(preprocessFeishuMarkdown(chunk)) {
			t.Fatal("code or unicode corrupted")
		}
	}
	first := f.shown("om_1")
	if err := c.Update(ctx, text+"\nAPPENDED"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetNotice(ctx, "Running"); err != nil {
		t.Fatal(err)
	}
	if f.shown("om_1") != first {
		t.Fatal("append changed earlier history")
	}
	if err := c.Finish(ctx, "SHORTENED", core.CardStatusDone); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.shown("om_1"), "SHORTENED") || !strings.Contains(f.shown(fmt.Sprintf("om_%d", count)), "LAST") || !strings.Contains(f.shown(fmt.Sprintf("om_%d", count)), "保留供回看") {
		t.Fatal("revision deleted historical output")
	}
}

func TestQueryCard_ContinuationRestartNeedsNoButtons(t *testing.T) {
	dir := t.TempDir()
	p, f := newSplitCardFixture(t, true, map[string]any{"cc_data_dir": dir})
	ctx := context.Background()
	sc, err := p.CreateStreamingCard(ctx, replyContext{chatID: "oc_test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sc.Finalize(ctx, "FIRST\n"+strings.Repeat("line\n", 2000)+"LAST"); err != nil {
		t.Fatal(err)
	}
	fresh := restartQueryPlatform(t, p, dir)
	for id, old := range p.queryCards {
		restored, err := fresh.loadQueryCard(id)
		if err != nil {
			t.Fatal(err)
		}
		if !restored.unpaged || restored.part != old.part {
			t.Fatal("continuation format lost on restart")
		}
		data, err := restored.renderLocked()
		if err != nil || strings.Contains(data, "query_page:") {
			t.Fatal("new history requires pagination")
		}
	}
	if !strings.Contains(f.shown("om_1"), "FIRST") {
		t.Fatal("earlier content no longer visible")
	}
}

// Real Engine + SessionManager + Feishu HTTP adapter. The only mocks are the
// agent process and Feishu's remote API: request -> approve -> next request.
func TestQueryCard_ContinuationEngineJourney(t *testing.T) {
	p, f := newSplitCardFixture(t, true)
	p.userNameCache.Store("owner", "Owner")
	p.chatNameCache.Store("oc_private", "Private")
	a := &continuationAgent{session: &continuationAgentSession{events: make(chan core.Event, 16)}}
	e := core.NewEngine("test", a, []core.Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), core.LangChinese)
	t.Cleanup(func() { e.Stop() })
	e.SetDisplayConfig(core.DisplayCfg{ThinkingMessages: true, ToolMessages: true})
	p.handler = e.ReceiveMessage
	send := func(text string) {
		e.ReceiveMessage(p, &core.Message{UserID: "owner", SessionKey: "feishu:oc_private:owner", Content: text, ReplyCtx: replyContext{chatID: "oc_private", sessionKey: "feishu:oc_private:owner"}})
	}
	snapshot := func() []string {
		f.mu.Lock()
		n := f.creates
		f.mu.Unlock()
		out := make([]string, n)
		for i := range out {
			out[i] = f.shown(fmt.Sprintf("om_%d", i+1))
		}
		return out
	}
	wait := func(fn func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if fn() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("card journey timed out")
	}
	send("long task")
	wait(func() bool {
		cards := snapshot()
		return len(cards) > 1 && strings.Contains(cards[len(cards)-1], "perm:allow")
	})
	cards := snapshot()
	for _, text := range cards[:len(cards)-1] {
		if strings.Contains(text, "perm:allow") || strings.Contains(text, "query_page:") {
			t.Fatal("old process part has active controls")
		}
	}
	// Recover exactly the button value shown to the user, then click it.
	var doc any
	if err := json.Unmarshal([]byte(cards[len(cards)-1]), &doc); err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if m, ok := v["value"].(map[string]any); ok && m["action"] == "perm:allow" {
				value = m
			}
			for _, x := range v {
				walk(x)
			}
		case []any:
			for _, x := range v {
				walk(x)
			}
		}
	}
	walk(doc)
	if value == nil {
		t.Fatal("allow button missing")
	}
	click := panelClick(fmt.Sprintf("om_%d", len(cards)), "perm:allow")
	click.Event.Action.Value = value
	if _, err := p.onCardAction(click); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return strings.Contains(strings.Join(snapshot(), "\n"), "ANSWER_LAST") })
	before := snapshot()
	for _, text := range before {
		if strings.Contains(text, "query_page:") || strings.Contains(text, "perm:allow") {
			t.Fatal("finished cards retain controls")
		}
	}
	send("next task")
	wait(func() bool { return strings.Contains(strings.Join(snapshot(), "\n"), "NEXT_RESULT") })
	after := snapshot()
	if len(after) != len(before)+2 {
		t.Fatalf("short next task should use process + answer cards: %d -> %d", len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatal("next request overwrote previous cards")
		}
	}
	if !strings.Contains(strings.Join(before, "\n"), "PROCESS_FIRST") {
		t.Fatal("early process output disappeared")
	}
}

type continuationAgent struct {
	panelAgent
	session *continuationAgentSession
}

func (a *continuationAgent) StartSession(context.Context, string) (core.AgentSession, error) {
	return a.session, nil
}

type continuationAgentSession struct {
	events chan core.Event
	sent   atomic.Int32
	closed atomic.Bool
}

func (s *continuationAgentSession) Send(string, string, []core.ImageAttachment, []core.FileAttachment) error {
	if s.sent.Add(1) == 1 {
		s.events <- core.Event{Type: core.EventThinking, Content: "PROCESS_FIRST\n" + strings.Repeat("process line\n", 500) + "PROCESS_LAST"}
		s.events <- core.Event{Type: core.EventPermissionRequest, RequestID: "approve-one", ToolName: "Bash", ToolInput: "pwd"}
	} else {
		s.events <- core.Event{Type: core.EventResult, Content: "NEXT_RESULT", Done: true}
	}
	return nil
}
func (s *continuationAgentSession) RespondPermission(_ string, r core.PermissionResult) error {
	if r.Behavior == "allow" {
		s.events <- core.Event{Type: core.EventResult, Content: "ANSWER_FIRST\n" + strings.Repeat("answer line\n", 500) + "ANSWER_LAST", Done: true}
	}
	return nil
}
func (s *continuationAgentSession) Events() <-chan core.Event { return s.events }
func (s *continuationAgentSession) CurrentSessionID() string  { return "continuation-session" }
func (s *continuationAgentSession) Alive() bool               { return !s.closed.Load() }
func (s *continuationAgentSession) Close() error              { s.closed.Store(true); return nil }

func TestWorkflow_TaskRetryReusesCardsAcrossRestart(t *testing.T) {
	p, f := newSplitCardFixture(t, true)
	p.queryCardStateDir = t.TempDir()
	ctx := context.Background()
	rc := replyContext{chatID: "oc_test", sessionKey: "feishu:oc_test:root:om_root", messageID: "om_root"}
	p.SetTaskThreadResolver(func(chat, root string) bool { return chat == "oc_test" && root == "om_root" })
	p.threadIsolation = false
	p.noReplyToTrigger = true
	c, err := p.CreateTaskStreamingCard(ctx, rc, "project-T001")
	if err != nil {
		t.Fatal(err)
	}
	if p.shouldReplyInThread(rc) || !p.shouldUseThreadOrReplyAPI(rc) {
		t.Fatal("task lost ordinary quoted reply routing")
	}
	if err = c.(core.FinalAnswerCard).FinalizeWithAnswer(ctx, "Process 1", "Answer 1"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	count := len(f.messages)
	f.mu.Unlock()
	// Simulate memory loss and expiry of ordinary query cards.
	p.queryCardsMu.Lock()
	p.queryCards = nil
	p.queryCardsMu.Unlock()
	p.pruneQueryCardSnapshots()
	c, err = p.CreateTaskStreamingCard(ctx, rc, "project-T001")
	if err != nil {
		t.Fatal(err)
	}
	if err = c.(core.FinalAnswerCard).FinalizeWithAnswer(ctx, "Process 2", "Answer 2"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	after := len(f.messages)
	f.mu.Unlock()
	if count != 2 || after != count {
		t.Fatalf("retry created more cards: %d -> %d", count, after)
	}
	if !strings.Contains(f.shown("om_1"), "Process 2") || !strings.Contains(f.shown("om_2"), "Answer 2") {
		t.Fatal("retry did not update existing cards")
	}
}
func TestWorkflow_OverviewUpdateDoesNotCreateReplacement(t *testing.T) {
	p, f := newSplitCardFixture(t, false)
	ctx := context.Background()
	card := core.NewCard().Title("Project", "blue").Markdown("Initial").Build()
	id, err := p.UpsertWorkflowCard(ctx, "oc_test", "", "project-board", card)
	if err != nil {
		t.Fatal(err)
	}
	card = core.NewCard().Title("Project", "blue").Markdown("Updated").Build()
	_, err = p.UpsertWorkflowCard(ctx, "oc_test", id, "project-board", card)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.failUpdate = true
	before := len(f.messages)
	f.mu.Unlock()
	if _, err = p.UpsertWorkflowCard(ctx, "oc_test", id, "project-board", card); err == nil {
		t.Fatal("update failure hidden")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.messages) != before {
		t.Fatal("update failure created replacement")
	}
}
