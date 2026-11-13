//go:build unix

package codex

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Each helper inherits the write end. EOF proves that both the direct child
// and its descendant have exited, without depending on zombie reaping by PID 1.
func TestUnixProcessTreeHelper(t *testing.T) {
	role := os.Getenv("CC_UNIX_TREE_HELPER")
	if role == "" {
		return
	}
	if role == "parent" {
		child := exec.Command(os.Args[0], "-test.run=^TestUnixProcessTreeHelper$", "-test.timeout=60s")
		child.Env = append(os.Environ(), "CC_UNIX_TREE_HELPER=child")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		_ = child.Wait()
		return
	}
	_, _ = fmt.Fprintln(os.Stdout, "ready")
	for {
		time.Sleep(time.Hour)
	}
}

func startUnixProcessTree(t *testing.T) (*exec.Cmd, <-chan struct{}) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestUnixProcessTreeHelper$", "-test.timeout=60s")
	cmd.Env = append(os.Environ(), "CC_UNIX_TREE_HELPER=parent")
	cmd.Stdout = writer
	cmd.Stderr = os.Stderr
	prepareCmdForKill(cmd)
	if err := cmd.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		t.Fatal(err)
	}
	_ = writer.Close()
	done := make(chan struct{})
	ready := make(chan error, 1)
	go func() {
		defer close(done)
		scan := bufio.NewReader(reader)
		line, err := scan.ReadString('\n')
		if err == nil && line != "ready\n" {
			err = fmt.Errorf("unexpected helper output %q", line)
		}
		ready <- err
		_, _ = io.Copy(io.Discard, scan)
	}()
	t.Cleanup(func() {
		// Only the process group created by this fixture is terminated.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("fixture pipe did not close")
		}
		_ = reader.Close()
	})
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("helper did not become ready")
	}
	return cmd, done
}

func TestUnixCleanupKillsDescendants(t *testing.T) {
	for _, mode := range []string{"forceKillCmd", "appServerClose", "appServerAbort"} {
		t.Run(mode, func(t *testing.T) {
			cmd, done := startUnixProcessTree(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			session := &appServerSession{ctx: ctx, cancel: cancel, cmd: cmd, events: make(chan core.Event)}
			switch mode {
			case "forceKillCmd":
				if err := forceKillCmd(cmd); err != nil {
					t.Fatal(err)
				}
			case "appServerClose":
				if err := session.Close(); err != nil {
					t.Fatal(err)
				}
			case "appServerAbort":
				session.abortTransport()
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("descendant survived cleanup (inherited pipe still open)")
			}
		})
	}
}
