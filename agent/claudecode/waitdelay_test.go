//go:build windows

package claudecode

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// /stop「杀不死」的根因回归（帝王鱼 2026-07-29）。
//
// 症状：taskkill /T /F 报成功，Close() 却在 10s 后返回
// "process tree (pid N) still alive 10s after SIGKILL reported success"，
// engine 认定 teardown 失败 —— 用户按了 /stop，却像在跟另一个 session 说话。
//
// 根因：session.go 把 stderr 接到 *bytes.Buffer（非 *os.File），os/exec 因此自建
// 管道 + io.Copy goroutine，而 cmd.Wait() **必须等该 goroutine 结束**，它要等管道 EOF。
// 只要任何一个孙进程（Claude Code 拉起的 MCP server）继承了 stderr 写端还活着，
// 管道就永不 EOF → Wait() 永不返回 → cs.done 永不关闭 → Close() 只能超时。
// **"还活着"的不是进程，是那根没人关的管道。**
//
// ⚠️ 复现的关键在于孙进程要**真继承句柄**：第一版脚手架用 `start /b` 起孙进程，
//    不继承，于是 Wait() 0.03s 就返回、复现不出来 —— 差点据此误判"猜错了"。
//    用 -NoNewWindow 才是生产里的形状（cc-connect → cmd 壳 → claude → MCP node 全继承）。

func spawnInheritingGrandchild() *exec.Cmd {
	return exec.Command("powershell", "-NoProfile", "-Command",
		"Start-Process ping -ArgumentList '-n','60','127.0.0.1' -NoNewWindow -PassThru | Out-Null; exit 0")
}

// 对照组：不设 WaitDelay → 必须卡住。这条证明症状确实由它引起，不是猜的。
func TestWithoutWaitDelayWaitHangsOnInheritedPipe(t *testing.T) {
	cmd := spawnInheritingGrandchild()
	var b bytes.Buffer
	cmd.Stderr = &b
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		t.Fatal("没复现出症状：不设 WaitDelay 也及时返回了 —— " +
			"若本机行为变了，本组对照失效，修复的必要性要重新评估")
	case <-time.After(12 * time.Second):
		_ = forceKillCmd(cmd)
	}
}

// 实验组：设了 WaitDelay → 同样形状下必须在界内返回。
func TestWaitDelayBoundsInheritedPipe(t *testing.T) {
	cmd := spawnInheritingGrandchild()
	var b bytes.Buffer
	cmd.Stderr = &b
	cmd.WaitDelay = 3 * time.Second
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("WaitDelay 没兜住管道 —— 修复失效")
	}
	_ = forceKillCmd(cmd)
}

// 🔴 上面两条只证明 WaitDelay **管用**，不证明**生产代码真的设了它** ——
// 把 session.go 里那行删掉，上面两条照样绿。这条守的就是那行本身。
// （同一天在 amazon-monitor 那边刚踩过同款：工具做好了但没挂上去，等于没有。）
func TestProductionSessionSetsWaitDelay(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(".", "session.go"))
	if err != nil {
		t.Fatalf("读 session.go: %v", err)
	}
	if !strings.Contains(string(src), "cmd.WaitDelay") {
		t.Fatal("session.go 没有设置 cmd.WaitDelay —— " +
			"少了它，孙进程持有 stderr 管道时 cmd.Wait() 会永不返回，" +
			"/stop 会重新变成『杀不死』。本仓库 gemini/kimi/antigravity/hooks 都设了，别只漏这个。")
	}
}

// /stop 的体感回归（Cheney 2026-07-31 实测逼出来的）。
//
// 症状：按 /stop 立刻回「执行已停止」，bot 却还在干活、还在说话，连按几次都一样。
// 实测 17:45:20 /stop → 17:47:29 真死，**2 分 09 秒**，而这两分钟里它照常写库推消息。
// 根因：Phase 1 graceful 窗口写死 120s，是留给 Stop hook 的 —— 而本机根本没有 Stop hook。
// 而且 graceful 对"正在跑"的会话本来就无效：Claude Code 要等当前这轮结束才理会 stdin EOF。
func TestGracefulStopTimeoutIsShortEnoughForUserStop(t *testing.T) {
	if defaultGracefulStopTimeout > 10*time.Second {
		t.Fatalf("graceful 窗口 %v 太长 —— /stop 的语义是『现在就停』。"+
			"用户按它是因为 bot 正在做错的事，等下去 = 让错事做完。"+
			"（曾经是 120s，实测 /stop 到真死要 2 分 09 秒）", defaultGracefulStopTimeout)
	}
	if defaultGracefulStopTimeout < 2*time.Second {
		t.Fatalf("graceful 窗口 %v 太短 —— 闲置会话来不及干净退出就被强杀，"+
			"会白白留下本可避免的孤儿进程", defaultGracefulStopTimeout)
	}
}

// 闲置会话必须走「干净退出」这条路，不该被 SIGKILL —— 否则 5s 就设错了。
func TestIdleSessionExitsWithinGracefulWindow(t *testing.T) {
	cmd := exec.Command("cmd", "/c", "more") // 读 stdin，EOF 即退
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	var b bytes.Buffer
	cmd.Stderr = &b
	cmd.WaitDelay = 3 * time.Second
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	_ = stdin.Close() // 模拟 Phase 1：关 stdin

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done: // 在 graceful 窗口内自己退了 ✅
	case <-time.After(defaultGracefulStopTimeout):
		_ = forceKillCmd(cmd)
		t.Fatalf("闲置进程在 %v 内没能靠关 stdin 退出 —— graceful 窗口设小了",
			defaultGracefulStopTimeout)
	}
}
