package core

import (
	"strings"
	"testing"
	"time"
)

// agentSessionWithAgent is a minimal AgentSession stub exposing GetAgent for
// the session-precedence path of replyFooterAgent.
type agentSessionWithAgent struct {
	controllableAgentSession
	agent string
}

func (s *agentSessionWithAgent) GetAgent() string { return s.agent }

func TestReplyFooterAgent(t *testing.T) {
	cases := []struct {
		name    string
		session AgentSession
		agent   Agent
		want    string
	}{
		{
			name:  "agent switcher returns name",
			agent: &stubAgentSwitchAgent{agent: "build"},
			want:  "build",
		},
		{
			name:  "agent switcher empty value",
			agent: &stubAgentSwitchAgent{agent: ""},
			want:  "",
		},
		{
			name:  "non agent switcher",
			agent: &stubAgent{},
			want:  "",
		},
		{
			name:    "session layer takes precedence",
			session: &agentSessionWithAgent{agent: "plan"},
			agent:   &stubAgentSwitchAgent{agent: "build"},
			want:    "plan",
		},
		{
			name:    "session layer empty falls back to agent",
			session: &agentSessionWithAgent{agent: ""},
			agent:   &stubAgentSwitchAgent{agent: "build"},
			want:    "build",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := replyFooterAgent(tc.session, tc.agent); got != tc.want {
				t.Errorf("replyFooterAgent() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestComposeRichStatusFooter_ShowsAgentPrefix(t *testing.T) {
	e := newClaudeFooterEngine()
	e.i18n = NewI18n(LangEnglish)
	session := newControllableSession("s1")
	session.model = "deepseek/deepseek-v4-pro"
	agent := &stubAgentSwitchAgent{agent: "build"}

	got := e.composeRichStatusFooter(false, time.Now(), agent, session, "/tmp/ws")
	if !strings.Contains(got, "build · deepseek/deepseek-v4-pro") {
		t.Errorf("footer = %q, want it to contain %q", got, "build · deepseek/deepseek-v4-pro")
	}
}

func TestComposeRichStatusFooter_NoAgentSwitcher(t *testing.T) {
	e := newClaudeFooterEngine()
	e.i18n = NewI18n(LangEnglish)
	session := newControllableSession("s1")
	session.model = "deepseek/deepseek-v4-pro"
	agent := &stubAgent{}

	got := e.composeRichStatusFooter(false, time.Now(), agent, session, "/tmp/ws")
	if strings.Contains(got, "build") {
		t.Errorf("footer = %q, must not contain agent name for non-AgentSwitcher agents", got)
	}
	if !strings.Contains(got, "deepseek/deepseek-v4-pro") {
		t.Errorf("footer = %q, want model name present", got)
	}
}

func TestComposeRichStatusFooter_EmptyAgentValue(t *testing.T) {
	e := newClaudeFooterEngine()
	e.i18n = NewI18n(LangEnglish)
	session := newControllableSession("s1")
	session.model = "deepseek/deepseek-v4-pro"
	agent := &stubAgentSwitchAgent{agent: ""}

	got := e.composeRichStatusFooter(false, time.Now(), agent, session, "/tmp/ws")
	if strings.HasPrefix(got, " · ") || strings.Contains(got, " · · ") {
		t.Errorf("footer = %q, want no dangling separators for empty agent", got)
	}
	if !strings.Contains(got, "deepseek/deepseek-v4-pro") {
		t.Errorf("footer = %q, want model name present", got)
	}
}

func TestReplyFooterDisplayModel(t *testing.T) {
	cases := []struct {
		name    string
		session AgentSession
		agent   Agent
		want    string
	}{
		{
			name:    "agent and model both present",
			session: &controllableAgentSession{model: "deepseek/deepseek-v4-pro"},
			agent:   &stubAgentSwitchAgent{agent: "brainstorm"},
			want:    "brainstorm · deepseek/deepseek-v4-pro",
		},
		{
			name:    "agent present model empty",
			session: &controllableAgentSession{},
			agent:   &stubAgentSwitchAgent{agent: "plan"},
			want:    "plan",
		},
		{
			name:    "model present agent empty",
			session: &controllableAgentSession{model: "gpt-5"},
			agent:   &stubAgentSwitchAgent{agent: ""},
			want:    "gpt-5",
		},
		{
			name:    "both empty",
			session: &controllableAgentSession{},
			agent:   &stubAgentSwitchAgent{agent: ""},
			want:    "",
		},
		{
			name:    "non agent switcher falls back to model",
			session: &controllableAgentSession{model: "claude-sonnet-4-5"},
			agent:   &stubAgent{},
			want:    "claude-sonnet-4-5",
		},
		{
			name:    "session layer agent takes precedence",
			session: &agentSessionWithAgent{agent: "session-agent", controllableAgentSession: controllableAgentSession{model: "m1"}},
			agent:   &stubAgentSwitchAgent{agent: "global-agent"},
			want:    "session-agent · m1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := replyFooterDisplayModel(tc.session, tc.agent); got != tc.want {
				t.Errorf("replyFooterDisplayModel() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestBuildReplyFooter_ShowsAgentPrefix is the regression test for the legacy
// footer path used by non-Claude agents (opencode + deepseek report no cache
// token signals, so buildClaudeStatusLineFooter returns "" and the engine
// falls through to buildReplyFooter). Before the fix, buildReplyFooter only
// rendered replyFooterModel() and the agent name was lost.
func TestBuildReplyFooter_ShowsAgentPrefix(t *testing.T) {
	e := newClaudeFooterEngine()
	e.i18n = NewI18n(LangEnglish)
	session := &controllableAgentSession{model: "deepseek/deepseek-v4-pro", workDir: "/tmp/ws"}
	agent := &stubAgentSwitchAgent{agent: "brainstorm"}

	got := e.buildReplyFooter(agent, session, "/tmp/ws", "[ctx: 12%]")
	if !strings.Contains(got, "brainstorm · deepseek/deepseek-v4-pro") {
		t.Errorf("buildReplyFooter = %q, want it to contain %q", got, "brainstorm · deepseek/deepseek-v4-pro")
	}
	if !strings.Contains(got, "/tmp/ws") {
		t.Errorf("buildReplyFooter = %q, want workdir present", got)
	}
}

// TestBuildReplyFooter_NoAgentSwitcher ensures agents without AgentSwitcher
// keep the plain model-only footer (no dangling separators).
func TestBuildReplyFooter_NoAgentSwitcher(t *testing.T) {
	e := newClaudeFooterEngine()
	e.i18n = NewI18n(LangEnglish)
	session := &controllableAgentSession{model: "deepseek/deepseek-v4-pro", workDir: "/tmp/ws"}
	agent := &stubAgent{}

	got := e.buildReplyFooter(agent, session, "/tmp/ws", "[ctx: 12%]")
	if strings.Contains(got, "· ·") || strings.HasPrefix(got, " ·") {
		t.Errorf("buildReplyFooter = %q, want no dangling separators", got)
	}
	if !strings.Contains(got, "deepseek/deepseek-v4-pro") {
		t.Errorf("buildReplyFooter = %q, want model name present", got)
	}
}

// TestBuildClaudeStatusLineFooter_ShowsAgentPrefix covers the cache-token
// statusline path (Claude agents): the agent name must prefix the model.
func TestBuildClaudeStatusLineFooter_ShowsAgentPrefix(t *testing.T) {
	e := newClaudeFooterEngine()
	session := &controllableAgentSession{
		model:   "claude-opus-4-7",
		workDir: "/tmp/ws",
		contextUsage: &ContextUsage{
			InputTokens:              1000,
			OutputTokens:             200,
			CachedInputTokens:        500,
			CacheCreationInputTokens: 300,
			ContextWindow:            1_000_000,
			UsedTokens:               800,
		},
	}
	agent := &stubAgentSwitchAgent{agent: "build"}

	got := e.buildClaudeStatusLineFooter(agent, session, "/tmp/ws")
	if !strings.Contains(got, "build · claude-opus-4-7") {
		t.Errorf("statusline = %q, want it to contain %q", got, "build · claude-opus-4-7")
	}
}
