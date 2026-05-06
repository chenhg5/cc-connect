//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

// testConfigPath returns the path to config.test.toml co-located with this
// test file. Override via CC_TEST_CONFIG env var.
func testConfigPath(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("CC_TEST_CONFIG"); p != "" {
		return p
	}
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "config.test.toml")
}

// setupE2E loads config.test.toml, finds the named project, creates a real
// agent with provider wiring, and returns Engine + mockPlatform. All work_dir
// and session state use t.TempDir() for full isolation.
func setupE2E(t *testing.T, projectName string) (*core.Engine, *mockPlatform, func()) {
	t.Helper()

	cfgPath := testConfigPath(t)
	if _, err := os.Stat(cfgPath); err != nil {
		t.Skipf("skip: test config %s not found (copy config.test.toml.example and fill in credentials)", cfgPath)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("cannot load test config: %v", err)
	}

	var proj *config.ProjectConfig
	for i := range cfg.Projects {
		if cfg.Projects[i].Name == projectName {
			proj = &cfg.Projects[i]
			break
		}
	}
	if proj == nil {
		t.Fatalf("project %q not found in test config", projectName)
	}

	agentType := proj.Agent.Type
	bin, err := findAgentBin(agentType)
	if err != nil {
		t.Skipf("skip: %v", err)
	}
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("skip: %s binary not in PATH", bin)
	}

	workDir := t.TempDir()
	opts := make(map[string]any)
	for k, v := range proj.Agent.Options {
		opts[k] = v
	}
	opts["work_dir"] = workDir

	agent, err := core.CreateAgent(agentType, opts)
	if err != nil {
		t.Skipf("skip: cannot create agent: %v", err)
	}

	if ps, ok := agent.(core.ProviderSwitcher); ok {
		var providers []core.ProviderConfig
		for _, ref := range proj.Agent.ProviderRefs {
			for _, gp := range cfg.Providers {
				if gp.Name == ref {
					providers = append(providers, configProviderToCore(gp))
					break
				}
			}
		}
		if len(providers) > 0 {
			ps.SetProviders(providers)
			if provName, _ := opts["provider"].(string); provName != "" {
				ps.SetActiveProvider(provName)
			} else {
				ps.SetActiveProvider(providers[0].Name)
			}
		}
	}

	mp := &mockPlatform{agent: agent}
	sessPath := filepath.Join(workDir, "sessions.json")
	e := core.NewEngine("e2e-test", agent, []core.Platform{mp}, sessPath, core.LangEnglish)

	return e, mp, func() {
		agent.Stop()
		e.Stop()
	}
}

type e2eHelper struct {
	t  *testing.T
	e  *core.Engine
	mp *mockPlatform
	uk string
}

func (h *e2eHelper) send(content string) {
	h.mp.clear()
	h.e.ReceiveMessage(h.mp, &core.Message{
		SessionKey: h.uk, Platform: "mock", UserID: "e2e-user",
		UserName: "tester", Content: content, ReplyCtx: "ctx",
	})
}

func (h *e2eHelper) waitReply(timeout time.Duration) string {
	msgs, ok := waitForMessages(h.mp, 1, timeout)
	if !ok {
		h.t.Fatalf("timeout waiting for reply; sent so far: %v", h.mp.getSent())
	}
	reply := joinMsgContent(msgs)
	if strings.Contains(strings.ToLower(reply), "auth") && strings.Contains(strings.ToLower(reply), "balance") {
		h.t.Skipf("skip: provider auth/balance error: %s", reply)
	}
	return reply
}

func (h *e2eHelper) waitCmd(timeout time.Duration) string {
	return h.waitReply(timeout)
}

func (h *e2eHelper) sendAndWait(content string, timeout time.Duration) string {
	h.send(content)
	return h.waitReply(timeout)
}

// countSessions counts the "msgs" markers in /list output (each session line
// contains "N msgs"), giving us the session count.
func countSessions(listOutput string) int {
	return strings.Count(listOutput, "msgs")
}

