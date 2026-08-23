import { api } from './client';

export type ConversationKind = 'draft' | 'thread';

export interface ConversationRef {
  kind: ConversationKind;
  id: string;
}

export interface Workspace {
  ref: string;
  project_id: string;
  project_name: string;
  root_index: number;
  root_name: string;
  root_path: string;
  available: boolean;
  error?: string;
  order: number;
}

export interface NativeThread {
  id: string;
  cwd: string;
  name?: string;
  preview?: string;
  status?: unknown;
  created_at: string;
  updated_at: string;
}

export interface NativeTurn {
  id: string;
  status: string;
  started_at?: string;
  completed_at?: string;
  duration_ms?: number;
  error?: unknown;
  items?: NativeItemRecord[];
}

export interface NativeItemRecord {
  turn_id: string;
  item: Record<string, unknown>;
}

export interface NativePage<T> {
  data: T[];
  next_cursor?: string;
  backwards_cursor?: string;
}

export interface CapabilityStatus {
  supported: boolean;
  reason?: string;
}

export interface ReasoningEffortOption {
  effort: string;
  description?: string;
}

export interface ServiceTierOption {
  id: string;
  name: string;
  description?: string;
}

export interface NativeModelOption {
  id: string;
  model: string;
  display_name: string;
  description?: string;
  hidden: boolean;
  default: boolean;
  default_reasoning_effort: string;
  reasoning_efforts: ReasoningEffortOption[];
  input_modalities: string[];
  supports_personality: boolean;
  service_tiers: ServiceTierOption[];
  default_service_tier?: string;
}

export interface NativeCollaborationModeOption {
  name: string;
  mode?: string;
  model?: string;
  reasoning_effort?: string;
}

export interface NativePermissionProfile {
  id: string;
  description?: string;
  allowed: boolean;
}

export interface NativeRuntimeCatalog {
  capabilities: Record<string, CapabilityStatus>;
  models: NativeModelOption[];
  modes: NativeCollaborationModeOption[];
  permissions: NativePermissionProfile[];
  personalities: string[];
  summaries: string[];
  voices: {
    v1: string[];
    v2: string[];
    default_v1?: string;
    default_v2?: string;
  };
}

function runtimeCatalogRecord(value: unknown, path: string): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error(`workspace_chat_invalid_runtime_catalog: ${path} must be an object`);
  }
  return value as Record<string, unknown>;
}

function runtimeCatalogRecordOrEmpty(value: unknown, path: string): Record<string, unknown> {
  return value === null || value === undefined ? {} : runtimeCatalogRecord(value, path);
}

function runtimeCatalogArray(value: unknown, path: string): unknown[] {
  if (value === null || value === undefined) return [];
  if (!Array.isArray(value)) {
    throw new Error(`workspace_chat_invalid_runtime_catalog: ${path} must be an array`);
  }
  return value;
}

function runtimeCatalogRecordArray<T>(value: unknown, path: string): T[] {
  return runtimeCatalogArray(value, path).map((item, index) =>
    runtimeCatalogRecord(item, `${path}[${index}]`) as unknown as T);
}

function runtimeCatalogStringArray(value: unknown, path: string): string[] {
  return runtimeCatalogArray(value, path).map((item, index) => {
    if (typeof item !== 'string') {
      throw new Error(`workspace_chat_invalid_runtime_catalog: ${path}[${index}] must be a string`);
    }
    return item;
  });
}

function runtimeCatalogOptionalString(value: unknown, path: string): string | undefined {
  if (value === null || value === undefined) return undefined;
  if (typeof value !== 'string') {
    throw new Error(`workspace_chat_invalid_runtime_catalog: ${path} must be a string`);
  }
  return value;
}

function normalizeRuntimeCapabilities(value: unknown): Record<string, CapabilityStatus> {
  const source = runtimeCatalogRecordOrEmpty(value, 'capabilities');
  return Object.fromEntries(Object.entries(source).map(([name, rawStatus]) => {
    const status = runtimeCatalogRecord(rawStatus, `capabilities.${name}`);
    if (typeof status.supported !== 'boolean') {
      throw new Error(`workspace_chat_invalid_runtime_catalog: capabilities.${name}.supported must be a boolean`);
    }
    const reason = runtimeCatalogOptionalString(status.reason, `capabilities.${name}.reason`);
    return [name, { supported: status.supported, ...(reason === undefined ? {} : { reason }) }];
  }));
}

