package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type nativeTestAgent struct {
	mu           sync.RWMutex
	name         string
	workDir      string
	commands     []NativeSlashCommand
	discoveryErr error
	policies     map[string]NativeSlashCommand
	skillDirs    []string
	commandDirs  []string
	session      AgentSession
	discoveries  int
	mode         string
}

type nativeProviderTestAgent struct {
	*nativeTestAgent
	providers []ProviderConfig
	active    string
}

func (a *nativeProviderTestAgent) ListProviders() []ProviderConfig {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]ProviderConfig(nil), a.providers...)
}

func (a *nativeProviderTestAgent) SetProviders(providers []ProviderConfig) {
	a.mu.Lock()
	a.providers = append([]ProviderConfig(nil), providers...)
	a.mu.Unlock()
}

func (a *nativeProviderTestAgent) GetActiveProvider() *ProviderConfig {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for i := range a.providers {
		if a.providers[i].Name == a.active {
			provider := a.providers[i]
			return &provider
		}
	}
	return nil
}

func (a *nativeProviderTestAgent) SetActiveProvider(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if name == "" {
		a.active = ""
		return true
	}
	for _, provider := range a.providers {
		if provider.Name == name {
			a.active = name
			return true
		}
	}
	return false
}

func (a *nativeProviderTestAgent) NativeSlashCommands(context.Context) ([]NativeSlashCommand, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.discoveries++
	for _, provider := range a.providers {
		if provider.Name == a.active {
			home := provider.Env["CAP_HOME"]
			return []NativeSlashCommand{{Name: "from-" + home, Target: "from-" + home, Description: home}}, nil
		}
	}
	return nil, nil
}

func (a *nativeTestAgent) Name() string {
	if a.name != "" {
		return a.name
	}
	return "grok"
}
func (a *nativeTestAgent) StartSession(context.Context, string) (AgentSession, error) {
	if a.session != nil {
		return a.session, nil
	}
	return &stubAgentSession{}, nil
}
func (a *nativeTestAgent) ListSessions(context.Context) ([]AgentSessionInfo, error) { return nil, nil }
func (a *nativeTestAgent) Stop() error                                              { return nil }
func (a *nativeTestAgent) GetWorkDir() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.workDir
}
func (a *nativeTestAgent) SetWorkDir(workDir string) {
	a.mu.Lock()
	a.workDir = workDir
	a.mu.Unlock()
}
func (a *nativeTestAgent) NativeSlashCommands(context.Context) ([]NativeSlashCommand, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.discoveries++
	if a.discoveryErr != nil {
		return nil, a.discoveryErr
	}
	return append([]NativeSlashCommand(nil), a.commands...), nil
}
func (a *nativeTestAgent) NativeSlashPolicy(name string) (NativeSlashCommand, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	command, ok := a.policies[strings.ToLower(strings.ReplaceAll(name, "_", "-"))]
	return command, ok
}
func (a *nativeTestAgent) SkillDirs() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]string(nil), a.skillDirs...)
}
func (a *nativeTestAgent) CommandDirs() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]string(nil), a.commandDirs...)
}
func (a *nativeTestAgent) SetMode(mode string) {
	a.mu.Lock()
	a.mode = mode
	a.mu.Unlock()
}
func (a *nativeTestAgent) GetMode() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.mode
}
func (a *nativeTestAgent) PermissionModes() []PermissionModeInfo {
	return []PermissionModeInfo{
		{Key: "auto", Name: "Auto", NameZh: "自动"},
		{Key: "yolo", Name: "Yolo", NameZh: "全部允许"},
	}
}

type nativeCaptureSession struct {
	mu      sync.Mutex
	events  chan Event
	sent    chan string
	prompts []string
	images  []int
	autoEnd bool
}

type nativeMenuPlatform struct {
	stubPlatformEngine
	mu         sync.Mutex
	registered []BotCommandInfo
	updates    chan []BotCommandInfo
}

func newNativeMenuPlatform(name string) *nativeMenuPlatform {
	return &nativeMenuPlatform{
		stubPlatformEngine: stubPlatformEngine{n: name},
		updates:            make(chan []BotCommandInfo, 8),
	}
}

func (p *nativeMenuPlatform) RegisterCommands(commands []BotCommandInfo) error {
	copy := append([]BotCommandInfo(nil), commands...)
	p.mu.Lock()
	p.registered = copy
	p.mu.Unlock()
	p.updates <- copy
	return nil
}

func waitNativeMenu(t *testing.T, platform *nativeMenuPlatform) []BotCommandInfo {
	t.Helper()
	select {
	case commands := <-platform.updates:
		return commands
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for command menu registration")
		return nil
	}
}

func newNativeCaptureSession(autoEnd bool) *nativeCaptureSession {
	return &nativeCaptureSession{
		events:  make(chan Event, 8),
		sent:    make(chan string, 8),
		autoEnd: autoEnd,
	}
}

func (s *nativeCaptureSession) Send(prompt, _ string, images []ImageAttachment, _ []FileAttachment) error {
	s.mu.Lock()
	s.prompts = append(s.prompts, prompt)
	s.images = append(s.images, len(images))
	s.mu.Unlock()
	s.sent <- prompt
	if s.autoEnd {
		s.events <- Event{Type: EventResult, Content: "ok", Done: true}
	}
	return nil
}
func (s *nativeCaptureSession) RespondPermission(string, PermissionResult) error { return nil }
func (s *nativeCaptureSession) Events() <-chan Event                             { return s.events }
func (s *nativeCaptureSession) CurrentSessionID() string                         { return "native-session" }
func (s *nativeCaptureSession) Alive() bool                                      { return true }
func (s *nativeCaptureSession) Close() error                                     { return nil }

