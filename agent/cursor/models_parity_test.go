package cursor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func sampleCatalog() []core.ModelOption {
	return []core.ModelOption{
		{Name: "auto", Desc: "Auto"},
		{Name: "composer-2.5", Desc: "Composer 2.5"},
		{Name: "composer-2.5-fast", Desc: "Composer 2.5 Fast"},
		{Name: "grok-4.7", Desc: "Grok 4.7"},
		{Name: "grok-4.7-low", Desc: "Grok 4.7 Low"},
		{Name: "grok-4.7-high", Desc: "Grok 4.7 High"},
		{Name: "grok-4.7-low-fast", Desc: "Grok 4.7 Low Fast"},
		{Name: "grok-4.7-high-fast", Desc: "Grok 4.7 High Fast"},
		{Name: "claude-opus-4-7-thinking-low", Desc: "Claude Opus Thinking Low"},
		{Name: "claude-opus-4-7-thinking-high", Desc: "Claude Opus Thinking High"},
		{Name: "claude-opus-4-7-thinking-high-fast", Desc: "Claude Opus Thinking High Fast"},
		{Name: "gpt-5.5-extra-high", Desc: "GPT-5.5 Extra High", Alias: "extra"},
		{Name: "gpt-5.5-high", Desc: "GPT-5.5 High", Alias: "extra"},
	}
}

func agentWithCatalog(model string) *Agent {
	return &Agent{
		cmd:         "nonexistent-cmd-that-will-fail",
		model:       model,
		modelsCache: sampleCatalog(),
		activeIdx:   -1,
	}
}

func TestLaunchModel_EffortMappingAndProviderOverride(t *testing.T) {
	a := agentWithCatalog("grok-4.7")
	a.reasoningEffort = "high"
	if got := a.GetModel(); got != "grok-4.7-high" {
		t.Fatalf("GetModel = %q, want grok-4.7-high", got)
	}
	sess, err := a.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := sess.Close(); err != nil {
			t.Errorf("close session: %v", err)
		}
	}()
	if sess.(*cursorSession).model != a.GetModel() {
		t.Fatalf("StartSession model %q != GetModel %q", sess.(*cursorSession).model, a.GetModel())
	}

	a.reasoningEffort = ""
	a.providers = []core.ProviderConfig{{Name: "p", Model: "grok-4.7-low"}}
	a.activeIdx = 0
	if got := a.GetModel(); got != "grok-4.7-low" {
		t.Fatalf("provider default = %q, want grok-4.7-low (effort=%q pinned=%v idx=%d model=%q)", got, a.reasoningEffort, a.modelPinned, a.activeIdx, a.model)
	}
	a.SetModel("grok-4.7-high")
	if got := a.GetModel(); got != "grok-4.7-high" {
		t.Fatalf("SetModel should beat provider default, got %q", got)
	}
	sess2, err := a.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := sess2.Close(); err != nil {
			t.Errorf("close second session: %v", err)
		}
	}()
	if sess2.(*cursorSession).model != "grok-4.7-high" {
		t.Fatalf("StartSession after SetModel = %q", sess2.(*cursorSession).model)
	}
}

func TestEffort_FastThinkingUnsupportedAndAuto(t *testing.T) {
	a := agentWithCatalog("grok-4.7-high-fast")
	a.modelPinned = true
	a.SetReasoningEffort("low")
	if got := a.GetModel(); got != "grok-4.7-low-fast" {
		t.Fatalf("fast family = %q, want grok-4.7-low-fast", got)
	}
	efforts := a.AvailableReasoningEfforts()
	if strings.Join(efforts, ",") != "low,high" {
		t.Fatalf("fast efforts = %v", efforts)
	}
	a.SetReasoningEffort("max")
	if a.GetReasoningEffort() != "low" {
		t.Fatalf("unsupported effort stored, got %q", a.GetReasoningEffort())
	}
	if got := a.GetModel(); got != "grok-4.7-low-fast" {
		t.Fatalf("unsupported level changed model to %q", got)
	}

	think := agentWithCatalog("claude-opus-4-7-thinking-high-fast")
	think.modelPinned = true
	think.reasoningEffort = "low"
	if got := think.GetModel(); got != "claude-opus-4-7-thinking-high-fast" {
		t.Fatalf("missing thinking-low-fast invented %q", got)
	}

	plain := agentWithCatalog("composer-2.5")
	plain.reasoningEffort = "high"
	if got := plain.GetModel(); got != "composer-2.5" {
		t.Fatalf("unparameterized model = %q", got)
	}
	if efforts := plain.AvailableReasoningEfforts(); len(efforts) != 0 {
		t.Fatalf("composer efforts = %v, want none", efforts)
	}

	auto := agentWithCatalog("auto")
	auto.reasoningEffort = "high"
	if got := auto.GetModel(); got != "auto" {
		t.Fatalf("auto = %q", got)
	}
	if efforts := auto.AvailableReasoningEfforts(); len(efforts) != 0 {
		t.Fatalf("auto efforts = %v", efforts)
	}
}

