package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

func teamInitFixture(t *testing.T, agent string) teamInitSpec {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"codex", "claude", "traecli"} {
		// Adapter construction only checks availability; never run these scripts.
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	spec := teamInitSpec{ConfigPath: filepath.Join(dir, "team", "config.toml"), WorkDir: dir, Agent: agent, BotID: "manager-main", Language: "zh"}
	if err := spec.normalize(); err != nil {
		t.Fatal(err)
	}
	return spec
}

func fakeTeamRegistration(registrationFlowOptions) (*registrationFlowResult, error) {
	return &registrationFlowResult{AppID: "cli_test_manager", AppSecret: "not-a-real-test-secret", OwnerOpenID: "ou_scanning_user", Platform: "feishu"}, nil
}

func TestTeamInit_QRRegistrationAutomaticallyBindsScanner(t *testing.T) {
	for _, backend := range []string{"codex", "claudecode", "trae"} {
		t.Run(backend, func(t *testing.T) {
			spec := teamInitFixture(t, backend)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/oauth/v1/app/registration" {
					t.Error("wrong registration path")
				}
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				calls++
				switch r.Form.Get("action") {
				case "init":
					fmt.Fprint(w, `{"supported_auth_methods":["client_secret"]}`)
				case "begin":
					if r.Form.Get("archetype") != "PersonalAgent" || r.Form.Get("request_user_info") != "open_id tenant_brand" {
						t.Error("incorrect registration archetype")
					}
					fmt.Fprint(w, `{"device_code":"test-device-grant","verification_uri_complete":"https://example.invalid/authorize","interval":1}`)
				case "poll":
					if r.Form.Get("device_code") != "test-device-grant" {
						t.Error("wrong grant")
					}
					fmt.Fprint(w, `{"client_id":"cli_test_manager","client_secret":"not-a-real-test-secret","user_info":{"open_id":"ou_scanning_user"}}`)
				default:
					t.Error("unexpected registration action")
				}
			}))
			defer server.Close()
			var authorizationLink string
			register := func(opts registrationFlowOptions) (*registrationFlowResult, error) {
				opts.OnVerification = func(link string) error { authorizationLink = link; return nil }
				return runRegistrationFlowWithClient(opts, &registrationClient{baseURL: server.URL, http: server.Client()})
			}
			receipt, err := initializeTeam(context.Background(), spec, 10, register)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 3 || authorizationLink != "https://example.invalid/authorize" {
				t.Fatal("QR flow was not completed")
			}
			cfg, err := config.Load(spec.ConfigPath)
			if err != nil {
				t.Fatal(err)
			}
			if len(cfg.Bots) != 0 || len(cfg.Projects) != 1 || len(cfg.Teams) != 1 || len(cfg.BusinessProjects) != 0 {
				t.Fatal("setup must create only one Bot, no business project")
			}
			bot := cfg.Projects[0]
			if bot.Platforms[0].Options["single_card_reply"] != true || bot.Platforms[0].Options["separate_final_card"] != true {
				t.Fatal("manager must use process and final answer cards")
			}
			switch backend {
			case "codex":
				if bot.Agent.Options["backend"] != "app_server" || bot.Agent.Options["mode"] != "suggest" {
					t.Fatal("manager must use the interactive Codex approval transport")
				}
			case "claudecode":
				if bot.Agent.Options["mode"] != "default" {
					t.Fatal("Claude manager must ask for permission")
				}
			case "trae":
				if got := fmt.Sprint(bot.Agent.Options["args"]); got != "[--permission-mode default acp serve]" {
					t.Fatalf("Trae manager approval args = %s", got)
				}
			}
			wantBackend := backend
			if backend == "trae" {
				wantBackend = "acp"
			}
			if cfg.Teams[0].Members[0].Role != "manager" || bot.Agent.Type != wantBackend || bot.Name != spec.BotID {
				t.Fatal("incorrect manager definition")
			}
			if bot.BotOwnerID != "ou_scanning_user" || bot.AdminFrom != "ou_scanning_user" || bot.BotOwnerClaimHash != "" {
				t.Fatal("scanner not automatically bound as owner")
			}
			for _, path := range []string{spec.ConfigPath, spec.ConfigPath + ".setup.json"} {
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("private storage required: %s", path)
				}
			}
			data, _ := os.ReadFile(spec.ConfigPath)
			if receipt.ClaimToken != "" || receipt.Version != 3 || !bytes.Contains(data, []byte("ou_scanning_user")) {
				t.Fatal("missing scanner or unexpected manual pairing")
			}
			var output bytes.Buffer
			if err := printTeamInitReady(&output, receipt); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(output.String(), "not-a-real-test-secret") || strings.Contains(output.String(), "/new claim ") || !strings.Contains(output.String(), "自动") {
				t.Fatal("incorrect setup instructions")
			}
			again, err := initializeTeam(context.Background(), spec, 10, func(registrationFlowOptions) (*registrationFlowResult, error) {
				t.Fatal("duplicate app creation")
				return nil, nil
			})
			if err != nil || again.Identity.OwnerOpenID != receipt.Identity.OwnerOpenID {
				t.Fatal("setup retry failed", err)
			}
		})
	}
}

