package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

func TestTeamInit_InvalidScannerNeverStartsOrRegistersAgain(t *testing.T) {
	for _, owner := range []string{"", "ou_", "*", "ou_owner,ou_intruder", " ou_owner", "ou_owner\n", "bot_id"} {
		t.Run(owner, func(t *testing.T) {
			spec := teamInitFixture(t, "codex")
			args := []string{"--config", spec.ConfigPath, "--work-dir", spec.WorkDir}
			calls := 0
			register := func(opts registrationFlowOptions) (*registrationFlowResult, error) {
				calls++
				r, err := fakeTeamRegistration(opts)
				r.OwnerOpenID = owner
				return r, err
			}
			start := func(string) error { t.Fatal("unowned bot started"); return nil }
			for attempt := 0; attempt < 2; attempt++ {
				var output bytes.Buffer
				err := teamInitCommandWithDeps(args, register, start, &output)
				if err == nil || !strings.Contains(err.Error(), core.NewI18n(core.LangChinese).T(core.MsgBotRegistrationOwnerMissing)) {
					t.Fatal("missing actionable binding error", err)
				}
				if _, err := os.Stat(spec.ConfigPath); !os.IsNotExist(err) {
					t.Fatal("unowned runtime config created")
				}
				if strings.Contains(output.String(), "/new claim") {
					t.Fatal("unexpected manual pairing fallback")
				}
			}
			if calls != 1 {
				t.Fatal("duplicate registration")
			}
			r, err := readTeamInitReceipt(spec.ConfigPath + ".setup.json")
			if err != nil || r.Identity.AppID != "cli_test_manager" {
				t.Fatal("registration result lost", err)
			}
		})
	}
}

func TestTeamInit_AutomaticOwnerSurvivesRestartAndRejectsOtherSenders(t *testing.T) {
	spec := teamInitFixture(t, "codex")
	if _, err := initializeTeam(context.Background(), spec, 10, fakeTeamRegistration); err != nil {
		t.Fatal(err)
	}
	for restart := 0; restart < 2; restart++ {
		cfg, err := config.Load(spec.ConfigPath)
		if err != nil {
			t.Fatal(err)
		}
		bot := cfg.Projects[0]
		p := &createdBotTestPlatform{}
		e := core.NewEngine(bot.Name, &createdBotTestAgent{}, []core.Platform{p}, filepath.Join(spec.DataDir, "sessions.json"), core.LangEnglish)
		e.SetBotOwnership(bot.BotOwnerClaimHash, bot.BotOwnerID, func(string) error {
			t.Fatal("auto-bound bot must not claim again")
			return nil
		})
		e.SetAdminFrom(bot.AdminFrom)
		for _, user := range []string{"ou_first_stranger", "ou_scanning_user"} {
			p.messages = nil
			e.ReceiveMessage(p, &core.Message{UserID: user, SessionKey: "test:" + user, Content: "/new control", ReplyCtx: "private"})
			out := strings.Join(p.messages, "\n")
			if user == "ou_first_stranger" {
				if !strings.Contains(out, core.NewI18n(core.LangEnglish).T(core.MsgBotOwnerRequired)) {
					t.Fatal("stranger accepted", out)
				}
			} else if !strings.Contains(out, "control") || strings.Contains(out, "/new claim") {
				t.Fatal("scanner cannot immediately use bot", out)
			}
		}
		e.Stop()
	}
}

func TestTeamInit_LarkAutomaticallyBindsOwnScanner(t *testing.T) {
	spec := teamInitFixture(t, "codex")
	r, err := initializeTeam(context.Background(), spec, 10, func(opts registrationFlowOptions) (*registrationFlowResult, error) {
		result, err := fakeTeamRegistration(opts)
		result.Platform, result.OwnerOpenID = "lark", "ou_lark_scanner"
		return result, err
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(spec.ConfigPath)
	if err != nil || cfg.Projects[0].BotOwnerID != "ou_lark_scanner" {
		t.Fatal("Lark owner lost", err)
	}
	var out bytes.Buffer
	if err := printTeamInitReady(&out, r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "https://applink.larksuite.com/") || strings.Contains(out.String(), "/new claim") {
		t.Fatal("wrong Lark instructions")
	}
}

func TestChatBotCreation_MissingScannerRetainsRegistrationWithoutActivation(t *testing.T) {
	c, starts := botCreatorFixture(t)
	calls := 0
	c.register = func(opts registrationFlowOptions) (*registrationFlowResult, error) {
		calls++
		r, err := fakeTeamRegistration(opts)
		r.OwnerOpenID = ""
		return r, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		var keys []core.MsgKey
		c.handler("supervisor")(context.Background(), &createdBotTestPlatform{}, "parent-owner", "developer", func(key core.MsgKey, _ ...any) { keys = append(keys, key) })
		if len(keys) != 1 || keys[0] != core.MsgBotRegistrationOwnerMissing {
			t.Fatal("missing owner not explained", keys)
		}
	}
	if calls != 1 || len(starts) != 0 {
		t.Fatal("unowned bot started or duplicate created")
	}
	cfg, err := config.Load(c.configPath)
	if err != nil || len(cfg.Projects) != 1 {
		t.Fatal("unowned bot saved", err)
	}
}

func TestChatBotCreation_AutomaticOwnerRetryAfterConnectionFailure(t *testing.T) {
	c, starts := botCreatorFixture(t)
	c.register = fakeTeamRegistration
	r, err := c.prepare(context.Background(), "supervisor", "parent-owner", "developer", func(core.MsgKey, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		key := make(chan core.MsgKey, 1)
		go c.finish(context.Background(), r, func(k core.MsgKey, _ ...any) { key <- k })
		req := <-starts
		cfg, err := config.Load(c.configPath)
		if err != nil || len(cfg.Projects) != 2 || cfg.Projects[1].BotOwnerID != "ou_scanning_user" || cfg.Projects[1].BotOwnerClaimHash != "" {
			t.Fatal("owner not saved before start", err)
		}
		if attempt == 0 {
			req.done <- errors.New("offline")
			if <-key != core.MsgBotAutoSavedOffline {
				t.Fatal("wrong retry guidance")
			}
		} else {
			req.done <- nil
			if <-key != core.MsgBotAutoReady {
				t.Fatal("retry failed")
			}
		}
	}
}