export function normalizeNativeRuntimeCatalog(value: unknown): NativeRuntimeCatalog {
  const catalog = runtimeCatalogRecord(value, 'catalog');
  const voices = runtimeCatalogRecordOrEmpty(catalog.voices, 'voices');
  const defaultV1 = runtimeCatalogOptionalString(voices.default_v1, 'voices.default_v1');
  const defaultV2 = runtimeCatalogOptionalString(voices.default_v2, 'voices.default_v2');
  const models = runtimeCatalogRecordArray<NativeModelOption>(catalog.models, 'models').map((model, index) => ({
    ...model,
    reasoning_efforts: runtimeCatalogRecordArray<ReasoningEffortOption>(model.reasoning_efforts, `models[${index}].reasoning_efforts`),
    input_modalities: runtimeCatalogStringArray(model.input_modalities, `models[${index}].input_modalities`),
    service_tiers: runtimeCatalogRecordArray<ServiceTierOption>(model.service_tiers, `models[${index}].service_tiers`),
  }));
  return {
    capabilities: normalizeRuntimeCapabilities(catalog.capabilities),
    models,
    modes: runtimeCatalogRecordArray<NativeCollaborationModeOption>(catalog.modes, 'modes'),
    permissions: runtimeCatalogRecordArray<NativePermissionProfile>(catalog.permissions, 'permissions'),
    personalities: runtimeCatalogStringArray(catalog.personalities, 'personalities'),
    summaries: runtimeCatalogStringArray(catalog.summaries, 'summaries'),
    voices: {
      v1: runtimeCatalogStringArray(voices.v1, 'voices.v1'),
      v2: runtimeCatalogStringArray(voices.v2, 'voices.v2'),
      ...(defaultV1 === undefined ? {} : { default_v1: defaultV1 }),
      ...(defaultV2 === undefined ? {} : { default_v2: defaultV2 }),
    },
  };
}

export interface NativeThreadSettings {
  revision: string;
  model: string;
  model_provider?: string;
  effort?: string;
  service_tier?: string;
  personality?: string;
  summary?: string;
  permission_profile?: string;
  approval_policy?: unknown;
  approvals_reviewer?: string;
  sandbox_policy?: unknown;
  collaboration_mode?: {
    mode: string;
    settings: {
      model: string;
      reasoning_effort: string | null;
      developer_instructions: string | null;
    };
  } | null;
}

export interface NativeThreadSettingsPatch {
  model?: string;
  effort?: string;
  plan_effort?: string;
  service_tier?: string;
  personality?: string;
  summary?: string;
  permission_profile?: string;
  mode?: string;
}

export interface WorkspaceChatDraft {
  id: string;
  owner_client_id: string;
  workspace_ref: string;
  state: string;
  thread_id?: string;
  settings_patch: NativeThreadSettingsPatch;
  created_at: string;
  updated_at: string;
}

export interface WorkspaceChatSelection {
  client_id: string;
  workspace_ref: string;
  conversation: ConversationRef;
  updated_at: string;
}

export interface NativeActiveTurn {
  id: string;
  request_id?: string;
  started_at: string;
}

export interface NativeInteraction {
  id: string;
  kind: string;
  thread_id: string;
  turn_id?: string;
  item_id?: string;
  allowed_decisions?: unknown[];
  payload: unknown;
  occurred_at: string;
}

export interface NativeConversationSnapshot {
  thread: NativeThread;
  settings: NativeThreadSettings;
  status?: unknown;
  usage?: unknown;
  active_turn?: NativeActiveTurn | null;
  pending_interactions: NativeInteraction[];
  capabilities: Record<string, CapabilityStatus>;
  deep_link: string;
}

export interface NativeUserInput {
  type: 'text';
  text: string;
}

export interface NativeEventEnvelope {
  method: string;
  thread_id?: string;
  turn_id?: string;
  item_id?: string;
  interaction_id?: string;
  allowed_decisions?: unknown[];
  settings?: NativeThreadSettings;
  payload: unknown;
  occurred_at: string;
}

export type WorkspaceChatServerEventType =
  | 'subscribed'
  | 'snapshot'
  | 'native_event'
  | 'thread_materialized'
  | 'resync_required'
  | 'protocol_error'
  | 'error';