func TestTeamInit_ExistingConfigOrInvalidInputsNeverRegister(t *testing.T) {
	for _, kind := range []string{"existing", "symlink", "missing-directory", "invalid-agent", "corrupt-receipt", "locked", "missing-cli"} {
		t.Run(kind, func(t *testing.T) {
			spec := teamInitFixture(t, "codex")
			if err := os.MkdirAll(filepath.Dir(spec.ConfigPath), 0700); err != nil {
				t.Fatal(err)
			}
			existing := []byte("# keep my configuration\n")
			switch kind {
			case "existing":
				os.WriteFile(spec.ConfigPath, existing, 0600)
			case "symlink":
				target := filepath.Join(spec.WorkDir, "untouched")
				os.WriteFile(target, existing, 0600)
				if err := os.Symlink(target, spec.ConfigPath); err != nil {
					t.Fatal(err)
				}
			case "missing-directory":
				spec.WorkDir = filepath.Join(spec.WorkDir, "missing")
			case "invalid-agent":
				spec.Agent = "invalid"
			case "corrupt-receipt":
				os.WriteFile(spec.ConfigPath+".setup.json", []byte(`{"Version":99}`), 0600)
			case "locked":
				lock, err := AcquireInstanceLock(spec.ConfigPath)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Release()
			case "missing-cli":
				t.Setenv("PATH", t.TempDir())
			}
			_, err := initializeTeam(context.Background(), spec, 10, func(registrationFlowOptions) (*registrationFlowResult, error) {
				t.Fatal("must reject before creating app")
				return nil, nil
			})
			if err == nil {
				t.Fatal("expected preflight failure")
			}
			if kind == "existing" || kind == "symlink" {
				after, _ := os.ReadFile(spec.ConfigPath)
				if !bytes.Equal(after, existing) {
					t.Fatal("existing file modified")
				}
			}
		})
	}
}

