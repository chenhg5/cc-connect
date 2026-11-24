package config

import (
	"os"
	"strings"
	"testing"
)

const providerDocumentFixture = `# provider document intro
language = "en" # keep language
unknown_top = "keep"

[[providers]]
name = "shared" # keep identity
api_key = "test-key"
model = "old" # keep model comment

[providers.env]
KEEP = "old" # keep env comment

[[projects]]
name = "alpha"
unknown_project = "keep"

[projects.agent]
type = "codex"
provider_refs = ["shared"] # keep refs comment

[[projects.agent.providers]]
name = "inline"
api_key = "inline-key"

[[projects]]
name = "beta" # untouched project

[projects.agent]
type = "codex"
`

func TestUpdateGlobalProviderPreservesDocument(t *testing.T) {
	path := writeConfigFixture(t, providerDocumentFixture)
	patchConfigPath(t, path)
	provider := ProviderConfig{
		Name:   "shared",
		APIKey: "test-key",
		Model:  "new",
		Env:    map[string]string{"KEEP": "new"},
	}
	if err := UpdateGlobalProvider("shared", provider); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(providerDocumentFixture, `model = "old" # keep model comment`, `model = "new" # keep model comment`, 1)
	want = strings.Replace(want, `KEEP = "old" # keep env comment`, `KEEP = "new" # keep env comment`, 1)
	if string(got) != want {
		t.Fatalf("provider update changed unrelated TOML:\n%s", got)
	}
}

func TestAddGlobalProviderPreservesDocument(t *testing.T) {
	path := writeConfigFixture(t, providerDocumentFixture)
	patchConfigPath(t, path)
	if err := AddGlobalProvider(ProviderConfig{Name: "second", APIKey: "test-two", Model: "model-two"}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), "# provider document intro\n") || !strings.Contains(string(got), `model = "old" # keep model comment`) || !strings.Contains(string(got), `name = "beta" # untouched project`) || !strings.Contains(string(got), `name = "second"`) {
		t.Fatalf("provider add lost original content: %s", got)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("provider add unexpectedly used fallback: %v", err)
	}
}

func TestRemoveGlobalProviderPreservesOtherContent(t *testing.T) {
	path := writeConfigFixture(t, providerDocumentFixture)
	patchConfigPath(t, path)
	if err := RemoveGlobalProvider("shared"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), "# provider document intro\n") || !strings.Contains(string(got), `name = "beta" # untouched project`) || strings.Contains(string(got), `name = "shared"`) || !strings.Contains(string(got), `provider_refs = [] # keep refs comment`) {
		t.Fatalf("provider removal changed unrelated content or refs: %s", got)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("provider removal unexpectedly used fallback: %v", err)
	}
}

func TestInlineProviderAndRefsPreserveDocument(t *testing.T) {
	t.Run("add", func(t *testing.T) {
		path := writeConfigFixture(t, providerDocumentFixture)
		patchConfigPath(t, path)
		if err := AddProviderToConfig("alpha", ProviderConfig{Name: "new-inline", APIKey: "test-new"}); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(got), "# provider document intro\n") || !strings.Contains(string(got), `name = "beta" # untouched project`) || !strings.Contains(string(got), `name = "new-inline"`) {
			t.Fatalf("inline provider add changed unrelated content: %s", got)
		}
	})
	t.Run("remove", func(t *testing.T) {
		path := writeConfigFixture(t, providerDocumentFixture)
		patchConfigPath(t, path)
		if err := RemoveProviderFromConfig("alpha", "inline"); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(got), "# provider document intro\n") || !strings.Contains(string(got), `name = "beta" # untouched project`) || strings.Contains(string(got), `name = "inline"`) {
			t.Fatalf("inline provider removal changed unrelated content: %s", got)
		}
	})
	t.Run("refs", func(t *testing.T) {
		path := writeConfigFixture(t, providerDocumentFixture)
		patchConfigPath(t, path)
		if err := SaveProviderRefs("alpha", []string{"shared", "another"}); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want := strings.Replace(providerDocumentFixture, `provider_refs = ["shared"] # keep refs comment`, `provider_refs = ["shared", "another"] # keep refs comment`, 1)
		if string(got) != want {
			t.Fatalf("provider refs update changed unrelated TOML:\n%s", got)
		}
	})
}

const richProviderFixture = `# rich provider intro
language = "en"
[[providers]]
name = "shared"
api_key = "test-key"
model = "old" # keep model note
agent_types = ["codex"] # keep types note

[providers.env]
KEEP = "old" # keep env note

[providers.endpoints]
codex = "https://old.example" # keep endpoint note

[providers.agent_models]
codex = "old-model"

[[providers.models]]
model = "m1" # keep first model note
alias = "one"

[[providers.agent_model_lists.codex]]
model = "codex-m1" # keep agent model note
alias = "first"

[providers.codex]
wire_api = "responses" # keep wire note

[providers.codex.http_headers]
"X-Test" = "old" # keep header note

[[projects]]
name = "alpha" # untouched
[projects.agent]
type = "codex"
`

