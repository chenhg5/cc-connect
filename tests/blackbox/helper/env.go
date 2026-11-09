// Package helper provides test environment setup for blackbox tests.
//
// BlackboxEnv wraps a real cc-connect Engine, a real Agent (Claude Code,
// Codex, etc.), and a MockPlatform. Tests inject messages and assert on what
// the platform receives — exactly what a real user would see.
package helper

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/chenhg5/cc-connect/tests/blackbox/fakemodel"
	bbplatform "github.com/chenhg5/cc-connect/tests/blackbox/platform"
)

const (
	// DefaultReplyTimeout is generous because real agents (Claude Code, etc.)
	// take 10-60 seconds to respond. Tests using the default timeout accept
	// that slowness is the price of real testing.
	DefaultReplyTimeout = 120 * time.Second

	// DefaultUser and DefaultChat are used by Send / helpers that don't need
	// multi-user scenarios.
	DefaultUser = "user1"
	DefaultChat = "chat1"
)

// Env is a fully wired blackbox test environment.
// Always create via NewEnv — never construct directly.
type Env struct {
	T        *testing.T
	Platform *bbplatform.MockPlatform
	// Engine is the in-process engine. It is nil for the binary backend; tests
	// that need it must call RequireInProcess first.
	Engine  *core.Engine
	WorkDir string
	DataDir string

	// Backend and Profile record how this Env was built.
	Backend Backend
	Profile bbplatform.Profile

	// agentType is stored for diagnostic messages only.
	agentType string

	// FakeModel is the scripted model server when Options.FakeModel is set.
	FakeModel *fakemodel.Server

	// binary backend only
	proc      *binaryProcess
	token     string
	bridgeURL string
	run       *binaryRun
}

// Backend selects how the cc-connect under test runs.
type Backend string

const (
	// BackendInProcess wires core.Engine directly inside the test process
	// (the historical blackbox behaviour, and the default).
	BackendInProcess Backend = "inprocess"
	// BackendBinary builds/starts the real cc-connect binary and drives it
	// through its bridge WebSocket. See binary.go.
	BackendBinary Backend = "binary"
)

// Options configures NewEnvWithOptions. The zero value matches NewEnv, with
// Backend and Profile taken from CC_BLACKBOX_BACKEND / CC_BLACKBOX_PROFILE.
type Options struct {
	// Backend overrides CC_BLACKBOX_BACKEND when set.
	Backend Backend
	// Profile overrides CC_BLACKBOX_PROFILE when set.
	Profile bbplatform.Profile
	// Setup configures the in-process engine before Start. Envs with a Setup
	// are skipped on the binary backend, which has no in-process engine.
	Setup func(*core.Engine)
	// FakeModel points the agent at a scripted fake model server
	// (tests/blackbox/fakemodel) instead of a real provider. Turns are then
	// deterministic and need no model key; drive them with FAKE: directives
	// in the message text. Supported: claudecode (Anthropic Messages) and
	// codex (OpenAI Responses).
	FakeModel bool
	// AgentOptions are merged into the agent's options (for example
	// {"backend": "app_server", "mode": "suggest"} for codex).
	AgentOptions map[string]any
	// NoCredentials only requires the agent CLI on PATH, not model
	// credentials. Use it for scenarios that never start an agent turn
	// (slash commands such as /help or /version), so they can run anywhere.
	NoCredentials bool
}

// BackendFromEnv returns the backend selected by CC_BLACKBOX_BACKEND.
func BackendFromEnv(t *testing.T) Backend {
	t.Helper()
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv("CC_BLACKBOX_BACKEND"))); v {
	case "", string(BackendInProcess):
		return BackendInProcess
	case string(BackendBinary):
		return BackendBinary
	default:
		t.Fatalf("blackbox: CC_BLACKBOX_BACKEND=%q (want inprocess or binary)", v)
		return ""
	}
}

