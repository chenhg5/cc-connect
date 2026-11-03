package helper

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	bbplatform "github.com/chenhg5/cc-connect/tests/blackbox/platform"
)

// Binary backend
//
// With CC_BLACKBOX_BACKEND=binary every blackbox Env starts a real cc-connect
// process instead of an in-process core.Engine:
//
//	test ──bridge WebSocket──▶ cc-connect binary ──▶ real agent CLI
//
// The test reaches the binary through its bridge server (docs/bridge-protocol.md),
// so config loading, startup, the bridge transport and the agent subprocess are
// all exercised exactly as in a deployment. Replies are recorded on the same
// MockPlatform, so the scenarios and assertions are unchanged.
//
// CC_BLACKBOX_BINARY points at a prebuilt binary (for example the release
// candidate in dist/); otherwise the current tree is built once per test
// process with -tags no_web.

const binaryProject = "blackbox"

var (
	buildOnce sync.Once
	builtPath string
	buildErr  error
)

// binaryStartTimeout bounds how long the process may take to come up.
const binaryStartTimeout = 60 * time.Second

func binaryPath(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("CC_BLACKBOX_BINARY"); p != "" {
		abs, err := filepath.Abs(p)
		if err != nil {
			t.Fatalf("blackbox: CC_BLACKBOX_BINARY: %v", err)
		}
		if _, err := os.Stat(abs); err != nil {
			t.Fatalf("blackbox: CC_BLACKBOX_BINARY %s: %v", abs, err)
		}
		return abs
	}
	buildOnce.Do(func() {
		root, err := moduleRoot()
		if err != nil {
			buildErr = err
			return
		}
		dir, err := os.MkdirTemp("", "cc-connect-blackbox-bin-")
		if err != nil {
			buildErr = err
			return
		}
		out := filepath.Join(dir, "cc-connect")
		cmd := exec.Command("go", "build", "-tags", "no_web", "-o", out, "./cmd/cc-connect")
		cmd.Dir = root
		if msg, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("go build ./cmd/cc-connect: %v\n%s", err, msg)
			return
		}
		builtPath = out
	})
	if buildErr != nil {
		t.Fatalf("blackbox: building cc-connect binary: %v", buildErr)
	}
	return builtPath
}

