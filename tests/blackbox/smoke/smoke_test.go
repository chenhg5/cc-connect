//go:build blackbox

// Package smoke runs a small set of end-to-end checks on every backend and
// capability profile:
//
//	backend: inprocess (core.Engine in the test) | binary (real cc-connect
//	         process driven through its bridge WebSocket)
//	profile: text | buttons | streaming | card | full | legacy
//
// The command checks never start an agent turn, so they only need one agent
// CLI on PATH and no model credentials. TestSmoke_AgentTurn needs credentials
// and skips without them.
//
// Run:
//
//	go test -tags blackbox -count=1 ./tests/blackbox/smoke/
//	CC_BLACKBOX_BINARY=dist/cc-connect-<ver>-linux-amd64 go test -tags blackbox ./tests/blackbox/smoke/
package smoke

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/tests/blackbox/helper"
	bbplatform "github.com/chenhg5/cc-connect/tests/blackbox/platform"
)

const (
	cmdTimeout  = 30 * time.Second
	turnTimeout = 3 * time.Minute
	idle        = 2 * time.Second
)

var backends = []helper.Backend{helper.BackendInProcess, helper.BackendBinary}

// smokeAgent picks the agent for command smoke: CC_BLACKBOX_SMOKE_AGENT, the
// first agent in CC_BLACKBOX_REQUIRE_AGENTS, or the first tier-one agent
// whose CLI is installed.
func smokeAgent(t *testing.T) string {
	t.Helper()
	if a := os.Getenv("CC_BLACKBOX_SMOKE_AGENT"); a != "" {
		return a
	}
	// In a strict (release) run the agent is never guessed: use the first
	// required one, whose absence then fails the run.
	if req := helper.RequiredAgents(); len(req) > 0 {
		return req[0]
	}
	for _, c := range []struct{ agent, bin string }{
		{"claudecode", "claude"}, {"codex", "codex"}, {"cursor", "agent"}, {"gemini", "gemini"},
	} {
		if _, err := exec.LookPath(c.bin); err == nil {
			return c.agent
		}
	}
	t.Skip("blackbox skip: no tier-one agent CLI (claude, codex, agent, gemini) on PATH")
	return ""
}

func forEachBackendAndProfile(t *testing.T, profiles []bbplatform.Profile, fn func(t *testing.T, env *helper.Env)) {
	agent := smokeAgent(t)
	for _, b := range backends {
		for _, pr := range profiles {
			b, pr := b, pr
			t.Run(string(b)+"/"+string(pr), func(t *testing.T) {
				t.Parallel()
				env := helper.NewEnvWithOptions(t, agent, helper.Options{Backend: b, Profile: pr, NoCredentials: true})
				fn(t, env)
			})
		}
	}
}

func command(t *testing.T, env *helper.Env, text string) []*bbplatform.SentMessage {
	t.Helper()
	return env.SendCompleteAs(helper.DefaultUser, helper.DefaultChat, text, idle, cmdTimeout)
}

func hasCapability(pr bbplatform.Profile, cap string) bool {
	for _, c := range pr.BridgeCapabilities() {
		if c == cap {
			return true
		}
	}
	return false
}

// TestSmoke_Commands: the process starts, accepts a message and answers the
// core slash commands on every backend and profile.
func TestSmoke_Commands(t *testing.T) {
	forEachBackendAndProfile(t, bbplatform.Profiles, func(t *testing.T, env *helper.Env) {
		help := helper.AnyText(command(t, env, "/help"))
		if !strings.Contains(help, "/new") || !strings.Contains(help, "/list") {
			t.Errorf("/help does not list /new and /list:\n%s", help)
		}

		status := helper.AnyText(command(t, env, "/status"))
		if !strings.Contains(status, "Agent:") {
			t.Errorf("/status has no Agent line:\n%s", status)
		}

		list := helper.AnyText(command(t, env, "/list"))
		if strings.TrimSpace(list) == "" {
			t.Errorf("/list returned an empty reply")
		}

		if env.Backend == helper.BackendBinary {
			// The in-process engine has no build metadata; the binary must
			// report its version.
			v := helper.AnyText(command(t, env, "/version"))
			if !strings.Contains(v, "cc-connect") {
				t.Errorf("/version from the binary does not name cc-connect: %q", v)
			}
			// A release gate passes the version it is about to publish, which
			// proves the smoke ran that artifact and not some other build.
			if want := os.Getenv("CC_BLACKBOX_EXPECT_VERSION"); want != "" && !strings.Contains(v, want) {
				t.Errorf("/version %q does not report the release version %q (wrong binary under test?)", v, want)
			}
		}
	})
}

