package coco

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
)

type cocoSession struct {
	workDir   string
	extraEnv  []string
	events    chan core.Event
	sessionID atomic.Value // stores string
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	alive     atomic.Bool
	cmd       *exec.Cmd
	stdin     io.WriteCloser
}

func newCocoSession(ctx context.Context, workDir, resumeID string, extraEnv []string) (*cocoSession, error) {
	sessionCtx, cancel := context.WithCancel(ctx)

	qs := &cocoSession{
		workDir:  workDir,
		extraEnv: extraEnv,
		events:   make(chan core.Event, 64),
		ctx:      sessionCtx,
		cancel:   cancel,
	}
	qs.alive.Store(true)

	if resumeID != "" && resumeID != core.ContinueSession {
		qs.sessionID.Store(resumeID)
	}

	err := qs.startProcess()
	if err != nil {
		return nil, err
	}

	return qs, nil
}

func (qs *cocoSession) startProcess() error {
	args := []string{}
	
	// 如果 coco 以后支持类似于 --format json 的流输出
	// args = append(args, "--format", "json")

	qs.cmd = exec.CommandContext(qs.ctx, "coco", args...)
	qs.cmd.Dir = qs.workDir
	if len(qs.extraEnv) > 0 {
		qs.cmd.Env = core.MergeEnv(os.Environ(), qs.extraEnv)
	}

	stdin, err := qs.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("cocoSession: stdin pipe: %w", err)
	}
	qs.stdin = stdin

	stdout, err := qs.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("cocoSession: stdout pipe: %w", err)
	}

	stderr, err := qs.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("cocoSession: stderr pipe: %w", err)
	}

	if err := qs.cmd.Start(); err != nil {
		return fmt.Errorf("cocoSession: start process: %w", err)
	}

	qs.wg.Add(1)
	go qs.readLoop(stdout)
	
	qs.wg.Add(1)
	go qs.readStderrLoop(stderr)

	go func() {
		err := qs.cmd.Wait()
		qs.alive.Store(false)
		if err != nil && qs.ctx.Err() == nil {
			slog.Error("coco process exited with error", "err", err)
		} else {
			slog.Debug("coco process exited normally")
		}
		qs.cancel()
	}()

	return nil
}

func (qs *cocoSession) Send(prompt string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if !qs.alive.Load() {
		return fmt.Errorf("session is closed")
	}

	if len(files) > 0 {
		filePaths := core.SaveFilesToDisk(qs.workDir, files)
		prompt = core.AppendFileRefs(prompt, filePaths)
	}

	// 将用户的 prompt 输入给 coco 进程
	_, err := fmt.Fprintf(qs.stdin, "%s\n", prompt)
	if err != nil {
		return fmt.Errorf("failed to write to coco stdin: %w", err)
	}

	return nil
}

func (qs *cocoSession) readLoop(r io.Reader) {
	defer qs.wg.Done()
	scanner := bufio.NewScanner(r)

	// 这里是一个极为简化的 fallback parser，如果 coco 没有官方 JSON 输出
	// 您可能需要自己使用正则清理 ANSI 控制符然后 emit Text
	for scanner.Scan() {
		line := scanner.Text()
		line = cleanAnsi(line)
		if line == "" {
			continue
		}

		select {
		case qs.events <- core.Event{Type: core.EventMessage, Data: line + "\n"}:
		case <-qs.ctx.Done():
			return
		}
	}
	
	// 最后发送一个完成事件（这里是伪逻辑，您需要根据 coco 真实的结束标志来判断什么时候一个回合结束）
	select {
	case qs.events <- core.Event{Type: core.EventTurnComplete}:
	default:
	}
}

func (qs *cocoSession) readStderrLoop(r io.Reader) {
	defer qs.wg.Done()
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		slog.Warn("coco stderr", "msg", cleanAnsi(line))
	}
}

func (qs *cocoSession) RespondPermission(requestID string, result core.PermissionResult) error {
	if !qs.alive.Load() {
		return fmt.Errorf("session is closed")
	}
	
	// 如果 yolo/allow，往 stdin 里写个 Y
	reply := "Y\n"
	if result.Behavior == "deny" {
		reply = "N\n"
	}
	
	_, err := io.WriteString(qs.stdin, reply)
	return err
}

func (qs *cocoSession) Events() <-chan core.Event {
	return qs.events
}

func (qs *cocoSession) CurrentSessionID() string {
	if val, ok := qs.sessionID.Load().(string); ok {
		return val
	}
	return ""
}

func (qs *cocoSession) Alive() bool {
	return qs.alive.Load()
}

func (qs *cocoSession) Close() error {
	if !qs.alive.Swap(false) {
		return nil
	}
	qs.cancel()
	if qs.cmd != nil && qs.cmd.Process != nil {
		_ = qs.cmd.Process.Kill()
	}
	
	closeCtx, cancelClose := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelClose()

	done := make(chan struct{})
	go func() {
		qs.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-closeCtx.Done():
		slog.Warn("cocoSession: close timed out waiting for goroutines")
	}

	return nil
}

// cleanAnsi 移除终端颜色等控制字符 (简化版)
func cleanAnsi(str string) string {
	var sb strings.Builder
	inEscape := false
	for i := 0; i < len(str); {
		r, size := utf8.DecodeRuneInString(str[i:])
		if r == '\x1b' {
			inEscape = true
			i += size
			continue
		}
		if inEscape {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
			i += size
			continue
		}
		sb.WriteRune(r)
		i += size
	}
	return sb.String()
}
