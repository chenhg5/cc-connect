package kimi

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// legacyKimiHelp is a representative slice of the older kimi-cli `--help`
// output (still on the kimi-cli `main` branch as of 2026). It advertises
// both `--print` and `--prompt`.
const legacyKimiHelp = `
 Usage: kimi [OPTIONS] COMMAND [ARGS]...

 Kimi, your next CLI agent.

 ╭─ Options ──────────────────────────────────────────────────────────────╮
 │ --version          -V          Show version and exit.                  │
 │ --work-dir         -w DIRECTORY Working directory for the agent.       │
 │ --session          -S [ID]     Resume a session.                       │
 │ --continue         -C          Continue the previous session.          │
 │ --model            -m TEXT     LLM model to use.                       │
 │ --thinking/--no-thinking       Enable thinking mode.                   │
 │ --yolo             -y          Automatically approve all actions.      │
 │ --plan                         Start in plan mode.                     │
 │ --prompt           -p TEXT     User prompt to the agent.               │
 │ --print                        Run in print mode (non-interactive).    │
 │ --output-format    FORMAT      Output format to use.                   │
 │ --quiet                        Alias for --print --output-format text. │
 ╰────────────────────────────────────────────────────────────────────────╯
`

// modernKimiHelp emulates the newer Kimi Code CLI, where --print has been
// removed entirely. This is the surface that triggers #1456 today.
const modernKimiHelp = `
 Usage: kimi [OPTIONS] COMMAND [ARGS]...

 Kimi, your next CLI agent.

 Options:
   -V, --version              Show version and exit.
   -w, --work-dir DIRECTORY   Working directory for the agent.
   -S, --session [ID]         Resume a session.
   -c, --continue             Continue the most recent session.
   -m, --model TEXT           LLM model to use.
   -p, --prompt TEXT          Run a single prompt non-interactively.
   --output-format FORMAT     Non-interactive output format.
   -y, --yolo                 Auto-approve regular tool calls.
   --plan                     Start a new session in Plan mode.
`

func TestParseKimiHelpFlags_LegacyAdvertisesPrint(t *testing.T) {
	flags := parseKimiHelpFlags(legacyKimiHelp)
	assert.True(t, flags["--print"], "legacy help text advertises --print")
	assert.True(t, flags["--prompt"], "legacy help text advertises --prompt")
	assert.True(t, flags["--output-format"])
	assert.True(t, flags["--plan"])
	assert.True(t, flags["--thinking"], "alias-split should pick up --thinking")
	assert.True(t, flags["--no-thinking"], "alias-split should pick up --no-thinking")
}

func TestParseKimiHelpFlags_ModernHidesPrint(t *testing.T) {
	flags := parseKimiHelpFlags(modernKimiHelp)
	// Regression for #1456: the new Kimi Code CLI must not be detected
	// as supporting --print.
	assert.False(t, flags["--print"], "modern help text must not advertise --print")
	assert.True(t, flags["--prompt"], "modern CLI still advertises --prompt")
	assert.True(t, flags["--output-format"])
}

func TestParseKimiHelpFlags_IgnoresPositionalAndShortOnly(t *testing.T) {
	help := `
  Arguments:
    COMMAND   Optional sub-command

  Options:
    -h        Show short-only help (must be ignored)
    --        Bare double-dash (must be ignored)
    --debug   Toggle debug mode
`
	flags := parseKimiHelpFlags(help)
	assert.True(t, flags["--debug"])
	assert.False(t, flags["--"], "bare -- must not be treated as a flag")
	assert.False(t, flags["-h"], "short-only flags are not in the long-flag set")
}

func TestProbeKimiFlags_FallbackOnMissingBinary(t *testing.T) {
	// A binary that almost certainly does not exist. probeKimiFlags must
	// not panic and must return the zero-value (modern-CLI default).
	got := probeKimiFlags(context.Background(),
		"kimi-binary-that-does-not-exist-1456-test",
		200*time.Millisecond)
	assert.Equal(t, kimiFlagSupport{}, got)
}
