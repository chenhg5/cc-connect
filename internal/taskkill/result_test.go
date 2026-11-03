package taskkill

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"testing"
)

func TestTaskkillNotRunning_LocalizedOutput(t *testing.T) {
	for _, output := range []string{
		`ERROR: The process "999999" not found.`,
		`错误: 没有找到进程 "999999"。`,
		`FEHLER: Der Prozess "999999" wurde nicht gefunden.`,
		"",
	} {
		t.Run(output, func(t *testing.T) {
			err := taskkillExitError(t, 128, output)
			if !IsNotRunning(err) {
				t.Fatalf("exit 128 must be recognized regardless of output %q", output)
			}
			if !IsNotRunning(fmt.Errorf("taskkill: %w", err)) {
				t.Fatal("wrapped exit 128 must be recognized")
			}
		})
	}
}

func TestTaskkillNotRunning_PreservesOtherFailures(t *testing.T) {
	for _, code := range []int{1, 5, 127, 129} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			if IsNotRunning(taskkillExitError(t, code, "not found")) {
				t.Fatalf("exit %d must not be accepted based on diagnostic text", code)
			}
		})
	}
	for _, err := range []error{nil, exec.ErrNotFound, errors.New("access denied")} {
		if IsNotRunning(err) {
			t.Errorf("non-exit error %v must not count as an absent process", err)
		}
	}
}

func taskkillExitError(t *testing.T, code int, output string) error {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestTaskkillExitHelper$", "--", strconv.Itoa(code))
	cmd.Env = append(os.Environ(), "CC_CONNECT_TASKKILL_TEST_HELPER=1", "CC_CONNECT_TASKKILL_TEST_OUTPUT="+output)
	actualOutput, err := cmd.CombinedOutput()
	if string(actualOutput) != output {
		t.Fatalf("helper output = %q, want %q", actualOutput, output)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != code {
		t.Fatalf("helper did not exit with %d: %v", code, err)
	}
	return err
}

func TestTaskkillExitHelper(t *testing.T) {
	if os.Getenv("CC_CONNECT_TASKKILL_TEST_HELPER") != "1" {
		return
	}
	code, err := strconv.Atoi(os.Args[len(os.Args)-1])
	if err != nil {
		os.Exit(2)
	}
	fmt.Print(os.Getenv("CC_CONNECT_TASKKILL_TEST_OUTPUT"))
	os.Exit(code)
}
