//go:build blackbox

package smoke

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/tests/blackbox/helper"
	bbplatform "github.com/chenhg5/cc-connect/tests/blackbox/platform"
)

// Critical user journeys against the fake model. Each one guards a class of
// release-breaking bug: a session that loses or mixes context, a /stop that
// does not stop or loses the conversation, one user's conversation reaching
// another user's model, a provider error that turns into silence, and a
// restart that forgets sessions.

const cmdWait = 30 * time.Second

func sayAs(t *testing.T, env *helper.Env, user, text string) string {
	t.Helper()
	return helper.AnyText(env.SendCompleteAs(user, helper.DefaultChat, text, 3*time.Second, fakeTurnTimeout))
}

func say(t *testing.T, env *helper.Env, text string) string {
	t.Helper()
	return sayAs(t, env, helper.DefaultUser, text)
}

func mustContain(t *testing.T, what, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("%s: want %q in:\n%s", what, w, got)
		}
	}
}

func mustNotContain(t *testing.T, what, got string, bad ...string) {
	t.Helper()
	for _, b := range bad {
		if strings.Contains(got, b) {
			t.Errorf("%s: %q must not appear in:\n%s", what, b, got)
		}
	}
}

// listNumber finds the /list entry number of the session whose title
// contains title.
func listNumber(t *testing.T, list, title string) string {
	t.Helper()
	re := regexp.MustCompile(`(\d+)\.\**\s+` + regexp.QuoteMeta(title))
	m := re.FindStringSubmatch(list)
	if m == nil {
		t.Fatalf("no /list entry titled %q in:\n%s", title, list)
	}
	return m[1]
}

// TestFake_SessionLifecycle: /new starts a separate conversation, /list shows
// both, and /switch brings the old context back without the new one.
func TestFake_SessionLifecycle(t *testing.T) {
	forEachFake(t, []bbplatform.Profile{bbplatform.ProfileText}, func(t *testing.T, env *helper.Env, a fakeAgent) {
		say(t, env, "FAKE:reply apple-one")
		mustContain(t, "/new", say(t, env, "/new"), "New session")
		say(t, env, "FAKE:reply banana-two")

		recall := say(t, env, "FAKE:recall")
		mustContain(t, "context after /new", recall, "banana-two")
		mustNotContain(t, "context after /new", recall, "apple-one")

		list := say(t, env, "/list")
		// Text platforms get "**x Sessions** (2)", card text "x Sessions (2)".
		if !regexp.MustCompile(`Sessions\**\s*\(2\)`).MatchString(list) {
			t.Errorf("/list: want 2 sessions in:\n%s", list)
		}
		n := listNumber(t, list, "FAKE:reply apple-one")

		mustContain(t, "/switch", say(t, env, "/switch "+n), "Switched")
		recall = say(t, env, "FAKE:recall")
		mustContain(t, "context after /switch", recall, "apple-one")
		mustNotContain(t, "context after /switch", recall, "banana-two")
	})
}

// TestFake_StopMidTurnKeepsSession: /stop ends a running turn early, and the
// next message continues the same conversation.
func TestFake_StopMidTurnKeepsSession(t *testing.T) {
	forEachFake(t, []bbplatform.Profile{bbplatform.ProfileText}, func(t *testing.T, env *helper.Env, a fakeAgent) {
		say(t, env, "FAKE:reply before-stop")

		start := env.Platform.MessageCount()
		env.SendNoWait("FAKE:slow 40 tick") // 12s of streaming
		waitForTurnRequest(t, env, "FAKE:slow 40 tick")
		time.Sleep(time.Second)

		stopped := time.Now()
		env.SendNoWait("/stop")
		env.WaitForMessageContaining(start, "stopped", cmdWait)
		if d := time.Since(stopped); d > 8*time.Second {
			t.Errorf("/stop took %v to take effect", d)
		}
		time.Sleep(3 * time.Second)
		for _, m := range env.Platform.GetSentMessages()[start:] {
			if strings.Count(m.Text(), "tick") >= 40 {
				t.Errorf("the stopped turn still delivered its full answer: %.200q", m.Text())
			}
		}

		recall := say(t, env, "FAKE:recall")
		mustContain(t, "context after /stop", recall, "before-stop")
	})
}

func waitForTurnRequest(t *testing.T, env *helper.Env, directive string) {
	t.Helper()
	deadline := time.Now().Add(fakeTurnTimeout)
	for time.Now().Before(deadline) {
		for _, r := range env.FakeModel.TurnRequests() {
			if r.Directive == directive {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the model never received %q", directive)
}

// TestFake_UserContextIsolation: two users of the same project have
// separate conversations; neither one's text reaches the other's model.
func TestFake_UserContextIsolation(t *testing.T) {
	forEachFake(t, []bbplatform.Profile{bbplatform.ProfileText}, func(t *testing.T, env *helper.Env, a fakeAgent) {
		sayAs(t, env, "user1", "FAKE:reply apple-u1")
		mustNotContain(t, "user2 context", sayAs(t, env, "user2", "FAKE:recall"), "apple-u1")
		sayAs(t, env, "user2", "FAKE:reply pear-u2")
		recall := sayAs(t, env, "user1", "FAKE:recall")
		mustContain(t, "user1 context", recall, "apple-u1")
		mustNotContain(t, "user1 context", recall, "pear-u2")
	})
}

// TestFake_ModelErrorSurfacesAndRecovers: when the model API fails, the
// user is told (not left in silence), and the next message works again.
func TestFake_ModelErrorSurfacesAndRecovers(t *testing.T) {
	forEachFake(t, []bbplatform.Profile{bbplatform.ProfileText}, func(t *testing.T, env *helper.Env, a fakeAgent) {
		failed := say(t, env, "FAKE:fail provider-down-7731")
		t.Logf("the user saw: %.300q", failed)
		if strings.TrimSpace(failed) == "" {
			t.Fatalf("a failed model call produced no message at all")
		}
		if !strings.Contains(failed, "provider-down-7731") && !strings.Contains(strings.ToLower(failed), "error") {
			t.Errorf("the user was not told the model call failed:\n%s", failed)
		}
		mustContain(t, "turn after the failure", say(t, env, "FAKE:reply recovered-ok"), "recovered-ok")
	})
}

// TestFake_RestartKeepsSession: after cc-connect restarts, the session is
// still listed and the next message continues it.
func TestFake_RestartKeepsSession(t *testing.T) {
	for _, a := range fakeAgents {
		a := a
		t.Run(a.name, func(t *testing.T) {
			t.Parallel()
			env := helper.NewEnvWithOptions(t, a.name, helper.Options{
				Backend: helper.BackendBinary, Profile: bbplatform.ProfileText, FakeModel: true, AgentOptions: a.opts,
			})
			say(t, env, "FAKE:reply apple-restart")
			env.Restart()
			mustContain(t, "/list after restart", say(t, env, "/list"), "apple-restart")
			mustContain(t, "context after restart", say(t, env, "FAKE:recall"), "apple-restart")
		})
	}
}
