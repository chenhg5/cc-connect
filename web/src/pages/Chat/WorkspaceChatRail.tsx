import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  ChevronDown,
  ChevronRight,
  Code2,
  Copy,
  FilePlus2,
  Folder,
  FolderGit2,
  Loader2,
  MessageSquare,
  Plus,
  X,
} from 'lucide-react';
import { cn } from '@/lib/utils';
import type { ConversationRef, NativeThread, Workspace } from '@/api/workspaceChat';
import { statusType } from './workspaceChatState';

interface Props {
  open: boolean;
  loading: boolean;
  creating: boolean;
  workspaces: Workspace[];
  threads: NativeThread[];
  workspaceRef?: string;
  conversation?: ConversationRef;
  onClose: () => void;
  onWorkspace: (workspace: Workspace) => void;
  onThread: (thread: NativeThread) => void;
  onNew: () => void;
  onCopyLink: (thread: NativeThread) => void;
}

function threadLabel(thread: NativeThread): string {
  return thread.name || thread.preview || thread.id.slice(0, 12);
}

function WorkspaceButton({ workspace, active, onClick, rootOnly = false }: { workspace: Workspace; active: boolean; onClick: () => void; rootOnly?: boolean }) {
  return (
    <button
      type="button"
      disabled={!workspace.available}
      onClick={onClick}
      title={workspace.available ? workspace.root_path : workspace.error}
      className={cn(
        'mb-1 flex w-full min-w-0 items-start gap-2 rounded-md px-2 py-2 text-left transition-colors',
        active ? 'bg-accent/12 text-accent' : 'hover:bg-gray-100 dark:hover:bg-white/[0.06]',
        !workspace.available && 'cursor-not-allowed opacity-45',
      )}
    >
      {rootOnly ? <Code2 size={15} className="mt-0.5 shrink-0" /> : <Folder size={15} className="mt-0.5 shrink-0 text-amber-500" />}
      <span className="min-w-0 flex-1">
        <span className="block truncate text-sm font-medium">{rootOnly ? workspace.root_name : workspace.project_name}</span>
        <span className="block truncate text-[10px] text-gray-400">{workspace.available ? workspace.root_path : workspace.error}</span>
      </span>
    </button>
  );
}