// ProfileFromEnv returns the profile selected by CC_BLACKBOX_PROFILE.
func ProfileFromEnv(t *testing.T) bbplatform.Profile {
	t.Helper()
	pr, err := bbplatform.ParseProfile(os.Getenv("CC_BLACKBOX_PROFILE"))
	if err != nil {
		t.Fatalf("blackbox: CC_BLACKBOX_PROFILE: %v", err)
	}
	return pr
}

// RequireInProcess skips the test on the binary backend. Use it before
// touching e.Engine directly.
func (e *Env) RequireInProcess() {
	e.T.Helper()
	if e.Backend != BackendInProcess {
		e.T.Skipf("blackbox skip: needs the in-process engine (backend=%s)", e.Backend)
	}
}

// Click simulates DefaultUser clicking a button with callback value action
// (for example "perm:allow" or "cmd:/list") and waits for the next message.
func (e *Env) Click(action string, timeout time.Duration) *bbplatform.SentMessage {
	e.T.Helper()
	before := e.Platform.MessageCount()
	if err := e.Platform.InjectCardAction(DefaultUser, DefaultChat, action); err != nil {
		e.T.Fatalf("blackbox: click %q: %v", action, err)
	}
	reply := e.Platform.WaitForReply(before, timeout)
	if reply == nil {
		e.T.Fatalf("blackbox timeout (%v): no message after clicking %q\nbackend=%s profile=%s\nall messages so far:\n%s",
			timeout, action, e.Backend, e.Profile, e.Platform.AllText())
	}
	return reply
}

// NewEnv creates a blackbox test environment with a real agent.
//
// agentType selects the agent: "claudecode", "codex", "gemini", etc.
// The test is skipped (not failed) when:
//   - the agent CLI binary is not in PATH
//   - the required API credentials are missing from the environment
//
// A successful NewEnv call registers a t.Cleanup that stops the engine.
func NewEnv(t *testing.T, agentType string) *Env {
	return NewEnvWithSetup(t, agentType, nil)
}

// NewEnvWithSetup is like NewEnv but accepts an optional setup function that
// is called after the engine is created but BEFORE engine.Start(). Use this
// to configure engine options (display, banned words, disabled commands, etc.)
// that must be set before the engine begins processing messages.
//
// Example:
//
//	env := helper.NewEnvWithSetup(t, "claudecode", func(e *core.Engine) {
//	    e.SetShowContextIndicator(false)
//	    e.SetBannedWords([]string{"secret"})
//	})
func NewEnvWithSetup(t *testing.T, agentType string, setup func(*core.Engine)) *Env {
	t.Helper()
	return NewEnvWithOptions(t, agentType, Options{Setup: setup})
}

