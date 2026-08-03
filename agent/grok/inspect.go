package grok

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/google/uuid"
)

const (
	grokInspectTimeout      = 5 * time.Second
	grokSlashProbeTimeout   = 15 * time.Second
	grokProbeCleanupTimeout = 5 * time.Second
	grokCommandWaitDelay    = 2 * time.Second
	grokCommandOutputLimit  = 4 << 20
)

// grokInspectRunner is injectable so the native discovery contract (binary,
// arguments, cwd, and environment) can be verified without launching Grok in
// unit tests. It is also used for the best-effort probe-session cleanup.
type grokInspectRunner func(ctx context.Context, command string, args []string, workDir string, env []string) ([]byte, error)

type grokInspectDocument struct {
	Skills *[]grokInspectSkill `json:"skills"`
}

type grokInspectSkill struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	UserInvocable bool   `json:"userInvocable"`
	Enabled       *bool  `json:"enabled"`
	Source        struct {
		Type       string `json:"type"`
		Path       string `json:"path"`
		PluginName string `json:"plugin_name"`
	} `json:"source"`
}

type grokSlashInit struct {
	Type          string    `json:"type"`
	Subtype       string    `json:"subtype"`
	SessionID     string    `json:"session_id"`
	SlashCommands *[]string `json:"slash_commands"`
	Skills        *[]string `json:"skills"`
}

// NativeSlashCommands asks a real, read-only Grok session for its initialized
// slash command table. Unlike `inspect`, the init frame contains the exact
// advertised targets after Grok applies scope qualification, collision
// handling, plugin namespaces, and built-in command registration.
func (a *Agent) NativeSlashCommands(ctx context.Context) ([]core.NativeSlashCommand, error) {
	a.mu.RLock()
	command := a.cmd
	extraArgs := append([]string(nil), a.cliExtraArgs...)
	workDir := a.workDir
	effectiveEnv := a.effectiveEnvLocked()
	runner := a.inspectRunner
	a.mu.RUnlock()

	workDir = normalizeGrokCapabilityWorkDir(workDir)
	env := grokProcessEnv(effectiveEnv)
	if runner == nil {
		runner = runBoundedGrokCommand
	}

	// Inspect supplies descriptions and is also the compatibility fallback for
	// older Grok builds that do not emit slash_commands in the init frame.
	inspectCtx, cancelInspect := context.WithTimeout(ctx, grokInspectTimeout)
	inspectOut, inspectErr := runner(inspectCtx, command,
		append(append([]string(nil), extraArgs...), "--no-auto-update", "inspect", "--json"),
		workDir, env)
	cancelInspect()
	inspectSkills, inspectParseErr := parseGrokInspectSkills(inspectOut)

	probeSessionID := uuid.NewString()
	probeHome := resolveGrokHome(effectiveEnv, workDir)
	for findGrokSessionDir(probeHome, workDir, probeSessionID) != "" {
		probeSessionID = uuid.NewString()
	}
	probeArgs := append(append([]string(nil), extraArgs...),
		"--no-auto-update",
		"--cwd", workDir,
		"--permission-mode", "plan",
		"--output-format", "streaming-messages-json",
		"--session-id", probeSessionID,
		"-p", "/session-info",
	)
	probeCtx, cancelProbe := context.WithTimeout(ctx, grokSlashProbeTimeout)
	probeOut, _ := runner(probeCtx, command, probeArgs, workDir, env)
	probeCtxErr := probeCtx.Err()
	cancelProbe()
	a.cleanupSlashProbeSession(runner, command, extraArgs, workDir, env, probeHome, probeSessionID)

	initFrame, initErr := parseGrokSlashInit(probeOut)
	if initErr == nil && initFrame.SessionID == probeSessionID {
		return nativeCommandsFromGrokInit(initFrame, inspectSkills), nil
	}

	// Never include stderr, raw stdout, command arguments, paths, or injected
	// runner error text in the returned error: all can carry private data.
	if inspectErr == nil && inspectParseErr == nil {
		return nativeCommandsFromInspect(inspectSkills), nil
	}
	if errors.Is(probeCtxErr, context.DeadlineExceeded) {
		return nil, errors.New("grok slash command discovery timed out")
	}
	if errors.Is(probeCtxErr, context.Canceled) {
		return nil, errors.New("grok slash command discovery canceled")
	}
	return nil, errors.New("grok slash command discovery failed")
}

