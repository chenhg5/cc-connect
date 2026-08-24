package runtimeprotocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Version 是 control、server 和 Runtime 共同实现的唯一协议版本。
const Version = "runtime-v1"

const SignedPurposeAttachmentDownload = "attachment/download"

type Method string

const (
	MethodCatalogList      Method = "catalog/list"
	MethodThreadList       Method = "thread/list"
	MethodThreadRead       Method = "thread/read"
	MethodThreadStart      Method = "thread/start"
	MethodTurnList         Method = "turn/list"
	MethodItemList         Method = "item/list"
	MethodRuntimeCatalog   Method = "runtime/catalog"
	MethodThreadSubscribe  Method = "thread/subscribe"
	MethodSettingsUpdate   Method = "settings/update"
	MethodTurnStart        Method = "turn/start"
	MethodTurnSteer        Method = "turn/steer"
	MethodTurnInterrupt    Method = "turn/interrupt"
	MethodInteractionReply Method = "interaction/respond"
	MethodRealtimeStart    Method = "realtime/start"
	MethodRealtimeAppend   Method = "realtime/append_text"
	MethodRealtimeStop     Method = "realtime/stop"
	MethodNativeEvent      Method = "native/event"
	MethodCatalogChanged   Method = "catalog/changed"
	MethodHeartbeat        Method = "runtime/heartbeat"
	MethodAcknowledge      Method = "runtime/ack"
	MethodUpdateRequired   Method = "runtime/update_required"
	MethodUpdateStage      Method = "runtime/update/stage"
	MethodUpdateActivate   Method = "runtime/update/activate"
	MethodUpdateConfirm    Method = "runtime/update/confirm"
)

var methods = map[Method]struct{}{
	MethodCatalogList: {}, MethodThreadList: {}, MethodThreadRead: {}, MethodThreadStart: {},
	MethodTurnList: {}, MethodItemList: {}, MethodRuntimeCatalog: {}, MethodThreadSubscribe: {},
	MethodSettingsUpdate: {}, MethodTurnStart: {}, MethodTurnSteer: {}, MethodTurnInterrupt: {},
	MethodInteractionReply: {}, MethodRealtimeStart: {}, MethodRealtimeAppend: {}, MethodRealtimeStop: {},
	MethodNativeEvent: {}, MethodCatalogChanged: {}, MethodHeartbeat: {}, MethodAcknowledge: {}, MethodUpdateRequired: {},
	MethodUpdateStage: {}, MethodUpdateActivate: {}, MethodUpdateConfirm: {},
}

type RuntimeUpdateRequest struct {
	Tag string `json:"tag"`
}

type RuntimeUpdateResult struct {
	Tag    string `json:"tag"`
	Status string `json:"status"`
}

type Acknowledgement struct {
	ConfirmedSequence uint64 `json:"confirmed_sequence"`
}

// ContractHash 对完整方法集合和 envelope 字段生成稳定指纹。协议不匹配时禁止兼容解析。
var ContractHash = contractHash()

