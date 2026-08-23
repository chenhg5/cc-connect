import type {
  NativeActiveTurn,
  NativeConversationSnapshot,
  NativeEventEnvelope,
  NativeInteraction,
  NativeItemRecord,
  NativeThreadSettings,
  NativeTurn,
  WorkspaceChatServerEventType,
  WorkspaceChatServerEvent,
} from '@/api/workspaceChat';
import { classifyWorkspaceChatServerEvent } from '@/api/workspaceChat';

export interface OptimisticInput {
  requestId: string;
  text: string;
  kind: 'turn_start' | 'turn_steer' | 'realtime';
}

export interface WorkspaceChatStreamState {
  epoch?: string;
  sequence?: number;
  eventType?: WorkspaceChatServerEventType;
  snapshot: NativeConversationSnapshot | null;
  turns: NativeTurn[];
  itemsByTurn: Record<string, NativeItemRecord[]>;
  liveItems: Record<string, Record<string, unknown>>;
  nativeEvents: NativeEventEnvelope[];
  optimisticInputs: OptimisticInput[];
  needsResync: boolean;
  needsHistoryRefresh: boolean;
  materializedThreadId?: string;
  error?: string;
}

export type WorkspaceChatStreamAction =
  | { type: 'reset' }
  | { type: 'history_loaded'; snapshot: NativeConversationSnapshot; turns: NativeTurn[]; itemsByTurn: Record<string, NativeItemRecord[]>; startedEpoch?: string; startedSequence?: number }
  | { type: 'settings_updated'; settings: NativeThreadSettings }
  | { type: 'optimistic_input'; input: OptimisticInput }
  | { type: 'clear_error' }
  | { type: 'server_event'; event: WorkspaceChatServerEvent }
  | { type: 'history_refreshed' };

export const initialWorkspaceChatStreamState: WorkspaceChatStreamState = {
  snapshot: null,
  turns: [],
  itemsByTurn: {},
  liveItems: {},
  nativeEvents: [],
  optimisticInputs: [],
  needsResync: false,
  needsHistoryRefresh: false,
};

const asRecord = (value: unknown): Record<string, unknown> | null =>
  value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : null;

const asString = (value: unknown): string => typeof value === 'string' ? value : '';

function asNativeThreadSettings(value: unknown): NativeThreadSettings | undefined {
  const settings = asRecord(value);
  return settings && typeof settings.revision === 'string' && typeof settings.model === 'string'
    ? value as NativeThreadSettings
    : undefined;
}

export function nativeEnvelopeFromEvent(event: WorkspaceChatServerEvent): NativeEventEnvelope | null {
  if (event.type !== 'native_event') return null;
  const payload = asRecord(event.payload);
  if (!payload || !('payload' in payload) || typeof payload.method !== 'string' || typeof payload.occurred_at !== 'string' || !payload.occurred_at) return null;
  return {
    method: payload.method,
    thread_id: asString(payload.thread_id),
    turn_id: asString(payload.turn_id),
    item_id: asString(payload.item_id),
    interaction_id: asString(payload.interaction_id),
    allowed_decisions: Array.isArray(payload.allowed_decisions)
      ? [...payload.allowed_decisions]
      : undefined,
    settings: asNativeThreadSettings(payload.settings),
    payload: payload.payload,
    occurred_at: asString(payload.occurred_at),
  };
}

function inferItemType(method: string): string {
  if (method.includes('agentMessage')) return 'agentMessage';
  if (method.includes('reasoning')) return 'reasoning';
  if (method.includes('commandExecution')) return 'commandExecution';
  if (method.includes('fileChange')) return 'fileChange';
  if (method.includes('mcpToolCall')) return 'mcpToolCall';
  if (method.includes('plan')) return 'plan';
  return 'unknown';
}

function appendDelta(item: Record<string, unknown>, method: string, payload: Record<string, unknown>): Record<string, unknown> {
  const delta = asString(payload.delta) || asString(payload.text) || asString(payload.output);
  if (!delta) return { ...item, ...payload };
  if (method.includes('commandExecution/outputDelta')) {
    return { ...item, aggregatedOutput: `${asString(item.aggregatedOutput)}${delta}` };
  }
  if (method.includes('reasoning/summary')) {
    return { ...item, summary: `${asString(item.summary)}${delta}` };
  }
  return { ...item, text: `${asString(item.text)}${delta}` };
}