func waitNativePrompt(t *testing.T, session *nativeCaptureSession) string {
	t.Helper()
	select {
	case prompt := <-session.sent:
		return prompt
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for native prompt")
		return ""
	}
}

func waitNativeTurnFinished(t *testing.T, engine *Engine, sessionKey string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		session := engine.sessions.GetOrCreateActive(sessionKey)
		if session.TryLock() {
			session.UnlockWithoutUpdate()
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for native turn to finish")
}

func TestNativeSlashDiscoveryIsAuthoritativeWithFilesystemFallbackOnError(t *testing.T) {
	skillRoot := t.TempDir()
	writeSkillFile(t, filepath.Join(skillRoot, "filesystem-skill", "SKILL.md"), "filesystem body")
	commandRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(commandRoot, "filesystem-command.md"), []byte("filesystem command"), 0o644))

	agent := &nativeTestAgent{
		workDir:     t.TempDir(),
		skillDirs:   []string{skillRoot},
		commandDirs: []string{commandRoot},
		commands: []NativeSlashCommand{{
			Name: "native-skill", Target: "native-skill", Description: "native", IsSkill: true,
		}},
	}
	e := NewEngine("native", agent, nil, "", LangEnglish)
	require.Equal(t, []string{"native_skill"}, skillNames(e.ListSkills()))
	_, found := e.commands.Resolve("filesystem-command")
	assert.False(t, found, "successful native discovery must skip filesystem commands")

	fallbackAgent := &nativeTestAgent{
		workDir:      t.TempDir(),
		skillDirs:    []string{skillRoot},
		commandDirs:  []string{commandRoot},
		discoveryErr: errors.New("private stderr must not escape"),
	}
	fallback := NewEngine("fallback", fallbackAgent, nil, "", LangEnglish)
	require.Equal(t, []string{"filesystem-skill"}, skillNames(fallback.ListSkills()))
	_, found = fallback.commands.Resolve("filesystem-command")
	assert.False(t, found, "native provider fallback must remain scoped to its agent/workspace")
	resolved, found := fallback.resolveScopedFallbackCommand(fallbackAgent, "filesystem-command")
	require.True(t, found)
	assert.Equal(t, "filesystem command", strings.TrimSpace(resolved.Prompt))
}

func TestNativeSlashFilesystemFallbackIsWorkspaceScopedAndRecovers(t *testing.T) {
	commandRootA := t.TempDir()
	commandRootB := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(commandRootA, "alpha-command.md"), []byte("alpha {{args}}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(commandRootB, "beta-command.md"), []byte("beta {{args}}"), 0o644))
	skillRootA := t.TempDir()
	skillRootB := t.TempDir()
	writeSkillFile(t, filepath.Join(skillRootA, "alpha-skill", "SKILL.md"), "alpha skill")
	writeSkillFile(t, filepath.Join(skillRootB, "beta-skill", "SKILL.md"), "beta skill")

	sessionA := newNativeCaptureSession(true)
	sessionB := newNativeCaptureSession(true)
	agentA := &nativeTestAgent{
		workDir: t.TempDir(), discoveryErr: errors.New("unavailable"), session: sessionA,
		commandDirs: []string{commandRootA}, skillDirs: []string{skillRootA},
	}
	agentB := &nativeTestAgent{
		workDir: t.TempDir(), discoveryErr: errors.New("unavailable"), session: sessionB,
		commandDirs: []string{commandRootB}, skillDirs: []string{skillRootB},
	}
	e := NewEngine("scoped-fallback", agentA, nil, "", LangEnglish)
	require.False(t, e.refreshNativeSlashCommands(context.Background(), agentB))

	assert.Equal(t, []string{"alpha-skill"}, skillNames(e.listSkillsForAgent(agentA)))
	assert.Equal(t, []string{"beta-skill"}, skillNames(e.listSkillsForAgent(agentB)))
	_, found := e.resolveScopedFallbackCommand(agentA, "beta-command")
	assert.False(t, found)
	_, found = e.resolveScopedFallbackCommand(agentB, "alpha-command")
	assert.False(t, found)
	menuNames := commandNames(e.GetAllCommands())
	assert.Contains(t, menuNames, "alpha-command")
	assert.Contains(t, menuNames, "beta-command")
	assert.Contains(t, menuNames, "alpha-skill")
	assert.Contains(t, menuNames, "beta-skill")

	p := &stubPlatformEngine{n: "scoped"}
	e.agent = agentA
	msgA := &Message{SessionKey: "scoped:a", UserID: "u", Content: "/alpha-command one", ReplyCtx: "a"}
	require.True(t, e.handleCommand(p, msgA, msgA.Content))
	assert.Contains(t, waitNativePrompt(t, sessionA), "alpha one")
	waitNativeTurnFinished(t, e, msgA.SessionKey)
	e.agent = agentB
	msgB := &Message{SessionKey: "scoped:b", UserID: "u", Content: "/beta-command two", ReplyCtx: "b"}
	require.True(t, e.handleCommand(p, msgB, msgB.Content))
	assert.Contains(t, waitNativePrompt(t, sessionB), "beta two")
	waitNativeTurnFinished(t, e, msgB.SessionKey)
	e.commands.Add("beta-command", "configured beta", "configured {{args}}", "", "", "config")
	configured := &Message{SessionKey: "scoped:b", UserID: "u", Content: "/beta-command three", ReplyCtx: "b2"}
	require.True(t, e.handleCommand(p, configured, configured.Content))
	configuredPrompt := waitNativePrompt(t, sessionB)
	assert.Contains(t, configuredPrompt, "configured three")
	assert.NotContains(t, configuredPrompt, "beta three")
	waitNativeTurnFinished(t, e, configured.SessionKey)

	agentA.mu.Lock()
	agentA.discoveryErr = nil
	agentA.commands = []NativeSlashCommand{{Name: "recovered", Target: "recovered", Description: "recovered"}}
	agentA.mu.Unlock()
	require.True(t, e.refreshNativeSlashCommands(context.Background(), agentA))
	_, found = e.resolveScopedFallbackCommand(agentA, "alpha-command")
	assert.False(t, found)
	assert.NotContains(t, commandNames(e.GetAllCommands()), "alpha-command")
	assert.Contains(t, commandNames(e.GetAllCommands()), "beta-command")
}

func TestNativeSlashPolicyFallbackOnlyAppliesAfterDiscoveryFailure(t *testing.T) {
	policy := NativeSlashCommand{Name: "always-approve", Target: "always-approve", AdminOnly: true, PolicyCommand: "mode"}
	authoritative := &nativeTestAgent{
		workDir:  t.TempDir(),
		policies: map[string]NativeSlashCommand{"always-approve": policy},
	}
	e := NewEngine("authoritative", authoritative, nil, "", LangEnglish)
	p := &stubPlatformEngine{n: "policy"}
	msg := &Message{SessionKey: "policy:a", UserID: "u", Content: "/always-approve", ReplyCtx: "ctx"}
	assert.False(t, e.handleCommand(p, msg, msg.Content), "successful discovery is authoritative even for known policy names")
	assert.False(t, msg.nativeSlash)

	commandRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(commandRoot, "always-approve.md"), []byte("must not execute"), 0o644))
	failed := &nativeTestAgent{
		workDir: t.TempDir(), commandDirs: []string{commandRoot}, discoveryErr: errors.New("unavailable"),
		policies: map[string]NativeSlashCommand{"always-approve": policy},
	}
	e = NewEngine("failed", failed, nil, "", LangEnglish)
	blocked := &Message{SessionKey: "policy:b", UserID: "u", Content: "/always-approve", ReplyCtx: "ctx"}
	assert.True(t, e.handleCommand(p, blocked, blocked.Content))
	assert.False(t, blocked.nativeSlash)
	require.NotEmpty(t, p.sent)
	assert.Contains(t, p.sent[len(p.sent)-1], "admin privilege")
}