func TestTeamInit_SaveFailureResumesAuthorizedRegistration(t *testing.T) {
	spec := teamInitFixture(t, "codex")
	calls := 0
	register := func(opts registrationFlowOptions) (*registrationFlowResult, error) {
		calls++
		// Another writer publishes a file while the user is scanning.
		if err := os.WriteFile(spec.ConfigPath, []byte("other writer"), 0600); err != nil {
			t.Fatal(err)
		}
		return fakeTeamRegistration(opts)
	}
	if _, err := initializeTeam(context.Background(), spec, 10, register); err == nil {
		t.Fatal("overwrote racing writer")
	}
	if calls != 1 {
		t.Fatal("expected one app creation")
	}
	if _, err := os.Stat(spec.ConfigPath + ".setup.json"); err != nil {
		t.Fatal("lost authorized result")
	}
	if err := os.Remove(spec.ConfigPath); err != nil {
		t.Fatal(err)
	}
	receipt, err := initializeTeam(context.Background(), spec, 10, func(registrationFlowOptions) (*registrationFlowResult, error) {
		t.Fatal("registered app twice")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Identity.AppID != "cli_test_manager" {
		t.Fatal("wrong recovered app")
	}
	spec.Agent = "claudecode"
	if _, err := initializeTeam(context.Background(), spec, 10, register); err == nil {
		t.Fatal("silently ignored changed setup options")
	}
}

func TestTeamInit_RegistrationFailureDoesNotCreateConfig(t *testing.T) {
	for _, registrationErr := range []error{context.Canceled, context.DeadlineExceeded, errors.New("authorization denied")} {
		t.Run(registrationErr.Error(), func(t *testing.T) {
			spec := teamInitFixture(t, "codex")
			_, err := initializeTeam(context.Background(), spec, 10, func(registrationFlowOptions) (*registrationFlowResult, error) { return nil, registrationErr })
			if !errors.Is(err, registrationErr) {
				t.Fatal("registration error lost")
			}
			for _, path := range []string{spec.ConfigPath, spec.ConfigPath + ".setup.json"} {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatal("failed registration left a configuration")
				}
			}
		})
	}
}

func TestTeamInit_LegacyReceiptResumesPrivateClaim(t *testing.T) {
	spec := teamInitFixture(t, "codex")
	identity, _ := fakeTeamRegistration(registrationFlowOptions{})
	identity.OwnerOpenID = ""
	oldReceipt := &teamInitReceipt{Version: 1, Spec: spec, Identity: *identity, ClaimToken: strings.Repeat("ab", 32)}
	data, _ := json.Marshal(oldReceipt)
	if err := os.MkdirAll(filepath.Dir(spec.ConfigPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spec.ConfigPath+".setup.json", data, 0600); err != nil {
		t.Fatal(err)
	}
	receipt, err := initializeTeam(context.Background(), spec, 10, func(registrationFlowOptions) (*registrationFlowResult, error) {
		t.Fatal("legacy receipt must not register another app")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(spec.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	oldPath := config.ConfigPath
	config.ConfigPath = spec.ConfigPath
	t.Cleanup(func() { config.ConfigPath = oldPath })
	p := &createdBotTestPlatform{}
	bot := cfg.Bots[0]
	e := core.NewEngine(bot.ID, &createdBotTestAgent{}, []core.Platform{p}, filepath.Join(spec.DataDir, "sessions.json"), core.LangEnglish)
	defer e.Stop()
	e.SetBotOwnership(bot.BotOwnerClaimHash, "", func(user string) error {
		if err := config.ClaimBotIdentity(bot.ID, bot.BotOwnerClaimHash, user); err != nil {
			return err
		}
		return cleanupTeamInitReceipt(spec.ConfigPath, bot.ID)
	})
	send := func(user, content, reply string) {
		e.ReceiveMessage(p, &core.Message{UserID: user, Content: content, SessionKey: "test:" + user, ReplyCtx: reply})
	}
	send("owner", "/new claim "+receipt.ClaimToken, "group")
	send("owner", "/new claim wrong", "private")
	send("ou_actual_new_app_user", "/new claim "+receipt.ClaimToken, "private")
	cfg, err = config.Load(spec.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Bots[0].BotOwnerID != "ou_actual_new_app_user" || cfg.Bots[0].AdminFrom != "ou_actual_new_app_user" || cfg.Bots[0].BotOwnerClaimHash != "" {
		t.Fatal("actual owner not persisted")
	}
	if _, err := os.Stat(spec.ConfigPath + ".setup.json"); !os.IsNotExist(err) {
		t.Fatal("completed receipt retained")
	}
	if strings.Contains(strings.Join(p.messages, "\n"), receipt.ClaimToken) {
		t.Fatal("claim token echoed")
	}
}

func TestRegistration_DebugDoesNotLeakCredentials(t *testing.T) {
	response := map[string]string{"client_id": "cli_example", "client_secret": "secret-not-for-logs", "device_code": "private-device-code"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { json.NewEncoder(w).Encode(response) }))
	defer server.Close()
	c := &registrationClient{baseURL: server.URL, http: server.Client(), debug: true}
	var got registrationPollResponse
	output := captureStderr(t, func() {
		if err := c.registrationCall("poll", nil, &got); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(output, response["client_secret"]) || strings.Contains(output, response["device_code"]) || !strings.Contains(output, "action=poll") {
		t.Fatal("unsafe debug output")
	}
	if got.ClientSecret != response["client_secret"] {
		t.Fatal("decode broken")
	}
}

func TestTeamInit_TranslationsComplete(t *testing.T) {
	for _, lang := range []core.Language{core.LangEnglish, core.LangChinese, core.LangTraditionalChinese, core.LangJapanese, core.LangSpanish} {
		i := core.NewI18n(lang)
		for _, key := range []core.MsgKey{core.MsgTeamUsage, core.MsgTeamInitScan, core.MsgTeamInitReady, core.MsgTeamInitStarting, core.MsgTeamInitNoStart, core.MsgTeamInitAutoReady, core.MsgBotAutoReady, core.MsgBotAutoSavedOffline, core.MsgBotRegistrationOwnerMissing} {
			if i.T(key) == "" || i.T(key) == string(key) {
				t.Fatalf("missing translation %s %s", lang, key)
			}
		}
	}
}
