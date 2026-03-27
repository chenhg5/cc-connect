import { useEffect, useState, useRef, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { useParams, Link } from 'react-router-dom';
import {
  ArrowLeft, Send, User, Bot, Circle, WifiOff,
  Copy, Check, FileText, Image as ImageIcon, Loader2,
  Slash, ChevronDown,
} from 'lucide-react';
import { Badge, Button } from '@/components/ui';
import { listSessions, getSession, type Session, type SessionDetail } from '@/api/sessions';
import {
  useBridgeSocket, fetchBridgeConfig,
  type BridgeConfig, type BridgeIncoming, type BridgeStatus,
} from '@/hooks/useBridgeSocket';
import CommandPalette, { type SlashCommand, slashCommands } from './CommandPalette';
import SessionDrawer from './SessionDrawer';
import CommandResultPanel, { type CommandResult } from './CommandResultPanel';
import Markdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import rehypeHighlight from 'rehype-highlight';
import { cn } from '@/lib/utils';

// ── Markdown renderers ───────────────────────────────────────

function CopyButton({ code }: { code: string }) {
  const [copied, setCopied] = useState(false);
  const handleCopy = () => {
    navigator.clipboard.writeText(code);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };
  return (
    <button
      onClick={handleCopy}
      className="absolute top-2 right-2 p-1.5 rounded-md bg-gray-200/80 dark:bg-gray-700/80 hover:bg-gray-300 dark:hover:bg-gray-600 text-gray-500 dark:text-gray-400 opacity-0 group-hover:opacity-100 transition-opacity z-10"
    >
      {copied ? <Check size={12} /> : <Copy size={12} />}
    </button>
  );
}

function PreBlock({ children, ...props }: React.HTMLAttributes<HTMLPreElement>) {
  const codeEl = (children as any)?.props;
  const lang = codeEl?.className?.replace(/^language-/, '') || '';
  const code = typeof codeEl?.children === 'string' ? codeEl.children.replace(/\n$/, '') : '';
  return (
    <div className="not-prose relative group my-4">
      {lang && (
        <div className="absolute top-0 left-0 px-2.5 py-1 text-[10px] font-medium uppercase tracking-wider text-gray-400 dark:text-gray-500 bg-gray-100 dark:bg-gray-800 rounded-tl-lg rounded-br-lg border-b border-r border-gray-200 dark:border-gray-700 font-mono">
          {lang}
        </div>
      )}
      <CopyButton code={code} />
      <pre className="overflow-x-auto rounded-lg bg-[#fafafa] dark:bg-[#0d1117] border border-gray-200 dark:border-gray-700/60 p-4 pt-8 text-[13px] leading-[1.6] font-mono" {...props}>
        {children}
      </pre>
    </div>
  );
}

function InlineCode({ children, className, ...props }: React.HTMLAttributes<HTMLElement>) {
  if (className) return <code className={className} {...props}>{children}</code>;
  return (
    <code className="px-1.5 py-0.5 rounded-md bg-gray-100 dark:bg-gray-800 text-pink-600 dark:text-pink-400 text-[0.875em] font-mono border border-gray-200/60 dark:border-gray-700/40" {...props}>
      {children}
    </code>
  );
}

function RenderMarkdown({ content }: { content: string }) {
  return (
    <div className={cn(
      'prose max-w-none dark:prose-invert',
      'prose-headings:font-semibold prose-headings:tracking-tight',
      'prose-h1:text-xl prose-h1:mt-5 prose-h1:mb-3 prose-h1:pb-1.5 prose-h1:border-b prose-h1:border-gray-200 dark:prose-h1:border-gray-700',
      'prose-h2:text-lg prose-h2:mt-5 prose-h2:mb-2',
      'prose-h3:text-base prose-h3:mt-4 prose-h3:mb-2',
      'prose-p:my-2.5 prose-p:leading-relaxed',
      'prose-li:my-0.5', 'prose-ul:my-2 prose-ol:my-2',
      'prose-a:text-accent prose-a:no-underline hover:prose-a:underline',
      'prose-strong:text-gray-900 dark:prose-strong:text-white prose-strong:font-semibold',
      'prose-blockquote:border-l-[3px] prose-blockquote:border-accent/40 prose-blockquote:bg-accent/[0.03] prose-blockquote:rounded-r-lg prose-blockquote:py-0.5 prose-blockquote:px-4 prose-blockquote:my-3 prose-blockquote:not-italic prose-blockquote:text-gray-600 dark:prose-blockquote:text-gray-300',
      'prose-hr:my-5 prose-hr:border-gray-200 dark:prose-hr:border-gray-700',
      'prose-table:text-sm prose-th:bg-gray-50 dark:prose-th:bg-gray-800 prose-th:px-3 prose-th:py-2 prose-td:px-3 prose-td:py-2',
      'prose-img:rounded-lg prose-img:shadow-sm',
    )}>
      <Markdown remarkPlugins={[remarkGfm]} rehypePlugins={[rehypeHighlight]} components={{ pre: PreBlock as any, code: InlineCode as any }}>
        {content}
      </Markdown>
    </div>
  );
}

// ── Chat message types ───────────────────────────────────────

interface ChatMsg {
  id: string;
  role: 'user' | 'assistant' | 'system';
  content: string;
  format?: 'text' | 'markdown' | 'card' | 'buttons' | 'image' | 'file';
  card?: any;
  buttons?: { text: string; data: string }[][];
  imageUrl?: string;
  fileName?: string;
  fileSize?: number;
  streaming?: boolean;
  timestamp?: string;
}

// ── Helpers ──────────────────────────────────────────────────

function parseListItemText(text: string): { cmd: string; desc: string } {
  const m = text.match(/^\*\*(.+?)\*\*\s*(.*)/);
  if (m) return { cmd: m[1], desc: m[2] };
  const sp = text.indexOf(' ');
  if (sp > 0) return { cmd: text.slice(0, sp), desc: text.slice(sp + 1) };
  return { cmd: text, desc: '' };
}

function InlineMd({ text }: { text: string }) {
  const parts = text.split(/(\*\*[^*]+\*\*)/g);
  return (
    <>
      {parts.map((p, i) =>
        p.startsWith('**') && p.endsWith('**')
          ? <strong key={i} className="font-semibold text-gray-900 dark:text-white">{p.slice(2, -2)}</strong>
          : <span key={i}>{p}</span>
      )}
    </>
  );
}

// ── Card renderer (flat, clean style for in-stream cards) ────

function CardBlock({ card, onAction }: { card: any; onAction: (v: string) => void }) {
  if (!card) return null;
  return (
    <div className="space-y-3">
      {card.header?.title && (
        <div className="text-sm font-semibold text-gray-900 dark:text-white">{card.header.title}</div>
      )}
      {card.elements?.map((el: any, i: number) => (
        <CardElement key={i} el={el} onAction={onAction} />
      ))}
    </div>
  );
}

function CardElement({ el, onAction }: { el: any; onAction: (v: string) => void }) {
  if (el.type === 'markdown') return <RenderMarkdown content={el.content} />;
  if (el.type === 'divider') return <div className="border-t border-gray-200/60 dark:border-gray-700/40" />;
  if (el.type === 'note') return <p className="text-[11px] text-gray-400 dark:text-gray-500">{el.text}</p>;
  if (el.type === 'actions') {
    return (
      <div className="flex flex-wrap gap-2">
        {el.buttons?.map((btn: any, j: number) => (
          <button key={j} onClick={() => onAction(btn.value)} className={cn(
            'px-3 py-1.5 rounded-lg text-xs font-medium transition-all duration-150',
            btn.btn_type === 'primary' ? 'bg-accent text-black hover:bg-accent-dim shadow-sm' :
            btn.btn_type === 'danger' ? 'bg-red-500/10 text-red-600 dark:text-red-400 hover:bg-red-500/20' :
            'bg-gray-100 dark:bg-gray-800 text-gray-600 dark:text-gray-300 hover:bg-gray-200 dark:hover:bg-gray-700',
          )}>
            {btn.text}
          </button>
        ))}
      </div>
    );
  }
  if (el.type === 'list_item') {
    const parsed = parseListItemText(el.text);
    const isCommand = parsed.cmd.startsWith('/');
    return (
      <button
        onClick={() => onAction(el.btn_value)}
        className="w-full flex items-center gap-3 py-2 text-left group"
      >
        {isCommand ? (
          <>
            <code className="shrink-0 w-20 text-xs font-mono font-medium text-accent">{parsed.cmd}</code>
            <span className="flex-1 text-sm text-gray-500 dark:text-gray-400 truncate">{parsed.desc}</span>
          </>
        ) : (
          <span className="flex-1 text-sm text-gray-700 dark:text-gray-300 truncate min-w-0">
            <InlineMd text={el.text} />
          </span>
        )}
        <span className={cn(
          'shrink-0 px-2 py-0.5 rounded-md text-[11px] font-medium transition-all',
          el.btn_type === 'primary'
            ? 'bg-accent/15 text-accent group-hover:bg-accent/25'
            : 'text-gray-400 dark:text-gray-500 bg-gray-100 dark:bg-gray-800 group-hover:bg-accent/15 group-hover:text-accent',
        )}>
          {el.btn_text}
        </span>
      </button>
    );
  }
  if (el.type === 'select') {
    return (
      <select
        defaultValue={el.init_value}
        onChange={(e) => onAction(e.target.value)}
        className="w-full px-3 py-2 text-sm rounded-lg border border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-800/80 text-gray-900 dark:text-white focus:outline-none focus:ring-2 focus:ring-accent/40"
      >
        {el.options?.map((opt: any, j: number) => (
          <option key={j} value={opt.value}>{opt.text}</option>
        ))}
      </select>
    );
  }
  return null;
}

function ButtonsBlock({ content, buttons, onAction }: { content: string; buttons: { text: string; data: string }[][]; onAction: (v: string) => void }) {
  return (
    <div className="space-y-3">
      <RenderMarkdown content={content} />
      {buttons.map((row, i) => (
        <div key={i} className="flex flex-wrap gap-2">
          {row.map((btn, j) => (
            <button key={j} onClick={() => onAction(btn.data)} className="px-3 py-1.5 rounded-lg text-xs font-medium bg-accent text-black hover:bg-accent-dim transition-colors">
              {btn.text}
            </button>
          ))}
        </div>
      ))}
    </div>
  );
}

function FileBlock({ name, size }: { name: string; size?: number }) {
  return (
    <div className="flex items-center gap-2 px-3 py-2 rounded-lg bg-gray-50 dark:bg-gray-800 border border-gray-200 dark:border-gray-700">
      <FileText size={16} className="text-gray-400 shrink-0" />
      <div className="min-w-0">
        <div className="text-sm font-medium text-gray-900 dark:text-white truncate">{name}</div>
        {size !== undefined && <div className="text-xs text-gray-400">{(size / 1024).toFixed(1)} KB</div>}
      </div>
    </div>
  );
}

function ImageBlock({ url }: { url: string }) {
  return <img src={url} alt="" className="max-w-sm rounded-lg border border-gray-200 dark:border-gray-700 shadow-sm" />;
}

function StatusBadge({ status }: { status: BridgeStatus }) {
  const { t } = useTranslation();
  if (status === 'connected') {
    return (
      <span className="flex items-center gap-1 text-[10px] text-emerald-600 dark:text-emerald-400 bg-emerald-50 dark:bg-emerald-900/20 px-1.5 py-0.5 rounded-full">
        <Circle size={5} className="fill-current" /> {t('sessions.bridgeConnected')}
      </span>
    );
  }
  if (status === 'connecting' || status === 'registering') {
    return (
      <span className="flex items-center gap-1 text-[10px] text-yellow-600 dark:text-yellow-400 bg-yellow-50 dark:bg-yellow-900/20 px-1.5 py-0.5 rounded-full">
        <Loader2 size={9} className="animate-spin" /> {t('sessions.bridgeConnecting')}
      </span>
    );
  }
  return (
    <span className="flex items-center gap-1 text-[10px] text-gray-400 bg-gray-100 dark:bg-gray-800 px-1.5 py-0.5 rounded-full">
      <WifiOff size={9} /> {t('sessions.bridgeDisconnected')}
    </span>
  );
}

// ── Main component ───────────────────────────────────────────

export default function ChatView() {
  const { t } = useTranslation();
  const { name: projectName } = useParams<{ name: string }>();

  // Session state
  const [sessions, setSessions] = useState<Session[]>([]);
  const [currentSession, setCurrentSession] = useState<SessionDetail | null>(null);
  const [messages, setMessages] = useState<ChatMsg[]>([]);
  const [input, setInput] = useState('');
  const [sending, setSending] = useState(false);
  const [loading, setLoading] = useState(true);
  const [typing, setTyping] = useState(false);
  const [bridgeCfg, setBridgeCfg] = useState<BridgeConfig | null>(null);
  // Whether the user explicitly picked a session from the drawer
  const [userPickedSession, setUserPickedSession] = useState(false);

  // UI state
  const [cmdOpen, setCmdOpen] = useState(false);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [cmdResult, setCmdResult] = useState<CommandResult | null>(null);

  const messagesEnd = useRef<HTMLDivElement>(null);
  const previewHandleCounter = useRef(0);
  const cmdBtnRef = useRef<HTMLButtonElement>(null);
  const sessionKeyRef = useRef('');
  // Track pending slash command so the next reply can be routed to the panel
  const pendingCmdRef = useRef<string | null>(null);
  // Mirrors cmdResult.command so card-action callbacks can route follow-ups back to the panel
  const cmdPanelRef = useRef<string | null>(null);

  // Web platform uses its own per-project session key by default.
  // Only use the original session's key when the user explicitly switches via the drawer.
  const webSessionKey = projectName ? `bridge:web-admin:${projectName}` : '';
  const sessionKey = userPickedSession && currentSession?.session_key
    ? currentSession.session_key
    : webSessionKey;
  sessionKeyRef.current = sessionKey;

  // Load project sessions and auto-select latest
  const fetchData = useCallback(async () => {
    if (!projectName) return;
    setLoading(true);
    try {
      const [{ sessions: allSessions }, cfg] = await Promise.all([
        listSessions(projectName),
        fetchBridgeConfig(),
      ]);
      setBridgeCfg(cfg);
      const sorted = (allSessions || []).sort(
        (a, b) => (b.updated_at || b.created_at || '').localeCompare(a.updated_at || a.created_at || ''),
      );
      setSessions(sorted);

      if (sorted.length > 0) {
        const latest = sorted[0];
        const detail = await getSession(projectName, latest.id, 200);
        setCurrentSession(detail);
        if (detail.history) {
          setMessages(detail.history.map((h, i) => ({
            id: `hist-${i}`,
            role: h.role as 'user' | 'assistant',
            content: h.content,
            format: 'markdown',
            timestamp: h.timestamp,
          })));
        }
      } else {
        setCurrentSession(null);
        setMessages([]);
      }
    } finally {
      setLoading(false);
    }
  }, [projectName]);

  useEffect(() => { fetchData(); }, [fetchData]);

  // Keep ref in sync with cmdResult so callbacks avoid stale closures
  useEffect(() => {
    cmdPanelRef.current = cmdResult?.command ?? null;
  }, [cmdResult]);

  // Switch to a different session (user explicitly chose from drawer)
  const switchToSession = useCallback(async (s: Session) => {
    if (!projectName) return;
    setDrawerOpen(false);
    setLoading(true);
    setUserPickedSession(true);
    try {
      const detail = await getSession(projectName, s.id, 200);
      setCurrentSession(detail);
      if (detail.history) {
        setMessages(detail.history.map((h, i) => ({
          id: `hist-${i}`,
          role: h.role as 'user' | 'assistant',
          content: h.content,
          format: 'markdown',
          timestamp: h.timestamp,
        })));
      } else {
        setMessages([]);
      }
    } finally {
      setLoading(false);
    }
  }, [projectName]);

  // Handle bridge incoming messages — only process messages for the current session
  const handleBridgeMessage = useCallback((msg: BridgeIncoming) => {
    const msgKey = (msg as any).session_key;
    if (msgKey && sessionKeyRef.current && msgKey !== sessionKeyRef.current) {
      return;
    }

    // If a slash command is pending, route the first reply/card to the panel
    const pending = pendingCmdRef.current;
    if (pending && (msg.type === 'reply' || msg.type === 'card' || msg.type === 'buttons')) {
      pendingCmdRef.current = null;
      if (msg.type === 'card') {
        const card = msg as Extract<BridgeIncoming, { type: 'card' }>;
        setCmdResult({ command: pending, content: '', format: 'card', card: card.card });
      } else if (msg.type === 'buttons') {
        const btns = msg as Extract<BridgeIncoming, { type: 'buttons' }>;
        setCmdResult({ command: pending, content: btns.content, format: 'buttons', buttons: btns.buttons });
      } else {
        const reply = msg as Extract<BridgeIncoming, { type: 'reply' }>;
        setCmdResult({ command: pending, content: reply.content, format: 'markdown' });
      }
      setTyping(false);
      return;
    }

    if (msg.type === 'reply') {
      setMessages(prev => {
        const streamIdx = prev.findIndex(m => m.streaming && m.role === 'assistant');
        if (streamIdx >= 0) {
          const updated = [...prev];
          updated[streamIdx] = { ...updated[streamIdx], content: msg.content, format: (msg as any).format === 'markdown' ? 'markdown' : 'text', streaming: false };
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
      const handle = `web-preview-${++previewHandleCounter.current}`;
      sendPreviewAck(ps.ref_id, handle);
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
    sessionKey,
    projectName: projectName || '',
    onMessage: handleBridgeMessage,
  });

  // Scroll to bottom on new messages
  useEffect(() => {
    messagesEnd.current?.scrollIntoView({ behavior: 'smooth' });
  }, [messages, typing]);

  // Send message
  const handleSend = useCallback(() => {
    if (!input.trim() || bridgeStatus !== 'connected') return;
    const content = input.trim();
    setInput('');
    setSending(true);

    const cmdToken = content.split(' ')[0];
    const isKnownCmd = knownCommands.has(cmdToken);
    if (isKnownCmd && !chatCommands.has(cmdToken)) {
      pendingCmdRef.current = cmdToken;
    } else {
      setMessages(prev => [...prev, { id: `user-${Date.now()}`, role: 'user', content }]);
    }
    bridgeSend(content);
    setTimeout(() => setSending(false), 300);
  }, [input, bridgeStatus, bridgeSend]);

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      handleSend();
    }
    if (e.key === '/' && !input) {
      e.preventDefault();
      setCmdOpen(true);
    }
  };

  // Commands whose result should go to the message stream (they change state)
  const chatCommands = new Set(['/new', '/stop', '/switch', '/delete-mode', '/upgrade']);
  const knownCommands = new Set(slashCommands.map(c => c.cmd));

  const handleCmdSelect = useCallback((cmd: SlashCommand) => {
    setCmdOpen(false);
    if (bridgeStatus !== 'connected') return;

    if (chatCommands.has(cmd.cmd)) {
      setMessages(prev => [...prev, { id: `user-${Date.now()}`, role: 'user', content: cmd.cmd }]);
    } else {
      pendingCmdRef.current = cmd.cmd;
    }
    bridgeSend(cmd.cmd);
  }, [bridgeStatus, bridgeSend]);

  const handleCardAction = useCallback((value: string) => {
    if (bridgeStatus !== 'connected') return;
    // If the command panel is showing, route the follow-up response back to it
    if (cmdPanelRef.current) {
      pendingCmdRef.current = cmdPanelRef.current;
    }
    sendCardAction(value);
  }, [bridgeStatus, sendCardAction]);

  const handleNewSession = useCallback(() => {
    if (bridgeStatus !== 'connected') return;
    setUserPickedSession(false);
    setMessages(prev => [...prev, { id: `user-${Date.now()}`, role: 'user', content: '/new' }]);
    bridgeSend('/new');
    setDrawerOpen(false);
  }, [bridgeStatus, bridgeSend]);

  const canSend = bridgeStatus === 'connected';

  if (loading && !currentSession && sessions.length === 0) {
    return <div className="flex items-center justify-center h-64 text-gray-400 animate-pulse">Loading...</div>;
  }

  return (
    <div className="flex flex-col h-[calc(100vh-8rem)] animate-fade-in">
      {/* Header */}
      <div className="flex items-center justify-between pb-3 border-b border-gray-200 dark:border-gray-800 shrink-0">
        <div className="flex items-center gap-3">
          <Link to="/chat" className="p-2 rounded-lg hover:bg-gray-100 dark:hover:bg-gray-800 transition-colors">
            <ArrowLeft size={18} className="text-gray-400" />
          </Link>
          <div>
            <div className="flex items-center gap-2">
              <h2 className="text-lg font-semibold text-gray-900 dark:text-white">{projectName}</h2>
              <StatusBadge status={bridgeStatus} />
            </div>
            <button
              type="button"
              onClick={() => setDrawerOpen(true)}
              className="flex items-center gap-1 text-xs text-gray-500 hover:text-accent transition-colors mt-0.5"
            >
              <span>{userPickedSession && currentSession
                ? (currentSession.name || currentSession.id.slice(0, 8))
                : t('chat.defaultSession')}</span>
              <ChevronDown size={12} />
            </button>
          </div>
        </div>
      </div>

      {/* Messages */}
      <div className="flex-1 overflow-y-auto py-6 space-y-5">
        {messages.length === 0 && !loading && (
          <div className="flex flex-col items-center justify-center h-full text-center py-12">
            <div className="w-16 h-16 rounded-2xl bg-accent/10 flex items-center justify-center mb-4">
              <Bot size={32} className="text-accent" />
            </div>
            <p className="text-sm text-gray-500 dark:text-gray-400 mb-1">{t('chat.emptyHint')}</p>
            <p className="text-xs text-gray-400 dark:text-gray-500">{t('chat.slashHint')}</p>
          </div>
        )}
        {messages.map((msg) => {
          const isUser = msg.role === 'user';
          return (
            <div key={msg.id} className={cn('flex gap-3', isUser ? 'justify-end' : 'justify-start')}>
              {!isUser && (
                <div className="w-8 h-8 rounded-lg bg-accent/10 flex items-center justify-center shrink-0 mt-1">
                  <Bot size={16} className="text-accent" />
                </div>
              )}
              <div className={cn(
                'rounded-2xl px-5 py-3.5 text-sm',
                isUser
                  ? 'max-w-[70%] bg-accent text-black rounded-br-md'
                  : 'max-w-[85%] bg-white dark:bg-gray-800/80 border border-gray-200 dark:border-gray-700/60 text-gray-900 dark:text-gray-100 rounded-bl-md shadow-sm',
                msg.streaming && 'animate-pulse-subtle',
              )}>
                {msg.format === 'card' ? (
                  <CardBlock card={msg.card} onAction={handleCardAction} />
                ) : msg.format === 'buttons' && msg.buttons ? (
                  <ButtonsBlock content={msg.content} buttons={msg.buttons} onAction={handleCardAction} />
                ) : msg.format === 'image' && msg.imageUrl ? (
                  <ImageBlock url={msg.imageUrl} />
                ) : msg.format === 'file' && msg.fileName ? (
                  <FileBlock name={msg.fileName} size={msg.fileSize} />
                ) : isUser ? (
                  <div className="whitespace-pre-wrap">{msg.content}</div>
                ) : (
                  <RenderMarkdown content={msg.content} />
                )}
                {msg.streaming && (
                  <span className="inline-block w-1.5 h-4 bg-accent/60 rounded-sm ml-0.5 animate-pulse" />
                )}
              </div>
              {isUser && (
                <div className="w-8 h-8 rounded-lg bg-gray-200 dark:bg-gray-700 flex items-center justify-center shrink-0 mt-1">
                  <User size={16} className="text-gray-500" />
                </div>
              )}
            </div>
          );
        })}
        {typing && !messages.some(m => m.streaming) && (
          <div className="flex gap-3 justify-start">
            <div className="w-8 h-8 rounded-lg bg-accent/10 flex items-center justify-center shrink-0 mt-1">
              <Bot size={16} className="text-accent" />
            </div>
            <div className="rounded-2xl px-5 py-3.5 text-sm bg-white dark:bg-gray-800/80 border border-gray-200 dark:border-gray-700/60 rounded-bl-md shadow-sm">
              <div className="flex gap-1.5">
                <span className="w-2 h-2 bg-gray-400 rounded-full animate-bounce" style={{ animationDelay: '0ms' }} />
                <span className="w-2 h-2 bg-gray-400 rounded-full animate-bounce" style={{ animationDelay: '150ms' }} />
                <span className="w-2 h-2 bg-gray-400 rounded-full animate-bounce" style={{ animationDelay: '300ms' }} />
              </div>
            </div>
          </div>
        )}
        <div ref={messagesEnd} />
      </div>

      {/* Input area */}
      <div className="border-t border-gray-200 dark:border-gray-800 pt-3 shrink-0">
        {canSend ? (
          <div className="relative flex items-end gap-2">
            {/* Command palette trigger */}
            <div className="relative">
              <button
                ref={cmdBtnRef}
                type="button"
                onClick={() => setCmdOpen(!cmdOpen)}
                className={cn(
                  'p-3 rounded-xl transition-all duration-200',
                  cmdOpen
                    ? 'bg-accent/15 text-accent ring-1 ring-accent/30'
                    : 'text-gray-400 hover:text-gray-600 dark:hover:text-gray-200 hover:bg-gray-100 dark:hover:bg-white/[0.06]',
                )}
                title={t('chat.commands')}
              >
                <Slash size={18} />
              </button>
              <CommandPalette
                open={cmdOpen}
                onClose={() => setCmdOpen(false)}
                onSelect={handleCmdSelect}
                anchorRef={cmdBtnRef}
              />
            </div>

            {/* Text input */}
            <div className="flex-1 relative">
              <input
                value={input}
                onChange={(e) => setInput(e.target.value)}
                onKeyDown={handleKeyDown}
                placeholder={t('chat.inputPlaceholder')}
                className="w-full px-4 py-3 text-sm rounded-xl border border-gray-300 dark:border-gray-700 bg-white dark:bg-gray-800 text-gray-900 dark:text-white focus:outline-none focus:ring-2 focus:ring-accent/50 focus:border-accent transition-colors placeholder:text-gray-400"
                disabled={sending}
              />
            </div>

            {/* Send button */}
            <button
              type="button"
              onClick={handleSend}
              disabled={sending || !input.trim()}
              className="p-3 rounded-xl bg-accent text-black hover:bg-accent-dim transition-colors disabled:opacity-50 flex items-center"
            >
              {sending ? <Loader2 size={18} className="animate-spin" /> : <Send size={18} />}
            </button>
          </div>
        ) : bridgeStatus === 'disconnected' || bridgeStatus === 'error' ? (
          <div className="flex items-center gap-2 px-4 py-3 text-sm text-amber-600 dark:text-amber-400 bg-amber-50 dark:bg-amber-900/20 rounded-xl">
            <WifiOff size={14} />
            <span>{t('sessions.bridgeNotAvailable')}</span>
          </div>
        ) : (
          <div className="flex items-center gap-2 px-4 py-3 text-sm text-gray-400 bg-gray-50 dark:bg-gray-800/50 rounded-xl">
            <Loader2 size={14} className="animate-spin" />
            <span>{t('sessions.bridgeConnecting')}</span>
          </div>
        )}
      </div>

      {/* Session drawer */}
      <SessionDrawer
        open={drawerOpen}
        onClose={() => setDrawerOpen(false)}
        sessions={sessions}
        currentSessionId={currentSession?.id || ''}
        onSelect={switchToSession}
        onNewSession={handleNewSession}
      />

      {/* Command result panel */}
      <CommandResultPanel
        result={cmdResult}
        onClose={() => setCmdResult(null)}
        onCardAction={handleCardAction}
      />
    </div>
  );
}
