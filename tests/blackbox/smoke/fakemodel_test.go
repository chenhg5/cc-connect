//go:build blackbox

package smoke

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/chenhg5/cc-connect/tests/blackbox/helper"
	bbplatform "github.com/chenhg5/cc-connect/tests/blackbox/platform"
)

// Agent-turn smoke against the scripted fake model (tests/blackbox/fakemodel):
// the real agent CLI and the real cc-connect, no model key, deterministic
// answers. Each test runs per agent, on both backends and on the profiles
// where the behaviour differs.

const fakeTurnTimeout = 2 * time.Minute

// permCommand creates a file and prints 42, so allow/deny can be checked by
// side effect. The command text itself does not contain "42".
const permCommand = "touch perm-proof.txt && echo $((6*7))"

// fakeAgent describes how one agent is run against the fake model.
type fakeAgent struct {
	name string
	// opts are extra agent options for ordinary turns.
	opts map[string]any
	// permOpts and permDirective drive a tool call that must ask the user.
	permOpts      map[string]any
	permDirective string
}

var fakeAgents = []fakeAgent{
	{name: "claudecode", permDirective: "FAKE:tool Bash "},
	{
		// Codex's default exec backend never asks for approval
		// (approval_policy=never). Approval prompts exist on the app_server
		// backend in suggest mode, and only when the model asks to run
		// outside the sandbox, which FAKE:escalate does.
		name:          "codex",
		permOpts:      map[string]any{"backend": "app_server", "mode": "suggest"},
		permDirective: "FAKE:escalate ",
	},
}

func forEachFake(t *testing.T, profiles []bbplatform.Profile, fn func(t *testing.T, env *helper.Env, a fakeAgent)) {
	forEachFakeWith(t, profiles, false, fn)
}

func forEachFakeWith(t *testing.T, profiles []bbplatform.Profile, perm bool, fn func(t *testing.T, env *helper.Env, a fakeAgent)) {
	for _, a := range fakeAgents {
		for _, b := range backends {
			for _, pr := range profiles {
				a, b, pr := a, b, pr
				t.Run(a.name+"/"+string(b)+"/"+string(pr), func(t *testing.T) {
					t.Parallel()
					opts := a.opts
					if perm {
						opts = a.permOpts
					}
					env := helper.NewEnvWithOptions(t, a.name, helper.Options{Backend: b, Profile: pr, FakeModel: true, AgentOptions: opts})
					fn(t, env, a)
				})
			}
		}
	}
}

func fakeTurn(t *testing.T, env *helper.Env, text string) []*bbplatform.SentMessage {
	t.Helper()
	return env.SendCompleteAs(helper.DefaultUser, helper.DefaultChat, text, 3*time.Second, fakeTurnTimeout)
}

// TestFake_Reply: a message reaches the model and its answer reaches the user.
func TestFake_Reply(t *testing.T) {
	forEachFake(t, []bbplatform.Profile{bbplatform.ProfileText, bbplatform.ProfileStreaming, bbplatform.ProfileFull}, func(t *testing.T, env *helper.Env, a fakeAgent) {
		msgs := fakeTurn(t, env, "FAKE:reply PONG-42")
		if !strings.Contains(helper.AnyText(msgs), "PONG-42") {
			t.Fatalf("answer did not reach the user:\n%s", env.Platform.AllText())
		}
		turns := env.FakeModel.TurnRequests()
		if len(turns) != 1 || turns[0].Directive != "FAKE:reply PONG-42" {
			t.Errorf("want exactly one model turn carrying the user text, got %+v", turns)
		}
	})
}

// TestFake_ContextCarriesOver: the second turn sends the first turn to the
// model (the session is resumed, not restarted).
func TestFake_ContextCarriesOver(t *testing.T) {
	forEachFake(t, []bbplatform.Profile{bbplatform.ProfileText}, func(t *testing.T, env *helper.Env, a fakeAgent) {
		fakeTurn(t, env, "FAKE:reply apple-7731")
		msgs := fakeTurn(t, env, "FAKE:recall")
		got := helper.AnyText(msgs)
		if !strings.Contains(got, "HISTORY:") || !strings.Contains(got, "apple-7731") {
			t.Fatalf("second turn did not carry the first one:\n%s", got)
		}
	})
}

