package acp

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// Opt-in: uses the locally installed, authenticated Trae CLI, sends no prompts,
// and deletes only the temporary session created by this test.
func TestTraeACPConfigIntegration(t *testing.T) {
	if os.Getenv("CC_ACP_TRAE_INTEGRATION") != "1" {
		t.Skip("set CC_ACP_TRAE_INTEGRATION=1 for local Trae capability check")
	}
	binary, err := exec.LookPath("traecli")
	if err != nil {
		t.Fatal(err)
	}
	opts := map[string]any{"cmd": binary, "args": []string{"--permission-mode", "default", "acp", "serve"}, "work_dir": t.TempDir()}
	agent, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	a := agent.(*Agent)
	var ownedID string
	defer func() {
		a.Stop()
		if ownedID != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if out, err := exec.CommandContext(ctx, binary, "delete", "--force", ownedID).CombinedOutput(); err != nil {
				t.Errorf("delete owned probe session: %v (%s)", err, out)
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := a.PrepareRuntimeConfig(ctx); err != nil {
		t.Fatal(err)
	}
	ownedID = a.configSession.CurrentSessionID()
	models := a.AvailableModels(ctx)
	t.Logf("models=%d reasoning=%v modes=%+v current_mode=%q", len(models), a.AvailableReasoningEfforts(), a.PermissionModes(), a.GetMode())
	if len(models) == 0 {
		t.Fatal("no models discovered")
	}
	if a.GetMode() != "default" {
		t.Fatalf("startup approval mode = %q", a.GetMode())
	}
	for _, mode := range []string{"plan", "default"} {
		if err := a.SetRuntimeConfig(ctx, "mode", mode); err != nil {
			t.Fatal(err)
		}
		if a.GetMode() != mode {
			t.Fatalf("mode switch not reflected: %q", a.GetMode())
		}
	}
	originalModel := a.GetModel()
	fast := ""
	for _, m := range models {
		if m.Name == "Doubao-Seed-Evolving" {
			fast = m.Name
			break
		}
	}
	if fast == "" {
		t.Fatal("expected local Trae no-reasoning model is not advertised")
	}
	if err := a.SetRuntimeConfig(ctx, "model", fast); err != nil {
		t.Fatal(err)
	}
	if len(a.AvailableReasoningEfforts()) != 0 {
		t.Fatal("stale reasoning options after model change")
	}
	if err := a.SetRuntimeConfig(ctx, "model", originalModel); err != nil {
		t.Fatal(err)
	}
	efforts := a.AvailableReasoningEfforts()
	if len(efforts) == 0 {
		t.Fatal("reasoning options did not return")
	}
	effort := efforts[0]
	if err := a.SetRuntimeConfig(ctx, "thought_level", effort); err != nil {
		t.Fatal(err)
	}
	session, err := a.StartSession(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if session.CurrentSessionID() != ownedID {
		session.Close()
		t.Fatal("created an extra session")
	}
	session.Close()
	resumed, err := a.StartSession(ctx, ownedID)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if resumed.CurrentSessionID() != ownedID {
		t.Fatal("did not resume owned session")
	}
	got, _ := resumed.(*acpSession).option("thought_level")
	if got.CurrentValue != effort {
		t.Fatalf("resumed effort = %s", got.CurrentValue)
	}
	if !resumed.(*acpSession).SetLiveMode("plan") {
		t.Fatal("live mode switch failed")
	}
	resumed.Close()
	fresh, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Stop()
	restarted, err := fresh.StartSession(ctx, ownedID)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if got := restarted.(*acpSession).CurrentMode(); got != "default" {
		t.Fatalf("explicit startup preset did not apply on resume: %q", got)
	}
	t.Log("model/reasoning/mode switch, warm reuse and fresh-process resume with default approval passed; no prompt sent")
}
