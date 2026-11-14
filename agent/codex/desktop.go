package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/google/uuid"
)

// ValidateDesktopThread checks exact rollout metadata rather than filename substrings.
func (a *Agent) ValidateDesktopThread(id string) error {
	a.mu.RLock()
	workDir := a.workDir
	a.mu.RUnlock()
	return a.ValidateDesktopThreadAt(id, workDir)
}

// Validate without creating a workspace agent or changing the agent's directory.
func (a *Agent) ValidateDesktopThreadAt(id, directory string) (err error) {
	a.mu.RLock()
	home := a.codexHome
	a.mu.RUnlock()
	f, err := os.Open(findSessionFileInCodexHome(resolveCodexHomeDir(home), id))
	if err != nil {
		return fmt.Errorf("desktop thread is unavailable")
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("desktop thread metadata could not be closed")
		}
	}()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 1024*1024)
	for scanner.Scan() {
		var entry struct {
			Type    string `json:"type"`
			Payload struct {
				ID     string `json:"id"`
				Cwd    string `json:"cwd"`
				Source string `json:"source"`
			} `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry.Type != "session_meta" {
			continue
		}
		actual, e1 := filepath.EvalSymlinks(entry.Payload.Cwd)
		expected, e2 := filepath.EvalSymlinks(directory)
		if e1 == nil && e2 == nil && actual == expected && entry.Payload.ID == id && entry.Payload.Source == "vscode" {
			return nil
		}
		break
	}
	return fmt.Errorf("thread does not belong to this desktop workspace")
}

// The helper talks to the existing desktop owner; there is no CLI resume fallback.
func (a *Agent) desktopCall(ctx context.Context, args ...string) (json.RawMessage, error) {
	a.mu.RLock()
	helper := append([]string(nil), a.desktopHelper...)
	home, state := a.codexHome, a.desktopStateDir
	env := append([]string(nil), a.configEnv...)
	a.mu.RUnlock()
	if len(helper) == 0 {
		return nil, fmt.Errorf("desktop helper is disabled")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, helper[0], append(helper[1:], args...)...)
	if home != "" {
		env = append(env, "CODEX_HOME="+home)
	}
	if state != "" {
		env = append(env, "CC_CONNECT_DESKTOP_STATE_DIR="+state)
	}
	cmd.Env = core.MergeEnv(os.Environ(), env)
	output, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			detail := string(exit.Stderr)
			for i, masked := range core.RedactEnv(cmd.Env) {
				if masked != cmd.Env[i] {
					_, secret, _ := strings.Cut(cmd.Env[i], "=")
					detail = core.RedactToken(detail, secret)
				}
			}
			for i, masked := range core.RedactArgs(helper) {
				if masked != helper[i] {
					secret := helper[i]
					if _, value, found := strings.Cut(secret, "="); found && masked != "***" {
						secret = value
					}
					detail = core.RedactToken(detail, secret)
				}
			}
			detail = strings.Join(strings.Fields(detail), " ")
			if runes := []rune(detail); len(runes) > 512 {
				detail = string(runes[:512]) + "…"
			}
			if detail != "" {
				return nil, fmt.Errorf("desktop helper failed; check the desktop before retrying: %w (%s)", err, detail)
			}
		}
		return nil, fmt.Errorf("desktop helper failed; check the desktop before retrying: %w", err)
	}
	if !json.Valid(output) {
		return nil, fmt.Errorf("invalid desktop acknowledgement; check the desktop before retrying")
	}
	return json.RawMessage(output), nil
}
func (a *Agent) desktopMutation(ctx context.Context, args ...string) error {
	output, err := a.desktopCall(ctx, args...)
	if err != nil {
		return err
	}
	var ack struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(output, &ack) != nil || ack.Status != "accepted" {
		return fmt.Errorf("desktop acceptance is unknown; check the desktop before retrying")
	}
	return nil
}
func (a *Agent) ReplyToThread(ctx context.Context, id, text string) error {
	return a.ReplyToThreadWithMode(ctx, id, text, "queue", uuid.NewString())
}
func (a *Agent) ReplyToThreadWithMode(ctx context.Context, id, text, mode, messageID string) error {
	if mode != "queue" && mode != "now" {
		return fmt.Errorf("invalid reply mode")
	}
	if messageID == "" {
		messageID = uuid.NewString()
	}
	output, err := a.desktopCall(ctx, "reply", id, mode, messageID, text)
	if err != nil {
		return err
	}
	var ack struct {
		Status    string `json:"status"`
		MessageID string `json:"message_id"`
	}
	if json.Unmarshal(output, &ack) != nil || ack.MessageID == "" || (ack.Status != "accepted" && ack.Status != "queued") || (mode == "now" && ack.Status != "accepted") {
		return fmt.Errorf("desktop reply acceptance is unknown; check the desktop before retrying")
	}
	return nil
}
func (a *Agent) AnswerThreadRequest(ctx context.Context, id, key, text string) error {
	return a.desktopMutation(ctx, "answer", id, key, text)
}
func (a *Agent) ThreadProgress(ctx context.Context, id string) (json.RawMessage, error) {
	return a.desktopCall(ctx, "progress", id)
}