func TestValidateModel_ExactAliasAndAmbiguity(t *testing.T) {
	a := agentWithCatalog("auto")
	ctx := context.Background()
	got, err := a.ValidateModel(ctx, "grok-4.7-high")
	if err != nil || got != "grok-4.7-high" {
		t.Fatalf("exact = %q err=%v", got, err)
	}
	got, err = a.ValidateModel(ctx, "GROK-4.7-HIGH")
	if err != nil || got != "grok-4.7-high" {
		t.Fatalf("case-insensitive = %q err=%v", got, err)
	}
	got, err = a.ValidateModel(ctx, "Grok 4.7 Low")
	if err != nil || got != "grok-4.7-low" {
		t.Fatalf("display alias = %q err=%v", got, err)
	}
	if _, err := a.ValidateModel(ctx, "grok"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("prefix ambiguity err = %v", err)
	}
	if _, err := a.ValidateModel(ctx, "extra"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("alias ambiguity err = %v", err)
	}
	if _, err := a.ValidateModel(ctx, "no-such-model"); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown err = %v", err)
	}
	got, err = a.ValidateModel(ctx, "composer-2.5-f")
	if err != nil || got != "composer-2.5-fast" {
		t.Fatalf("unique prefix = %q err=%v", got, err)
	}
}

func TestNew_ReasoningEffort(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "agent")
	body := "#!/bin/sh\nprintf '%s\\n' 'auto - Auto' 'grok-4.7 - Grok 4.7' 'grok-4.7-high - Grok 4.7 High'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ag, err := New(map[string]any{
		"cmd":              "agent",
		"model":            "grok-4.7",
		"reasoning_effort": "HIGH",
	})
	if err != nil {
		t.Fatal(err)
	}
	a := ag.(*Agent)
	if a.GetReasoningEffort() != "high" {
		t.Fatalf("constructor effort = %q", a.GetReasoningEffort())
	}
	if got := a.GetModel(); got != "grok-4.7-high" {
		t.Fatalf("constructor launch model = %q", got)
	}
	sess, err := a.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := sess.Close(); err != nil {
			t.Errorf("close session: %v", err)
		}
	}()
	if sess.(*cursorSession).model != "grok-4.7-high" {
		t.Fatalf("constructor StartSession model = %q", sess.(*cursorSession).model)
	}

	bad, err := New(map[string]any{
		"cmd":              "agent",
		"model":            "grok-4.7",
		"reasoning_effort": "max",
	})
	if err != nil {
		t.Fatal(err)
	}
	unsupported := bad.(*Agent)
	if got := unsupported.GetReasoningEffort(); got != "" {
		t.Fatalf("unsupported constructor effort reported %q", got)
	}
	if got := unsupported.GetModel(); got != "grok-4.7" {
		t.Fatalf("unsupported constructor launch = %q", got)
	}
}

func TestEffortDisplayAndModelOverridesEffort(t *testing.T) {
	a := agentWithCatalog("grok-4.7-low")
	if got := a.GetReasoningEffort(); got != "low" {
		t.Fatalf("initial suffix effort = %q", got)
	}
	a.SetReasoningEffort("high")
	a.SetModel("auto")
	if got := a.GetModel(); got != "auto" {
		t.Fatalf("auto model = %q", got)
	}
	if got := a.GetReasoningEffort(); got != "" {
		t.Fatalf("auto effort = %q", got)
	}

	b := agentWithCatalog("grok-4.7")
	b.SetReasoningEffort("low")
	if got := b.GetModel(); got != "grok-4.7-low" {
		t.Fatalf("after effort low, model = %q", got)
	}
	b.SetModel("grok-4.7-high")
	if got := b.GetModel(); got != "grok-4.7-high" {
		t.Fatalf("SetModel high after effort low = %q", got)
	}
	if got := b.GetReasoningEffort(); got != "high" {
		t.Fatalf("effort display after SetModel = %q", got)
	}
	sess, err := b.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := sess.Close(); err != nil {
			t.Errorf("close session: %v", err)
		}
	}()
	if sess.(*cursorSession).model != "grok-4.7-high" {
		t.Fatalf("launch after SetModel = %q", sess.(*cursorSession).model)
	}
}