// TestFake_StreamingPreview: on streaming profiles a long answer is previewed
// while it streams, and the user ends with the complete answer.
func TestFake_StreamingPreview(t *testing.T) {
	forEachFake(t, []bbplatform.Profile{bbplatform.ProfileStreaming, bbplatform.ProfileFull}, func(t *testing.T, env *helper.Env, a fakeAgent) {
		msgs := fakeTurn(t, env, "FAKE:slow 20 tick")
		var previews int
		for _, m := range env.Platform.GetSentMessages() {
			if m.Kind == bbplatform.KindPreview {
				previews++
			}
		}
		if previews == 0 {
			t.Errorf("no streaming preview on profile %s", env.Profile)
		}
		if n := strings.Count(helper.AnyText(msgs), "tick"); n < 20 {
			t.Errorf("final answer incomplete: %d of 20 chunks\n%s", n, env.Platform.AllText())
		}
		for _, m := range env.Platform.GetSentMessages() {
			if m.Kind == bbplatform.KindPreview && !m.Deleted && strings.Count(m.Content, "tick") < 20 && m.Content != "" && strings.Contains(m.Content, "tick") {
				t.Errorf("preview %s left with a partial answer (%d chunks)", m.Handle, strings.Count(m.Content, "tick"))
			}
		}
	})
}

func waitForPermissionPrompt(t *testing.T, env *helper.Env, start int) *bbplatform.SentMessage {
	t.Helper()
	return env.WaitForMessageContaining(start, "Permission Request", fakeTurnTimeout)
}

// answerPermission answers the prompt the way the profile allows: a button
// click when the prompt carries buttons, a text reply otherwise.
func answerPermission(t *testing.T, env *helper.Env, prompt *bbplatform.SentMessage, answer string) {
	t.Helper()
	action := "perm:" + strings.ReplaceAll(answer, " ", "_")
	hasButtons := false
	for _, v := range prompt.ButtonValues() {
		if v == action {
			hasButtons = true
		}
	}
	wantButtons := env.Profile != bbplatform.ProfileText && env.Profile != bbplatform.ProfileStreaming
	if wantButtons != hasButtons {
		t.Errorf("profile %s: permission prompt buttons=%v, want buttons=%v", env.Profile, prompt.ButtonValues(), wantButtons)
	}
	if hasButtons {
		if err := env.Platform.InjectCardAction(helper.DefaultUser, helper.DefaultChat, action); err != nil {
			t.Fatalf("click %s: %v", action, err)
		}
		return
	}
	env.SendNoWait(answer)
}

// TestFake_PermissionAllow: a tool call asks for permission; allowing it runs
// the tool and the model receives its output.
func TestFake_PermissionAllow(t *testing.T) {
	forEachFakeWith(t, []bbplatform.Profile{bbplatform.ProfileText, bbplatform.ProfileButtons, bbplatform.ProfileFull}, true, func(t *testing.T, env *helper.Env, a fakeAgent) {
		start := env.Platform.MessageCount()
		env.SendNoWait(a.permDirective + permCommand)
		prompt := waitForPermissionPrompt(t, env, start)
		answerPermission(t, env, prompt, "allow")

		done := env.WaitForMessageContaining(start, "TOOL_DONE", fakeTurnTimeout)
		if !strings.Contains(done.Text(), "42") {
			t.Errorf("tool output did not reach the model: %q", done.Text())
		}
		if _, err := os.Stat(filepath.Join(env.WorkDir, "perm-proof.txt")); err != nil {
			t.Errorf("allowed tool did not run: %v", err)
		}
	})
}