func skillNames(skills []*Skill) []string {
	names := make([]string, 0, len(skills))
	for _, skill := range skills {
		names = append(names, skill.Name)
	}
	return names
}

func TestNativeSlashAliasesAvoidBuiltinsAndCustomCommands(t *testing.T) {
	agent := &nativeTestAgent{
		workDir: t.TempDir(),
		commands: []NativeSlashCommand{
			{Name: "compact", Target: "compact", Description: "native compact"},
			{Name: "skill", Target: "skill", Description: "native skill", IsSkill: true},
			{Name: "plugin:review", Target: "plugin:review", Description: "qualified", IsSkill: true},
			{Name: "review", Target: "review", Description: "native review", IsSkill: true},
		},
	}
	e := NewEngine("native", agent, nil, "", LangEnglish)

	assert.Equal(t, "grok_compact", commandByDescription(t, e.GetAllCommands(), "native compact").Command)
	assert.Equal(t, "grok_skill", commandByDescription(t, e.GetAllCommands(), "native skill").Command)
	assert.Equal(t, "plugin_review", commandByDescription(t, e.GetAllCommands(), "qualified").Command)
	assert.Equal(t, "review", commandByDescription(t, e.GetAllCommands(), "native review").Command)
	p := &stubPlatformEngine{n: "discord"}
	exactSkill := &Message{SessionKey: "discord:c:u", Platform: "discord", UserID: "u", Content: "/skill"}
	assert.True(t, e.handleCommand(p, exactSkill, exactSkill.Content), "cc-connect /skills alias keeps priority")
	assert.False(t, exactSkill.nativeSlash)
	nativeSkill := &Message{SessionKey: "discord:c:u", Platform: "discord", UserID: "u", Content: "/grok_skill"}
	assert.False(t, e.handleCommand(p, nativeSkill, nativeSkill.Content))
	assert.Equal(t, "/skill", nativeSkill.Content)

	e.commands.Add("review", "configured review", "configured prompt", "", "", "config")
	commands := e.GetAllCommands()
	assert.Equal(t, "review", commandByDescription(t, commands, "configured review").Command)
	assert.Equal(t, "grok_review", commandByDescription(t, commands, "native review").Command)
}

func TestCommandMenuPolicyKeepsEveryNativeCommandAtHardLimit(t *testing.T) {
	var commands []BotCommandInfo
	for index := 0; index < 43; index++ {
		commands = append(commands, BotCommandInfo{Command: "cc_" + string(rune('a'+index%26)) + string(rune('a'+index/26))})
	}
	for index := 0; index < 59; index++ {
		name := "native_" + string(rune('a'+index%26)) + string(rune('a'+index/26))
		if index == 57 {
			name = "review"
		}
		if index == 58 {
			name = "andrej_karpathy_skills_karpathy"
		}
		commands = append(commands, BotCommandInfo{
			Command:  name,
			IsNative: true,
		})
	}

	plan := applyCommandMenuPolicy(commands, CommandMenuPolicy{Limit: 100, PreserveNative: true}, strings.ToLower)
	menu := plan.commands
	require.Len(t, menu, 100)
	nativeCount := 0
	for _, command := range menu {
		if command.IsNative {
			nativeCount++
		}
	}
	assert.Equal(t, 59, nativeCount, "Discord truncation must not silently drop Grok capabilities")
	assert.Contains(t, commandNames(menu), "review")
	assert.Contains(t, commandNames(menu), "andrej_karpathy_skills_karpathy")
	assert.Equal(t, menu, applyCommandMenuPolicy(commands, CommandMenuPolicy{Limit: 100, PreserveNative: true}, strings.ToLower).commands,
		"menu selection must be deterministic")
}