func TestThinkingSuffixOrderFromCatalog(t *testing.T) {
	a := agentWithCatalog("claude-4.6-opus-high-thinking")
	a.modelsCache = append(a.modelsCache,
		core.ModelOption{Name: "claude-4.6-opus-high-thinking"},
		core.ModelOption{Name: "claude-4.6-opus-max-thinking"},
		core.ModelOption{Name: "claude-opus-4-7-thinking-high"},
		core.ModelOption{Name: "claude-opus-4-7-thinking-low"},
	)
	a.modelPinned = true
	a.SetReasoningEffort("max")
	if got := a.GetModel(); got != "claude-4.6-opus-max-thinking" {
		t.Fatalf("thinking-after effort = %q", got)
	}
	efforts := a.AvailableReasoningEfforts()
	if strings.Join(efforts, ",") != "high,max" {
		t.Fatalf("thinking-after efforts = %v", efforts)
	}

	before := agentWithCatalog("claude-opus-4-7-thinking-high")
	before.modelsCache = a.modelsCache
	before.modelPinned = true
	before.SetReasoningEffort("low")
	if got := before.GetModel(); got != "claude-opus-4-7-thinking-low" {
		t.Fatalf("thinking-before effort = %q", got)
	}
}

func TestParseCombinedCurrentDefaultMarker(t *testing.T) {
	models := parseAgentModelsOutput("auto - Auto (current, default)\ngrok-4.7-high - Grok 4.7 High (current)\n")
	if len(models) != 2 {
		t.Fatalf("parsed %#v", models)
	}
	for _, m := range models {
		if strings.Contains(m.Desc, "current") || strings.Contains(m.Desc, "default") {
			t.Fatalf("marker left on %q", m.Desc)
		}
	}
}

func TestProviderAndEnvInvalidateCacheAndPin(t *testing.T) {
	a := agentWithCatalog("grok-4.7-high")
	a.modelPinned = true
	a.reasoningEffort = "low"
	a.providers = []core.ProviderConfig{
		{Name: "a", Model: "grok-4.7-low"},
		{Name: "b", Model: "composer-2.5"},
	}
	a.activeIdx = 0
	if a.SetActiveProvider("missing") {
		t.Fatal("unknown provider should fail")
	}
	if !a.modelPinned || len(a.modelsCache) == 0 {
		t.Fatal("failed switch changed pin or cache")
	}
	if !a.SetActiveProvider("b") {
		t.Fatal("switch")
	}
	if a.modelPinned || a.modelsCache != nil {
		t.Fatalf("pin=%v cache=%v", a.modelPinned, a.modelsCache)
	}
	if got := a.GetModel(); got != "composer-2.5" {
		t.Fatalf("provider default after switch = %q", got)
	}
	if got := a.GetReasoningEffort(); got != "" {
		t.Fatalf("composer effort = %q", got)
	}

	a.modelsCache = sampleCatalog()
	a.SetWorkDir(t.TempDir())
	if a.modelsCache != nil {
		t.Fatal("workdir left model cache")
	}
	a.modelsCache = sampleCatalog()
	a.SetSessionEnv([]string{"FOO=1"})
	if a.modelsCache != nil {
		t.Fatal("session env left model cache")
	}
	a.modelsCache = sampleCatalog()
	a.SetProviders(a.providers)
	if a.modelsCache != nil {
		t.Fatal("SetProviders left model cache")
	}
}

