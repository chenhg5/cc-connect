package pi

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const minimumRPCVersion = "0.99.0"

type rpcVersionError struct {
	err      error
	guidance string
}

func (err *rpcVersionError) Error() string       { return err.err.Error() }
func (err *rpcVersionError) Unwrap() error       { return err.err }
func (err *rpcVersionError) UserMessage() string { return err.guidance }

// Pi's RPC lifecycle dispositions were introduced in 0.99.0. Only accept
// an unambiguous plain SemVer, as returned by the official CLI's --version.
var rpcVersionPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

func parseSemverParts(output string) []string {
	return rpcVersionPattern.FindStringSubmatch(strings.TrimSpace(output))
}

func supportsRPCLifecycle(output string) bool {
	parts := parseSemverParts(output)
	if parts == nil {
		return false
	}
	var version [3]uint64
	for i := range version {
		n, err := strconv.ParseUint(parts[i+1], 10, 64)
		if err != nil {
			return false
		}
		version[i] = n
	}
	// Require a stable release: prereleases cannot guarantee this protocol.
	return parts[4] == "" && (version[0] > 0 || version[1] >= 99)
}

// Probe the same immutable command snapshot used to start the RPC process.
func checkRPCVersion(ctx context.Context, command, workDir string, i18n *core.I18n, extraArgs, extraEnv []string) error {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	args := append(append([]string(nil), extraArgs...), "--version")
	cmd := exec.CommandContext(probeCtx, command, args...)
	cmd.Dir = workDir
	cmd.Env = core.MergeEnv(os.Environ(), extraEnv)
	// Bound waits even if a wrapper exits leaving an inherited output pipe open.
	cmd.WaitDelay = time.Second
	output, err := cmd.Output()
	if i18n == nil {
		i18n = core.NewI18n(core.LangEnglish)
	}
	install := "npm install -g @earendil-works/pi-coding-agent@latest"
	if err == nil && supportsRPCLifecycle(string(output)) {
		return nil
	}
	// Distinguish the actionable cases: an old or unstable Pi needs an upgrade,
	// while a probe that failed or returned ambiguous output needs a wrapper
	// that forwards --version — telling that user to upgrade loops them into
	// the identical message.
	var guidance string
	versioned := err == nil && parseSemverParts(string(output)) != nil
	if versioned {
		guidance = i18n.Tf(core.MsgAgentVersionRequired, "Pi RPC", "Pi", minimumRPCVersion, install)
	} else {
		guidance = i18n.Tf(core.MsgAgentVersionUnreadable, "Pi RPC", "Pi", minimumRPCVersion, install)
	}
	if probeCtx.Err() != nil {
		err = fmt.Errorf("pi: %s (--version: %w)", guidance, probeCtx.Err())
	} else if err != nil {
		err = fmt.Errorf("pi: %s (--version: %w)", guidance, err)
	} else {
		err = fmt.Errorf("pi: %s", guidance)
	}
	return &rpcVersionError{err: err, guidance: guidance}
}