func commandNames(commands []BotCommandInfo) []string {
	names := make([]string, 0, len(commands))
	for _, command := range commands {
		names = append(names, command.Command)
	}
	return names
}

func commandByDescription(t *testing.T, commands []BotCommandInfo, description string) BotCommandInfo {
	t.Helper()
	for _, command := range commands {
		if command.Description == description {
			return command
		}
	}
	t.Fatalf("command with description %q not found in %#v", description, commands)
	return BotCommandInfo{}
}

func TestNativeSlashRawRewriteCustomOverrideAndPolicy(t *testing.T) {
	agent := &nativeTestAgent{
		workDir: t.TempDir(),
		commands: []NativeSlashCommand{
			{Name: "plugin:review", Target: "plugin:review", Description: "qualified", IsSkill: true},
			{Name: "always-approve", Target: "always-approve", Description: "policy", AdminOnly: true, PolicyCommand: "mode"},
		},
	}
	p := &stubPlatformEngine{n: "discord"}
	e := NewEngine("native", agent, []Platform{p}, "", LangEnglish)
	policyAlias := commandByDescription(t, e.GetAllCommands(), "policy").Command

	msg := &Message{SessionKey: "discord:c:u", Platform: "discord", UserID: "u", Content: "quoted reply\n/plugin_review \"hello world\"  a\\b"}
	raw := "/plugin_review \"hello world\"  a\\b"
	assert.False(t, e.handleCommand(p, msg, raw))
	assert.True(t, msg.nativeSlash)
	assert.Equal(t, "/plugin:review \"hello world\"  a\\b", msg.Content)

	e.SetDisabledCommands([]string{"mode"})
	blocked := &Message{SessionKey: "discord:c:u", Platform: "discord", UserID: "u", Content: "/" + policyAlias}
	assert.True(t, e.handleCommand(p, blocked, blocked.Content))
	assert.False(t, blocked.nativeSlash)

	e.SetDisabledCommands(nil)
	nonAdmin := &Message{SessionKey: "discord:c:u", Platform: "discord", UserID: "u", Content: "/" + policyAlias}
	assert.True(t, e.handleCommand(p, nonAdmin, nonAdmin.Content))
	e.SetAdminFrom("*")
	admin := &Message{SessionKey: "discord:c:u", Platform: "discord", UserID: "u", Content: "/" + policyAlias}
	assert.False(t, e.handleCommand(p, admin, admin.Content))
	assert.Equal(t, "/always-approve", admin.Content)
}

func TestNativeSlashReplacementPersistsAcrossTurnsAndAppliesPolicyFirst(t *testing.T) {
	session := newNativeCaptureSession(true)
	agent := &nativeTestAgent{
		workDir: t.TempDir(),
		session: session,
		mode:    "auto",
		commands: []NativeSlashCommand{
			{Name: "always-approve", Target: "always-approve", AdminOnly: true, PolicyCommand: "mode", ReplacementCommand: "mode yolo"},
			{Name: "auto", Target: "auto", AdminOnly: true, PolicyCommand: "mode", ReplacementCommand: "mode auto"},
		},
	}
	p := &stubPlatformEngine{n: "discord"}
	e := NewEngine("native-replacement", agent, []Platform{p}, "", LangEnglish)
	e.SetAdminFrom("admin")
	defer e.cancel()

	approve := &Message{SessionKey: "discord:c:admin", Platform: "discord", UserID: "admin", Content: "/always-approve", ReplyCtx: "approve"}
	require.True(t, e.handleCommand(p, approve, approve.Content))
	assert.Equal(t, "yolo", agent.GetMode())
	assert.False(t, approve.nativeSlash)
	select {
	case prompt := <-session.sent:
		t.Fatalf("replacement leaked to native agent: %q", prompt)
	default:
	}

	normal := &Message{SessionKey: approve.SessionKey, Platform: "discord", UserID: "admin", Content: "hello", ReplyCtx: "normal"}
	e.handleMessage(p, normal)
	assert.Contains(t, waitNativePrompt(t, session), "hello")
	waitNativeTurnFinished(t, e, normal.SessionKey)
	assert.Equal(t, "yolo", agent.GetMode(), "the replacement must affect subsequent agent turns")

	auto := &Message{SessionKey: approve.SessionKey, Platform: "discord", UserID: "admin", Content: "/auto", ReplyCtx: "auto"}
	require.True(t, e.handleCommand(p, auto, auto.Content))
	assert.Equal(t, "auto", agent.GetMode())
	assert.False(t, auto.nativeSlash)
	select {
	case prompt := <-session.sent:
		t.Fatalf("replacement leaked to native agent: %q", prompt)
	default:
	}

	blocked := &Message{SessionKey: "discord:c:user", Platform: "discord", UserID: "user", Content: "/always-approve", ReplyCtx: "blocked"}
	require.True(t, e.handleCommand(p, blocked, blocked.Content))
	assert.Equal(t, "auto", agent.GetMode(), "admin policy must run before the replacement")
	assert.False(t, blocked.nativeSlash)
	assert.Contains(t, p.getSent()[len(p.getSent())-1], "admin privilege")
}

