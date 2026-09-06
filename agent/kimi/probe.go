package kimi

import (
	"bytes"
	"context"
	"log/slog"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
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
//	The same CLI release also dropped `--work-dir` (the working directory is
//	now taken from the process cwd only) and rejects the flag with `error:
//	unknown option --work-dir`, and dropped `--quiet` as well. The older
//	kimi-cli still requires `--print` for `--output-format` to take effect
//	and exposes `--work-dir` for non-default workspace locations. We probe
//	the help text once and adapt accordingly. See #1456, #1476, #1561.
type kimiFlagSupport struct {
	Print   bool
	WorkDir bool
	Quiet   bool
}

// isModernFlavor reports whether the probed binary speaks the newer Kimi
// Code CLI dialect. `--print` is the family discriminator established in
// #1456: every Kimi Code CLI build drops it, every legacy kimi-cli build
// advertises it. Dialect-level differences that cannot be help-probed
// reliably (e.g. `--resume` vs `-r`, which legacy kimi-cli accepts but does
// not always advertise) branch on this. See #1561.
func (s kimiFlagSupport) isModernFlavor() bool {
	return !s.Print
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
		Print:   flags["--print"],
		WorkDir: flags["--work-dir"],
		Quiet:   flags["--quiet"],
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

// probeKimiModels discovers the set of model identifiers the locally installed
// kimi-code CLI knows about, so that the /model picker tracks the binary
// instead of drifting every release. See #1795.
//
// Strategy: the modern kimi-code CLI (0.38+) exposes a `kimi model list`
// (and sometimes `kimi models`) subcommand that prints model identifiers.
// We probe both shapes with a short timeout; if either returns parseable
// content we return it, otherwise the caller falls back to the built-in
// list. Older / legacy kimi-cli builds (#1456) don't have these subcommands,
// so the probe naturally returns nil and the built-in list is used — that
// matches the reporter's expectation that the hardcoded list should still be
// the safety net for older CLIs.
//
// The probe is intentionally tolerant: a parse that returns 0 entries is
// treated identically to a CLI that timed out, so a future layout tweak in
// kimi-code (e.g. changing "kimi model list" to "kimi models ls") won't
// silently break the picker — the failure path stays predictable.
func probeKimiModels(parent context.Context, cmd string, timeout time.Duration) []core.ModelOption {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	candidates := [][]string{
		{"model", "list"},
		{"models"},
		{"models", "list"},
	}

	for _, args := range candidates {
		models := runKimiModelProbe(parent, cmd, args, timeout)
		if len(models) > 0 {
			slog.Debug("kimi: model probe succeeded",
				"cmd", cmd, "args", strings.Join(args, " "), "count", len(models))
			return models
		}
	}

	slog.Debug("kimi: model probe returned nothing, falling back to built-in list", "cmd", cmd)
	return nil
}

// runKimiModelProbe executes a single `<cmd> <args...>` attempt and parses
// its combined stdout+stderr into model identifiers. Returns nil when the
// subcommand is missing (non-zero exit) or the output is unparseable; the
// caller treats both cases the same and tries the next candidate.
func runKimiModelProbe(parent context.Context, cmd string, args []string, timeout time.Duration) []core.ModelOption {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	c := exec.CommandContext(ctx, cmd, args...)
	var out bytes.Buffer
	c.Stdout = &out
	c.Stderr = &out
	if err := c.Run(); err != nil {
		slog.Debug("kimi: model probe attempt failed",
			"cmd", cmd, "args", strings.Join(args, " "), "error", err)
		return nil
	}
	return parseKimiModelList(out.String())
}

// modelIdentRe matches plausible model identifiers in the kimi-code CLI's
// `kimi model list` output. We deliberately accept a fairly broad shape
// (lowercase letters, digits, dashes, underscores, dots, slashes) so that
// both compact aliases (k3, k3-256k) and explicit namespaced forms
// (kimi-code/k3, kimi-k2-6-preview) parse correctly. The probe output for
// #1795 has not been pinned to a single layout yet, so the parser must
// stay tolerant.
var modelIdentRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._/\-]{0,63}$`)

// modelIdentMustContainRe enforces a stronger non-letter disambiguator
// inside any candidate identifier. We require a digit, dash, or slash —
// the three characters that appear in every real Kimi model identifier
// (k3, k3-256k, kimi-k2-6-preview, kimi-code/k3) but never appear inside
// the English header / footer tokens the CLI might emit around the model
// list ("switch.", "details.", "Run", "Recently"). Dot and underscore
// alone are not sufficient because they slip into sentences ending in
// "." or fields like "see_help".
var modelIdentMustContainRe = regexp.MustCompile(`[0-9/\-]`)

// parseKimiModelList extracts model options from the stdout of `kimi model
// list` (or one of the other probe shapes). The exact format is not yet
// pinned (#1795): modern kimi-code 0.38+ may emit one model per line, with
// or without a description; legacy formats may have headers like
// "Available models:" followed by a usage hint line ("Run 'kimi help'
// …"). To stay robust we:
//
//  1. Drop comment and "Usage" header lines outright.
//  2. On every remaining line, take the first whitespace-delimited token
//     that looks like a real model identifier as the Name.
//  3. Treat the rest of the line (everything after the identifier, joined
//     with single spaces, with edge punctuation trimmed) as the
//     Description, but only if it's non-empty and not a stop word.
//  4. Deduplicate by Name; the first occurrence wins, matching the way
//     humans read top-down.
//
// The function is deterministic and side-effect free so it can be unit
// tested against a variety of synthetic probe outputs without touching
// the real CLI.
func parseKimiModelList(output string) []core.ModelOption {
	seen := make(map[string]bool)
	var models []core.ModelOption

	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "Usage") {
			continue
		}

		fields := strings.Fields(line)
		var id string
		idIdx := -1
		for i, field := range fields {
			field = strings.Trim(field, ",;:. ")
			if looksLikeModelIdentifier(field) {
				id = field
				idIdx = i
				break
			}
		}
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true

		var desc string
		if idIdx >= 0 && idIdx+1 < len(fields) {
			rest := strings.Join(fields[idIdx+1:], " ")
			rest = strings.TrimLeft(rest, " \t,;:[")
			rest = strings.TrimRight(rest, " \t,;.]")
			rest = strings.TrimSpace(rest)
			if rest != "" && !isModelListStopWord(rest) {
				desc = rest
			}
		}
		models = append(models, core.ModelOption{Name: id, Desc: desc})
	}
	return models
}

