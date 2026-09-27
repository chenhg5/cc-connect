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

// kimiCodeHelpV026 is the verbatim `kimi --help` output of the Kimi Code CLI
// v0.26.0 (Node.js, commander.js layout) — the build whose incompatibility
// with the kimi agent dialect is tracked in #1561. It advertises neither
// --print, nor --work-dir, nor --quiet, nor --resume.
const kimiCodeHelpV026 = `
Usage: kimi [options] [command]

The Starting Point for Next-Gen Agents

Options:
  -V, --version                 output the version number
  -S, --session [id]            Resume a session. With ID: resume that session. Without ID:
                                interactively pick.
  -c, --continue                Continue the previous session for the working directory. (default:
                                false)
  -y, --yolo                    Automatically approve all actions. (default: false)
  --auto                        Start in auto permission mode. (default: false)
  -m, --model <model>           LLM model alias to use for this invocation. Defaults to
                                default_model in config.toml.
  -p, --prompt <prompt>         Run one prompt non-interactively and print the response.
  --output-format <format>      Output format for prompt mode. Defaults to text. (choices: "text",
                                "stream-json")
  --skills-dir <dir>            Load skills from this directory instead of auto-discovered user and
                                project directories. Can be repeated. (default: [])
  --add-dir <dir>               Add an additional workspace directory for this session. Can be
                                repeated. (default: [])
  --plan                        Start in plan mode. (default: false)
  -h, --help                    Show help.

Commands:
  export [options] [sessionId]  Export a session as a ZIP archive.
  provider                      Manage LLM providers non-interactively.
  acp [options]                 Run kimi-code as an Agent Client Protocol (ACP) server over stdio.
`

// TestParseKimiHelpFlags_KimiCodeV026 anchors the probe to the real Kimi
// Code CLI v0.26.0 help surface (#1561): none of the legacy-only flags may
// be detected.
func TestParseKimiHelpFlags_KimiCodeV026(t *testing.T) {
	flags := parseKimiHelpFlags(kimiCodeHelpV026)
	assert.False(t, flags["--print"], "kimi-code does not advertise --print")
	assert.False(t, flags["--work-dir"], "kimi-code does not advertise --work-dir")
	assert.False(t, flags["--quiet"], "kimi-code does not advertise --quiet")
	assert.False(t, flags["--resume"], "kimi-code does not advertise --resume")
	assert.True(t, flags["--prompt"], "kimi-code advertises --prompt")
	assert.True(t, flags["--output-format"], "kimi-code advertises --output-format")
	assert.True(t, flags["--plan"], "kimi-code advertises --plan")
	assert.True(t, flags["--session"], "kimi-code advertises --session")
}

// TestKimiFlagSupport_QuietMapping covers the --quiet probe-mapping added in
// #1561: legacy kimi-cli advertises --quiet, Kimi Code CLI does not.
func TestKimiFlagSupport_QuietMapping(t *testing.T) {
	legacy := parseKimiHelpFlags(legacyKimiHelp)
	assert.True(t, legacy["--quiet"], "legacy help advertises --quiet")

	modern := parseKimiHelpFlags(kimiCodeHelpV026)
	assert.False(t, modern["--quiet"], "kimi-code help must not advertise --quiet")
}

// TestKimiFlagSupport_IsModernFlavor pins the family discriminator: presence
// of --print means legacy kimi-cli dialect, absence means Kimi Code dialect.
func TestKimiFlagSupport_IsModernFlavor(t *testing.T) {
	assert.False(t, kimiFlagSupport{Print: true}.isModernFlavor(),
		"--print advertised => legacy kimi-cli flavor")
	assert.True(t, kimiFlagSupport{}.isModernFlavor(),
		"no --print (incl. probe failure fallback) => modern Kimi Code flavor")
}