func TestNativeSlashSkipsSenderAndReplyQuoteWithImages(t *testing.T) {
	session := newNativeCaptureSession(true)
	agent := &nativeTestAgent{
		workDir: t.TempDir(),
		session: session,
		commands: []NativeSlashCommand{{
			Name: "bundled:design", Target: "bundled:design", Description: "design", IsSkill: true,
		}},
	}
	p := &stubPlatformEngine{n: "discord"}
	e := NewEngine("native", agent, []Platform{p}, "", LangEnglish)
	e.injectSender = true
	defer e.cancel()

	msg := &Message{
		SessionKey:   "discord:c:u",
		Platform:     "discord",
		UserID:       "u",
		UserName:     "User",
		Content:      "/bundled_design \"make this\"  exact\\path",
		ExtraContent: "reply quote that must not precede native slash",
		Images:       []ImageAttachment{{MimeType: "image/png", Data: []byte("image")}},
	}
	e.handleMessage(p, msg)
	assert.Equal(t, "/bundled:design \"make this\"  exact\\path", waitNativePrompt(t, session))
	session.mu.Lock()
	require.Equal(t, []int{1}, session.images)
	session.mu.Unlock()
}

func TestQueuedNativeSlashKeepsFirstByteAndRawArguments(t *testing.T) {
	session := newNativeCaptureSession(false)
	agent := &nativeTestAgent{
		workDir: t.TempDir(),
		session: session,
		commands: []NativeSlashCommand{{
			Name: "user:review", Target: "user:review", Description: "review", IsSkill: true,
		}},
	}
	p := &stubPlatformEngine{n: "discord"}
	e := NewEngine("native", agent, []Platform{p}, "", LangEnglish)
	e.injectSender = true
	defer e.cancel()

	first := &Message{SessionKey: "discord:c:u", Platform: "discord", UserID: "u", Content: "/user_review first"}
	e.handleMessage(p, first)
	assert.Equal(t, "/user:review first", waitNativePrompt(t, session))

	second := &Message{SessionKey: "discord:c:u", Platform: "discord", UserID: "u", Content: "/user_review \"hello world\"  a\\b", ExtraContent: "quote"}
	e.handleMessage(p, second)
	session.events <- Event{Type: EventResult, Content: "first done", Done: true}
	assert.Equal(t, "/user:review \"hello world\"  a\\b", waitNativePrompt(t, session))
	session.events <- Event{Type: EventResult, Content: "second done", Done: true}
}

func TestNativeSlashScheduledJobsPassThroughAndRejectAdminOnly(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*Engine, string) error
	}{
		{name: "cron", run: func(e *Engine, prompt string) error {
			return e.ExecuteCronJob(&CronJob{ID: "cron-native", SessionKey: "discord:c:u", Prompt: prompt, Mute: true})
		}},
		{name: "timer", run: func(e *Engine, prompt string) error {
			return e.ExecuteTimerJob(&TimerJob{ID: "timer-native", SessionKey: "discord:c:u", Prompt: prompt, Mute: true})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := newNativeCaptureSession(true)
			agent := &nativeTestAgent{
				workDir: t.TempDir(),
				session: session,
				commands: []NativeSlashCommand{
					{Name: "user:review", Target: "user:review", Description: "review", IsSkill: true},
					{Name: "always-approve", Target: "always-approve", Description: "admin policy", AdminOnly: true, PolicyCommand: "mode"},
				},
			}
			platform := &stubCronReplyTargetPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}}
			e := NewEngine("native", agent, []Platform{platform}, "", LangEnglish)
			e.injectSender = true
			defer e.cancel()
			adminAlias := commandByDescription(t, e.GetAllCommands(), "admin policy").Command

			require.NoError(t, test.run(e, "/user_review \"hello world\"  a\\b"))
			assert.Equal(t, "/user:review \"hello world\"  a\\b", waitNativePrompt(t, session))
			err := test.run(e, "/"+adminAlias)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "requires an interactive administrator")
		})
	}
}

func TestNativeSlashWorkspaceScopeUnionAndDirRefresh(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	agentA := &nativeTestAgent{workDir: dirA, commands: []NativeSlashCommand{{Name: "alpha", Target: "alpha", Description: "alpha", IsSkill: true}}}
	agentB := &nativeTestAgent{workDir: dirB, commands: []NativeSlashCommand{{Name: "beta", Target: "beta", Description: "beta", IsSkill: true}}}
	e := NewEngine("native", agentA, nil, "", LangEnglish)
	require.True(t, e.refreshNativeSlashCommands(context.Background(), agentB))
	_, found := e.resolveNativeSlashCommand(agentA, "beta")
	assert.False(t, found, "workspace A must not invoke workspace B's command")
	_, found = e.resolveNativeSlashCommand(agentB, "beta")
	assert.True(t, found)
	assert.Equal(t, []string{"alpha"}, skillNames(e.listSkillsForAgent(agentA)))
	assert.Equal(t, []string{"beta"}, skillNames(e.listSkillsForAgent(agentB)))
	assert.Equal(t, "alpha", commandByDescription(t, e.GetAllCommands(), "alpha").Command)
	assert.Equal(t, "beta", commandByDescription(t, e.GetAllCommands(), "beta").Command)

	platform := &stubLifecyclePlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}}
	agentA.mu.Lock()
	agentA.commands = []NativeSlashCommand{{Name: "beta", Target: "beta", Description: "after-dir"}}
	agentA.mu.Unlock()
	e.platforms = []Platform{platform}
	e.SetAdminFrom("*")
	e.OnPlatformReady(platform)
	msg := &Message{SessionKey: "discord:c:u", Platform: "discord", UserID: "u", ReplyCtx: "ctx"}
	e.cmdDir(platform, msg, []string{dirB})
	_, found = e.resolveNativeSlashCommand(agentA, "alpha")
	assert.False(t, found, "non-workspace /dir must remove the old native state")
	refreshed, found := e.resolveNativeSlashCommand(agentA, "beta")
	assert.True(t, found)
	assert.Equal(t, "after-dir", refreshed.Description)
	assert.GreaterOrEqual(t, platform.registerCalls, 2)
}