func TestUpdateRichGlobalProviderKeepsUnchangedModelComments(t *testing.T) {
	path := writeConfigFixture(t, richProviderFixture)
	patchConfigPath(t, path)
	p := readConfigFixture(t, path).Providers[0]
	p.Model = "new"
	p.Env["KEEP"] = "new"
	p.Endpoints["codex"] = "https://new.example"
	p.AgentModels["codex"] = "new-model"
	p.Codex.HTTPHeaders["X-Test"] = "new"
	if err := UpdateGlobalProvider("shared", p); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{
		`model = "m1" # keep first model note`,
		`model = "codex-m1" # keep agent model note`,
		`name = "alpha" # untouched`,
		`wire_api = "responses" # keep wire note`,
		`"X-Test" = "new" # keep header note`,
	} {
		if !strings.Contains(string(got), keep) {
			t.Fatalf("missing %q after rich provider update:\n%s", keep, got)
		}
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("rich provider update unexpectedly used fallback: %v", err)
	}
}

func TestAppendProviderModelsKeepsExistingComments(t *testing.T) {
	path := writeConfigFixture(t, richProviderFixture)
	patchConfigPath(t, path)
	p := readConfigFixture(t, path).Providers[0]
	p.Models = append(p.Models, ProviderModelConfig{Model: "m2", Alias: "two"})
	p.AgentModelLists["codex"] = append(p.AgentModelLists["codex"], ProviderModelConfig{Model: "codex-m2", Alias: "second"})
	if err := UpdateGlobalProvider("shared", p); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{
		`model = "m1" # keep first model note`,
		`model = "codex-m1" # keep agent model note`,
		`model = "m2"`,
		`model = "codex-m2"`,
		`name = "alpha" # untouched`,
	} {
		if !strings.Contains(string(got), keep) {
			t.Fatalf("missing %q after model append:\n%s", keep, got)
		}
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("model append unexpectedly used fallback: %v", err)
	}
}

func TestEditProviderModelKeepsOtherModelComments(t *testing.T) {
	original := strings.Replace(richProviderFixture,
		"alias = \"one\"\n",
		"alias = \"one\" # edited item note\n\n[[providers.models]]\nmodel = \"m2\" # untouched item note\nalias = \"two\"\n", 1)
	path := writeConfigFixture(t, original)
	patchConfigPath(t, path)
	p := readConfigFixture(t, path).Providers[0]
	p.Models[0].Alias = "first"
	if err := UpdateGlobalProvider("shared", p); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `alias = "first" # edited item note`) || !strings.Contains(string(got), `model = "m2" # untouched item note`) || !strings.Contains(string(got), `name = "alpha" # untouched`) {
		t.Fatalf("model edit lost comments:\n%s", got)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("model edit unexpectedly used fallback: %v", err)
	}
}

func TestUpdateSecondGlobalProviderKeepsFirstProvider(t *testing.T) {
	original := strings.Replace(providerDocumentFixture, "[[projects]]", `[[providers]]
name = "second"
api_key = "second-key" # edit this value

[[projects]]`, 1)
	path := writeConfigFixture(t, original)
	patchConfigPath(t, path)
	p := readConfigFixture(t, path).Providers[1]
	p.APIKey = "changed-key"
	if err := UpdateGlobalProvider("second", p); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(original, `api_key = "second-key" # edit this value`, `api_key = "changed-key" # edit this value`, 1)
	if string(got) != want {
		t.Fatalf("updating second provider changed unrelated content:\n%s", got)
	}
}

func TestSaveEmptyProviderRefsKeepsProjectComments(t *testing.T) {
	path := writeConfigFixture(t, providerDocumentFixture)
	patchConfigPath(t, path)
	if err := SaveProviderRefs("alpha", nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(providerDocumentFixture, `provider_refs = ["shared"] # keep refs comment`, `provider_refs = [] # keep refs comment`, 1)
	if string(got) != want {
		t.Fatalf("empty refs changed unrelated content:\n%s", got)
	}
}

func TestEditAgentModelListKeepsComments(t *testing.T) {
	path := writeConfigFixture(t, richProviderFixture)
	patchConfigPath(t, path)
	p := readConfigFixture(t, path).Providers[0]
	p.AgentModelLists["codex"][0].Alias = "renamed"
	if err := UpdateGlobalProvider("shared", p); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(richProviderFixture, `alias = "first"`, `alias = "renamed"`, 1)
	if string(got) != want {
		t.Fatalf("agent model edit changed unrelated TOML:\n%s", got)
	}
}
