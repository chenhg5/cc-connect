import { useCallback, useEffect, useMemo, useReducer, useRef, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import {
  AlertTriangle,
  CircleStop,
  Copy,
  Loader2,
  Menu,
  Mic,
  MicOff,
  Send,
} from 'lucide-react';
import { Badge } from '@/components/ui';
import { cn } from '@/lib/utils';
import {
  collectAllPages,
  conversationPath,
  createWorkspaceDraft,
  getWorkspaceChatSelection,
  getWorkspaceRuntimeCatalog,
  listWorkspaceItems,
  listWorkspaceThreads,
  listWorkspaceTurns,
  putWorkspaceChatSelection,
  readWorkspaceDraft,
  readWorkspaceThread,
  updateWorkspaceDraftSettings,
  updateWorkspaceThreadSettings,
  listWorkspaces,
  type ConversationRef,
  type NativeEventEnvelope,
  type NativeItemRecord,
  type NativeRuntimeCatalog,
  type NativeThread,
  type NativeThreadSettingsPatch,
  type NativeTurn,
  type Workspace,
  type WorkspaceChatClientRequest,
  type WorkspaceChatDraft,
  type WorkspaceChatServerEvent,
} from '@/api/workspaceChat';
import { useWorkspaceChatSocket } from '@/hooks/useWorkspaceChatSocket';
import { useWorkspaceRealtime } from '@/hooks/useWorkspaceRealtime';
import { WorkspaceHistory } from './WorkspaceChatItems';
import { WorkspaceInteraction } from './WorkspaceChatInteractions';
import { WorkspaceChatRail } from './WorkspaceChatRail';
import { WorkspaceChatSettings } from './WorkspaceChatSettings';
import {
  initialWorkspaceChatStreamState,
  nativeEnvelopeFromEvent,
  statusType,
  totalTokenCount,
  workspaceChatStreamReducer,
} from './workspaceChatState';

function errorMessage(cause: unknown): string {
  return cause instanceof Error ? cause.message : String(cause);
}

function supportsCapability(catalog: NativeRuntimeCatalog | null, name: string): { supported: boolean; reason?: string } {
  if (!catalog) return { supported: false };
  return catalog.capabilities?.[name] || { supported: false };
}

function statusVariant(status: string): 'default' | 'success' | 'warning' | 'danger' {
  if (status === 'idle') return 'success';
  if (status === 'active') return 'warning';
  if (status === 'systemError') return 'danger';
  return 'default';
}

async function loadThreadHistory(workspaceRef: string, threadId: string, validatedSnapshot?: Awaited<ReturnType<typeof readWorkspaceThread>>) {
  const [snapshot, turns] = await Promise.all([
    validatedSnapshot ? Promise.resolve(validatedSnapshot) : readWorkspaceThread(workspaceRef, threadId),
    collectAllPages((cursor) => listWorkspaceTurns(workspaceRef, threadId, { cursor, sortDirection: 'asc' })),
  ]);
  const itemsByTurn: Record<string, NativeItemRecord[]> = {};
  for (const turn of turns) {
    itemsByTurn[turn.id] = await collectAllPages((cursor) =>
      listWorkspaceItems(workspaceRef, threadId, turn.id, { cursor, sortDirection: 'asc' }));
  }
  return { snapshot, turns, itemsByTurn };
}

export default function WorkspaceChat() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const params = useParams<{ workspaceRef?: string; threadId?: string; draftRef?: string }>();
  const workspaceRef = params.workspaceRef;
  const conversation = useMemo<ConversationRef | undefined>(() => {
    if (params.draftRef) return { kind: 'draft', id: params.draftRef };
    if (params.threadId) return { kind: 'thread', id: params.threadId };
    return undefined;
  }, [params.draftRef, params.threadId]);
  const targetKey = workspaceRef && conversation ? `${workspaceRef}\u0000${conversation.kind}\u0000${conversation.id}` : '';

  const [workspaces, setWorkspaces] = useState<Workspace[]>([]);
  const [threads, setThreads] = useState<NativeThread[]>([]);
  const [draft, setDraft] = useState<WorkspaceChatDraft | null>(null);
  const [catalog, setCatalog] = useState<NativeRuntimeCatalog | null>(null);
  const [draftSettings, setDraftSettings] = useState<NativeThreadSettingsPatch>({});
  const [stream, dispatch] = useReducer(workspaceChatStreamReducer, initialWorkspaceChatStreamState);
  const [input, setInput] = useState('');
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const [creating, setCreating] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [updatingSettings, setUpdatingSettings] = useState(false);
  const [respondingInteraction, setRespondingInteraction] = useState('');
  const [projectPanelOpen, setProjectPanelOpen] = useState(false);
  const [voice, setVoice] = useState('');
  const endRef = useRef<HTMLDivElement>(null);
  const loadGeneration = useRef(0);
  const targetKeyRef = useRef(targetKey);
  const realtimeEventRef = useRef<(event: NativeEventEnvelope) => void>(() => undefined);
  const realtimeRequestErrorRef = useRef<(requestID: string | undefined, message: string) => void>(() => undefined);
	const streamCursorRef = useRef<{ epoch?: string; sequence?: number }>({});
  const reconnectPendingRef = useRef(false);
	const selectionWriteRef = useRef<Promise<void>>(Promise.resolve());
	targetKeyRef.current = targetKey;
	streamCursorRef.current = { epoch: stream.epoch, sequence: stream.sequence };
	const persistSelection = useCallback((ref: string, nextConversation: ConversationRef) => {
		const write = selectionWriteRef.current
			.catch(() => undefined)
			.then(() => putWorkspaceChatSelection(ref, nextConversation));
		selectionWriteRef.current = write.then(() => undefined, () => undefined);
		return write;
	}, []);

  const loadWorkspaceCatalog = useCallback(async () => {
    const response = await listWorkspaces();
    setWorkspaces(response.workspaces || []);
    return response.workspaces || [];
  }, []);

  const fetchThreads = useCallback((ref: string) =>
    collectAllPages((cursor) => listWorkspaceThreads(ref, { cursor, sortDirection: 'desc' })), []);

  const refreshThread = useCallback(async () => {
    if (!workspaceRef || conversation?.kind !== 'thread') return;
    const expectedTarget = targetKey;
	let result;
	const started = streamCursorRef.current;
    try {
      result = await loadThreadHistory(workspaceRef, conversation.id);
    } catch (cause) {
      if (targetKeyRef.current === expectedTarget) throw cause;
      return;
    }
    if (targetKeyRef.current !== expectedTarget) return;
	dispatch({ type: 'history_loaded', ...result, startedEpoch: started.epoch, startedSequence: started.sequence });
  }, [conversation?.id, conversation?.kind, targetKey, workspaceRef]);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    loadWorkspaceCatalog()
      .catch((cause) => { if (!cancelled) setError(errorMessage(cause)); })
      .finally(() => { if (!cancelled && !conversation) setLoading(false); });
    return () => { cancelled = true; };
  }, [conversation, loadWorkspaceCatalog]);

  useEffect(() => {
    if (workspaceRef || conversation) return;
    let cancelled = false;
    getWorkspaceChatSelection().then((selection) => {
      if (!cancelled && selection) navigate(conversationPath(selection.workspace_ref, selection.conversation), { replace: true });
    }).catch((cause) => { if (!cancelled) setError(errorMessage(cause)); });
    return () => { cancelled = true; };
  }, [conversation, navigate, workspaceRef]);

  useEffect(() => {
    if (!workspaceRef || !conversation) return;
    const generation = ++loadGeneration.current;
    let cancelled = false;
    dispatch({ type: 'reset' });
    setDraft(null);
    setDraftSettings({});
    setCatalog(null);
    setLoading(true);
    setError('');
    setSubmitting(false);
    setUpdatingSettings(false);
    setRespondingInteraction('');
	const load = async () => {
	  let nextDraft: WorkspaceChatDraft | null = null;
	  let validatedSnapshot: Awaited<ReturnType<typeof readWorkspaceThread>> | undefined;
      if (conversation.kind === 'draft') {
        nextDraft = await readWorkspaceDraft(workspaceRef, conversation.id);
        if (cancelled || generation !== loadGeneration.current || targetKeyRef.current !== targetKey) return;
        if (nextDraft.state === 'materialized') {
          if (!nextDraft.thread_id) throw new Error(t('workspaceChat.materializationMissingThread'));
          await readWorkspaceThread(workspaceRef, nextDraft.thread_id);
          if (cancelled || generation !== loadGeneration.current || targetKeyRef.current !== targetKey) return;
          const materializedConversation: ConversationRef = { kind: 'thread', id: nextDraft.thread_id };
		  await persistSelection(workspaceRef, materializedConversation);
          if (cancelled || generation !== loadGeneration.current || targetKeyRef.current !== targetKey) return;
          navigate(conversationPath(workspaceRef, materializedConversation), { replace: true });
          return;
        }
		if (nextDraft.state !== 'draft') {
          throw new Error(`workspace_chat_invalid_draft_state: ${nextDraft.state}`);
		}
	  } else {
		validatedSnapshot = await readWorkspaceThread(workspaceRef, conversation.id);
		if (cancelled || generation !== loadGeneration.current || targetKeyRef.current !== targetKey) return;
	  }
		  await persistSelection(workspaceRef, conversation);
	  if (cancelled || generation !== loadGeneration.current || targetKeyRef.current !== targetKey) return;
	  const [runtimeCatalog, availableThreads, loadedThread] = await Promise.all([
        getWorkspaceRuntimeCatalog(workspaceRef),
        fetchThreads(workspaceRef),
		conversation.kind === 'thread' ? loadThreadHistory(workspaceRef, conversation.id, validatedSnapshot) : Promise.resolve(null),
	  ]);
	  if (cancelled || generation !== loadGeneration.current || targetKeyRef.current !== targetKey) return;
      setThreads(availableThreads);
      setCatalog(runtimeCatalog);
      const preferredVoice = runtimeCatalog.voices.default_v2
        || runtimeCatalog.voices.v2[0]
        || runtimeCatalog.voices.default_v1
        || runtimeCatalog.voices.v1[0]
        || '';
      setVoice(preferredVoice);
      if (conversation.kind === 'draft') {
        if (!nextDraft) throw new Error('workspace_chat_draft_not_loaded');
        setDraft(nextDraft);
        setDraftSettings(nextDraft.settings_patch);
        return;
      }
      if (!loadedThread) throw new Error('workspace_chat_thread_not_loaded');
      dispatch({ type: 'history_loaded', ...loadedThread });
    };
    load().catch((cause) => {
      if (!cancelled && generation === loadGeneration.current && targetKeyRef.current === targetKey) setError(errorMessage(cause));
    })
      .finally(() => { if (!cancelled && generation === loadGeneration.current) setLoading(false); });
    return () => { cancelled = true; };
	  }, [conversation?.id, conversation?.kind, fetchThreads, navigate, persistSelection, t, targetKey, workspaceRef]);

  const onSocketEvent = useCallback((event: WorkspaceChatServerEvent) => {
    if (!workspaceRef || !conversation || event.workspace_ref !== workspaceRef) return;
    const isCurrentConversation = event.conversation.kind === conversation.kind
      && event.conversation.id === conversation.id;
    const isDraftMaterialization = event.type === 'thread_materialized'
      && conversation.kind === 'draft'
      && event.conversation.kind === 'thread'
      && !!event.thread_id
      && event.conversation.id === event.thread_id;
    if (!isCurrentConversation && !isDraftMaterialization) return;
    dispatch({ type: 'server_event', event });
    const envelope = nativeEnvelopeFromEvent(event);
    if (envelope) {
      realtimeEventRef.current(envelope);
      if (envelope.method === 'turn/started' || envelope.method === 'turn/completed') setSubmitting(false);
    }
    if (event.type === 'error' || event.type === 'protocol_error') {
      realtimeRequestErrorRef.current(event.request_id, event.error || t('workspaceChat.unknownError'));
      setSubmitting(false);
      setRespondingInteraction('');
      setError(event.error || t('workspaceChat.unknownError'));
    }
    if (event.type === 'thread_materialized') {
      const threadID = event.thread_id || '';
      if (!threadID) {
        setError(t('workspaceChat.materializationMissingThread'));
        return;
      }
      setSubmitting(false);
      navigate(conversationPath(workspaceRef, { kind: 'thread', id: threadID }), { replace: true });
    }
  }, [conversation, navigate, t, workspaceRef]);

	const { status: socketStatus, send } = useWorkspaceChatSocket({
    workspaceRef,
    conversation,
    onEvent: onSocketEvent,
    onProtocolError: (cause) => setError(cause.message),
	});

	useEffect(() => {
	  if (socketStatus === 'disconnected') {
		reconnectPendingRef.current = true;
		setSubmitting(false);
		setRespondingInteraction('');
		return;
	  }
	  if (socketStatus !== 'connected' || !reconnectPendingRef.current || !workspaceRef || !conversation) return;
	  reconnectPendingRef.current = false;
	  let cancelled = false;
	  const reconcile = async () => {
		const selection = await getWorkspaceChatSelection();
		if (cancelled) return;
		if (selection && (selection.workspace_ref !== workspaceRef || selection.conversation.kind !== conversation.kind || selection.conversation.id !== conversation.id)) {
		  navigate(conversationPath(selection.workspace_ref, selection.conversation), { replace: true });
		  return;
		}
		if (conversation.kind === 'thread') await refreshThread();
		else {
		  const currentDraft = await readWorkspaceDraft(workspaceRef, conversation.id);
		  if (!cancelled && currentDraft.state === 'materialized' && currentDraft.thread_id) {
			navigate(conversationPath(workspaceRef, { kind: 'thread', id: currentDraft.thread_id }), { replace: true });
		  }
		}
	  };
	  reconcile().catch((cause) => { if (!cancelled) setError(errorMessage(cause)); });
	  return () => { cancelled = true; };
	}, [conversation, navigate, refreshThread, socketStatus, workspaceRef]);

  const realtimeCapability = supportsCapability(catalog, 'realtime');
  const realtime = useWorkspaceRealtime({
    workspaceRef,
    conversation,
    supported: conversation?.kind === 'thread' && realtimeCapability.supported,
    send,
  });
  realtimeEventRef.current = realtime.handleNativeEvent;
  realtimeRequestErrorRef.current = realtime.handleRequestError;

  useEffect(() => {
    if (socketStatus !== 'connected' && realtime.status !== 'idle') realtime.stop();
  }, [realtime.status, realtime.stop, socketStatus]);

  useEffect(() => {
    if ((!stream.needsHistoryRefresh && !stream.needsResync) || conversation?.kind !== 'thread') return;
    dispatch({ type: 'history_refreshed' });
    refreshThread().catch((cause) => setError(errorMessage(cause)));
  }, [conversation?.kind, refreshThread, stream.needsHistoryRefresh, stream.needsResync]);

  useEffect(() => {
    if (!respondingInteraction) return;
    const pending = stream.snapshot?.pending_interactions.some((interaction) => interaction.id === respondingInteraction);
    if (pending === false) setRespondingInteraction('');
  }, [respondingInteraction, stream.snapshot?.pending_interactions]);

  useEffect(() => {
    endRef.current?.scrollIntoView({ behavior: 'smooth', block: 'end' });
  }, [stream.liveItems, stream.optimisticInputs, stream.turns]);

  useEffect(() => {
    const refresh = () => {
      loadWorkspaceCatalog().catch((cause) => setError(errorMessage(cause)));
      if (workspaceRef) {
        const expectedTarget = targetKey;
        fetchThreads(workspaceRef).then((values) => {
          if (targetKeyRef.current === expectedTarget) setThreads(values);
        }).catch((cause) => {
          if (targetKeyRef.current === expectedTarget) setError(errorMessage(cause));
        });
      }
      if (conversation?.kind === 'thread') refreshThread().catch((cause) => setError(errorMessage(cause)));
    };
    window.addEventListener('cc:refresh', refresh);
    return () => window.removeEventListener('cc:refresh', refresh);
  }, [conversation?.kind, fetchThreads, loadWorkspaceCatalog, refreshThread, targetKey, workspaceRef]);

  const activeWorkspace = workspaces.find((workspace) => workspace.ref === workspaceRef);
  const activeTurn = stream.snapshot?.active_turn || null;
  const currentStatus = statusType(stream.snapshot?.status);
  const tokenCount = totalTokenCount(stream.snapshot?.usage);
  const unsupportedCapabilities = Object.entries(catalog?.capabilities || {}).filter(([, status]) => !status.supported);

  const createDraft = useCallback(async (ref: string) => {
    setCreating(true);
    setError('');
    try {
      const nextDraft = await createWorkspaceDraft(ref);
      const nextConversation: ConversationRef = { kind: 'draft', id: nextDraft.id };
      navigate(conversationPath(ref, nextConversation));
      setProjectPanelOpen(false);
    } catch (cause) {
      setError(errorMessage(cause));
    } finally {
      setCreating(false);
    }
  }, [navigate]);

  const chooseWorkspace = async (workspace: Workspace) => {
    if (!workspace.available) return;
    setError('');
    try {
      const availableThreads = await fetchThreads(workspace.ref);
      if (availableThreads.length === 0) {
        await createDraft(workspace.ref);
        return;
      }
      const nextConversation: ConversationRef = { kind: 'thread', id: availableThreads[0].id };
      navigate(conversationPath(workspace.ref, nextConversation));
      setProjectPanelOpen(false);
    } catch (cause) {
      setError(errorMessage(cause));
    }
  };

  const chooseThread = async (thread: NativeThread) => {
    if (!workspaceRef) return;
    const nextConversation: ConversationRef = { kind: 'thread', id: thread.id };
    try {
      navigate(conversationPath(workspaceRef, nextConversation));
      setProjectPanelOpen(false);
    } catch (cause) {
      setError(errorMessage(cause));
    }
  };

  const copyLink = async (thread: NativeThread) => {
    try {
      if (!workspaceRef) return;
      if (!navigator.clipboard?.writeText) throw new Error('workspace_chat_clipboard_unavailable');
      const link = stream.snapshot?.thread.id === thread.id && stream.snapshot.deep_link
        ? stream.snapshot.deep_link
        : (await readWorkspaceThread(workspaceRef, thread.id)).deep_link;
      await navigator.clipboard.writeText(link);
    } catch (cause) {
      setError(errorMessage(cause));
    }
  };

  const patchSettings = async (patch: NativeThreadSettingsPatch) => {
    if (!workspaceRef || !conversation) return;
    const expectedTarget = targetKey;
    setUpdatingSettings(true);
    setError('');
    try {
      if (conversation.kind === 'draft') {
        const nextDraft = await updateWorkspaceDraftSettings(workspaceRef, conversation.id, patch);
        if (targetKeyRef.current !== expectedTarget) return;
        setDraft(nextDraft);
        setDraftSettings(nextDraft.settings_patch);
        return;
      }
      const settings = await updateWorkspaceThreadSettings(workspaceRef, conversation.id, patch);
      if (targetKeyRef.current !== expectedTarget) return;
      dispatch({ type: 'settings_updated', settings });
    } catch (cause) {
      if (targetKeyRef.current === expectedTarget) setError(errorMessage(cause));
    } finally {
      if (targetKeyRef.current === expectedTarget) setUpdatingSettings(false);
    }
  };

  const sendRequest = (request: WorkspaceChatClientRequest, optimistic?: { requestId: string; text: string; kind: 'turn_start' | 'turn_steer' | 'realtime' }) => {
    setError('');
    dispatch({ type: 'clear_error' });
    if (!send(request)) {
      setError(t('workspaceChat.disconnected'));
      return false;
    }
    if (optimistic) dispatch({ type: 'optimistic_input', input: optimistic });
    return true;
  };

  const submit = () => {
    const text = input.trim();
    if (!text || !workspaceRef || !conversation) return;
    const requestId = crypto.randomUUID();
    const base = { request_id: requestId, workspace_ref: workspaceRef, conversation };
    let request: WorkspaceChatClientRequest;
    let kind: 'turn_start' | 'turn_steer' | 'realtime';
    if (realtime.status === 'connected') {
      request = { ...base, type: 'realtime_append_text', text };
      kind = 'realtime';
    } else if (activeTurn) {
      request = { ...base, type: 'turn_steer', expected_turn_id: activeTurn.id, input: [{ type: 'text', text }] };
      kind = 'turn_steer';
    } else {
      const hasDraftSettings = conversation.kind === 'draft' && Object.keys(draftSettings).length > 0;
      request = {
        ...base,
        type: 'turn_start',
        input: [{ type: 'text', text }],
        ...(hasDraftSettings ? { payload: { settings: draftSettings } } : {}),
      };
      kind = 'turn_start';
    }
    if (!sendRequest(request, { requestId, text, kind })) return;
    setInput('');
    if (kind === 'turn_start') setSubmitting(true);
  };

  const interrupt = () => {
    if (!workspaceRef || !conversation || !activeTurn) return;
    sendRequest({
      type: 'turn_interrupt',
      request_id: crypto.randomUUID(),
      workspace_ref: workspaceRef,
      conversation,
      expected_turn_id: activeTurn.id,
    });
  };

  const respondInteraction = (interactionId: string, response: unknown) => {
    if (!workspaceRef || !conversation) return;
    const sent = sendRequest({
      type: 'interaction_response',
      request_id: crypto.randomUUID(),
      workspace_ref: workspaceRef,
      conversation,
      interaction_id: interactionId,
      response,
    });
    if (sent) setRespondingInteraction(interactionId);
  };

  const toggleRealtime = async () => {
    try {
      if (realtime.status !== 'idle') {
        realtime.stop();
        return;
      }
      const version = catalog?.voices?.v2?.length ? 'v2' : catalog?.voices?.v1?.length ? 'v1' : undefined;
      await realtime.start({ voice: voice || undefined, version });
    } catch (cause) {
      setError(errorMessage(cause));
    }
  };

  const currentSettings = conversation?.kind === 'draft' ? draftSettings : stream.snapshot?.settings || null;
  const selectedThread = conversation?.kind === 'thread' ? threads.find((thread) => thread.id === conversation.id) : undefined;
  const realtimeVoices = catalog?.voices?.v2?.length ? catalog.voices.v2 : catalog?.voices?.v1 || [];
  const composerDisabled = !conversation || socketStatus !== 'connected' || loading || submitting;

  return (
    <div className="relative flex h-[calc(100dvh-9.5rem)] min-h-0 overflow-hidden rounded-md border border-gray-200 bg-white dark:border-white/[0.08] dark:bg-[#0b0b0d]">
      <WorkspaceChatRail
        open={projectPanelOpen}
        loading={loading}
        creating={creating}
        workspaces={workspaces}
        threads={threads}
        workspaceRef={workspaceRef}
        conversation={conversation}
        onClose={() => setProjectPanelOpen(false)}
        onWorkspace={chooseWorkspace}
        onThread={chooseThread}
        onNew={() => workspaceRef && void createDraft(workspaceRef)}
        onCopyLink={(thread) => void copyLink(thread)}
      />

      <section className="flex min-w-0 flex-1 flex-col">
        <header className="flex min-h-12 shrink-0 items-center gap-2 border-b border-gray-200 px-3 dark:border-white/[0.08]">
          <button type="button" className="rounded-md p-1.5 hover:bg-gray-100 md:hidden dark:hover:bg-white/[0.08]" onClick={() => setProjectPanelOpen(true)} aria-label={t('workspaceChat.openWorkspaces')}><Menu size={18} /></button>
          <div className="min-w-0 flex-1">
            <div className="truncate text-sm font-semibold">{activeWorkspace ? `${activeWorkspace.project_name}${activeWorkspace.root_name !== activeWorkspace.project_name ? ` / ${activeWorkspace.root_name}` : ''}` : t('workspaceChat.chooseWorkspace')}</div>
            {activeWorkspace && <div className="truncate text-[11px] text-gray-400">{conversation?.kind === 'draft' ? t('workspaceChat.newDraft') : selectedThread?.name || selectedThread?.preview || `${activeWorkspace.device_name} / ${activeWorkspace.root_name}`}</div>}
          </div>
          {currentStatus && <Badge variant={statusVariant(currentStatus)} className="hidden sm:inline-flex">{currentStatus}</Badge>}
          {tokenCount !== null && <Badge variant="outline" className="hidden sm:inline-flex">{t('workspaceChat.tokens', { count: tokenCount.toLocaleString() })}</Badge>}
          {unsupportedCapabilities.length > 0 && <span className="hidden text-amber-600 sm:inline-flex" title={unsupportedCapabilities.map(([name, value]) => `${name}: ${value.reason || t('workspaceChat.notSupported')}`).join('\n')}><AlertTriangle size={15} /></span>}
          {selectedThread && <button type="button" onClick={() => void copyLink(selectedThread)} className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-gray-500 hover:bg-gray-100 hover:text-gray-900 dark:hover:bg-white/[0.08] dark:hover:text-white" title={t('workspaceChat.copyLink')} aria-label={t('workspaceChat.copyLink')}><Copy size={15} /></button>}
          {conversation?.kind === 'draft' && <button type="button" disabled className="flex h-8 w-8 shrink-0 cursor-not-allowed items-center justify-center rounded-md text-gray-300 dark:text-gray-700" title={t('workspaceChat.afterFirstTurn')} aria-label={t('workspaceChat.copyLink')}><Copy size={15} /></button>}
          {activeTurn && <button type="button" onClick={interrupt} className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-red-500 hover:bg-red-500/10" title={t('workspaceChat.cancelTurn')} aria-label={t('workspaceChat.cancelTurn')}><CircleStop size={16} /></button>}
        </header>

        {conversation && <WorkspaceChatSettings catalog={catalog} settings={currentSettings} disabled={loading || submitting} updating={updatingSettings} onPatch={(patch) => void patchSettings(patch)} />}

        <main className="min-h-0 flex-1 overflow-y-auto px-3 sm:px-5">
          {!conversation ? (
            <div className="flex h-full items-center justify-center text-center text-gray-400"><p className="max-w-xs text-sm">{t('workspaceChat.chooseWorkspace')}</p></div>
          ) : loading ? (
            <div className="flex h-full items-center justify-center"><Loader2 className="animate-spin text-accent" size={24} /></div>
          ) : (
            <div className="mx-auto w-full max-w-4xl">
              <WorkspaceHistory
                turns={stream.turns as NativeTurn[]}
                itemsByTurn={stream.itemsByTurn}
                liveItems={stream.liveItems}
                optimisticInputs={stream.optimisticInputs}
                nativeEvents={stream.nativeEvents}
                interactions={stream.snapshot?.pending_interactions || []}
                renderInteraction={(interaction) => (
                  <WorkspaceInteraction
                    interaction={interaction}
                    disabled={socketStatus !== 'connected' || !!respondingInteraction}
                    onRespond={respondInteraction}
                  />
                )}
              />
              <div ref={endRef} />
            </div>
          )}
        </main>

        {(error || stream.error) && <div role="alert" className="shrink-0 border-t border-red-200 bg-red-50 px-4 py-2 text-xs text-red-700 dark:border-red-900/50 dark:bg-red-950/30 dark:text-red-300">{error || stream.error}</div>}
        {realtime.error && <div role="alert" className="shrink-0 border-t border-amber-200 bg-amber-50 px-4 py-2 text-xs text-amber-700 dark:border-amber-900/50 dark:bg-amber-950/30 dark:text-amber-300">{realtime.error}</div>}

        <footer className="shrink-0 border-t border-gray-200 px-3 py-2 dark:border-white/[0.08]">
          <div className="mx-auto flex max-w-4xl items-end gap-2">
            {conversation?.kind === 'thread' && realtimeCapability.supported && realtimeVoices.length > 0 && (
              <select value={voice} disabled={realtime.status !== 'idle'} onChange={(event) => setVoice(event.target.value)} className="hidden h-[46px] max-w-[7.5rem] rounded-md border border-gray-300 bg-white px-2 text-xs outline-none focus:border-accent sm:block dark:border-white/[0.12] dark:bg-black/30" title={t('workspaceChat.voice')}>
                {realtimeVoices.map((option) => <option key={option} value={option}>{option}</option>)}
              </select>
            )}
            <button type="button" onClick={() => void toggleRealtime()} disabled={conversation?.kind !== 'thread' || !realtimeCapability.supported || socketStatus !== 'connected'} className={cn('flex h-[46px] w-[46px] shrink-0 items-center justify-center rounded-md border transition-colors disabled:cursor-not-allowed disabled:opacity-35', realtime.status === 'idle' ? 'border-gray-300 text-gray-500 hover:bg-gray-100 dark:border-white/[0.12] dark:hover:bg-white/[0.08]' : 'border-red-400 bg-red-500/10 text-red-600')} title={conversation?.kind === 'draft' ? t('workspaceChat.afterFirstTurn') : realtimeCapability.supported ? t(realtime.status === 'idle' ? 'workspaceChat.startVoice' : 'workspaceChat.stopVoice') : realtimeCapability.reason || t('workspaceChat.notSupported')} aria-label={t(realtime.status === 'idle' ? 'workspaceChat.startVoice' : 'workspaceChat.stopVoice')}>
              {realtime.status === 'requesting_microphone' || realtime.status === 'connecting' ? <Loader2 size={17} className="animate-spin" /> : realtime.status === 'idle' ? <Mic size={17} /> : <MicOff size={17} />}
            </button>
            <textarea
              value={input}
              onChange={(event) => setInput(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === 'Enter' && !event.shiftKey) {
                  event.preventDefault();
                  submit();
                }
              }}
              disabled={!conversation || loading}
              rows={2}
              placeholder={realtime.status === 'connected' ? t('workspaceChat.realtimePlaceholder') : activeTurn ? t('workspaceChat.steerPlaceholder') : t('workspaceChat.inputPlaceholder')}
              className="max-h-36 min-h-[46px] min-w-0 flex-1 resize-none rounded-md border border-gray-300 bg-white px-3 py-2 text-sm outline-none focus:border-accent focus:ring-2 focus:ring-accent/30 disabled:opacity-50 dark:border-white/[0.12] dark:bg-black/30"
            />
            <button type="button" onClick={submit} disabled={!input.trim() || composerDisabled} className="flex h-[46px] w-[46px] shrink-0 items-center justify-center rounded-md bg-accent text-white disabled:opacity-40 dark:text-black" title={activeTurn ? t('workspaceChat.steer') : t('workspaceChat.send')} aria-label={activeTurn ? t('workspaceChat.steer') : t('workspaceChat.send')}><Send size={18} /></button>
          </div>
          <div className="mx-auto mt-1 flex max-w-4xl items-center justify-end gap-2 text-[10px] text-gray-400">
            {draft && <span>{t('workspaceChat.draft')}</span>}
            <span>{socketStatus === 'connected' ? t('workspaceChat.connected') : t('workspaceChat.disconnected')}</span>
          </div>
          <audio ref={realtime.audioRef} autoPlay className="hidden" />
        </footer>
      </section>
    </div>
  );
}