// looksLikeModelIdentifier returns true when s has the shape we expect for a
// kimi-code model identifier (k3, k3-256k, kimi-k2-6-preview, kimi-code/k3,
// …). Two filters must both pass:
//
//  1. modelIdentRe: matches the broad character class (leading letter,
//     allowed body of letters/digits/dots/underscores/dashes/slashes,
//     reasonable length cap).
//  2. modelIdentMustContainRe: requires at least one digit, dot,
//     underscore, dash, or slash inside the token. This rejects pure-
//     alphabetic English words like "Available", "Default", "Run" that
//     would otherwise slip through filter 1. Kimi's actual model
//     identifiers always carry one of those disambiguators.
func looksLikeModelIdentifier(s string) bool {
	if len(s) < 2 || len(s) > 64 {
		return false
	}
	if !modelIdentRe.MatchString(s) {
		return false
	}
	return modelIdentMustContainRe.MatchString(s)
}

// isModelListStopWord returns true for short phrases that may appear as the
// "rest of the line" after an identifier on a probe row but don't add
// information to the picker. Matching is case-insensitive and tolerates
// trailing punctuation.
func isModelListStopWord(s string) bool {
	switch strings.ToLower(strings.TrimRight(s, ":.")) {
	case "available", "model", "models", "alias", "aliases",
		"provider", "default", "name", "id", "description", "desc":
		return true
	}
	return false
}
