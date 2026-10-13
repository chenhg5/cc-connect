package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestACPError_DiagnosticIsBoundedAndTurnScoped(t *testing.T) {
	var d stderrDiagnostics
	d.beginTurn()
	_, _ = d.Write([]byte(strings.Repeat("x", stderrTailLimit*4)))
	if len(d.String()) > stderrTailLimit {
		t.Fatal("stderr grew without bounds")
	}
	d.beginTurn()
	_, _ = d.Write([]byte("ERROR traex_acp::thread: 当前仓库密级较高，所选模型不可用，请切换到支持 L4 "))
	_, _ = d.Write([]byte("仓库的模型。\n"))
	err := &rpcErrPayload{Code: -32603, Message: "Internal error"}
	if !strings.Contains(d.promptError(context.Background(), err).Error(), "4037") {
		t.Fatal("diagnostic split across writes was lost")
	}
	d.beginTurn()
	_, _ = d.Write([]byte("INFO unrelated output: 当前仓库密级较高，支持 L4 仓库的模型\n"))
	if got := d.promptError(context.Background(), err); got != err {
		t.Fatalf("previous turn or unrelated output replaced the error: %v", got)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 50; n++ {
				_, _ = d.Write([]byte("diagnostic\n"))
				_ = d.String()
			}
		}()
	}
	wg.Wait()
}

func TestACPError_RepositoryPolicySurvivesGenericRPCError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := newACPSession(ctx, acpSessionConfig{
		command: os.Args[0], args: []string{"-test.run=^TestACPDiagnosticHelper$", "--"},
		extraEnv: []string{"CC_ACP_DIAGNOSTIC_HELPER=1"}, workDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for turn := 0; turn < 3; turn++ {
		err := s.Send("test", "", nil, nil)
		if err == nil {
			t.Fatal("expected native error")
		}
		select {
		case event := <-s.Events():
			if event.Type != core.EventError || event.Error == nil {
				t.Fatalf("expected error event: %+v", event)
			}
			for _, failure := range []error{err, event.Error} {
				if turn == 0 && (!strings.Contains(failure.Error(), "L4") || !strings.Contains(failure.Error(), "4037")) {
					t.Fatalf("lost repository-policy diagnosis: %v", failure)
				}
				if turn > 0 && strings.Contains(failure.Error(), "L4") {
					t.Fatalf("stale diagnosis or specific error overwritten: %v", failure)
				}
				if strings.Contains(failure.Error(), "private-diagnostic") {
					t.Fatal("raw stderr leaked into report")
				}
			}
		case <-ctx.Done():
			t.Fatal("error event missing")
		}
	}
}

func TestACPDiagnosticHelper(t *testing.T) {
	if os.Getenv("CC_ACP_DIAGNOSTIC_HELPER") != "1" {
		return
	}
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	turn := 0
	for {
		var req struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		if decoder.Decode(&req) != nil {
			os.Exit(0)
		}
		if req.ID == nil {
			continue
		}
		result := map[string]any{}
		switch req.Method {
		case "initialize":
			result["protocolVersion"] = 1
		case "session/new":
			result["sessionId"] = "diagnostic-session"
		case "session/prompt":
			if turn != 1 {
				fmt.Fprintln(os.Stderr, "ERROR codex_api::sse::trae_chat: Trae chat error: invalid request: 当前仓库密级较高，所选模型不可用，请切换到支持 L4 仓库的模型。 code=4037 private-diagnostic")
			}
			code, message := -32603, "Internal error"
			if turn == 2 {
				code, message = -32000, "Specific failure"
			}
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": *req.ID, "error": map[string]any{"code": code, "message": message}})
			turn++
			continue
		}
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": result})
	}
}
