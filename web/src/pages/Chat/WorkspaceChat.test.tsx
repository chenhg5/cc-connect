import { act, fireEvent, render, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { MemoryRouter, Route, Routes, useNavigate } from 'react-router-dom';
import WorkspaceChat from './WorkspaceChat';

const mocks = vi.hoisted(() => ({
  send: vi.fn((_request: unknown) => true),
  listWorkspaces: vi.fn(),
  listWorkspaceThreads: vi.fn(),
  createWorkspaceDraft: vi.fn(),
  readWorkspaceDraft: vi.fn(),
  readWorkspaceThread: vi.fn(),
  listWorkspaceTurns: vi.fn(),
  listWorkspaceItems: vi.fn(),
  getWorkspaceRuntimeCatalog: vi.fn(),
  getWorkspaceChatSelection: vi.fn(),
  putWorkspaceChatSelection: vi.fn(),
  updateWorkspaceDraftSettings: vi.fn(),
  updateWorkspaceThreadSettings: vi.fn(),
  writeClipboard: vi.fn(),
  socketEvent: vi.fn(),
}));

vi.mock('@/api/workspaceChat', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/workspaceChat')>();
  return {
    ...actual,
    ...mocks,
    getWorkspaceRuntimeCatalog: async (workspaceRef: string) =>
      actual.normalizeNativeRuntimeCatalog(await mocks.getWorkspaceRuntimeCatalog(workspaceRef)),
  };
});

vi.mock('@/hooks/useWorkspaceChatSocket', () => ({
  useWorkspaceChatSocket: ({ onEvent }: { onEvent: (event: unknown) => void }) => {
    mocks.socketEvent.mockImplementation(onEvent);
    return { status: 'connected', send: mocks.send };
  },
}));

vi.mock('@/hooks/useWorkspaceRealtime', () => ({
  useWorkspaceRealtime: () => ({
    status: 'idle',
    error: '',
    audioRef: { current: null },
    start: vi.fn(),
    stop: vi.fn(),
    appendText: vi.fn(),
    handleNativeEvent: vi.fn(),
    handleRequestError: vi.fn(),
  }),
}));

const workspace = {
  ref: 'workspace-1',
  device_id: 'device-1',
  device_name: 'Mac One',
  project_id: 'project-1',
  project_name: 'Project One',
  root_index: 0,
  root_name: 'Project One',
  available: true,
  online: true,
  order: 0,
};

const catalog = {
  capabilities: { realtime: { supported: true } },
  models: [{
    id: 'gpt-5.6', model: 'gpt-5.6', display_name: 'GPT-5.6', hidden: false, default: true,
    default_reasoning_effort: 'medium', reasoning_efforts: [{ effort: 'medium' }, { effort: 'high' }],
    input_modalities: ['text'], supports_personality: true, service_tiers: [{ id: 'priority', name: 'Priority' }],
  }],
  modes: [{ name: 'default', mode: 'default' }, { name: 'plan', mode: 'plan' }],
  permissions: [{ id: 'workspace-write', allowed: true }],
  personalities: ['pragmatic'],
  summaries: ['auto'],
  voices: { v1: [], v2: ['alloy'], default_v2: 'alloy' },
};

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

function threadSnapshot(threadID: string, messageSettings: Record<string, unknown> = {}) {
  return {
    thread: {
      id: threadID, name: threadID,
      created_at: '2026-08-23T00:00:00Z', updated_at: '2026-08-23T00:00:00Z',
    },
    settings: { revision: 'r1', model: 'gpt-5.6', effort: 'medium', ...messageSettings },
    status: { type: 'idle' },
    usage: { tokenUsage: { total: { totalTokens: 12 } } },
    active_turn: null,
    pending_interactions: [],
    capabilities: {},
    deep_link: `codex://threads/${threadID}`,
  };
}

function NavigationControls() {
  const navigate = useNavigate();
  return (
    <>
      <button type="button" onClick={() => navigate('/chat/workspace-1/thread-2')}>go thread 2</button>
      <button type="button" onClick={() => navigate('/chat/workspace-1/draft/draft-2')}>go draft 2</button>
      <button type="button" onClick={() => navigate('/chat/workspace-2/thread-2')}>go workspace 2</button>
    </>
  );
}