// NewEnvWithOptions creates a blackbox environment on the selected backend
// and capability profile. See Options.
func NewEnvWithOptions(t *testing.T, agentType string, o Options) *Env {
	t.Helper()

	backend := o.Backend
	if backend == "" {
		backend = BackendFromEnv(t)
	}
	profile := o.Profile
	if profile == "" {
		profile = ProfileFromEnv(t)
	}
	if backend == BackendBinary && o.Setup != nil {
		t.Skip("blackbox skip: this test configures the in-process engine directly (backend=binary)")
	}

	var fake *fakemodel.Server
	switch {
	case o.FakeModel:
		if agentType != "claudecode" && agentType != "codex" {
			t.Skipf("blackbox skip: the fake model speaks the Anthropic and OpenAI Responses APIs only (agent=%s)", agentType)
		}
		requireAgentBinary(t, agentType)
		fake = fakemodel.New()
		t.Cleanup(fake.Close)
	case o.NoCredentials:
		requireAgentBinary(t, agentType)
	default:
		requireAgent(t, agentType)
	}

	workDir := t.TempDir()

	opts := map[string]any{
		"work_dir": workDir,
	}

	// Cursor requires force/yolo mode in automated tests to bypass interactive
	// workspace-trust prompts. Without --force the agent exits immediately.
	if agentType == "cursor" {
		opts["mode"] = "force"
	}

	for k, v := range o.AgentOptions {
		opts[k] = v
	}

	if fake == nil {
		applyProviderFromEnv(t, agentType, opts)
	} else if agentType == "codex" {
		// cc-connect writes the provider into $CODEX_HOME/config.toml and
		// auth.json; keep that away from the developer's ~/.codex.
		opts["codex_home"] = lingeringTempDir(t, "codex-home-")
	}

	if backend == BackendBinary {
		provider := providerFromEnv(agentType)
		if fake != nil {
			provider = fakeProvider(fake, agentType).toml()
		}
		env := newBinaryEnv(t, agentType, opts, profile, provider)
		env.FakeModel = fake
		return env
	}

	dataDir := t.TempDir()

	agent, err := core.CreateAgent(agentType, opts)
	if err != nil {
		t.Skipf("blackbox skip: cannot create %s agent: %v", agentType, err)
	}

	if fake != nil {
		ps, ok := agent.(core.ProviderSwitcher)
		if !ok {
			t.Fatalf("blackbox: %s cannot take a provider, so it cannot use the fake model", agentType)
		}
		pc := fakeProvider(fake, agentType)
		ps.SetProviders([]core.ProviderConfig{pc.core()})
		ps.SetActiveProvider(pc.Name)
	} else {
		wireProviders(t, agentType, agent)
	}

	mp := bbplatform.New(agentType + "-mock")

	sessPath := filepath.Join(dataDir, "sessions.json")
	engine := core.NewEngine("blackbox-"+t.Name(), agent, []core.Platform{mp.Platform(profile)}, sessPath, core.LangEnglish)

	if o.Setup != nil {
		o.Setup(engine)
	}

	if err := engine.Start(); err != nil {
		t.Fatalf("blackbox: engine.Start failed: %v", err)
	}

	t.Cleanup(func() {
		engine.Stop()
		agent.Stop()
	})

	return &Env{
		T:         t,
		Platform:  mp,
		FakeModel: fake,
		Engine:    engine,
		WorkDir:   workDir,
		DataDir:   dataDir,
		Backend:   BackendInProcess,
		Profile:   profile,
		agentType: agentType,
	}
}

// ── Message helpers ───────────────────────────────────────────────────────────

// Send injects a message from DefaultUser in DefaultChat and waits up to
// DefaultReplyTimeout for the first reply. Fails the test on timeout.
func (e *Env) Send(content string) *bbplatform.SentMessage {
	e.T.Helper()
	return e.SendAs(DefaultUser, DefaultChat, content, DefaultReplyTimeout)
}

// SendWithTimeout is like Send but uses a custom timeout.
func (e *Env) SendWithTimeout(content string, timeout time.Duration) *bbplatform.SentMessage {
	e.T.Helper()
	return e.SendAs(DefaultUser, DefaultChat, content, timeout)
}

// SendAs injects a message from a specific user/chat pair and waits for a
// reply. Use this for multi-user isolation tests.
func (e *Env) SendAs(userID, chatID, content string, timeout time.Duration) *bbplatform.SentMessage {
	e.T.Helper()
	before := e.Platform.MessageCount()
	e.Platform.InjectMessage(userID, chatID, content)
	reply := e.Platform.WaitForReply(before, timeout)
	if reply == nil {
		e.T.Fatalf(
			"blackbox timeout (%v): no reply to %q from %s/%s\nagent=%s\nall messages so far:\n%s",
			timeout, content, userID, chatID, e.agentType,
			e.Platform.AllText(),
		)
	}
	return reply
}

// SendComplete injects a message and waits for the full agent turn to finish
// (i.e., the message stream is stable for idlePeriod). Returns all messages
// sent during the turn. Use this instead of Send when you need the turn to be
// fully done before sending the next message (multi-turn context tests).
func (e *Env) SendComplete(content string) []*bbplatform.SentMessage {
	e.T.Helper()
	return e.SendCompleteAs(DefaultUser, DefaultChat, content, 5*time.Second, DefaultReplyTimeout)
}

