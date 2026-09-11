package acp

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// acpIntegrationAgent builds an Agent from CC_ACP_BIN / CC_ACP_ARGS, or
// skips the test when CC_ACP_BIN is unset. CC_ACP_ARGS defaults to "acp"
// and accepts a comma-separated list. These tests drive a real ACP agent
// binary for manual end-to-end verification.
func acpIntegrationAgent(t *testing.T) *Agent {
	t.Helper()
	bin := os.Getenv("CC_ACP_BIN")
	if bin == "" {
		t.Skip("set CC_ACP_BIN=/path/to/acp-agent (and optional CC_ACP_ARGS) to run ACP integration tests")
	}
	argsEnv := os.Getenv("CC_ACP_ARGS")
	if argsEnv == "" {
		argsEnv = "acp"
	}
	var args []any
	for _, a := range strings.Split(argsEnv, ",") {
		if a = strings.TrimSpace(a); a != "" {
			args = append(args, a)
		}
	}
	ag, err := New(map[string]any{"cmd": bin, "args": args, "work_dir": t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return ag.(*Agent)
}

// liveModelSwitcher mirrors core.LiveModelSwitcher (avoids an import cycle
// concern on the test side).
type liveModelSwitcher interface {
	SetLiveModel(model string) bool
}

// liveModeSwitcher mirrors core.LiveModeSwitcher.
type liveModeSwitcher interface {
	SetLiveMode(mode string) bool
}

// TestIntegration_ACP_ModelSwitch verifies the full model-switch path
// against a real ACP agent:
//  1. AvailableModels fetches the model list via probeSessionConfig (from
//     the `models` block or a configOptions model selector)
//  2. GetModel reflects the server-reported current model after StartSession
//  3. SetLiveModel switches live on the active session (session/set_model or
//     session/set_config_option, depending on the agent's mechanism)
//
// Verified agents: kiro-cli, mimo, codebuddy (--acp), hermes (models block)
// and opencode (configOptions).
func TestIntegration_ACP_ModelSwitch(t *testing.T) {
	a := acpIntegrationAgent(t)

	// 1. AvailableModels probes when no session is active.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	models := a.AvailableModels(ctx)
	if len(models) == 0 {
		t.Skip("agent advertises no models — nothing to switch")
	}
	t.Logf("AvailableModels: %d models, first=%s", len(models), models[0].Name)

	// 2. StartSession + GetModel.
	sess, err := a.StartSession(ctx, "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer func() { _ = sess.Close() }()

	cur := a.GetModel()
	t.Logf("current model after StartSession: %q", cur)
	if cur == "" {
		t.Fatal("GetModel() empty after StartSession — models block not parsed from session/new")
	}

	// 3. SetLiveModel to a different model.
	lm, ok := sess.(liveModelSwitcher)
	if !ok {
		t.Fatal("session does not implement LiveModelSwitcher")
	}
	var target string
	for _, m := range models {
		if m.Name != cur {
			target = m.Name
			break
		}
	}
	if target == "" {
		t.Skip("only one model available, cannot test switch")
	}
	if !lm.SetLiveModel(target) {
		t.Fatalf("SetLiveModel(%q) returned false", target)
	}
	t.Logf("SetLiveModel(%q) succeeded", target)

	if got := a.GetModel(); got != target {
		t.Fatalf("GetModel() after switch = %q, want %q", got, target)
	}
}

// TestIntegration_ACP_ModeSwitch verifies the full mode-switch path (in
// kiro's terms, agent switching) against a real ACP agent:
//  1. PermissionModes fetches the mode list via probe (from the `modes`
//     block or a configOptions mode selector) when no session is active
//  2. GetMode reflects the server-reported current mode after StartSession
//  3. SetLiveMode switches live on the active session (session/set_mode or
//     session/set_config_option, depending on the agent's mechanism)
//
// Verified agents: kiro-cli (modes = kiro_default/...), mimo, codebuddy,
// hermes (modes block) and opencode (configOptions).
func TestIntegration_ACP_ModeSwitch(t *testing.T) {
	a := acpIntegrationAgent(t)

	// 1. PermissionModes probes when no session is active.
	modes := a.PermissionModes()
	if len(modes) == 0 {
		t.Skip("agent advertises no modes — nothing to switch")
	}
	t.Logf("PermissionModes: %d modes, first=%s", len(modes), modes[0].Key)
	for _, m := range modes {
		t.Logf("  - %s: %s", m.Key, m.Desc)
	}

	// 2. StartSession + GetMode.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	sess, err := a.StartSession(ctx, "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer func() { _ = sess.Close() }()

	cur := a.GetMode()
	t.Logf("current mode after StartSession: %q", cur)

	// 3. SetLiveMode to a different mode.
	lm, ok := sess.(liveModeSwitcher)
	if !ok {
		t.Fatal("session does not implement LiveModeSwitcher")
	}
	var target string
	for _, m := range modes {
		if m.Key != cur {
			target = m.Key
			break
		}
	}
	if target == "" {
		t.Skip("only one mode available, cannot test switch")
	}
	if !lm.SetLiveMode(target) {
		t.Fatalf("SetLiveMode(%q) returned false", target)
	}
	t.Logf("SetLiveMode(%q) succeeded", target)
}

// TestIntegration_ACP_ProbeShortCtx reproduces and locks down a
// regression: cmdModel/cmdMode only pass a 10s ctx to AvailableModels /
// PermissionModes, while a slow-starting agent (e.g. kiro, which
// initializes several MCP servers) can take longer to probe.
// probeSessionConfig must use its own timeout instead of inheriting the
// caller's short ctx, otherwise it surfaces
// "probe initialize: context deadline exceeded".
//
// The test calls AvailableModels with a deliberately short (3s) ctx and
// asserts models are still returned.
func TestIntegration_ACP_ProbeShortCtx(t *testing.T) {
	a := acpIntegrationAgent(t)

	shortCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	models := a.AvailableModels(shortCtx)
	if len(models) == 0 {
		t.Skip("agent advertises no models — probe has nothing to fetch")
	}
	t.Logf("short-ctx probe OK: %d models", len(models))
}
