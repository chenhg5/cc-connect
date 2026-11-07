package pi

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// The probe is deliberately held between process startup and its version reply.
// This lets the parent change the agent's next-session workdir without relying
// on scheduler timing or making the fake RPC process contact a provider.
func TestRPCVersionSnapshotHelperProcess(t *testing.T) {
	if os.Getenv("CC_PI_SNAPSHOT_HELPER") != "1" {
		return
	}
	gateDir := os.Getenv("CC_PI_SNAPSHOT_GATE")
	cwd, err := os.Getwd()
	if err != nil {
		os.Exit(84)
	}
	if os.Args[len(os.Args)-1] == "--version" {
		if err := os.WriteFile(filepath.Join(gateDir, "probe-cwd"), []byte(cwd), 0600); err != nil {
			os.Exit(85)
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(gateDir, "release-probe")); err == nil {
				fmt.Println("0.99.0")
				os.Exit(0)
			}
			if time.Now().After(deadline) {
				os.Exit(86)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	if err := os.WriteFile(filepath.Join(gateDir, "rpc-cwd"), []byte(cwd), 0600); err != nil {
		os.Exit(87)
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), "get_state") {
			fmt.Println(`{"type":"response","id":"cc-connect-state-probe","command":"get_state","success":true,"data":{"sessionId":"snapshot-test-session"}}`)
		}
	}
	os.Exit(0)
}

func TestStartSessionRPCVersionAndProcessShareWorkdirSnapshot(t *testing.T) {
	a := versionTestAgent(t, "0.99.0")
	initialDir := a.workDir
	nextDir := t.TempDir()
	gateDir := t.TempDir()
	a.cliExtraArgs = []string{"-test.run=^TestRPCVersionSnapshotHelperProcess$", "--"}
	a.configEnv = append(a.configEnv,
		"CC_PI_SNAPSHOT_HELPER=1",
		"CC_PI_SNAPSHOT_GATE="+gateDir,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	type result struct {
		session core.AgentSession
		err     error
	}
	started := make(chan result, 1)
	go func() {
		s, err := a.StartSession(ctx, "")
		started <- result{s, err}
	}()

	probeFile := filepath.Join(gateDir, "probe-cwd")
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(probeFile); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		select {
		case r := <-started:
			if r.session != nil {
				_ = r.session.Close()
			}
			t.Fatalf("StartSession returned before the held version probe: %v", r.err)
		case <-deadline.C:
			t.Fatal("version probe did not start")
		case <-tick.C:
		}
	}

	// The update applies to the next StartSession. This already-starting session
	// must keep the directory that its version probe actually validated.
	a.SetWorkDir(nextDir)
	if err := os.WriteFile(filepath.Join(gateDir, "release-probe"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	var r result
	select {
	case r = <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if r.err != nil {
		t.Fatal(r.err)
	}
	defer func() { _ = r.session.Close() }()
	for _, name := range []string{"probe-cwd", "rpc-cwd"} {
		data, err := os.ReadFile(filepath.Join(gateDir, name))
		if err != nil {
			t.Fatal(err)
		}
		got, err := filepath.EvalSymlinks(string(data))
		if err != nil {
			t.Fatal(err)
		}
		want, err := filepath.EvalSymlinks(initialDir)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s = %q, want initial session directory %q (updated next-session directory %q)", name, got, want, nextDir)
		}
	}
	if got := r.session.(*piSession).workDir; got != initialDir {
		t.Fatalf("session workDir = %q, want snapshot %q", got, initialDir)
	}
}
