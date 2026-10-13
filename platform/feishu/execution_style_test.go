package feishu

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestExecutionEmptyStartWaitsForContentThenCreatesOneCard(t *testing.T) {
	p, f := newSplitCardFixture(t, true)
	c := &queryCardStream{p: p.Platform, native: true, rc: replyContext{chatID: "oc_group"}}
	cleanupExecutionStream(t, c)
	if err := c.UpdateExecution(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(f.messages) != 0 || len(c.cards) != 0 {
		t.Fatal("empty execution created a status-only card")
	}
	c.lastWrite = time.Time{}
	if err := c.UpdateExecution(context.Background(), []core.ExecutionBlock{{Text: "Now checking the implementation."}}); err != nil {
		t.Fatal(err)
	}
	if len(c.cards) != 1 || len(f.messages) != 1 {
		t.Fatal("first output must create exactly one card")
	}
	var card map[string]any
	if err := json.Unmarshal([]byte(f.shown("om_1")), &card); err != nil {
		t.Fatal(err)
	}
	if card["config"].(map[string]any)["width_mode"] == "compact" || !strings.Contains(f.shown("om_1"), "Now checking the implementation.") {
		t.Fatal("first output remained constrained to the loading placeholder")
	}
	assertExecutionFits(t, c.cards[0])
}

// Exercise the delivered payload rather than a second HTML implementation.
// Optional exports are fed to CardKit for real platform schema validation.
func TestExecutionStyledControlsKeepQuestionsWithoutCommandLinks(t *testing.T) {
	p, f := newSplitCardFixture(t, true)
	p.cardLanguage = func() core.Language { return core.LangChinese }
	c := &queryCardStream{p: p.Platform, native: true, rc: replyContext{chatID: "oc_group"}}
	cleanupExecutionStream(t, c)
	blocks := []core.ExecutionBlock{
		{Text: "先检查回复逻辑，再验证卡片容量和更新行为。", Done: true},
		{Tool: "command_execution", Command: "rg -n 'reply_in_thread|ReplyInThread' platform/feishu", Output: "platform/feishu/feishu.go: ordinary replies enabled", Done: true, DurationMS: 200},
		{Tool: "command_execution", Command: "go test ./platform/feishu -run TestExecution", Output: "=== RUN TestExecution\nPASS", Status: "in_progress"},
	}
	if err := c.UpdateExecution(context.Background(), blocks); err != nil {
		t.Fatal(err)
	}
	part := c.cards[0]
	save := func(name string) map[string]any {
		t.Helper()
		var card map[string]any
		raw := f.shown("om_1")
		if err := json.Unmarshal([]byte(raw), &card); err != nil {
			t.Fatal(err)
		}
		if part.view.Key == "" {
			if strings.Contains(raw, ":open:") || strings.Contains(raw, p.cardI18n().Tf(core.MsgExecSummaryHint, 2)) {
				t.Fatal("command summary still advertises or links to details")
			}
			if !strings.Contains(raw, "rg -n") || !strings.Contains(raw, "go test ./platform/feishu") {
				t.Fatal("removing command links also removed command previews")
			}
		}
		if dir := os.Getenv("CC_CARD_VISUAL_FIXTURE_DIR"); dir != "" {
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
		}
		assertExecutionFits(t, part)
		return card
	}
	save("summary")
	executionClick(t, part, "open", "b1")
	save("details")
	prompt := core.NewCard().Title("等待你授权", "orange").Markdown("**操作：** 发布已经确认的改动。\n**范围：** 仅本次测试环境发布。").Buttons(core.PrimaryBtn("同意本次", "perm:allow"), core.DefaultBtn("拒绝", "perm:deny")).Build()
	if err := c.SetPrompt(context.Background(), prompt); err != nil {
		t.Fatal(err)
	}
	save("approval")
	prompt = core.NewCard().Title("等待你补充", "orange").Markdown("需要覆盖哪些客户端？").Buttons(core.DefaultBtn("桌面端", "askq:0:0"), core.DefaultBtn("移动端", "askq:0:1")).Build()
	prompt.DecisionToken = rand.Text()
	if err := c.SetPrompt(context.Background(), prompt); err != nil {
		t.Fatal(err)
	}
	action := fmt.Sprintf("exec:%s:back::%d", part.id, part.view.Revision)
	if err := p.navigateExecution(action, "oc_group", "om_1", "ou_owner", map[string]any{"answer": "重点检查手机端长命令换行"}); err != nil {
		t.Fatal(err)
	}
	card := save("question")
	buttons, submit, commands, columns := 0, 0, 0, 0
	names := map[string]bool{}
	var inspect func(any)
	inspect = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if id, _ := x["element_id"].(string); len(id) > 20 {
				t.Fatalf("native element ID exceeds platform limit: %d", len(id))
			}
			if x["tag"] == "column_set" {
				columns++
			}
			if x["tag"] == "button" {
				buttons++
				name, _ := x["name"].(string)
				if x["form_action_type"] != "submit" || name == "" || names[name] {
					t.Fatal("nested navigation no longer submits the draft uniquely")
				}
				names[name] = true
				value := x["behaviors"].([]any)[0].(map[string]any)["value"].(map[string]any)
				if value["action"] == "question_submit" {
					submit++
					if value["permission_token"] != prompt.DecisionToken || value["execution_card"] != part.id {
						t.Fatal("question lost its original request")
					}
				}
				if a, _ := value["action"].(string); strings.Contains(a, ":open:") {
					commands++
					label := x["text"].(map[string]any)["content"].(string)
					if strings.Contains(label, "<font") || strings.Contains(label, "**") {
						t.Fatal("command form label leaks Markdown styling")
					}
				}
			}
			for _, child := range x {
				inspect(child)
			}
		case []any:
			for _, child := range x {
				inspect(child)
			}
		}
	}
	inspect(card)
	if submit != 1 || commands != 0 || buttons != 3 || columns < 2 || part.decisionDraft != "重点检查手机端长命令换行" {
		t.Fatalf("styled question lost controls or draft: submit=%d commands=%d buttons=%d columns=%d", submit, commands, buttons, columns)
	}
}
