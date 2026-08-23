import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import Markdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import {
  AlertCircle,
  Bot,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  CircleStop,
  Code2,
  FileCode2,
  Folder,
  FolderGit2,
  Loader2,
  Menu,
  MessageSquarePlus,
  Plus,
  Search,
  Send,
  TerminalSquare,
  User,
  Wrench,
  X,
} from 'lucide-react';
import { Button, Input } from '@/components/ui';
import { cn } from '@/lib/utils';
import {
  createWorkspaceThread,
  getWorkspaceChatSelection,
  listWorkspaces,
  listWorkspaceThreads,
  putWorkspaceChatSelection,
  readWorkspaceThread,
  type NativeThread,
  type NativeThreadDetail,
  type NativeTurn,
  type Workspace,
  type WorkspaceChatEvent,
} from '@/api/workspaceChat';
import { useWorkspaceChatSocket } from '@/hooks/useWorkspaceChatSocket';

const asString = (value: unknown) => typeof value === 'string' ? value : '';

function itemText(item: Record<string, unknown>): string {
  const text = asString(item.text);
  if (text) return text;
  if (!Array.isArray(item.content)) return '';
  return item.content
    .map((block) => block && typeof block === 'object' ? asString((block as Record<string, unknown>).text) : '')
    .filter(Boolean)
    .join('\n');
}

function JsonDetails({ value, label }: { value: unknown; label: string }) {
  return (
    <details className="group text-xs text-gray-500 dark:text-gray-400">
      <summary className="cursor-pointer select-none hover:text-gray-800 dark:hover:text-gray-200">{label}</summary>
      <pre className="mt-2 max-h-80 overflow-auto rounded-md bg-gray-100 dark:bg-black/40 p-3 text-[11px] leading-5 text-gray-700 dark:text-gray-300">
        {JSON.stringify(value, null, 2)}
      </pre>
    </details>
  );
}

function MarkdownText({ children }: { children: string }) {
  return (
    <div className="prose prose-sm max-w-none dark:prose-invert prose-pre:rounded-md prose-pre:bg-gray-950 prose-a:text-accent">
      <Markdown remarkPlugins={[remarkGfm]}>{children}</Markdown>
    </div>
  );
}

