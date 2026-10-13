package feishu

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestExecutionActivityFollowsContentAndTracksTools(t *testing.T) {
	p, f := newSplitCardFixture(t, true)
	p.cardLanguage = func() core.Language { return core.LangChinese }
	c := &queryCardStream{p: p.Platform, native: true, rc: replyContext{chatID: "oc_group"}}
	cleanupExecutionStream(t, c)
	blocks := []core.ExecutionBlock{{Text: "检查实现", Done: true}, {Tool: "command_execution", Command: "go test ./core", ID: "test"}}
	check := func(want string) {
		t.Helper()
		c.lastWrite = time.Time{}
		if err := c.UpdateExecution(context.Background(), blocks); err != nil {
			t.Fatal(err)
		}
		var card map[string]any
		if err := json.Unmarshal([]byte(f.shown("om_1")), &card); err != nil {
			t.Fatal(err)
		}
		elements := card["body"].(map[string]any)["elements"].([]any)
		if elements[0].(map[string]any)["content"] != "检查实现" {
			t.Fatal("activity still precedes public content")
		}
		if !strings.Contains(elements[len(elements)-1].(map[string]any)["content"].(string), want) {
			t.Fatalf("bottom activity does not show %q: %s", want, f.shown("om_1"))
		}
	}
	check("工具执行中")
	blocks[1].Done = true
	check("等待模型响应")
	blocks = append(blocks, core.ExecutionBlock{Text: "正在汇总"})
	check("正在整理回复")
}

func TestExecutionEmptyTurnDoesNotPublishStatusOnlyCard(t *testing.T) {
	for _, task := range []bool{false, true} {
		p, f := newSplitCardFixture(t, false)
		p.SetTaskThreadResolver(func(string, string) bool { return true })
		rc := replyContext{chatID: "oc_group", sessionKey: "feishu:oc_group:root:source"}
		var sc core.StreamingCard
		var err error
		if task {
			sc, err = p.CreateTaskStreamingCard(context.Background(), rc, "task")
		} else {
			sc, err = p.CreateStreamingCard(context.Background(), rc)
		}
		if err != nil {
			t.Fatal(err)
		}
		c := sc.(*queryCardStream)
		cleanupExecutionStream(t, c)
		if err := c.UpdateExecution(context.Background(), []core.ExecutionBlock{{Text: " \n\t"}}); err != nil {
			t.Fatal(err)
		}
		if err := c.FinishExecution(context.Background(), nil, "", core.CardStatusDone); err != nil {
			t.Fatal(err)
		}
		if len(f.messages) != 0 || len(c.cards) != 0 {
			t.Fatal("empty turn left a status-only message")
		}
	}
}

func TestResultMarkdownWithoutHumanMentionPreservesQuoteAndFormatting(t *testing.T) {
	p, f := newSplitCardFixture(t, false)
	body := "**结论已通过**\n\n- 验证完成\n\n```sh\necho ok\n```\n[详情](https://example.com/result)"
	if _, err := p.SendQuotedResultMessage(context.Background(), "oc_group", "feishu:oc_group:root:source", "", body, "result"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 1 {
		t.Fatal("result was split or duplicated")
	}
	r := f.requests[0]
	if !strings.HasSuffix(r["path"].(string), "/source/reply") || r["reply_in_thread"] != false {
		t.Fatal("result lost its ordinary quoted reply")
	}
	var post map[string]any
	if err := json.Unmarshal([]byte(r["content"].(string)), &post); err != nil {
		t.Fatal(err)
	}
	rows := post["zh_cn"].(map[string]any)["content"].([]any)
	if len(rows) != 1 {
		t.Fatal("ordinary result inserted a mention row")
	}
	md := rows[0].([]any)[0].(map[string]any)
	if md["tag"] != "md" || md["text"] != body {
		t.Fatal("result is plain text or Markdown content was rewritten")
	}
}

func TestExecutionTemporaryNoticeDoesNotLeaveAnEmptyCompletedCard(t *testing.T) {
	for _, entity := range []bool{false, true} {
		p, f := newSplitCardFixture(t, entity)
		c := &queryCardStream{p: p.Platform, native: true, rc: replyContext{chatID: "oc_group"}}
		cleanupExecutionStream(t, c)
		ctx := context.Background()
		if err := c.SetNotice(ctx, "等待模型响应"); err != nil {
			t.Fatal(err)
		}
		if len(f.messages) != 1 {
			t.Fatal("meaningful wait notice must remain visible")
		}
		if err := c.SetNotice(ctx, ""); err != nil {
			t.Fatal(err)
		}
		if err := c.FinishExecution(ctx, nil, "Final answer", core.CardStatusDone); err != nil {
			t.Fatal(err)
		}
		if len(f.messages) != 1 || !strings.Contains(f.shown("om_2"), "Final answer") || f.shown("om_1") != "" {
			t.Fatal("temporary notice left an empty completed process card beside the result")
		}
	}
}
