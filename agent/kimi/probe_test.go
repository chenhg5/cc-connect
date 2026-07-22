package kimi

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// legacyKimiHelp is a representative slice of the older kimi-cli `--help`
// output (still on the kimi-cli `main` branch as of 2026). It advertises
// `--print`, `--work-dir`, `--resume` and `--quiet` alongside `--prompt`.
const legacyKimiHelp = `
 Usage: kimi [OPTIONS] COMMAND [ARGS]...

 Kimi, your next CLI agent.

 ╭─ Options ──────────────────────────────────────────────────────────────╮
 │ --version          -V          Show version and exit.                  │
 │ --work-dir         -w DIRECTORY Working directory for the agent.       │
 │ --session          -S [ID]     Resume a session.                       │
 │ --resume           -r ID       Resume a session by ID.                 │
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

// modernKimiHelp is the real `kimi --help` output of Kimi Code CLI 0.28.1.
// It has no --print, no --work-dir, no --resume and no --quiet; session
// resume is done via the hidden `-r` alias (the CLI's own resume_hint
// meta line says `kimi -r <id>`).
const modernKimiHelp = `
Usage: kimi [options] [command]
Options:
  -V, --version
  -S, --session [id]            Resume a session. With ID: resume that session. Without ID: interactively pick.
  -c, --continue                Continue the previous session for the working directory.
  -y, --yolo                    Auto-approve regular tool calls
  --auto                        Start in auto permission mode
  -m, --model <model>           LLM model alias
  -p, --prompt <prompt>         Run one prompt non-interactively and print the response.
  --output-format <format>      (choices: "text", "stream-json")
  --skills-dir <dir>
  --add-dir <dir>
  --plan                        Start in plan mode.
  -h, --help
`

func TestParseKimiHelpFlags_LegacyAdvertisesPrint(t *testing.T) {
	flags := parseKimiHelpFlags(legacyKimiHelp)
	assert.True(t, flags["--print"], "legacy help text advertises --print")
	assert.True(t, flags["--prompt"], "legacy help text advertises --prompt")
	assert.True(t, flags["--work-dir"], "legacy help text advertises --work-dir")
	assert.True(t, flags["--resume"], "legacy help text advertises --resume")
	assert.True(t, flags["--quiet"], "legacy help text advertises --quiet")
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
	// 0.28.1 also dropped these flags; passing them is a hard error.
	assert.False(t, flags["--work-dir"], "0.28.1 does not advertise --work-dir")
	assert.False(t, flags["--resume"], "0.28.1 does not advertise --resume")
	assert.False(t, flags["--quiet"], "0.28.1 does not advertise --quiet")
	assert.True(t, flags["--prompt"], "modern CLI still advertises --prompt")
	assert.True(t, flags["--output-format"])
	assert.True(t, flags["--plan"])
	assert.True(t, flags["--help"])
	assert.True(t, flags["--add-dir"])
	assert.True(t, flags["--skills-dir"])
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
