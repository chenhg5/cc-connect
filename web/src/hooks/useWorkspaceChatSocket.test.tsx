import { act, render, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import type { WorkspaceChatServerEvent } from '@/api/workspaceChat';
import { useWorkspaceChatSocket } from './useWorkspaceChatSocket';

class FakeWebSocket {
  static readonly OPEN = 1;
  static instances: FakeWebSocket[] = [];
  readyState = 0;
  sent: string[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;

  constructor(public readonly url: string) {
    FakeWebSocket.instances.push(this);
  }

  open() {
    this.readyState = FakeWebSocket.OPEN;
    this.onopen?.();
  }

  emit(value: unknown) {
    this.onmessage?.({ data: JSON.stringify(value) } as MessageEvent);
  }

  send(value: string) {
    this.sent.push(value);
  }

  close() {
    this.readyState = 3;
  }
}

function Harness({ onEvent, onError, conversationID = 'thread-1' }: {
  onEvent: (event: WorkspaceChatServerEvent) => void;
  onError: (error: Error) => void;
  conversationID?: string;
}) {
  const socket = useWorkspaceChatSocket({
    workspaceRef: 'workspace-1',
    conversation: { kind: 'thread', id: conversationID },
    onEvent,
    onProtocolError: onError,
  });
  return <span>{socket.status}</span>;
}

function event(
  sequence: number,
  type: WorkspaceChatServerEvent['type'] = 'native_event',
  epoch = 'epoch-1',
): WorkspaceChatServerEvent {
  return {
    type,
    epoch,
    sequence,
    workspace_ref: 'workspace-1',
    conversation: { kind: 'thread', id: 'thread-1' },
    payload: { method: 'turn/started', payload: {}, occurred_at: '2026-08-23T00:00:00Z' },
    occurred_at: '2026-08-23T00:00:00Z',
  };
}

describe('useWorkspaceChatSocket', () => {
  it('使用未推进的游标重新订阅首事件异常和 sequence 缺口', async () => {
    FakeWebSocket.instances = [];
    vi.stubGlobal('WebSocket', FakeWebSocket);
    const onEvent = vi.fn();
    const onError = vi.fn();
    const view = render(<Harness onEvent={onEvent} onError={onError} />);
    const socket = FakeWebSocket.instances[0];
    act(() => socket.open());
    await waitFor(() => expect(socket.sent.length).toBe(1));
    expect(JSON.parse(socket.sent[0])).toMatchObject({
      type: 'subscribe',
      workspace_ref: 'workspace-1',
      conversation: { kind: 'thread', id: 'thread-1' },
    });
    expect(JSON.parse(socket.sent[0])).not.toHaveProperty('thread_id');

    act(() => socket.emit(event(2)));
    expect(onEvent).not.toHaveBeenCalled();
    expect(socket.sent).toHaveLength(2);
    expect(JSON.parse(socket.sent[1])).not.toHaveProperty('after_sequence');

    act(() => {
      socket.emit(event(0, 'subscribed'));
      socket.emit(event(1));
      socket.emit(event(1));
      socket.emit(event(3));
    });
    expect(onEvent.mock.calls.map(([value]) => [value.type, value.sequence])).toEqual([
      ['subscribed', 0],
      ['native_event', 1],
    ]);
    expect(socket.sent).toHaveLength(3);
    expect(JSON.parse(socket.sent[2])).toMatchObject({
      type: 'subscribe',
      after_epoch: 'epoch-1',
      after_sequence: 1,
    });

    act(() => {
      socket.emit(event(1, 'subscribed'));
      socket.emit(event(2));
      socket.emit(event(1));
    });
    expect(onEvent.mock.calls.map(([value]) => [value.type, value.sequence])).toEqual([
      ['subscribed', 0],
      ['native_event', 1],
      ['subscribed', 1],
      ['native_event', 2],
    ]);

    act(() => socket.emit({ type: ['agent', 'event'].join('_'), legacy_payload: 'legacy' }));
    expect(onError).toHaveBeenCalledWith(expect.objectContaining({ message: 'workspace_chat_invalid_event' }));
    view.unmount();
  });

  it('传递与控制事件同 sequence 的 snapshot 并拒绝旧游标', async () => {
    FakeWebSocket.instances = [];
    vi.stubGlobal('WebSocket', FakeWebSocket);
    const onEvent = vi.fn();
    const view = render(<Harness onEvent={onEvent} onError={vi.fn()} />);
    const socket = FakeWebSocket.instances[0];
    act(() => socket.open());
    await waitFor(() => expect(socket.sent.length).toBe(1));

    act(() => {
      socket.emit(event(0, 'subscribed'));
      socket.emit(event(0, 'snapshot'));
      socket.emit(event(5, 'resync_required'));
      socket.emit(event(5, 'snapshot'));
      socket.emit(event(4, 'snapshot'));
      socket.emit(event(6, 'snapshot', 'epoch-old'));
      socket.emit(event(2, 'subscribed', 'epoch-2'));
      socket.emit(event(2, 'resync_required', 'epoch-2'));
      socket.emit(event(2, 'snapshot', 'epoch-2'));
      socket.emit(event(0, 'protocol_error', ''));
      socket.emit(event(3, 'native_event', 'epoch-2'));
    });
    expect(onEvent.mock.calls.map(([value]) => [value.type, value.sequence])).toEqual([
      ['subscribed', 0],
      ['snapshot', 0],
      ['resync_required', 5],
      ['snapshot', 5],
      ['subscribed', 2],
      ['resync_required', 2],
      ['snapshot', 2],
      ['protocol_error', 0],
      ['native_event', 3],
    ]);
    view.unmount();
  });

  it('切换会话后忽略已销毁 Socket 排队到达的旧物化事件', async () => {
    FakeWebSocket.instances = [];
    vi.stubGlobal('WebSocket', FakeWebSocket);
    const onEvent = vi.fn();
    const onError = vi.fn();
    const view = render(<Harness onEvent={onEvent} onError={onError} conversationID="draft-1" />);
    const oldSocket = FakeWebSocket.instances[0];
    act(() => oldSocket.open());
    await waitFor(() => expect(oldSocket.sent.length).toBe(1));

    view.rerender(<Harness onEvent={onEvent} onError={onError} conversationID="draft-2" />);
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(2));
    act(() => oldSocket.emit({
      ...event(1, 'thread_materialized'),
      thread_id: 'thread-from-old-draft',
      conversation: { kind: 'thread', id: 'thread-from-old-draft' },
    }));

    expect(onEvent).not.toHaveBeenCalled();
    expect(onError).not.toHaveBeenCalled();
    view.unmount();
  });
});
