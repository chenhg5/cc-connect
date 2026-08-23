import { Fragment, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import Markdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import {
  AlertCircle,
  Bot,
  CheckCircle2,
  FileCode2,
  Image,
  Search,
  TerminalSquare,
  User,
  Wrench,
} from 'lucide-react';
import type { NativeEventEnvelope, NativeInteraction, NativeItemRecord, NativeTurn } from '@/api/workspaceChat';
import type { OptimisticInput } from './workspaceChatState';
import { textFromNativeItem } from './workspaceChatState';

const asString = (value: unknown) => typeof value === 'string' ? value : '';
const asRecord = (value: unknown): Record<string, unknown> | null =>
  value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : null;

export function JsonDetails({ value, label }: { value: unknown; label: string }) {
  return (
    <details className="group min-w-0 text-xs text-gray-500 dark:text-gray-400">
      <summary className="cursor-pointer select-none hover:text-gray-800 dark:hover:text-gray-200">{label}</summary>
      <pre className="mt-2 max-h-80 max-w-full overflow-auto rounded-md bg-gray-100 p-3 text-[11px] leading-5 text-gray-700 dark:bg-black/40 dark:text-gray-300">
        {JSON.stringify(value, null, 2)}
      </pre>
    </details>
  );
}

function MarkdownText({ children }: { children: string }) {
  return (
    <div className="prose prose-sm max-w-none break-words dark:prose-invert prose-pre:max-w-full prose-pre:overflow-auto prose-pre:rounded-md prose-pre:bg-gray-950 prose-a:text-accent">
      <Markdown remarkPlugins={[remarkGfm]}>{children}</Markdown>
    </div>
  );
}

function itemCommand(item: Record<string, unknown>): string {
  if (typeof item.command === 'string') return item.command;
  const command = asRecord(item.command);
  if (Array.isArray(command?.actions)) {
    return command.actions.map((action) => asString(asRecord(action)?.command)).filter(Boolean).join('\n');
  }
  return '';
}

export function NativeItemView({ item, pending = false }: { item: Record<string, unknown>; pending?: boolean }) {
  const { t } = useTranslation();
  const type = asString(item.type) || 'unknown';
  const text = textFromNativeItem(item);

  if (type === 'userMessage') {
	const hasStructuredContent = Array.isArray(item.content)
		&& item.content.some((block) => {
			const value = asRecord(block);
			return value && (!asString(value.text) || Object.keys(value).some((key) => key !== 'type' && key !== 'text'));
		});
    return (
      <div className="flex min-w-0 justify-end gap-2">
        <div className="max-w-[82%] break-words rounded-md bg-accent px-4 py-3 text-sm text-white dark:text-black">
          <span className="whitespace-pre-wrap">{text}</span>
		  {hasStructuredContent && <div className="mt-2 text-left"><JsonDetails value={item.content} label={t('workspaceChat.rawItem')} /></div>}
          {pending && <span className="ml-2 text-[10px] opacity-70">{t('workspaceChat.sending')}</span>}
        </div>
        <div className="mt-1 flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-gray-200 dark:bg-gray-700"><User size={14} /></div>
      </div>
    );
  }
  if (type === 'agentMessage') {
    return (
      <div className="flex min-w-0 gap-2">
        <div className="mt-1 flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-accent/12 text-accent"><Bot size={14} /></div>
        <div className="min-w-0 flex-1 py-1 text-sm"><MarkdownText>{text}</MarkdownText></div>
      </div>
    );
  }
  if (type === 'reasoning') {
    return (
      <details open className="ml-0 min-w-0 border-l-2 border-amber-400/50 pl-3 text-sm text-gray-600 sm:ml-9 dark:text-gray-400">
        <summary className="cursor-pointer text-xs font-medium text-amber-700 dark:text-amber-300">{t('workspaceChat.reasoning')}</summary>
        <div className="mt-2 whitespace-pre-wrap break-words leading-6">{text}</div>
        <JsonDetails value={item} label={t('workspaceChat.rawItem')} />
      </details>
    );
  }
  if (type === 'plan') {
    return (
      <div className="ml-0 min-w-0 border-l-2 border-blue-400/50 pl-3 text-sm sm:ml-9">
        <div className="mb-1 text-xs font-medium text-blue-700 dark:text-blue-300">{t('workspaceChat.plan')}</div>
        {text ? <MarkdownText>{text}</MarkdownText> : <JsonDetails value={item} label={t('workspaceChat.planDetails')} />}
      </div>
    );
  }
  if (type === 'commandExecution') {
    const command = itemCommand(item);
    const output = asString(item.aggregatedOutput);
    return (
      <div className="ml-0 min-w-0 border-l-2 border-gray-400/50 pl-3 sm:ml-9">
        <div className="mb-2 flex flex-wrap items-center gap-2 text-xs font-medium text-gray-600 dark:text-gray-300">
          <TerminalSquare size={14} /> {asString(item.status) || t('workspaceChat.command')}
          {item.exitCode !== undefined && <span>{t('workspaceChat.exitCode', { code: String(item.exitCode) })}</span>}
        </div>
        {(command || output) && <pre className="max-w-full overflow-auto rounded-md bg-gray-950 p-3 text-xs leading-5 text-gray-100">{command ? `$ ${command}` : ''}{output ? `\n${output}` : ''}</pre>}
        <JsonDetails value={item} label={t('workspaceChat.commandDetails')} />
      </div>
    );
  }
  if (type === 'fileChange') {
    return (
      <div className="ml-0 min-w-0 border-l-2 border-emerald-400/50 pl-3 text-sm sm:ml-9">
        <div className="mb-2 flex items-center gap-2 text-xs font-medium text-emerald-700 dark:text-emerald-300"><FileCode2 size={14} /> {t('workspaceChat.fileChanges')}</div>
        <JsonDetails value={item.changes ?? item} label={t('workspaceChat.changes')} />
      </div>
    );
  }
  if (type === 'mcpToolCall' || type === 'dynamicToolCall' || type === 'collabToolCall') {
    return (
      <div className="ml-0 min-w-0 border-l-2 border-violet-400/50 pl-3 text-sm sm:ml-9">
        <div className="mb-2 flex items-center gap-2 text-xs font-medium text-violet-700 dark:text-violet-300"><Wrench size={14} /> {asString(item.server)} {asString(item.tool) || asString(item.name) || type}</div>
        <JsonDetails value={item} label={t('workspaceChat.toolDetails')} />
      </div>
    );
  }
  if (type === 'webSearch') {
    return (
      <div className="ml-0 flex min-w-0 items-start gap-2 border-l-2 border-cyan-400/50 pl-3 text-sm text-gray-700 sm:ml-9 dark:text-gray-300">
        <Search size={14} className="mt-1 shrink-0" /> <span className="break-words">{asString(item.query) || text}</span>
      </div>
    );
  }
  if (type === 'imageView') {
    return <div className="ml-0 flex items-center gap-2 border-l-2 border-fuchsia-400/50 pl-3 text-sm sm:ml-9"><Image size={14} /><JsonDetails value={item} label={t('workspaceChat.imageDetails')} /></div>;
  }
  if (type === 'error') {
    return <div className="ml-0 break-words rounded-md bg-red-50 p-3 text-sm text-red-700 sm:ml-9 dark:bg-red-950/30 dark:text-red-300">{text || asString(item.message) || JSON.stringify(item)}</div>;
  }
  return <div className="ml-0 min-w-0 border-l-2 border-gray-300 pl-3 sm:ml-9"><JsonDetails value={item} label={type} /></div>;
}

function TurnView({
  turn,
  items,
  interactions,
  renderInteraction,
}: {
  turn: NativeTurn;
  items: NativeItemRecord[];
  interactions: NativeInteraction[];
  renderInteraction?: (interaction: NativeInteraction) => ReactNode;
}) {
  const { t } = useTranslation();
  const completed = turn.status === 'completed';
  return (
    <section className="border-b border-gray-200 py-6 last:border-b-0 dark:border-white/[0.08]">
      <div className="mb-4 flex flex-wrap items-center gap-2 text-[11px] text-gray-400">
        {completed ? <CheckCircle2 size={13} className="text-emerald-500" /> : <AlertCircle size={13} className="text-amber-500" />}
        <span className="font-medium uppercase">{turn.status || t('workspaceChat.turn')}</span>
        {turn.duration_ms !== undefined && <span>{(turn.duration_ms / 1000).toFixed(1)}s</span>}
      </div>
      <div className="space-y-4">
        {items.map((record, index) => {
          const itemID = asString(record.item.id);
          return (
            <Fragment key={itemID || `${turn.id}-${index}`}>
              <NativeItemView item={record.item} />
              {renderInteraction && interactions.filter((interaction) => interaction.item_id === itemID).map((interaction) => (
                <div key={interaction.id} className="ml-0 sm:ml-9">{renderInteraction(interaction)}</div>
              ))}
            </Fragment>
          );
        })}
        {turn.error !== undefined && <JsonDetails value={turn.error} label={t('workspaceChat.turnError')} />}
      </div>
    </section>
  );
}

function isProjectedMethod(method: string): boolean {
  return method === 'item/started'
    || method === 'item/completed'
    || method === 'turn/started'
    || method === 'turn/completed'
    || method === 'thread/status/changed'
    || method === 'thread/tokenUsage/updated'
    || method === 'thread/settings/updated'
    || method === 'serverRequest/resolved'
    || method.includes('/delta')
    || method === 'turn/plan/updated'
    || method.startsWith('thread/realtime/');
}

interface WorkspaceHistoryProps {
  turns: NativeTurn[];
  itemsByTurn: Record<string, NativeItemRecord[]>;
  liveItems: Record<string, Record<string, unknown>>;
  optimisticInputs: OptimisticInput[];
  nativeEvents: NativeEventEnvelope[];
  interactions?: NativeInteraction[];
  renderInteraction?: (interaction: NativeInteraction) => ReactNode;
}

export function WorkspaceHistory({ turns, itemsByTurn, liveItems, optimisticInputs, nativeEvents, interactions = [], renderInteraction }: WorkspaceHistoryProps) {
  const { t } = useTranslation();
  const unknownEvents = nativeEvents.filter((event) => !isProjectedMethod(event.method));
  const itemIDs = new Set([
    ...turns.flatMap((turn) => (itemsByTurn[turn.id] || []).map((record) => asString(record.item.id))),
    ...Object.keys(liveItems),
  ].filter(Boolean));
  const unboundInteractions = interactions.filter((interaction) => !interaction.item_id || !itemIDs.has(interaction.item_id));
  const empty = turns.length === 0 && Object.keys(liveItems).length === 0 && optimisticInputs.length === 0 && unknownEvents.length === 0 && interactions.length === 0;
  if (empty) {
    return <div className="flex min-h-64 flex-col items-center justify-center text-gray-400"><Bot size={30} className="mb-3" /><p className="text-sm">{t('workspaceChat.emptyThread')}</p></div>;
  }
  return (
    <>
      {turns.map((turn) => <TurnView key={turn.id} turn={turn} items={itemsByTurn[turn.id] || []} interactions={interactions} renderInteraction={renderInteraction} />)}
      {(optimisticInputs.length > 0 || Object.keys(liveItems).length > 0 || unknownEvents.length > 0 || unboundInteractions.length > 0) && (
        <section className="space-y-4 py-6">
          {optimisticInputs.map((input) => <NativeItemView key={input.requestId} item={{ type: 'userMessage', text: input.text }} pending />)}
          {Object.entries(liveItems).map(([id, item]) => (
            <Fragment key={id}>
              <NativeItemView item={item} />
              {renderInteraction && interactions.filter((interaction) => interaction.item_id === id).map((interaction) => (
                <div key={interaction.id} className="ml-0 sm:ml-9">{renderInteraction(interaction)}</div>
              ))}
            </Fragment>
          ))}
          {unknownEvents.map((event, index) => <div key={`${event.method}-${event.occurred_at}-${index}`} className="ml-0 border-l-2 border-gray-300 pl-3 sm:ml-9"><JsonDetails value={event.payload} label={event.method} /></div>)}
          {renderInteraction && unboundInteractions.map((interaction) => <div key={interaction.id}>{renderInteraction(interaction)}</div>)}
        </section>
      )}
    </>
  );
}
