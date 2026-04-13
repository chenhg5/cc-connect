//go:build windows

package core

import (
	"context"
	"errors"
	"os/exec"
)

// DefaultEnvAllowlist is a stub on Windows — run_as_user is not supported.
var DefaultEnvAllowlist = []string{}

// SpawnOptions is a stub on Windows.
type SpawnOptions struct {
	RunAsUser    string
	EnvAllowlist []string
}

// IsolationMode always returns false on Windows.
func (o SpawnOptions) IsolationMode() bool { return false }

// BuildSpawnCommand on Windows ignores RunAsUser (config validation rejects
// it before we get here) and delegates to exec.CommandContext.
func BuildSpawnCommand(ctx context.Context, _ SpawnOptions, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}

// FilterEnvForSpawn on Windows is a no-op pass-through.
func FilterEnvForSpawn(env []string, _ SpawnOptions) []string { return env }

// SudoRunner is a stub interface on Windows for API compatibility.
type SudoRunner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// ExecSudoRunner is a stub on Windows.
type ExecSudoRunner struct{}

// Run always fails on Windows.
func (ExecSudoRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	return nil, errors.New("sudo is not supported on Windows")
}

// VerifyRunAsUserCheap always fails on Windows.
func VerifyRunAsUserCheap(_ context.Context, _ SudoRunner, runAsUser string) error {
	if runAsUser == "" {
		return nil
	}
	return errors.New("run_as_user is not supported on Windows")
}
