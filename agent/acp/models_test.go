package acp

import (
	"context"
	"sync"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// Compile-time interface conformance check.
var _ core.ModelSwitcher = (*Agent)(nil)

// NOTE: these unit tests use neutral fixture ids ("model-a", "mode-a", ...)
// rather than real model/mode names on purpose. They exercise cache and
// switching *logic* with deterministic inputs; the set of real models a
// given ACP agent advertises changes over time and is verified separately
// and dynamically in acp_integration_test.go (which reads the live list and
// picks a target at runtime — never hard-coding a model name).

// --- Agent: models cache & ModelSwitcher (models block) ------------

func TestAgent_reportModels_populatesCache(t *testing.T) {
	a := &Agent{}
	a.reportModels(acpModelsBlock{
		CurrentModelID: "model-b",
		AvailableModels: []acpModelInfo{
			{ModelID: "model-a", Name: "model-a", Description: "Auto select"},
			{ModelID: "model-b", Name: "model-b", Description: "Balanced"},
			{ModelID: "model-c", Name: "model-c", Description: "Most capable"},
		},
	})

	if got := a.GetModel(); got != "model-b" {
		t.Fatalf("GetModel() = %q, want model-b", got)
	}
	models := a.AvailableModels(context.Background())
	if len(models) != 3 {
		t.Fatalf("AvailableModels() len = %d, want 3", len(models))
	}
	if models[0].Name != "model-a" || models[0].Desc != "Auto select" {
		t.Fatalf("models[0] = %+v, want model-a", models[0])
	}
	if models[1].Name != "model-b" {
		t.Fatalf("models[1].Name = %q, want model-b", models[1].Name)
	}
}

func TestAgent_GetModel_NoModels(t *testing.T) {
	a := &Agent{}
	a.sessionConfigProbed.Store(true) // avoid triggering a real probe/spawn
	if got := a.GetModel(); got != "" {
		t.Fatalf("GetModel() = %q, want empty", got)
	}
	if models := a.AvailableModels(context.Background()); models != nil {
		t.Fatalf("AvailableModels() = %v, want nil", models)
	}
}

// Regression: after `/model switch X`, the engine calls SetModel(X) then
// reads back GetModel() to display and apply. The pending SetModel MUST
// win over the server-reported currentModelId (mirrors GetMode).
func TestAgent_SetModel_PendingWinsOverCurrent(t *testing.T) {
	a := &Agent{}
	a.reportModels(acpModelsBlock{
		CurrentModelID: "model-a",
		AvailableModels: []acpModelInfo{
			{ModelID: "model-a", Name: "model-a"},
			{ModelID: "model-b", Name: "model-b"},
		},
	})
	a.SetModel("model-b")
	if got := a.GetModel(); got != "model-b" {
		t.Fatalf("GetModel() after SetModel = %q, want model-b", got)
	}
}

func TestAgent_GetModel_FallbackToServerCurrent(t *testing.T) {
	a := &Agent{}
	// No SetModel: GetModel falls back to the server-reported currentModel.
	a.reportModels(acpModelsBlock{
		CurrentModelID:  "model-x",
		AvailableModels: []acpModelInfo{{ModelID: "model-x", Name: "model-x"}},
	})
	if got := a.GetModel(); got != "model-x" {
		t.Fatalf("GetModel() = %q, want model-x", got)
	}
}

// --- Agent: PermissionModes probe behaviour ------------------------

// The cache being populated means PermissionModes returns directly
// without triggering a probe.
func TestAgent_PermissionModes_returnsCachedModes(t *testing.T) {
	a := &Agent{}
	a.reportModes(acpModesBlock{
		CurrentModeID: "mode-a",
		AvailableModes: []acpModeInfo{
			{ID: "mode-a", Name: "mode-a", Description: "First mode"},
			{ID: "mode-b", Name: "mode-b", Description: "Second mode"},
		},
	})

	modes := a.PermissionModes()
	if len(modes) != 2 {
		t.Fatalf("PermissionModes() len = %d, want 2", len(modes))
	}
	if modes[0].Key != "mode-a" || modes[1].Key != "mode-b" {
		t.Fatalf("PermissionModes() = %+v", modes)
	}
}

// Once probed, an empty cache must not trigger another probe (returns
// empty rather than spawning).
func TestAgent_PermissionModes_skipsProbeWhenProbed(t *testing.T) {
	a := &Agent{}
	a.sessionConfigProbed.Store(true) // probed but the agent has no modes

	if modes := a.PermissionModes(); len(modes) != 0 {
		t.Fatalf("PermissionModes() = %+v, want empty (probe skipped)", modes)
	}
}

// modes and models share one probe flag: once probed, neither entry
// point spawns again even with an empty cache.
func TestAgent_SharedProbeFlag(t *testing.T) {
	a := &Agent{}
	a.sessionConfigProbed.Store(true)

	if got := a.PermissionModes(); len(got) != 0 {
		t.Fatalf("PermissionModes = %+v, want empty", got)
	}
	if got := a.AvailableModels(context.Background()); got != nil {
		t.Fatalf("AvailableModels = %+v, want nil", got)
	}
}

// A zero-value Agent (no cmd) must not attempt to spawn a probe process
// when the caches are empty.
func TestAgent_ensureProbe_noCmdNoSpawn(t *testing.T) {
	a := &Agent{} // cmd == ""
	if got := a.AvailableModels(context.Background()); got != nil {
		t.Fatalf("AvailableModels = %+v, want nil", got)
	}
	if got := a.PermissionModes(); len(got) != 0 {
		t.Fatalf("PermissionModes = %+v, want empty", got)
	}
	// Probe must not have been marked as run (no cmd to probe).
	if a.sessionConfigProbed.Load() {
		t.Fatal("sessionConfigProbed should stay false when cmd is empty")
	}
}

// --- session: absorbModels / SetLiveModel / current_model_update ---

func TestSession_absorbModels_reportsViaCallback(t *testing.T) {
	cb := &fakeCallbacks{}
	s := &acpSession{callbacks: cb}

	s.absorbModels(&acpModelsBlock{
		CurrentModelID:  "model-a",
		AvailableModels: []acpModelInfo{{ModelID: "model-a", Name: "A"}, {ModelID: "model-b", Name: "B"}},
	})

	got, ok := cb.lastModels()
	if !ok || got.CurrentModelID != "model-a" || len(got.AvailableModels) != 2 {
		t.Fatalf("callback got %+v (ok=%v), want currentModelId=model-a with 2 models", got, ok)
	}
	s.modelsMu.RLock()
	defer s.modelsMu.RUnlock()
	if s.currentModel != "model-a" || len(s.availableModels) != 2 {
		t.Fatalf("session cache: current=%q models=%d", s.currentModel, len(s.availableModels))
	}
}

func TestSession_absorbModels_emptyNoOp(t *testing.T) {
	cb := &fakeCallbacks{}
	s := &acpSession{callbacks: cb}
	s.absorbModels(nil)
	s.absorbModels(&acpModelsBlock{})
	if _, ok := cb.lastModels(); ok {
		t.Fatal("callback should not fire for an empty models block")
	}
}

func TestSession_matchAvailableModel(t *testing.T) {
	s := &acpSession{
		availableModels: []acpModelInfo{
			{ModelID: "model-a", Name: "model-a"},
			{ModelID: "model-b", Name: "model-b"},
		},
	}
	if got := s.matchAvailableModel("model-b"); got != "model-b" {
		t.Fatalf("match exact = %q", got)
	}
	if got := s.matchAvailableModel("MODEL-A"); got != "model-a" {
		t.Fatalf("match case-insensitive = %q", got)
	}
	if got := s.matchAvailableModel("nonexistent"); got != "" {
		t.Fatalf("match unknown = %q, want empty", got)
	}
}

func TestSession_maybeAbsorbCurrentModelUpdate(t *testing.T) {
	cb := &fakeCallbacks{}
	s := &acpSession{
		callbacks:       cb,
		availableModels: []acpModelInfo{{ModelID: "model-a"}, {ModelID: "model-b"}},
	}
	params := []byte(`{"update":{"sessionUpdate":"current_model_update","currentModelId":"model-b"}}`)
	s.maybeAbsorbCurrentModelUpdate(params)

	got, ok := cb.lastModels()
	if !ok || got.CurrentModelID != "model-b" {
		t.Fatalf("reported currentModelId = %q (ok=%v), want model-b", got.CurrentModelID, ok)
	}
	s.modelsMu.RLock()
	defer s.modelsMu.RUnlock()
	if s.currentModel != "model-b" {
		t.Fatalf("session currentModel = %q, want model-b", s.currentModel)
	}
}

func TestSession_maybeAbsorbCurrentModelUpdate_ignoresOther(t *testing.T) {
	cb := &fakeCallbacks{}
	s := &acpSession{callbacks: cb}
	// A mode update must not trigger the model callback.
	params := []byte(`{"update":{"sessionUpdate":"current_mode_update","currentModeId":"mode-a"}}`)
	s.maybeAbsorbCurrentModelUpdate(params)
	if _, ok := cb.lastModels(); ok {
		t.Fatal("model callback should not fire for a mode update")
	}
}

// --- session: configOptions mechanism (OpenCode-style) ------------

func TestSession_absorbConfigOptions_model(t *testing.T) {
	cb := &fakeCallbacks{}
	s := &acpSession{callbacks: cb}

	s.absorbConfigOptions([]acpConfigOption{
		{
			ID:           "model",
			Name:         "Model",
			Category:     "model",
			Type:         "select",
			CurrentValue: "model-b",
			Options: []acpConfigSelectOptions{
				{Value: "model-a", Name: "Model A"},
				{Value: "model-b", Name: "Model B"},
			},
		},
	})

	// Normalised into the models cache, with configId recorded for switching.
	s.modelsMu.RLock()
	defer s.modelsMu.RUnlock()
	if s.currentModel != "model-b" {
		t.Fatalf("currentModel = %q, want model-b", s.currentModel)
	}
	if len(s.availableModels) != 2 || s.availableModels[0].ModelID != "model-a" {
		t.Fatalf("availableModels = %+v", s.availableModels)
	}
	if s.modelConfigID != "model" {
		t.Fatalf("modelConfigID = %q, want model", s.modelConfigID)
	}
	if got, ok := cb.lastModels(); !ok || got.CurrentModelID != "model-b" {
		t.Fatalf("reported models = %+v (ok=%v)", got, ok)
	}
}

func TestSession_absorbConfigOptions_mode(t *testing.T) {
	cb := &fakeCallbacks{}
	s := &acpSession{callbacks: cb}

	s.absorbConfigOptions([]acpConfigOption{
		{
			ID:           "mode",
			Category:     "mode",
			CurrentValue: "mode-a",
			Options: []acpConfigSelectOptions{
				{Value: "mode-a", Name: "Ask"},
				{Value: "mode-b", Name: "Code"},
			},
		},
	})

	s.modesMu.RLock()
	defer s.modesMu.RUnlock()
	if s.currentMode != "mode-a" || s.modeConfigID != "mode" {
		t.Fatalf("currentMode=%q modeConfigID=%q", s.currentMode, s.modeConfigID)
	}
	if len(s.availableModes) != 2 || s.availableModes[1].ID != "mode-b" {
		t.Fatalf("availableModes = %+v", s.availableModes)
	}
}

func TestSession_maybeAbsorbConfigOptionUpdate(t *testing.T) {
	cb := &fakeCallbacks{}
	s := &acpSession{callbacks: cb}
	params := []byte(`{"update":{"sessionUpdate":"config_option_update","configOptions":[` +
		`{"id":"model","category":"model","currentValue":"model-b","options":[{"value":"model-a"},{"value":"model-b"}]}]}}`)
	s.maybeAbsorbConfigOptionUpdate(params)

	got, ok := cb.lastModels()
	if !ok || got.CurrentModelID != "model-b" {
		t.Fatalf("reported currentModelId = %q (ok=%v), want model-b", got.CurrentModelID, ok)
	}
}

func TestFlattenModelOptions_flatAndGrouped(t *testing.T) {
	// Flat options.
	flat := flattenModelOptions(acpConfigOption{
		Options: []acpConfigSelectOptions{{Value: "a", Name: "A"}, {Value: "b", Name: "B"}},
	})
	if len(flat) != 2 || flat[0].ModelID != "a" || flat[1].Name != "B" {
		t.Fatalf("flat = %+v", flat)
	}
	// Grouped options.
	grouped := flattenModelOptions(acpConfigOption{
		Options: []acpConfigSelectOptions{
			{Group: "G1", Options: []acpConfigSelectOption{{Value: "x", Name: "X"}}},
			{Group: "G2", Options: []acpConfigSelectOption{{Value: "y"}, {Value: "z"}}},
		},
	})
	if len(grouped) != 3 || grouped[0].ModelID != "x" || grouped[2].ModelID != "z" {
		t.Fatalf("grouped = %+v", grouped)
	}
}

// When an agent advertises BOTH the legacy models/modes blocks AND
// configOptions selectors, the newer configOptions must win — regardless
// of the absorb order — and the state must stay consistent (the switch
// path via modelConfigID/modeConfigID must match the reported list).
func TestSession_bothMechanisms_configOptionsWins(t *testing.T) {
	assert := func(t *testing.T, s *acpSession) {
		t.Helper()
		s.modelsMu.RLock()
		defer s.modelsMu.RUnlock()
		if s.modelConfigID != "model" {
			t.Fatalf("modelConfigID = %q, want model (configOptions wins)", s.modelConfigID)
		}
		if s.currentModel != "new-a" {
			t.Fatalf("currentModel = %q, want new-a (consistent with configOptions)", s.currentModel)
		}
		if len(s.availableModels) != 2 || s.availableModels[0].ModelID != "new-a" {
			t.Fatalf("availableModels = %+v, want the configOptions list", s.availableModels)
		}
	}
	block := &acpModelsBlock{
		CurrentModelID:  "old-a",
		AvailableModels: []acpModelInfo{{ModelID: "old-a"}, {ModelID: "old-b"}},
	}
	cfg := []acpConfigOption{{
		ID: "model", Category: "model", CurrentValue: "new-a",
		Options: []acpConfigSelectOptions{{Value: "new-a"}, {Value: "new-b"}},
	}}

	// configOptions first, then the block (handshake order).
	s1 := &acpSession{}
	s1.absorbConfigOptions(cfg)
	s1.absorbModels(block)
	assert(t, s1)

	// Reversed order must yield the same result — precedence is not
	// order-dependent (the block is skipped once modelConfigID is set).
	s2 := &acpSession{}
	s2.absorbModels(block)
	s2.absorbConfigOptions(cfg)
	assert(t, s2)
}

// Mixed mechanisms: an agent provides a model selector via configOptions
// but a mode selector only via the legacy `modes` block. The two selectors
// must resolve independently — model from configOptions (modelConfigID set),
// mode from the block (modeConfigID empty). Order-independent.
func TestSession_mixedMechanisms_modelCfgModeBlock(t *testing.T) {
	assert := func(t *testing.T, s *acpSession) {
		t.Helper()
		s.modelsMu.RLock()
		if s.modelConfigID != "model" || s.currentModel != "cfg-model" ||
			len(s.availableModels) != 1 || s.availableModels[0].ModelID != "cfg-model" {
			s.modelsMu.RUnlock()
			t.Fatalf("model: configID=%q current=%q avail=%+v — want configOptions",
				s.modelConfigID, s.currentModel, s.availableModels)
		}
		s.modelsMu.RUnlock()

		s.modesMu.RLock()
		defer s.modesMu.RUnlock()
		if s.modeConfigID != "" || s.currentMode != "block-mode" ||
			len(s.availableModes) != 1 || s.availableModes[0].ID != "block-mode" {
			t.Fatalf("mode: configID=%q current=%q avail=%+v — want legacy block",
				s.modeConfigID, s.currentMode, s.availableModes)
		}
	}
	modeBlock := &acpModesBlock{
		CurrentModeID:  "block-mode",
		AvailableModes: []acpModeInfo{{ID: "block-mode", Name: "Block Mode"}},
	}
	// configOptions carries only a model selector (no mode category).
	cfg := []acpConfigOption{{
		ID: "model", Category: "model", CurrentValue: "cfg-model",
		Options: []acpConfigSelectOptions{{Value: "cfg-model"}},
	}}

	// configOptions first, then the mode block (handshake order).
	s1 := &acpSession{}
	s1.absorbConfigOptions(cfg)
	s1.absorbModes(modeBlock)
	assert(t, s1)

	// Reversed order must yield the same result.
	s2 := &acpSession{}
	s2.absorbModes(modeBlock)
	s2.absorbConfigOptions(cfg)
	assert(t, s2)
}

// --- concurrency safety (run with -race) ---------------------------

func TestAgent_ConcurrentModelAccess(t *testing.T) {
	a := &Agent{}
	a.sessionConfigProbed.Store(true)
	a.reportModels(acpModelsBlock{
		CurrentModelID:  "model-a",
		AvailableModels: []acpModelInfo{{ModelID: "model-a"}, {ModelID: "model-b"}},
	})

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				a.reportModels(acpModelsBlock{
					CurrentModelID:  "model-a",
					AvailableModels: []acpModelInfo{{ModelID: "model-a"}, {ModelID: "model-b"}},
				})
			}
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			_ = a.GetModel()
			_ = a.AvailableModels(context.Background())
			a.SetModel("model-b")
		}
		close(stop)
	}()
	wg.Wait()
}
