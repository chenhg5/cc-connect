package codex

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDesktopHelperKeepsArgumentsAndFailsClosed(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, "helper.sh")
	script := `[ "$1" = reply ] && [ "$2" = thread ] && [ "$3" = queue ] && [ -n "$4" ] && [ "$5" = 'spaces $(echo unsafe)' ] || exit 7
printf '{"status":"queued","message_id":"test-id"}'
`
	if err := os.WriteFile(helper, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	a := &Agent{desktopHelper: []string{"/bin/sh", helper}, desktopStateDir: dir, codexHome: dir}
	if err := a.ReplyToThread(context.Background(), "thread", "spaces $(echo unsafe)"); err != nil {
		t.Fatal(err)
	}
	if a.AnswerThreadRequest(context.Background(), "thread", "key", "yes") == nil {
		t.Fatal("helper failure hidden")
	}
	opts := a.WorkspaceAgentOptions()
	if len(opts["desktop_helper"].([]string)) != 2 || opts["desktop_state_dir"] != dir {
		t.Fatal("workspace lost helper")
	}
	a.desktopHelper = nil
	if a.ReplyToThread(context.Background(), "thread", "yes") == nil {
		t.Fatal("disabled helper accepted")
	}
}

func TestDesktopHelperRequiresExplicitAcceptance(t *testing.T) {
	for _, ack := range []string{`{}`, `null`, `{"status":"rejected"}`} {
		a := &Agent{desktopHelper: []string{"/bin/sh", "-c", "printf '%s' '" + ack + "'"}}
		if a.ReplyToThread(context.Background(), "thread", "yes") == nil {
			t.Fatal("unknown acknowledgement accepted", ack)
		}
	}
}

func TestDesktopHelperImmediateReplyPreservesModeAndMessageID(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, "helper.sh")
	script := `[ "$1" = reply ] && [ "$2" = thread ] && [ "$3" = now ] && [ "$4" = platform-message ] && [ "$5" = 'follow-up' ] || exit 7
printf '{"status":"accepted","message_id":"stable-id"}'
`
	if err := os.WriteFile(helper, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	a := &Agent{desktopHelper: []string{"/bin/sh", helper}}
	if err := a.ReplyToThreadWithMode(context.Background(), "thread", "follow-up", "now", "platform-message"); err != nil {
		t.Fatal(err)
	}
	a.desktopHelper = []string{"/bin/sh", "-c", `printf '{"status":"queued","message_id":"stable-id"}'`}
	if a.ReplyToThreadWithMode(context.Background(), "thread", "follow-up", "now", "platform-message") == nil {
		t.Fatal("immediate reply accepted queued acknowledgement")
	}
	if a.ReplyToThreadWithMode(context.Background(), "thread", "follow-up", "invalid", "platform-message") == nil {
		t.Fatal("invalid reply mode accepted")
	}
}
