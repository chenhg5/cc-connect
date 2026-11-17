package antigravity

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// writeFakeAgy writes a fake agy script. "--help" (as the last argument, the
// way the probe calls it) prints help and counts the probe in <script>.probes;
// any other invocation appends its arguments to <script>.args, fails like a
// real CLI if it is given a flag listed in rejectFlags, and otherwise prints a
// successful stream-json turn. An empty help makes "--help" fail.
func writeFakeAgy(t *testing.T, help, rejectFlags string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "agy")
	helpFile := script + ".help"
	if err := os.WriteFile(helpFile, []byte(help), 0o600); err != nil {
		t.Fatal(err)
	}
	body := `#!/bin/sh
for last; do :; done
if [ "$last" = "--help" ]; then
  echo probe >> "$0.probes"
  [ -s "$0.help" ] || exit 1
  cat "$0.help"
  exit 0
fi
printf '%s\n' "$*" >> "$0.args"
for a; do
  case " ` + rejectFlags + ` " in
    *" $a "*) echo "error: unknown flag: $a" >&2; exit 2 ;;
  esac
done
printf '{"event":"init","conversation_id":"conv-1"}\n'
printf '{"event":"result","result":{"conversation_id":"conv-1","status":"SUCCESS","response":"ok"}}\n'
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

const fakeAgyHelpNew = `Usage: agy [options] [prompt]

Options:
  -p, --print                 Print the response and exit
      --conversation <id>     Resume a conversation
  -m, --model <model>         Model to use
      --effort <level>        Reasoning effort (low|medium|high)
      --output-format <fmt>   Output format
`

const fakeAgyHelpOld = `Usage: agy [options] [prompt]

Options:
  -p, --print                 Print the response and exit
      --conversation <id>     Resume a conversation
      --model-provider <name> Not the model flag
      --output-format <fmt>   Output format