function ThreadItem({ item }: { item: Record<string, unknown> }) {
  const { t } = useTranslation();
  const type = asString(item.type) || 'unknown';
  const text = itemText(item);

  if (type === 'userMessage') {
    return (
      <div className="flex justify-end gap-2">
        <div className="max-w-[78%] rounded-md bg-accent px-4 py-3 text-sm text-white dark:text-black whitespace-pre-wrap">{text}</div>
        <div className="mt-1 flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-gray-200 dark:bg-gray-700"><User size={14} /></div>
      </div>
    );
  }
  if (type === 'agentMessage') {
    return (
      <div className="flex gap-2">
        <div className="mt-1 flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-accent/12 text-accent"><Bot size={14} /></div>
        <div className="min-w-0 flex-1 py-1 text-sm"><MarkdownText>{text}</MarkdownText></div>
      </div>
    );
  }
  if (type === 'reasoning') {
    const reasoning = text || asString(item.summary) || asString(item.reasoning);
    return (
      <details open className="ml-9 border-l-2 border-amber-400/50 pl-3 text-sm text-gray-600 dark:text-gray-400">
        <summary className="cursor-pointer text-xs font-medium text-amber-700 dark:text-amber-300">{t('workspaceChat.reasoning')}</summary>
        <div className="mt-2 whitespace-pre-wrap leading-6">{reasoning}</div>
        <JsonDetails value={item} label={t('workspaceChat.rawItem')} />
      </details>
    );
  }
  if (type === 'plan') {
    return (
      <div className="ml-9 border-l-2 border-blue-400/50 pl-3 text-sm">
        <div className="mb-1 text-xs font-medium text-blue-700 dark:text-blue-300">{t('workspaceChat.plan')}</div>
        {text ? <MarkdownText>{text}</MarkdownText> : <JsonDetails value={item} label={t('workspaceChat.planDetails')} />}
      </div>
    );
  }
  if (type === 'commandExecution') {
    return (
      <div className="ml-9 border-l-2 border-gray-400/50 pl-3">
        <div className="mb-2 flex items-center gap-2 text-xs font-medium text-gray-600 dark:text-gray-300">
          <TerminalSquare size={14} /> {asString(item.status) || t('workspaceChat.command')}
          {item.exitCode !== undefined && <span>{t('workspaceChat.exitCode', { code: String(item.exitCode) })}</span>}
        </div>
        <pre className="overflow-auto rounded-md bg-gray-950 p-3 text-xs leading-5 text-gray-100">$ {asString(item.command)}{asString(item.aggregatedOutput) ? `\n${asString(item.aggregatedOutput)}` : ''}</pre>
      </div>
    );
  }
  if (type === 'fileChange') {
    return (
      <div className="ml-9 border-l-2 border-emerald-400/50 pl-3 text-sm">
        <div className="mb-2 flex items-center gap-2 text-xs font-medium text-emerald-700 dark:text-emerald-300"><FileCode2 size={14} /> {t('workspaceChat.fileChanges')}</div>
        <JsonDetails value={item.changes ?? item} label={t('workspaceChat.changes')} />
      </div>
    );
  }
  if (type === 'mcpToolCall' || type === 'dynamicToolCall') {
    return (
      <div className="ml-9 border-l-2 border-violet-400/50 pl-3 text-sm">
        <div className="mb-2 flex items-center gap-2 text-xs font-medium text-violet-700 dark:text-violet-300"><Wrench size={14} /> {asString(item.server)} {asString(item.tool)}</div>
        <JsonDetails value={item} label={t('workspaceChat.toolDetails')} />
      </div>
    );
  }
  if (type === 'webSearch') {
    return (
      <div className="ml-9 flex items-start gap-2 border-l-2 border-cyan-400/50 pl-3 text-sm text-gray-700 dark:text-gray-300">
        <Search size={14} className="mt-1 shrink-0" /> <span>{asString(item.query) || text}</span>
      </div>
    );
  }
  if (type === 'error') {
    return <div className="ml-9 rounded-md bg-red-50 p-3 text-sm text-red-700 dark:bg-red-950/30 dark:text-red-300">{text || asString(item.message) || JSON.stringify(item)}</div>;
  }
  return <div className="ml-9 border-l-2 border-gray-300 pl-3"><JsonDetails value={item} label={type} /></div>;
}

function TurnView({ turn }: { turn: NativeTurn }) {
  const { t } = useTranslation();
  const completed = turn.status === 'completed';
  return (
    <section className="border-b border-gray-200 py-6 last:border-b-0 dark:border-white/[0.08]">
      <div className="mb-4 flex items-center gap-2 text-[11px] text-gray-400">
        {completed ? <CheckCircle2 size={13} className="text-emerald-500" /> : <AlertCircle size={13} className="text-amber-500" />}
        <span className="font-medium uppercase">{turn.status || t('workspaceChat.turn')}</span>
        {turn.duration_ms !== undefined && <span>{(turn.duration_ms / 1000).toFixed(1)}s</span>}
      </div>
      <div className="space-y-4">
        {(turn.items || []).map((item, index) => <ThreadItem key={asString(item.id) || `${turn.id}-${index}`} item={item} />)}
        {turn.error !== undefined && <JsonDetails value={turn.error} label={t('workspaceChat.turnError')} />}
      </div>
    </section>
  );
}

