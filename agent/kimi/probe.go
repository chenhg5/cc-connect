package kimi

import (
	"bytes"
	"context"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// kimiFlagSupport records which optional CLI flags the locally installed Kimi
// binary understands. It is populated once at Agent construction by probing
// `kimi --help` so that buildArgs can adapt to CLI versions that have added
// or removed flags.
//
// Why this exists:
//
//	The newer Kimi Code CLI removed the standalone `--print` flag — passing it
//	now produces: `error: unknown option '--print' (Did you mean --prompt?)`.
//	The older kimi-cli still requires `--print` for `--output-format` to take
//	effect. We probe the help text once and adapt accordingly. See #1456.
type kimiFlagSupport struct {
	Print bool
}

// probeKimiFlags runs `<cmd> --help` with a short timeout and returns the
// detected flag-support set. If the probe fails (binary missing, timeout,
// non-zero exit, unrecognisable output) the returned struct has all fields
// false — i.e. we conservatively assume the newer CLI surface, which matches
// the direction Kimi Code CLI is moving and avoids the hard-failure mode of
// passing `--print` to a CLI that no longer accepts it.
func probeKimiFlags(parent context.Context, cmd string, timeout time.Duration) kimiFlagSupport {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	c := exec.CommandContext(ctx, cmd, "--help")
	var out bytes.Buffer
	c.Stdout = &out
	c.Stderr = &out
	if err := c.Run(); err != nil {
		slog.Debug("kimi: flag probe failed, assuming modern CLI surface",
			"cmd", cmd, "error", err)
		return kimiFlagSupport{}
	}

	flags := parseKimiHelpFlags(out.String())
	support := kimiFlagSupport{
		Print: flags["--print"],
	}
	slog.Debug("kimi: flag probe complete", "cmd", cmd, "support", support)
	return support
}

// parseKimiHelpFlags scans the output of `kimi --help` and returns the set of
// long flag names (`--xxx`) it advertises. It handles the two common option-
// table layouts Kimi has shipped:
//
//	Typer/click box style:   "│ --print            Run in print mode (...) │"
//	Standard click style:    "  -p, --prompt TEXT  User prompt (...)"
//	Slash aliases:           "  --thinking/--no-thinking   Enable thinking."
//
// To stay robust against future layout tweaks the parser only treats a line
// as a flag-definition line when one of its first two whitespace-separated
// tokens starts with `--`; description prose later in the line is ignored.
func parseKimiHelpFlags(helpText string) map[string]bool {
	flags := make(map[string]bool)
	for _, rawLine := range strings.Split(helpText, "\n") {
		line := strings.TrimLeft(rawLine, " \t│|*")
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		// Look at up to the first two tokens so `-p, --prompt …` works
		// alongside `--print …`. Anything past that is help-text prose.
		for i := 0; i < len(fields) && i < 2; i++ {
			tok := strings.TrimRight(fields[i], ",")
			if !strings.HasPrefix(tok, "--") {
				continue
			}
			for _, name := range strings.Split(tok, "/") {
				name = strings.TrimSpace(name)
				name = strings.TrimRight(name, ",")
				if !strings.HasPrefix(name, "--") || len(name) <= 2 {
					continue
				}
				flags[name] = true
			}
		}
	}
	return flags
}