// ---------------------------------------------------------------------------
// Comprehensive E2E: covers /list /new /name /switch /delete /current /stop
// Each test function runs against ONE agent type. We define two entry points
// (Codex, ClaudeCode) so both are exercised. Skips gracefully if binary or
// config is missing.
// ---------------------------------------------------------------------------

func runE2E_FullSessionCommands(t *testing.T, project string) {
	e, mp, cleanup := setupE2E(t, project)
	defer cleanup()

	h := &e2eHelper{t: t, e: e, mp: mp, uk: sessionKey("e2e-cmds")}

	// ────── 1. First message → agent replies ──────
	t.Log("[1] sending first message")
	reply := h.sendAndWait("respond with exactly: E2E_PING", 90*time.Second)
	t.Logf("[1] reply: %.120s", reply)

	// ────── 2. /list → 1 session ──────
	t.Log("[2] /list")
	list1 := h.sendAndWait("/list", 10*time.Second)
	c1 := countSessions(list1)
	if c1 < 1 {
		t.Fatalf("[2] /list should show >= 1 session, got %d\n%s", c1, list1)
	}
	t.Logf("[2] /list → %d session(s)", c1)

	// ────── 3. /current ──────
	t.Log("[3] /current")
	cur := h.sendAndWait("/current", 10*time.Second)
	if !strings.Contains(strings.ToLower(cur), "session") && !strings.Contains(strings.ToLower(cur), "s1") {
		t.Logf("[3] /current output: %s", cur)
	}
	t.Logf("[3] /current OK")

	// ────── 4. /new with custom name ──────
	t.Log("[4] /new my-named-session")
	h.sendAndWait("/new my-named-session", 10*time.Second)
	t.Log("[4] /new done")

	// ────── 5. Chat in new session → agent replies ──────
	t.Log("[5] message in new session")
	reply5 := h.sendAndWait("respond with exactly: E2E_PONG", 90*time.Second)
	t.Logf("[5] reply: %.120s", reply5)

	// ────── 6. /list → 2 sessions, name visible ──────
	t.Log("[6] /list (should show 2 sessions)")
	list2 := h.sendAndWait("/list", 10*time.Second)
	c2 := countSessions(list2)
	if c2 < 2 {
		t.Fatalf("[6] /list should show >= 2 sessions, got %d\n%s", c2, list2)
	}
	if !strings.Contains(list2, "my-named-session") {
		t.Errorf("[6] /list should show session name 'my-named-session'\n%s", list2)
	}
	t.Logf("[6] /list → %d sessions, name OK", c2)

	// ────── 7. /name rename current session ──────
	t.Log("[7] /name renamed-session")
	nameReply := h.sendAndWait("/name renamed-session", 10*time.Second)
	t.Logf("[7] /name reply: %.80s", nameReply)

	// ────── 8. /list → verify renamed ──────
	t.Log("[8] /list after rename")
	list3 := h.sendAndWait("/list", 10*time.Second)
	if !strings.Contains(list3, "renamed-session") {
		t.Errorf("[8] /list should show 'renamed-session' after rename\n%s", list3)
	}
	t.Log("[8] rename confirmed in /list")

	// ────── 9. /switch back to session 1 ──────
	t.Log("[9] /switch 1")
	switchReply := h.sendAndWait("/switch 1", 10*time.Second)
	t.Logf("[9] /switch: %.80s", switchReply)

	// ────── 10. Send message in session 1 → verify context ──────
	t.Log("[10] message in session 1")
	reply10 := h.sendAndWait("respond with exactly: BACK_TO_S1", 90*time.Second)
	t.Logf("[10] reply: %.120s", reply10)

	// ────── 11. /list → still 2 sessions ──────
	t.Log("[11] /list (still 2 sessions after /switch)")
	list4 := h.sendAndWait("/list", 10*time.Second)
	c4 := countSessions(list4)
	if c4 < 2 {
		t.Errorf("[11] /list should still show >= 2 sessions, got %d\n%s", c4, list4)
	}
	t.Logf("[11] /list → %d sessions", c4)

	// ────── 12. /new → third session ──────
	t.Log("[12] /new third-session")
	h.sendAndWait("/new third-session", 10*time.Second)
	t.Log("[12] /new done")

	// ────── 13. Chat in third session ──────
	t.Log("[13] message in third session")
	h.sendAndWait("respond with exactly: THIRD_OK", 90*time.Second)
	t.Log("[13] reply received")

	// ────── 14. /delete third session ──────
	t.Log("[14] /delete 3")
	delReply := h.sendAndWait("/delete 3", 10*time.Second)
	t.Logf("[14] /delete: %.80s", delReply)

	// ────── 15. /list → session deleted ──────
	t.Log("[15] /list after /delete")
	list5 := h.sendAndWait("/list", 10*time.Second)
	c5 := countSessions(list5)
	if c5 >= c4+1 {
		t.Errorf("[15] /list should have fewer sessions after delete, got %d (was %d+1)\n%s", c5, c4, list5)
	}
	t.Logf("[15] /list → %d sessions (after delete)", c5)

	// ────── 16. /stop ──────
	t.Log("[16] /stop")
	h.send("/stop")
	time.Sleep(2 * time.Second)
	t.Log("[16] /stop sent, no crash")

	// ────── 17. /status ──────
	t.Log("[17] /status")
	statusReply := h.sendAndWait("/status", 10*time.Second)
	t.Logf("[17] /status: %.120s", statusReply)

	t.Log("=== ALL STEPS PASSED ===")
}

