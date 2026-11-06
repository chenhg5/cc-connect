package acp

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestACPBridgePrompt_AutomaticallyLoadedWithoutCustomOptions(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(map[string]any{"cmd": bin, "work_dir": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.(*Agent).bridgePrompt, "cc-connect send --file") {
		t.Fatal("worker with no custom prompt did not load bridge instructions")
	}
}

func TestACPBridgePrompt_ResumedRequestsAndCompactKeepHostContext(t *testing.T) {
	for _, embedded := range []bool{true, false} {
		t.Run(fmt.Sprint(embedded), func(t *testing.T) {
			s, wResp, rReq := newTestSession(t, &fakeCallbacks{})
			s.bridgePrompt = core.AgentSessionPrompt(core.LangEnglish, "", "")
			s.embeddedContext = embedded
			requests := make(chan map[string]any, 4)
			go func() {
				dec := json.NewDecoder(rReq)
				for {
					var req map[string]any
					if dec.Decode(&req) != nil {
						return
					}
					requests <- req
					_, _ = fmt.Fprintf(wResp, `{"jsonrpc":"2.0","id":%v,"result":{"stopReason":"end_turn"}}`+"\n", req["id"])
				}
			}()
			for _, prompt := range []string{"send existing report", "/compact", "send it again"} {
				if err := s.Send(prompt, "", nil, nil); err != nil {
					t.Fatal(err)
				}
				req := <-requests
				blocks := req["params"].(map[string]any)["prompt"].([]any)
				if len(blocks) != 2 || blocks[0].(map[string]any)["text"] != prompt {
					t.Fatal("user/slash input changed", blocks)
				}
				host := blocks[1].(map[string]any)
				text, _ := host["text"].(string)
				if embedded {
					if host["type"] != "resource" {
						t.Fatal(host)
					}
					resource := host["resource"].(map[string]any)
					if resource["uri"] != "cc-connect://session/instructions" {
						t.Fatal(resource)
					}
					text, _ = resource["text"].(string)
				}
				if !strings.Contains(text, "cc-connect send --file") {
					t.Fatal("bridge context lost", host)
				}
			}
		})
	}
}
