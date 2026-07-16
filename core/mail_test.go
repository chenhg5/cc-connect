package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMailArgs(t *testing.T) {
	opts, err := parseMailArgs(nil)
	if err != nil || opts.Thread != "" || opts.Since != "" {
		t.Fatalf("empty args: opts=%+v err=%v", opts, err)
	}

	opts, err = parseMailArgs([]string{"--thread", "cc-connect-maintenance", "--since", "07-15"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Thread != "cc-connect-maintenance" || opts.Since != "07-15" {
		t.Fatalf("got %+v", opts)
	}

	opts, err = parseMailArgs([]string{"-t", "foo", "-s", "2026-07-15"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Thread != "foo" || opts.Since != "2026-07-15" {
		t.Fatalf("got %+v", opts)
	}

	opts, err = parseMailArgs([]string{"--thread=bar", "--since=07-16"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Thread != "bar" || opts.Since != "07-16" {
		t.Fatalf("got %+v", opts)
	}

	if _, err := parseMailArgs([]string{"--thread"}); err == nil {
		t.Fatal("expected error for missing --thread value")
	}
	if _, err := parseMailArgs([]string{"--bogus"}); err == nil {
		t.Fatal("expected error for unknown arg")
	}
}

func TestNormalizeIndexDateAndCompare(t *testing.T) {
	if got := normalizeIndexDate("2026-07-15"); got != "07-15" {
		t.Fatalf("normalize YYYY-MM-DD: %q", got)
	}
	if got := normalizeIndexDate("07-15"); got != "07-15" {
		t.Fatalf("normalize MM-DD: %q", got)
	}
	if !dateOnOrAfter("07-16", "07-15") {
		t.Fatal("07-16 should be >= 07-15")
	}
	if dateOnOrAfter("07-14", "07-15") {
		t.Fatal("07-14 should not be >= 07-15")
	}
	if !dateOnOrAfter("2026-07-16", "07-15") {
		t.Fatal("YYYY-MM-DD should compare via MM-DD")
	}
}

func sampleIndexTail() string {
	return strings.Join([]string{
		"| ID | Type | Thread | Parent | 一句话摘要 | Date |",
		"|---|---|---|---|---|---|",
		"| L-0430 | QUERY | cc-connect-maintenance | L-0410 | 实现 Option C toast+DM | 07-15 |",
		"| L-0430 | RESULT | cc-connect-maintenance | L-0410 | STUCK: 基线失败待确认 | 07-16 |",
		"| L-0431 | QUERY | cc-connect-permission-mode | L-0404 | Telegram 429 | 07-15 |",
		"| L-0431 | RESULT | cc-connect-permission-mode | L-0404 | DONE: 建议 B+A+C | 07-16 |",
		"| L-0435 | QUERY | cc-connect-repo-strategy | L-0407 | footer 断言 | 07-15 |",
		"| L-0435 | RESULT | cc-connect-repo-strategy | L-0407 | DONE: footer 对齐 | 07-15 |",
		"| L-0435 | CLOSED | cc-connect-repo-strategy | L-0407 | ✅ 封信 | 07-15 |",
		"| L-0440 | QUERY | cc-connect-maintenance | L-0430 | 实现 /mail 内置命令 | 07-16 |",
		"| — | NOTE | cc-connect-maintenance | L-0439 | 暂不处理 | 07-16 |",
	}, "\n")
}

func TestCollectActiveMailLetters(t *testing.T) {
	letters := collectActiveMailLetters(sampleIndexTail(), mailOpts{})
	ids := map[string]mailLetter{}
	for _, l := range letters {
		ids[l.ID] = l
	}
	if _, ok := ids["L-0435"]; ok {
		t.Fatal("CLOSED letter L-0435 should be excluded")
	}
	if l, ok := ids["L-0440"]; !ok || l.Type != "QUERY" || l.Status != "OPEN" {
		t.Fatalf("L-0440: %+v", l)
	}
	if l, ok := ids["L-0430"]; !ok || l.Status != "STUCK" {
		t.Fatalf("L-0430 should be STUCK: %+v", l)
	}
	if l, ok := ids["L-0431"]; !ok || l.Status != "DONE" {
		t.Fatalf("L-0431 should be DONE awaiting close: %+v", l)
	}
}

func TestCollectActiveMailLetters_ThreadAndSince(t *testing.T) {
	letters := collectActiveMailLetters(sampleIndexTail(), mailOpts{
		Thread: "cc-connect-maintenance",
		Since:  "07-16",
	})
	if len(letters) != 2 {
		t.Fatalf("want 2 letters in maintenance since 07-16, got %d: %+v", len(letters), letters)
	}
	for _, l := range letters {
		if l.Thread != "cc-connect-maintenance" {
			t.Fatalf("wrong thread: %+v", l)
		}
		if normalizeIndexDate(l.Date) < "07-16" {
			t.Fatalf("date filter failed: %+v", l)
		}
	}
}

func TestFormatMailOverview(t *testing.T) {
	letters := collectActiveMailLetters(sampleIndexTail(), mailOpts{})
	body := formatMailOverview(letters, mailOpts{}, 200)
	if !strings.Contains(body, "## cc-connect-maintenance") {
		t.Fatalf("missing thread header:\n%s", body)
	}
	if !strings.Contains(body, "L-0440 QUERY [OPEN]") {
		t.Fatalf("missing L-0440 line:\n%s", body)
	}
	if strings.Contains(body, "L-0435") {
		t.Fatalf("CLOSED letter leaked:\n%s", body)
	}
}

func TestCmdMail_ZeroAIPath(t *testing.T) {
	dir := t.TempDir()
	indexPath := filepath.Join(dir, "INDEX.md")
	if err := os.WriteFile(indexPath, []byte(sampleIndexTail()+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &stubPlatformEngine{n: "plain"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangChinese)
	e.notifyConfig = NotifyConfig{IndexPath: indexPath}
	msg := &Message{SessionKey: "telegram:1:1", ReplyCtx: "ctx", Content: "/mail"}

	e.cmdMail(p, msg, nil)
	sent := p.getSent()
	if len(sent) == 0 {
		t.Fatal("expected a reply")
	}
	got := strings.Join(sent, "\n")
	if !strings.Contains(got, "L-0440") {
		t.Fatalf("reply missing L-0440:\n%s", got)
	}

	p.clearSent()
	e.cmdMail(p, msg, []string{"--thread", "cc-connect-maintenance"})
	got = strings.Join(p.getSent(), "\n")
	if !strings.Contains(got, "筛选 thread: cc-connect-maintenance") {
		t.Fatalf("thread filter banner missing:\n%s", got)
	}
	if strings.Contains(got, "cc-connect-permission-mode") {
		t.Fatalf("other threads leaked:\n%s", got)
	}

	if id := matchPrefix("inbox", builtinCommands); id != "mail" {
		t.Fatalf("inbox alias = %q", id)
	}
	if id := matchPrefix("mail", builtinCommands); id != "mail" {
		t.Fatalf("mail id = %q", id)
	}
}

func TestCmdMail_NoArchive(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "telegram:1:1", ReplyCtx: "ctx"}
	e.cmdMail(p, msg, nil)
	sent := p.getSent()
	if len(sent) != 1 || !strings.Contains(sent[0], "No letter archive") {
		t.Fatalf("want no-archive message, got %q", sent)
	}
}

func TestParseIndexRow_CapturesDate(t *testing.T) {
	r, ok := parseIndexRow("| L-0440 | QUERY | cc-connect-maintenance | L-0430 | 实现 /mail | 07-16 |")
	if !ok {
		t.Fatal("parse failed")
	}
	if r.date != "07-16" {
		t.Fatalf("date = %q", r.date)
	}
}
