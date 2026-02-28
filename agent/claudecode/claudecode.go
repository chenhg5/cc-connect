package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("claudecode", New)
}

type Agent struct {
	workDir string
	model   string
}

func New(opts map[string]any) (core.Agent, error) {
	workDir, _ := opts["work_dir"].(string)
	if workDir == "" {
		workDir = "."
	}
	model, _ := opts["model"].(string)

	if _, err := exec.LookPath("claude"); err != nil {
		return nil, fmt.Errorf("claudecode: 'claude' CLI not found in PATH, please install Claude Code first")
	}

	return &Agent{
		workDir: workDir,
		model:   model,
	}, nil
}

func (a *Agent) Name() string { return "claudecode" }

func (a *Agent) Execute(ctx context.Context, sessionID string, prompt string) (<-chan core.Response, error) {
	args := []string{"-p", prompt, "--output-format", "stream-json"}
	if sessionID != "" {
		args = append(args, "--session-id", sessionID)
	}
	if a.model != "" {
		args = append(args, "--model", a.model)
	}

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = a.workDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("claudecode: create stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("claudecode: start process: %w", err)
	}

	ch := make(chan core.Response, 1)

	go func() {
		defer close(ch)
		defer func() {
			if err := cmd.Wait(); err != nil {
				slog.Debug("claudecode: process exited", "error", err)
			}
		}()

		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

		var lastContent string
		var detectedSessionID string

		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}

			var event map[string]any
			if err := json.Unmarshal([]byte(line), &event); err != nil {
				slog.Debug("claudecode: skip non-json line", "line", line)
				continue
			}

			eventType, _ := event["type"].(string)
			switch eventType {
			case "system":
				if sid, ok := event["session_id"].(string); ok {
					detectedSessionID = sid
				}
			case "assistant":
				if text, ok := event["text"].(string); ok {
					lastContent += text
				}
			case "result":
				if result, ok := event["result"].(string); ok {
					lastContent = result
				}
				if sid, ok := event["session_id"].(string); ok {
					detectedSessionID = sid
				}
			}
		}

		if err := scanner.Err(); err != nil {
			ch <- core.Response{Error: fmt.Errorf("claudecode: read output: %w", err)}
			return
		}

		ch <- core.Response{
			Content:   lastContent,
			SessionID: detectedSessionID,
			Done:      true,
		}
	}()

	return ch, nil
}

func (a *Agent) Stop() error {
	return nil
}
