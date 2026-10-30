package wecom

import (
	"bytes"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func unauthorizedCallback(t *testing.T, id int, chat string) wsFrame {
	t.Helper()
	body := wsMsgCallbackBody{MsgID: fmt.Sprintf("denied-%d", id), ChatID: chat, ChatType: "group", MsgType: "text"}
	body.From.UserID = fmt.Sprintf("blocked-user-%d", id)
	body.Text.Content = "hello"
	return wsCallbackFrame(t, fmt.Sprintf("denied-request-%d", id), body)
}

func blockedUnauthorizedPlatform(t *testing.T, chats int) *WSPlatform {
	t.Helper()
	q := testQuota(t, &quotaRegistry{}, testLimits(1, 100), "bot")
	for i := range chats {
		takeQuota(t, q, quotaTarget{recipient: fmt.Sprintf("blocked-%d", i)}).finish(true)
	}
	p := &WSPlatform{allowFrom: "allowed-user", quota: q}
	t.Cleanup(func() {
		if err := p.Stop(); err != nil {
			t.Error(err)
		}
	})
	return p
}

func unauthorizedWaiters(p *WSPlatform) int {
	p.quota.account.mu.Lock()
	defer p.quota.account.mu.Unlock()
	n := 0
	for _, b := range p.quota.account.buckets {
		n += len(b.queue)
	}
	return n
}

// Count only denial tasks, including the old inline callback goroutine so this
// regression also detects the pre-fix implementation. Ignore runtime workers.
func unauthorizedGoroutines() int {
	n, _ := runtime.GoroutineProfile(nil)
	for {
		records := make([]runtime.StackRecord, n+16)
		var ok bool
		n, ok = runtime.GoroutineProfile(records)
		if !ok {
			continue
		}
		count := 0
		for _, record := range records[:n] {
			frames := runtime.CallersFrames(record.Stack())
			for {
				frame, more := frames.Next()
				if strings.Contains(frame.Function, "(*WSPlatform).replyUnauthorized.func") ||
					strings.Contains(frame.Function, "(*WSPlatform).handleMsgCallback.func1") {
					count++
					break
				}
				if !more {
					break
				}
			}
		}
		return count
	}
}

func captureUnauthorizedWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

func TestWSQuota_UnauthorizedFloodBounded(t *testing.T) {
	for _, chats := range []int{1, 500} {
		t.Run(fmt.Sprintf("conversations_%d", chats), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				logs := captureUnauthorizedWarnings(t)
				p := blockedUnauthorizedPlatform(t, chats)
				before := unauthorizedGoroutines()
				start := time.Now()
				for i := range 500 {
					p.handleFrame(unauthorizedCallback(t, i, fmt.Sprintf("blocked-%d", i%chats)))
				}
				synctest.Wait()
				if elapsed := time.Since(start); elapsed != 0 {
					t.Errorf("callback dispatch waited for quota: %v", elapsed)
				}
				if got := unauthorizedWaiters(p); got != 1 {
					t.Errorf("500 callbacks created %d quota waiters, want 1", got)
				}
				if got := unauthorizedGoroutines() - before; got != 1 {
					t.Errorf("500 callbacks created %d denial goroutines, want 1", got)
				}
				if err := p.Stop(); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				if got := unauthorizedWaiters(p); got != 0 {
					t.Errorf("Stop left %d quota waiters", got)
				}
				if got := unauthorizedGoroutines() - before; got != 0 {
					t.Errorf("Stop left %d denial goroutines", got)
				}
				if logs.Len() != 0 {
					t.Errorf("flood shutdown produced %d warning/error records, want 0", strings.Count(logs.String(), "\n"))
				}
			})
		})
	}
}

func TestWSQuota_UnauthorizedTimeoutReleasesSlot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		logs := captureUnauthorizedWarnings(t)
		p := blockedUnauthorizedPlatform(t, 1)
		p.handleFrame(unauthorizedCallback(t, 0, "blocked-0"))
		synctest.Wait()
		time.Sleep(5*time.Second - time.Nanosecond)
		synctest.Wait()
		if got := unauthorizedWaiters(p); got != 1 {
			t.Fatalf("deadline fired early: waiters=%d", got)
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if got := unauthorizedWaiters(p); got != 0 {
			t.Fatalf("deadline left %d waiters", got)
		}
		if got := unauthorizedGoroutines(); got != 0 {
			t.Fatalf("deadline left %d denial goroutines", got)
		}
		p.handleFrame(unauthorizedCallback(t, 1, "blocked-0"))
		synctest.Wait()
		if got := unauthorizedWaiters(p); got != 1 {
			t.Fatalf("new notice could not reuse slot: waiters=%d", got)
		}
		// Once the minute window opens, neither expired notice may attempt a send.
		// There is no connection, so a late attempt would log a send failure.
		time.Sleep(time.Minute)
		synctest.Wait()
		if got := unauthorizedWaiters(p); got != 0 {
			t.Fatalf("expired notice remained queued: %d", got)
		}
		if logs.Len() != 0 {
			t.Fatalf("expired notice attempted a late send or logged an error: %s", logs.String())
		}
	})
}