// TestFake_PermissionDeny: denying the prompt keeps the tool from running.
func TestFake_PermissionDeny(t *testing.T) {
	forEachFakeWith(t, []bbplatform.Profile{bbplatform.ProfileText, bbplatform.ProfileFull}, true, func(t *testing.T, env *helper.Env, a fakeAgent) {
		start := env.Platform.MessageCount()
		env.SendNoWait(a.permDirective + permCommand)
		prompt := waitForPermissionPrompt(t, env, start)
		answerPermission(t, env, prompt, "deny")

		env.Platform.WaitForTurnComplete(start, 5*time.Second, fakeTurnTimeout)
		if _, err := os.Stat(filepath.Join(env.WorkDir, "perm-proof.txt")); err == nil {
			t.Errorf("denied tool ran anyway")
		}
		if strings.Contains(env.Platform.AllText(), "TOOL_DONE: 42") {
			t.Errorf("denied tool output reached the model:\n%s", env.Platform.AllText())
		}
	})
}

// TestFake_ToolOutputReachesModel: on each agent's default setup a shell
// tool call runs and its output goes back to the model. Whether the agent
// asks first depends on the agent (Claude Code auto-approves read-only
// commands such as echo; Codex's exec backend never asks), so a prompt is
// allowed if one appears but not required.
func TestFake_ToolOutputReachesModel(t *testing.T) {
	forEachFake(t, []bbplatform.Profile{bbplatform.ProfileFull}, func(t *testing.T, env *helper.Env, a fakeAgent) {
		start := env.Platform.MessageCount()
		env.SendNoWait("FAKE:tool Bash echo $((6*7))")
		done := waitAllowingPrompts(t, env, start, "TOOL_DONE")
		if !strings.Contains(done.Text(), "42") {
			t.Errorf("tool output did not reach the model: %q", done.Text())
		}
	})
}

// waitAllowingPrompts waits for a message containing want, allowing any
// permission prompt that shows up on the way.
func waitAllowingPrompts(t *testing.T, env *helper.Env, start int, want string) *bbplatform.SentMessage {
	t.Helper()
	answered := map[*bbplatform.SentMessage]bool{}
	deadline := time.Now().Add(fakeTurnTimeout)
	for time.Now().Before(deadline) {
		msgs := env.Platform.GetSentMessages()
		for i := start; i < len(msgs); i++ {
			m := msgs[i]
			if strings.Contains(m.Text(), want) {
				return m
			}
			if strings.Contains(m.Text(), "Permission Request") && !answered[m] {
				answered[m] = true
				answerPermission(t, env, m, "allow")
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("blackbox timeout (%v): no message containing %q\n%s", fakeTurnTimeout, want, env.Platform.AllText())
	return nil
}

// onePixelPNG is a valid 1x1 PNG.
var onePixelPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0xf8, 0xcf, 0xc0, 0xf0,
	0x1f, 0x00, 0x05, 0x00, 0x01, 0xff, 0x89, 0x99, 0x3d, 0x1d, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45,
	0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
}

// TestFake_ImageReachesModel: an image sent by the user arrives at the model.
func TestFake_ImageReachesModel(t *testing.T) {
	forEachFake(t, []bbplatform.Profile{bbplatform.ProfileFull}, func(t *testing.T, env *helper.Env, a fakeAgent) {
		start := env.Platform.MessageCount()
		env.Platform.InjectMessageWithAttachments(helper.DefaultUser, helper.DefaultChat, "FAKE:reply got-image",
			[]core.ImageAttachment{{MimeType: "image/png", Data: onePixelPNG, FileName: "dot.png"}}, nil)
		env.Platform.WaitForTurnComplete(start, 3*time.Second, fakeTurnTimeout)
		if !strings.Contains(env.Platform.AllText(), "got-image") {
			t.Fatalf("no answer to the image message:\n%s", env.Platform.AllText())
		}
		var images int
		for _, r := range env.FakeModel.TurnRequests() {
			images += r.Images
		}
		if images == 0 {
			t.Errorf("model never received an image block; turn requests: %+v", env.FakeModel.TurnRequests())
		}
	})
}
