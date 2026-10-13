package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

func TestBotProfile_NameUsesTargetAppAndChecksAPIResult(t *testing.T) {
	for _, code := range []int{0, 999} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			appID := "cli_name_test_" + fmt.Sprint(code)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
					var body map[string]string
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["app_id"] != appID || body["app_secret"] != "test-secret" {
						t.Error("name lookup used the wrong application")
					}
					fmt.Fprint(w, `{"code":0,"tenant_access_token":"test-token","expire":7200}`)
					return
				}
				if r.Method != "GET" || r.URL.Path != "/open-apis/bot/v3/info" || r.Header.Get("Authorization") != "Bearer test-token" {
					t.Error("unexpected bot info request")
				}
				calls++
				fmt.Fprintf(w, `{"code":%d,"bot":{"app_name":"代码审查助手"}}`, code)
			}))
			defer server.Close()
			name, err := readFeishuBotNameWithClient(context.Background(), registrationFlowResult{AppID: appID, AppSecret: "test-secret", Platform: "feishu"}, server.URL, server.Client())
			if calls != 1 || (code == 0 && (err != nil || name != "代码审查助手")) || (code != 0 && (err == nil || name != "")) {
				t.Fatalf("name=%q err=%v calls=%d", name, err, calls)
			}
		})
	}
}

func TestBotProfile_SharedCardHidesStableID(t *testing.T) {
	_, cfg, team, _ := projectFixture(t)
	cfg.Language = "en"
	member := team.Members[1]
	member.DisplayName = "代码审查助手"
	create := teamCreationForm(cfg, team, "manager", "bot")
	edit := teamMemberForm(cfg, team, member)
	if create.Form.SubmitText != "Create" || edit.Form.SubmitText != "Modify" {
		t.Fatal("wrong action labels")
	}
	if len(create.Form.Fields) != 3 || len(edit.Form.Fields) != 3 {
		t.Fatal("different profile fields")
	}
	for i, f := range create.Form.Fields {
		if f.Name != "role" && f.Name != "backend" && f.Name != "prompt" {
			t.Fatalf("unexpected editable field %q", f.Name)
		}
		other := edit.Form.Fields[i]
		if f.Name != other.Name || f.Kind != other.Kind || f.Label != other.Label || !reflect.DeepEqual(f.Options, other.Options) {
			t.Fatal("creation and editing don't share the same fields")
		}
	}
	if !strings.Contains(edit.RenderText(), "代码审查助手") || !strings.Contains(edit.RenderText(), "read-only") {
		t.Fatal("missing read-only Feishu name")
	}
	if strings.Contains(create.RenderText(), "Bot name in Feishu") || strings.Contains(create.RenderText(), "代码审查助手") {
		t.Fatal("creation asks for a name")
	}
	id := strings.TrimPrefix(create.Form.Command, "/team bot create ")
	if !core.ValidBotName(id) || strings.Contains(create.RenderText(), id) {
		t.Fatal("internal id visible or invalid")
	}
	second := teamCreationForm(cfg, team, "manager", "bot")
	if second.Form.Command == create.Form.Command {
		t.Fatal("new create forms share an internal id")
	}
	args, err := creationFormArgs(create, map[string][]string{"role": {"reviewer"}, "backend": {"codex"}}, team.Manager)
	if err != nil || args[2] != id {
		t.Fatalf("creation without a name failed: %v %v", args, err)
	}
	args, err = creationFormArgs(create, map[string][]string{"name": {"forged name"}, "id": {"forged-id"}, "bot": {"manager"}}, team.Manager)
	if err != nil || args[2] != id {
		t.Fatalf("submitted values changed the generated identity: %v %v", args, err)
	}
}

func TestBotProfile_MembersRefreshNamesAndBackendEdit(t *testing.T) {
	s, cfg := memberFormsFixture(t)
	for i := range cfg.Projects {
		cfg.Projects[i].Platforms[0].Options["app_secret"] = "test-secret"
	}
	cfg.Teams[0].Members[1].DisplayName = "Old name"
	writeFormConfig(t, s.path, cfg)
	s.readBotName = func(_ context.Context, id registrationFlowResult) (string, error) {
		if id.AppID == "cli_dev" {
			return "代码审查助手", nil
		}
		return "项目管理者", nil
	}
	p := &memberFormTestPlatform{}
	msg := &core.Message{UserID: "owner"}
	if _, err := s.handler("manager")(context.Background(), p, msg, []string{"members"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.getSent(), "代码审查助手 · developer · codex") || strings.Contains(p.getSent(), "Old name") {
		t.Fatal("Members did not use current platform name")
	}
	if _, err := s.handler("manager")(context.Background(), p, msg, []string{"members", "dev"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.latestForm().RenderText(), "代码审查助手") {
		t.Fatal("editing name differs from roster")
	}
	msg.FormValues = map[string][]string{"name": {"forged name"}, "id": {"forged-id"}, "bot": {"manager"}, "role": {"reviewer"}, "backend": {"trae"}, "prompt": {"Review changes"}}
	if _, err := s.handler("manager")(context.Background(), p, msg, []string{"members", "dev"}); err != nil {
		t.Fatal(err)
	}
	saved, err := config.Load(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Teams[0].Members[1].Bot != "dev" || saved.Teams[0].Members[1].DisplayName != "代码审查助手" || saved.Projects[1].Name != "dev" || saved.Projects[1].Agent.Type != "acp" {
		t.Fatal("display edit changed identity keys or failed to update backend")
	}
	if _, exists := saved.Projects[1].Agent.Options["model"]; exists {
		t.Fatal("foreign backend options retained")
	}
}
