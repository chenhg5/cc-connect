package pi_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/agent/pi"
	"github.com/chenhg5/cc-connect/core"
)

func TestPiEngine_StartFailureDeliversLocalizedVersionGuidance(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, language := range []core.Language{
		core.LangEnglish, core.LangChinese, core.LangTraditionalChinese, core.LangJapanese, core.LangSpanish,
	} {
		for _, version := range []string{"0.98.9", "wrapper 0.99.0", "fail"} {
			t.Run(string(language)+"/"+version, func(t *testing.T) {
				agent, err := pi.New(map[string]any{
					"cmd": []string{executable, "-test.run=^TestRPCVersionCLIHelper$", "--", "wrapper-option"},
					"rpc": true, "language": string(language), "work_dir": t.TempDir(),
					"env": map[string]string{"CC_PI_VERSION_HELPER": "1", "CC_PI_VERSION": version},
				})
				if err != nil {
					t.Fatal(err)
				}
				platform := &issue2161Platform{changed: make(chan struct{}, 16)}
				engine := core.NewEngine("version-guidance", agent, []core.Platform{platform}, "", language)
				t.Cleanup(func() { _ = engine.Stop() })
				engine.ReceiveMessage(platform, &core.Message{
					SessionKey: "issue2161:version-user", Platform: platform.Name(), UserID: "version-user",
					MessageID: "startup", Content: "start the task", ReplyCtx: "startup",
				})
				select {
				case <-platform.changed:
				case <-time.After(5 * time.Second):
					t.Fatal("no startup failure reply")
				}
				// A parsable old version is told to upgrade; a probe failure or
				// ambiguous output is told the wrapper must forward --version,
				// so users on a current Pi do not loop into the same upgrade text.
				key := core.MsgAgentVersionUnreadable
				if version == "0.98.9" {
					key = core.MsgAgentVersionRequired
				}
				want := core.NewI18n(language).Tf(key,
					"Pi RPC", "Pi", "0.99.0", "npm install -g @earendil-works/pi-coding-agent@latest")
				sent := platform.snapshot()
				if len(sent) != 1 || sent[0].Text != want || sent[0].Method != "reply" || sent[0].ReplyTo != "startup" {
					t.Fatalf("startup deliveries = %+v, want localized version guidance %q", sent, want)
				}
				if strings.Contains(sent[0].Text, "exit status") {
					t.Fatalf("startup reply exposed probe diagnostics: %q", sent[0].Text)
				}
			})
		}
	}
}