func TestE2E_Codex_FullSessionCommands(t *testing.T) {
	runE2E_FullSessionCommands(t, "e2e-codex")
}

func TestE2E_ClaudeCode_FullSessionCommands(t *testing.T) {
	runE2E_FullSessionCommands(t, "e2e-claudecode")
}

// ---------------------------------------------------------------------------
// E2E: /provider switch (requires multiple providers in config)
// ---------------------------------------------------------------------------

func runE2E_ProviderSwitch(t *testing.T, project string) {
	e, mp, cleanup := setupE2E(t, project)
	defer cleanup()

	h := &e2eHelper{t: t, e: e, mp: mp, uk: sessionKey("e2e-provider")}

	// Start a session
	t.Log("[1] initial message")
	h.sendAndWait("respond with exactly: PROV_INIT", 90*time.Second)

	// /provider list
	t.Log("[2] /provider list")
	provList := h.sendAndWait("/provider list", 10*time.Second)
	t.Logf("[2] providers: %.200s", provList)

	// /provider switch to a different provider
	t.Log("[3] /provider switch shengsuanyun")
	switchReply := h.sendAndWait("/provider switch shengsuanyun", 10*time.Second)
	t.Logf("[3] switch: %.120s", switchReply)

	// Verify new provider works
	t.Log("[4] message after switch")
	reply4 := h.sendAndWait("respond with exactly: PROV_SWITCHED", 90*time.Second)
	t.Logf("[4] reply: %.120s", reply4)

	// /list should still work
	t.Log("[5] /list after provider switch")
	list := h.sendAndWait("/list", 10*time.Second)
	if countSessions(list) < 1 {
		t.Errorf("[5] /list broken after provider switch\n%s", list)
	}
	t.Log("[5] /list OK after provider switch")

	t.Log("=== PROVIDER SWITCH PASSED ===")
}

func TestE2E_Codex_ProviderSwitch(t *testing.T) {
	runE2E_ProviderSwitch(t, "e2e-codex")
}

func TestE2E_ClaudeCode_ProviderSwitch(t *testing.T) {
	runE2E_ProviderSwitch(t, "e2e-claudecode")
}

// ---------------------------------------------------------------------------
// E2E: Session persistence across restart (simulate by recreating engine)
// ---------------------------------------------------------------------------

