//go:build integration && !windows

package daxiangbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const testClientSecret = "0123456789abcdef0123456789abcdef"

type lifecycleRecorder struct {
	ready       chan struct{}
	unavailable chan error
}

func (r *lifecycleRecorder) OnPlatformReady(core.Platform) {
	select {
	case r.ready <- struct{}{}:
	default:
	}
}

func (r *lifecycleRecorder) OnPlatformUnavailable(_ core.Platform, err error) {
	select {
	case r.unavailable <- err:
	default:
	}
}

func TestPlatform_RealUGCAgentToolsRoundTrip(t *testing.T) {
	baseURL, stop := startUGCAgentTools(t)
	defer stop()

	rawPlatform, err := New(map[string]any{
		"ws_url":        strings.Replace(baseURL, "http://", "ws://", 1) + "/ws",
		"client_id":     "test-ccconnect-01",
		"client_secret": testClientSecret,
		"bot_id":        int64(10001),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	platform := rawPlatform.(*Platform)

	ready := make(chan struct{}, 1)
	unavailable := make(chan error, 1)
	platform.SetLifecycleHandler(&lifecycleRecorder{ready: ready, unavailable: unavailable})

	received := make(chan *core.Message, 1)
	if err := platform.Start(func(_ core.Platform, msg *core.Message) {
		select {
		case received <- msg:
		default:
		}
	}); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer platform.Stop()

	select {
	case <-ready:
	case err := <-unavailable:
		t.Fatalf("platform became unavailable before ready: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("timeout waiting for daxiangbridge platform ready")
	}

	sendResp := postBridgeTestMessage(t, baseURL, map[string]any{
		"botId":          10001,
		"sessionId":      "sess_real_e2e_001",
		"conversationId": "conv_real_e2e_001",
		"messageId":      "msg_real_e2e_001",
		"fromUserId":     "u_real_e2e_001",
		"fromUserName":   "张三",
		"text":           "hello from cc-connect integration",
	})
	if !sendResp.Ok {
		t.Fatalf("bridge/test/send ok=false, reason=%q", sendResp.Reason)
	}
	if sendResp.RequestID == "" {
		t.Fatal("bridge/test/send returned empty requestId")
	}

	var msg *core.Message
	select {
	case msg = <-received:
	case err := <-unavailable:
		t.Fatalf("platform became unavailable while waiting message: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("timeout waiting for inbound bridge message")
	}

	if msg.Platform != "daxiangbridge" {
		t.Fatalf("msg.Platform = %q, want daxiangbridge", msg.Platform)
	}
	if msg.Content != "hello from cc-connect integration" {
		t.Fatalf("msg.Content = %q, want hello from cc-connect integration", msg.Content)
	}
	if msg.ChatName != "conv_real_e2e_001" {
		t.Fatalf("msg.ChatName = %q, want conv_real_e2e_001", msg.ChatName)
	}
	if msg.UserName != "张三" {
		t.Fatalf("msg.UserName = %q, want 张三", msg.UserName)
	}

	if err := platform.Send(context.Background(), msg.ReplyCtx, "bridge-ok"); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	waitForOutboundText(t, baseURL, "conv_real_e2e_001", "bridge-ok")
}

type bridgeSendResponse struct {
	Ok        bool   `json:"ok"`
	RequestID string `json:"requestId"`
	Reason    string `json:"reason"`
}

type bridgeOutboundResponse struct {
	Records []map[string]any `json:"records"`
}

func postBridgeTestMessage(t *testing.T, baseURL string, body map[string]any) bridgeSendResponse {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	resp, err := http.Post(baseURL+"/bridge/test/send", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("post bridge/test/send: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bridge/test/send status = %d, want 200", resp.StatusCode)
	}
	var decoded bridgeSendResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode bridge/test/send: %v", err)
	}
	return decoded
}

func waitForOutboundText(t *testing.T, baseURL, conversationID, wantText string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var lastStatus int
	var lastBody string
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/bridge/test/outbound")
		if err == nil {
			found := false
			func() {
				defer resp.Body.Close()
				lastStatus = resp.StatusCode
				body, readErr := io.ReadAll(resp.Body)
				if readErr == nil {
					lastBody = string(body)
				}
				if resp.StatusCode != http.StatusOK {
					return
				}
				var decoded bridgeOutboundResponse
				if err := json.Unmarshal(body, &decoded); err != nil {
					lastBody = fmt.Sprintf("decode error: %v raw=%s", err, string(body))
					return
				}
				for _, record := range decoded.Records {
					if record["type"] == "text" && record["conversationId"] == conversationID && record["text"] == wantText {
						found = true
						return
					}
				}
			}()
			if found {
				return
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("timeout waiting outbound text conversationId=%s text=%s status=%d body=%s", conversationID, wantText, lastStatus, lastBody)
}

func registerCleanup(t *testing.T, cleanup func()) func() {
	t.Helper()
	var once sync.Once
	wrapped := func() {
		once.Do(cleanup)
	}
	t.Cleanup(wrapped)
	return wrapped
}

func terminateProcessGroup(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGTERM)
}

func killProcessGroup(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

func closeLogFile(file *os.File) {
	_ = file.Close()
}

func tryReadProcessExit(waitDone <-chan error) bool {
	select {
	case <-waitDone:
		return true
	default:
		return false
	}
}

func waitForProcessExit(waitDone <-chan error, timeout time.Duration, pid int) {
	if tryReadProcessExit(waitDone) {
		return
	}
	select {
	case <-waitDone:
	case <-time.After(timeout):
		killProcessGroup(pid)
		<-waitDone
	}
}

func cleanupStartedProcess(logFile *os.File, waitDone <-chan error, pid int) {
	if !tryReadProcessExit(waitDone) {
		terminateProcessGroup(pid)
		waitForProcessExit(waitDone, 5*time.Second, pid)
	}
	closeLogFile(logFile)
}

func startUGCAgentTools(t *testing.T) (string, func()) {
	t.Helper()
	ugcDir := findUGCAgentToolsDir(t)
	wrapper := filepath.Join(ugcDir, "ugc-agent-tools-start", "mvnw")
	if _, err := os.Stat(wrapper); err != nil {
		t.Skipf("ugc-agent-tools wrapper not found: %v", err)
	}

	listener, port := reservePort(t)
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	logPath := filepath.Join(t.TempDir(), "ugc-agent-tools.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create log file: %v", err)
	}

	javaHome := os.Getenv("JAVA17_HOME")
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("/usr/libexec/java_home", "-v", "17").Output()
		if err == nil {
			javaHome = strings.TrimSpace(string(out))
		}
	}
	if javaHome == "" {
		javaHome = os.Getenv("JAVA_HOME")
	}
	if javaHome == "" {
		_ = listener.Close()
		closeLogFile(logFile)
		t.Skip("JAVA_HOME is not set and automatic Java 17 lookup is unavailable")
	}

	ssoExcludes := "/monitor/**,/static/**,/favicon.ico,/ws/**,/ws,/bridge/test/**,/bridge/test"
	buildCmd := exec.Command(
		wrapper,
		"-f", filepath.Join(ugcDir, "pom.xml"),
		"-pl", "ugc-agent-tools-starter",
		"-am",
		"-DskipTests",
		"package",
	)
	buildCmd.Dir = ugcDir
	buildCmd.Env = core.MergeEnv(os.Environ(), []string{
		"JAVA_HOME=" + javaHome,
		"PATH=" + javaHome + "/bin:" + os.Getenv("PATH"),
	})
	buildOutput, err := buildCmd.CombinedOutput()
	if err != nil {
		_ = listener.Close()
		closeLogFile(logFile)
		t.Fatalf("package ugc-agent-tools-starter: %v\n%s", err, string(buildOutput))
	}

	jarPath := filepath.Join(ugcDir, "ugc-agent-tools-starter", "target", "ugc-agent-tools-starter.jar")
	cmd := exec.Command(
		filepath.Join(javaHome, "bin", "java"),
		"-Dserver.port="+fmt.Sprintf("%d", port),
		"-Dmanagement.server.port=0",
		"-Dugc.agent.tools.bridge.test-api.enabled=true",
		fmt.Sprintf("-Dugc.agent.tools.bridge.clients.test-ccconnect-01.secret=%s", testClientSecret),
		fmt.Sprintf("-Dmdp.sso.exclude-paths=%s", ssoExcludes),
		"-jar",
		jarPath,
	)
	cmd.Dir = ugcDir
	cmd.Env = core.MergeEnv(os.Environ(), []string{
		"JAVA_HOME=" + javaHome,
		"PATH=" + javaHome + "/bin:" + os.Getenv("PATH"),
	})
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = listener.Close()
		closeLogFile(logFile)
		t.Fatalf("start ugc-agent-tools: %v", err)
	}
	_ = listener.Close()

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
	}()

	cleanup := registerCleanup(t, func() {
		cleanupStartedProcess(logFile, waitDone, cmd.Process.Pid)
	})

	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		select {
		case err := <-waitDone:
			cleanupLog := readTail(logPath)
			t.Fatalf("ugc-agent-tools exited before ready: %v\n%s", err, cleanupLog)
		default:
		}
		resp, err := client.Get(baseURL + "/bridge/test/clients")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return baseURL, cleanup
			}
		}
		time.Sleep(1 * time.Second)
	}

	cleanupLog := readTail(logPath)
	cleanup()
	t.Fatalf("timeout waiting ugc-agent-tools ready\n%s", cleanupLog)
	return "", func() {}
}

func findUGCAgentToolsDir(t *testing.T) string {
	t.Helper()
	if dir := os.Getenv("UGC_AGENT_TOOLS_DIR"); dir != "" {
		if _, err := os.Stat(filepath.Join(dir, "pom.xml")); err == nil {
			return dir
		}
	}
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	ccRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	candidate := filepath.Clean(filepath.Join(ccRoot, "..", "ugc-agent-tools"))
	if _, err := os.Stat(filepath.Join(candidate, "pom.xml")); err != nil {
		t.Skipf("ugc-agent-tools repo not found at %s", candidate)
	}
	return candidate
}

func reservePort(t *testing.T) (net.Listener, int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	return listener, listener.Addr().(*net.TCPAddr).Port
}

func readTail(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("read log failed: %v", err)
	}
	if len(data) > 4000 {
		data = data[len(data)-4000:]
	}
	return string(data)
}