func (a *Agent) cleanupSlashProbeSession(runner grokInspectRunner, command string, extraArgs []string, workDir string, env []string, home, sessionID string) {
	existed := findGrokSessionDir(home, workDir, sessionID) != ""
	cleanupCtx, cancel := context.WithTimeout(context.Background(), grokProbeCleanupTimeout)
	defer cancel()
	args := append(append([]string(nil), extraArgs...), "--no-auto-update", "sessions", "delete", sessionID)
	if _, err := runner(cleanupCtx, command, args, workDir, env); err != nil && existed {
		// Deliberately omit the underlying error and session ID; runner errors
		// and CLI output may contain credentials or private filesystem paths.
		slog.Warn("grok: native command probe session cleanup failed")
	}
}

// boundedBuffer keeps subprocess output memory-bounded while reporting all
// bytes as consumed so a verbose CLI cannot deadlock on a full pipe.
type boundedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	written := len(p)
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.buf.Write(p)
	}
	return written, nil
}

func runBoundedGrokCommand(ctx context.Context, command string, args []string, workDir string, env []string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.Command(command, args...)
	cmd.Dir = workDir
	cmd.Env = env
	cmd.Stderr = io.Discard
	prepareCmdForKill(cmd)
	cmd.WaitDelay = grokCommandWaitDelay

	stdout := &boundedBuffer{limit: grokCommandOutputLimit}
	cmd.Stdout = stdout
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	select {
	case err := <-waitDone:
		return stdout.buf.Bytes(), err
	case <-ctx.Done():
		_ = forceKillCmd(cmd)
		select {
		case <-waitDone:
			return stdout.buf.Bytes(), ctx.Err()
		case <-time.After(grokCommandWaitDelay):
			// The process tree failed to terminate. Do not read or return the
			// buffer while the orphaned copy goroutine may still be writing it.
			return nil, ctx.Err()
		}
	}
}

func parseGrokSlashInit(data []byte) (grokSlashInit, error) {
	var fallback grokSlashInit
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		var frame grokSlashInit
		if err := decoder.Decode(&frame); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fallback, err
		}
		if frame.SessionID != "" && fallback.SessionID == "" {
			fallback.SessionID = frame.SessionID
		}
		if frame.Type == "system" && frame.Subtype == "init" && frame.SlashCommands != nil && frame.Skills != nil {
			return frame, nil
		}
	}
	return fallback, errors.New("grok init frame not found")
}

func parseGrokInspectSkills(data []byte) ([]grokInspectSkill, error) {
	var document grokInspectDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	if document.Skills == nil {
		return nil, errors.New("grok inspect skills field missing")
	}
	result := make([]grokInspectSkill, 0, len(*document.Skills))
	for _, skill := range *document.Skills {
		if strings.TrimSpace(skill.Name) == "" || !skill.UserInvocable || (skill.Enabled != nil && !*skill.Enabled) {
			continue
		}
		result = append(result, skill)
	}
	return result, nil
}

