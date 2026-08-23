package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	errTurnLifecyclePumpRunning = errors.New("turn lifecycle pump is already running")
	errTurnLifecyclePumpClosed  = errors.New("turn lifecycle pump owner is closed")
	errTurnLifecyclePumpTimeout = errors.New("turn lifecycle pump stop timed out")
)

type turnLifecyclePumpRun struct {
	cancel context.CancelFunc
	done   chan struct{}
}

type turnLifecycleEventExit uint8

const (
	turnLifecycleEventContextDone turnLifecycleEventExit = iota
	turnLifecycleEventSourceClosed
	turnLifecycleEventHandlerStopped
)

type turnLifecycleEventCallbacks[T any] struct {
	Handle  func(T) bool
	Handoff func(T)
	Cleanup func(turnLifecycleEventExit)
}

// runTurnLifecycleEventPump 统一事件 channel 的取消仲裁和退出清理。
// Handle 返回 false 时由业务处理器主动结束；若取消与事件同时就绪，已经取出的
// 事件交给 Handoff 收口，避免审批等不可重放事件在所有权切换时丢失。
func runTurnLifecycleEventPump[T any](ctx context.Context, events <-chan T, callbacks turnLifecycleEventCallbacks[T]) (exit turnLifecycleEventExit) {
	if callbacks.Cleanup != nil {
		defer func() { callbacks.Cleanup(exit) }()
	}
	for {
		select {
		case <-ctx.Done():
			return turnLifecycleEventContextDone
		case event, ok := <-events:
			if !ok {
				return turnLifecycleEventSourceClosed
			}
			select {
			case <-ctx.Done():
				if callbacks.Handoff != nil {
					callbacks.Handoff(event)
				}
				return turnLifecycleEventContextDone
			default:
			}
			if callbacks.Handle == nil || !callbacks.Handle(event) {
				return turnLifecycleEventHandlerStopped
			}
		}
	}
}

// turnLifecyclePumpOwner 只管理一个事件泵的 goroutine、context 和退出等待。
// 事件解释、终态发布和其他业务清理由调用方继续拥有。
type turnLifecyclePumpOwner struct {
	mu     sync.Mutex
	active *turnLifecyclePumpRun
	closed bool
}

func (o *turnLifecyclePumpOwner) Start(parent context.Context, pump func(context.Context)) error {
	if parent == nil {
		return fmt.Errorf("turn lifecycle pump: parent context is required")
	}
	if pump == nil {
		return fmt.Errorf("turn lifecycle pump: pump function is required")
	}

	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return errTurnLifecyclePumpClosed
	}
	if o.active != nil {
		o.mu.Unlock()
		return errTurnLifecyclePumpRunning
	}

	ctx, cancel := context.WithCancel(parent)
	run := &turnLifecyclePumpRun{cancel: cancel, done: make(chan struct{})}
	o.active = run
	o.mu.Unlock()

	go func() {
		defer func() {
			cancel()
			o.mu.Lock()
			if o.active == run {
				o.active = nil
			}
			close(run.done)
			o.mu.Unlock()
		}()
		pump(ctx)
	}()
	return nil
}

func (o *turnLifecyclePumpOwner) Stop(timeout time.Duration) error {
	o.mu.Lock()
	run := o.active
	if run != nil {
		run.cancel()
	}
	o.mu.Unlock()
	return waitTurnLifecyclePump(run, timeout)
}

func (o *turnLifecyclePumpOwner) Close(timeout time.Duration) error {
	o.mu.Lock()
	o.closed = true
	run := o.active
	if run != nil {
		run.cancel()
	}
	o.mu.Unlock()
	return waitTurnLifecyclePump(run, timeout)
}

func (o *turnLifecyclePumpOwner) activeDone() <-chan struct{} {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.active == nil {
		return nil
	}
	return o.active.done
}

func waitTurnLifecyclePump(run *turnLifecyclePumpRun, timeout time.Duration) error {
	if run == nil {
		return nil
	}
	if timeout <= 0 {
		select {
		case <-run.done:
			return nil
		default:
			return fmt.Errorf("%w after %s", errTurnLifecyclePumpTimeout, timeout)
		}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-run.done:
		return nil
	case <-timer.C:
		return fmt.Errorf("%w after %s", errTurnLifecyclePumpTimeout, timeout)
	}
}