`

// runFakeAgyTurns sends one prompt per entry and returns the recorded agy
// command lines, failing the test if a turn reports an error.
func runFakeAgyTurns(t *testing.T, cmd string, extraArgs []string, script, model, effort string, turns int) []string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	s, err := newAntigravitySession(context.Background(), cmd, extraArgs, t.TempDir(), model, effort, "yolo", "", nil, 0)
	if err != nil {
		t.Fatalf("newAntigravitySession: %v", err)
	}
	defer func() { _ = s.Close() }()
	for i := 0; i < turns; i++ {
		if err := s.Send("hi", "", nil, nil); err != nil {
			t.Fatalf("Send: %v", err)
		}
		waitForCleanResult(t, s)
	}
	return readLines(t, script+".args")
}

func waitForCleanResult(t *testing.T, s *antigravitySession) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-s.Events():
			if ev.Error != nil {
				t.Fatalf("turn failed: %v", ev.Error)
			}
			if ev.Done {
				return
			}
		case <-deadline:
			t.Fatal("timeout waiting for result")
		}
	}
}

func TestSession_OldAgyDoesNotGetModelOrEffortFlags(t *testing.T) {
	// This agy fails the turn on --model/--effort, like a release that
	// predates them. The turn must still succeed with the flags left out.
	script := writeFakeAgy(t, fakeAgyHelpOld, "--model --effort")
	runs := runFakeAgyTurns(t, script, nil, script, "gemini-3.8-flash", "high", 1)
	if len(runs) != 1 {
		t.Fatalf("expected 1 turn, got %v", runs)
	}
	if strings.Contains(runs[0], "--model") || strings.Contains(runs[0], "--effort") {
		t.Fatalf("old agy must not receive --model/--effort: %q", runs[0])
	}
}

func TestSession_NewAgyGetsModelAndEffortAndIsProbedOnce(t *testing.T) {
	script := writeFakeAgy(t, fakeAgyHelpNew, "")
	runs := runFakeAgyTurns(t, script, nil, script, "gemini-3.8-flash", "HIGH", 2)
	if len(runs) != 2 {
		t.Fatalf("expected 2 turns, got %v", runs)
	}
	for _, run := range runs {
		if !strings.Contains(run, "--model gemini-3.8-flash") || !strings.Contains(run, "--effort high") {
			t.Fatalf("expected --model and --effort, got %q", run)
		}
	}
	if probes := readLines(t, script+".probes"); len(probes) != 1 {
		t.Fatalf("agy --help ran %d times, want 1 (cached per process)", len(probes))
	}
}

func TestSession_UnsupportedEffortValueIsNotPassed(t *testing.T) {
	// xhigh is not in this agy's list and would fail the turn if passed.
	script := writeFakeAgy(t, fakeAgyHelpNew, "xhigh")
	runs := runFakeAgyTurns(t, script, nil, script, "gemini-3.8-flash", "xhigh", 1)
	if len(runs) != 1 {
		t.Fatalf("expected 1 turn, got %v", runs)
	}
	if strings.Contains(runs[0], "--effort") {
		t.Fatalf("unsupported effort must not be passed: %q", runs[0])
	}
	if !strings.Contains(runs[0], "--model gemini-3.8-flash") {
		t.Fatalf("model should still be passed: %q", runs[0])
	}
}

func TestSession_ProbeFailureDropsModelAndEffort(t *testing.T) {
	script := writeFakeAgy(t, "", "--model --effort")
	runs := runFakeAgyTurns(t, script, nil, script, "gemini-3.8-flash", "high", 2)
	if len(runs) != 2 {
		t.Fatalf("expected 2 turns, got %v", runs)
	}
	for _, run := range runs {
		if strings.Contains(run, "--model") || strings.Contains(run, "--effort") {
			t.Fatalf("flags must not be passed when the probe fails: %q", run)
		}
	}
	// A failed probe is remembered for a while rather than re-run per turn.
	if probes := readLines(t, script+".probes"); len(probes) != 1 {
		t.Fatalf("agy --help ran %d times, want 1", len(probes))
	}
}

func TestSession_ProbeGoesThroughCmdPrefix(t *testing.T) {
	// cmd = ["env", "<agy>"]: the binary is "env" and agy lives in extraArgs.
	script := writeFakeAgy(t, fakeAgyHelpNew, "")
	runs := runFakeAgyTurns(t, "env", []string{script}, script, "gemini-3.8-flash", "", 1)
	if len(runs) != 1 || !strings.Contains(runs[0], "--model gemini-3.8-flash") {
		t.Fatalf("expected --model through the cmd prefix, got %v", runs)
	}
	if probes := readLines(t, script+".probes"); len(probes) != 1 {
		t.Fatalf("agy --help ran %d times through the prefix, want 1", len(probes))
	}
}

func TestSession_NoProbeWithoutModelOrEffort(t *testing.T) {
	script := writeFakeAgy(t, fakeAgyHelpNew, "")
	runFakeAgyTurns(t, script, nil, script, "", "", 1)
	if probes := readLines(t, script+".probes"); len(probes) != 0 {
		t.Fatalf("agy --help ran %d times, want 0 when nothing needs gating", len(probes))
	}
}

func TestParseAgyHelp(t *testing.T) {
	tests := []struct {
		name        string
		help        string
		wantModel   bool
		wantEffort  bool
		wantEfforts []string
	}{
		{name: "old agy", help: fakeAgyHelpOld},
		{name: "pipe list in description", help: fakeAgyHelpNew, wantModel: true, wantEffort: true, wantEfforts: []string{"low", "medium", "high"}},
		{
			name:       "pipe list as placeholder",
			help:       "  --model string\n  --effort <low|medium|high|xhigh>\n",
			wantModel:  true,
			wantEffort: true, wantEfforts: []string{"low", "medium", "high", "xhigh"},
		},
		{
			name:       "values listed without a pipe list",
			help:       "  --effort <EFFORT>  Reasoning effort [possible values: low, medium, high]\n",
			wantEffort: true,
		},
		{
			name:       "values not listed",
			help:       "  --model <id>    Model\n  --effort <level>  Reasoning effort (default: medium)\n  --mode <m>  One of (fast|slow)\n",
			wantModel:  true,
			wantEffort: true,
		},
		{name: "similar flags only", help: "  --model-provider x\n  --no-effort\n  --effort_budget n\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseAgyHelp(tt.help)
			if got.model != tt.wantModel || got.effort != tt.wantEffort {
				t.Fatalf("model=%v effort=%v, want model=%v effort=%v", got.model, got.effort, tt.wantModel, tt.wantEffort)
			}
			if !reflect.DeepEqual(got.efforts, tt.wantEfforts) {
				t.Fatalf("efforts = %v, want %v", got.efforts, tt.wantEfforts)
			}
		})
	}
}

func TestAvailableReasoningEfforts_FromAgyHelp(t *testing.T) {
	tests := []struct {
		name string
		help string
		want []string
	}{
		{name: "listed by agy", help: "  --effort <low|high|max>  Reasoning effort\n", want: []string{"low", "high", "max"}},
		{name: "not listed falls back to the conservative set", help: "  --effort <level>  Reasoning effort\n", want: []string{"low", "medium", "high"}},
		{name: "no effort flag", help: fakeAgyHelpOld, want: nil},
		{name: "probe fails", help: "", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Agent{cmd: writeFakeAgy(t, tt.help, ""), activeIdx: -1}
			if got := a.AvailableReasoningEfforts(); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("AvailableReasoningEfforts = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSetReasoningEffort_RejectsValueAgyDoesNotList(t *testing.T) {
	a := &Agent{cmd: writeFakeAgy(t, "  --effort <low|medium|high>  Reasoning effort\n", ""), activeIdx: -1}
	a.SetReasoningEffort("xhigh")
	if got := a.GetReasoningEffort(); got != "" {
		t.Fatalf("effort = %q, want empty for a value agy does not list", got)
	}
	a.SetReasoningEffort("medium")
	if got := a.GetReasoningEffort(); got != "medium" {
		t.Fatalf("effort = %q, want medium", got)
	}
}

// testdata/agy-1.4.9-help.txt is the verbatim output of "agy --help" from
// agy 1.4.9, which prints it on stderr and exits 0.
func TestProbeAgyCapabilities_RealAgy149Help(t *testing.T) {
	fixture, err := filepath.Abs(filepath.Join("testdata", "agy-1.4.9-help.txt"))
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "agy")
	body := "#!/bin/sh\n[ \"$1\" = --help ] || exit 2\ncat \"$AGY_HELP_FIXTURE\" >&2\nexit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "AGY_HELP_FIXTURE="+fixture)

	caps := agyCapabilitiesFor(context.Background(), script, nil, env)
	if !caps.probed || !caps.model || !caps.effort {
		t.Fatalf("caps = %+v, want probed with --model and --effort", caps)
	}
	want := []string{"low", "medium", "high", "xhigh", "max"}
	if !reflect.DeepEqual(caps.efforts, want) {
		t.Fatalf("efforts = %v, want %v", caps.efforts, want)
	}
	if got := caps.supportedEfforts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("supportedEfforts = %v, want %v", got, want)
	}
}

func TestSetReasoningEffort_KeptWhenProbeFails(t *testing.T) {
	// "--help" fails, so nothing is known about this agy. The selection must
	// survive; the per-turn gate decides whether it is passed.
	script := writeFakeAgy(t, "", "")
	a := &Agent{cmd: script, activeIdx: -1}
	a.SetReasoningEffort("XHigh")
	if got := a.GetReasoningEffort(); got != "xhigh" {
		t.Fatalf("effort = %q, want xhigh kept after a failed probe", got)
	}
	a.SetReasoningEffort("not a level")
	if got := a.GetReasoningEffort(); got != "" {
		t.Fatalf("effort = %q, want empty for a malformed value", got)
	}
}
