import { useState, useRef, useEffect, useCallback } from 'react';
import { Search, Send, Plus, WifiOff, Loader2, Circle, Bookmark, BookmarkCheck } from 'lucide-react';
import { cn, formatLocalDate } from '@/lib/utils';
import { listProjects, type ProjectSummary } from '@/api/projects';
import { listSessions, getSession, type Session, type SessionDetail } from '@/api/sessions';
import {
  useBridgeSocket, fetchBridgeConfig,
  type BridgeConfig, type BridgeIncoming, type BridgeStatus,
} from '@/hooks/useBridgeSocket';
import { createSaved, createTimeline } from '@/api/aelios';

// ── Internal message type ─────────────────────────────────────

interface CompanionMsg {
  id: string;
  role: 'user' | 'assistant' | 'system';
  content: string;
  format?: 'text' | 'markdown' | 'card' | 'buttons';
  card?: any;
  buttons?: { text: string; data: string }[][];
  streaming?: boolean;
  timestamp?: string;
}

// ── Component ─────────────────────────────────────────────────

export default function ChatView() {
  // Project / session state
  const [project, setProject] = useState<ProjectSummary | null>(null);
  const [sessions, setSessions] = useState<Session[]>([]);
  const [currentSession, setCurrentSession] = useState<SessionDetail | null>(null);
  const [bridgeCfg, setBridgeCfg] = useState<BridgeConfig | null>(null);
  const [loading, setLoading] = useState(true);

  // Message state
  const [messages, setMessages] = useState<CompanionMsg[]>([]);
  const [draft, setDraft] = useState('');
  const [typing, setTyping] = useState(false);
  // Bookmark state: msg id → 'idle' | 'saving' | 'saved' | 'error'
  const [bookmarkState, setBookmarkState] = useState<Record<string, string>>({});

  // Refs
  const scrollRef = useRef<HTMLDivElement>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const sessionKeyRef = useRef('');
  const previewHandleCounter = useRef(0);
  const sendPreviewAckRef = useRef<(refId: string, handle: string) => void>(() => {});

  // Derived
  const sessionKey = project ? `bridge:aelios-companion:${project.name}` : '';
  sessionKeyRef.current = sessionKey;

  // ── Textarea auto-resize ────────────────────────────────────

  const adjustHeight = useCallback(() => {
    const el = textareaRef.current;
    if (!el) return;
    el.style.height = 'auto';
    el.style.height = `${el.scrollHeight}px`;
  }, []);

  useEffect(() => { adjustHeight(); }, [draft, adjustHeight]);

  // ── Scroll to bottom ────────────────────────────────────────

  useEffect(() => {
    scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight, behavior: 'smooth' });
  }, [messages, typing]);

  // ── Bootstrap: fetch project + bridge config + session history

  useEffect(() => {
    let alive = true;
    (async () => {
      setLoading(true);
      try {
        const [projRes, cfg] = await Promise.all([
          listProjects(),
          fetchBridgeConfig(),
        ]);
        if (!alive) return;
        setBridgeCfg(cfg);

        const projects = projRes.projects || [];
        if (projects.length === 0) { setLoading(false); return; }

        const chosen = projects[0];
        setProject(chosen);

        // Load sessions for the chosen project
        const sessRes = await listSessions(chosen.name);
        if (!alive) return;
        const sorted = (sessRes.sessions || []).sort(
          (a, b) => (b.updated_at || b.created_at || '').localeCompare(a.updated_at || a.created_at || ''),
        );
        setSessions(sorted);

        // Try to find a session whose key matches our companion key.
        // Compute locally — the outer sessionKey state is still empty on first render.
        const companionSessionKey = `bridge:aelios-companion:${chosen.name}`;
        const companionSession = sorted.find(s => s.session_key === companionSessionKey);
        if (companionSession) {
          const detail = await getSession(chosen.name, companionSession.id, 200);
          if (!alive) return;
          setCurrentSession(detail);
          if (detail.history?.length) {
            setMessages(detail.history.map((h, i) => ({
              id: `hist-${i}`,
              role: h.role as 'user' | 'assistant',
              content: h.content,
              format: 'markdown',
              timestamp: h.timestamp,
            })));
          }
        }
      } finally {
        if (alive) setLoading(false);
      }
    })();
    return () => { alive = false; };
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  // ── Bridge message handler ──────────────────────────────────

  const handleBridgeMessage = useCallback((msg: BridgeIncoming) => {
    const msgKey = (msg as any).session_key;
    if (msgKey && sessionKeyRef.current && msgKey !== sessionKeyRef.current) return;

    if (msg.type === 'reply') {
      setMessages(prev => {
        const idx = prev.findIndex(m => m.streaming && m.role === 'assistant');
        if (idx >= 0) {
          const updated = [...prev];
          updated[idx] = { ...updated[idx], content: msg.content, format: (msg as any).format === 'markdown' ? 'markdown' : 'text', streaming: false };
          return updated;
        }
        return [...prev, { id: `reply-${Date.now()}`, role: 'assistant', content: msg.content, format: (msg as any).format === 'markdown' ? 'markdown' : 'text' }];
      });
      setTyping(false);
    } else if (msg.type === 'reply_stream') {
      const stream = msg as Extract<BridgeIncoming, { type: 'reply_stream' }>;
      if (stream.done) {
        setMessages(prev => {
          const idx = prev.findIndex(m => m.streaming);
          if (idx >= 0) {
            const updated = [...prev];
            updated[idx] = { ...updated[idx], content: stream.full_text, streaming: false };
            return updated;
          }
          return [...prev, { id: `stream-done-${Date.now()}`, role: 'assistant', content: stream.full_text, format: 'markdown' }];
        });
        setTyping(false);
      } else {
        setMessages(prev => {
          const idx = prev.findIndex(m => m.streaming);
          if (idx >= 0) {
            const updated = [...prev];
            updated[idx] = { ...updated[idx], content: stream.full_text };
            return updated;
          }
          return [...prev, { id: `stream-${Date.now()}`, role: 'assistant', content: stream.full_text, format: 'markdown', streaming: true }];
        });
      }
    } else if (msg.type === 'card') {
      const card = msg as Extract<BridgeIncoming, { type: 'card' }>;
      setMessages(prev => [...prev, { id: `card-${Date.now()}`, role: 'assistant', content: '', format: 'card', card: card.card }]);
      setTyping(false);
    } else if (msg.type === 'buttons') {
      const btns = msg as Extract<BridgeIncoming, { type: 'buttons' }>;
      setMessages(prev => [...prev, { id: `btn-${Date.now()}`, role: 'assistant', content: btns.content, format: 'buttons', buttons: btns.buttons }]);
      setTyping(false);
    } else if (msg.type === 'typing_start') {
      setTyping(true);
    } else if (msg.type === 'typing_stop') {
      setTyping(false);
    } else if (msg.type === 'preview_start') {
      const ps = msg as Extract<BridgeIncoming, { type: 'preview_start' }>;
      const handle = `aelios-preview-${++previewHandleCounter.current}`;
      sendPreviewAckRef.current(ps.ref_id, handle);
      setMessages(prev => [...prev, { id: `stream-${handle}`, role: 'assistant', content: ps.content, format: 'markdown', streaming: true }]);
    } else if (msg.type === 'update_message') {
      const um = msg as Extract<BridgeIncoming, { type: 'update_message' }>;
      setMessages(prev => {
        const idx = prev.findIndex(m => m.streaming);
        if (idx >= 0) {
          const updated = [...prev];
          updated[idx] = { ...updated[idx], content: um.content };
          return updated;
        }
        return prev;
      });
    } else if (msg.type === 'delete_message') {
      setMessages(prev => {
        const idx = prev.findIndex(m => m.streaming);
        if (idx >= 0) return prev.filter((_, i) => i !== idx);
        return prev;
      });
    }
  }, []);

  const { status: bridgeStatus, sendMessage: bridgeSend, sendCardAction, sendPreviewAck } = useBridgeSocket({
    bridgeCfg,
    platformName: 'aelios-companion',
    sessionKey,
    projectName: project?.name,
    onMessage: handleBridgeMessage,
  });
  sendPreviewAckRef.current = sendPreviewAck;

  // ── Send ────────────────────────────────────────────────────

  const canSend = bridgeStatus === 'connected';

  const handleSend = useCallback(() => {
    if (!draft.trim() || !canSend) return;
    const content = draft.trim();
    setDraft('');
    setMessages(prev => [...prev, { id: `user-${Date.now()}`, role: 'user', content }]);
    bridgeSend(content);
    // Reset textarea height
    if (textareaRef.current) textareaRef.current.style.height = 'auto';
  }, [draft, canSend, bridgeSend]);

  // ── Bookmark ────────────────────────────────────────────────

  const handleBookmark = useCallback(async (msg: CompanionMsg) => {
    if (!msg.content || msg.streaming) return;
    const state = bookmarkState[msg.id];
    if (state === 'saving' || state === 'saved') return;

    setBookmarkState(prev => ({ ...prev, [msg.id]: 'saving' }));
    try {
      await createSaved({
        type: 'text',
        content: msg.content,
        source: 'chat',
      });
      setBookmarkState(prev => ({ ...prev, [msg.id]: 'saved' }));
      // Write a timeline event — fire-and-forget, don't fail the bookmark
      createTimeline({
        type: 'favorite',
        content: msg.content,
        source: 'chat',
        date: formatLocalDate(),
      }).catch((err) => console.warn('timeline write failed:', err));
    } catch {
      setBookmarkState(prev => ({ ...prev, [msg.id]: 'error' }));
      // Auto-clear error after 2s
      setTimeout(() => {
        setBookmarkState(prev => {
          if (prev[msg.id] === 'error') return { ...prev, [msg.id]: 'idle' };
          return prev;
        });
      }, 2000);
    }
  }, [bookmarkState]);

  // ── Loading state ───────────────────────────────────────────

  if (loading) {
    return (
      <div className="flex flex-col h-full bg-[#f6f1e7] items-center justify-center">
        <Loader2 size={24} className="text-[#9e9590] animate-spin" />
        <p className="mt-3 text-xs font-mono text-[#9e9590]">Connecting...</p>
      </div>
    );
  }

  // No project available
  if (!project) {
    return (
      <div className="flex flex-col h-full bg-[#f6f1e7] items-center justify-center px-8 text-center">
        <WifiOff size={32} className="text-[#b5b0a8] mb-3" />
        <p className="text-sm text-[#6b6560]">No project available</p>
        <p className="text-xs font-mono text-[#9e9590] mt-1">Add a project in the admin panel first.</p>
      </div>
    );
  }

  // ── Bridge status label ─────────────────────────────────────

  const statusLabel = bridgeStatus === 'connected'
    ? 'connected'
    : bridgeStatus === 'connecting' || bridgeStatus === 'registering'
      ? 'connecting...'
      : 'disconnected';

  const statusColor = bridgeStatus === 'connected'
    ? 'text-[#2d6a4f]'
    : bridgeStatus === 'connecting' || bridgeStatus === 'registering'
      ? 'text-[#e85d3a]'
      : 'text-[#9e9590]';

  // ── Bookmark button helper ──────────────────────────────────

  const BookmarkBtn = ({ msg }: { msg: CompanionMsg }) => {
    if (!msg.content || msg.streaming) return null;
    const state = bookmarkState[msg.id];
    if (state === 'saved') {
      return (
        <button className="p-1 text-[#2d6a4f]" title="Saved">
          <BookmarkCheck size={13} />
        </button>
      );
    }
    if (state === 'saving') {
      return (
        <span className="p-1 text-[#9e9590]">
          <Loader2 size={13} className="animate-spin" />
        </span>
      );
    }
    return (
      <button
        onClick={() => handleBookmark(msg)}
        className={cn(
          'p-1 transition-colors rounded',
          state === 'error'
            ? 'text-[#e85d3a]'
            : 'text-[#b5b0a8] hover:text-[#6b6560]',
        )}
        title={state === 'error' ? 'Failed — retry' : 'Save'}
      >
        <Bookmark size={13} />
      </button>
    );
  };

  // ── Render ──────────────────────────────────────────────────

  return (
    <div className="flex flex-col h-full bg-[#f6f1e7]">
      {/* Header */}
      <div className="flex-shrink-0 px-5 pt-4 pb-3 flex items-end justify-between">
        <div className="min-w-0">
          <h1 className="font-serif text-2xl font-medium text-[#1a1915] tracking-tight leading-none">
            Chat
          </h1>
          <p className={cn('text-[11px] font-mono mt-1 tracking-wider uppercase', statusColor)}>
            {project.name} · {statusLabel}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <button className="w-9 h-9 rounded-full border border-[#e5dfd5] bg-[#fffcf5] flex items-center justify-center text-[#6b6560] hover:bg-[#ede8db] transition-colors">
            <Search size={16} />
          </button>
          <div className="w-9 h-9 rounded-full bg-[#1a1915] flex items-center justify-center">
            <span className="font-serif text-sm font-medium text-[#f6f1e7]">A</span>
          </div>
        </div>
      </div>

      {/* Messages */}
      <div ref={scrollRef} className="flex-1 overflow-y-auto px-4 pb-4 flex flex-col gap-0.5">
        {/* Empty state */}
        {messages.length === 0 && (
          <div className="flex-1 flex flex-col items-center justify-center text-center py-16">
            <div className="w-14 h-14 rounded-2xl bg-[#ede8db] border border-[#e5dfd5] flex items-center justify-center mb-4">
              <Circle size={24} className="text-[#b5b0a8]" />
            </div>
            <p className="text-sm text-[#6b6560]">Start a conversation</p>
            <p className="text-[11px] font-mono text-[#9e9590] mt-1">
              {bridgeStatus === 'connected' ? 'Type a message below' : 'Waiting for bridge connection...'}
            </p>
          </div>
        )}

        {/* Message list */}
        {messages.map((msg, i) => {
          const isUser = msg.role === 'user';
          const prev = messages[i - 1];
          const grouped = prev?.role === msg.role && i > 0;

          // Card / buttons — degraded to text placeholder
          if (msg.format === 'card' || msg.format === 'buttons') {
            return (
              <div key={msg.id} className="flex justify-start mt-1.5">
                <div className="max-w-[85%]">
                  <div className="px-3.5 py-2.5 text-sm rounded-2xl rounded-tl-md bg-[#fffcf5] border border-[#e5dfd5] text-[#6b6560]">
                    {msg.content ? (
                      <p className="whitespace-pre-wrap leading-relaxed">{msg.content}</p>
                    ) : (
                      <p className="text-xs italic text-[#b5b0a8]">[Interactive content]</p>
                    )}
                    {msg.buttons && (
                      <div className="mt-2 flex flex-wrap gap-1.5">
                        {msg.buttons.flat().map((btn, j) => (
                          <button
                            key={j}
                            onClick={() => sendCardAction(btn.data)}
                            className="px-2.5 py-1 text-xs font-medium rounded-lg bg-[#ede8db] text-[#1a1915] hover:bg-[#d4cfc5] transition-colors"
                          >
                            {btn.text}
                          </button>
                        ))}
                      </div>
                    )}
                  </div>
                  <div className="flex justify-start mt-0.5">
                    <BookmarkBtn msg={msg} />
                  </div>
                </div>
              </div>
            );
          }

          if (isUser) {
            return (
              <div key={msg.id} className={cn('flex justify-end', grouped ? '-mt-0.5' : 'mt-1.5')}>
                <div className="max-w-[78%] flex flex-col items-end gap-0.5">
                  <div className="px-3.5 py-2 text-sm leading-relaxed break-words bg-[#1a1915] text-[#f6f1e7] rounded-2xl rounded-tr-md">
                    {msg.content}
                  </div>
                  <div className="flex items-center gap-1 mr-0.5">
                    {!grouped && (
                      <span className="text-[10px] font-mono text-[#9e9590] tracking-wider">
                        {msg.timestamp ? new Date(msg.timestamp).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : ''}
                      </span>
                    )}
                    <BookmarkBtn msg={msg} />
                  </div>
                </div>
              </div>
            );
          }

          // Assistant / system message
          return (
            <div key={msg.id} className={cn('flex justify-start', grouped ? '-mt-0.5' : 'mt-1.5')}>
              <div className="max-w-[85%] flex flex-col items-start gap-0.5">
                {!grouped && (
                  <span className="text-[10px] font-mono text-[#9e9590] ml-0.5 tracking-wider">
                    Aelios · {msg.timestamp ? new Date(msg.timestamp).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : ''}
                  </span>
                )}
                <div
                  className={cn(
                    'px-3.5 py-2 text-sm leading-relaxed break-words',
                    'bg-[#fffcf5] text-[#1a1915] border border-[#e5dfd5] rounded-2xl rounded-tl-md',
                    msg.streaming && 'animate-pulse',
                  )}
                >
                  <p className="whitespace-pre-wrap">{msg.content}</p>
                  {msg.streaming && (
                    <span className="inline-block w-1.5 h-3.5 bg-[#e85d3a]/60 rounded-sm ml-0.5 animate-pulse" />
                  )}
                </div>
                <div className="flex items-center gap-1 ml-0.5">
                  <BookmarkBtn msg={msg} />
                </div>
              </div>
            </div>
          );
        })}

        {/* Typing indicator */}
        {typing && !messages.some(m => m.streaming) && (
          <div className="flex items-center gap-1.5 pt-1 text-[#9e9590]">
            <span className="text-[11px] font-mono">Aelios</span>
            <span className="inline-flex gap-0.5">
              {[0, 1, 2].map(i => (
                <span
                  key={i}
                  className="w-1 h-1 rounded-full bg-[#b5b0a8] animate-pulse"
                  style={{ animationDelay: `${i * 0.15}s` }}
                />
              ))}
            </span>
          </div>
        )}
      </div>

      {/* Composer */}
      <div className="flex-shrink-0 px-3 pb-3 pt-2 border-t border-[#e5dfd5] bg-[#f6f1e7]">
        {canSend ? (
          <div className="flex items-end gap-2 rounded-[22px] pl-3.5 pr-1.5 py-1.5 bg-[#ede8db] border border-[#e5dfd5]">
            <button className="w-7 h-7 rounded-full flex items-center justify-center text-[#9e9590] hover:text-[#6b6560] transition-colors flex-shrink-0">
              <Plus size={18} />
            </button>
            <textarea
              ref={textareaRef}
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && !e.shiftKey) {
                  e.preventDefault();
                  handleSend();
                }
              }}
              rows={1}
              placeholder="跟 Aelios 说点什么..."
              className={cn(
                'flex-1 bg-transparent resize-none outline-none border-0',
                'text-sm text-[#1a1915] placeholder:text-[#b5b0a8]',
                'py-1.5 min-h-[22px] max-h-20 overflow-y-auto',
              )}
            />
            <button
              onClick={handleSend}
              disabled={!draft.trim()}
              className={cn(
                'w-8 h-8 rounded-full flex items-center justify-center transition-all duration-200 flex-shrink-0',
                draft.trim()
                  ? 'bg-[#1a1915] text-[#f6f1e7]'
                  : 'text-[#b5b0a8]',
              )}
            >
              <Send size={14} />
            </button>
          </div>
        ) : (
          <div className="flex items-center gap-2 px-4 py-2.5 rounded-xl bg-[#ede8db] border border-[#e5dfd5]">
            <WifiOff size={14} className="text-[#9e9590]" />
            <span className="text-xs text-[#6b6560]">
              {bridgeStatus === 'connecting' || bridgeStatus === 'registering'
                ? 'Connecting to bridge...'
                : 'Bridge disconnected'}
            </span>
          </div>
        )}
      </div>
    </div>
  );
}