// TestSmoke_CardButtonsClickThrough: on profiles with cards, the help card
// carries navigation buttons, and clicking them reaches the engine:
// nav:/act: through the card navigation handler, cmd: as a command.
func TestSmoke_CardButtonsClickThrough(t *testing.T) {
	forEachBackendAndProfile(t, []bbplatform.Profile{bbplatform.ProfileCard, bbplatform.ProfileFull}, func(t *testing.T, env *helper.Env) {
		msgs := command(t, env, "/help")
		var card *bbplatform.SentMessage
		for _, m := range msgs {
			if m.Kind == bbplatform.KindCard {
				card = m
			}
		}
		if card == nil {
			t.Fatalf("/help did not produce a card on profile %s:\n%s", env.Profile, helper.AnyText(msgs))
		}
		values := card.ButtonValues()
		if !containsValue(values, "nav:/help session") || !containsValue(values, "nav:/list") {
			t.Fatalf("help card is missing navigation buttons: %v", values)
		}

		nav := env.Click("nav:/help session", cmdTimeout)
		if nav.Kind != bbplatform.KindCard || !strings.Contains(nav.Text(), "/new") {
			t.Errorf("nav:/help session: want a card listing /new, got kind=%s:\n%s", nav.Kind, nav.Text())
		}

		list := env.Click("cmd:/list", cmdTimeout)
		if !strings.Contains(strings.ToLower(list.Text()), "session") {
			t.Errorf("cmd:/list: want the session list, got kind=%s:\n%s", list.Kind, list.Text())
		}
	})
}

// TestSmoke_TextProfileHasNoButtons: platforms without buttons or cards must
// receive plain messages with no callback buttons attached.
func TestSmoke_TextProfileHasNoButtons(t *testing.T) {
	forEachBackendAndProfile(t, []bbplatform.Profile{bbplatform.ProfileText, bbplatform.ProfileStreaming}, func(t *testing.T, env *helper.Env) {
		for _, c := range []string{"/help", "/list", "/status"} {
			for _, m := range command(t, env, c) {
				if m.Kind == bbplatform.KindCard || m.Kind == bbplatform.KindButtons || len(m.ButtonValues()) > 0 {
					t.Errorf("%s on profile %s produced kind=%s buttons=%v", c, env.Profile, m.Kind, m.ButtonValues())
				}
			}
		}
	})
}

// TestSmoke_BridgeRejectsBadToken: the bridge refuses an adapter with the
// wrong token (binary backend only).
func TestSmoke_BridgeRejectsBadToken(t *testing.T) {
	agent := smokeAgent(t)
	env := helper.NewEnvWithOptions(t, agent, helper.Options{Backend: helper.BackendBinary, Profile: bbplatform.ProfileText, NoCredentials: true})
	intruder := bbplatform.New("intruder")
	c, err := bbplatform.ConnectBridge(intruder, env.BridgeURL(), "wrong-token", "", bbplatform.ProfileText)
	if err == nil {
		c.Close()
		t.Fatal("bridge accepted an adapter with the wrong token")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("want HTTP 401 for a wrong token, got: %v", err)
	}
}

// TestSmoke_AgentTurn: one real agent turn per profile. Needs model
// credentials (see helper.requireCredentials) and skips without them.
// Streaming profiles must leave a finished preview behind, not a stale one.
func TestSmoke_AgentTurn(t *testing.T) {
	agent := os.Getenv("CC_BLACKBOX_SMOKE_AGENT")
	if agent == "" {
		agent = "claudecode"
	}
	for _, b := range backends {
		for _, pr := range []bbplatform.Profile{bbplatform.ProfileText, bbplatform.ProfileStreaming, bbplatform.ProfileFull} {
			b, pr := b, pr
			t.Run(string(b)+"/"+string(pr), func(t *testing.T) {
				t.Parallel()
				env := helper.NewEnvWithOptions(t, agent, helper.Options{Backend: b, Profile: pr})
				msgs := env.SendCompleteAs(helper.DefaultUser, helper.DefaultChat,
					"Reply with exactly the word PONG and nothing else. Do not use any tools.", 5*time.Second, turnTimeout)
				if !strings.Contains(strings.ToUpper(helper.AnyText(msgs)), "PONG") {
					t.Fatalf("no PONG in the turn:\n%s", env.Platform.AllText())
				}
				if hasCapability(pr, "preview") {
					for _, m := range env.Platform.GetSentMessages() {
						if m.Kind == bbplatform.KindPreview && !m.Deleted && strings.TrimSpace(m.Content) == "" {
							t.Errorf("preview %s left empty after the turn", m.Handle)
						}
					}
				}
			})
		}
	}
}

func containsValue(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