function LiveEventView({ event, onApproval }: { event: WorkspaceChatEvent; onApproval: (decision: string) => void }) {
  const { t } = useTranslation();
  if (event.type === 'optimistic_user') {
    return <ThreadItem item={{ type: 'userMessage', text: event.content || '' }} />;
  }
  if (event.type === 'approval_requested') {
    return (
      <div className="ml-9 rounded-md border border-amber-300 bg-amber-50 p-3 text-sm dark:border-amber-700 dark:bg-amber-950/30">
        <div className="mb-3 whitespace-pre-wrap">{event.content}</div>
        <div className="flex flex-wrap gap-2">
          <Button size="sm" onClick={() => onApproval('allow')}>{t('workspaceChat.allow')}</Button>
          <Button size="sm" variant="secondary" onClick={() => onApproval('allow_all')}>{t('workspaceChat.allowAll')}</Button>
          <Button size="sm" variant="danger" onClick={() => onApproval('deny')}>{t('workspaceChat.deny')}</Button>
        </div>
      </div>
    );
  }
  if (event.type === 'agent_event') {
    const payload = event.payload || {};
    const eventType = asString(payload.event_type);
    if (eventType === 'text') return <ThreadItem item={{ type: 'agentMessage', text: payload.content }} />;
    if (eventType === 'thinking') return <ThreadItem item={{ type: 'reasoning', text: payload.content }} />;
    return <div className="ml-9 border-l-2 border-gray-300 pl-3"><JsonDetails value={payload} label={eventType || t('workspaceChat.agentEvent')} /></div>;
  }
  if (event.type === 'error' || event.error) {
    const message = event.error === 'workspace_chat_invalid_event' ? t('workspaceChat.invalidEvent') : event.error;
    return <div className="ml-9 text-sm text-red-600 dark:text-red-400">{message}</div>;
  }
  return null;
}

function threadLabel(thread: NativeThread): string {
  return thread.name || thread.preview || thread.id.slice(0, 12);
}

function chatPath(workspaceRef: string, threadId: string): string {
  return `/chat/${encodeURIComponent(workspaceRef)}/${encodeURIComponent(threadId)}`;
}

