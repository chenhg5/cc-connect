package runtimeclient

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/chenhg5/cc-connect/runtimeprotocol"
	"github.com/gorilla/websocket"
)

type replacementSubscriptionBackend struct {
	core.NativeConversationBackend
	subscription core.NativeConversationSubscription
}

func (b replacementSubscriptionBackend) SubscribeNativeConversation(context.Context, core.Workspace, string) (core.NativeConversationSubscription, error) {
	return b.subscription, nil
}

func TestClientRejectsResponseFromStaleConnectionGeneration(t *testing.T) {
	active := &websocket.Conn{}
	client := &Client{connection: active, generation: 2}

	err := client.writeForConnection(runtimeprotocol.Envelope{RequestID: "request-1", Method: runtimeprotocol.MethodCatalogList}, active, 1)
	if err == nil {
		t.Fatal("旧连接代际的响应不应写入当前连接")
	}
	if client.sequence != 0 {
		t.Fatalf("旧响应改变了当前连接序列: %d", client.sequence)
	}
}

func TestHandlerOldSubscriptionCannotDeleteReplacement(t *testing.T) {
	events := make(chan core.NativeEventEnvelope, 1)
	events <- core.NativeEventEnvelope{Method: "turn/completed", TurnID: "turn-1"}
	close(events)
	var cancelled atomic.Int32
	old := &runtimeSubscription{cancel: func() { cancelled.Add(1) }, done: make(chan struct{})}
	replacement := &runtimeSubscription{cancel: func() {}, done: make(chan struct{})}
	handler := &Handler{
		subscriptions: map[string]*runtimeSubscription{"workspace\x00thread": replacement},
		turnArtifacts: make(map[string][]string), terminalTurns: make(map[string]struct{}),
		emit: func(runtimeprotocol.Method, runtimeprotocol.Resource, json.RawMessage) error { return nil },
	}

	handler.forwardEvents("workspace", "thread", "workspace\x00thread", old, core.NativeConversationSubscription{
		Events: events, Cancel: old.cancel,
	})

	if handler.subscriptions["workspace\x00thread"] != replacement {
		t.Fatal("旧订阅退出时删除了同一会话的新订阅")
	}
	if cancelled.Load() != 1 {
		t.Fatalf("旧订阅取消次数 = %d, want 1", cancelled.Load())
	}
}

func TestHandlerReplacementWaitsForOldSubscriptionPump(t *testing.T) {
	oldEvents := make(chan core.NativeEventEnvelope)
	oldCancelled := make(chan struct{})
	var cancelOld sync.Once
	old := &runtimeSubscription{cancel: func() { cancelOld.Do(func() { close(oldCancelled) }) }, done: make(chan struct{})}
	newEvents := make(chan core.NativeEventEnvelope)
	var cancelNew sync.Once
	backend := replacementSubscriptionBackend{subscription: core.NativeConversationSubscription{
		Generation: 2,
		Events:     newEvents,
		Cancel:     func() { cancelNew.Do(func() { close(newEvents) }) },
	}}
	handler := &Handler{
		dependencies:  Dependencies{Backend: backend},
		subscriptions: map[string]*runtimeSubscription{"workspace\x00thread": old},
		turnArtifacts: make(map[string][]string), terminalTurns: make(map[string]struct{}),
		emit: func(runtimeprotocol.Method, runtimeprotocol.Resource, json.RawMessage) error { return nil },
	}
	go handler.forwardEvents("workspace", "thread", "workspace\x00thread", old, core.NativeConversationSubscription{
		Events: oldEvents,
		Cancel: old.cancel,
	})

	result := make(chan error, 1)
	go func() {
		_, err := handler.subscribe(context.Background(), core.Workspace{Ref: "workspace"}, "thread")
		result <- err
	}()
	select {
	case <-oldCancelled:
	case <-time.After(time.Second):
		t.Fatal("替代订阅没有取消旧事件泵")
	}
	select {
	case err := <-result:
		t.Fatalf("旧事件泵退出前替代订阅已返回: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(oldEvents)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("替代订阅失败: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("旧事件泵退出后替代订阅仍未返回")
	}
	handler.Close()
}

func TestHandlerReleaseConnectionCancelsAndWaitsForSubscriptions(t *testing.T) {
	events := make(chan core.NativeEventEnvelope)
	cancelled := make(chan struct{})
	registered := &runtimeSubscription{cancel: func() { close(cancelled) }, done: make(chan struct{})}
	handler := &Handler{
		subscriptions: map[string]*runtimeSubscription{"workspace\x00thread": registered},
		turnArtifacts: make(map[string][]string), terminalTurns: make(map[string]struct{}),
		emit: func(runtimeprotocol.Method, runtimeprotocol.Resource, json.RawMessage) error { return nil },
	}
	go func() {
		<-cancelled
		close(events)
	}()
	go handler.forwardEvents("workspace", "thread", "workspace\x00thread", registered, core.NativeConversationSubscription{
		Events: events, Cancel: func() {},
	})

	handler.ReleaseConnection()
	select {
	case <-registered.done:
	default:
		t.Fatal("ReleaseConnection 未等待订阅事件泵退出")
	}
	if len(handler.subscriptions) != 0 {
		t.Fatal("ReleaseConnection 后仍保留订阅")
	}
}

func TestHandlerPayloadRejectsTrailingJSON(t *testing.T) {
	if _, err := decodePayload[map[string]any](json.RawMessage(`{} {}`)); err == nil {
		t.Fatal("Runtime payload 接受了尾随 JSON")
	}
}
