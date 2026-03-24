import { useEffect, useState, useRef, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { useParams, Link } from 'react-router-dom';
import { ArrowLeft, Send, User, Bot, RotateCw, Circle, WifiOff, Copy, Check } from 'lucide-react';
import { Badge, Button } from '@/components/ui';
import { getSession, sendMessage, type SessionDetail } from '@/api/sessions';
import Markdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import rehypeHighlight from 'rehype-highlight';
import { cn } from '@/lib/utils';

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
  const code = typeof codeEl?.children === 'string'
    ? codeEl.children.replace(/\n$/, '')
    : '';

  return (
    <div className="not-prose relative group my-4">
      {lang && (
        <div className="absolute top-0 left-0 px-2.5 py-1 text-[10px] font-medium uppercase tracking-wider text-gray-400 dark:text-gray-500 bg-gray-100 dark:bg-gray-800 rounded-tl-lg rounded-br-lg border-b border-r border-gray-200 dark:border-gray-700 font-mono">
          {lang}
        </div>
      )}
      <CopyButton code={code} />
      <pre
        className="overflow-x-auto rounded-lg bg-[#fafafa] dark:bg-[#0d1117] border border-gray-200 dark:border-gray-700/60 p-4 pt-8 text-[13px] leading-[1.6] font-mono"
        {...props}
      >
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

function ChatMarkdown({ content, isUser }: { content: string; isUser: boolean }) {
  if (isUser) {
    return (
      <div className="prose prose-sm max-w-none [&_*]:text-black [&_code]:bg-black/10 [&_code]:text-black [&_a]:text-black [&_a]:underline [&>p]:my-0.5 [&_li]:my-0">
        <Markdown remarkPlugins={[remarkGfm]}>{content}</Markdown>
      </div>
    );
  }

  return (
    <div className={cn(
      'prose max-w-none dark:prose-invert',
      'prose-headings:font-semibold prose-headings:tracking-tight',
      'prose-h1:text-xl prose-h1:mt-5 prose-h1:mb-3 prose-h1:pb-1.5 prose-h1:border-b prose-h1:border-gray-200 dark:prose-h1:border-gray-700',
      'prose-h2:text-lg prose-h2:mt-5 prose-h2:mb-2',
      'prose-h3:text-base prose-h3:mt-4 prose-h3:mb-2',
      'prose-p:my-2.5 prose-p:leading-relaxed',
      'prose-li:my-0.5',
      'prose-ul:my-2 prose-ol:my-2',
      'prose-a:text-accent prose-a:no-underline hover:prose-a:underline',
      'prose-strong:text-gray-900 dark:prose-strong:text-white prose-strong:font-semibold',
      'prose-blockquote:border-l-[3px] prose-blockquote:border-accent/40 prose-blockquote:bg-accent/[0.03] prose-blockquote:rounded-r-lg prose-blockquote:py-0.5 prose-blockquote:px-4 prose-blockquote:my-3 prose-blockquote:not-italic prose-blockquote:text-gray-600 dark:prose-blockquote:text-gray-300',
      'prose-hr:my-5 prose-hr:border-gray-200 dark:prose-hr:border-gray-700',
      'prose-table:text-sm prose-th:bg-gray-50 dark:prose-th:bg-gray-800 prose-th:px-3 prose-th:py-2 prose-td:px-3 prose-td:py-2 prose-th:border-gray-200 dark:prose-th:border-gray-700 prose-td:border-gray-200 dark:prose-td:border-gray-700',
      'prose-img:rounded-lg prose-img:shadow-sm',
    )}>
      <Markdown
        remarkPlugins={[remarkGfm]}
        rehypePlugins={[rehypeHighlight]}
        components={{
          pre: PreBlock as any,
          code: InlineCode as any,
        }}
      >
        {content}
      </Markdown>
    </div>
  );
}

export default function SessionChat() {
  const { t } = useTranslation();
  const { project, id } = useParams<{ project: string; id: string }>();
  const [session, setSession] = useState<SessionDetail | null>(null);
  const [input, setInput] = useState('');
  const [sending, setSending] = useState(false);
  const [loading, setLoading] = useState(true);
  const messagesEnd = useRef<HTMLDivElement>(null);

  const fetchSession = useCallback(async () => {
    if (!project || !id) return;
    try {
      setLoading(true);
      const data = await getSession(project, id, 200);
      setSession(data);
    } finally {
      setLoading(false);
    }
  }, [project, id]);

  useEffect(() => {
    fetchSession();
  }, [fetchSession]);

  useEffect(() => {
    messagesEnd.current?.scrollIntoView({ behavior: 'smooth' });
  }, [session?.history]);

  const handleSend = async () => {
    if (!input.trim() || !project || !session) return;
    const msg = input.trim();
    setInput('');
    setSending(true);
    try {
      await sendMessage(project, { session_key: session.session_key, message: msg });
      setTimeout(fetchSession, 1500);
    } finally {
      setSending(false);
    }
  };

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      handleSend();
    }
  };

  if (loading && !session) {
    return <div className="flex items-center justify-center h-64 text-gray-400 animate-pulse">Loading...</div>;
  }

  return (
    <div className="flex flex-col h-[calc(100vh-8rem)] animate-fade-in">
      {/* Header */}
      <div className="flex items-center justify-between pb-4 border-b border-gray-200 dark:border-gray-800">
        <div className="flex items-center gap-3">
          <Link to="/sessions" className="p-2 rounded-lg hover:bg-gray-100 dark:hover:bg-gray-800 transition-colors">
            <ArrowLeft size={18} className="text-gray-400" />
          </Link>
          <div>
            <div className="flex items-center gap-2">
              <h2 className="text-lg font-semibold text-gray-900 dark:text-white">{session?.name || id}</h2>
              {session?.live ? (
                <span className="flex items-center gap-1 text-[10px] text-emerald-600 dark:text-emerald-400 bg-emerald-50 dark:bg-emerald-900/20 px-1.5 py-0.5 rounded-full">
                  <Circle size={5} className="fill-current" /> live
                </span>
              ) : (
                <span className="flex items-center gap-1 text-[10px] text-gray-400 bg-gray-100 dark:bg-gray-800 px-1.5 py-0.5 rounded-full">
                  <WifiOff size={9} /> {t('sessions.offline')}
                </span>
              )}
            </div>
            <div className="flex items-center gap-2 mt-0.5">
              <Badge>{project}</Badge>
              {session?.platform && <Badge variant="info">{session.platform}</Badge>}
              <span className="text-xs text-gray-500">{session?.session_key}</span>
            </div>
          </div>
        </div>
        <Button size="sm" variant="ghost" onClick={fetchSession}>
          <RotateCw size={14} /> {t('common.refresh')}
        </Button>
      </div>

      {/* Messages */}
      <div className="flex-1 overflow-y-auto py-6 space-y-5">
        {(!session?.history || session.history.length === 0) && (
          <p className="text-center text-sm text-gray-400 py-12">{t('sessions.noMessages')}</p>
        )}
        {session?.history?.map((msg, i) => {
          const isUser = msg.role === 'user';
          return (
            <div key={i} className={cn('flex gap-3', isUser ? 'justify-end' : 'justify-start')}>
              {!isUser && (
                <div className="w-8 h-8 rounded-lg bg-accent/10 flex items-center justify-center shrink-0 mt-1">
                  <Bot size={16} className="text-accent" />
                </div>
              )}
              <div className={cn(
                'rounded-2xl px-5 py-3.5 text-sm',
                isUser
                  ? 'max-w-[70%] bg-accent text-black rounded-br-md'
                  : 'max-w-[85%] bg-white dark:bg-gray-800/80 border border-gray-200 dark:border-gray-700/60 text-gray-900 dark:text-gray-100 rounded-bl-md shadow-sm'
              )}>
                <ChatMarkdown content={msg.content} isUser={isUser} />
              </div>
              {isUser && (
                <div className="w-8 h-8 rounded-lg bg-gray-200 dark:bg-gray-700 flex items-center justify-center shrink-0 mt-1">
                  <User size={16} className="text-gray-500" />
                </div>
              )}
            </div>
          );
        })}
        <div ref={messagesEnd} />
      </div>

      {/* Input */}
      <div className="border-t border-gray-200 dark:border-gray-800 pt-4">
        {session?.live ? (
          <div className="flex gap-3">
            <input
              value={input}
              onChange={(e) => setInput(e.target.value)}
              onKeyDown={handleKeyDown}
              placeholder={t('sessions.messageInput')}
              className="flex-1 px-4 py-3 text-sm rounded-xl border border-gray-300 dark:border-gray-700 bg-white dark:bg-gray-800 text-gray-900 dark:text-white focus:outline-none focus:ring-2 focus:ring-accent/50 focus:border-accent transition-colors placeholder:text-gray-400"
              disabled={sending}
            />
            <button
              onClick={handleSend}
              disabled={sending || !input.trim()}
              className="px-4 py-3 rounded-xl bg-accent text-black hover:bg-accent-dim transition-colors disabled:opacity-50 flex items-center gap-2"
            >
              {sending ? (
                <svg className="animate-spin h-4 w-4" viewBox="0 0 24 24">
                  <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" fill="none" />
                  <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z" />
                </svg>
              ) : (
                <Send size={18} />
              )}
            </button>
          </div>
        ) : (
          <div className="flex items-center gap-2 px-4 py-3 text-sm text-gray-400 dark:text-gray-500 bg-gray-50 dark:bg-gray-800/50 rounded-xl">
            <WifiOff size={14} />
            <span>{t('sessions.notLiveHint')}</span>
          </div>
        )}
      </div>
    </div>
  );
}
