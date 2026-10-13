package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Mock only the external Codex process. Exercise both real constructors and
// Send paths, including resuming a thread created before team coordination.
func TestRolePrompt_ResumeRefreshesInstructionsWithoutLosingThread(t *testing.T) {
	for _, backend := range []string{"exec", "app_server"} {
		for _, resume := range []string{"", "retained-thread"} {
			t.Run(backend+"/"+resume, func(t *testing.T) {
				dir := t.TempDir()
				capture := filepath.Join(dir, "requests.jsonl")
				env := []string{"CC_CODEX_ROLE_TEST=1", "CC_CODEX_ROLE_CAPTURE=" + capture}
				args := []string{"-test.run=^TestRolePromptHelperProcess$", "--"}
				start := func(id, instructions string) core.AgentSession {
					t.Helper()
					var session core.AgentSession
					var err error
					if backend == "app_server" {
						session, err = newAppServerSession(context.Background(), os.Args[0], args, "stdio://", dir, "", "", "suggest", id, "", "", env, "", "Manager policy", instructions, nil)
					} else {
						session, err = newCodexSession(context.Background(), os.Args[0], args, dir, "", "", "suggest", id, "", env, "", "Manager policy", instructions)
					}
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = session.Close() })
					return session
				}
				send := func(session core.AgentSession, message string) {
					t.Helper()
					if err := session.Send(message, "", nil, nil); err != nil {
						t.Fatal(err)
					}
					if cs, ok := session.(*codexSession); ok {
						cs.wg.Wait() // wait for the CLI before reading its captured stdin
					}
				}
				instructions := "Team members: dev | developer\ncc-connect-team team dispatch --to dev --message task"
				session := start(resume, instructions)
				send(session, "Implement the plan")
				send(session, "Show task progress")
				id := session.CurrentSessionID()
				if id != "retained-thread" {
					t.Fatalf("history moved to another thread: %q", id)
				}
				_ = session.Close()
				// Reconnect after a settings/service change, retaining conversation.
				session = start(id, instructions+"\nNew member: qa | tester")
				send(session, "Continue verification")
				b, err := os.ReadFile(capture)
				if err != nil {
					t.Fatal(err)
				}
				var prompts []string
				for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
					var prompt string
					if err := json.Unmarshal([]byte(line), &prompt); err != nil {
						t.Fatal(err)
					}
					prompts = append(prompts, prompt)
				}
				if len(prompts) != 3 {
					t.Fatalf("got %d turns", len(prompts))
				}
				for _, i := range []int{0, 2} {
					if !strings.Contains(prompts[i], instructions) || !strings.Contains(prompts[i], "Manager policy") {
						t.Fatalf("turn %d lost current role/dispatch instructions: %q", i+1, prompts[i])
					}
				}
				if prompts[1] != "Show task progress" {
					t.Fatal("instructions repeated on every turn of the same runtime session")
				}
				if !strings.Contains(prompts[2], "New member: qa | tester") {
					t.Fatal("reconnection retained stale roster")
				}
			})
		}
	}
}

func TestRolePromptHelperProcess(t *testing.T) {
	if os.Getenv("CC_CODEX_ROLE_TEST") != "1" {
		return
	}
	writePrompt := func(prompt string) {
		f, err := os.OpenFile(os.Getenv("CC_CODEX_ROLE_CAPTURE"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(2)
		}
		if err := json.NewEncoder(f).Encode(prompt); err != nil {
			os.Exit(2)
		}
		_ = f.Close()
	}
	if !strings.Contains(strings.Join(os.Args, " "), "app-server") {
		b, _ := io.ReadAll(os.Stdin)
		writePrompt(string(b))
		fmt.Println(`{"type":"thread.started","thread_id":"retained-thread"}`)
		fmt.Println(`{"type":"turn.completed"}`)
		os.Exit(0)
	}
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var req struct {
			ID     *int64         `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if decoder.Decode(&req) != nil {
			os.Exit(0)
		}
		if req.ID == nil {
			continue
		}
		result := map[string]any{}
		switch req.Method {
		case "thread/start", "thread/resume":
			id := "retained-thread"
			if req.Method == "thread/resume" {
				id = req.Params["threadId"].(string)
			}
			result["thread"] = map[string]any{"id": id}
		case "turn/start":
			input := req.Params["input"].([]any)[0].(map[string]any)
			writePrompt(input["text"].(string))
			result["turn"] = map[string]any{"id": fmt.Sprintf("turn-%d", time.Now().UnixNano())}
		}
		if err := encoder.Encode(map[string]any{"id": req.ID, "result": result}); err != nil {
			os.Exit(2)
		}
	}
}