func moduleRoot() (string, error) {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", fmt.Errorf("go env GOMOD: %w", err)
	}
	mod := strings.TrimSpace(string(out))
	if mod == "" || mod == os.DevNull {
		return "", fmt.Errorf("go env GOMOD: not inside a module")
	}
	return filepath.Dir(mod), nil
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("blackbox: free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// binaryConfig is the config.toml written for one Env. Only the fields the
// harness needs are modelled; everything else keeps cc-connect's defaults.
type binaryConfig struct {
	DataDir  string                 `toml:"data_dir"`
	Language string                 `toml:"language"`
	Log      map[string]any         `toml:"log"`
	Bridge   map[string]any         `toml:"bridge"`
	Projects []binaryProjectSection `toml:"projects"`
}

type binaryProjectSection struct {
	Name      string                  `toml:"name"`
	Agent     binaryAgentSection      `toml:"agent"`
	Platforms []binaryPlatformSection `toml:"platforms"`
}

type binaryAgentSection struct {
	Type      string           `toml:"type"`
	Options   map[string]any   `toml:"options"`
	Providers []map[string]any `toml:"providers,omitempty"`
}

type binaryPlatformSection struct {
	Type    string         `toml:"type"`
	Options map[string]any `toml:"options"`
}

// binaryProcess is a running cc-connect started for one Env.
type binaryProcess struct {
	cmd     *exec.Cmd
	logPath string
	exited  chan struct{}
	exitErr error
}

// LogTail returns the last n lines of the process log, with the bridge token
// redacted.
func (p *binaryProcess) LogTail(n int, token string) string {
	data, err := os.ReadFile(p.logPath)
	if err != nil {
		return fmt.Sprintf("<cannot read %s: %v>", p.logPath, err)
	}
	if token != "" {
		data = bytes.ReplaceAll(data, []byte(token), []byte("REDACTED"))
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// newBinaryEnv starts cc-connect for agentType with agentOpts (and provider,
// when not nil) and connects a bridge adapter with the given capability
// profile.
func newBinaryEnv(t *testing.T, agentType string, agentOpts map[string]any, profile bbplatform.Profile, provider map[string]any) *Env {
	t.Helper()

	bin := binaryPath(t)
	home := t.TempDir()
	dataDir := filepath.Join(home, ".cc-connect")
	workDir, _ := agentOpts["work_dir"].(string)
	bridgePort := freePort(t)
	token := randomToken(t)

	opts := make(map[string]any, len(agentOpts)+1)
	for k, v := range agentOpts {
		opts[k] = v
	}
	agent := binaryAgentSection{Type: agentType, Options: opts}
	if provider != nil {
		agent.Providers = []map[string]any{provider}
		opts["provider"] = provider["name"]
	}

	cfg := binaryConfig{
		DataDir:  dataDir,
		Language: "en",
		Log:      map[string]any{"level": "debug"},
		Bridge: map[string]any{
			"enabled": true,
			"port":    bridgePort,
			"token":   token,
		},
		Projects: []binaryProjectSection{{
			Name:  binaryProject,
			Agent: agent,
			// A project must declare at least one platform. A cloud_web
			// gateway that listens on loopback and never registers anywhere
			// satisfies that without any outbound traffic; the test talks to
			// the project only through the bridge.
			Platforms: []binaryPlatformSection{{
				Type: "cloud_web",
				Options: map[string]any{
					"transport": "gateway",
					"token":     token,
					"base_url":  "http://127.0.0.1:9",
					"listen":    fmt.Sprintf("127.0.0.1:%d", freePort(t)),
				},
			}},
		}},
	}

	cfgPath := filepath.Join(home, "config.toml")
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		t.Fatalf("blackbox: encode config: %v", err)
	}
	if err := os.WriteFile(cfgPath, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("blackbox: write config: %v", err)
	}

	logPath := filepath.Join(home, "cc-connect.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("blackbox: create log: %v", err)
	}

	cmd := exec.Command(bin, "--config", cfgPath)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		logFile.Close()
		t.Fatalf("blackbox: start %s: %v", bin, err)
	}
	proc := &binaryProcess{cmd: cmd, logPath: logPath, exited: make(chan struct{})}
	go func() {
		proc.exitErr = cmd.Wait()
		logFile.Close()
		close(proc.exited)
	}()

	mp := bbplatform.New(agentType + "-mock")
	wsURL := fmt.Sprintf("ws://127.0.0.1:%d/bridge/ws", bridgePort)
	client := connectWithRetry(t, mp, wsURL, token, profile, proc)

	t.Cleanup(func() {
		client.Close()
		stopProcess(proc)
		if t.Failed() {
			t.Logf("blackbox: cc-connect log tail (%s):\n%s", logPath, proc.LogTail(80, token))
		}
	})

	return &Env{
		T:         t,
		Platform:  mp,
		WorkDir:   workDir,
		DataDir:   dataDir,
		Backend:   BackendBinary,
		Profile:   profile,
		agentType: agentType,
		proc:      proc,
		token:     token,
		bridgeURL: wsURL,
	}
}

func connectWithRetry(t *testing.T, mp *bbplatform.MockPlatform, wsURL, token string, profile bbplatform.Profile, proc *binaryProcess) *bbplatform.BridgeClient {
	t.Helper()
	deadline := time.Now().Add(binaryStartTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-proc.exited:
			t.Fatalf("blackbox: cc-connect exited before the bridge came up (%v)\n%s", proc.exitErr, proc.LogTail(60, token))
		default:
		}
		c, err := bbplatform.ConnectBridge(mp, wsURL, token, binaryProject, profile)
		if err == nil {
			return c
		}
		lastErr = err
		time.Sleep(200 * time.Millisecond)
	}
	stopProcess(proc)
	t.Fatalf("blackbox: bridge not reachable within %v: %v\n%s", binaryStartTimeout, lastErr, proc.LogTail(60, token))
	return nil
}

func stopProcess(p *binaryProcess) {
	select {
	case <-p.exited:
		return
	default:
	}
	terminateProcessGroup(p.cmd)
	select {
	case <-p.exited:
	case <-time.After(10 * time.Second):
		killProcessGroup(p.cmd)
		<-p.exited
	}
}

// providerFromEnv returns a [[projects.agent.providers]] entry when
// CC_BLACKBOX_<AGENT>_API_KEY is set, mirroring wireProviders.
func providerFromEnv(agentType string) map[string]any {
	prefix := "CC_BLACKBOX_" + strings.ToUpper(agentType) + "_"
	apiKey := os.Getenv(prefix + "API_KEY")
	if apiKey == "" {
		return nil
	}
	p := map[string]any{"name": "blackbox-test", "api_key": apiKey}
	if v := os.Getenv(prefix + "BASE_URL"); v != "" {
		p["base_url"] = v
	}
	if v := os.Getenv(prefix + "MODEL"); v != "" {
		p["model"] = v
	}
	if v := os.Getenv(prefix + "WIRE_API"); v != "" {
		p["codex"] = map[string]any{"wire_api": v}
	}
	return p
}

func randomToken(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("blackbox: random token: %v", err)
	}
	return hex.EncodeToString(b)
}

// BridgeURL returns the bridge WebSocket URL of the binary backend ("" for
// the in-process backend).
func (e *Env) BridgeURL() string { return e.bridgeURL }