export default function WorkspaceChat() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const params = useParams<{ workspaceRef?: string; threadId?: string }>();
  const workspaceRef = params.workspaceRef;
  const threadId = params.threadId;
  const [workspaces, setWorkspaces] = useState<Workspace[]>([]);
  const [threads, setThreads] = useState<NativeThread[]>([]);
  const [detail, setDetail] = useState<NativeThreadDetail | null>(null);
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [liveEvents, setLiveEvents] = useState<WorkspaceChatEvent[]>([]);
  const [input, setInput] = useState('');
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const [running, setRunning] = useState(false);
  const [projectPanelOpen, setProjectPanelOpen] = useState(false);
  const [newDialogOpen, setNewDialogOpen] = useState(false);
  const [newThreadName, setNewThreadName] = useState('');
  const [creating, setCreating] = useState(false);
  const endRef = useRef<HTMLDivElement>(null);

  const loadCatalog = useCallback(async () => {
    const response = await listWorkspaces();
    const items = response.workspaces || [];
    setWorkspaces(items);
    setExpanded(new Set(items.map((workspace) => workspace.project_id)));
  }, []);

  const loadThread = useCallback(async () => {
    if (!workspaceRef || !threadId) {
      setDetail(null);
      return;
    }
    const [threadResponse, threadList] = await Promise.all([
      readWorkspaceThread(workspaceRef, threadId),
      listWorkspaceThreads(workspaceRef),
    ]);
    setDetail(threadResponse);
    setThreads(threadList.threads || []);
  }, [workspaceRef, threadId]);

  useEffect(() => {
    let cancelled = false;
    const initialize = async () => {
      setLoading(true);
      setError('');
      try {
        await loadCatalog();
        if (workspaceRef && threadId) {
          await putWorkspaceChatSelection(workspaceRef, threadId);
        } else {
          const selected = await getWorkspaceChatSelection();
          if (!cancelled) {
            if (selected) navigate(chatPath(selected.workspace_ref, selected.thread_id), { replace: true });
          }
        }
      } catch (cause) {
        if (!cancelled) setError(cause instanceof Error ? cause.message : String(cause));
      } finally {
        if (!cancelled) setLoading(false);
      }
    };
    initialize();
    return () => { cancelled = true; };
  }, [workspaceRef, threadId, navigate, loadCatalog]);

  useEffect(() => {
    if (!workspaceRef || !threadId) return;
    setLiveEvents([]);
    loadThread().catch((cause) => setError(cause instanceof Error ? cause.message : String(cause)));
  }, [workspaceRef, threadId, loadThread]);

  const onSocketEvent = useCallback((event: WorkspaceChatEvent) => {
    if (event.type === 'subscribed') return;
    if (event.type === 'turn_queued' || event.type === 'turn_started') setRunning(true);
    if (event.type === 'turn_completed' || event.type === 'turn_failed' || event.type === 'turn_cancelled') {
      setRunning(false);
      loadThread().then(() => setLiveEvents([])).catch((cause) => setError(cause instanceof Error ? cause.message : String(cause)));
      return;
    }
    setLiveEvents((current) => [...current, event]);
  }, [loadThread]);

  const { status: socketStatus, send } = useWorkspaceChatSocket({ workspaceRef, threadId, onEvent: onSocketEvent });

  useEffect(() => {
    endRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [detail, liveEvents]);

  useEffect(() => {
    const refresh = () => {
      loadCatalog().catch(() => undefined);
      loadThread().catch(() => undefined);
    };
    window.addEventListener('cc:refresh', refresh);
    return () => window.removeEventListener('cc:refresh', refresh);
  }, [loadCatalog, loadThread]);

  const groupedWorkspaces = useMemo(() => {
    const groups = new Map<string, Workspace[]>();
    workspaces.forEach((workspace) => groups.set(workspace.project_id, [...(groups.get(workspace.project_id) || []), workspace]));
    return [...groups.values()];
  }, [workspaces]);

  const activeWorkspace = workspaces.find((workspace) => workspace.ref === workspaceRef);

  const chooseWorkspace = async (workspace: Workspace) => {
    if (!workspace.available) return;
    setError('');
    try {
      const selected = await putWorkspaceChatSelection(workspace.ref);
      setProjectPanelOpen(false);
      navigate(chatPath(selected.workspace_ref, selected.thread_id));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    }
  };

  const chooseThread = async (nextThreadId: string) => {
    if (!workspaceRef) return;
    try {
      const selected = await putWorkspaceChatSelection(workspaceRef, nextThreadId);
      navigate(chatPath(selected.workspace_ref, selected.thread_id));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    }
  };

  const createThread = async () => {
    if (!workspaceRef) return;
    setCreating(true);
    try {
      const thread = await createWorkspaceThread(workspaceRef, newThreadName.trim());
      setNewDialogOpen(false);
      setNewThreadName('');
      navigate(chatPath(workspaceRef, thread.id));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setCreating(false);
    }
  };

  const submit = () => {
    const content = input.trim();
    if (!content || !workspaceRef || !threadId) return;
    const requestId = crypto.randomUUID();
    if (!send({ type: 'turn_start', request_id: requestId, workspace_ref: workspaceRef, thread_id: threadId, content })) {
      setError(t('workspaceChat.disconnected'));
      return;
    }
    setInput('');
    setRunning(true);
    setLiveEvents((current) => [...current, { type: 'optimistic_user', request_id: requestId, content }]);
  };

  const cancelTurn = () => {
    if (!workspaceRef || !threadId) return;
    send({ type: 'cancel', request_id: crypto.randomUUID(), workspace_ref: workspaceRef, thread_id: threadId });
  };

  const approve = (decision: string) => {
    if (!workspaceRef || !threadId) return;
    send({ type: 'approval_response', request_id: crypto.randomUUID(), workspace_ref: workspaceRef, thread_id: threadId, decision });
  };

  return (
    <div className="relative flex h-[calc(100vh-8.5rem)] min-h-[520px] overflow-hidden rounded-md border border-gray-200 bg-white dark:border-white/[0.08] dark:bg-[#0b0b0d]">
      <aside className={cn(
        'absolute inset-y-0 left-0 z-30 w-[280px] border-r border-gray-200 bg-white transition-transform dark:border-white/[0.08] dark:bg-[#0b0b0d] md:static md:z-auto md:translate-x-0',
        projectPanelOpen ? 'translate-x-0' : '-translate-x-full',
      )}>
        <div className="flex h-12 items-center justify-between border-b border-gray-200 px-3 dark:border-white/[0.08]">
          <div className="flex items-center gap-2 text-sm font-semibold"><FolderGit2 size={16} /> {t('workspaceChat.workspaces')}</div>
          <button type="button" className="p-1 md:hidden" onClick={() => setProjectPanelOpen(false)} aria-label={t('common.close')}><X size={17} /></button>
        </div>
        <nav className="h-[calc(100%-3rem)] overflow-y-auto p-2">
          {groupedWorkspaces.map((roots) => {
            const project = roots[0];
            const isExpanded = expanded.has(project.project_id);
            const multiRoot = roots.length > 1;
            if (!multiRoot) {
              const workspace = roots[0];
              return <WorkspaceButton key={workspace.ref} workspace={workspace} active={workspace.ref === workspaceRef} onClick={() => chooseWorkspace(workspace)} />;
            }
            return (
              <div key={project.project_id} className="mb-1">
                <button type="button" onClick={() => setExpanded((current) => {
                  const next = new Set(current);
                  if (next.has(project.project_id)) next.delete(project.project_id); else next.add(project.project_id);
                  return next;
                })} className="flex w-full items-center gap-2 rounded-md px-2 py-2 text-left text-sm font-medium hover:bg-gray-100 dark:hover:bg-white/[0.06]">
                  {isExpanded ? <ChevronDown size={15} /> : <ChevronRight size={15} />}<Folder size={15} className="text-amber-500" /><span className="truncate">{project.project_name}</span>
                </button>
                {isExpanded && <div className="ml-5 border-l border-gray-200 pl-1 dark:border-white/[0.1]">{roots.map((workspace) => <WorkspaceButton key={workspace.ref} workspace={workspace} active={workspace.ref === workspaceRef} onClick={() => chooseWorkspace(workspace)} rootOnly />)}</div>}
              </div>
            );
          })}
          {!loading && workspaces.length === 0 && <p className="p-4 text-sm text-gray-400">{t('workspaceChat.noWorkspaces')}</p>}
        </nav>
      </aside>

      {projectPanelOpen && <button type="button" className="absolute inset-0 z-20 bg-black/30 md:hidden" onClick={() => setProjectPanelOpen(false)} aria-label={t('common.close')} />}

      <section className="flex min-w-0 flex-1 flex-col">
        <header className="flex min-h-12 items-center gap-2 border-b border-gray-200 px-3 dark:border-white/[0.08]">
          <button type="button" className="p-1.5 md:hidden" onClick={() => setProjectPanelOpen(true)} aria-label={t('workspaceChat.openWorkspaces')}><Menu size={18} /></button>
          <div className="min-w-0 flex-1">
            <div className="truncate text-sm font-semibold">{activeWorkspace ? `${activeWorkspace.project_name}${activeWorkspace.root_name !== activeWorkspace.project_name ? ` / ${activeWorkspace.root_name}` : ''}` : t('workspaceChat.chooseWorkspace')}</div>
            {activeWorkspace && <div className="truncate text-[11px] text-gray-400">{activeWorkspace.root_path}</div>}
          </div>
          {workspaceRef && threadId && (
            <>
              <select value={threadId} onChange={(event) => chooseThread(event.target.value)} className="max-w-[220px] rounded-md border border-gray-200 bg-white px-2 py-1.5 text-xs dark:border-white/[0.12] dark:bg-black/30">
                {threads.map((thread) => <option key={thread.id} value={thread.id}>{threadLabel(thread)}</option>)}
              </select>
              <button type="button" onClick={() => setNewDialogOpen(true)} className="rounded-md p-2 text-gray-500 hover:bg-gray-100 dark:hover:bg-white/[0.06]" title={t('workspaceChat.newThread')}><MessageSquarePlus size={17} /></button>
              {running && <button type="button" onClick={cancelTurn} className="rounded-md p-2 text-red-500 hover:bg-red-500/10" title={t('workspaceChat.cancelTurn')}><CircleStop size={17} /></button>}
            </>
          )}
        </header>

        {error && <div className="flex items-start gap-2 border-b border-red-200 bg-red-50 px-4 py-2 text-xs text-red-700 dark:border-red-900 dark:bg-red-950/30 dark:text-red-300"><AlertCircle size={14} className="mt-0.5 shrink-0" /><span className="flex-1">{error}</span><button type="button" onClick={() => setError('')}><X size={14} /></button></div>}

        <main className="flex-1 overflow-y-auto px-4 md:px-8">
          {!workspaceRef || !threadId ? (
            <div className="flex h-full flex-col items-center justify-center text-center text-gray-400">
              <FolderGit2 size={34} className="mb-3" />
              <p className="text-sm">{t('workspaceChat.chooseWorkspace')}</p>
            </div>
          ) : loading && !detail ? (
            <div className="flex h-full items-center justify-center"><Loader2 className="animate-spin text-gray-400" /></div>
          ) : (
            <div className="mx-auto max-w-4xl">
              {(detail?.turns || []).map((turn) => <TurnView key={turn.id} turn={turn} />)}
              {liveEvents.length > 0 && <section className="space-y-4 py-6">{liveEvents.map((event, index) => <LiveEventView key={`${event.request_id || event.type}-${index}`} event={event} onApproval={approve} />)}</section>}
              {detail && detail.turns.length === 0 && liveEvents.length === 0 && <div className="flex min-h-64 flex-col items-center justify-center text-gray-400"><Bot size={30} className="mb-3" /><p className="text-sm">{t('workspaceChat.emptyThread')}</p></div>}
              <div ref={endRef} />
            </div>
          )}
        </main>

        <footer className="border-t border-gray-200 p-3 dark:border-white/[0.08]">
          <div className="mx-auto flex max-w-4xl items-end gap-2">
            <textarea value={input} onChange={(event) => setInput(event.target.value)} onKeyDown={(event) => {
              if (event.key === 'Enter' && !event.shiftKey) { event.preventDefault(); submit(); }
            }} disabled={!threadId || socketStatus !== 'connected'} rows={2} placeholder={t('workspaceChat.inputPlaceholder')} className="max-h-36 min-h-[46px] flex-1 resize-none rounded-md border border-gray-300 bg-white px-3 py-2 text-sm focus:border-accent focus:outline-none focus:ring-2 focus:ring-accent/30 dark:border-white/[0.12] dark:bg-black/30" />
            <button type="button" onClick={submit} disabled={!input.trim() || socketStatus !== 'connected'} className="flex h-[46px] w-[46px] shrink-0 items-center justify-center rounded-md bg-accent text-white disabled:opacity-40 dark:text-black" title={t('workspaceChat.send')}><Send size={18} /></button>
          </div>
          <div className="mx-auto mt-1 max-w-4xl text-right text-[10px] text-gray-400">{socketStatus === 'connected' ? t('workspaceChat.connected') : t('workspaceChat.disconnected')}</div>
        </footer>
      </section>

      {newDialogOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/45 p-4">
          <div className="w-full max-w-sm rounded-md bg-white p-5 shadow-xl dark:bg-gray-900">
            <div className="mb-4 flex items-center justify-between"><h2 className="text-sm font-semibold">{t('workspaceChat.newThread')}</h2><button type="button" onClick={() => setNewDialogOpen(false)}><X size={17} /></button></div>
            <Input label={t('workspaceChat.threadName')} value={newThreadName} onChange={(event) => setNewThreadName(event.target.value)} autoFocus />
            <div className="mt-5 flex justify-end gap-2"><Button variant="secondary" onClick={() => setNewDialogOpen(false)}>{t('common.cancel')}</Button><Button loading={creating} onClick={createThread}><Plus size={15} />{t('workspaceChat.create')}</Button></div>
          </div>
        </div>
      )}
    </div>
  );
}

function WorkspaceButton({ workspace, active, onClick, rootOnly = false }: { workspace: Workspace; active: boolean; onClick: () => void; rootOnly?: boolean }) {
  return (
    <button type="button" disabled={!workspace.available} onClick={onClick} title={workspace.available ? workspace.root_path : workspace.error} className={cn(
      'mb-1 flex w-full items-start gap-2 rounded-md px-2 py-2 text-left transition-colors',
      active ? 'bg-accent/12 text-accent' : 'hover:bg-gray-100 dark:hover:bg-white/[0.06]',
      !workspace.available && 'cursor-not-allowed opacity-45',
    )}>
      {rootOnly ? <Code2 size={15} className="mt-0.5 shrink-0" /> : <Folder size={15} className="mt-0.5 shrink-0 text-amber-500" />}
      <span className="min-w-0"><span className="block truncate text-sm font-medium">{rootOnly ? workspace.root_name : workspace.project_name}</span><span className="block truncate text-[10px] text-gray-400">{workspace.available ? workspace.root_path : workspace.error}</span></span>
    </button>
  );
}