func TestNativeSlashPrewarmsExistingBindingsBeforeFirstPlatformMenu(t *testing.T) {
	agentName := "native-prewarm-test-agent"
	RegisterAgent(agentName, func(opts map[string]any) (Agent, error) {
		workDir, _ := opts["work_dir"].(string)
		base := filepath.Base(workDir)
		return &nativeTestAgent{
			name: agentName, workDir: workDir,
			commands: []NativeSlashCommand{{Name: base, Target: base, Description: "prewarm-" + base}},
		}, nil
	})

	rootDir := t.TempDir()
	workspaceA := filepath.Join(t.TempDir(), "workspace-a")
	workspaceB := filepath.Join(t.TempDir(), "workspace-b")
	require.NoError(t, os.MkdirAll(workspaceA, 0o755))
	require.NoError(t, os.MkdirAll(workspaceB, 0o755))
	bindingPath := filepath.Join(t.TempDir(), "bindings.json")
	bindings := NewWorkspaceBindingManager(bindingPath)
	bindings.Bind("project:prewarm", "menu:a", "", workspaceA)
	bindings.Bind(sharedWorkspaceBindingsKey, "menu:b", "", workspaceB)

	rootAgent := &nativeTestAgent{name: agentName, workDir: rootDir}
	platform := newNativeMenuPlatform("bounded-menu")
	e := NewEngine("prewarm", rootAgent, []Platform{platform}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	defer e.cancel()
	e.SetMultiWorkspace(t.TempDir(), bindingPath)
	e.OnPlatformReady(platform)
	firstMenu := waitNativeMenu(t, platform)
	assert.Equal(t, "workspace_a", commandByDescription(t, firstMenu, "prewarm-workspace-a").Command)
	assert.Equal(t, "workspace_b", commandByDescription(t, firstMenu, "prewarm-workspace-b").Command)
	select {
	case <-platform.updates:
		t.Fatal("prewarm must not cause repeated command registration before first platform ready")
	case <-time.After(50 * time.Millisecond):
	}

	// Reaping releases the workspace agents but retains capabilities while a
	// persistent project/shared binding still references each path.
	e.workspacePool.mu.Lock()
	e.workspacePool.idleTimeout = time.Nanosecond
	for _, state := range e.workspacePool.states {
		state.mu.Lock()
		state.lastActivity = time.Now().Add(-time.Hour)
		state.mu.Unlock()
	}
	e.workspacePool.mu.Unlock()
	e.reapIdleWorkspaces()
	assert.Contains(t, commandNames(e.GetAllCommands()), "workspace_a")
	assert.Contains(t, commandNames(e.GetAllCommands()), "workspace_b")

	// Rebinding the project channel away from A makes A unreferenced. Because
	// it has already left the live pool, reconciliation removes only A's stale
	// capability and keeps B (still shared-bound).
	e.workspaceBindings.Bind("project:prewarm", "menu:a", "", workspaceB)
	e.reconcileNativeSlashWorkspaceStates()
	assert.NotContains(t, commandNames(e.GetAllCommands()), "workspace_a")
	assert.Contains(t, commandNames(e.GetAllCommands()), "workspace_b")
}

func TestWorkspaceAgentWithEmptyRootStoreDoesNotWriteIntoWorkingDirectory(t *testing.T) {
	agentName := "native-empty-session-store-test-agent"
	RegisterAgent(agentName, func(opts map[string]any) (Agent, error) {
		workDir, _ := opts["work_dir"].(string)
		return &nativeTestAgent{name: agentName, workDir: workDir}, nil
	})
	root := &nativeTestAgent{name: agentName, workDir: t.TempDir()}
	e := NewEngine("native-empty-store-regression", root, nil, "", LangEnglish)
	defer e.cancel()
	e.SetMultiWorkspace(t.TempDir(), filepath.Join(t.TempDir(), "bindings.json"))
	_, sessions, err := e.getOrCreateWorkspaceAgent(t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, sessions.StorePath())
	sessions.Save()
	matches, err := filepath.Glob("native-empty-store-regression_ws_*.json")
	require.NoError(t, err)
	assert.Empty(t, matches)
}

func TestNativeSlashReloadRefreshesOnlyItsAgentAfterSuccessfulTurn(t *testing.T) {
	session := newNativeCaptureSession(false)
	agent := &nativeTestAgent{
		workDir: t.TempDir(), session: session,
		commands: []NativeSlashCommand{
			{Name: "reload-plugins", Target: "reload-plugins", Description: "reload", AdminOnly: true},
			{Name: "old", Target: "old", Description: "old"},
		},
	}
	other := &nativeTestAgent{workDir: t.TempDir(), commands: []NativeSlashCommand{{Name: "other", Target: "other", Description: "other"}}}
	platform := newNativeMenuPlatform("reload-menu")
	e := NewEngine("reload", agent, []Platform{platform}, "", LangEnglish)
	defer e.cancel()
	e.SetAdminFrom("*")
	require.True(t, e.refreshNativeSlashCommands(context.Background(), other))
	e.OnPlatformReady(platform)
	_ = waitNativeMenu(t, platform)

	msg := &Message{SessionKey: "reload:u", UserID: "u", Content: "/reload-plugins", ReplyCtx: "ctx"}
	e.handleMessage(platform, msg)
	assert.Equal(t, "/reload-plugins", waitNativePrompt(t, session))
	agent.mu.Lock()
	agent.commands = append(agent.commands, NativeSlashCommand{Name: "new-plugin", Target: "new-plugin", Description: "new plugin"})
	agent.mu.Unlock()
	session.events <- Event{Type: EventResult, Content: "reloaded", Done: true}
	updatedMenu := waitNativeMenu(t, platform)
	assert.Contains(t, commandNames(updatedMenu), "new_plugin")
	_, found := e.resolveNativeSlashCommand(other, "other")
	assert.True(t, found, "refreshing one workspace must not replace another workspace's state")
}

func TestProviderSwitchRefreshesNativeStateAndMenuForResolvedWorkspaceAgent(t *testing.T) {
	rootAgent := &nativeTestAgent{
		name: "provider-native", workDir: t.TempDir(),
		commands: []NativeSlashCommand{{Name: "root-command", Target: "root-command", Description: "root"}},
	}
	workspaceAgent := &nativeProviderTestAgent{
		nativeTestAgent: &nativeTestAgent{name: "provider-native", workDir: t.TempDir()},
		providers: []ProviderConfig{
			{Name: "provider-a", Env: map[string]string{"CAP_HOME": "home-a"}},
			{Name: "provider-b", Env: map[string]string{"CAP_HOME": "home-b"}},
		},
		active: "provider-a",
	}
	platform := newNativeMenuPlatform("provider-menu")
	e := NewEngine("provider-refresh", rootAgent, []Platform{platform}, "", LangEnglish)
	defer e.cancel()
	e.SetMultiWorkspace(t.TempDir(), filepath.Join(t.TempDir(), "bindings.json"))
	channelID := "provider-native-workspace"
	e.workspaceBindings.Bind("project:provider-refresh", channelID, "", workspaceAgent.workDir)
	workspace := e.workspacePool.GetOrCreate(workspaceAgent.workDir)
	workspace.agent = workspaceAgent
	workspace.sessions = NewSessionManager("")
	require.True(t, e.refreshNativeSlashCommands(context.Background(), workspaceAgent))

	e.OnPlatformReady(platform)
	initial := waitNativeMenu(t, platform)
	assert.Contains(t, commandNames(initial), "from_home_a")
	assert.Contains(t, commandNames(initial), "root_command")

	msg := &Message{SessionKey: "feishu:" + channelID + ":admin", UserID: "admin", ReplyCtx: "ctx"}
	e.cmdProvider(platform, msg, []string{"switch", "provider-b"})
	updated := waitNativeMenu(t, platform)
	assert.NotContains(t, commandNames(updated), "from_home_a")
	assert.Contains(t, commandNames(updated), "from_home_b")
	assert.Contains(t, commandNames(updated), "root_command", "workspace refresh must not replace the root state")
	_, found := e.resolveNativeSlashCommand(workspaceAgent, "from-home-a")
	assert.False(t, found)
	_, found = e.resolveNativeSlashCommand(workspaceAgent, "from-home-b")
	assert.True(t, found)
}

func TestNativeSlashReaperDoesNotDeleteSamePathReplacement(t *testing.T) {
	workspace := t.TempDir()
	oldAgent := &nativeTestAgent{workDir: workspace, commands: []NativeSlashCommand{{Name: "old", Target: "old"}}}
	newAgent := &nativeTestAgent{workDir: workspace, commands: []NativeSlashCommand{{Name: "new", Target: "new"}}}
	e := NewEngine("reaper", oldAgent, nil, "", LangEnglish)
	e.workspacePool = newWorkspacePool(time.Nanosecond)
	oldState := e.workspacePool.GetOrCreate(workspace)
	oldState.mu.Lock()
	oldState.agent = oldAgent
	oldState.lastActivity = time.Now().Add(-time.Hour)
	oldState.mu.Unlock()
	reaped := e.workspacePool.ReapIdleStates()
	require.Len(t, reaped, 1)

	newState := e.workspacePool.GetOrCreate(workspace)
	newState.mu.Lock()
	newState.agent = newAgent
	newState.mu.Unlock()
	require.True(t, e.refreshNativeSlashCommands(context.Background(), newAgent))
	interactive := &interactiveState{agent: newAgent, workspaceDir: workspace, agentSession: newNativeCaptureSession(false)}
	e.interactiveStates["replacement"] = interactive

	e.cleanupReapedWorkspaces(reaped)
	assert.Same(t, interactive, e.interactiveStates["replacement"])
	_, found := e.resolveNativeSlashCommand(newAgent, "new")
	assert.True(t, found)
}

func TestCanceledInteractiveTurnReleasesIncomingWorkspaceLease(t *testing.T) {
	workspaceDir := t.TempDir()
	agent := &nativeTestAgent{name: "lease-cancel", workDir: workspaceDir}
	e := NewEngine("lease-cancel", agent, nil, "", LangEnglish)
	sessions := NewSessionManager("")
	e.workspacePool = newWorkspacePool(time.Hour)
	workspace := e.workspacePool.GetOrCreate(workspaceDir)
	workspace.mu.Lock()
	workspace.agent = agent
	workspace.sessions = sessions
	workspace.mu.Unlock()
	lease := e.workspacePool.AcquireTurn(workspaceDir, agent)
	require.NotNil(t, lease)
	session := sessions.GetOrCreateActive("lease:cancel")
	require.True(t, session.TryLock())
	e.cancel()

	e.processInteractiveMessageWithLease(
		&stubPlatformEngine{n: "lease"},
		&Message{SessionKey: "lease:cancel", ReplyCtx: "ctx"},
		session, agent, sessions, "lease:cancel", workspaceDir, "lease:cancel", lease,
	)

	assert.False(t, workspace.HasActiveTurn(), "a transferred lease must be released before any cancellation return")
}

func TestNativeSlashReaperPreservesRootKeySharedBySamePathWorkspaceAgent(t *testing.T) {
	workspaceDir := t.TempDir()
	rootAgent := &nativeTestAgent{
		name: "same-path-native", workDir: workspaceDir,
		commands: []NativeSlashCommand{{Name: "root-command", Target: "root-command", Description: "root"}},
	}
	workspaceAgent := &nativeTestAgent{
		name: "same-path-native", workDir: workspaceDir,
		commands: []NativeSlashCommand{{Name: "shared-command", Target: "shared-command", Description: "shared"}},
	}
	e := NewEngine("same-path-native", rootAgent, nil, "", LangEnglish)
	require.True(t, e.refreshNativeSlashCommands(context.Background(), workspaceAgent))
	e.workspacePool = newWorkspacePool(time.Nanosecond)
	workspace := e.workspacePool.GetOrCreate(workspaceDir)
	workspace.mu.Lock()
	workspace.agent = workspaceAgent
	workspace.lastActivity = time.Now().Add(-time.Hour)
	workspace.mu.Unlock()

	e.reapIdleWorkspaces()

	assert.Contains(t, commandNames(e.GetAllCommands()), "shared_command")
	_, found := e.resolveNativeSlashCommand(rootAgent, "shared-command")
	assert.True(t, found, "reaping a same-path workspace must not delete the base agent's shared native key")
}

func TestNativeSlashConfigCommandOverridesInvocation(t *testing.T) {
	session := newNativeCaptureSession(true)
	agent := &nativeTestAgent{
		workDir: t.TempDir(),
		session: session,
		commands: []NativeSlashCommand{{
			Name: "review", Target: "review", Description: "native review", IsSkill: true,
		}},
	}
	p := &stubPlatformEngine{n: "discord"}
	e := NewEngine("native", agent, []Platform{p}, "", LangEnglish)
	e.commands.Add("review", "configured", "configured {{args}}", "", "", "config")
	defer e.cancel()

	msg := &Message{SessionKey: "discord:c:u", Platform: "discord", UserID: "u", Content: "/review exact args", ReplyCtx: "ctx"}
	assert.True(t, e.handleCommand(p, msg, msg.Content))
	prompt := waitNativePrompt(t, session)
	assert.Contains(t, prompt, "configured exact args")
	assert.False(t, msg.nativeSlash)
	assert.False(t, strings.HasPrefix(prompt, "/review"))
}

func TestNativeSlashImagePreservesHistoricalConfigPassthrough(t *testing.T) {
	session := newNativeCaptureSession(true)
	agent := &nativeTestAgent{
		workDir: t.TempDir(),
		session: session,
		commands: []NativeSlashCommand{{
			Name: "review", Target: "review", Description: "native review", IsSkill: true,
		}},
	}
	p := &stubPlatformEngine{n: "discord"}
	e := NewEngine("native", agent, []Platform{p}, "", LangEnglish)
	e.commands.Add("review", "configured", "configured {{args}}", "", "", "config")
	defer e.cancel()

	msg := &Message{
		SessionKey: "discord:c:u", Platform: "discord", UserID: "u",
		Content: "/review image args", ReplyCtx: "ctx",
		Images: []ImageAttachment{{MimeType: "image/png", Data: []byte("image")}},
	}
	e.handleMessage(p, msg)
	prompt := waitNativePrompt(t, session)
	assert.Contains(t, prompt, "/review image args")
	assert.NotContains(t, prompt, "configured image args")
	assert.False(t, msg.nativeSlash)
}

func TestNativeSlashImagesPreserveHistoricalBuiltinPassthrough(t *testing.T) {
	for _, raw := range []string{"/new with image", "/mode plan", "/shell echo safe"} {
		t.Run(strings.Fields(raw)[0], func(t *testing.T) {
			session := newNativeCaptureSession(true)
			agent := &nativeTestAgent{
				workDir: t.TempDir(), session: session,
				commands: []NativeSlashCommand{{Name: strings.TrimPrefix(strings.Fields(raw)[0], "/"), Target: strings.TrimPrefix(strings.Fields(raw)[0], "/")}},
			}
			p := &stubPlatformEngine{n: "image-menu"}
			e := NewEngine("native", agent, []Platform{p}, "", LangEnglish)
			defer e.cancel()
			msg := &Message{
				SessionKey: "image:user", UserID: "u", Content: raw, ReplyCtx: "ctx",
				Images: []ImageAttachment{{MimeType: "image/png", Data: []byte("image")}},
			}
			e.handleMessage(p, msg)
			assert.Contains(t, waitNativePrompt(t, session), raw)
			assert.False(t, msg.nativeSlash)
		})
	}
}