export function WorkspaceChatRail(props: Props) {
  const { t } = useTranslation();
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const groupedWorkspaces = useMemo(() => {
    const groups = new Map<string, Workspace[]>();
    props.workspaces.forEach((workspace) => groups.set(workspace.project_id, [...(groups.get(workspace.project_id) || []), workspace]));
    return [...groups.values()];
  }, [props.workspaces]);

  useEffect(() => {
    setExpanded(new Set(groupedWorkspaces.map((roots) => roots[0]?.project_id).filter(Boolean)));
  }, [groupedWorkspaces]);

  return (
    <>
      <aside className={cn(
        'absolute inset-y-0 left-0 z-30 flex w-[min(19rem,calc(100vw-4rem))] flex-col border-r border-gray-200 bg-white transition-transform dark:border-white/[0.08] dark:bg-[#0b0b0d] md:static md:z-auto md:w-[19rem] md:translate-x-0',
        props.open ? 'translate-x-0' : '-translate-x-full',
      )}>
        <div className="flex h-12 shrink-0 items-center justify-between border-b border-gray-200 px-3 dark:border-white/[0.08]">
          <div className="flex min-w-0 items-center gap-2 text-sm font-semibold"><FolderGit2 size={16} className="shrink-0" /><span className="truncate">{t('workspaceChat.workspaces')}</span></div>
          <button type="button" className="rounded-md p-1 hover:bg-gray-100 md:hidden dark:hover:bg-white/[0.08]" onClick={props.onClose} aria-label={t('common.close')}><X size={17} /></button>
        </div>

        <nav className="min-h-0 flex-1 overflow-y-auto p-2">
          {groupedWorkspaces.map((roots) => {
            const project = roots[0];
            if (!project) return null;
            const isExpanded = expanded.has(project.project_id);
            if (roots.length === 1) {
              return <WorkspaceButton key={project.ref} workspace={project} active={project.ref === props.workspaceRef} onClick={() => props.onWorkspace(project)} />;
            }
            return (
              <div key={project.project_id} className="mb-1">
                <button type="button" onClick={() => setExpanded((current) => {
                  const next = new Set(current);
                  if (next.has(project.project_id)) next.delete(project.project_id); else next.add(project.project_id);
                  return next;
                })} className="flex w-full min-w-0 items-center gap-2 rounded-md px-2 py-2 text-left text-sm font-medium hover:bg-gray-100 dark:hover:bg-white/[0.06]">
                  {isExpanded ? <ChevronDown size={15} /> : <ChevronRight size={15} />}<Folder size={15} className="shrink-0 text-amber-500" /><span className="truncate">{project.project_name}</span>
                </button>
                {isExpanded && <div className="ml-5 border-l border-gray-200 pl-1 dark:border-white/[0.1]">{roots.map((workspace) => <WorkspaceButton key={workspace.ref} workspace={workspace} active={workspace.ref === props.workspaceRef} onClick={() => props.onWorkspace(workspace)} rootOnly />)}</div>}
              </div>
            );
          })}
          {!props.loading && props.workspaces.length === 0 && <p className="p-4 text-sm text-gray-400">{t('workspaceChat.noWorkspaces')}</p>}

          {props.workspaceRef && (
            <section className="mt-4 border-t border-gray-200 pt-3 dark:border-white/[0.08]">
              <div className="mb-2 flex items-center justify-between px-2">
                <div className="flex items-center gap-2 text-xs font-semibold uppercase text-gray-500"><MessageSquare size={13} />{t('workspaceChat.threads')}</div>
                <button type="button" onClick={props.onNew} disabled={props.creating} className="flex h-7 w-7 items-center justify-center rounded-md text-gray-500 hover:bg-gray-100 hover:text-gray-900 disabled:opacity-40 dark:hover:bg-white/[0.08] dark:hover:text-white" title={t('workspaceChat.newThread')} aria-label={t('workspaceChat.newThread')}>
                  {props.creating ? <Loader2 size={14} className="animate-spin" /> : <Plus size={15} />}
                </button>
              </div>
              {props.conversation?.kind === 'draft' && (
                <div className="mb-1 flex min-w-0 items-center gap-2 rounded-md bg-accent/12 px-2 py-2 text-accent">
                  <FilePlus2 size={14} className="shrink-0" /><span className="min-w-0 flex-1 truncate text-sm font-medium">{t('workspaceChat.newDraft')}</span>
                </div>
              )}
              {props.threads.map((thread) => (
                <div key={thread.id} className={cn('group mb-1 flex min-w-0 items-center rounded-md', props.conversation?.kind === 'thread' && props.conversation.id === thread.id ? 'bg-accent/12 text-accent' : 'text-gray-700 hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-white/[0.06]')}>
                  <button type="button" onClick={() => props.onThread(thread)} className="min-w-0 flex-1 px-2 py-2 text-left">
                    <span className="block truncate text-sm font-medium">{threadLabel(thread)}</span>
                    <span className="flex items-center gap-1.5 truncate text-[10px] text-gray-400"><span className="h-1.5 w-1.5 shrink-0 rounded-full bg-current" />{statusType(thread.status) || '-'}</span>
                  </button>
                  <button type="button" onClick={() => props.onCopyLink(thread)} className="mr-1 flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-gray-400 opacity-100 hover:bg-white/70 hover:text-gray-900 md:opacity-0 md:group-hover:opacity-100 dark:hover:bg-black/30 dark:hover:text-white" title={t('workspaceChat.copyLink')} aria-label={t('workspaceChat.copyLink')}><Copy size={13} /></button>
                </div>
              ))}
              {!props.loading && props.threads.length === 0 && props.conversation?.kind !== 'draft' && <p className="px-2 py-3 text-xs text-gray-400">{t('workspaceChat.noThreads')}</p>}
            </section>
          )}
        </nav>
      </aside>
      {props.open && <button type="button" className="absolute inset-0 z-20 bg-black/30 md:hidden" onClick={props.onClose} aria-label={t('common.close')} />}
    </>
  );
}
