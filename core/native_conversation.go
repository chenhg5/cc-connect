package core

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrWorkspaceChatNotConfigured             = errors.New("workspace chat is not configured")
	ErrWorkspaceChatClosed                    = errors.New("workspace chat is closed")
	ErrWorkspaceNotFound                      = errors.New("workspace not found")
	ErrNativeThreadNotFound                   = errors.New("native thread not found")
	ErrWorkspaceTurnRunning                   = errors.New("workspace turn is already running")
	ErrWorkspaceTurnNotRunning                = errors.New("workspace turn is not running")
	ErrWorkspaceStaleTurn                     = errors.New("workspace turn id is stale")
	ErrNativeCapabilityUnavailable            = errors.New("native capability is unavailable")
	ErrNativeConnectionStale                  = errors.New("native connection generation is stale")
	ErrNativeAcceptanceUnknown                = errors.New("native mutation acceptance is unknown")
	ErrWorkspaceDraftMaterialized             = errors.New("workspace draft is already materialized")
	ErrWorkspaceDraftNotFound                 = errors.New("workspace draft not found")
	ErrWorkspaceDraftMaterializationUncertain = errors.New("workspace draft materialization is uncertain")
	ErrWorkspaceInteractionStale              = errors.New("workspace interaction is stale")
)

// NativeAcceptanceUnknownError 表示变更请求可能已经被原生后端接受，调用方不得自动重试。
type NativeAcceptanceUnknownError struct {
	Operation string
	Cause     error
}

func (e *NativeAcceptanceUnknownError) Error() string {
	if e == nil {
		return ErrNativeAcceptanceUnknown.Error()
	}
	if e.Cause == nil {
		if e.Operation == "" {
			return ErrNativeAcceptanceUnknown.Error()
		}
		return e.Operation + ": " + ErrNativeAcceptanceUnknown.Error()
	}
	cause := e.Cause.Error()
	if e.Operation == "" {
		return ErrNativeAcceptanceUnknown.Error() + ": " + cause
	}
	return e.Operation + ": " + ErrNativeAcceptanceUnknown.Error() + ": " + cause
}

func (e *NativeAcceptanceUnknownError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *NativeAcceptanceUnknownError) Is(target error) bool {
	return target == ErrNativeAcceptanceUnknown
}

func IsNativeAcceptanceUnknown(err error) bool {
	return errors.Is(err, ErrNativeAcceptanceUnknown)
}

type ConversationScope string

const (
	ConversationScopeDirect ConversationScope = "direct"
	ConversationScopeGroup  ConversationScope = "group"
)

// Workspace 是目录源签发的只读根目录。Ref 是客户端唯一可以提交的目录标识。
type Workspace struct {
	Ref         string `json:"ref"`
	DeviceID    string `json:"device_id"`
	DeviceName  string `json:"device_name"`
	ProjectID   string `json:"project_id"`
	ProjectName string `json:"project_name"`
	RootIndex   int    `json:"root_index"`
	RootName    string `json:"root_name"`
	RootPath    string `json:"-"`
	Available   bool   `json:"available"`
	Online      bool   `json:"online"`
	Error       string `json:"error,omitempty"`
	Order       int    `json:"order"`
}