// SendCompleteAs is like SendComplete but for a specific user/chat.
func (e *Env) SendCompleteAs(userID, chatID, content string, idlePeriod, timeout time.Duration) []*bbplatform.SentMessage {
	e.T.Helper()
	before := e.Platform.MessageCount()
	e.Platform.InjectMessage(userID, chatID, content)
	msgs := e.Platform.WaitForTurnComplete(before, idlePeriod, timeout)
	if msgs == nil {
		e.T.Fatalf(
			"blackbox timeout (%v): no complete turn for %q from %s/%s\nagent=%s\nall messages so far:\n%s",
			timeout, content, userID, chatID, e.agentType,
			e.Platform.AllText(),
		)
	}
	return msgs
}

// LastText returns the text of the last message in a turn's message slice.
// Convenient for context retention assertions.
func LastText(msgs []*bbplatform.SentMessage) string {
	if len(msgs) == 0 {
		return ""
	}
	return msgs[len(msgs)-1].Text()
}

// AnyText returns all texts joined, for searching across multi-message turns.
func AnyText(msgs []*bbplatform.SentMessage) string {
	parts := make([]string, len(msgs))
	for i, m := range msgs {
		parts[i] = m.Text()
	}
	return strings.Join(parts, " ")
}

// SendNoWait injects a message but does not wait for a reply.
// Use for /stop scenarios where you send a command mid-processing.
func (e *Env) SendNoWait(content string) {
	e.Platform.InjectMessage(DefaultUser, DefaultChat, content)
}

// SendNoWaitAs injects from a specific user/chat without waiting.
func (e *Env) SendNoWaitAs(userID, chatID, content string) {
	e.Platform.InjectMessage(userID, chatID, content)
}

// ExpectReply waits for the next reply (after the current message count).
// Fails the test if nothing arrives before timeout.
func (e *Env) ExpectReply(timeout time.Duration) *bbplatform.SentMessage {
	e.T.Helper()
	before := e.Platform.MessageCount()
	reply := e.Platform.WaitForReply(before, timeout)
	if reply == nil {
		e.T.Fatalf(
			"blackbox timeout (%v): expected a reply\nagent=%s\nall messages so far:\n%s",
			timeout, e.agentType, e.Platform.AllText(),
		)
	}
	return reply
}

// WaitForMessageContaining waits for any message (from startIdx) containing
// substr (case-insensitive). Fails on timeout.
func (e *Env) WaitForMessageContaining(startIdx int, substr string, timeout time.Duration) *bbplatform.SentMessage {
	e.T.Helper()
	msg := e.Platform.WaitForMessageContaining(startIdx, substr, timeout)
	if msg == nil {
		e.T.Fatalf(
			"blackbox timeout (%v): no message containing %q\nagent=%s\nall messages:\n%s",
			timeout, substr, e.agentType, e.Platform.AllText(),
		)
	}
	return msg
}

// SessionKey returns the session key for the default user/chat, matching the
// format cc-connect uses internally: "<platform>:<chat>:<user>".
func (e *Env) SessionKey() string {
	return fmt.Sprintf("%s:%s:%s", e.Platform.Name(), DefaultChat, DefaultUser)
}

// SessionKeyFor returns the session key for a specific user/chat.
func (e *Env) SessionKeyFor(userID, chatID string) string {
	return fmt.Sprintf("%s:%s:%s", e.Platform.Name(), chatID, userID)
}

// ── Skip guards ───────────────────────────────────────────────────────────────

// requireAgent skips (never fails) if the agent binary or API credentials are
// unavailable.  The distinction between skip and fail is intentional: missing
// credentials mean the test simply cannot run in this environment; a logical
// assertion failure inside the test is a real failure.
func requireAgent(t *testing.T, agentType string) {
	t.Helper()
	requireAgentBinary(t, agentType)
	requireCredentials(t, agentType)
}

