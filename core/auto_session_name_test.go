package core

import (
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// ----------------------------------------------------------------------------
// deriveAutoTitle
// ----------------------------------------------------------------------------

func TestDeriveAutoTitle_Basic(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxRunes int
		want     string
	}{
		{
			name:     "empty input returns empty",
			input:    "",
			maxRunes: 28,
			want:     "",
		},
		{
			name:     "whitespace only returns empty",
			input:    "   \n\t  ",
			maxRunes: 28,
			want:     "",
		},
		{
			name:     "short english passes through",
			input:    "Add login button",
			maxRunes: 28,
			want:     "Add login button",
		},
		{
			name:     "short chinese passes through",
			input:    "新增登录按钮",
			maxRunes: 28,
			want:     "新增登录按钮",
		},
		{
			name:     "first sentence by chinese period",
			input:    "新增登录按钮。要支持多种主题色。",
			maxRunes: 28,
			want:     "新增登录按钮",
		},
		{
			name:     "first sentence by western period",
			input:    "Add login button. Should support themes.",
			maxRunes: 28,
			want:     "Add login button",
		},
		{
			name:     "first sentence by question mark",
			input:    "Why is the test failing? Help debug it.",
			maxRunes: 28,
			want:     "Why is the test failing",
		},
		{
			name:     "newline terminates first line",
			input:    "Title line\nThis is the body of the prompt with details",
			maxRunes: 28,
			want:     "Title line",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deriveAutoTitle(tt.input, tt.maxRunes)
			if got != tt.want {
				t.Errorf("deriveAutoTitle(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestDeriveAutoTitle_StripsNoise(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "fenced code block stripped",
			input: "看一下 ```go\nfunc main(){}\n``` 这段代码",
			want:  "看一下 这段代码",
		},
		{
			name:  "inline code stripped",
			input: "看 `foo()` 文件",
			want:  "看 文件",
		},
		{
			name:  "url stripped",
			input: "参考 https://example.com/docs?a=1 这个文档",
			want:  "参考 这个文档",
		},
		{
			name:  "at-reference stripped",
			input: "@scripts/foo.sh 改一下入参",
			want:  "改一下入参",
		},
		{
			name:  "bold markers stripped",
			input: "**重要** 要改这里",
			want:  "重要 要改这里",
		},
		{
			name:  "italic single underscore stripped",
			input: "_emphasis_ test",
			want:  "emphasis test",
		},
		{
			name:  "fenced block leaks no backticks",
			input: "前 ```\n` lone backtick inside\n``` 后",
			want:  "前 后",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deriveAutoTitle(tt.input, 80)
			if got != tt.want {
				t.Errorf("deriveAutoTitle(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestDeriveAutoTitle_TruncationRuneSafe(t *testing.T) {
	// 30 Chinese characters (90 bytes in UTF-8) — must be cut at rune boundary.
	long := "设计一个支持多语言的全文检索系统并写出详细的实现方案以及部署步骤说明文档"
	got := deriveAutoTitle(long, 10)

	// Strip the trailing ellipsis for length verification.
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected trailing ellipsis, got %q", got)
	}
	body := strings.TrimSuffix(got, "…")
	if utf8.RuneCountInString(body) != 10 {
		t.Errorf("body rune count = %d, want 10 (got %q)", utf8.RuneCountInString(body), body)
	}
	if !utf8.ValidString(got) {
		t.Errorf("output is not valid UTF-8: %q", got)
	}
}

func TestDeriveAutoTitle_NoTruncationWhenWithinLimit(t *testing.T) {
	in := "九字以内"
	got := deriveAutoTitle(in, 28)
	if got != in {
		t.Errorf("got %q, want unchanged %q", got, in)
	}
	if strings.HasSuffix(got, "…") {
		t.Errorf("must not append ellipsis when within limit, got %q", got)
	}
}

func TestDeriveAutoTitle_DefaultMaxRunesWhenZeroOrNegative(t *testing.T) {
	in := strings.Repeat("a", 100)
	for _, n := range []int{0, -1, -999} {
		got := deriveAutoTitle(in, n)
		body := strings.TrimSuffix(got, "…")
		if utf8.RuneCountInString(body) != autoTitleDefaultMaxRunes {
			t.Errorf("maxRunes=%d: body rune count = %d, want default %d",
				n, utf8.RuneCountInString(body), autoTitleDefaultMaxRunes)
		}
	}
}

// ----------------------------------------------------------------------------
// Engine.maybeAutoNameSession
// ----------------------------------------------------------------------------

// newAutoNameTestEngine constructs the minimal Engine state needed to
// exercise maybeAutoNameSession in isolation. This matches the
// constructor pattern used by other engine_test.go cases that do not
// need a real platform/agent.
func newAutoNameTestEngine() *Engine {
	return &Engine{
		sessions:              NewSessionManager(""),
		autoSessionName:       "first_message",
		autoSessionNameMaxLen: 28,
	}
}

// newBoundSession returns a session pre-bound to an agent session ID
// (so GetAgentSessionID() returns non-empty), with the given history.
func newBoundSession(t *testing.T, sm *SessionManager, agentSID string, history ...HistoryEntry) *Session {
	t.Helper()
	s := sm.GetOrCreateActive("test-user")
	s.SetAgentSessionID(agentSID, "cursor-agent")
	for _, h := range history {
		s.AddHistory(h.Role, h.Content)
	}
	return s
}

func TestMaybeAutoNameSession_FirstUserMessage(t *testing.T) {
	e := newAutoNameTestEngine()
	s := newBoundSession(t, e.sessions, "agent-sid-001",
		HistoryEntry{Role: "user", Content: "实现一个分页接口"},
	)

	e.maybeAutoNameSession(s)

	if got := e.sessions.GetSessionName("agent-sid-001"); got != "实现一个分页接口" {
		t.Errorf("session name = %q, want %q", got, "实现一个分页接口")
	}
}

func TestMaybeAutoNameSession_PreservesUserAssignedName(t *testing.T) {
	e := newAutoNameTestEngine()
	s := newBoundSession(t, e.sessions, "agent-sid-002",
		HistoryEntry{Role: "user", Content: "first prompt"},
	)
	// Simulate /name set by the user.
	e.sessions.SetSessionName("agent-sid-002", "my-custom-name")

	e.maybeAutoNameSession(s)

	if got := e.sessions.GetSessionName("agent-sid-002"); got != "my-custom-name" {
		t.Errorf("expected user-assigned name to win, got %q", got)
	}
}

func TestMaybeAutoNameSession_IdempotentOnSecondCall(t *testing.T) {
	e := newAutoNameTestEngine()
	s := newBoundSession(t, e.sessions, "agent-sid-003",
		HistoryEntry{Role: "user", Content: "原始第一条"},
	)
	e.maybeAutoNameSession(s)
	first := e.sessions.GetSessionName("agent-sid-003")

	// Simulate a second turn: another user message, then another call.
	s.AddHistory("assistant", "ok")
	s.AddHistory("user", "完全不同的第二条问题")
	e.maybeAutoNameSession(s)

	if got := e.sessions.GetSessionName("agent-sid-003"); got != first {
		t.Errorf("name changed across calls: %q -> %q (want stable)", first, got)
	}
}

func TestMaybeAutoNameSession_DisabledMode(t *testing.T) {
	e := newAutoNameTestEngine()
	e.autoSessionName = "off"
	s := newBoundSession(t, e.sessions, "agent-sid-004",
		HistoryEntry{Role: "user", Content: "should not be named"},
	)

	e.maybeAutoNameSession(s)

	if got := e.sessions.GetSessionName("agent-sid-004"); got != "" {
		t.Errorf("with mode=off, name should remain empty, got %q", got)
	}
}

func TestMaybeAutoNameSession_NoUserMessage(t *testing.T) {
	e := newAutoNameTestEngine()
	s := newBoundSession(t, e.sessions, "agent-sid-005",
		// Only assistant history (e.g. agent emitted a greeting before any user input).
		HistoryEntry{Role: "assistant", Content: "Hello!"},
	)

	e.maybeAutoNameSession(s)

	if got := e.sessions.GetSessionName("agent-sid-005"); got != "" {
		t.Errorf("name should remain empty when no user turn yet, got %q", got)
	}
}

func TestMaybeAutoNameSession_NoAgentSessionID(t *testing.T) {
	e := newAutoNameTestEngine()
	// Don't bind an agent session ID — represents the period before agent
	// has acknowledged the session.
	s := e.sessions.GetOrCreateActive("test-user")
	s.AddHistory("user", "early message before agent attaches")

	e.maybeAutoNameSession(s)

	// Confirm we wrote nothing into sessionNames (would have used "" key
	// on entry). Use len() on the map; not exposed publicly so probe via
	// GetSessionName("").
	if got := e.sessions.GetSessionName(""); got != "" {
		t.Errorf("unexpected name written for empty agent SID: %q", got)
	}
}

func TestMaybeAutoNameSession_DerivesEmptyTitleSkipsWrite(t *testing.T) {
	e := newAutoNameTestEngine()
	// First user message contains only noise that gets stripped to empty.
	s := newBoundSession(t, e.sessions, "agent-sid-006",
		HistoryEntry{Role: "user", Content: "https://example.com `code` @file ***"},
	)

	e.maybeAutoNameSession(s)

	if got := e.sessions.GetSessionName("agent-sid-006"); got != "" {
		t.Errorf("expected no write when derived title is empty, got %q", got)
	}
}

func TestMaybeAutoNameSession_NilGuards(t *testing.T) {
	// Must not panic on nil receiver or nil session.
	var e *Engine
	e.maybeAutoNameSession(nil) // both nil

	e2 := newAutoNameTestEngine()
	e2.maybeAutoNameSession(nil) // nil session
}

// TestMaybeAutoNameSession_ConcurrentSafe verifies that concurrent calls
// from multiple goroutines on different sessions do not race. Each
// goroutine targets its own agent session ID; the SessionManager's
// internal RWMutex serializes the writes.
func TestMaybeAutoNameSession_ConcurrentSafe(t *testing.T) {
	e := newAutoNameTestEngine()

	// Pre-create N sessions, each bound to its own agent SID with a
	// distinct first user message.
	const n = 32
	sessions := make([]*Session, n)
	for i := 0; i < n; i++ {
		userID := userKey(i)
		s := e.sessions.GetOrCreateActive(userID)
		s.SetAgentSessionID(agentSID(i), "cursor-agent")
		s.AddHistory("user", "并发命名第"+itoa(i)+"个")
		sessions[i] = s
	}

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			// Hammer: each session is named twice from two goroutines.
			e.maybeAutoNameSession(sessions[i])
			e.maybeAutoNameSession(sessions[i])
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		got := e.sessions.GetSessionName(agentSID(i))
		want := "并发命名第" + itoa(i) + "个"
		if got != want {
			t.Errorf("session %d: got %q, want %q", i, got, want)
		}
	}
}

// Tiny local helpers (avoid pulling in strconv just for two lines).
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

func userKey(i int) string { return "user-" + itoa(i) }
func agentSID(i int) string {
	return "agent-sid-concurrent-" + itoa(i)
}