function applyNativeEvent(state: WorkspaceChatStreamState, envelope: NativeEventEnvelope): WorkspaceChatStreamState {
  const payload = asRecord(envelope.payload) || {};
  let snapshot = state.snapshot;
  let liveItems = state.liveItems;
  let optimisticInputs = state.optimisticInputs;
  let needsHistoryRefresh = state.needsHistoryRefresh;

  if (envelope.method === 'thread/status/changed' && snapshot) {
    snapshot = { ...snapshot, status: payload.status };
  } else if (envelope.method === 'thread/tokenUsage/updated' && snapshot) {
    snapshot = { ...snapshot, usage: payload };
  } else if (envelope.method === 'thread/settings/updated' && snapshot && envelope.settings) {
    snapshot = { ...snapshot, settings: envelope.settings };
  } else if (envelope.method === 'turn/started' && snapshot) {
    const turnID = envelope.turn_id || '';
    const active: NativeActiveTurn = {
      id: turnID,
      request_id: asString(payload.requestId) || undefined,
      started_at: envelope.occurred_at,
    };
    snapshot = { ...snapshot, active_turn: active };
    if (active.request_id) optimisticInputs = optimisticInputs.filter((input) => input.requestId !== active.request_id);
  } else if (envelope.method === 'turn/completed' && snapshot) {
    snapshot = { ...snapshot, active_turn: null };
    optimisticInputs = [];
    needsHistoryRefresh = true;
  }

  if (envelope.method === 'item/started' || envelope.method === 'item/completed') {
    const item = asRecord(payload.item);
    const itemID = envelope.item_id || '';
    if (item && itemID) {
      liveItems = { ...liveItems, [itemID]: item };
      if (asString(item.type) === 'userMessage') {
        const text = textFromNativeItem(item);
        optimisticInputs = optimisticInputs.filter((input) => input.text !== text);
      }
    }
  } else if (envelope.method.includes('/delta') || envelope.method === 'turn/plan/updated') {
    const itemID = envelope.item_id
      || (envelope.method === 'turn/plan/updated' ? `plan:${envelope.turn_id || 'active'}` : '');
    if (itemID) {
      const existing = liveItems[itemID] || { id: itemID, type: inferItemType(envelope.method) };
      liveItems = { ...liveItems, [itemID]: appendDelta(existing, envelope.method, payload) };
    }
  } else if (envelope.method === 'thread/realtime/transcript/done') {
    const role = asString(payload.role);
    const text = asString(payload.text);
    const itemID = `${envelope.method}:${envelope.occurred_at}:${role}`;
    if (text) {
      liveItems = {
        ...liveItems,
        [itemID]: { id: itemID, type: role === 'assistant' ? 'agentMessage' : 'userMessage', text },
      };
    }
  }

  const interaction = interactionFromEnvelope(envelope);
  if (interaction && snapshot) {
    snapshot = {
      ...snapshot,
      pending_interactions: [
        ...snapshot.pending_interactions.filter((candidate) => candidate.id !== interaction.id),
        interaction,
      ],
    };
  }
  if (envelope.method === 'serverRequest/resolved' && snapshot) {
    const interactionID = envelope.interaction_id;
    if (interactionID) {
      snapshot = {
        ...snapshot,
        pending_interactions: snapshot.pending_interactions.filter((candidate) => candidate.id !== interactionID),
      };
    }
  }

  return {
    ...state,
    snapshot,
    liveItems,
    optimisticInputs,
    needsHistoryRefresh,
    nativeEvents: [...state.nativeEvents, envelope],
  };
}

export function interactionFromEnvelope(envelope: NativeEventEnvelope): NativeInteraction | null {
  if (![
    'item/commandExecution/requestApproval',
    'item/fileChange/requestApproval',
    'item/permissions/requestApproval',
    'item/tool/requestUserInput',
    'mcpServer/elicitation/request',
  ].includes(envelope.method) || !envelope.interaction_id) return null;
  return {
    id: envelope.interaction_id,
    kind: envelope.method,
    thread_id: envelope.thread_id || '',
    turn_id: envelope.turn_id || undefined,
    item_id: envelope.item_id || undefined,
    allowed_decisions: envelope.allowed_decisions,
    payload: envelope.payload,
    occurred_at: envelope.occurred_at,
  };
}