func nativeCommandsFromGrokInit(initFrame grokSlashInit, inspectSkills []grokInspectSkill) []core.NativeSlashCommand {
	skills := *initFrame.Skills
	slashCommands := *initFrame.SlashCommands
	skillSet := make(map[string]bool, len(skills))
	descriptions := make(map[string]string, len(skills))
	for _, name := range skills {
		name = strings.TrimSpace(name)
		if name != "" {
			skillSet[name] = true
		}
	}
	// Grok 0.2.118 emits inspect's invocable skills in the same order as the
	// already-qualified init skills. Only trust this association on exact
	// cardinality; otherwise use generic menu descriptions.
	if len(skills) == len(inspectSkills) {
		for i, name := range skills {
			descriptions[strings.TrimSpace(name)] = strings.TrimSpace(inspectSkills[i].Description)
		}
	}

	seen := make(map[string]bool)
	commands := make([]core.NativeSlashCommand, 0, len(slashCommands))
	for _, target := range slashCommands {
		target = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(target), "/"))
		seenKey := strings.ToLower(target)
		if target == "" || strings.ContainsAny(target, " \t\r\n") || seen[seenKey] {
			continue
		}
		seen[seenKey] = true
		commands = append(commands, core.NativeSlashCommand{
			Name:               target,
			Target:             target,
			Description:        descriptions[target],
			IsSkill:            skillSet[target],
			AdminOnly:          grokNativeCommandRequiresAdmin(target),
			PolicyCommand:      grokNativePolicyCommand(target),
			ReplacementCommand: grokNativeReplacementCommand(target),
		})
	}
	return commands
}

func grokNativeReplacementCommand(target string) string {
	switch strings.ToLower(target) {
	case "always-approve":
		return "mode yolo"
	case "auto":
		return "mode auto"
	case "compact":
		return "compress"
	case "skill":
		return "skills"
	default:
		return ""
	}
}

func grokNativeCommandRequiresAdmin(target string) bool {
	switch strings.ToLower(target) {
	case "always-approve", "auto", "hooks-trust", "hooks-list", "hooks-add", "hooks-remove", "hooks-untrust", "plugins", "reload-plugins", "feedback":
		return true
	default:
		return false
	}
}

func grokNativePolicyCommand(target string) string {
	switch strings.ToLower(target) {
	case "always-approve", "auto":
		return "mode"
	case "compact":
		return "compress"
	case "skill":
		return "skills"
	default:
		return ""
	}
}

// NativeSlashPolicy exposes stable policy metadata for built-in Grok commands
// without depending on a successful live discovery. This keeps a filesystem
// fallback with the same name from bypassing core policy while Grok is
// temporarily unavailable.
func (a *Agent) NativeSlashPolicy(name string) (core.NativeSlashCommand, bool) {
	target := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(name), "/")))
	target = strings.ReplaceAll(target, "_", "-")
	adminOnly := grokNativeCommandRequiresAdmin(target)
	policyCommand := grokNativePolicyCommand(target)
	if !adminOnly && policyCommand == "" {
		return core.NativeSlashCommand{}, false
	}
	return core.NativeSlashCommand{
		Name:               target,
		Target:             target,
		AdminOnly:          adminOnly,
		PolicyCommand:      policyCommand,
		ReplacementCommand: grokNativeReplacementCommand(target),
	}, true
}

func nativeCommandsFromInspect(skills []grokInspectSkill) []core.NativeSlashCommand {
	seen := make(map[string]bool)
	commands := make([]core.NativeSlashCommand, 0, len(skills))
	for _, skill := range skills {
		name := strings.TrimSpace(skill.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		path := filepath.ToSlash(strings.ReplaceAll(skill.Source.Path, `\`, "/"))
		commands = append(commands, core.NativeSlashCommand{
			Name:               name,
			Target:             name,
			Description:        strings.TrimSpace(skill.Description),
			IsSkill:            strings.EqualFold(filepath.Base(path), "SKILL.md"),
			AdminOnly:          grokNativeCommandRequiresAdmin(name),
			PolicyCommand:      grokNativePolicyCommand(name),
			ReplacementCommand: grokNativeReplacementCommand(name),
		})
	}
	return commands
}

// parseGrokNativeSlashCommands is kept small and deterministic for fixtures
// that exercise inspect-only compatibility behavior.
func parseGrokNativeSlashCommands(data []byte) ([]core.NativeSlashCommand, error) {
	skills, err := parseGrokInspectSkills(data)
	if err != nil {
		return nil, err
	}
	return nativeCommandsFromInspect(skills), nil
}