func TestWSQuota_UnauthorizedSendFailureReleasesSlot(t *testing.T) {
	for _, disabledQuota := range []bool{false, true} {
		t.Run(fmt.Sprintf("disabled_quota_%t", disabledQuota), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				logs := captureUnauthorizedWarnings(t)
				p := &WSPlatform{allowFrom: "allowed-user"}
				if disabledQuota {
					p.quota = testQuota(t, &quotaRegistry{}, outboundQuotaConfig{}, "bot")
				}
				for i := range 3 {
					p.handleFrame(unauthorizedCallback(t, i, "chat"))
					synctest.Wait()
					if got := p.reqSeq.Load(); got != int64(i+1) {
						t.Fatalf("failed send did not release slot: attempts=%d", got)
					}
					if got := strings.Count(logs.String(), `"level":"WARN"`); got != i+1 {
						t.Fatalf("want one warning per failed send, got %d", got)
					}
				}
				if strings.Contains(logs.String(), `"level":"ERROR"`) {
					t.Fatal("denial also logged an ordinary Reply error")
				}
				if err := p.Stop(); err != nil {
					t.Fatal(err)
				}
				p.handleFrame(unauthorizedCallback(t, 3, "chat"))
				synctest.Wait()
				if got := p.reqSeq.Load(); got != 3 {
					t.Fatalf("admitted a notice after Stop: attempts=%d", got)
				}
			})
		})
	}
}

func TestWSQuota_UnauthorizedStopRacesFlood(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		logs := captureUnauthorizedWarnings(t)
		p := blockedUnauthorizedPlatform(t, 1)
		p.handleFrame(unauthorizedCallback(t, 0, "blocked-0"))
		synctest.Wait()
		start := make(chan struct{})
		var wg sync.WaitGroup
		for worker := range 10 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for i := range 50 {
					p.handleFrame(unauthorizedCallback(t, 1+worker*50+i, "blocked-0"))
				}
			}()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := p.Stop(); err != nil {
				t.Error(err)
			}
		}()
		close(start)
		wg.Wait()
		synctest.Wait()
		if err := p.Stop(); err != nil {
			t.Fatal(err)
		}
		before := p.reqSeq.Load()
		for i := range 500 {
			p.handleFrame(unauthorizedCallback(t, i+501, "blocked-0"))
		}
		synctest.Wait()
		if got := p.reqSeq.Load(); got != before {
			t.Fatalf("Stop allowed %d new notices", got-before)
		}
		if got := unauthorizedWaiters(p); got != 0 {
			t.Errorf("Stop left %d waiters", got)
		}
		if got := unauthorizedGoroutines(); got != 0 {
			t.Errorf("Stop left %d denial goroutines", got)
		}
		if logs.Len() != 0 {
			t.Errorf("Stop/flood race logged unexpected warnings: %s", logs.String())
		}
	})
}

func TestWSQuota_UnauthorizedFloodBoundedWithoutQuota(t *testing.T) {
	for _, disabledQuota := range []bool{false, true} {
		t.Run(fmt.Sprintf("disabled_quota_%t", disabledQuota), func(t *testing.T) {
			p := &WSPlatform{allowFrom: "allowed-user"}
			if disabledQuota {
				p.quota = testQuota(t, &quotaRegistry{}, outboundQuotaConfig{}, "bot")
			}
			// A busy writer holds the task open even with quota enforcement disabled.
			// The read loop must still reject the flood without spawning more tasks.
			func() {
				p.mu.Lock()
				defer p.mu.Unlock()
				done := make(chan struct{})
				go func() {
					defer close(done)
					for i := range 500 {
						p.handleFrame(unauthorizedCallback(t, i, "chat"))
					}
				}()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Fatal("callback dispatch blocked behind writer")
				}
				if got := unauthorizedGoroutines(); got != 1 {
					t.Errorf("flood with busy writer created %d tasks, want 1", got)
				}
				// Cancel while the writer is blocked; the context must be checked again
				// before writing once the lock becomes available.
				p.unauthorizedMu.Lock()
				cancel := p.unauthorizedCancel
				p.unauthorizedMu.Unlock()
				if cancel == nil {
					t.Fatal("denial task was not admitted")
				}
				cancel()
			}()
			if err := p.Stop(); err != nil {
				t.Fatal(err)
			}
			waitUnauthorizedIdle(t, p)
		})
	}
}

func waitUnauthorizedIdle(t *testing.T, p *WSPlatform) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		p.unauthorizedMu.Lock()
		idle := p.unauthorizedCancel == nil
		p.unauthorizedMu.Unlock()
		if idle {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("denial task did not release its slot")
}