export function workspaceChatStreamReducer(
  state: WorkspaceChatStreamState,
  action: WorkspaceChatStreamAction,
): WorkspaceChatStreamState {
  if (action.type === 'reset') return initialWorkspaceChatStreamState;
  if (action.type === 'history_loaded') {
    const persistedItemIDs = new Set(Object.values(action.itemsByTurn).flat().map((record) => {
      const item = asRecord(record.item);
      return asString(item?.id);
    }).filter(Boolean));
    const liveAdvanced = !!action.startedEpoch && state.epoch === action.startedEpoch
      && (state.sequence || 0) > (action.startedSequence || 0);
    const snapshot = liveAdvanced && state.snapshot
      ? {
          ...action.snapshot,
          settings: state.snapshot.settings,
          status: state.snapshot.status,
          usage: state.snapshot.usage,
          active_turn: state.snapshot.active_turn,
          pending_interactions: state.snapshot.pending_interactions,
        }
      : action.snapshot;
    const liveItems = (liveAdvanced || snapshot.active_turn)
      ? Object.fromEntries(Object.entries(state.liveItems).filter(([id]) => !persistedItemIDs.has(id)))
      : {};
    return {
      ...state,
      snapshot,
      turns: action.turns,
      itemsByTurn: action.itemsByTurn,
      liveItems,
      optimisticInputs: (liveAdvanced || snapshot.active_turn) ? state.optimisticInputs : [],
      needsResync: false,
      needsHistoryRefresh: false,
      error: undefined,
    };
  }
  if (action.type === 'history_refreshed') return { ...state, needsHistoryRefresh: false };
  if (action.type === 'settings_updated') {
    return state.snapshot ? { ...state, snapshot: { ...state.snapshot, settings: action.settings } } : state;
  }
  if (action.type === 'clear_error') return { ...state, error: undefined };
  if (action.type === 'optimistic_input') {
    return { ...state, optimisticInputs: [...state.optimisticInputs, action.input] };
  }

  const { event } = action;
  if (event.type === 'protocol_error') {
    return {
      ...state,
      eventType: event.type,
      optimisticInputs: event.request_id
        ? state.optimisticInputs.filter((input) => input.requestId !== event.request_id)
        : state.optimisticInputs,
      error: event.error || 'workspace_chat_unknown_error',
    };
  }
  const disposition = classifyWorkspaceChatServerEvent(state, event);
  if (disposition === 'ignore') return state;
  if (disposition === 'resync') return { ...state, needsResync: true };
  let next: WorkspaceChatStreamState = {
    ...state,
    epoch: event.epoch,
    sequence: event.sequence,
    eventType: event.type,
  };
  if (event.type === 'snapshot') {
    const payload = asRecord(event.payload);
    if (payload && asRecord(payload.thread)) {
      next = { ...next, snapshot: event.payload as NativeConversationSnapshot, needsResync: false, error: undefined };
    }
  } else if (event.type === 'resync_required') {
    next = { ...next, needsResync: true };
  } else if (event.type === 'thread_materialized') {
    next = {
      ...next,
      materializedThreadId: event.thread_id,
    };
  } else if (event.type === 'native_event') {
    const envelope = nativeEnvelopeFromEvent(event);
    if (envelope) next = applyNativeEvent(next, envelope);
  } else if (event.type === 'error') {
    next = {
      ...next,
      optimisticInputs: event.request_id
        ? next.optimisticInputs.filter((input) => input.requestId !== event.request_id)
        : next.optimisticInputs,
      error: event.error || 'workspace_chat_unknown_error',
    };
  }
  return next;
}

export function textFromNativeItem(item: Record<string, unknown>): string {
  const direct = asString(item.text) || asString(item.summary) || asString(item.reasoning);
  if (direct) return direct;
  if (!Array.isArray(item.content)) return '';
  return item.content
    .map((block) => {
      const value = asRecord(block);
      return value ? asString(value.text) : '';
    })
    .filter(Boolean)
    .join('\n');
}

export function statusType(status: unknown): string {
  const value = asRecord(status);
  return value ? asString(value.type) : asString(status);
}

export function totalTokenCount(usage: unknown): number | null {
  const root = asRecord(usage);
  const tokenUsage = asRecord(root?.tokenUsage);
  const total = asRecord(tokenUsage?.total);
  const raw = total?.totalTokens;
  return typeof raw === 'number' ? raw : null;
}