// requireAgentBinary skips when the agent CLI is not on PATH.
func requireAgentBinary(t *testing.T, agentType string) {
	t.Helper()

	// agentBinName returns the primary CLI binary name for the agent type.
	// The cursor type uses "agent" (from @anthropic-ai/cursor-agent), not
	// the Cursor IDE "cursor" binary.
	bin := agentBinName(agentType)
	if bin == "" {
		skipOrFail(t, agentType, "unknown agent type %q", agentType)
		return
	}
	if _, err := exec.LookPath(bin); err != nil {
		// For cursor, also accept if the "cursor" IDE binary is present
		// (some setups install it under that name instead).
		if agentType == "cursor" {
			if _, err2 := exec.LookPath("cursor"); err2 != nil {
				skipOrFail(t, agentType, "cursor agent binary %q not in PATH (also checked 'cursor')", bin)
			}
		} else {
			skipOrFail(t, agentType, "%s binary %q not in PATH", agentType, bin)
		}
	}
}

// RequiredAgents returns the agents listed in CC_BLACKBOX_REQUIRE_AGENTS
// (comma separated). A release smoke run sets it so that a missing agent
// CLI fails the run instead of silently skipping its scenarios.
func RequiredAgents() []string {
	var out []string
	for _, a := range strings.Split(os.Getenv("CC_BLACKBOX_REQUIRE_AGENTS"), ",") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

func isRequiredAgent(agentType string) bool {
	for _, a := range RequiredAgents() {
		if strings.EqualFold(a, agentType) {
			return true
		}
	}
	return false
}

// skipOrFail skips the test, unless agentType is required by
// CC_BLACKBOX_REQUIRE_AGENTS, in which case the missing prerequisite is a
// failure: a release gate must not pass because its tests did not run.
func skipOrFail(t *testing.T, agentType, format string, args ...any) {
	t.Helper()
	msg := fmt.Sprintf(format, args...)
	if isRequiredAgent(agentType) {
		t.Fatalf("blackbox: required agent %s unavailable (CC_BLACKBOX_REQUIRE_AGENTS): %s", agentType, msg)
	}
	t.Skipf("blackbox skip: %s", msg)
}

// requireCredentials skips when the model credentials for agentType are
// missing.
func requireCredentials(t *testing.T, agentType string) {
	t.Helper()
	switch agentType {
	case "claudecode":
		if os.Getenv("ANTHROPIC_API_KEY") == "" && !hasProviderEnv("claudecode") {
			t.Skipf("blackbox skip: ANTHROPIC_API_KEY not set")
		}
	case "codex":
		if os.Getenv("OPENAI_API_KEY") == "" && !hasProviderEnv("codex") {
			t.Skipf("blackbox skip: OPENAI_API_KEY not set")
		}
	case "gemini":
		if os.Getenv("GEMINI_API_KEY") == "" && os.Getenv("GOOGLE_API_KEY") == "" && !hasProviderEnv("gemini") {
			t.Skipf("blackbox skip: GEMINI_API_KEY or GOOGLE_API_KEY not set")
		}
	case "cursor":
		// Cursor can run without an explicit API key if the user is already
		// authenticated via `cursor login`. Only skip when the binary is absent
		// (handled above). Let it fail naturally with an auth error if needed.
	case "opencode":
		if os.Getenv("ANTHROPIC_API_KEY") == "" && !hasProviderEnv("opencode") {
			t.Skipf("blackbox skip: ANTHROPIC_API_KEY not set for opencode")
		}
	}
}

// hasProviderEnv checks if a CC_BLACKBOX_<AGENT>_API_KEY override is set.
// Allows CI to inject credentials specifically for blackbox tests without
// polluting the main API key env vars.
func hasProviderEnv(agentType string) bool {
	key := "CC_BLACKBOX_" + strings.ToUpper(agentType) + "_API_KEY"
	return os.Getenv(key) != ""
}

// applyProviderFromEnv injects API credentials into agent opts.
// Preference order:
//  1. CC_BLACKBOX_<AGENT>_BASE_URL + CC_BLACKBOX_<AGENT>_API_KEY (test-specific)
//  2. Standard env vars (ANTHROPIC_API_KEY, OPENAI_API_KEY, etc.)
func applyProviderFromEnv(t *testing.T, agentType string, opts map[string]any) {
	t.Helper()
	prefix := "CC_BLACKBOX_" + strings.ToUpper(agentType) + "_"

	apiKey := os.Getenv(prefix + "API_KEY")
	baseURL := os.Getenv(prefix + "BASE_URL")
	model := os.Getenv(prefix + "MODEL")

	if apiKey == "" {
		switch agentType {
		case "claudecode":
			apiKey = os.Getenv("ANTHROPIC_API_KEY")
			baseURL = os.Getenv("ANTHROPIC_BASE_URL")
		case "codex":
			apiKey = os.Getenv("OPENAI_API_KEY")
			baseURL = os.Getenv("OPENAI_BASE_URL")
		case "gemini":
			apiKey = firstNonEmpty(os.Getenv("GEMINI_API_KEY"), os.Getenv("GOOGLE_API_KEY"))
		case "opencode":
			apiKey = os.Getenv("ANTHROPIC_API_KEY")
			baseURL = os.Getenv("ANTHROPIC_BASE_URL")
		case "cursor":
			apiKey = firstNonEmpty(os.Getenv("CURSOR_API_KEY"), os.Getenv("ANTHROPIC_API_KEY"))
		}
	}

	if apiKey != "" {
		opts["api_key"] = apiKey
	}
	if baseURL != "" {
		opts["base_url"] = baseURL
	}
	if model != "" {
		opts["model"] = model
	}
}

// wireProviders calls SetProviders on the agent when CC_BLACKBOX_<AGENT>_API_KEY
// is set, giving agents (e.g. claudecode) their credentials via the provider
// interface rather than relying solely on ANTHROPIC_API_KEY env vars.
//
// Supported env vars (prefix = CC_BLACKBOX_<AGENTTYPE>_):
//
//	<prefix>API_KEY   — required (except cursor which can use local login)
//	<prefix>BASE_URL  — provider base URL / proxy endpoint
//	<prefix>MODEL     — model override
//	<prefix>WIRE_API  — codex wire API format, e.g. "chat" or "responses"
//
// Agent-specific base URL injection:
//   - claudecode: base_url → stored in ProviderConfig.BaseURL (provider sets ANTHROPIC_BASE_URL)
//   - opencode:   base_url → injected as ANTHROPIC_BASE_URL env var via ProviderConfig.Env
//   - cursor:     no base_url injection (uses CURSOR_API_KEY or local auth)
func wireProviders(t *testing.T, agentType string, agent core.Agent) {
	t.Helper()
	ps, ok := agent.(core.ProviderSwitcher)
	if !ok {
		return
	}

	prefix := "CC_BLACKBOX_" + strings.ToUpper(agentType) + "_"
	apiKey := os.Getenv(prefix + "API_KEY")
	baseURL := os.Getenv(prefix + "BASE_URL")
	model := os.Getenv(prefix + "MODEL")
	wireAPI := os.Getenv(prefix + "WIRE_API")

	// Skip if no explicit API key override (opencode and cursor can fall back
	// to their native auth from env or local login).
	if apiKey == "" && agentType != "cursor" && agentType != "opencode" {
		return
	}
	// For cursor/opencode with no API key, still wire a provider if we have model.
	if apiKey == "" && model == "" {
		return
	}

	provider := core.ProviderConfig{
		Name:         "blackbox-test",
		APIKey:       apiKey,
		BaseURL:      baseURL,
		Model:        model,
		CodexWireAPI: wireAPI,
	}

	// opencode's providerEnvLocked only injects APIKey (as ANTHROPIC_API_KEY)
	// and the Env map. To use a custom proxy (e.g. minimax), inject BaseURL as
	// ANTHROPIC_BASE_URL via the Env map so opencode picks it up.
	if agentType == "opencode" && baseURL != "" {
		if provider.Env == nil {
			provider.Env = make(map[string]string)
		}
		provider.Env["ANTHROPIC_BASE_URL"] = baseURL
	}

	ps.SetProviders([]core.ProviderConfig{provider})
	ps.SetActiveProvider("blackbox-test")
	t.Logf("blackbox: wired provider base_url=%s model=%s wire_api=%s", baseURL, model, wireAPI)
}

func agentBinName(agentType string) string {
	switch agentType {
	case "claudecode":
		return "claude"
	case "codex":
		return "codex"
	case "gemini":
		return "gemini"
	case "cursor":
		// Cursor Agent CLI installed via npm: @anthropic-ai/cursor-agent.
		// The binary is named "agent" by default; some installations also link
		// it as "cursor". We try "agent" first.
		return "agent"
	case "opencode":
		return "opencode"
	case "qoder":
		return "qoder"
	case "kimi":
		return "kimi"
	default:
		return agentType
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// fakeProviderConfig is the provider that points an agent at the fake model.
type fakeProviderConfig struct {
	Name, APIKey, BaseURL string
	// WireAPI is the Codex wire format ("responses"); empty for Claude Code.
	WireAPI string
	Env     map[string]string
}

func fakeProvider(fake *fakemodel.Server, agentType string) fakeProviderConfig {
	p := fakeProviderConfig{
		Name:    "blackbox-fake",
		APIKey:  "sk-blackbox-fake",
		BaseURL: fake.URL(),
		// Keep loopback traffic off any HTTP(S) proxy in the environment.
		Env: map[string]string{"NO_PROXY": "127.0.0.1,localhost", "no_proxy": "127.0.0.1,localhost"},
	}
	if agentType == "codex" {
		// Codex appends /responses to the provider base URL.
		p.BaseURL = fake.URL() + "/v1"
		p.WireAPI = "responses"
	}
	return p
}

func (p fakeProviderConfig) core() core.ProviderConfig {
	return core.ProviderConfig{Name: p.Name, APIKey: p.APIKey, BaseURL: p.BaseURL, Env: p.Env, CodexWireAPI: p.WireAPI}
}

func (p fakeProviderConfig) toml() map[string]any {
	m := map[string]any{"name": p.Name, "api_key": p.APIKey, "base_url": p.BaseURL, "env": p.Env}
	if p.WireAPI != "" {
		m["codex"] = map[string]any{"wire_api": p.WireAPI}
	}
	return m
}

// lingeringTempDir is like t.TempDir for directories that a child process may
// still write to for a moment after the test stops it. Codex, for example,
// finishes a background plugin clone in $CODEX_HOME after its session ends,
// which makes t.TempDir's cleanup fail with "directory not empty". The
// directory is removed with retries, and a leftover is only logged.
func lingeringTempDir(t *testing.T, prefix string) string {
	t.Helper()
	dir, err := os.MkdirTemp(scratchRoot(t), prefix)
	if err != nil {
		t.Fatalf("blackbox: temp dir: %v", err)
	}
	t.Cleanup(func() {
		var err error
		for i := 0; i < 50; i++ {
			if err = os.RemoveAll(dir); err == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Logf("blackbox: could not remove %s after 5s: %v", dir, err)
	})
	return dir
}

// scratchRoot is where lingeringTempDir creates directories: under the user
// cache dir rather than the system temp dir, because Codex refuses to set up
// its helper binaries when CODEX_HOME is under /tmp and reports that on every
// failed turn, which no real deployment does.
func scratchRoot(t *testing.T) string {
	t.Helper()
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	root := filepath.Join(base, "cc-connect-blackbox")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return ""
	}
	return root
}