function renderChat(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <NavigationControls />
      <Routes>
        <Route path="/chat/:workspaceRef/draft/:draftRef" element={<WorkspaceChat />} />
        <Route path="/chat/:workspaceRef/:threadId" element={<WorkspaceChat />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe('WorkspaceChat', () => {
  beforeEach(() => {
    mocks.send.mockReset().mockReturnValue(true);
    mocks.socketEvent.mockReset();
    mocks.listWorkspaces.mockResolvedValue({ workspaces: [workspace] });
    mocks.listWorkspaceThreads.mockResolvedValue({ data: [] });
    mocks.getWorkspaceRuntimeCatalog.mockResolvedValue(catalog);
    mocks.getWorkspaceChatSelection.mockResolvedValue(null);
    mocks.putWorkspaceChatSelection.mockResolvedValue({
      client_id: 'web:admin', workspace_ref: workspace.ref,
      conversation: { kind: 'draft', id: 'draft-1' }, updated_at: '2026-08-23T00:00:00Z',
    });
    mocks.readWorkspaceDraft.mockResolvedValue({
      id: 'draft-1', owner_client_id: 'web:admin', workspace_ref: workspace.ref,
      state: 'draft', settings_patch: {}, created_at: '2026-08-23T00:00:00Z', updated_at: '2026-08-23T00:00:00Z',
    });
    mocks.readWorkspaceThread.mockReset();
    mocks.listWorkspaceTurns.mockResolvedValue({ data: [] });
    mocks.listWorkspaceItems.mockResolvedValue({ data: [] });
    mocks.createWorkspaceDraft.mockResolvedValue({
      id: 'draft-2', owner_client_id: 'web:admin', workspace_ref: workspace.ref,
      state: 'draft', settings_patch: {}, created_at: '2026-08-23T00:00:00Z', updated_at: '2026-08-23T00:00:00Z',
    });
    mocks.updateWorkspaceDraftSettings.mockReset().mockResolvedValue({
      id: 'draft-1', owner_client_id: 'web:admin', workspace_ref: workspace.ref,
	  state: 'draft', settings_patch: { mode: 'plan', plan_effort: 'high' },
      created_at: '2026-08-23T00:00:00Z', updated_at: '2026-08-23T00:00:01Z',
    });
    mocks.updateWorkspaceThreadSettings.mockResolvedValue({ revision: 'r2', model: 'gpt-5.6', effort: 'high' });
    mocks.writeClipboard.mockReset().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: mocks.writeClipboard },
    });
  });

  it('草稿设置持久化成功后采用服务端返回值并随首个 turn_start 提交', async () => {
    const view = renderChat('/chat/workspace-1/draft/draft-1');
    const plan = await view.findByRole('button', { name: 'Plan' });
    await waitFor(() => expect((plan as HTMLButtonElement).disabled).toBe(false));
    expect(view.queryByText('Conversation name (optional)')).toBeNull();
    fireEvent.click(plan);
    await waitFor(() => expect(mocks.updateWorkspaceDraftSettings).toHaveBeenCalledWith(
      'workspace-1', 'draft-1', { mode: 'plan' },
    ));
    await waitFor(() => expect(plan.getAttribute('aria-pressed')).toBe('true'));
    expect((view.getByTitle('Reasoning effort') as HTMLSelectElement).value).toBe('high');
    const input = view.getByPlaceholderText('Message Codex...');
    fireEvent.change(input, { target: { value: 'Inspect this repository' } });
    fireEvent.click(view.getByRole('button', { name: 'Send message' }));
    expect(mocks.send).toHaveBeenCalledWith(expect.objectContaining({
      type: 'turn_start',
      workspace_ref: 'workspace-1',
      conversation: { kind: 'draft', id: 'draft-1' },
      input: [{ type: 'text', text: 'Inspect this repository' }],
	  payload: { settings: { mode: 'plan', plan_effort: 'high' } },
    }));
    expect(mocks.send.mock.calls[0][0]).not.toHaveProperty('content');
  });

  it('刷新草稿时从服务端 settings_patch 恢复设置', async () => {
    mocks.readWorkspaceDraft.mockResolvedValue({
      id: 'draft-1', owner_client_id: 'web:admin', workspace_ref: workspace.ref,
      state: 'draft',
	  settings_patch: { model: 'gpt-5.6', effort: 'medium', plan_effort: 'high', mode: 'plan', permission_profile: 'workspace-write' },
      created_at: '2026-08-23T00:00:00Z', updated_at: '2026-08-23T00:00:01Z',
    });

    const view = renderChat('/chat/workspace-1/draft/draft-1');
    const plan = await view.findByRole('button', { name: 'Plan' });
    await waitFor(() => expect(plan.getAttribute('aria-pressed')).toBe('true'));
    expect((view.getByTitle('Reasoning effort') as HTMLSelectElement).value).toBe('high');
    expect((view.getByTitle('Permission profile') as HTMLSelectElement).value).toBe('workspace-write');
  });

  it('刷新已物化草稿时校验真实 thread 并替换选择与 URL', async () => {
    mocks.readWorkspaceDraft.mockResolvedValue({
      id: 'draft-1', owner_client_id: 'web:admin', workspace_ref: workspace.ref,
      state: 'materialized', thread_id: 'thread-1', settings_patch: {},
      created_at: '2026-08-23T00:00:00Z', updated_at: '2026-08-23T00:00:01Z',
    });
    mocks.listWorkspaceThreads.mockResolvedValue({ data: [threadSnapshot('thread-1').thread] });
    mocks.readWorkspaceThread.mockResolvedValue(threadSnapshot('thread-1'));

    const view = renderChat('/chat/workspace-1/draft/draft-1');

    await waitFor(() => expect(mocks.putWorkspaceChatSelection).toHaveBeenCalledWith(
      'workspace-1', { kind: 'thread', id: 'thread-1' },
    ));
    expect(mocks.putWorkspaceChatSelection).not.toHaveBeenCalledWith(
      'workspace-1', { kind: 'draft', id: 'draft-1' },
    );
    expect(await view.findByPlaceholderText('Message Codex...')).toBeTruthy();
    expect(mocks.readWorkspaceThread).toHaveBeenCalledWith('workspace-1', 'thread-1');
  });

  it('草稿首 Turn 接收 thread 身份的物化事件后切换到真实会话', async () => {
    mocks.listWorkspaceThreads.mockResolvedValue({ data: [threadSnapshot('thread-1').thread] });
    mocks.readWorkspaceThread.mockResolvedValue(threadSnapshot('thread-1'));
    const view = renderChat('/chat/workspace-1/draft/draft-1');
    const input = await view.findByPlaceholderText('Message Codex...');
    fireEvent.change(input, { target: { value: 'Materialize this draft' } });
    fireEvent.click(view.getByRole('button', { name: 'Send message' }));
    const request = mocks.send.mock.calls
      .map(([value]) => value as { type: string; request_id: string })
      .find((value) => value.type === 'turn_start');
    if (!request) throw new Error('turn_start request was not sent');

    act(() => mocks.socketEvent({
      type: 'thread_materialized', epoch: 'epoch-1', sequence: 1,
      workspace_ref: 'workspace-1', conversation: { kind: 'thread', id: 'thread-1' },
      thread_id: 'thread-1', turn_id: 'turn-1', request_id: request.request_id,
      occurred_at: '2026-08-23T00:00:01Z',
    }));

    await waitFor(() => expect(mocks.readWorkspaceThread).toHaveBeenCalledWith('workspace-1', 'thread-1'));
    await waitFor(() => expect(mocks.putWorkspaceChatSelection).toHaveBeenCalledWith(
      'workspace-1', { kind: 'thread', id: 'thread-1' },
    ));
  });

  it('草稿物化切换后再次读取权威历史收口首 Turn 终态', async () => {
    const active = {
      ...threadSnapshot('thread-1'),
      active_turn: {
        id: 'turn-1',
        started_at: '2026-08-23T00:00:01Z',
      },
    };
    mocks.listWorkspaceThreads.mockResolvedValue({ data: [active.thread] });
    mocks.readWorkspaceThread
      .mockResolvedValueOnce(active)
      .mockResolvedValue(threadSnapshot('thread-1'));
    mocks.listWorkspaceTurns.mockResolvedValue({ data: [{ id: 'turn-1', status: 'completed' }] });

    const view = renderChat('/chat/workspace-1/draft/draft-1');
    const input = await view.findByPlaceholderText('Message Codex...');
    fireEvent.change(input, { target: { value: 'Materialize this draft' } });
    fireEvent.click(view.getByRole('button', { name: 'Send message' }));
    const request = mocks.send.mock.calls
      .map(([value]) => value as { type: string; request_id: string })
      .find((value) => value.type === 'turn_start');
    if (!request) throw new Error('turn_start request was not sent');

    act(() => mocks.socketEvent({
      type: 'thread_materialized', epoch: 'epoch-1', sequence: 1,
      workspace_ref: 'workspace-1', conversation: { kind: 'thread', id: 'thread-1' },
      thread_id: 'thread-1', turn_id: 'turn-1', request_id: request.request_id,
      occurred_at: '2026-08-23T00:00:01Z',
    }));

    await waitFor(() => expect(mocks.readWorkspaceThread.mock.calls.filter(
      ([workspaceRef, threadID]) => workspaceRef === 'workspace-1' && threadID === 'thread-1',
    ).length).toBeGreaterThanOrEqual(2));
    await waitFor(() => expect(view.queryByRole('button', { name: 'Cancel current Turn' })).toBeNull());
  });

  it('Plan 模式显示 collaboration mode 的独立 effort', async () => {
    mocks.listWorkspaceThreads.mockResolvedValue({ data: [threadSnapshot('thread-1').thread] });
    mocks.readWorkspaceThread.mockResolvedValue(threadSnapshot('thread-1', {
      collaboration_mode: {
        mode: 'plan',
        settings: { model: 'gpt-5.6', reasoning_effort: 'high', developer_instructions: null },
      },
    }));

    const view = renderChat('/chat/workspace-1/thread-1');

    await waitFor(() => expect(view.getByRole('button', { name: 'Plan' }).getAttribute('aria-pressed')).toBe('true'));
    expect((view.getByTitle('Reasoning effort') as HTMLSelectElement).value).toBe('high');
  });

  it('只接受 runtime catalog 的精确 realtime capability key', async () => {
    mocks.getWorkspaceRuntimeCatalog.mockResolvedValue({
      ...catalog,
      capabilities: { native_realtime_preview: { supported: true } },
    });
    mocks.listWorkspaceThreads.mockResolvedValue({ data: [threadSnapshot('thread-1').thread] });
    mocks.readWorkspaceThread.mockResolvedValue(threadSnapshot('thread-1'));

    const view = renderChat('/chat/workspace-1/thread-1');
    const voice = await view.findByRole('button', { name: 'Start voice conversation' });

    await waitFor(() => expect((voice as HTMLButtonElement).disabled).toBe(true));
    expect(voice.getAttribute('title')).toBe('Not supported');
  });

  it('realtime 不可用时接受 Go 零值 catalog 且禁用语音与空设置', async () => {
    mocks.getWorkspaceRuntimeCatalog.mockResolvedValue({
      capabilities: { realtime: { supported: false, reason: 'realtime unavailable' } },
      models: null,
      modes: null,
      permissions: null,
      personalities: null,
      summaries: null,
      voices: { v1: null, v2: null },
    });
    mocks.listWorkspaceThreads.mockResolvedValue({ data: [threadSnapshot('thread-1').thread] });
    mocks.readWorkspaceThread.mockResolvedValue(threadSnapshot('thread-1'));

    const view = renderChat('/chat/workspace-1/thread-1');
    const voice = await view.findByRole('button', { name: 'Start voice conversation' });

    expect((voice as HTMLButtonElement).disabled).toBe(true);
    expect(voice.getAttribute('title')).toBe('realtime unavailable');
    expect((view.getByTitle('Model') as HTMLSelectElement).disabled).toBe(true);
    expect((view.getByTitle('Permission profile') as HTMLSelectElement).disabled).toBe(true);
  });

  it('permissions 为 null 时保留其他 catalog 设置并禁用权限选择', async () => {
    mocks.getWorkspaceRuntimeCatalog.mockResolvedValue({
      ...catalog,
      permissions: null,
    });
    mocks.listWorkspaceThreads.mockResolvedValue({ data: [threadSnapshot('thread-1').thread] });
    mocks.readWorkspaceThread.mockResolvedValue(threadSnapshot('thread-1'));

    const view = renderChat('/chat/workspace-1/thread-1');
    const permission = await view.findByTitle('Permission profile');

    expect((permission as HTMLSelectElement).disabled).toBe(true);
    expect((view.getByTitle('Model') as HTMLSelectElement).value).toBe('gpt-5.6');
  });

  it('草稿设置请求失败时显示真实错误且不产生本地假成功', async () => {
    mocks.updateWorkspaceDraftSettings.mockRejectedValue(new Error('workspace chat: invalid draft setting'));
    const view = renderChat('/chat/workspace-1/draft/draft-1');
    const plan = await view.findByRole('button', { name: 'Plan' });
    await waitFor(() => expect((plan as HTMLButtonElement).disabled).toBe(false));

    fireEvent.click(plan);
    expect(await view.findByText('workspace chat: invalid draft setting')).toBeTruthy();
    expect(plan.getAttribute('aria-pressed')).toBe('false');

    const input = view.getByPlaceholderText('Message Codex...');
    fireEvent.change(input, { target: { value: 'Do not use rejected settings' } });
    fireEvent.click(view.getByRole('button', { name: 'Send message' }));
    expect(mocks.send).toHaveBeenCalledWith(expect.not.objectContaining({ payload: expect.anything() }));
  });

  it('活动 Turn 只发送显式 steer 和带准确 ID 的 interrupt', async () => {
    mocks.listWorkspaceThreads.mockResolvedValue({ data: [{
      id: 'thread-1', name: 'Thread One',
      created_at: '2026-08-23T00:00:00Z', updated_at: '2026-08-23T00:00:00Z',
    }] });
    mocks.readWorkspaceThread.mockResolvedValue({
      thread: { id: 'thread-1', name: 'Thread One', created_at: '2026-08-23T00:00:00Z', updated_at: '2026-08-23T00:00:00Z' },
      settings: { revision: 'r1', model: 'gpt-5.6', effort: 'medium' },
      status: { type: 'active' }, usage: { tokenUsage: { total: { totalTokens: 12 } } },
      active_turn: { id: 'turn-active', started_at: '2026-08-23T00:00:00Z' },
      pending_interactions: [], capabilities: {}, deep_link: 'codex://threads/thread-1',
    });
    const view = renderChat('/chat/workspace-1/thread-1');
    const input = await view.findByPlaceholderText('Add instructions to the current turn...');
    fireEvent.change(view.getByTitle('Reasoning effort'), { target: { value: 'high' } });
    await waitFor(() => expect(mocks.updateWorkspaceThreadSettings).toHaveBeenCalledWith('workspace-1', 'thread-1', { effort: 'high' }));
    fireEvent.change(input, { target: { value: 'Focus on the API' } });
    fireEvent.click(view.getByRole('button', { name: 'Steer current turn' }));
    expect(mocks.send).toHaveBeenCalledWith(expect.objectContaining({
      type: 'turn_steer', expected_turn_id: 'turn-active',
      input: [{ type: 'text', text: 'Focus on the API' }],
    }));
    fireEvent.click(view.getByRole('button', { name: 'Cancel current turn' }));
    expect(mocks.send).toHaveBeenCalledWith(expect.objectContaining({
      type: 'turn_interrupt', expected_turn_id: 'turn-active',
    }));
    fireEvent.click(view.getAllByRole('button', { name: 'Copy Codex link' })[0]);
    await waitFor(() => expect(mocks.writeClipboard).toHaveBeenCalledWith('codex://threads/thread-1'));
    fireEvent.click(view.getByRole('button', { name: 'New conversation' }));
    await waitFor(() => expect(mocks.createWorkspaceDraft).toHaveBeenCalledWith('workspace-1'));
    expect(mocks.putWorkspaceChatSelection).not.toHaveBeenCalledWith('workspace-1', { kind: 'draft', id: 'draft-1' });
  });

  it('切换 thread 后不提交旧 thread 的慢历史响应', async () => {
    const slowThread = deferred<ReturnType<typeof threadSnapshot>>();
    const threads = [threadSnapshot('thread-1').thread, threadSnapshot('thread-2').thread];
    mocks.listWorkspaceThreads.mockResolvedValue({ data: threads });
    mocks.readWorkspaceThread.mockImplementation((_workspaceRef: string, threadID: string) =>
      threadID === 'thread-1' ? slowThread.promise : Promise.resolve(threadSnapshot('thread-2')));
    mocks.listWorkspaceTurns.mockImplementation(async (_workspaceRef: string, threadID: string) => ({
      data: [{ id: `turn-${threadID}`, status: 'completed' }],
    }));
    mocks.listWorkspaceItems.mockImplementation(async (_workspaceRef: string, threadID: string) => ({
      data: [{ turn_id: `turn-${threadID}`, item: { id: `item-${threadID}`, type: 'agentMessage', text: `${threadID} response` } }],
    }));

    const view = renderChat('/chat/workspace-1/thread-1');
    await waitFor(() => expect(mocks.readWorkspaceThread).toHaveBeenCalledWith('workspace-1', 'thread-1'));
    fireEvent.click(view.getByRole('button', { name: 'go thread 2' }));
    expect(await view.findByText('thread-2 response')).toBeTruthy();

    await act(async () => slowThread.resolve(threadSnapshot('thread-1')));
    expect(view.queryByText('thread-1 response')).toBeNull();
    expect(view.getByText('thread-2 response')).toBeTruthy();
  });

  it('切换到新草稿时串行提交 selection 并让最新目标最终生效', async () => {
    const slowSelection = deferred<{
      client_id: string;
      workspace_ref: string;
      conversation: { kind: 'thread'; id: string };
      updated_at: string;
    }>();
    const committed: string[] = [];
    mocks.listWorkspaceThreads.mockResolvedValue({ data: [threadSnapshot('thread-1').thread] });
    mocks.readWorkspaceThread.mockResolvedValue(threadSnapshot('thread-1'));
    mocks.readWorkspaceDraft.mockImplementation(async (_workspaceRef: string, draftID: string) => ({
      id: draftID, owner_client_id: 'web:admin', workspace_ref: workspace.ref,
      state: 'draft', settings_patch: {}, created_at: '2026-08-23T00:00:00Z', updated_at: '2026-08-23T00:00:00Z',
    }));
    mocks.putWorkspaceChatSelection.mockImplementation(async (_workspaceRef: string, conversation: { kind: string; id: string }) => {
      if (conversation.id === 'thread-1') {
        const value = await slowSelection.promise;
        committed.push(conversation.id);
        return value;
      }
      committed.push(conversation.id);
      return {
        client_id: 'web:admin', workspace_ref: workspace.ref, conversation,
        updated_at: '2026-08-23T00:00:01Z',
      };
    });

    const view = renderChat('/chat/workspace-1/thread-1');
    await waitFor(() => expect(mocks.putWorkspaceChatSelection).toHaveBeenCalledWith(
      'workspace-1', { kind: 'thread', id: 'thread-1' },
    ));
    fireEvent.click(view.getByRole('button', { name: 'go draft 2' }));
    await waitFor(() => expect(mocks.readWorkspaceDraft).toHaveBeenCalledWith('workspace-1', 'draft-2'));

    await act(async () => slowSelection.resolve({
      client_id: 'web:admin', workspace_ref: workspace.ref,
      conversation: { kind: 'thread', id: 'thread-1' }, updated_at: '2026-08-23T00:00:00Z',
    }));
    await waitFor(() => expect(mocks.putWorkspaceChatSelection).toHaveBeenCalledWith(
      'workspace-1', { kind: 'draft', id: 'draft-2' },
    ));
    expect(committed).toEqual(['thread-1', 'draft-2']);
  });

  it('切换 workspace 后不提交旧 workspace 的慢 thread 列表', async () => {
    const slowThreads = deferred<{ data: ReturnType<typeof threadSnapshot>['thread'][] }>();
    const workspaceTwo = {
      ...workspace,
      ref: 'workspace-2', project_id: 'project-2', project_name: 'Project Two',
      root_name: 'Project Two',
    };
    const currentThread = { ...threadSnapshot('thread-2').thread, name: 'Workspace Two Thread' };
    const staleThread = { ...threadSnapshot('thread-1').thread, name: 'Stale Workspace One Thread' };
    mocks.listWorkspaces.mockResolvedValue({ workspaces: [workspace, workspaceTwo] });
    mocks.listWorkspaceThreads.mockImplementation((workspaceRef: string) =>
      workspaceRef === 'workspace-1' ? slowThreads.promise : Promise.resolve({ data: [currentThread] }));
    mocks.readWorkspaceThread.mockImplementation(async (_workspaceRef: string, threadID: string) => threadSnapshot(threadID));

    const view = renderChat('/chat/workspace-1/thread-1');
    await waitFor(() => expect(mocks.listWorkspaceThreads).toHaveBeenCalledWith(
      'workspace-1', expect.objectContaining({ sortDirection: 'desc' }),
    ));
    fireEvent.click(view.getByRole('button', { name: 'go workspace 2' }));
    expect(await view.findAllByText('Workspace Two Thread')).not.toHaveLength(0);

    await act(async () => slowThreads.resolve({ data: [staleThread] }));
    expect(view.queryByText('Stale Workspace One Thread')).toBeNull();
    expect(view.getAllByText('Workspace Two Thread').length).toBeGreaterThan(0);
  });

  it('展示 protocol_error 并按 request_id 清除乐观输入', async () => {
    mocks.listWorkspaceThreads.mockResolvedValue({ data: [threadSnapshot('thread-1').thread] });
    mocks.readWorkspaceThread.mockResolvedValue(threadSnapshot('thread-1'));
    const view = renderChat('/chat/workspace-1/thread-1');
    const input = await view.findByPlaceholderText('Message Codex...');
    fireEvent.change(input, { target: { value: 'will be rejected' } });
    fireEvent.click(view.getByRole('button', { name: 'Send message' }));
    const request = mocks.send.mock.calls
      .map(([value]) => value as { type: string; request_id: string })
      .find((value) => value.type === 'turn_start');
    if (!request) throw new Error('turn_start request was not sent');
    expect(await view.findByText('will be rejected')).toBeTruthy();

    act(() => mocks.socketEvent({
      type: 'protocol_error', epoch: '', sequence: 0,
      workspace_ref: 'workspace-1', conversation: { kind: 'thread', id: 'thread-1' },
      request_id: request.request_id, error: 'invalid request payload', occurred_at: '2026-08-23T00:00:01Z',
    }));

    expect(await view.findByText('invalid request payload')).toBeTruthy();
    expect(view.queryByText('will be rejected')).toBeNull();
  });

  it('复制非当前 thread 时只使用服务端签发的 deep_link', async () => {
    const threads = [
      { id: 'thread-1', name: 'Thread One', status: { type: 'active' }, created_at: '2026-08-23T00:00:00Z', updated_at: '2026-08-23T00:00:00Z' },
      { id: 'thread-2', name: 'Thread Two', status: { type: 'idle' }, created_at: '2026-08-23T00:00:00Z', updated_at: '2026-08-23T00:00:01Z' },
    ];
    mocks.listWorkspaceThreads.mockResolvedValue({ data: threads });
    mocks.readWorkspaceThread.mockImplementation(async (_workspaceRef: string, threadID: string) => ({
      thread: threads.find((thread) => thread.id === threadID),
      settings: { revision: 'r1', model: 'gpt-5.6', effort: 'medium' },
      status: { type: 'idle' },
      active_turn: null,
      pending_interactions: [],
      capabilities: {},
      deep_link: threadID === 'thread-2'
        ? 'codex://threads/server-signed?workspace=workspace-1&nonce=abc'
        : 'codex://threads/thread-1',
    }));

    const view = renderChat('/chat/workspace-1/thread-1');
    const copyButtons = await view.findAllByRole('button', { name: 'Copy Codex link' });
    expect(view.getAllByText('idle').length).toBeGreaterThan(0);
    fireEvent.click(copyButtons[1]);

    await waitFor(() => expect(mocks.readWorkspaceThread).toHaveBeenCalledWith('workspace-1', 'thread-2'));
    expect(mocks.writeClipboard).toHaveBeenCalledWith('codex://threads/server-signed?workspace=workspace-1&nonce=abc');
  });

  it('读取非当前 thread 深链失败时显示服务端真实错误', async () => {
    const threads = [
      { id: 'thread-1', name: 'Thread One', created_at: '2026-08-23T00:00:00Z', updated_at: '2026-08-23T00:00:00Z' },
      { id: 'thread-2', name: 'Thread Two', created_at: '2026-08-23T00:00:00Z', updated_at: '2026-08-23T00:00:01Z' },
    ];
    mocks.listWorkspaceThreads.mockResolvedValue({ data: threads });
    mocks.readWorkspaceThread
      .mockResolvedValueOnce({
        thread: threads[0],
        settings: { revision: 'r1', model: 'gpt-5.6', effort: 'medium' },
        status: { type: 'idle' },
        active_turn: null,
        pending_interactions: [],
        capabilities: {},
        deep_link: 'codex://threads/thread-1',
      })
      .mockRejectedValueOnce(new Error('workspace chat: thread cwd mismatch'));

    const view = renderChat('/chat/workspace-1/thread-1');
    const copyButtons = await view.findAllByRole('button', { name: 'Copy Codex link' });
    fireEvent.click(copyButtons[1]);

    expect(await view.findByText('workspace chat: thread cwd mismatch')).toBeTruthy();
    expect(mocks.writeClipboard).not.toHaveBeenCalled();
  });
});