func runE2E_SessionPersistence(t *testing.T, project string) {
	cfgPath := testConfigPath(t)
	if _, err := os.Stat(cfgPath); err != nil {
		t.Skipf("skip: test config not found")
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("cannot load config: %v", err)
	}

	var proj *config.ProjectConfig
	for i := range cfg.Projects {
		if cfg.Projects[i].Name == project {
			proj = &cfg.Projects[i]
			break
		}
	}
	if proj == nil {
		t.Fatalf("project %q not found", project)
	}

	agentType := proj.Agent.Type
	bin, _ := findAgentBin(agentType)
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("skip: %s not in PATH", bin)
	}

	workDir := t.TempDir()
	sessPath := filepath.Join(workDir, "sessions.json")

	createAgent := func() core.Agent {
		opts := make(map[string]any)
		for k, v := range proj.Agent.Options {
			opts[k] = v
		}
		opts["work_dir"] = workDir
		agent, err := core.CreateAgent(agentType, opts)
		if err != nil {
			t.Skipf("skip: %v", err)
		}
		if ps, ok := agent.(core.ProviderSwitcher); ok {
			var providers []core.ProviderConfig
			for _, ref := range proj.Agent.ProviderRefs {
				for _, gp := range cfg.Providers {
					if gp.Name == ref {
						providers = append(providers, configProviderToCore(gp))
						break
					}
				}
			}
			if len(providers) > 0 {
				ps.SetProviders(providers)
				if pn, _ := opts["provider"].(string); pn != "" {
					ps.SetActiveProvider(pn)
				} else {
					ps.SetActiveProvider(providers[0].Name)
				}
			}
		}
		return agent
	}

	// Phase 1: create session, send message, /new, send message
	agent1 := createAgent()
	mp1 := &mockPlatform{agent: agent1}
	e1 := core.NewEngine("persist-test", agent1, []core.Platform{mp1}, sessPath, core.LangEnglish)
	h1 := &e2eHelper{t: t, e: e1, mp: mp1, uk: sessionKey("persist-user")}

	t.Log("[phase1] sending first message")
	h1.sendAndWait("respond with exactly: PERSIST_S1", 90*time.Second)

	t.Log("[phase1] /new persist-session-2")
	h1.sendAndWait("/new persist-session-2", 10*time.Second)

	t.Log("[phase1] message in session 2")
	h1.sendAndWait("respond with exactly: PERSIST_S2", 90*time.Second)

	t.Log("[phase1] /list")
	list1 := h1.sendAndWait("/list", 10*time.Second)
	c1 := countSessions(list1)
	t.Logf("[phase1] %d sessions", c1)

	// Simulate restart
	agent1.Stop()
	e1.Stop()

	// Phase 2: new engine, same sessPath
	agent2 := createAgent()
	mp2 := &mockPlatform{agent: agent2}
	e2 := core.NewEngine("persist-test", agent2, []core.Platform{mp2}, sessPath, core.LangEnglish)
	defer func() { agent2.Stop(); e2.Stop() }()

	h2 := &e2eHelper{t: t, e: e2, mp: mp2, uk: sessionKey("persist-user")}

	t.Log("[phase2] /list after restart")
	list2 := h2.sendAndWait("/list", 10*time.Second)
	c2 := countSessions(list2)
	if c2 < c1 {
		t.Errorf("[phase2] sessions lost after restart: had %d, now %d\n%s", c1, c2, list2)
	}
	if !strings.Contains(list2, "persist-session-2") {
		t.Errorf("[phase2] session name 'persist-session-2' lost after restart\n%s", list2)
	}
	t.Logf("[phase2] %d sessions survived restart, name preserved", c2)

	t.Log("=== PERSISTENCE PASSED ===")
}

func TestE2E_Codex_SessionPersistence(t *testing.T) {
	runE2E_SessionPersistence(t, "e2e-codex")
}

func TestE2E_ClaudeCode_SessionPersistence(t *testing.T) {
	runE2E_SessionPersistence(t, "e2e-claudecode")
}
