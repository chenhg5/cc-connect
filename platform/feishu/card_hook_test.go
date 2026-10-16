package feishu

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeHookScript(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestSafeHookPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hooks")
	cases := []struct {
		name string
		want string
		ok   bool
	}{
		{"survey.sh", filepath.Join(dir, "survey.sh"), true},
		{"sub/vote.sh", filepath.Join(dir, "sub", "vote.sh"), true},
		{"", "", false},
		{"/etc/passwd", "", false},
		{"../escape.sh", "", false},
		{"a/../../escape.sh", "", false},
		{".", "", false},
	}
	for _, c := range cases {
		got, err := safeHookPath(dir, c.name)
		if c.ok {
			if err != nil || got != c.want {
				t.Errorf("safeHookPath(%q) = %q, %v; want %q, nil", c.name, got, err, c.want)
			}
		} else if err == nil {
			t.Errorf("safeHookPath(%q) = %q; want error", c.name, got)
		}
	}
}

func TestRunCardHookJSONCard(t *testing.T) {
	dir := t.TempDir()
	writeHookScript(t, dir, "echo.sh", "#!/bin/sh\ncat >/dev/null\necho '{\"elements\":[{\"tag\":\"div\"}]}'\n")
	p := &Platform{cardHookDir: dir, cardHookTimeout: 3 * time.Second}
	card, err := p.runCardHook("echo.sh", map[string]any{"hook": "echo.sh", "answer": "A"}, cardHookCaller{})
	if err != nil {
		t.Fatalf("runCardHook: %v", err)
	}
	if _, ok := card["elements"].([]any); !ok {
		t.Errorf("card = %#v, want elements array", card)
	}
}

// The hook must receive the full button value as JSON on stdin so it can act on
// the clicked option without an agent round-trip.
func TestRunCardHookReceivesValueOnStdin(t *testing.T) {
	dir := t.TempDir()
	writeHookScript(t, dir, "mirror.sh", "#!/bin/sh\ncat\n")
	p := &Platform{cardHookDir: dir, cardHookTimeout: 3 * time.Second}
	card, err := p.runCardHook("mirror.sh", map[string]any{"hook": "mirror.sh", "answer": "B"}, cardHookCaller{})
	if err != nil {
		t.Fatalf("runCardHook: %v", err)
	}
	if card["answer"] != "B" {
		t.Errorf("stdin value = %#v, want answer=B echoed back", card)
	}
}

func TestRunCardHookNonJSONStdout(t *testing.T) {
	dir := t.TempDir()
	writeHookScript(t, dir, "plain.sh", "#!/bin/sh\necho 'not json'\n")
	p := &Platform{cardHookDir: dir, cardHookTimeout: 3 * time.Second}
	if _, err := p.runCardHook("plain.sh", nil, cardHookCaller{}); err == nil {
		t.Error("non-JSON stdout: want error")
	}
}

func TestRunCardHookMissingScript(t *testing.T) {
	p := &Platform{cardHookDir: t.TempDir(), cardHookTimeout: time.Second}
	if _, err := p.runCardHook("nope.sh", nil, cardHookCaller{}); err == nil {
		t.Error("missing script: want error")
	}
}

func TestRunCardHookTimeout(t *testing.T) {
	dir := t.TempDir()
	writeHookScript(t, dir, "slow.sh", "#!/bin/sh\nsleep 5\n")
	p := &Platform{cardHookDir: dir, cardHookTimeout: 200 * time.Millisecond}
	start := time.Now()
	if _, err := p.runCardHook("slow.sh", nil, cardHookCaller{}); err == nil {
		t.Error("slow hook: want timeout error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("hook took %v; want it cut off near the 200ms timeout", elapsed)
	}
}

// The hook must be able to tell **who clicked**: the button value cannot say,
// and "who approved it" is exactly what a hook acting on an approval needs.
func TestRunCardHookReceivesCallerInEnv(t *testing.T) {
	dir := t.TempDir()
	writeHookScript(t, dir, "who.sh", `#!/bin/sh
cat >/dev/null
printf '{"open_id":"%s","user_id":"%s","chat":"%s","msg":"%s"}' \
  "$CC_CARD_HOOK_OPERATOR_OPEN_ID" "$CC_CARD_HOOK_OPERATOR_USER_ID" \
  "$CC_CARD_HOOK_CHAT_ID" "$CC_CARD_HOOK_MESSAGE_ID"
`)
	p := &Platform{cardHookDir: dir, cardHookTimeout: 3 * time.Second}
	card, err := p.runCardHook("who.sh", map[string]any{"hook": "who.sh"}, cardHookCaller{
		OpenID: "ou_alice", UserID: "u_alice", ChatID: "oc_chat", MessageID: "om_msg",
	})
	if err != nil {
		t.Fatalf("runCardHook: %v", err)
	}
	for key, want := range map[string]any{
		"open_id": "ou_alice", "user_id": "u_alice", "chat": "oc_chat", "msg": "om_msg",
	} {
		if card[key] != want {
			t.Errorf("caller env %s = %#v, want %q", key, card[key], want)
		}
	}
}

// Empty fields are left out (not set to an empty string) so a script can use
// [ -n "$VAR" ] to tell "not provided" from "provided empty".
func TestCardHookEnvOmitsEmpty(t *testing.T) {
	got := cardHookEnv(cardHookCaller{OpenID: "ou_alice"})
	if len(got) != 1 || got[0] != "CC_CARD_HOOK_OPERATOR_OPEN_ID=ou_alice" {
		t.Errorf("cardHookEnv = %v, want only the open_id", got)
	}
	if len(cardHookEnv(cardHookCaller{})) != 0 {
		t.Errorf("cardHookEnv(empty) = %v, want nothing", cardHookEnv(cardHookCaller{}))
	}
}
