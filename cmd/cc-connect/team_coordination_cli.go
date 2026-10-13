package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

func teamActionCLI(args []string) error {
	fs := flag.NewFlagSet("team "+args[0], flag.ContinueOnError)
	req := core.TeamActionRequest{Action: args[0]}
	fs.StringVar(&req.From, "from", os.Getenv("CC_PROJECT"), "manager bot ID")
	fs.StringVar(&req.SessionKey, "session-key", os.Getenv("CC_SESSION_KEY"), "project conversation")
	fs.StringVar(&req.Title, "title", "", "short task title")
	fs.StringVar(&req.Deliverable, "deliverable", "", "expected artifacts and verification")
	fs.StringVar(&req.TaskID, "task-id", "", "existing task ID for retry or acceptance")
	fs.StringVar(&req.Evidence, "evidence", "", "artifacts inspected and verification for acceptance")
	deps := fs.String("depends-on", "", "comma-separated prerequisite task IDs (must be accepted before dispatch)")
	fs.StringVar(&req.To, "to", "", "target member bot ID")
	fs.StringVar(&req.Message, "message", "", "task and acceptance criteria")
	fs.StringVar(&req.Role, "role", "", "required role")
	fs.StringVar(&req.Backend, "backend", "codex", "backend for new role")
	fs.StringVar(&req.Reason, "reason", "", "why a new role is needed, its task and expected deliverable (shown to the owner)")
	dataDir := fs.String("data-dir", "", "service data directory")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || req.From == "" || req.SessionKey == "" {
		return fmt.Errorf("team action requires the current manager and project session")
	}
	if *deps != "" {
		for _, dep := range strings.Split(*deps, ",") {
			req.DependsOn = append(req.DependsOn, strings.TrimSpace(dep))
		}
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return err
	}
	resp, err := apiPost(resolveSocketPath(*dataDir), "/team/action", payload)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("team action: %s", body)
	}
	fmt.Println(string(body))
	return nil
}
