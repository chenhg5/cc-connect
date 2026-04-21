package coco

import (
	"bytes"
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
	"github.com/creack/pty"
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
	ptmx      *os.File
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
	
	qs.cmd = exec.CommandContext(qs.ctx, "coco", args...)
	qs.cmd.Dir = qs.workDir
	if len(qs.extraEnv) > 0 {
		qs.cmd.Env = core.MergeEnv(os.Environ(), qs.extraEnv)
	}

	ptmx, err := pty.Start(qs.cmd)
	if err != nil {
		return fmt.Errorf("cocoSession: start pty: %w", err)
	}
	qs.ptmx = ptmx

	qs.wg.Add(1)
	go qs.readLoop(ptmx)
	
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
	_, err := fmt.Fprintf(qs.ptmx, "%s\r", prompt) // 注意这里是 \r 而不是 \n，因为是 PTY
	if err != nil {
		return fmt.Errorf("failed to write to coco pty: %w", err)
	}

	return nil
}

func (qs *cocoSession) readLoop(r io.Reader) {
	defer qs.wg.Done()

	buf := make([]byte, 1024)
	var accumulated bytes.Buffer

	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			accumulated.Write(chunk)
			
			// 发送实时流事件（简化处理）
			select {
			case qs.events <- core.Event{Type: core.EventText, Content: string(chunk)}:
			case <-qs.ctx.Done():
				return
			}
		}

		if err != nil {
			if err != io.EOF {
				slog.Error("cocoSession read error", "error", err)
			}
			break
		}
	}
	
	// 这里通过超时判断或者特定的提示符判断 turn 完成，为了简单起见，暂时在 PTY 断开时发送结果
	finalText := cleanAnsi(accumulated.String())
	select {
	case qs.events <- core.Event{Type: core.EventResult, Content: finalText, SessionID: qs.CurrentSessionID(), Done: true}:
	case <-qs.ctx.Done():
	}
}

func (qs *cocoSession) RespondPermission(requestID string, result core.PermissionResult) error {
	if !qs.alive.Load() {
		return fmt.Errorf("session is closed")
	}
	
	reply := "Y\r"
	if result.Behavior == "deny" {
		reply = "N\r"
	}

	slog.Debug("coco: responding to permission prompt", "requestID", requestID, "behavior", result.Behavior, "reply", strings.TrimSuffix(reply, "\r"))
	_, err := io.WriteString(qs.ptmx, reply)
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
	if qs.ptmx != nil {
		_ = qs.ptmx.Close()
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
