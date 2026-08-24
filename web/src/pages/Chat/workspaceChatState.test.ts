import { describe, expect, it } from 'vitest';
import type { NativeConversationSnapshot, WorkspaceChatServerEvent } from '@/api/workspaceChat';
import {
  initialWorkspaceChatStreamState,
  workspaceChatStreamReducer,
} from './workspaceChatState';

const snapshot: NativeConversationSnapshot = {
  thread: {
    id: 'thread-1',
    created_at: '2026-08-23T00:00:00Z',
    updated_at: '2026-08-23T00:00:00Z',
  },
  settings: { revision: 'r1', model: 'gpt-5.6' },
  status: { type: 'idle' },
  usage: { tokenUsage: { total: { totalTokens: 10 } } },
  active_turn: null,
  pending_interactions: [],
  capabilities: {},
  deep_link: 'codex://threads/thread-1',
};

function event(
  sequence: number,
  type: WorkspaceChatServerEvent['type'],
  payload?: unknown,
  epoch = 'epoch-1',
): WorkspaceChatServerEvent {
  return {
    type,
    epoch,
    sequence,
    workspace_ref: 'workspace-1',
    conversation: { kind: 'thread', id: 'thread-1' },
    payload,
    occurred_at: '2026-08-23T00:00:00Z',
  };
}

function stateWithHistory(historySnapshot: NativeConversationSnapshot = snapshot) {
  const subscribed = workspaceChatStreamReducer(initialWorkspaceChatStreamState, {
    type: 'server_event',
    event: event(0, 'subscribed'),
  });
  return workspaceChatStreamReducer(subscribed, {
    type: 'history_loaded', snapshot: historySnapshot, turns: [], itemsByTurn: {},
  });
}

