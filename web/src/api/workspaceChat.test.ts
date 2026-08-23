import { describe, expect, it, vi } from 'vitest';
import {
  classifyWorkspaceChatServerEvent,
  collectAllPages,
  conversationPath,
  isWorkspaceChatServerEvent,
  normalizeNativeRuntimeCatalog,
} from './workspaceChat';

describe('workspaceChat protocol helpers', () => {
  it('完整读取所有分页且不截断', async () => {
    const load = vi.fn(async (cursor?: string) => cursor
      ? { data: [3], next_cursor: undefined }
      : { data: [1, 2], next_cursor: 'next' });
    await expect(collectAllPages(load)).resolves.toEqual([1, 2, 3]);
    expect(load).toHaveBeenCalledTimes(2);
  });

  it('拒绝循环 cursor', async () => {
    await expect(collectAllPages(async () => ({ data: [], next_cursor: 'same' }))).rejects.toThrow('workspace_chat_cursor_cycle');
  });

  it('生成草稿与 thread 的唯一 URL', () => {
    expect(conversationPath('w/1', { kind: 'draft', id: 'd/1' })).toBe('/chat/w%2F1/draft/d%2F1');
    expect(conversationPath('w/1', { kind: 'thread', id: 't/1' })).toBe('/chat/w%2F1/t%2F1');
  });

  it('归一化 Go 零值 catalog 的 null slice', () => {
    const normalized = {
      capabilities: {},
      models: [],
      modes: [],
      permissions: [],
      personalities: [],
      summaries: [],
      voices: { v1: [], v2: [] },
    };
    expect(normalizeNativeRuntimeCatalog({
      capabilities: null,
      models: null,
      modes: null,
      permissions: null,
      personalities: null,
      summaries: null,
      voices: { v1: null, v2: null },
    })).toEqual(normalized);
    expect(normalizeNativeRuntimeCatalog({})).toEqual(normalized);
  });

  it('拒绝非 slice catalog 字段而不静默降级', () => {
    expect(() => normalizeNativeRuntimeCatalog({
      capabilities: {}, models: {}, modes: [], permissions: [], personalities: [], summaries: [],
      voices: { v1: [], v2: [] },
    })).toThrow('workspace_chat_invalid_runtime_catalog: models must be an array');
  });

  it('只接受连续事件并将 sequence 缺口分类为 resync', () => {
    const serverEvent = (sequence: number, type: 'native_event' | 'resync_required' | 'subscribed') => ({
      type,
      epoch: 'epoch-1',
      sequence,
      workspace_ref: 'workspace-1',
      conversation: { kind: 'thread' as const, id: 'thread-1' },
      occurred_at: '2026-08-23T00:00:00Z',
    });
    expect(classifyWorkspaceChatServerEvent({}, serverEvent(2, 'native_event'))).toBe('resync');
    expect(classifyWorkspaceChatServerEvent(
      { epoch: 'epoch-1', sequence: 1, eventType: 'native_event' },
      serverEvent(2, 'native_event'),
    )).toBe('apply');
    expect(classifyWorkspaceChatServerEvent(
      { epoch: 'epoch-1', sequence: 1, eventType: 'native_event' },
      serverEvent(3, 'native_event'),
    )).toBe('resync');
    expect(classifyWorkspaceChatServerEvent(
      { epoch: 'epoch-1', sequence: 1, eventType: 'native_event' },
      serverEvent(3, 'resync_required'),
    )).toBe('apply');
    expect(classifyWorkspaceChatServerEvent(
      { epoch: 'epoch-1', sequence: 1, eventType: 'native_event' },
      serverEvent(3, 'subscribed'),
    )).toBe('ignore');
  });

  it('只接受当前服务端事件封装', () => {
    expect(isWorkspaceChatServerEvent({
      type: 'native_event', epoch: 'e', sequence: 1, workspace_ref: 'w',
      conversation: { kind: 'thread', id: 't' }, occurred_at: 'now',
      payload: { method: 'turn/started', payload: { turn: { id: 'turn-1' } }, occurred_at: 'now' },
    })).toBe(true);
    expect(isWorkspaceChatServerEvent({
      type: 'native_event', epoch: 'e', sequence: 1, workspace_ref: 'w',
      conversation: { kind: 'thread', id: 't' }, occurred_at: 'now', payload: {},
    })).toBe(false);
    expect(isWorkspaceChatServerEvent({
      type: 'protocol_error', epoch: '', sequence: 0, workspace_ref: 'w',
      conversation: { kind: 'thread', id: 't' }, occurred_at: 'now', error: 'invalid request',
    })).toBe(true);
    const legacyType = ['agent', 'event'].join('_');
    expect(isWorkspaceChatServerEvent({ type: legacyType, legacy_payload: 'legacy' })).toBe(false);
  });
});