func TestAvailableModels_CLIEnvPrefixFailureAndCache(t *testing.T) {
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "seen")
	script := filepath.Join(dir, "fake-agent")
	body := "#!/bin/sh\n" +
		"printf '%s\\n' \"pwd=$(pwd)\" \"args=$*\" \"FOO=$FOO\" \"CFG=$CFG\" \"PROV=$PROV\" \"SESS=$SESS\" \"KEY=$CURSOR_API_KEY\" > " + shellQuote(outPath) + "\n" +
		"printf '%s\\n' 'auto - Auto' 'grok-4.7-low - Grok 4.7 Low'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	a := &Agent{
		cmd:          script,
		cliExtraArgs: []string{"--profile", "work"},
		workDir:      work,
		configEnv:    []string{"FOO=config", "CFG=1"},
		sessionEnv:   []string{"FOO=session", "SESS=1"},
		providers: []core.ProviderConfig{{
			Name:   "p",
			APIKey: "secret-key",
			Env:    map[string]string{"FOO": "provider", "PROV": "1"},
		}},
		activeIdx: 0,
		model:     "grok-4.7-high",
	}
	models := a.AvailableModels(context.Background())
	if len(models) == 0 || models[0].Name == "" {
		t.Fatalf("models = %#v", models)
	}
	seen, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(seen)
	resolvedWork, err := filepath.EvalSymlinks(work)
	if err != nil {
		resolvedWork = work
	}
	if !strings.Contains(text, "pwd="+work) && !strings.Contains(text, "pwd="+resolvedWork) {
		t.Errorf("workdir not honored: %s", text)
	}
	if !strings.Contains(text, "args=--profile work models") {
		t.Errorf("cli prefix not honored: %s", text)
	}
	if !strings.Contains(text, "FOO=session") || !strings.Contains(text, "CFG=1") || !strings.Contains(text, "PROV=1") || !strings.Contains(text, "SESS=1") || !strings.Contains(text, "KEY=secret-key") {
		t.Errorf("env merge = %s", text)
	}

	a.cmd = "nonexistent-cmd-that-will-fail"
	fallback := a.AvailableModels(context.Background())
	names := modelNames(fallback)
	if !strings.Contains(names, "auto") || !strings.Contains(names, "grok-4.7-low") || !strings.Contains(names, "grok-4.7-high") {
		t.Fatalf("cached fallback = %s", names)
	}

	prev := cursorModelsTimeout
	cursorModelsTimeout = 200 * time.Millisecond
	t.Cleanup(func() { cursorModelsTimeout = prev })
	slow := filepath.Join(dir, "slow-agent")
	if err := os.WriteFile(slow, []byte("#!/bin/sh\nwhile true; do sleep 1; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	bare := &Agent{cmd: slow, model: "current-model"}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	got := bare.AvailableModels(ctx)
	if time.Since(start) > 3*time.Second {
		t.Fatalf("models fetch was not bounded: %s", time.Since(start))
	}
	names = modelNames(got)
	if !strings.Contains(names, "auto") || !strings.Contains(names, "current-model") {
		t.Fatalf("timeout fallback = %s", names)
	}
	for _, stale := range []string{"claude-sonnet-4-20250514", "gpt-4o", "cursor-small"} {
		if strings.Contains(names, stale) {
			t.Fatalf("stale model %s still advertised", stale)
		}
	}
}

func TestRememberModelsRejectsStaleGeneration(t *testing.T) {
	a := &Agent{modelsGen: 2, modelsCache: []core.ModelOption{{Name: "fresh", Desc: "Fresh"}}}
	if a.rememberModels(1, []core.ModelOption{{Name: "stale-family-high"}}) {
		t.Fatal("stale generation was published")
	}
	if len(a.modelsCache) != 1 || a.modelsCache[0].Name != "fresh" {
		t.Fatalf("cache = %#v", a.modelsCache)
	}
	got := a.publishFetchedModels(1, []core.ModelOption{{Name: "stale-family-high"}})
	if len(got) != 1 || got[0].Name != "fresh" {
		t.Fatalf("rejected fetch returned %#v", got)
	}
}

func TestStaleModelsFetchDoesNotPublishAfterSwitch(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "slow-models")
	body := "#!/bin/sh\nsleep 0.4\nprintf '%s\\n' 'stale-family - Stale' 'stale-family-high - Stale High'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	a := &Agent{
		cmd:             script,
		model:           "stale-family",
		reasoningEffort: "high",
		activeIdx:       -1,
		providers: []core.ProviderConfig{
			{Name: "old", Model: "stale-family"},
			{Name: "new", Model: "composer-2.5"},
		},
	}
	done := make(chan []core.ModelOption, 1)
	go func() {
		done <- a.AvailableModels(context.Background())
	}()
	time.Sleep(80 * time.Millisecond)
	a.SetSessionEnv([]string{"AFTER_SWITCH=1"})
	if !a.SetActiveProvider("new") {
		t.Fatal("provider switch")
	}
	a.mu.Lock()
	a.modelsCache = []core.ModelOption{{Name: "composer-2.5", Desc: "Composer"}}
	a.mu.Unlock()

	var listed []core.ModelOption
	select {
	case listed = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("models fetch did not finish")
	}
	if strings.Contains(modelNames(listed), "stale-family-high") || strings.Contains(modelNames(a.modelsCache), "stale-family") {
		t.Fatalf("stale catalog published listed=%s cache=%s", modelNames(listed), modelNames(a.modelsCache))
	}
	if got := a.GetModel(); got == "stale-family-high" {
		t.Fatal("launch resolved with stale catalog")
	}
}

func modelNames(models []core.ModelOption) string {
	parts := make([]string, len(models))
	for i, m := range models {
		parts[i] = m.Name
	}
	return strings.Join(parts, ",")
}

func shellQuote(path string) string {
	return "'" + strings.ReplaceAll(path, "'", "'\\''") + "'"
}
