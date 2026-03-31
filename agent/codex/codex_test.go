package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestNew_ParsesOptionEnv(t *testing.T) {
	workDir := t.TempDir()
	binDir := filepath.Join(workDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	scriptPath := filepath.Join(binDir, "codex")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	got, err := New(map[string]any{
		"work_dir": workDir,
		"env": map[string]any{
			"HTTP_PROXY": "http://127.0.0.1:7890",
			"NO_PROXY":   "127.0.0.1,localhost",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	agent, ok := got.(*Agent)
	if !ok {
		t.Fatalf("New returned %T, want *Agent", got)
	}
	if !containsEnvPair(agent.extraEnv, "HTTP_PROXY=http://127.0.0.1:7890") {
		t.Fatalf("extraEnv missing HTTP_PROXY: %v", agent.extraEnv)
	}
	if !containsEnvPair(agent.extraEnv, "NO_PROXY=127.0.0.1,localhost") {
		t.Fatalf("extraEnv missing NO_PROXY: %v", agent.extraEnv)
	}
}

func TestStartSession_MergesOptionProviderAndSessionEnv(t *testing.T) {
	agent := &Agent{
		workDir:  t.TempDir(),
		extraEnv: []string{"HTTP_PROXY=http://127.0.0.1:7890", "NO_PROXY=127.0.0.1"},
		providers: []core.ProviderConfig{
			{
				Name:    "proxy",
				APIKey:  "sk-test",
				BaseURL: "https://relay.example/v1",
				Env: map[string]string{
					"HTTPS_PROXY": "http://127.0.0.1:7890",
				},
			},
		},
		activeIdx:  0,
		sessionEnv: []string{"CC_SESSION_KEY=session-123"},
	}

	got, err := agent.StartSession(context.Background(), "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	cs, ok := got.(*codexSession)
	if !ok {
		t.Fatalf("StartSession returned %T, want *codexSession", got)
	}
	if !containsEnvPair(cs.extraEnv, "HTTP_PROXY=http://127.0.0.1:7890") {
		t.Fatalf("extraEnv missing option env: %v", cs.extraEnv)
	}
	if !containsEnvPair(cs.extraEnv, "OPENAI_API_KEY=sk-test") {
		t.Fatalf("extraEnv missing provider API key: %v", cs.extraEnv)
	}
	if !containsEnvPair(cs.extraEnv, "OPENAI_BASE_URL=https://relay.example/v1") {
		t.Fatalf("extraEnv missing provider base URL: %v", cs.extraEnv)
	}
	if !containsEnvPair(cs.extraEnv, "HTTPS_PROXY=http://127.0.0.1:7890") {
		t.Fatalf("extraEnv missing provider env: %v", cs.extraEnv)
	}
	if !containsEnvPair(cs.extraEnv, "CC_SESSION_KEY=session-123") {
		t.Fatalf("extraEnv missing session env: %v", cs.extraEnv)
	}
}

func TestSend_PropagatesExtraEnvToProcess(t *testing.T) {
	workDir := t.TempDir()
	binDir := filepath.Join(workDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	envFile := filepath.Join(workDir, "env.txt")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$HTTP_PROXY\" > \"$CODEX_ENV_FILE\"\n" +
		"printf '%s\\n' '{\"type\":\"thread.started\",\"thread_id\":\"thread-env\"}'\n" +
		"printf '%s\\n' '{\"type\":\"turn.completed\"}'\n"
	scriptPath := filepath.Join(binDir, "codex")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	t.Setenv("CODEX_ENV_FILE", envFile)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cs, err := newCodexSession(context.Background(), workDir, "", "", "", "", []string{
		"HTTP_PROXY=http://127.0.0.1:7890",
	})
	if err != nil {
		t.Fatalf("newCodexSession: %v", err)
	}
	defer cs.Close()

	if err := cs.Send("ping", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	waitForFileEquals(t, envFile, "http://127.0.0.1:7890")
}

func containsEnvPair(env []string, want string) bool {
	for _, item := range env {
		if strings.EqualFold(item, want) {
			return true
		}
	}
	return false
}