func contractHash() string {
	names := make([]string, 0, len(methods))
	for method := range methods {
		names = append(names, string(method))
	}
	sort.Strings(names)
	canonical := Version + "|contract_hash,device_id,connection_generation,sequence,request_id,method,resource,payload,error|" + strings.Join(names, ",") +
		"|native_user_input:type,text,attachment_ref,mime_type,file_name,detail|attachment_download:workspace_ref,type,mime_type,file_name,data|runtime_update:tag"
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

type Resource struct {
	WorkspaceRef    string `json:"workspace_ref,omitempty"`
	ConversationRef string `json:"conversation_ref,omitempty"`
	TurnID          string `json:"turn_id,omitempty"`
	ItemID          string `json:"item_id,omitempty"`
	InteractionID   string `json:"interaction_id,omitempty"`
}

// Workspace 描述 Runtime 本地目录的可公开元数据。LocalRef 仅在 control 与
// Runtime 之间传输，RootPath/CODEX_HOME 不属于协议。
type Workspace struct {
	LocalRef    string `json:"local_ref"`
	ProjectID   string `json:"project_id"`
	ProjectName string `json:"project_name"`
	RootIndex   int    `json:"root_index"`
	RootName    string `json:"root_name"`
	Available   bool   `json:"available"`
	Reason      string `json:"reason,omitempty"`
	Order       int    `json:"order"`
}

type Catalog struct {
	Workspaces []Workspace `json:"workspaces"`
}

type AttachmentUpload struct {
	Type     string `json:"type"`
	MimeType string `json:"mime_type,omitempty"`
	FileName string `json:"file_name,omitempty"`
	Data     []byte `json:"data"`
}

type AttachmentStageRequest struct {
	DeviceID     string             `json:"device_id"`
	WorkspaceRef string             `json:"workspace_ref"`
	Attachments  []AttachmentUpload `json:"attachments"`
}

type AttachmentReference struct {
	Ref      string `json:"ref"`
	Type     string `json:"type"`
	MimeType string `json:"mime_type,omitempty"`
	FileName string `json:"file_name,omitempty"`
}

type AttachmentContent struct {
	WorkspaceRef string `json:"workspace_ref"`
	Type         string `json:"type"`
	MimeType     string `json:"mime_type,omitempty"`
	FileName     string `json:"file_name,omitempty"`
	Data         []byte `json:"data"`
}

// SignedRequestMessage 是 Runtime HTTP 边界的唯一签名规范。
func SignedRequestMessage(purpose, deviceID, resource, timestamp, nonce string) []byte {
	return []byte(strings.Join([]string{ContractHash, purpose, deviceID, resource, timestamp, nonce}, "\n"))
}

type InternalRequest struct {
	DeviceID string          `json:"device_id,omitempty"`
	Method   Method          `json:"method"`
	Resource Resource        `json:"resource,omitempty"`
	Payload  json.RawMessage `json:"payload,omitempty"`
}

type InternalResponse struct {
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Details json.RawMessage `json:"details,omitempty"`
}

type Envelope struct {
	ContractHash         string          `json:"contract_hash"`
	DeviceID             string          `json:"device_id"`
	ConnectionGeneration uint64          `json:"connection_generation"`
	Sequence             uint64          `json:"sequence"`
	RequestID            string          `json:"request_id,omitempty"`
	Method               Method          `json:"method"`
	Resource             Resource        `json:"resource,omitempty"`
	Payload              json.RawMessage `json:"payload,omitempty"`
	Error                *RPCError       `json:"error,omitempty"`
}

var (
	ErrContractMismatch = errors.New("runtime protocol contract mismatch")
	ErrUnknownMethod    = errors.New("runtime protocol method is not declared")
)

func (e Envelope) Validate() error {
	if e.ContractHash != ContractHash {
		return fmt.Errorf("%w: received %q, expected %q", ErrContractMismatch, e.ContractHash, ContractHash)
	}
	if strings.TrimSpace(e.DeviceID) == "" {
		return errors.New("runtime protocol: device_id is required")
	}
	if e.ConnectionGeneration == 0 {
		return errors.New("runtime protocol: connection_generation must be positive")
	}
	if e.Sequence == 0 {
		return errors.New("runtime protocol: sequence must be positive")
	}
	if _, ok := methods[e.Method]; !ok {
		return fmt.Errorf("%w: %q", ErrUnknownMethod, e.Method)
	}
	if e.Error != nil && strings.TrimSpace(e.Error.Code) == "" {
		return errors.New("runtime protocol: error.code is required")
	}
	return nil
}

func Decode(raw []byte) (Envelope, error) {
	var envelope Envelope
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return Envelope{}, fmt.Errorf("runtime protocol: decode envelope: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return Envelope{}, err
	}
	if err := envelope.Validate(); err != nil {
		return Envelope{}, err
	}
	return envelope, nil
}

func DecodePayload[T any](envelope Envelope) (T, error) {
	var value T
	if len(envelope.Payload) == 0 {
		return value, errors.New("runtime protocol: payload is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(envelope.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, fmt.Errorf("runtime protocol: decode %s payload: %w", envelope.Method, err)
	}
	if err := ensureEOF(decoder); err != nil {
		return value, err
	}
	return value, nil
}

func MarshalPayload(value any) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("runtime protocol: encode payload: %w", err)
	}
	return raw, nil
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("runtime protocol: multiple JSON values are not allowed")
		}
		return fmt.Errorf("runtime protocol: read trailing data: %w", err)
	}
	return nil
}

type SequenceGuard struct {
	generation uint64
	sequence   uint64
}

func (g *SequenceGuard) Accept(envelope Envelope) error {
	if err := envelope.Validate(); err != nil {
		return err
	}
	if g.generation != envelope.ConnectionGeneration {
		g.generation = envelope.ConnectionGeneration
		g.sequence = 0
	}
	if envelope.Sequence != g.sequence+1 {
		return fmt.Errorf("runtime protocol: sequence gap: received %d after %d", envelope.Sequence, g.sequence)
	}
	g.sequence = envelope.Sequence
	return nil
}
