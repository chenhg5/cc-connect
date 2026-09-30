package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// This subprocess implements only metadata RPCs. Any attempt to create a
// thread, start a turn, or use a different workspace fails the test.
func TestSkillsMetadataProcess(t *testing.T) {
	if os.Getenv("CC_SKILLS_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for _, method := range []string{"initialize", "initialized", "skills/list"} {
		if !scanner.Scan() {
			os.Exit(2)
		}
		var req struct {
			ID     int            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil || req.Method != method {
			os.Exit(3)
		}
		if method == "initialized" {
			continue
		}
		if method == "initialize" {
			fmt.Printf("{\"id\":%d,\"result\":{}}\n", req.ID)
			continue
		}
		cwd, _ := os.Getwd()
		cwds, _ := req.Params["cwds"].([]any)
		if len(cwds) != 1 || cwds[0] != cwd || req.Params["forceReload"] != true || os.Getenv("CODEX_HOME") != os.Getenv("CC_EXPECT_CODEX_HOME") {
			os.Exit(4)
		}
		if os.Getenv("CC_SKILLS_HANG") == "1" {
			time.Sleep(time.Minute)
		}
		fmt.Printf("{\"id\":%d,\"result\":%s}\n", req.ID, os.Getenv("CC_SKILLS_RESPONSE"))
	}
	// Remain alive so the caller must clean up the metadata process.
	scanner.Scan()
	os.Exit(0)
}

func TestListSkills_ExcludesClaudeDisabledAndCachedSkills(t *testing.T) {
	tmp := t.TempDir()
	workDir := filepath.Join(tmp, "workspace")
	codexHome := filepath.Join(tmp, "codex")
	setTestHome(t, tmp)
	t.Setenv("CODEX_HOME", filepath.Join(tmp, "wrong-home"))
	write := func(root, name string) string {
		t.Helper()
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "SKILL.md")
		if err := os.WriteFile(path, []byte("---\nname: "+name+"\ndescription: Fixture\n---\nInstructions for "+name), 0644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	project := write(filepath.Join(workDir, ".agents", "skills"), "onboarding")
	system := write(filepath.Join(codexHome, "skills", ".system"), "system")
	pluginRoot := filepath.Join(codexHome, "plugins", "cache", "market", "plugin", "v1", "skills")
	plugin := write(pluginRoot, "enabled")
	disabled := write(pluginRoot, "disabled-sibling")
	write(filepath.Join(workDir, ".claude", "skills"), "claude-only")
	write(filepath.Join(codexHome, "superpowers", "skills"), "unlinked-superpower")
	write(filepath.Join(codexHome, "plugins", "cache", "market", "unused", "v0", "skills"), "cached-only")

	response := map[string]any{"data": []any{map[string]any{
		"cwd": workDir, "errors": []any{}, "skills": []any{
			map[string]any{"name": "onboarding", "path": project, "enabled": true, "description": "Project skill"},
			map[string]any{"name": "system", "path": system, "enabled": true},
			map[string]any{"name": "plugin:enabled", "path": plugin, "enabled": true},
			map[string]any{"name": "plugin:disabled", "path": disabled, "enabled": false},
		},
	}}}
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{cmd: bin, cliExtraArgs: []string{"-test.run=^TestSkillsMetadataProcess$", "--"}, workDir: workDir, codexHome: codexHome,
		configEnv: []string{"CC_SKILLS_HELPER=1", "CC_EXPECT_CODEX_HOME=" + codexHome, "CC_SKILLS_RESPONSE=" + string(data)}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	skills, err := a.ListSkills(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range skills {
		names = append(names, s.Name)
		if !strings.HasPrefix(s.Prompt, "Instructions for ") {
			t.Fatalf("skill instructions not loaded: %+v", s)
		}
	}
	if want := []string{"onboarding", "system", "plugin:enabled"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("skills=%v, want %v", names, want)
	}
	// Both text /skills and card navigation use this interface in core.
	var _ core.SkillCatalogProvider = a

	a.configEnv = append(a.configEnv, "CC_SKILLS_HANG=1")
	ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := a.ListSkills(ctx); err == nil {
		t.Fatal("hung metadata process must fail")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("metadata process did not respect cancellation")
	}
}

func TestListSkills_CatalogErrorsAndEmptyAreAuthoritative(t *testing.T) {
	workDir := t.TempDir()
	for _, tc := range []struct {
		name, payload string
		wantError     bool
	}{
		{"empty", `{"data":[{"cwd":%q,"skills":[]}]}`, false},
		{"missing workspace", `{"data":[],"ignored":%q}`, true},
		{"discovery error", `{"data":[{"cwd":%q,"errors":[{"message":"invalid skill"}]}]}`, true},
		{"relative file", `{"data":[{"cwd":%q,"skills":[{"enabled":true,"name":"bad","path":"relative/SKILL.md"}]}]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var r codexSkillsResponse
			if err := json.Unmarshal([]byte(fmt.Sprintf(tc.payload, workDir)), &r); err != nil {
				t.Fatal(err)
			}
			skills, err := r.load(workDir)
			if (err != nil) != tc.wantError || len(skills) != 0 {
				t.Fatalf("skills=%v err=%v", skills, err)
			}
		})
	}
}
