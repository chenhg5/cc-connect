package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/config"
)

// Exercise the real management handlers and TOML persistence in memory. No
// listener, authentication fixture, provider, or external service is involved.
func TestManagementCommentary_SaveAndReload(t *testing.T) {
	originalPath := config.ConfigPath
	t.Cleanup(func() { config.ConfigPath = originalPath })
	for _, initial := range []string{"absent", "false", "true"} {
		for _, project := range []string{"absent", "false", "true"} {
			t.Run(initial+"/project="+project, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "config.toml")
				raw := ""
				if initial != "absent" {
					raw = "[display]\ncommentary_messages = " + initial + "\n"
				}
				raw += "[[projects]]\nname = \"test\"\n[projects.agent]\ntype = \"stub\"\n[[projects.platforms]]\ntype = \"test\"\n"
				if project != "absent" {
					raw += "[projects.display]\ncommentary_messages = " + project + "\n"
				}
				if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
					t.Fatal(err)
				}
				config.ConfigPath = path
				p := &stubPlatformEngine{n: "test"}
				e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
				t.Cleanup(func() { _ = e.Stop() })
				m := NewManagementServer(0, "", nil)
				m.RegisterEngine("test", e)
				m.SetGetGlobalSettings(config.GetGlobalSettings)
				m.SetSaveGlobalSettings(func(updates map[string]any) error {
					u := config.GlobalSettingsUpdate{}
					if value, ok := updates["commentary_messages"].(bool); ok {
						u.CommentaryMessages = &value
					}
					return config.SaveGlobalSettings(u)
				})
				var reloaded *config.Config
				e.SetConfigReloadFunc(func() (*ConfigReloadResult, error) {
					cfg, err := config.Load(path)
					if err != nil {
						return nil, err
					}
					reloaded = cfg
					enabled := config.EffectiveCommentaryMessages(cfg, &cfg.Projects[0])
					e.SetDisplayConfig(DisplayCfg{Mode: "quiet", CommentaryMessages: &enabled})
					return &ConfigReloadResult{DisplayUpdated: true}, nil
				})
				handler := m.buildHandler(http.NewServeMux())
				request := func(method, route, body string) mgmtResponse {
					t.Helper()
					recorder := httptest.NewRecorder()
					req := httptest.NewRequest(method, route, strings.NewReader(body))
					handler.ServeHTTP(recorder, req)
					var response mgmtResponse
					if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
						t.Fatal(err)
					}
					if recorder.Code != http.StatusOK || !response.OK {
						t.Fatalf("%s %s failed: %s", method, route, recorder.Body.String())
					}
					return response
				}
				request(http.MethodPost, "/api/v1/reload", "")
				wantGlobal := initial != "false"
				wantPresent := initial != "absent"
				for _, update := range []string{"absent", "false", "absent", "true", "absent"} {
					body := "{}"
					if update != "absent" {
						body = `{"commentary_messages":` + update + `}`
						wantGlobal = update == "true"
						wantPresent = true
					}
					before := *e.display.CommentaryMessages
					response := request(http.MethodPatch, "/api/v1/settings", body)
					var settings map[string]any
					if err := json.Unmarshal(response.Data, &settings); err != nil {
						t.Fatal(err)
					}
					if settings["commentary_messages"] != wantGlobal {
						t.Fatalf("saved setting = %v, want %v", settings["commentary_messages"], wantGlobal)
					}
					if *e.display.CommentaryMessages != before {
						t.Fatal("saving settings must not silently bypass reload")
					}
					request(http.MethodPost, "/api/v1/reload", "")
					if (reloaded.Display.CommentaryMessages != nil) != wantPresent {
						t.Fatalf("save/reload changed absent/explicit state: %+v", reloaded.Display)
					}
					want := wantGlobal
					if project != "absent" {
						want = project == "true"
					}
					if *e.display.CommentaryMessages != want {
						t.Fatalf("reloaded engine commentary = %v, want %v", *e.display.CommentaryMessages, want)
					}
					beforeMessages := len(p.getSent())
					e.relayCommentary(p, "ctx", "Reloaded progress", "")
					if got := len(p.getSent()) > beforeMessages; got != want {
						t.Fatalf("standalone message after reload = %v, want %v", got, want)
					}
					get := request(http.MethodGet, "/api/v1/settings", "")
					if err := json.Unmarshal(get.Data, &settings); err != nil {
						t.Fatal(err)
					}
					if settings["commentary_messages"] != wantGlobal {
						t.Fatal("GET did not retain the saved global setting")
					}
				}
			})
		}
	}
}