export interface WorkspaceChatServerEvent {
  type: WorkspaceChatServerEventType;
  epoch: string;
  sequence: number;
  workspace_ref: string;
  conversation: ConversationRef;
  thread_id?: string;
  turn_id?: string;
  request_id?: string;
  payload?: unknown;
  error?: string;
  occurred_at: string;
}

export interface WorkspaceChatEventCursor {
  epoch?: string;
  sequence?: number;
  eventType?: WorkspaceChatServerEventType;
}

export type WorkspaceChatServerEventDisposition = 'apply' | 'ignore' | 'resync';

export function classifyWorkspaceChatServerEvent(
  cursor: WorkspaceChatEventCursor,
  event: WorkspaceChatServerEvent,
): WorkspaceChatServerEventDisposition {
  if (event.type === 'protocol_error') return 'apply';
  if (cursor.epoch === undefined || cursor.sequence === undefined) {
    return event.type === 'subscribed' ? 'apply' : 'resync';
  }
  if (event.epoch !== cursor.epoch) return event.type === 'subscribed' ? 'apply' : 'ignore';
  if (event.type === 'subscribed') {
    return event.sequence === cursor.sequence ? 'apply' : 'ignore';
  }
  if (event.sequence < cursor.sequence) return 'ignore';
  if (event.sequence === cursor.sequence) {
    if (event.type === 'resync_required') {
      return cursor.eventType === 'subscribed' ? 'apply' : 'ignore';
    }
    return event.type === 'snapshot'
      && (cursor.eventType === 'subscribed' || cursor.eventType === 'resync_required')
      ? 'apply'
      : 'ignore';
  }
  if (event.type === 'resync_required') return 'apply';
  return event.sequence === cursor.sequence + 1 ? 'apply' : 'resync';
}

interface WorkspaceChatRequestBase {
  request_id: string;
  workspace_ref: string;
  conversation: ConversationRef;
}

export type WorkspaceChatClientRequest =
  | (WorkspaceChatRequestBase & { type: 'subscribe'; after_epoch?: string; after_sequence?: number })
  | (WorkspaceChatRequestBase & {
      type: 'turn_start';
      input: NativeUserInput[];
      payload?: { settings: NativeThreadSettingsPatch };
    })
  | (WorkspaceChatRequestBase & { type: 'turn_steer'; input: NativeUserInput[]; expected_turn_id: string })
  | (WorkspaceChatRequestBase & { type: 'turn_interrupt'; expected_turn_id: string })
  | (WorkspaceChatRequestBase & { type: 'interaction_response'; interaction_id: string; response: unknown })
  | (WorkspaceChatRequestBase & { type: 'realtime_start'; sdp: string; voice?: string; version?: string })
  | (WorkspaceChatRequestBase & { type: 'realtime_append_text'; text: string })
  | (WorkspaceChatRequestBase & { type: 'realtime_stop' });

interface PageOptions {
  cursor?: string;
  limit?: number;
  sortDirection?: 'asc' | 'desc';
}

const pageParams = ({ cursor, limit, sortDirection }: PageOptions = {}): Record<string, string> => {
  const params: Record<string, string> = {};
  if (cursor) params.cursor = cursor;
  if (limit) params.limit = String(limit);
  if (sortDirection) params.sort_direction = sortDirection;
  return params;
};

const workspacePath = (workspaceRef: string) => `/chat/workspaces/${encodeURIComponent(workspaceRef)}`;

export const listWorkspaces = () => api.get<{ workspaces: Workspace[] }>('/chat/workspaces');

export const listWorkspaceThreads = (workspaceRef: string, options?: PageOptions) =>
  api.get<NativePage<NativeThread>>(`${workspacePath(workspaceRef)}/threads`, pageParams(options));

export const createWorkspaceDraft = (workspaceRef: string) =>
  api.post<WorkspaceChatDraft>(`${workspacePath(workspaceRef)}/drafts`, {});

export const readWorkspaceDraft = (workspaceRef: string, draftRef: string) =>
  api.get<WorkspaceChatDraft>(`${workspacePath(workspaceRef)}/drafts/${encodeURIComponent(draftRef)}`);

export const updateWorkspaceDraftSettings = (
  workspaceRef: string,
  draftRef: string,
  patch: NativeThreadSettingsPatch,
) => api.patch<WorkspaceChatDraft>(
  `${workspacePath(workspaceRef)}/drafts/${encodeURIComponent(draftRef)}/settings`,
  patch,
);

