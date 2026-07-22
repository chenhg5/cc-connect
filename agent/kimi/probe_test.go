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
// removed entirely. This is the surface that triggers #1456 today. As of the
// build tested in #1476, the CLI also still advertises --work-dir here (we
// keep this constant realistic so #1456 tests stay anchored to a known CLI
// surface); see modernKimiHelpWithoutWorkDir below for the deeper-no-work-dir
// surface that triggers #1476.
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

// modernKimiHelpWithoutWorkDir models the Kimi Code CLI build the #1476
// reporter hits: --print is gone, --work-dir is gone too. The CLI now takes
// the working directory from the process cwd, so passing --work-dir must be
// avoided entirely (it triggers `error: unknown option --work-dir`).
const modernKimiHelpWithoutWorkDir = `
 Usage: kimi [OPTIONS] COMMAND [ARGS]...

 Kimi, your next CLI agent.

 Options:
   -V, --version              Show version and exit.
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
	assert.True(t, flags["--work-dir"], "legacy help text advertises --work-dir (#1476)")
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
	assert.True(t, flags["--work-dir"], "this modern CLI build still advertises --work-dir")
	assert.True(t, flags["--prompt"], "modern CLI still advertises --prompt")
	assert.True(t, flags["--output-format"])
}

// TestParseKimiHelpFlags_ModernWithoutWorkDir is the regression test for
// #1476: when the installed Kimi Code CLI build no longer advertises
// --work-dir, parseKimiHelpFlags must NOT detect it, so the probe can drive
// buildArgs to omit --work-dir entirely (the CLI then takes the cwd from
// exec.Command.Dir, which the Go process already sets correctly).
func TestParseKimiHelpFlags_ModernWithoutWorkDir(t *testing.T) {
	flags := parseKimiHelpFlags(modernKimiHelpWithoutWorkDir)
	assert.False(t, flags["--work-dir"],
		"modern CLI without --work-dir must NOT advertise it (#1476)")
	assert.False(t, flags["--print"],
		"modern CLI still without --print (#1456 compatibility)")
	assert.True(t, flags["--prompt"], "still advertises --prompt")
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

// TestKimiFlagSupport_LegacyHelpSetsWorkDir verifies the full probe-mapping
// for the legacy help surface: both Print and WorkDir must come back true so
// existing kimi-cli users keep receiving --work-dir.
func TestKimiFlagSupport_LegacyHelpSetsWorkDir(t *testing.T) {
	flags := parseKimiHelpFlags(legacyKimiHelp)
	assert.True(t, flags["--work-dir"], "legacy help advertises --work-dir")
	support := kimiFlagSupport{
		Print:   flags["--print"],
		WorkDir: flags["--work-dir"],
	}
	assert.True(t, support.Print)
	assert.True(t, support.WorkDir, "modern probe must surface work-dir support")
}

// TestKimiFlagSupport_ModernWithoutWorkDir verifies the full probe-mapping
// for the #1476 surface: WorkDir must come back false so buildArgs can drop
// the flag.
func TestKimiFlagSupport_ModernWithoutWorkDir(t *testing.T) {
	flags := parseKimiHelpFlags(modernKimiHelpWithoutWorkDir)
	support := kimiFlagSupport{
		Print:   flags["--print"],
		WorkDir: flags["--work-dir"],
	}
	assert.False(t, support.Print)
	assert.False(t, support.WorkDir,
		"probe must NOT surface work-dir support on CLI builds that dropped it (#1476)")
}
