import { useTranslation } from 'react-i18next';
import {
  X, MessageSquare, Circle, User, Bot, Plus,
} from 'lucide-react';
import { Badge } from '@/components/ui';
import type { Session } from '@/api/sessions';
import { cn } from '@/lib/utils';

function timeAgo(iso: string): string {
  if (!iso) return '';
  const diff = Date.now() - new Date(iso).getTime();
  const mins = Math.floor(diff / 60000);
  if (mins < 1) return '<1m';
  if (mins < 60) return `${mins}m`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h`;
  return `${Math.floor(hours / 24)}d`;
}

interface Props {
  open: boolean;
  onClose: () => void;
  sessions: Session[];
  currentSessionId: string;
  onSelect: (session: Session) => void;
  onNewSession?: () => void;
}

export default function SessionDrawer({ open, onClose, sessions, currentSessionId, onSelect, onNewSession }: Props) {
  const { t } = useTranslation();

  return (
    <>
      {/* Backdrop */}
      {open && (
        <div className="fixed inset-0 bg-black/20 dark:bg-black/40 z-40 transition-opacity" onClick={onClose} />
      )}

      {/* Drawer */}
      <div
        className={cn(
          'fixed top-0 right-0 h-full w-80 z-50 flex flex-col transition-transform duration-300 ease-out',
          'bg-white/95 backdrop-blur-xl border-l border-gray-200/80 shadow-2xl shadow-black/15',
          'dark:bg-[rgba(15,15,15,0.97)] dark:border-white/[0.08] dark:shadow-black/50',
          open ? 'translate-x-0' : 'translate-x-full',
        )}
      >
        {/* Header */}
        <div className="flex items-center justify-between px-4 h-14 border-b border-gray-200/80 dark:border-white/[0.08] shrink-0">
          <h3 className="text-sm font-semibold text-gray-900 dark:text-white">{t('chat.sessions')}</h3>
          <div className="flex items-center gap-1">
            {onNewSession && (
              <button
                type="button"
                onClick={onNewSession}
                className="p-1.5 rounded-lg text-gray-400 hover:text-accent hover:bg-accent/10 transition-colors"
                title={t('cmd.new')}
              >
                <Plus size={16} />
              </button>
            )}
            <button
              type="button"
              onClick={onClose}
              className="p-1.5 rounded-lg text-gray-400 hover:text-gray-600 dark:hover:text-gray-200 hover:bg-gray-100 dark:hover:bg-white/[0.06] transition-colors"
            >
              <X size={16} />
            </button>
          </div>
        </div>

        {/* Session list */}
        <div className="flex-1 overflow-y-auto py-2 px-2">
          {sessions.length === 0 ? (
            <div className="text-center text-sm text-gray-400 py-8">{t('sessions.noSessions')}</div>
          ) : (
            sessions.map((s) => {
              const isCurrent = s.id === currentSessionId;
              return (
                <button
                  key={s.id}
                  type="button"
                  onClick={() => onSelect(s)}
                  className={cn(
                    'w-full text-left p-3 rounded-xl mb-1 transition-all duration-200',
                    isCurrent
                      ? 'bg-accent/10 ring-1 ring-accent/30'
                      : 'hover:bg-gray-100/80 dark:hover:bg-white/[0.04]',
                  )}
                >
                  <div className="flex items-start justify-between gap-2 mb-1">
                    <div className="flex items-center gap-1.5 min-w-0">
                      <MessageSquare
                        size={13}
                        className={cn(s.live ? 'text-accent' : 'text-gray-400', 'shrink-0')}
                      />
                      <span className="text-sm font-medium text-gray-900 dark:text-white truncate">
                        {s.name || s.user_name || s.id.slice(0, 8)}
                      </span>
                      {s.live && <Circle size={4} className="fill-emerald-500 text-emerald-500 shrink-0" />}
                    </div>
                    <span className="text-[10px] text-gray-400 shrink-0 mt-0.5">
                      {timeAgo(s.updated_at || s.created_at)}
                    </span>
                  </div>

                  {s.last_message && (
                    <p className="text-xs text-gray-500 dark:text-gray-400 line-clamp-1 leading-relaxed mb-1.5 pl-5">
                      {s.last_message.role === 'user' ? (
                        <User size={9} className="inline mr-0.5 -mt-0.5 opacity-60" />
                      ) : (
                        <Bot size={9} className="inline mr-0.5 -mt-0.5 opacity-60" />
                      )}
                      {s.last_message.content.replace(/\n/g, ' ').slice(0, 80)}
                    </p>
                  )}

                  <div className="flex items-center gap-1.5 pl-5">
                    {s.platform && <Badge variant="info" className="text-[9px] px-1 py-0">{s.platform}</Badge>}
                    <span className="text-[10px] text-gray-400 ml-auto">{s.history_count} msgs</span>
                  </div>
                </button>
              );
            })
          )}
        </div>
      </div>
    </>
  );
}
