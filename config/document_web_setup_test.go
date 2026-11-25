package config

import (
	"os"
	"strings"
	"testing"
)

const webSetupDocumentFixture = `# custom config header
language = "en" # untouched
unknown_top = "keep"

[management]
enabled = false # toggle management
port = 0 # assign default
token = "" # assign token
cors_origins = [] # assign origins
unknown_management = "keep"

[bridge]
enabled = false # toggle bridge
port = 0 # assign default
token = "" # assign token
cors_origins = [] # assign origins
unknown_bridge = "keep"

[[projects]]
name = "alpha" # untouched project
[projects.agent]
type = "codex"
`

func TestEnableWebAdminPreservesDocument(t *testing.T) {
	path := writeConfigFixture(t, webSetupDocumentFixture)
	patchConfigPath(t, path)
	result, err := EnableWebAdmin("test-management-token", "test-bridge-token")
	if err != nil {
		t.Fatal(err)
	}
	if result.AlreadyEnabled || result.ManagementPort != 9820 || result.BridgePort != 9810 {
		t.Fatalf("unexpected setup result: %+v", result)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := webSetupDocumentFixture
	want = strings.ReplaceAll(want, `enabled = false # toggle`, `enabled = true # toggle`)
	want = strings.Replace(want, `port = 0 # assign default`, `port = 9820 # assign default`, 1)
	want = strings.Replace(want, `port = 0 # assign default`, `port = 9810 # assign default`, 1)
	want = strings.Replace(want, `token = "" # assign token`, `token = "test-management-token" # assign token`, 1)
	want = strings.Replace(want, `token = "" # assign token`, `token = "test-bridge-token" # assign token`, 1)
	want = strings.ReplaceAll(want, `cors_origins = [] # assign origins`, `cors_origins = ["*"] # assign origins`)
	if string(got) != want {
		t.Fatalf("web setup rewrote unrelated TOML:\n%s", got)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("web setup unexpectedly used fallback: %v", err)
	}
}

func TestEnableWebAdminPreservesAlreadyEnabledSection(t *testing.T) {
	original := strings.Replace(webSetupDocumentFixture, `enabled = false # toggle management`, `enabled = true # toggle management`, 1)
	original = strings.Replace(original, `port = 0 # assign default`, `port = 9000 # assign default`, 1)
	original = strings.Replace(original, `token = "" # assign token`, `token = "existing-token" # assign token`, 1)
	path := writeConfigFixture(t, original)
	patchConfigPath(t, path)
	result, err := EnableWebAdmin("new-management-token", "test-bridge-token")
	if err != nil {
		t.Fatal(err)
	}
	if result.ManagementPort != 9000 || result.ManagementToken != "existing-token" {
		t.Fatalf("existing management settings changed: %+v", result)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `token = "existing-token" # assign token`) || !strings.Contains(string(got), `name = "alpha" # untouched project`) || !strings.Contains(string(got), `token = "test-bridge-token" # assign token`) {
		t.Fatalf("partial setup changed existing content:\n%s", got)
	}
}

func TestEnableWebAdminAddsMissingSectionsWithoutRewritingProjects(t *testing.T) {
	original := `# keep header
language = "en"

[[projects]]
name = "alpha" # keep identity
[projects.agent]
type = "codex"
`
	path := writeConfigFixture(t, original)
	patchConfigPath(t, path)
	if _, err := EnableWebAdmin("test-management-token", "test-bridge-token"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), original) || !strings.Contains(string(got), "[management]\n") || !strings.Contains(string(got), "[bridge]\n") || !strings.Contains(string(got), `name = "alpha" # keep identity`) {
		t.Fatalf("missing-section setup changed original content:\n%s", got)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("missing-section setup unexpectedly used fallback: %v", err)
	}
}

func TestEnableWebAdminAlreadyEnabledLeavesDocumentUntouched(t *testing.T) {
	original := strings.ReplaceAll(webSetupDocumentFixture, "enabled = false", "enabled = true")
	path := writeConfigFixture(t, original)
	patchConfigPath(t, path)
	result, err := EnableWebAdmin("new-management-token", "new-bridge-token")
	if err != nil {
		t.Fatal(err)
	}
	if !result.AlreadyEnabled {
		t.Fatal("expected existing setup to be reported")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("already-enabled setup rewrote config:\n%s", got)
	}
}
