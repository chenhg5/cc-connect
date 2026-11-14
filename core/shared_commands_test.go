package core

import (
	"strings"
	"testing"
	"time"
)

func TestSharedCancelFailureDoesNotClearHistoryOrCreateSession(t *testing.T) {
	env, _, as := newSharedSettingsEnv(t)
	as.mu.Lock()
	as.interruptError = true
	as.mu.Unlock()
	env.send("/cancel")
	env.await("simulated interrupt failure")
	assertSharedSettingsPreserved(t, env, as)
}

func TestSharedDetachDuringForegroundLeavesTurnAndDropsQueuedInput(t *testing.T) {
	env, a := newSharedCompatEnv(t)
	env.send("FIRST QUESTION")
	call := awaitSharedCompatCall(t, a)
	env.send("QUEUED QUESTION")
	env.send("/detach")
	env.await(env.e.i18n.T(MsgSharedDetached))
	if call.session.RuntimeState().TurnID != call.turn {
		t.Fatal("detach interrupted daemon turn")
	}
	call.finish("AFTER DETACH")
	time.Sleep(10 * time.Millisecond)
	if strings.Contains(env.visible(), "AFTER DETACH") {
		t.Fatal("detached observer delivered result")
	}
	select {
	case extra := <-a.calls:
		t.Fatalf("queued turn started after detach: %v", extra)
	default:
	}
}
