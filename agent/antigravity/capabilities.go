package antigravity

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// agy only learned --model and --effort in later releases; an older CLI rejects
// the unknown flag and the whole turn fails. Rather than mapping version
// numbers to features, ask the installed CLI what it accepts: its --help text
// is the ground truth for both the flags and the effort values.

const (
	agyProbeTimeout = 10 * time.Second
	// A failed probe is retried after this delay instead of being cached for
	// the lifetime of the process, so a transient failure does not disable
	// model selection until restart.
	agyProbeRetryAfter = time.Minute
)

// agyFallbackEfforts is used when agy accepts --effort but its help text does
// not list the values in a form we can parse. Kept deliberately narrow.
var agyFallbackEfforts = []string{"low", "medium", "high"}

// agyCapabilities describes what the installed agy CLI accepts.
type agyCapabilities struct {
	probed  bool     // false: the probe itself failed, nothing is known
	model   bool     // --model is accepted
	effort  bool     // --effort is accepted
	efforts []string // values listed for --effort; empty if not listed
}

// supportedEfforts returns the effort values that are safe to pass.
func (c agyCapabilities) supportedEfforts() []string {
	if !c.effort {
		return nil
	}
	if len(c.efforts) > 0 {
		return append([]string(nil), c.efforts...)
	}
	return append([]string(nil), agyFallbackEfforts...)
}

func (c agyCapabilities) supportsEffort(effort string) bool {
	return effort != "" && slices.Contains(c.supportedEfforts(), effort)
}

type agyCapEntry struct {
	mu      sync.Mutex
	done    bool
	caps    agyCapabilities
	retryAt time.Time
}

// The cache key is the command line only; it ignores env (e.g. a PATH set via
// options.env), so the first probe for a command line wins.
var (
	agyCapMu    sync.Mutex
	agyCapCache = map[string]*agyCapEntry{}
)

// agyCapabilitiesFor probes "<cmd> <extraArgs...> --help" at most once per
// process for a given command line and caches the result. cmd and extraArgs
// are the same pair used to launch turns, so wrappers such as
// "timeout 3600 agy" and custom binary paths are probed the way they are run.
func agyCapabilitiesFor(ctx context.Context, cmd string, extraArgs, env []string) agyCapabilities {
	key := cmd + "\x00" + strings.Join(extraArgs, "\x00")
	agyCapMu.Lock()
	entry := agyCapCache[key]
	if entry == nil {
		entry = &agyCapEntry{}
		agyCapCache[key] = entry
	}
	agyCapMu.Unlock()

	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.done && (entry.caps.probed || time.Now().Before(entry.retryAt)) {
		return entry.caps
	}

	caps, err := probeAgyCapabilities(ctx, cmd, extraArgs, env)
	if err != nil && ctx.Err() != nil {
		// The caller went away (session closed mid-probe); that says nothing
		// about the CLI, so do not cache it.
		return caps
	}
	if err != nil {
		slog.Warn("antigravity: could not probe agy capabilities, --model and --effort will not be passed",
			"cmd", cmd, "error", err)
		entry.retryAt = time.Now().Add(agyProbeRetryAfter)
	} else {
		slog.Debug("antigravity: probed agy capabilities",
			"cmd", cmd, "model", caps.model, "effort", caps.effort, "efforts", caps.efforts)
	}
	entry.caps = caps
	entry.done = true
	return caps
}

func probeAgyCapabilities(ctx context.Context, cmd string, extraArgs, env []string) (agyCapabilities, error) {
	if cmd == "" {
		return agyCapabilities{}, fmt.Errorf("no agy command configured")
	}
	ctx, cancel := context.WithTimeout(ctx, agyProbeTimeout)
	defer cancel()

	args := append(append([]string{}, extraArgs...), "--help")
	c := exec.CommandContext(ctx, cmd, args...)
	c.Env = env
	c.WaitDelay = time.Second
	// Some CLIs print help on stderr or exit non-zero for --help, so read
	// both streams and judge by the content.
	out, err := c.CombinedOutput()
	caps := parseAgyHelp(string(out))
	if err != nil && !caps.model && !caps.effort {
		return agyCapabilities{}, fmt.Errorf("%s --help: %w", cmd, err)
	}
	return caps, nil
}

// parseAgyHelp extracts flag support from agy's help text.
func parseAgyHelp(help string) agyCapabilities {
	caps := agyCapabilities{
		probed: true,
		model:  agyHelpHasFlag(help, "--model"),
		effort: agyHelpHasFlag(help, "--effort"),
	}
	if caps.effort {
		caps.efforts = parseAgyEffortValues(help)
	}
	return caps
}

func isFlagNameByte(b byte) bool {
	return b == '-' || b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// agyHelpFlagIndex returns the offset of flag as a whole word (so "--model"
// does not match "--model-provider" or "--no-model"), or -1.
func agyHelpFlagIndex(text, flag string) int {
	for from := 0; from < len(text); {
		i := strings.Index(text[from:], flag)
		if i < 0 {
			return -1
		}
		i += from
		end := i + len(flag)
		if (i == 0 || !isFlagNameByte(text[i-1])) && (end == len(text) || !isFlagNameByte(text[end])) {
			return i
		}
		from = end
	}
	return -1
}

func agyHelpHasFlag(help, flag string) bool {
	return agyHelpFlagIndex(help, flag) >= 0
}

var (
	agyEffortToken    = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	agyEffortPipeList = regexp.MustCompile(`[a-z][a-z0-9_-]*(?:\|[a-z][a-z0-9_-]*)+`)
)

// parseAgyEffortValues returns the values agy lists for --effort, or nil when
// the help text does not enumerate them. agy prints them as a pipe list on the
// flag's line, e.g. "(low|medium|high|xhigh|max)" in 1.4.9 (see
// testdata/agy-1.4.9-help.txt). Any other layout yields nil and the caller
// falls back to agyFallbackEfforts.
func parseAgyEffortValues(help string) []string {
	for _, line := range strings.Split(help, "\n") {
		idx := agyHelpFlagIndex(line, "--effort")
		if idx < 0 {
			continue
		}
		list := agyEffortPipeList.FindString(strings.ToLower(line[idx+len("--effort"):]))
		if list == "" {
			return nil
		}
		var values []string
		for _, v := range strings.Split(list, "|") {
			if !slices.Contains(values, v) {
				values = append(values, v)
			}
		}
		return values
	}
	return nil
}