describe('workspaceChatStreamReducer', () => {
	it('历史请求期间收到的 live Turn 和 delta 不会被旧快照覆盖', () => {
		let state = stateWithHistory();
		state = workspaceChatStreamReducer(state, {
			type: 'server_event',
			event: event(1, 'native_event', {
				method: 'turn/started', turn_id: 'turn-live', payload: { requestId: 'request-live' }, occurred_at: '2026-08-23T00:00:01Z',
			}),
		});
		state = workspaceChatStreamReducer(state, {
			type: 'server_event',
			event: event(2, 'native_event', {
				method: 'item/agentMessage/delta', turn_id: 'turn-live', item_id: 'item-live', payload: { delta: 'still live' }, occurred_at: '2026-08-23T00:00:02Z',
			}),
		});
		state = workspaceChatStreamReducer(state, {
			type: 'history_loaded', snapshot, turns: [{ id: 'turn-old', status: 'completed' }],
			itemsByTurn: { 'turn-old': [{ turn_id: 'turn-old', item: { id: 'item-old', type: 'agentMessage', text: 'old' } }] },
			startedEpoch: 'epoch-1', startedSequence: 0,
		});
    expect(state.snapshot?.active_turn?.id).toBe('turn-live');
    expect(state.liveItems['item-live'].text).toBe('still live');
    expect(state.itemsByTurn['turn-old']).toHaveLength(1);
    expect(state.needsHistoryRefresh).toBe(true);
  });

  it('历史请求期间完成的 Turn 会保留二次权威刷新标记', () => {
    let state = stateWithHistory({
      ...snapshot,
      active_turn: { id: 'turn-live', started_at: '2026-08-23T00:00:01Z' },
    });
    state = workspaceChatStreamReducer(state, { type: 'history_refreshed' });
    state = workspaceChatStreamReducer(state, {
      type: 'server_event',
      event: event(1, 'native_event', {
        method: 'turn/completed', turn_id: 'turn-live', payload: {}, occurred_at: '2026-08-23T00:00:02Z',
      }),
    });
    state = workspaceChatStreamReducer(state, {
      type: 'history_loaded', snapshot, turns: [{ id: 'turn-old', status: 'completed' }], itemsByTurn: {},
      startedEpoch: 'epoch-1', startedSequence: 0,
    });

    expect(state.snapshot?.active_turn).toBeNull();
    expect(state.needsHistoryRefresh).toBe(true);
  });

  it('接受初始 subscribed 后同 sequence 的权威 snapshot', () => {
    const subscribed = workspaceChatStreamReducer(initialWorkspaceChatStreamState, {
      type: 'server_event',
      event: event(0, 'subscribed'),
    });
    const loaded = workspaceChatStreamReducer(subscribed, {
      type: 'server_event',
      event: event(0, 'snapshot', snapshot),
    });
    expect(loaded.snapshot?.thread.id).toBe('thread-1');

    const duplicate = workspaceChatStreamReducer(loaded, {
      type: 'server_event',
      event: event(0, 'snapshot', { ...snapshot, deep_link: 'unexpected' }),
    });
    expect(duplicate).toBe(loaded);
  });

  it('新 epoch 使用较小服务端基线并保留同序 resync 控制链', () => {
    let state = workspaceChatStreamReducer(initialWorkspaceChatStreamState, {
      type: 'server_event', event: event(99_999, 'subscribed', undefined, 'epoch-old'),
    });
    state = workspaceChatStreamReducer(state, {
      type: 'server_event', event: event(99_999, 'snapshot', snapshot, 'epoch-old'),
    });
    state = workspaceChatStreamReducer(state, {
      type: 'server_event', event: event(2, 'subscribed', undefined, 'epoch-new'),
    });
    state = workspaceChatStreamReducer(state, {
      type: 'server_event', event: event(2, 'resync_required', { reason: 'event_gap' }, 'epoch-new'),
    });
    expect(state.needsResync).toBe(true);
    state = workspaceChatStreamReducer(state, {
      type: 'server_event', event: event(2, 'snapshot', { ...snapshot, deep_link: 'native://new' }, 'epoch-new'),
    });
    expect(state.epoch).toBe('epoch-new');
    expect(state.sequence).toBe(2);
    expect(state.snapshot?.deep_link).toBe('native://new');
    expect(state.needsResync).toBe(false);

    const protocolError = workspaceChatStreamReducer(state, {
      type: 'server_event',
      event: { ...event(0, 'protocol_error', undefined, ''), error: 'invalid request' },
    });
    expect(protocolError.epoch).toBe('epoch-new');
    expect(protocolError.sequence).toBe(2);
    expect(protocolError.error).toBe('invalid request');
  });

  it('以 snapshot 为权威并忽略重复序号', () => {
    const subscribed = workspaceChatStreamReducer(initialWorkspaceChatStreamState, {
      type: 'server_event',
      event: event(2, 'subscribed'),
    });
    const loaded = workspaceChatStreamReducer(subscribed, {
      type: 'server_event',
      event: event(2, 'snapshot', snapshot),
    });
    expect(loaded.snapshot?.thread.id).toBe('thread-1');

    const stale = workspaceChatStreamReducer(loaded, {
      type: 'server_event',
      event: event(1, 'error', undefined),
    });
    expect(stale).toBe(loaded);
  });

  it('普通事件出现 sequence 缺口时请求 resync 且不推进游标', () => {
    let state = stateWithHistory();
    state = workspaceChatStreamReducer(state, {
      type: 'server_event',
      event: event(1, 'native_event', {
        method: 'item/agentMessage/delta', item_id: 'item-1', payload: { delta: 'first' },
        occurred_at: '2026-08-23T00:00:01Z',
      }),
    });
    const gap = workspaceChatStreamReducer(state, {
      type: 'server_event',
      event: event(3, 'native_event', {
        method: 'item/agentMessage/delta', item_id: 'item-1', payload: { delta: 'skipped' },
        occurred_at: '2026-08-23T00:00:03Z',
      }),
    });

    expect(gap.sequence).toBe(1);
    expect(gap.eventType).toBe('native_event');
    expect(gap.needsResync).toBe(true);
    expect(gap.liveItems['item-1'].text).toBe('first');
  });

  it('投影原生 Turn、delta、用量和终态', () => {
    let state = stateWithHistory();
    state = workspaceChatStreamReducer(state, {
      type: 'server_event',
      event: event(1, 'native_event', {
        method: 'turn/started',
        turn_id: 'turn-1',
        payload: { turn: { id: 'turn-1' } },
        occurred_at: '2026-08-23T00:00:01Z',
      }),
    });
    expect(state.snapshot?.active_turn?.id).toBe('turn-1');

    state = workspaceChatStreamReducer(state, {
      type: 'server_event',
      event: event(2, 'native_event', {
        method: 'item/agentMessage/delta',
        turn_id: 'turn-1',
        item_id: 'item-1',
        payload: { delta: 'hello' },
        occurred_at: '2026-08-23T00:00:02Z',
      }),
    });
    expect(state.liveItems['item-1'].text).toBe('hello');

    state = workspaceChatStreamReducer(state, {
      type: 'server_event',
      event: event(3, 'native_event', {
        method: 'thread/tokenUsage/updated',
        payload: { tokenUsage: { total: { totalTokens: 42 } } },
        occurred_at: '2026-08-23T00:00:03Z',
      }),
    });
    expect(state.snapshot?.usage).toEqual({ tokenUsage: { total: { totalTokens: 42 } } });

    state = workspaceChatStreamReducer(state, {
      type: 'server_event',
      event: event(4, 'native_event', {
        method: 'turn/completed',
        turn_id: 'turn-1',
        payload: {},
        occurred_at: '2026-08-23T00:00:04Z',
      }),
    });
    expect(state.snapshot?.active_turn).toBeNull();
    expect(state.needsHistoryRefresh).toBe(true);
  });

  it('只使用 envelope.settings 物化设置并保留 raw payload', () => {
    const loaded = stateWithHistory();
    const rawPayload = { threadSettings: { model: 'raw-upstream-model' } };
    const state = workspaceChatStreamReducer(loaded, {
      type: 'server_event',
      event: event(1, 'native_event', {
        method: 'thread/settings/updated',
        settings: { revision: 'r2', model: 'gpt-5.6', effort: 'high' },
        payload: rawPayload,
        occurred_at: '2026-08-23T00:00:04Z',
      }),
    });

    expect(state.snapshot?.settings).toEqual({ revision: 'r2', model: 'gpt-5.6', effort: 'high' });
    expect(state.nativeEvents[state.nativeEvents.length - 1]?.payload).toEqual(rawPayload);
  });

  it('接受 gap resync 后同 sequence 的独立 snapshot', () => {
    const nextSnapshot = { ...snapshot, status: { type: 'active' } };
    const subscribed = workspaceChatStreamReducer(initialWorkspaceChatStreamState, {
      type: 'server_event',
      event: event(7, 'subscribed'),
    });
    const loaded = workspaceChatStreamReducer(subscribed, {
      type: 'server_event',
      event: event(7, 'snapshot', snapshot),
    });
    const resync = workspaceChatStreamReducer(loaded, {
      type: 'server_event',
      event: event(8, 'resync_required', { reason: 'event_gap' }),
    });
    expect(resync.needsResync).toBe(true);
    expect(resync.snapshot?.status).toEqual({ type: 'idle' });

    const refreshed = workspaceChatStreamReducer(resync, {
      type: 'server_event',
      event: event(8, 'snapshot', nextSnapshot),
    });
    expect(refreshed.needsResync).toBe(false);
    expect(refreshed.snapshot?.status).toEqual({ type: 'active' });

    const staleEpoch = workspaceChatStreamReducer(refreshed, {
      type: 'server_event',
      event: event(9, 'snapshot', snapshot, 'epoch-old'),
    });
    expect(staleEpoch).toBe(refreshed);
  });

  it('从原生 envelope 顶层保留交互 ID 和允许决定', () => {
    const loaded = stateWithHistory();
    const state = workspaceChatStreamReducer(loaded, {
      type: 'server_event',
      event: event(1, 'native_event', {
        method: 'item/commandExecution/requestApproval',
        thread_id: 'thread-1',
        turn_id: 'turn-1',
        item_id: 'item-1',
        interaction_id: 'interaction-1',
        allowed_decisions: ['accept', { acceptWithPolicy: { command: 'git status' } }, 'decline'],
        payload: { command: 'git status' },
        occurred_at: '2026-08-23T00:00:01Z',
      }),
    });
    expect(state.snapshot?.pending_interactions).toMatchObject([{
      id: 'interaction-1',
      allowed_decisions: ['accept', { acceptWithPolicy: { command: 'git status' } }, 'decline'],
    }]);
  });

  it('使用 serverRequest/resolved 清除 pending interaction', () => {
    const pendingSnapshot: NativeConversationSnapshot = {
      ...snapshot,
      pending_interactions: [{
        id: 'interaction-1',
        kind: 'item/commandExecution/requestApproval',
        thread_id: 'thread-1',
        payload: { command: 'git status' },
        occurred_at: '2026-08-23T00:00:00Z',
      }],
    };
    const loaded = stateWithHistory(pendingSnapshot);
    const resolved = workspaceChatStreamReducer(loaded, {
      type: 'server_event',
      event: event(1, 'native_event', {
        method: 'serverRequest/resolved',
        thread_id: 'thread-1',
        interaction_id: 'interaction-1',
        payload: { requestId: 'request-1' },
        occurred_at: '2026-08-23T00:00:01Z',
      }),
    });
    expect(resolved.snapshot?.pending_interactions).toEqual([]);
  });

  it('操作错误只清除匹配 request_id 的乐观输入', () => {
    let state = workspaceChatStreamReducer(initialWorkspaceChatStreamState, {
      type: 'server_event', event: event(0, 'subscribed'),
    });
    state = workspaceChatStreamReducer(state, {
      type: 'optimistic_input', input: { requestId: 'request-1', text: 'first', kind: 'turn_start' },
    });
    state = workspaceChatStreamReducer(state, {
      type: 'optimistic_input', input: { requestId: 'request-2', text: 'second', kind: 'turn_steer' },
    });
    state = workspaceChatStreamReducer(state, {
      type: 'server_event',
      event: { ...event(1, 'error'), request_id: 'request-1', error: 'rejected' },
    });

    expect(state.optimisticInputs).toEqual([{ requestId: 'request-2', text: 'second', kind: 'turn_steer' }]);
    expect(state.error).toBe('rejected');
  });
});
