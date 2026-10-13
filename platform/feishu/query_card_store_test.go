package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func restartQueryPlatform(t *testing.T, p *interactivePlatform, dir string) *interactivePlatform {
	t.Helper()
	platform, err := New(map[string]any{"app_id": p.appID, "app_secret": "test", "separate_final_card": true, "cc_data_dir": dir})
	if err != nil {
		t.Fatal(err)
	}
	fresh := platform.(*interactivePlatform)
	fresh.client = p.client
	fresh.SetCardLanguageProvider(func() core.Language { return core.LangChinese })
	return fresh
}

func TestQueryCard_RestartRestoresProcessAndAnswerPages(t *testing.T) {
	for _, entity := range []bool{true, false} {
		t.Run(fmt.Sprint(entity), func(t *testing.T) {
			dir := t.TempDir()
			p, shown := newSplitCardFixture(t, entity, map[string]any{"cc_data_dir": dir})
			ctx := context.Background()
			sc, err := createLegacyStreamingCard(p, ctx, replyContext{chatID: "oc_private", sessionKey: "feishu:oc_private:owner"})
			if err != nil {
				t.Fatal(err)
			}
			c := sc.(*queryCard)
			progress := "PROGRESS_FIRST\n" + strings.Repeat("progress line\n", 700) + "PROGRESS_LAST"
			answer := "ANSWER_FIRST\n" + strings.Repeat("answer line\n", 700) + "ANSWER_LAST"
			if err := c.FinalizeWithAnswer(ctx, progress, answer); err != nil {
				t.Fatal(err)
			}
			var answerID string
			for id, card := range p.queryCards {
				if card.answer {
					answerID = id
				}
			}
			if answerID == "" {
				t.Fatal("answer missing")
			}
			fresh := restartQueryPlatform(t, p, dir)
			for _, tc := range []struct{ id, message, marker string }{{c.id, "om_1", "PROGRESS_LAST"}, {answerID, "om_2", "ANSWER_LAST"}} {
				resp, err := fresh.onCardAction(panelClick(tc.message, "query_page:"+tc.id+":latest"))
				if err != nil || resp != nil && resp.Toast != nil {
					t.Fatalf("restored callback failed: %v %#v", err, resp)
				}
				if !strings.Contains(shown.shown(tc.message), tc.marker) {
					t.Fatal("restart lost page content")
				}
				file := filepath.Join(fresh.queryCardStateDir, tc.id+".json")
				info, err := os.Stat(file)
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatal("snapshot is not private")
				}
				data, _ := os.ReadFile(file)
				var s queryCardSnapshot
				if err := json.Unmarshal(data, &s); err != nil {
					t.Fatal(err)
				}
				if s.CardID != "" && s.Sequence < 1 {
					t.Fatal("entity sequence not preserved")
				}
			}
			// A second restart still has both messages and their independent pages.
			second := restartQueryPlatform(t, fresh, dir)
			if err := second.pageQueryCard("query_page:"+c.id+":0", "oc_private", "om_1"); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(shown.shown("om_1"), "PROGRESS_FIRST") || !strings.Contains(shown.shown("om_2"), "ANSWER_LAST") {
				t.Fatal("independent pagination lost after restart")
			}
		})
	}
}

func TestQueryCard_RestartDoesNotRestoreApproval(t *testing.T) {
	dir := t.TempDir()
	p, shown := newSplitCardFixture(t, false, map[string]any{"cc_data_dir": dir})
	sc, err := createLegacyStreamingCard(p, context.Background(), replyContext{chatID: "oc_private", sessionKey: "feishu:oc_private:owner"})
	if err != nil {
		t.Fatal(err)
	}
	c := sc.(*queryCard)
	if err := c.Update(context.Background(), strings.Repeat("line\n", 2000)+"LAST"); err != nil {
		t.Fatal(err)
	}
	prompt := core.NewCard().Markdown("PRIVATE_APPROVAL_PROMPT").Build()
	prompt.Elements = append(prompt.Elements, core.CardActions{Buttons: []core.CardButton{{Text: "Allow", Value: "perm:allow", Extra: map[string]string{"permission_token": "PRIVATE_TOKEN"}}}})
	if err := c.SetPrompt(context.Background(), prompt); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(p.queryCardStateDir, c.id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "PRIVATE_") {
		t.Fatal("approval controls persisted")
	}
	fresh := restartQueryPlatform(t, p, dir)
	if err := fresh.pageQueryCard("query_page:"+c.id+":latest", "oc_private", "om_1"); err != nil {
		t.Fatal(err)
	}
	text := shown.shown("om_1")
	if !strings.Contains(text, "LAST") || !strings.Contains(text, "服务重启前") || strings.Contains(text, "perm:allow") {
		t.Fatal("unsafe or incomplete restored card")
	}
	before := text
	if err := fresh.pageQueryCard("query_page:"+c.id+":0", "wrong-chat", "om_1"); err == nil {
		t.Fatal("cross-chat page allowed")
	}
	if err := fresh.pageQueryCard("query_page:"+c.id+":0", "oc_private", "wrong-message"); err == nil {
		t.Fatal("cross-message page allowed")
	}
	if shown.shown("om_1") != before {
		t.Fatal("invalid callback changed card")
	}
}

func TestQueryCard_PageFailuresShowFeedback(t *testing.T) {
	p, shown := newSplitCardFixture(t, false)
	sc, err := createLegacyStreamingCard(p, context.Background(), replyContext{chatID: "oc_private", sessionKey: "feishu:oc_private:owner"})
	if err != nil {
		t.Fatal(err)
	}
	c := sc.(*queryCard)
	if err := c.Finalize(context.Background(), strings.Repeat("line\n", 2000)); err != nil {
		t.Fatal(err)
	}
	click := panelClick("om_1", "query_page:"+c.id+":1")
	c.mu.Lock()
	start := time.Now()
	resp, err := p.onCardAction(click)
	c.mu.Unlock()
	if err != nil || resp == nil || resp.Toast == nil || !strings.Contains(resp.Toast.Content, "正在更新") || time.Since(start) > time.Second {
		t.Fatal("busy callback blocked or gave no feedback")
	}
	shown.mu.Lock()
	shown.failUpdate = true
	shown.mu.Unlock()
	resp, err = p.onCardAction(click)
	if err != nil || resp == nil || resp.Toast == nil || !strings.Contains(resp.Toast.Content, "更新失败") {
		t.Fatal("API failure was silent")
	}
}

func TestQueryCard_ExpiredAndInvalidSnapshots(t *testing.T) {
	dir := t.TempDir()
	p, _ := newSplitCardFixture(t, false, map[string]any{"cc_data_dir": dir})
	c, err := p.createQueryCard(context.Background(), replyContext{chatID: "oc_private"}, strings.Repeat("line\n", 1000), true)
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.created = time.Now().Add(-25 * time.Hour)
	err = c.persistLocked(false)
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	fresh := restartQueryPlatform(t, p, dir)
	for _, id := range []string{c.id, "../escape", "missing"} {
		if _, err := fresh.loadQueryCard(id); err == nil {
			t.Fatalf("invalid snapshot accepted: %q", id)
		}
	}
	file := filepath.Join(p.queryCardStateDir, c.id+".json")
	if err := os.Chtimes(file, time.Now().Add(-25*time.Hour), time.Now().Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	fresh.pruneQueryCardSnapshots()
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("expired snapshot retained")
	}
}
