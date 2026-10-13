package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func foldElements(t *testing.T, text string) []map[string]any {
	t.Helper()
	var doc struct {
		Schema string `json:"schema"`
		Body   struct {
			Elements []map[string]any `json:"elements"`
		} `json:"body"`
	}
	if err := json.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Schema != "2.0" {
		t.Fatal("fold requires card 2.0")
	}
	return doc.Body.Elements
}
func TestQueryCard_FoldKeepsSummaryAlertsAndApprovalsVisible(t *testing.T) {
	for _, entity := range []bool{true, false} {
		t.Run(fmt.Sprint(entity), func(t *testing.T) {
			p, shown := newSplitCardFixture(t, entity)
			sc, err := p.CreateStreamingCard(context.Background(), replyContext{chatID: "oc_test"})
			if err != nil {
				t.Fatal(err)
			}
			sc.(core.ProgressSummaryCard).SetProgressSummary("Jordan · fixing login · 2/4")
			const details = "FULL_PLAN_START\ninspect\nimplement\ntest\nFULL_PLAN_END"
			if err := sc.Update(context.Background(), details); err != nil {
				t.Fatal(err)
			}
			ic := sc.(core.InteractiveStreamingCard)
			prompt := core.NewCard().Markdown("HUMAN_DECISION").Buttons(core.PrimaryBtn("Allow", "perm:allow")).Build()
			if err := ic.SetPrompt(context.Background(), prompt); err != nil {
				t.Fatal(err)
			}
			if err := ic.SetNotice(context.Background(), "QUEUE_WARNING"); err != nil {
				t.Fatal(err)
			}
			elements := foldElements(t, shown.shown("om_1"))
			var top string
			var panel map[string]any
			for _, el := range elements {
				if el["tag"] == "collapsible_panel" {
					panel = el
					continue
				}
				b, _ := json.Marshal(el)
				top += string(b)
			}
			for _, expected := range []string{"Jordan", "2/4", "HUMAN_DECISION", "perm:allow", "QUEUE_WARNING"} {
				if !strings.Contains(top, expected) {
					t.Fatalf("important content hidden: %s", expected)
				}
			}
			if strings.Contains(top, "FULL_PLAN") || panel == nil || panel["expanded"] != false {
				t.Fatal("details not folded by default")
			}
			data, _ := json.Marshal(panel)
			for _, marker := range []string{"FULL_PLAN_START", "FULL_PLAN_END"} {
				if !strings.Contains(string(data), marker) {
					t.Fatal("fold truncated details")
				}
			}
			if strings.Contains(string(data), "callback") {
				t.Fatal("native fold should not depend on a callback")
			}
			if err := sc.(core.FinalAnswerCard).FinalizeWithAnswer(context.Background(), details, "FINAL_DELIVERY"); err != nil {
				t.Fatal(err)
			}
			answer := shown.shown("om_2")
			if strings.Contains(answer, "collapsible_panel") || !strings.Contains(answer, "FINAL_DELIVERY") {
				t.Fatal("answer unexpectedly folded")
			}
			shown.mu.Lock()
			defer shown.mu.Unlock()
			if len(shown.messages) != 2 {
				t.Fatal("fold created a duplicate card")
			}
		})
	}
}
func TestQueryCard_FoldOverflowAndRestartKeepAllDetails(t *testing.T) {
	dir := t.TempDir()
	p, shown := newSplitCardFixture(t, true, map[string]any{"cc_data_dir": dir})
	sc, err := p.CreateStreamingCard(context.Background(), replyContext{chatID: "oc_test", sessionKey: "feishu:oc_test:owner"})
	if err != nil {
		t.Fatal(err)
	}
	sc.(core.ProgressSummaryCard).SetProgressSummary("OWNER_AND_STATE")
	content := "FIRST_DETAIL\n" + strings.Repeat("complete tool result\n", 450) + "LAST_DETAIL"
	if err := sc.(core.FinalAnswerCard).FinalizeWithAnswer(context.Background(), content, ""); err != nil {
		t.Fatal(err)
	}
	stream := sc.(*queryCardStream)
	if len(stream.cards) < 2 {
		t.Fatal("overflow was truncated")
	}
	fresh := restartQueryPlatform(t, p, dir)
	var restored []string
	for n, part := range stream.cards {
		data := shown.shown(fmt.Sprintf("om_%d", n+1))
		foldElements(t, data)
		if !strings.Contains(data, "collapsible_panel") || strings.Contains(data, "query_page:") {
			t.Fatal("overflow uses broken pagination")
		}
		c, err := fresh.loadQueryCard(part.id)
		if err != nil {
			t.Fatal(err)
		}
		if c.summary != "OWNER_AND_STATE" {
			t.Fatal("summary lost after restart")
		}
		restored = append(restored, c.content)
	}
	if strings.Join(restored, "\n") != strings.Join(core.SplitMessageCodeFenceAware(content, queryPageRunes), "\n") {
		t.Fatal("full detail lost")
	}
}

func TestQueryCard_FoldNeverHidesAnswerOnDeliveryFailure(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint(legacy), func(t *testing.T) {
			p, shown := newSplitCardFixture(t, true)
			ctx := context.Background()
			var sc core.StreamingCard
			var err error
			if legacy {
				sc, err = p.createQueryCard(ctx, replyContext{chatID: "oc_test"}, "", false)
			} else {
				sc, err = p.CreateStreamingCard(ctx, replyContext{chatID: "oc_test"})
			}
			if err != nil {
				t.Fatal(err)
			}
			sc.(core.ProgressSummaryCard).SetProgressSummary("WORKING_SUMMARY")
			shown.mu.Lock()
			shown.failCreate = true
			shown.mu.Unlock()
			if err := sc.(core.FinalAnswerCard).FinalizeWithAnswer(ctx, "PROCESS_DETAILS", "FINAL_ANSWER"); err == nil {
				t.Fatal("expected delivery failure")
			}
			data := shown.shown("om_1")
			if !strings.Contains(data, "FINAL_ANSWER") || strings.Contains(data, "collapsible_panel") || strings.Contains(data, "WORKING_SUMMARY") {
				t.Fatal("fallback answer hidden by old summary or fold")
			}
		})
	}
}
