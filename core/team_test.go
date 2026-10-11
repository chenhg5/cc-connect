package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestChatOf(t *testing.T) {
	if chatOf("discord:123:456") != chatOf("discord:123") {
		t.Fatal("per-user and shared keys of one channel should match")
	}
	if chatOf("discord:123") == chatOf("discord:999") {
		t.Fatal("different channels must differ")
	}
}

func writeTeamFile(t *testing.T, dir string, f teamFile) {
	t.Helper()
	b, _ := json.Marshal(f)
	if err := os.WriteFile(filepath.Join(dir, f.Project+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTeamSummary(t *testing.T) {
	dir := t.TempDir()
	e := newTestEngine()
	e.name = "maomao"
	e.team = &teamState{dir: dir, name: "橘", inject: true, seen: map[string]*teamSession{}}
	now := time.Now()
	writeTeamFile(t, dir, teamFile{Project: "longniang", Name: "龙娘", Updated: now, Sessions: []teamEntry{
		{Key: "discord:111", Chat: "general", Task: "修 bug", Busy: true, Since: now.Add(-3 * time.Minute)},
		{Key: "discord:222", Chat: "dev", Task: "写测试", DoneAt: now.Add(-2 * time.Minute)},
	}})
	writeTeamFile(t, dir, teamFile{Project: "jingyu", Name: "鲸鱼娘", Updated: now.Add(-2 * time.Hour), Sessions: []teamEntry{
		{Key: "discord:111", Busy: true, Since: now},
	}})
	writeTeamFile(t, dir, teamFile{Project: "maomao", Name: "橘", Updated: now, Sessions: []teamEntry{
		{Key: "discord:111:9", Busy: true, Since: now},
	}})

	got := e.teamSummary("discord:111:727")
	for _, want := range []string{"龙娘：在本频道忙了 3 分钟 「修 bug」", "龙娘：2 分钟前在#dev做完 「写测试」"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "鲸鱼娘") {
		t.Errorf("stale file should be ignored:\n%s", got)
	}
	if strings.Count(got, "\n") != 2 {
		t.Errorf("own file should be skipped:\n%s", got)
	}

	e.team.inject = false
	if e.teamSummary("discord:111") != "" {
		t.Error("inject off should give no summary")
	}
}

func TestApplyGateBotContextNeverStartsTurn(t *testing.T) {
	e := newTestEngine()
	e.gate = &gateState{url: "http://127.0.0.1:1/never", timeout: time.Millisecond, fallback: "reply",
		buf: map[string][]string{}, held: map[string]*heldMsg{}, released: map[*Message]bool{}}
	s := &Session{}
	msg := &Message{SessionKey: "discord:1", Content: "【龙娘】做完了", FromBot: true}
	if !e.applyGate(nil, msg, s, "k") {
		t.Fatal("bot context must be consumed")
	}
	if got := e.gate.buf["k"]; len(got) != 1 || got[0] != "【龙娘】做完了" {
		t.Fatalf("buffer = %v", got)
	}
	e.gate = nil
	if !e.applyGate(nil, &Message{Content: "x", FromBot: true}, s, "k") {
		t.Fatal("without a gate, bot context must still never start a turn")
	}
}