export const readWorkspaceThread = (workspaceRef: string, threadId: string) =>
  api.get<NativeConversationSnapshot>(`${workspacePath(workspaceRef)}/threads/${encodeURIComponent(threadId)}`);

export const listWorkspaceTurns = (workspaceRef: string, threadId: string, options?: PageOptions) =>
  api.get<NativePage<NativeTurn>>(
    `${workspacePath(workspaceRef)}/threads/${encodeURIComponent(threadId)}/turns`,
    pageParams(options),
  );

export const listWorkspaceItems = (
  workspaceRef: string,
  threadId: string,
  turnId?: string,
  options?: PageOptions,
) => api.get<NativePage<NativeItemRecord>>(
  `${workspacePath(workspaceRef)}/threads/${encodeURIComponent(threadId)}/items`,
  { ...pageParams(options), ...(turnId ? { turn_id: turnId } : {}) },
);

export const getWorkspaceRuntimeCatalog = async (workspaceRef: string) =>
  normalizeNativeRuntimeCatalog(await api.get<unknown>(`${workspacePath(workspaceRef)}/runtime-catalog`));

export const updateWorkspaceThreadSettings = (
  workspaceRef: string,
  threadId: string,
  patch: NativeThreadSettingsPatch,
) => api.patch<NativeThreadSettings>(
  `${workspacePath(workspaceRef)}/threads/${encodeURIComponent(threadId)}/settings`,
  patch,
);

export const getWorkspaceChatSelection = () => api.get<WorkspaceChatSelection | null>('/chat/selection');

export const putWorkspaceChatSelection = (workspaceRef: string, conversation: ConversationRef) =>
  api.put<WorkspaceChatSelection>('/chat/selection', {
    workspace_ref: workspaceRef,
    conversation,
  });

export function workspaceChatWebSocketURL(): string {
  const scheme = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  const url = new URL(`${scheme}//${window.location.host}/api/v1/chat/ws`);
  const token = api.getToken();
  if (token) url.searchParams.set('token', token);
  return url.toString();
}

export function conversationPath(workspaceRef: string, conversation: ConversationRef): string {
  const workspace = encodeURIComponent(workspaceRef);
  const id = encodeURIComponent(conversation.id);
  return conversation.kind === 'draft' ? `/chat/${workspace}/draft/${id}` : `/chat/${workspace}/${id}`;
}

export async function collectAllPages<T>(load: (cursor?: string) => Promise<NativePage<T>>): Promise<T[]> {
  const values: T[] = [];
  const seen = new Set<string>();
  let cursor: string | undefined;
  do {
    const page = await load(cursor);
    values.push(...(page.data || []));
    cursor = page.next_cursor || undefined;
    if (cursor) {
      if (seen.has(cursor)) throw new Error('workspace_chat_cursor_cycle');
      seen.add(cursor);
    }
  } while (cursor);
  return values;
}

export function isWorkspaceChatServerEvent(value: unknown): value is WorkspaceChatServerEvent {
  if (!value || typeof value !== 'object') return false;
  const event = value as Partial<WorkspaceChatServerEvent>;
  const validBase = typeof event.type === 'string'
    && ['subscribed', 'snapshot', 'native_event', 'thread_materialized', 'resync_required', 'protocol_error', 'error'].includes(event.type)
    && typeof event.epoch === 'string'
    && typeof event.sequence === 'number'
    && typeof event.workspace_ref === 'string'
    && typeof event.occurred_at === 'string'
    && !!event.conversation
    && (event.conversation.kind === 'draft' || event.conversation.kind === 'thread')
    && typeof event.conversation.id === 'string';
  if (!validBase) return false;
  if (event.type !== 'native_event') return true;
  if (!event.payload || typeof event.payload !== 'object' || Array.isArray(event.payload)) return false;
  const envelope = event.payload as Record<string, unknown>;
  if (typeof envelope.method !== 'string' || typeof envelope.occurred_at !== 'string' || !('payload' in envelope)) return false;
  if (envelope.settings !== undefined) {
    if (!envelope.settings || typeof envelope.settings !== 'object' || Array.isArray(envelope.settings)) return false;
    const settings = envelope.settings as Record<string, unknown>;
    if (typeof settings.revision !== 'string' || typeof settings.model !== 'string') return false;
  }
  return true;
}
