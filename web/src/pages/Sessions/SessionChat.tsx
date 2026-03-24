import { useEffect, useState, useRef, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { useParams, Link } from 'react-router-dom';
import { ArrowLeft, Send, User, Bot } from 'lucide-react';
import { Badge } from '@/components/ui';
import { getSession, sendMessage, type SessionDetail } from '@/api/sessions';
import Markdown from 'react-markdown';
import { cn } from '@/lib/utils';

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
      setTimeout(fetchSession, 1000);
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
      <div className="flex items-center gap-3 pb-4 border-b border-gray-200 dark:border-gray-800">
        <Link to="/sessions" className="p-2 rounded-lg hover:bg-gray-100 dark:hover:bg-gray-800 transition-colors">
          <ArrowLeft size={18} className="text-gray-400" />
        </Link>
        <div>
          <h2 className="text-lg font-semibold text-gray-900 dark:text-white">{session?.name || id}</h2>
          <div className="flex items-center gap-2 mt-0.5">
            <Badge>{session?.platform}</Badge>
            <span className="text-xs text-gray-500">{session?.session_key}</span>
          </div>
        </div>
      </div>

      {/* Messages */}
      <div className="flex-1 overflow-y-auto py-4 space-y-4">
        {session?.history?.map((msg, i) => (
          <div key={i} className={cn('flex gap-3', msg.role === 'user' ? 'justify-end' : 'justify-start')}>
            {msg.role !== 'user' && (
              <div className="w-8 h-8 rounded-lg bg-accent/10 flex items-center justify-center shrink-0">
                <Bot size={16} className="text-accent" />
              </div>
            )}
            <div className={cn(
              'max-w-[70%] rounded-2xl px-4 py-3 text-sm',
              msg.role === 'user'
                ? 'bg-accent text-black rounded-br-md'
                : 'bg-gray-100 dark:bg-gray-800 text-gray-900 dark:text-gray-100 rounded-bl-md'
            )}>
              <div className="prose prose-sm dark:prose-invert max-w-none [&>p]:m-0">
                <Markdown>{msg.content}</Markdown>
              </div>
            </div>
            {msg.role === 'user' && (
              <div className="w-8 h-8 rounded-lg bg-gray-200 dark:bg-gray-700 flex items-center justify-center shrink-0">
                <User size={16} className="text-gray-500" />
              </div>
            )}
          </div>
        ))}
        <div ref={messagesEnd} />
      </div>

      {/* Input */}
      <div className="border-t border-gray-200 dark:border-gray-800 pt-4">
        <div className="flex gap-3">
          <input
            value={input}
            onChange={(e) => setInput(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder={t('sessions.messageInput')}
            className="flex-1 px-4 py-3 text-sm rounded-xl border border-gray-300 dark:border-gray-700 bg-white dark:bg-gray-800 text-gray-900 dark:text-white focus:outline-none focus:ring-2 focus:ring-accent/50 focus:border-accent transition-colors"
            disabled={sending}
          />
          <button
            onClick={handleSend}
            disabled={sending || !input.trim()}
            className="px-4 py-3 rounded-xl bg-accent text-black hover:bg-accent-dim transition-colors disabled:opacity-50"
          >
            <Send size={18} />
          </button>
        </div>
      </div>
    </div>
  );
}
