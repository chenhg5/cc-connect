package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/timmyagentic/cc-connect-next/config"
)

func TestResolveFeishuSetupInputs_AutoModeWithoutCredentialsUsesNew(t *testing.T) {
	mode, appID, appSecret, err := resolveFeishuSetupInputs(feishuSetupModeAuto, "", "", "")
	if err != nil {
		t.Fatalf("resolveFeishuSetupInputs returned error: %v", err)
	}
	if mode != feishuSetupModeNew {
		t.Fatalf("mode = %q, want %q", mode, feishuSetupModeNew)
	}
	if appID != "" || appSecret != "" {
		t.Fatalf("credentials should be empty, got appID=%q appSecret=%q", appID, appSecret)
	}
}

func TestResolveFeishuSetupInputs_AutoModeWithAppUsesBind(t *testing.T) {
	mode, appID, appSecret, err := resolveFeishuSetupInputs(feishuSetupModeAuto, "cli_xxx:sec_xxx", "", "")
	if err != nil {
		t.Fatalf("resolveFeishuSetupInputs returned error: %v", err)
	}
	if mode != feishuSetupModeBind {
		t.Fatalf("mode = %q, want %q", mode, feishuSetupModeBind)
	}
	if appID != "cli_xxx" || appSecret != "sec_xxx" {
		t.Fatalf("credentials = (%q, %q), want (%q, %q)", appID, appSecret, "cli_xxx", "sec_xxx")
	}
}

func TestResolveFeishuSetupInputs_BindRequiresCredentials(t *testing.T) {
	_, _, _, err := resolveFeishuSetupInputs(feishuSetupModeBind, "", "", "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestResolveFeishuSetupInputs_RejectsMixedCredentialFlags(t *testing.T) {
	_, _, _, err := resolveFeishuSetupInputs(feishuSetupModeAuto, "cli_xxx:sec_xxx", "cli_xxx", "sec_xxx")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestParseAppPair_SecretCanContainColon(t *testing.T) {
	appID, appSecret, err := parseAppPair("cli_xxx:sec:yyy")
	if err != nil {
		t.Fatalf("parseAppPair returned error: %v", err)
	}
	if appID != "cli_xxx" || appSecret != "sec:yyy" {
		t.Fatalf("result = (%q, %q), want (%q, %q)", appID, appSecret, "cli_xxx", "sec:yyy")
	}
}

func TestSaveQRCodeImage_CreatesPNG(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-qr.png")

	if err := saveQRCodeImage("https://example.com/test", path); err != nil {
		t.Fatalf("saveQRCodeImage failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}
	if len(data) < 100 {
		t.Fatalf("PNG file too small: %d bytes", len(data))
	}
	// PNG magic bytes
	if data[0] != 0x89 || data[1] != 'P' || data[2] != 'N' || data[3] != 'G' {
		t.Fatal("output file is not a valid PNG")
	}
}

func TestSaveQRCodeImage_InvalidPath(t *testing.T) {
	err := saveQRCodeImage("https://example.com", "/nonexistent/dir/qr.png")
	if err == nil {
		t.Fatal("expected error for invalid path, got nil")
	}
}

func TestResolveRecommendedProfileFlags(t *testing.T) {
	tests := []struct {
		name        string
		yes, no     bool
		wantDecided bool
		wantApply   bool
		wantErr     bool
	}{
		{name: "no flags leaves the decision to the prompt"},
		{name: "explicit yes", yes: true, wantDecided: true, wantApply: true},
		{name: "explicit no", no: true, wantDecided: true, wantApply: false},
		{name: "both flags is a mistake worth reporting", yes: true, no: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decided, apply, err := resolveRecommendedProfileFlags(tt.yes, tt.no)
			if tt.wantErr {
				if err == nil {
					t.Fatal("error = nil, want a conflicting-flags error")
				}
				return
			}
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if decided != tt.wantDecided || apply != tt.wantApply {
				t.Fatalf("decided=%v apply=%v, want %v/%v", decided, apply, tt.wantDecided, tt.wantApply)
			}
		})
	}
}

func TestPromptYesNo(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		defaultYes bool
		want       bool
	}{
		{name: "empty line takes the default", input: "\n", defaultYes: true, want: true},
		{name: "empty line takes a negative default", input: "\n", defaultYes: false, want: false},
		{name: "y", input: "y\n", want: true},
		{name: "yes with spaces and case", input: "  YES  \n", want: true},
		{name: "chinese yes", input: "是\n", want: true},
		{name: "n", input: "n\n", defaultYes: true, want: false},
		{name: "no", input: "no\n", defaultYes: true, want: false},
		{name: "closed stdin takes the default", input: "", defaultYes: true, want: true},
		{name: "anything else is not a yes", input: "maybe\n", defaultYes: true, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			got := promptYesNo(strings.NewReader(tt.input), &out, "question?", tt.defaultYes)
			if got != tt.want {
				t.Fatalf("promptYesNo(%q, default=%v) = %v, want %v", tt.input, tt.defaultYes, got, tt.want)
			}
			if !strings.Contains(out.String(), "question?") {
				t.Fatalf("prompt was not shown: %q", out.String())
			}
		})
	}
}

func TestPrintRecommendedFeishuProfileShowsEverySetting(t *testing.T) {
	var out bytes.Buffer
	settings := config.RecommendedFeishuProfile("codex")
	printRecommendedFeishuProfile(&out, "demo", settings)

	text := out.String()
	// The listing is column-aligned for reading; compare on the collapsed form
	// so the assertion is about content, not padding.
	collapsed := regexp.MustCompile(` +`).ReplaceAllString(text, " ")
	for _, setting := range settings {
		if !strings.Contains(collapsed, setting.Key+" = "+setting.Value) {
			t.Fatalf("setting %q is not shown:\n%s", setting.Key, text)
		}
		if !strings.Contains(text, setting.Why) {
			t.Fatalf("rationale for %q is not shown:\n%s", setting.Key, text)
		}
	}
	for _, header := range []string{"[projects.display]", "[projects.references]", "[projects.platforms.options]"} {
		if !strings.Contains(text, header) {
			t.Fatalf("table %q is not shown:\n%s", header, text)
		}
	}
	// group_reply_all widens who the bot answers, so the consequence has to be
	// visible before the user says yes.
	if !strings.Contains(text, "allow_from") {
		t.Fatalf("group_reply_all is shown without its allow_from caveat:\n%s", text)
	}
}