type NativeThread struct {
	ID        string          `json:"id"`
	Cwd       string          `json:"-"`
	Name      string          `json:"name,omitempty"`
	Preview   string          `json:"preview,omitempty"`
	Status    json.RawMessage `json:"status,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type NativeTurn struct {
	ID          string            `json:"id"`
	Status      string            `json:"status"`
	StartedAt   *time.Time        `json:"started_at,omitempty"`
	CompletedAt *time.Time        `json:"completed_at,omitempty"`
	DurationMS  *int64            `json:"duration_ms,omitempty"`
	Error       json.RawMessage   `json:"error,omitempty"`
	Items       []json.RawMessage `json:"items,omitempty"`
}

type WorkspaceCatalogProvider interface {
	ListWorkspaces(ctx context.Context) ([]Workspace, error)
	ResolveWorkspace(ctx context.Context, ref string) (Workspace, error)
}

type WorkspaceDevice struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Online  bool   `json:"online"`
	Revoked bool   `json:"revoked"`
}

type WorkspaceDeviceCatalogProvider interface {
	ListWorkspaceDevices(ctx context.Context) ([]WorkspaceDevice, error)
}

// WorkspaceAccessValidator 在目录状态的权威设备上重新验证目录和 thread 归属。
// Linux control/server 不得用本机文件系统推断 macOS Runtime 的目录状态。
type WorkspaceAccessValidator interface {
	ValidateWorkspaceAccess(ctx context.Context, workspace Workspace) error
	ValidateNativeThreadAccess(ctx context.Context, workspace Workspace, thread NativeThread) error
}

type ConversationKind string

const (
	ConversationKindDraft  ConversationKind = "draft"
	ConversationKindThread ConversationKind = "thread"
)

type ConversationRef struct {
	Kind ConversationKind `json:"kind"`
	ID   string           `json:"id"`
}

type WorkspaceChatSelection struct {
	ClientID     string          `json:"client_id"`
	WorkspaceRef string          `json:"workspace_ref"`
	Conversation ConversationRef `json:"conversation"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

type WorkspaceChatMenuSnapshot struct {
	ClientID  string    `json:"client_id"`
	Kind      string    `json:"kind"`
	Revision  string    `json:"revision"`
	ItemIDs   []string  `json:"item_ids"`
	UpdatedAt time.Time `json:"updated_at"`
}

type NativePageRequest struct {
	Cursor        string `json:"cursor,omitempty"`
	Limit         int    `json:"limit,omitempty"`
	SortDirection string `json:"sort_direction,omitempty"`
}

type NativeThreadPage struct {
	Data            []NativeThread `json:"data"`
	NextCursor      string         `json:"next_cursor,omitempty"`
	BackwardsCursor string         `json:"backwards_cursor,omitempty"`
}

type NativeTurnPage struct {
	Data            []NativeTurn `json:"data"`
	NextCursor      string       `json:"next_cursor,omitempty"`
	BackwardsCursor string       `json:"backwards_cursor,omitempty"`
}

type NativeItem struct {
	TurnID string          `json:"turn_id"`
	Item   json.RawMessage `json:"item"`
}

type NativeItemPage struct {
	Data            []NativeItem `json:"data"`
	NextCursor      string       `json:"next_cursor,omitempty"`
	BackwardsCursor string       `json:"backwards_cursor,omitempty"`
}

type CapabilityStatus struct {
	Supported bool   `json:"supported"`
	Reason    string `json:"reason,omitempty"`
}

type ReasoningEffortOption struct {
	Effort      string `json:"effort"`
	Description string `json:"description,omitempty"`
}

type ServiceTierOption struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type NativeModelOption struct {
	ID                     string                  `json:"id"`
	Model                  string                  `json:"model"`
	DisplayName            string                  `json:"display_name"`
	Description            string                  `json:"description,omitempty"`
	Hidden                 bool                    `json:"hidden"`
	Default                bool                    `json:"default"`
	DefaultReasoningEffort string                  `json:"default_reasoning_effort"`
	ReasoningEfforts       []ReasoningEffortOption `json:"reasoning_efforts"`
	InputModalities        []string                `json:"input_modalities"`
	SupportsPersonality    bool                    `json:"supports_personality"`
	ServiceTiers           []ServiceTierOption     `json:"service_tiers"`
	DefaultServiceTier     string                  `json:"default_service_tier,omitempty"`
}

type NativeCollaborationModeOption struct {
	Name            string  `json:"name"`
	Mode            *string `json:"mode,omitempty"`
	Model           *string `json:"model,omitempty"`
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`
}

type NativePermissionProfile struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
	Allowed     bool   `json:"allowed"`
}

type NativeRealtimeVoiceCatalog struct {
	V1        []string `json:"v1"`
	V2        []string `json:"v2"`
	DefaultV1 string   `json:"default_v1,omitempty"`
	DefaultV2 string   `json:"default_v2,omitempty"`
}

type NativeRuntimeCatalog struct {
	Capabilities  map[string]CapabilityStatus     `json:"capabilities"`
	Models        []NativeModelOption             `json:"models"`
	Modes         []NativeCollaborationModeOption `json:"modes"`
	Permissions   []NativePermissionProfile       `json:"permissions"`
	Personalities []string                        `json:"personalities"`
	Summaries     []string                        `json:"summaries"`
	Voices        NativeRealtimeVoiceCatalog      `json:"voices"`
}

type NativeCollaborationSettings struct {
	Model                 string  `json:"model"`
	ReasoningEffort       *string `json:"reasoning_effort"`
	DeveloperInstructions *string `json:"developer_instructions"`
}

type NativeCollaborationMode struct {
	Mode     string                      `json:"mode"`
	Settings NativeCollaborationSettings `json:"settings"`
}

type NativeThreadSettings struct {
	Revision          string                   `json:"revision"`
	Model             string                   `json:"model"`
	ModelProvider     string                   `json:"model_provider,omitempty"`
	Effort            string                   `json:"effort,omitempty"`
	ServiceTier       string                   `json:"service_tier,omitempty"`
	Personality       string                   `json:"personality,omitempty"`
	Summary           string                   `json:"summary,omitempty"`
	PermissionProfile string                   `json:"permission_profile,omitempty"`
	ApprovalPolicy    json.RawMessage          `json:"approval_policy,omitempty"`
	ApprovalsReviewer string                   `json:"approvals_reviewer,omitempty"`
	SandboxPolicy     json.RawMessage          `json:"sandbox_policy,omitempty"`
	CollaborationMode *NativeCollaborationMode `json:"collaboration_mode,omitempty"`
}

type NativeThreadSettingsPatch struct {
	Model             *string `json:"model,omitempty"`
	Effort            *string `json:"effort,omitempty"`
	PlanEffort        *string `json:"plan_effort,omitempty"`
	ServiceTier       *string `json:"service_tier,omitempty"`
	Personality       *string `json:"personality,omitempty"`
	Summary           *string `json:"summary,omitempty"`
	PermissionProfile *string `json:"permission_profile,omitempty"`
	Mode              *string `json:"mode,omitempty"`
}

type WorkspaceChatDraft struct {
	ID            string                    `json:"id"`
	OwnerClientID string                    `json:"owner_client_id"`
	WorkspaceRef  string                    `json:"workspace_ref"`
	State         string                    `json:"state"`
	ThreadID      string                    `json:"thread_id,omitempty"`
	SettingsPatch NativeThreadSettingsPatch `json:"settings_patch"`
	CreatedAt     time.Time                 `json:"created_at"`
	UpdatedAt     time.Time                 `json:"updated_at"`
}

type WorkspaceChatSubmission struct {
	RequestID      string          `json:"request_id"`
	ClientID       string          `json:"client_id"`
	WorkspaceRef   string          `json:"workspace_ref"`
	Conversation   ConversationRef `json:"conversation"`
	ThreadID       string          `json:"thread_id,omitempty"`
	NativeTurnID   string          `json:"native_turn_id,omitempty"`
	Kind           string          `json:"kind"`
	ExpectedTurnID string          `json:"expected_turn_id,omitempty"`
	InputJSON      json.RawMessage `json:"-"`
	Status         string          `json:"status"`
	Error          string          `json:"error,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

type WorkspaceChatInteractionRecord struct {
	Interaction          NativeInteraction `json:"interaction"`
	ConnectionGeneration uint64            `json:"connection_generation"`
	Status               string            `json:"status"`
	ExpiresAt            time.Time         `json:"expires_at,omitempty"`
}

type WorkspaceChatSettingIntent struct {
	ID           string                    `json:"id"`
	WorkspaceRef string                    `json:"workspace_ref"`
	ThreadID     string                    `json:"thread_id"`
	Patch        NativeThreadSettingsPatch `json:"patch"`
	Status       string                    `json:"status"`
	Error        string                    `json:"error,omitempty"`
	CreatedAt    time.Time                 `json:"created_at"`
	UpdatedAt    time.Time                 `json:"updated_at"`
}

type WorkspaceChatDeliveryRecord struct {
	ID           string          `json:"id"`
	ClientID     string          `json:"client_id"`
	WorkspaceRef string          `json:"workspace_ref"`
	Conversation ConversationRef `json:"conversation"`
	RequestID    string          `json:"request_id,omitempty"`
	Transport    string          `json:"transport"`
	Destination  string          `json:"destination"`
	Status       string          `json:"status"`
	Error        string          `json:"error,omitempty"`
	Metadata     json.RawMessage `json:"metadata,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

type WorkspaceChatRepository interface {
	GetSelection(ctx context.Context, clientID string) (*WorkspaceChatSelection, error)
	PutSelection(ctx context.Context, selection WorkspaceChatSelection) error
	DeleteSelection(ctx context.Context, clientID string) error
	GetMenu(ctx context.Context, clientID, kind string) (*WorkspaceChatMenuSnapshot, error)
	PutMenu(ctx context.Context, snapshot WorkspaceChatMenuSnapshot) error
	CreateDraftAndSelect(ctx context.Context, draft WorkspaceChatDraft, selection WorkspaceChatSelection) error
	GetDraft(ctx context.Context, draftID string) (*WorkspaceChatDraft, error)
	UpdateDraftSettings(ctx context.Context, draftID, ownerClientID, workspaceRef string, patch NativeThreadSettingsPatch, updatedAt time.Time) error
	MarkDraftMaterializationUncertain(ctx context.Context, draftID string) error
	MaterializeDraft(ctx context.Context, draftID, requestID, threadID, nativeTurnID string) error
	BeginSubmission(ctx context.Context, submission WorkspaceChatSubmission) error
	MarkSubmissionAccepted(ctx context.Context, requestID, threadID, nativeTurnID string) error
	FinishSubmission(ctx context.Context, requestID, status, errorMessage string) error
	ListUnfinishedSubmissions(ctx context.Context) ([]WorkspaceChatSubmission, error)
	PutInteraction(ctx context.Context, interaction WorkspaceChatInteractionRecord) error
	ResolveInteraction(ctx context.Context, interactionID, status string) error
	ListPendingInteractions(ctx context.Context, threadID string) ([]WorkspaceChatInteractionRecord, error)
	ExpirePendingInteractions(ctx context.Context, status string) error
	PutSettingIntent(ctx context.Context, intent WorkspaceChatSettingIntent) error
	ResolveSettingIntent(ctx context.Context, intentID, status, errorMessage string) error
	ListPendingSettingIntents(ctx context.Context) ([]WorkspaceChatSettingIntent, error)
	PutDelivery(ctx context.Context, delivery WorkspaceChatDeliveryRecord) error
	FinishDelivery(ctx context.Context, deliveryID, status, errorMessage string) error
	ListPendingDeliveries(ctx context.Context) ([]WorkspaceChatDeliveryRecord, error)
	Close() error
}

type NativeActiveTurn struct {
	ID        string    `json:"id"`
	RequestID string    `json:"request_id,omitempty"`
	StartedAt time.Time `json:"started_at"`
}

type NativeInteraction struct {
	ID                   string            `json:"id"`
	Kind                 string            `json:"kind"`
	ThreadID             string            `json:"thread_id"`
	TurnID               string            `json:"turn_id,omitempty"`
	ItemID               string            `json:"item_id,omitempty"`
	RequestID            json.RawMessage   `json:"-"`
	ConnectionGeneration uint64            `json:"-"`
	AllowedDecisions     []json.RawMessage `json:"allowed_decisions,omitempty"`
	Payload              json.RawMessage   `json:"payload"`
	OccurredAt           time.Time         `json:"occurred_at"`
}

type NativeConversationSnapshot struct {
	Thread              NativeThread                `json:"thread"`
	Settings            NativeThreadSettings        `json:"settings"`
	Status              json.RawMessage             `json:"status,omitempty"`
	Usage               json.RawMessage             `json:"usage,omitempty"`
	ActiveTurn          *NativeActiveTurn           `json:"active_turn,omitempty"`
	PendingInteractions []NativeInteraction         `json:"pending_interactions"`
	Capabilities        map[string]CapabilityStatus `json:"capabilities"`
	DeepLink            string                      `json:"deep_link"`
}

type NativeUserInput struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// AttachmentRef 只在 control 与已认证 Runtime 之间传输。公开客户端即使
	// 伪造该字段，也会被 WorkspaceChatService 的非可信输入校验拒绝。
	AttachmentRef string `json:"attachment_ref,omitempty"`
	MimeType      string `json:"mime_type,omitempty"`
	FileName      string `json:"file_name,omitempty"`
	Detail        string `json:"detail,omitempty"`
	// 以下字段只存在于对应进程内，不能跨 JSON 边界传输。
	URL       string `json:"-"`
	LocalPath string `json:"-"`
	Data      []byte `json:"-"`
}

type NativeTurnStartRequest struct {
	ClientMessageID string            `json:"client_message_id"`
	Input           []NativeUserInput `json:"input"`
}

type NativeTurnResult struct {
	TurnID string `json:"turn_id"`
}

type NativeInteractionResponse struct {
	InteractionID string          `json:"interaction_id"`
	Response      json.RawMessage `json:"response"`
}

type NativeRealtimeStartRequest struct {
	SDP     string `json:"sdp"`
	Voice   string `json:"voice,omitempty"`
	Version string `json:"version,omitempty"`
}

type NativeEventEnvelope struct {
	Method               string                `json:"method"`
	ThreadID             string                `json:"thread_id,omitempty"`
	TurnID               string                `json:"turn_id,omitempty"`
	ItemID               string                `json:"item_id,omitempty"`
	RequestID            json.RawMessage       `json:"-"`
	ConnectionGeneration uint64                `json:"-"`
	InteractionID        string                `json:"interaction_id,omitempty"`
	AllowedDecisions     []json.RawMessage     `json:"allowed_decisions,omitempty"`
	Settings             *NativeThreadSettings `json:"settings,omitempty"`
	Payload              json.RawMessage       `json:"payload"`
	OccurredAt           time.Time             `json:"occurred_at"`
}

// WorkspaceChatEvent 是 WebSocket 唯一服务端事件封装。原生通知始终放在
// type=native_event 的 Payload 中，不再扁平转换为另一种事件。
type WorkspaceChatEvent struct {
	Type         string          `json:"type"`
	Epoch        string          `json:"epoch"`
	Sequence     uint64          `json:"sequence"`
	WorkspaceRef string          `json:"workspace_ref"`
	Conversation ConversationRef `json:"conversation"`
	ThreadID     string          `json:"thread_id,omitempty"`
	TurnID       string          `json:"turn_id,omitempty"`
	RequestID    string          `json:"request_id,omitempty"`
	Payload      json.RawMessage `json:"payload,omitempty"`
	Error        string          `json:"error,omitempty"`
	OccurredAt   time.Time       `json:"occurred_at"`
}

type WorkspaceChatSubscription struct {
	Initial []WorkspaceChatEvent
	Events  <-chan WorkspaceChatEvent
	Cancel  func()
}

// NativeConversationSubscription 将事件流绑定到一个物理后端连接代际。
// 所有会改变原生会话状态的操作都必须携带该代际，避免断线后把操作发送到
// 尚未完成 thread/resume 和事件订阅的新连接。
type NativeConversationSubscription struct {
	Generation uint64
	Events     <-chan NativeEventEnvelope
	Cancel     func()
}

type NativeConversationBackend interface {
	ListNativeConversations(ctx context.Context, workspace Workspace, page NativePageRequest) (NativeThreadPage, error)
	ReadNativeConversation(ctx context.Context, workspace Workspace, threadID string) (NativeConversationSnapshot, error)
	StartNativeConversation(ctx context.Context, workspace Workspace) (NativeConversationSnapshot, error)
	ListNativeTurns(ctx context.Context, workspace Workspace, threadID string, page NativePageRequest) (NativeTurnPage, error)
	ListNativeItems(ctx context.Context, workspace Workspace, threadID, turnID string, page NativePageRequest) (NativeItemPage, error)
	NativeRuntimeCatalog(ctx context.Context, workspace Workspace) (NativeRuntimeCatalog, error)
	SubscribeNativeConversation(ctx context.Context, workspace Workspace, threadID string) (NativeConversationSubscription, error)
}

type NativeConversationSettingsController interface {
	UpdateNativeConversationSettings(ctx context.Context, workspace Workspace, threadID string, generation uint64, patch NativeThreadSettingsPatch) (NativeThreadSettings, error)
}

type NativeConversationTurnController interface {
	StartNativeTurn(ctx context.Context, workspace Workspace, threadID string, generation uint64, request NativeTurnStartRequest) (NativeTurnResult, error)
	SteerNativeTurn(ctx context.Context, workspace Workspace, threadID string, generation uint64, expectedTurnID string, input []NativeUserInput) (NativeTurnResult, error)
	InterruptNativeTurn(ctx context.Context, workspace Workspace, threadID string, generation uint64, turnID string) error
	RespondNativeInteraction(ctx context.Context, workspace Workspace, threadID string, generation uint64, requestID, response json.RawMessage) error
}

type NativeConversationRealtimeController interface {
	StartNativeRealtime(ctx context.Context, workspace Workspace, threadID string, generation uint64, request NativeRealtimeStartRequest) error
	AppendNativeRealtimeText(ctx context.Context, workspace Workspace, threadID string, generation uint64, text string) error
	StopNativeRealtime(ctx context.Context, workspace Workspace, threadID string, generation uint64) error
}
