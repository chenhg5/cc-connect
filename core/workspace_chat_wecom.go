package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const workspaceChatMenuPageSize = 8

var (
	errWorkspaceChatAttachmentSaveFailed = errors.New("workspace chat: failed to save verified platform attachments")
	errWorkspaceChatEmptyMessage         = errors.New("workspace chat: platform message contains no supported input")
)

// WorkspaceChatTransportSource 由明确接入统一工作区聊天的平台实现。
// 返回值只用于匹配 workspace_chat.transports；平台名称本身不代表接入资格。
type WorkspaceChatTransportSource interface {
	WorkspaceChatTransport() string
}

// HandleIncoming 将企业微信单聊直接接入同一个原生 conversation actor。
// 平台 Session 属于另一产品域，不会经过这里创建或更新。
func (s *WorkspaceChatService) HandleIncoming(platform Platform, message *Message) bool {
	if platform == nil || message == nil {
		return false
	}
	source, ok := platform.(WorkspaceChatTransportSource)
	if !ok || !s.TransportEnabled(source.WorkspaceChatTransport()) {
		return false
	}
	s.engine.i18n.DetectAndSet(message.Content)
	if message.Scope == ConversationScopeGroup {
		s.replyWorkspaceChat(platform, message, s.engine.i18n.T(MsgWorkspaceChatGroupUnsupported))
		return true
	}
	if strings.TrimSpace(message.UserID) == "" {
		s.replyWorkspaceChat(platform, message, s.engine.i18n.T(MsgWorkspaceChatUserIDMissing))
		return true
	}
	clientID := "wecom:user:" + strings.TrimSpace(message.UserID)
	command, argument := splitWorkspaceChatCommand(message.Content)
	switch command {
	case "/projects":
		s.handleWorkspaceProjectsCommand(platform, message, clientID, argument)
	case "/project":
		s.handleWorkspaceProjectCommand(platform, message, clientID, argument)
	case "/threads":
		s.handleWorkspaceThreadsCommand(platform, message, clientID, argument)
	case "/switch":
		s.handleWorkspaceSwitchCommand(platform, message, clientID, argument)
	case "/new":
		s.handleWorkspaceNewCommand(platform, message, clientID, argument)
	case "/link":
		s.handleWorkspaceLinkCommand(platform, message, clientID)
	case "/current":
		s.handleWorkspaceCurrentCommand(platform, message, clientID)
	case "/history":
		s.handleWorkspaceHistoryCommand(platform, message, clientID, argument)
	case "/usage":
		s.handleWorkspaceUsageCommand(platform, message, clientID)
	case "/cancel":
		s.handleWorkspaceCancelCommand(platform, message, clientID)
	case "/steer":
		s.handleWorkspaceSteerCommand(platform, message, clientID, argument)
	case "/requests":
		s.handleWorkspaceRequestsCommand(platform, message, clientID)
	case "/respond":
		s.handleWorkspaceRespondCommand(platform, message, clientID, argument)
	case "/answer":
		s.handleWorkspaceAnswerCommand(platform, message, clientID, argument)
	case "/models", "/efforts", "/modes", "/permissions", "/tiers", "/personalities", "/summaries":
		s.handleWorkspaceSettingsListCommand(platform, message, clientID, strings.TrimPrefix(command, "/"), argument)
	case "/model", "/effort", "/mode", "/permission", "/tier", "/personality", "/summary":
		s.handleWorkspaceSettingSelectCommand(platform, message, clientID, strings.TrimPrefix(command, "/"), argument)
	case "":
		s.handleWorkspaceOrdinaryMessage(platform, message, clientID)
	default:
		s.replyWorkspaceChat(platform, message, s.engine.i18n.T(MsgWorkspaceChatUsage))
	}
	return true
}

func (s *WorkspaceChatService) handleWorkspaceProjectsCommand(p Platform, msg *Message, clientID, argument string) {
	page, err := parseWorkspaceChatPositiveInt(argument, 1)
	if err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	text, err := s.renderWorkspaceMenu(context.Background(), clientID, page)
	if err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	s.replyWorkspaceChat(p, msg, text)
}

func (s *WorkspaceChatService) handleWorkspaceProjectCommand(p Platform, msg *Message, clientID, argument string) {
	index, err := parseWorkspaceChatPositiveInt(argument, 0)
	if err != nil || index == 0 {
		s.replyWorkspaceChatError(p, msg, errWorkspacePositiveNumber)
		return
	}
	ref, err := s.MenuItem(context.Background(), clientID, "projects", index)
	if err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	selection, err := s.SelectWorkspace(context.Background(), clientID, ref)
	if err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	if selection.Conversation.Kind == ConversationKindDraft {
		s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatProjectSelectedDraft))
	} else {
		s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatProjectSelectedThread))
	}
}

func (s *WorkspaceChatService) handleWorkspaceThreadsCommand(p Platform, msg *Message, clientID, argument string) {
	page, err := parseWorkspaceChatPositiveInt(argument, 1)
	if err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	text, err := s.renderThreadMenu(context.Background(), clientID, page)
	if err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	s.replyWorkspaceChat(p, msg, text)
}

func (s *WorkspaceChatService) handleWorkspaceSwitchCommand(p Platform, msg *Message, clientID, argument string) {
	index, err := parseWorkspaceChatPositiveInt(argument, 0)
	if err != nil || index == 0 {
		s.replyWorkspaceChatError(p, msg, errWorkspacePositiveNumber)
		return
	}
	threadID, err := s.MenuItem(context.Background(), clientID, "threads", index)
	if err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	selection, err := s.Selection(context.Background(), clientID)
	if err != nil || selection == nil {
		s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatNoSelection))
		return
	}
	if _, err := s.SelectConversation(context.Background(), clientID, selection.WorkspaceRef, ConversationRef{Kind: ConversationKindThread, ID: threadID}); err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	snapshot, err := s.ReadThread(context.Background(), selection.WorkspaceRef, threadID)
	if err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	s.replyWorkspaceChat(p, msg, s.engine.i18n.Tf(MsgWorkspaceChatThreadSelected, s.threadDisplayName(snapshot.Thread)))
}

func (s *WorkspaceChatService) handleWorkspaceNewCommand(p Platform, msg *Message, clientID, argument string) {
	if strings.TrimSpace(argument) != "" {
		s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatNewArguments))
		return
	}
	selection, err := s.Selection(context.Background(), clientID)
	if err != nil || selection == nil {
		s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatNoSelection))
		return
	}
	if _, err := s.CreateDraft(context.Background(), clientID, selection.WorkspaceRef); err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatDraftCreated))
}

func (s *WorkspaceChatService) handleWorkspaceLinkCommand(p Platform, msg *Message, clientID string) {
	selection, err := s.Selection(context.Background(), clientID)
	if err != nil || selection == nil {
		s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatNeedConversation))
		return
	}
	if selection.Conversation.Kind == ConversationKindDraft {
		s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatLinkAfterFirstTurn))
		return
	}
	snapshot, err := s.ReadThread(context.Background(), selection.WorkspaceRef, selection.Conversation.ID)
	if err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	s.replyWorkspaceChat(p, msg, snapshot.DeepLink)
}

func (s *WorkspaceChatService) handleWorkspaceCurrentCommand(p Platform, msg *Message, clientID string) {
	selection, err := s.Selection(context.Background(), clientID)
	if err != nil || selection == nil {
		s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatCurrentNoProject))
		return
	}
	workspace, err := s.resolveWorkspace(context.Background(), selection.WorkspaceRef)
	if err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	name := workspaceDisplayName(workspace)
	if selection.Conversation.Kind == ConversationKindDraft {
		s.replyWorkspaceChat(p, msg, s.engine.i18n.Tf(MsgWorkspaceChatCurrentDraft, name, selection.Conversation.ID))
		return
	}
	snapshot, err := s.ReadThread(context.Background(), selection.WorkspaceRef, selection.Conversation.ID)
	if err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	status := s.engine.i18n.T(MsgWorkspaceChatStatusIdle)
	if snapshot.ActiveTurn != nil {
		status = s.engine.i18n.Tf(MsgWorkspaceChatStatusRunning, snapshot.ActiveTurn.ID)
	}
	mode := "default"
	effort := snapshot.Settings.Effort
	if collaboration := snapshot.Settings.CollaborationMode; collaboration != nil {
		if value := strings.TrimSpace(collaboration.Mode); value != "" {
			mode = value
		}
		if collaboration.Settings.ReasoningEffort != nil {
			effort = strings.TrimSpace(*collaboration.Settings.ReasoningEffort)
		}
	}
	s.replyWorkspaceChat(p, msg, s.engine.i18n.Tf(MsgWorkspaceChatCurrent, name, s.threadDisplayName(snapshot.Thread), status, snapshot.Settings.Model, mode, effort))
}

func (s *WorkspaceChatService) handleWorkspaceHistoryCommand(p Platform, msg *Message, clientID, argument string) {
	count, err := parseWorkspaceChatPositiveInt(argument, 10)
	if err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	if count > 50 {
		count = 50
	}
	selection, snapshot, ok := s.selectedThread(p, msg, clientID)
	if !ok {
		return
	}
	turns, err := s.ListTurns(context.Background(), selection.WorkspaceRef, snapshot.Thread.ID, NativePageRequest{Limit: count, SortDirection: "desc"})
	if err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	var blocks []string
	for i := len(turns.Data) - 1; i >= 0; i-- {
		cursor := ""
		seenCursors := map[string]struct{}{"": {}}
		for {
			items, err := s.ListItems(context.Background(), selection.WorkspaceRef, snapshot.Thread.ID, turns.Data[i].ID, NativePageRequest{
				Cursor: cursor, Limit: 100, SortDirection: "asc",
			})
			if err != nil {
				s.replyWorkspaceChatError(p, msg, err)
				return
			}
			for _, item := range items.Data {
				if role, text := s.nativeHistoryItem(item.Item); text != "" {
					blocks = append(blocks, fmt.Sprintf("**%s**\n%s", role, truncateWorkspaceChatText(text, 1500)))
				}
			}
			if items.NextCursor == "" {
				break
			}
			if _, repeated := seenCursors[items.NextCursor]; repeated {
				s.replyWorkspaceChatError(p, msg, fmt.Errorf("workspace chat: native item pagination repeated cursor %q", items.NextCursor))
				return
			}
			cursor = items.NextCursor
			seenCursors[cursor] = struct{}{}
		}
	}
	if len(blocks) == 0 {
		s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatHistoryEmpty))
		return
	}
	s.replyWorkspaceChat(p, msg, strings.Join(blocks, "\n\n"))
}

func (s *WorkspaceChatService) handleWorkspaceUsageCommand(p Platform, msg *Message, clientID string) {
	_, snapshot, ok := s.selectedThread(p, msg, clientID)
	if !ok {
		return
	}
	if len(snapshot.Usage) == 0 || string(snapshot.Usage) == "null" {
		s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatUsageUnavailable))
		return
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, snapshot.Usage, "", "  "); err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	s.replyWorkspaceChat(p, msg, "```json\n"+pretty.String()+"\n```")
}

func (s *WorkspaceChatService) handleWorkspaceCancelCommand(p Platform, msg *Message, clientID string) {
	selection, snapshot, ok := s.selectedThread(p, msg, clientID)
	if !ok {
		return
	}
	if snapshot.ActiveTurn == nil {
		s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatTurnNotRunning))
		return
	}
	if err := s.InterruptTurn(context.Background(), selection.WorkspaceRef, snapshot.Thread.ID, snapshot.ActiveTurn.ID); err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatCancelled))
}

func (s *WorkspaceChatService) handleWorkspaceSteerCommand(p Platform, msg *Message, clientID, argument string) {
	if strings.TrimSpace(argument) == "" && len(msg.Images) == 0 && len(msg.Files) == 0 && msg.Audio == nil {
		s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatSteerUsage))
		return
	}
	selection, snapshot, ok := s.selectedThread(p, msg, clientID)
	if !ok {
		return
	}
	if snapshot.ActiveTurn == nil {
		s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatSteerNoTurn))
		return
	}
	inputs, err := nativeInputsFromPlatformMessage(snapshot.Thread.Cwd, msg, argument)
	if err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	requestID := workspaceMessageRequestID(msg)
	if _, err := s.steerTrustedTurn(context.Background(), clientID, requestID, selection.WorkspaceRef, snapshot.Thread.ID, snapshot.ActiveTurn.ID, inputs); err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatSteerSubmitted))
}

func (s *WorkspaceChatService) handleWorkspaceRequestsCommand(p Platform, msg *Message, clientID string) {
	selection, snapshot, ok := s.selectedThread(p, msg, clientID)
	if !ok {
		return
	}
	if len(snapshot.PendingInteractions) == 0 {
		s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatRequestsEmpty))
		return
	}
	ids := make([]string, 0, len(snapshot.PendingInteractions))
	var lines []string
	for i, request := range snapshot.PendingInteractions {
		ids = append(ids, request.ID)
		decisions := workspaceChatInteractionDecisions(request)
		if decisions != "" {
			decisions = s.engine.i18n.Tf(MsgWorkspaceChatRequestDecisions, decisions)
		}
		lines = append(lines, fmt.Sprintf("%d. %s%s%s", i+1, request.Kind, decisions, workspaceChatInteractionDetail(request)))
	}
	if err := s.SaveMenu(context.Background(), clientID, "requests", ids); err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	s.replyWorkspaceChat(p, msg, s.engine.i18n.Tf(MsgWorkspaceChatRequests, strings.Join(lines, "\n")))
	_ = selection
}

func (s *WorkspaceChatService) handleWorkspaceRespondCommand(p Platform, msg *Message, clientID, argument string) {
	parts := strings.SplitN(strings.TrimSpace(argument), " ", 2)
	if len(parts) != 2 {
		s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatRespondUsage))
		return
	}
	index, err := strconv.Atoi(parts[0])
	if err != nil || index < 1 {
		s.replyWorkspaceChatError(p, msg, errWorkspacePositiveNumber)
		return
	}
	interactionID, err := s.MenuItem(context.Background(), clientID, "requests", index)
	if err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	selection, snapshot, ok := s.selectedThread(p, msg, clientID)
	if !ok {
		return
	}
	var interaction *NativeInteraction
	for _, candidate := range snapshot.PendingInteractions {
		if candidate.ID == interactionID {
			current := candidate
			interaction = &current
			break
		}
	}
	if interaction == nil {
		s.replyWorkspaceChatError(p, msg, ErrWorkspaceInteractionStale)
		return
	}
	response, err := workspaceChatDecisionResponse(*interaction, parts[1])
	if err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	if err := s.RespondInteraction(context.Background(), selection.WorkspaceRef, snapshot.Thread.ID, interactionID, response); err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatRequestSubmitted))
}

func (s *WorkspaceChatService) handleWorkspaceAnswerCommand(p Platform, msg *Message, clientID, argument string) {
	parts := strings.SplitN(strings.TrimSpace(argument), " ", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
		s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatAnswerUsage))
		return
	}
	index, err := strconv.Atoi(parts[0])
	if err != nil || index < 1 {
		s.replyWorkspaceChatError(p, msg, errWorkspacePositiveNumber)
		return
	}
	interactionID, err := s.MenuItem(context.Background(), clientID, "requests", index)
	if err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	selection, snapshot, ok := s.selectedThread(p, msg, clientID)
	if !ok {
		return
	}
	var interaction *NativeInteraction
	for i := range snapshot.PendingInteractions {
		if snapshot.PendingInteractions[i].ID == interactionID {
			interaction = &snapshot.PendingInteractions[i]
			break
		}
	}
	if interaction == nil || interaction.Kind != "item/tool/requestUserInput" {
		s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatAnswerWrongKind))
		return
	}
	if len(workspaceChatQuestionIDs(interaction.Payload)) == 0 {
		s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatAnswerMissingQuestion))
		return
	}
	response, err := workspaceChatAnswerResponse(*interaction, parts[1])
	if err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	if err := s.RespondInteraction(context.Background(), selection.WorkspaceRef, snapshot.Thread.ID, interactionID, response); err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatAnswerSubmitted))
}

func (s *WorkspaceChatService) handleWorkspaceSettingsListCommand(p Platform, msg *Message, clientID, category, argument string) {
	page, err := parseWorkspaceChatPositiveInt(argument, 1)
	if err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	current, ok := s.selectedWorkspaceSettings(p, msg, clientID)
	if !ok {
		return
	}
	values, labels := nativeSettingMenuOptions(category, current.catalog, current.settings)
	start, end, err := workspaceMenuPage(len(values), page)
	if err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	menuKind := workspaceChatSettingsMenuKind(category, current.selection)
	if err := s.SaveMenu(context.Background(), clientID, menuKind, append([]string(nil), values[start:end]...)); err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	lines := []string{s.engine.i18n.Tf(MsgWorkspaceChatSettingsTitle, category, page)}
	for i, label := range labels[start:end] {
		lines = append(lines, fmt.Sprintf("%d. %s", i+1, label))
	}
	singular := strings.TrimSuffix(category, "s")
	switch category {
	case "personalities":
		singular = "personality"
	case "summaries":
		singular = "summary"
	}
	lines = append(lines, s.engine.i18n.Tf(MsgWorkspaceChatSettingsHint, singular))
	s.replyWorkspaceChat(p, msg, strings.Join(lines, "\n"))
}

func (s *WorkspaceChatService) handleWorkspaceSettingSelectCommand(p Platform, msg *Message, clientID, setting, argument string) {
	index, err := parseWorkspaceChatPositiveInt(argument, 0)
	if err != nil || index == 0 {
		s.replyWorkspaceChatError(p, msg, errWorkspacePositiveNumber)
		return
	}
	category := setting + "s"
	switch setting {
	case "personality":
		category = "personalities"
	case "summary":
		category = "summaries"
	}
	current, ok := s.selectedWorkspaceSettings(p, msg, clientID)
	if !ok {
		return
	}
	menuKind := workspaceChatSettingsMenuKind(category, current.selection)
	value, err := s.MenuItem(context.Background(), clientID, menuKind, index)
	if err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	patch := NativeThreadSettingsPatch{}
	switch setting {
	case "model":
		patch.Model = &value
	case "effort":
		if current.settings.CollaborationMode != nil && current.settings.CollaborationMode.Mode == "plan" {
			patch.PlanEffort = &value
		} else {
			patch.Effort = &value
		}
	case "mode":
		patch.Mode = &value
	case "permission":
		patch.PermissionProfile = &value
	case "tier":
		patch.ServiceTier = &value
	case "personality":
		patch.Personality = &value
	case "summary":
		patch.Summary = &value
	default:
		s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatSettingUnsupported))
		return
	}
	if current.draft != nil {
		draft, err := s.UpdateDraftSettings(context.Background(), clientID, current.selection.WorkspaceRef, current.draft.ID, patch)
		if err != nil {
			s.replyWorkspaceChatError(p, msg, err)
			return
		}
		revision := "draft-" + strconv.FormatInt(draft.UpdatedAt.UnixMilli(), 10)
		s.replyWorkspaceChat(p, msg, s.engine.i18n.Tf(MsgWorkspaceChatSettingApplied, revision))
		return
	}
	settings, err := s.UpdateSettings(context.Background(), current.selection.WorkspaceRef, current.threadID, patch)
	if err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	s.replyWorkspaceChat(p, msg, s.engine.i18n.Tf(MsgWorkspaceChatSettingApplied, settings.Revision))
}

type workspaceChatSelectedSettings struct {
	selection *WorkspaceChatSelection
	catalog   NativeRuntimeCatalog
	settings  NativeThreadSettings
	draft     *WorkspaceChatDraft
	threadID  string
}

func (s *WorkspaceChatService) selectedWorkspaceSettings(p Platform, msg *Message, clientID string) (workspaceChatSelectedSettings, bool) {
	selection, err := s.Selection(context.Background(), clientID)
	if err != nil || selection == nil {
		s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatNeedConversation))
		return workspaceChatSelectedSettings{}, false
	}
	catalog, err := s.RuntimeCatalog(context.Background(), selection.WorkspaceRef)
	if err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return workspaceChatSelectedSettings{}, false
	}
	current := workspaceChatSelectedSettings{selection: selection, catalog: catalog}
	switch selection.Conversation.Kind {
	case ConversationKindDraft:
		draft, err := s.ReadDraft(context.Background(), clientID, selection.WorkspaceRef, selection.Conversation.ID)
		if err != nil {
			s.replyWorkspaceChatError(p, msg, err)
			return workspaceChatSelectedSettings{}, false
		}
		current.draft = &draft
		current.settings = nativeDraftSettings(catalog, draft.SettingsPatch)
	case ConversationKindThread:
		snapshot, err := s.ReadThread(context.Background(), selection.WorkspaceRef, selection.Conversation.ID)
		if err != nil {
			s.replyWorkspaceChatError(p, msg, err)
			return workspaceChatSelectedSettings{}, false
		}
		current.threadID = snapshot.Thread.ID
		current.settings = snapshot.Settings
	default:
		s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatNeedConversation))
		return workspaceChatSelectedSettings{}, false
	}
	return current, true
}

func workspaceChatSettingsMenuKind(category string, selection *WorkspaceChatSelection) string {
	return strings.Join([]string{"settings", category, selection.WorkspaceRef, string(selection.Conversation.Kind), selection.Conversation.ID}, ":")
}

func nativeDraftSettings(catalog NativeRuntimeCatalog, patch NativeThreadSettingsPatch) NativeThreadSettings {
	settings := defaultNativeSettings(catalog)
	if patch.Model != nil {
		settings.Model = *patch.Model
	}
	if patch.Effort != nil {
		settings.Effort = *patch.Effort
	}
	planEffort := patch.PlanEffort
	if patch.ServiceTier != nil {
		settings.ServiceTier = *patch.ServiceTier
	}
	if patch.Personality != nil {
		settings.Personality = *patch.Personality
	}
	if patch.Summary != nil {
		settings.Summary = *patch.Summary
	}
	if patch.PermissionProfile != nil {
		settings.PermissionProfile = *patch.PermissionProfile
	}
	if patch.Mode != nil {
		modeModel := settings.Model
		if option := findNativeModeOption(catalog.Modes, *patch.Mode); option != nil {
			if option.Model != nil && strings.TrimSpace(*option.Model) != "" {
				modeModel = strings.TrimSpace(*option.Model)
			}
			if planEffort == nil && option.ReasoningEffort != nil {
				value := strings.TrimSpace(*option.ReasoningEffort)
				planEffort = &value
			}
		}
		if planEffort == nil && settings.Effort != "" {
			value := settings.Effort
			planEffort = &value
		}
		settings.CollaborationMode = &NativeCollaborationMode{
			Mode: *patch.Mode,
			Settings: NativeCollaborationSettings{
				Model: modeModel, ReasoningEffort: planEffort,
			},
		}
	}
	return settings
}

func nativeSettingMenuOptions(category string, catalog NativeRuntimeCatalog, settings NativeThreadSettings) ([]string, []string) {
	var values, labels []string
	switch category {
	case "models":
		for _, option := range catalog.Models {
			if option.Hidden {
				continue
			}
			value := option.Model
			if value == "" {
				value = option.ID
			}
			label := option.DisplayName
			if label == "" {
				label = value
			}
			values, labels = append(values, value), append(labels, label)
		}
	case "efforts":
		modelName := settings.Model
		if settings.CollaborationMode != nil && settings.CollaborationMode.Mode == "plan" {
			modelName = settings.CollaborationMode.Settings.Model
		}
		if model := findNativeModel(catalog.Models, modelName); model != nil {
			for _, option := range model.ReasoningEfforts {
				values, labels = append(values, option.Effort), append(labels, option.Effort)
			}
		}
	case "modes":
		for _, option := range catalog.Modes {
			value := ""
			if option.Mode != nil {
				value = *option.Mode
			}
			label := option.Name
			if label == "" {
				label = value
			}
			if value != "" {
				values, labels = append(values, value), append(labels, label)
			}
		}
	case "permissions":
		for _, option := range catalog.Permissions {
			if option.Allowed {
				values, labels = append(values, option.ID), append(labels, option.ID)
			}
		}
	case "tiers":
		if model := findNativeModel(catalog.Models, settings.Model); model != nil {
			for _, option := range model.ServiceTiers {
				label := option.Name
				if label == "" {
					label = option.ID
				}
				values, labels = append(values, option.ID), append(labels, label)
			}
		}
	case "personalities":
		values, labels = append(values, catalog.Personalities...), append(labels, catalog.Personalities...)
	case "summaries":
		values, labels = append(values, catalog.Summaries...), append(labels, catalog.Summaries...)
	}
	return values, labels
}

func (s *WorkspaceChatService) handleWorkspaceOrdinaryMessage(p Platform, msg *Message, clientID string) {
	selection, err := s.Selection(context.Background(), clientID)
	if err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	if selection == nil {
		text, listErr := s.renderWorkspaceMenu(context.Background(), clientID, 1)
		if listErr != nil {
			s.replyWorkspaceChatError(p, msg, listErr)
			return
		}
		s.replyWorkspaceChat(p, msg, s.engine.i18n.Tf(MsgWorkspaceChatBindProjectFirst, text))
		return
	}
	workspace, err := s.resolveWorkspace(context.Background(), selection.WorkspaceRef)
	if err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	if selection.Conversation.Kind == ConversationKindThread {
		snapshot, err := s.ReadThread(context.Background(), selection.WorkspaceRef, selection.Conversation.ID)
		if err != nil {
			s.replyWorkspaceChatError(p, msg, err)
			return
		}
		if snapshot.ActiveTurn != nil {
			s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatTurnRunning))
			return
		}
	}
	inputs, err := nativeInputsFromPlatformMessage(workspace.RootPath, msg, msg.Content)
	if err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	requestID := workspaceMessageRequestID(msg)
	delivery := &workspaceChatDeliveryTarget{clientID: clientID, requestID: requestID, platform: p, replyCtx: msg.ReplyCtx, destination: msg.UserID}
	if _, err := s.startTrustedTurn(context.Background(), clientID, requestID, selection.WorkspaceRef, selection.Conversation, inputs, delivery); err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return
	}
	s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatTurnSubmitted))
}

func (s *WorkspaceChatService) selectedThread(p Platform, msg *Message, clientID string) (*WorkspaceChatSelection, NativeConversationSnapshot, bool) {
	selection, err := s.Selection(context.Background(), clientID)
	if err != nil || selection == nil {
		s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatNeedConversation))
		return nil, NativeConversationSnapshot{}, false
	}
	if selection.Conversation.Kind != ConversationKindThread {
		s.replyWorkspaceChat(p, msg, s.engine.i18n.T(MsgWorkspaceChatDraftUnavailable))
		return nil, NativeConversationSnapshot{}, false
	}
	snapshot, err := s.ReadThread(context.Background(), selection.WorkspaceRef, selection.Conversation.ID)
	if err != nil {
		s.replyWorkspaceChatError(p, msg, err)
		return nil, NativeConversationSnapshot{}, false
	}
	return selection, snapshot, true
}

func (s *WorkspaceChatService) SaveMenu(ctx context.Context, clientID, kind string, itemIDs []string) error {
	hash := sha256.Sum256([]byte(strings.Join(itemIDs, "\x00")))
	return s.repo.PutMenu(ctx, WorkspaceChatMenuSnapshot{
		ClientID: clientID, Kind: kind, Revision: hex.EncodeToString(hash[:]),
		ItemIDs: append([]string(nil), itemIDs...), UpdatedAt: time.Now(),
	})
}

func (s *WorkspaceChatService) MenuItem(ctx context.Context, clientID, kind string, index int) (string, error) {
	if index < 1 {
		return "", errWorkspaceMenuInvalidIndex
	}
	snapshot, err := s.repo.GetMenu(ctx, clientID, kind)
	if err != nil {
		return "", err
	}
	if snapshot == nil || index > len(snapshot.ItemIDs) {
		return "", errWorkspaceMenuInvalidIndex
	}
	item := strings.TrimSpace(snapshot.ItemIDs[index-1])
	if item == "" {
		return "", errWorkspaceMenuItemNotFound
	}
	return item, nil
}

func (s *WorkspaceChatService) renderWorkspaceMenu(ctx context.Context, clientID string, page int) (string, error) {
	workspaces, err := s.ListWorkspaces(ctx)
	if err != nil {
		return "", err
	}
	start, end, err := workspaceMenuPage(len(workspaces), page)
	if err != nil {
		return "", err
	}
	ids := make([]string, 0, end-start)
	lines := []string{s.engine.i18n.Tf(MsgWorkspaceChatProjectsTitle, page)}
	if len(workspaces) == 0 {
		lines = append(lines, s.engine.i18n.T(MsgWorkspaceChatProjectsEmpty))
	}
	for i, workspace := range workspaces[start:end] {
		ids = append(ids, workspace.Ref)
		line := fmt.Sprintf("%d. %s", i+1, workspaceDisplayName(workspace))
		if !workspace.Available {
			line += s.engine.i18n.Tf(MsgWorkspaceChatUnavailableReason, workspace.Error)
		}
		lines = append(lines, line)
	}
	if err := s.SaveMenu(ctx, clientID, "projects", ids); err != nil {
		return "", err
	}
	lines = append(lines, s.engine.i18n.T(MsgWorkspaceChatProjectsHint))
	return strings.Join(lines, "\n"), nil
}

func (s *WorkspaceChatService) renderThreadMenu(ctx context.Context, clientID string, page int) (string, error) {
	selection, err := s.Selection(ctx, clientID)
	if err != nil || selection == nil {
		return s.engine.i18n.T(MsgWorkspaceChatNoSelection), err
	}
	var cursor string
	var result NativeThreadPage
	for current := 1; current <= page; current++ {
		result, err = s.ListThreads(ctx, selection.WorkspaceRef, NativePageRequest{Cursor: cursor, Limit: workspaceChatMenuPageSize, SortDirection: "desc"})
		if err != nil {
			return "", err
		}
		if current < page {
			if result.NextCursor == "" {
				return "", errWorkspacePageOutOfRange
			}
			cursor = result.NextCursor
		}
	}
	if len(result.Data) == 0 {
		return s.engine.i18n.T(MsgWorkspaceChatThreadsEmpty), nil
	}
	ids := make([]string, 0, len(result.Data))
	lines := []string{s.engine.i18n.Tf(MsgWorkspaceChatThreadsTitle, page)}
	for i, thread := range result.Data {
		ids = append(ids, thread.ID)
		lines = append(lines, fmt.Sprintf("%d. %s", i+1, s.threadDisplayName(thread)))
	}
	if err := s.SaveMenu(ctx, clientID, "threads", ids); err != nil {
		return "", err
	}
	lines = append(lines, s.engine.i18n.T(MsgWorkspaceChatThreadsHint))
	return strings.Join(lines, "\n"), nil
}

func splitWorkspaceChatCommand(content string) (string, string) {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "/") {
		return "", ""
	}
	parts := strings.SplitN(content, " ", 2)
	command := strings.ToLower(parts[0])
	if len(parts) == 1 {
		return command, ""
	}
	return command, strings.TrimSpace(parts[1])
}

func parseWorkspaceChatPositiveInt(raw string, defaultValue int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if defaultValue > 0 {
			return defaultValue, nil
		}
		return 0, errWorkspacePositiveNumber
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 0, errWorkspacePositiveNumber
	}
	return value, nil
}

func workspaceMenuPage(total, page int) (int, int, error) {
	if page < 1 {
		return 0, 0, errWorkspacePageOutOfRange
	}
	start := (page - 1) * workspaceChatMenuPageSize
	if total == 0 {
		if page == 1 {
			return 0, 0, nil
		}
		return 0, 0, errWorkspacePageOutOfRange
	}
	if start >= total {
		return 0, 0, errWorkspacePageOutOfRange
	}
	end := start + workspaceChatMenuPageSize
	if end > total {
		end = total
	}
	return start, end, nil
}

func workspaceDisplayName(workspace Workspace) string {
	if workspace.RootName != "" && workspace.RootName != workspace.ProjectName {
		return workspace.ProjectName + " / " + workspace.RootName
	}
	if workspace.ProjectName != "" {
		return workspace.ProjectName
	}
	return filepath.Base(workspace.RootPath)
}

func (s *WorkspaceChatService) threadDisplayName(thread NativeThread) string {
	for _, value := range []string{thread.Name, thread.Preview, thread.ID} {
		if value = strings.TrimSpace(value); value != "" {
			return truncateWorkspaceChatText(value, 64)
		}
	}
	return s.engine.i18n.T(MsgUntitled)
}

func truncateWorkspaceChatText(value string, maxRunes int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= maxRunes {
		return string(runes)
	}
	return string(runes[:maxRunes]) + "..."
}

func nativeInputsFromPlatformMessage(workspaceRoot string, message *Message, text string) ([]NativeUserInput, error) {
	inputs := make([]NativeUserInput, 0, 1+len(message.Images)+len(message.Files))
	content := strings.TrimSpace(strings.Join([]string{message.ExtraContent, text}, "\n"))
	if content != "" {
		inputs = append(inputs, NativeUserInput{Type: "text", Text: content})
	}
	files := make([]FileAttachment, 0, len(message.Images)+len(message.Files)+1)
	for _, image := range message.Images {
		files = append(files, FileAttachment(image))
	}
	files = append(files, message.Files...)
	if message.Audio != nil {
		name := "audio"
		if message.Audio.Format != "" {
			name += "." + message.Audio.Format
		}
		files = append(files, FileAttachment{MimeType: message.Audio.MimeType, Data: message.Audio.Data, FileName: name})
	}
	paths := SaveFilesToDisk(workspaceRoot, workspaceMessageRequestID(message), files)
	if len(paths) != len(files) {
		return nil, errWorkspaceChatAttachmentSaveFailed
	}
	index := 0
	for _, image := range message.Images {
		inputs = append(inputs, NativeUserInput{Type: "image", LocalPath: paths[index], MimeType: image.MimeType, FileName: image.FileName})
		index++
	}
	for _, file := range message.Files {
		inputs = append(inputs, NativeUserInput{Type: "file", LocalPath: paths[index], MimeType: file.MimeType, FileName: file.FileName})
		index++
	}
	if message.Audio != nil {
		inputs = append(inputs, NativeUserInput{Type: "audio", LocalPath: paths[index], MimeType: message.Audio.MimeType, FileName: filepath.Base(paths[index])})
	}
	if len(inputs) == 0 {
		return nil, errWorkspaceChatEmptyMessage
	}
	return inputs, nil
}

func workspaceMessageRequestID(message *Message) string {
	if value := strings.TrimSpace(message.MessageID); value != "" {
		return value
	}
	return newWorkspaceChatID("wecom")
}

func (s *WorkspaceChatService) nativeHistoryItem(raw json.RawMessage) (string, string) {
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return "", ""
	}
	role := strings.ToLower(asString(value["role"]))
	typeName := strings.ToLower(asString(value["type"]))
	if role == "user" || strings.Contains(typeName, "usermessage") {
		return s.engine.i18n.T(MsgWorkspaceChatRoleUser), flattenText(value["content"])
	}
	if text := findAssistantText(value); text != "" {
		return s.engine.i18n.T(MsgWorkspaceChatRoleAssistant), text
	}
	return "", ""
}

func workspaceChatDecisionResponse(interaction NativeInteraction, raw string) (json.RawMessage, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "{") {
		var object map[string]json.RawMessage
		if json.Unmarshal([]byte(raw), &object) != nil || object == nil {
			return nil, fmt.Errorf("workspace chat: interaction response must be a JSON object")
		}
		candidate := json.RawMessage(raw)
		if len(interaction.AllowedDecisions) > 0 && nativeDecisionValueAllowed(interaction.AllowedDecisions, candidate) {
			response, err := json.Marshal(map[string]json.RawMessage{"decision": candidate})
			return response, err
		}
		return candidate, nil
	}
	if len(strings.Fields(raw)) != 1 {
		return nil, fmt.Errorf("workspace chat: interaction decision must be one declared value or a JSON object")
	}
	if interaction.Kind == "mcpServer/elicitation/request" {
		if raw == "accept" {
			return nil, fmt.Errorf("workspace chat: MCP accept requires JSON content")
		}
		response, err := json.Marshal(map[string]any{"action": raw, "content": nil})
		return response, err
	}
	response, err := json.Marshal(map[string]string{"decision": raw})
	return response, err
}

func workspaceChatAnswerResponse(interaction NativeInteraction, raw string) (json.RawMessage, error) {
	questionIDs := workspaceChatQuestionIDs(interaction.Payload)
	if len(questionIDs) == 0 {
		return nil, fmt.Errorf("workspace chat: structured question has no question IDs")
	}
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "{") {
		if len(questionIDs) != 1 {
			return nil, fmt.Errorf("workspace chat: multiple questions require a JSON answer map")
		}
		response, err := json.Marshal(map[string]any{
			"answers": map[string]any{questionIDs[0]: map[string]any{"answers": []string{raw}}},
		})
		return response, err
	}
	var object map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &object) != nil || object == nil {
		return nil, fmt.Errorf("workspace chat: structured answers must be a JSON object")
	}
	if _, fullResponse := object["answers"]; fullResponse {
		return json.RawMessage(raw), nil
	}
	allowed := make(map[string]struct{}, len(questionIDs))
	for _, id := range questionIDs {
		allowed[id] = struct{}{}
	}
	answers := make(map[string]any, len(object))
	for id, value := range object {
		if _, ok := allowed[id]; !ok {
			return nil, fmt.Errorf("workspace chat: answer references unknown question %q", id)
		}
		var values []string
		var single string
		switch {
		case json.Unmarshal(value, &single) == nil:
			values = []string{single}
		case json.Unmarshal(value, &values) == nil:
		default:
			return nil, fmt.Errorf("workspace chat: answer for question %q must be text or a text array", id)
		}
		answers[id] = map[string]any{"answers": values}
	}
	if len(answers) != len(questionIDs) {
		return nil, fmt.Errorf("workspace chat: every structured question must be answered")
	}
	response, err := json.Marshal(map[string]any{"answers": answers})
	return response, err
}

func workspaceChatQuestionIDs(payload json.RawMessage) []string {
	var request struct {
		Questions []struct {
			ID string `json:"id"`
		} `json:"questions"`
	}
	if json.Unmarshal(payload, &request) != nil {
		return nil
	}
	ids := make([]string, 0, len(request.Questions))
	for _, question := range request.Questions {
		if id := strings.TrimSpace(question.ID); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func workspaceChatInteractionDecisions(interaction NativeInteraction) string {
	values := make([]string, 0, len(interaction.AllowedDecisions))
	for _, decision := range interaction.AllowedDecisions {
		if len(decision) > 0 && json.Valid(decision) {
			values = append(values, string(decision))
		}
	}
	return truncateWorkspaceChatText(strings.Join(values, ", "), 500)
}

func workspaceChatInteractionDetail(interaction NativeInteraction) string {
	if interaction.Kind == "item/tool/requestUserInput" {
		var request struct {
			Questions []struct {
				ID       string `json:"id"`
				Header   string `json:"header"`
				Question string `json:"question"`
				Options  []struct {
					Label string `json:"label"`
				} `json:"options"`
			} `json:"questions"`
		}
		if json.Unmarshal(interaction.Payload, &request) == nil {
			lines := make([]string, 0, len(request.Questions))
			for _, question := range request.Questions {
				prompt := strings.TrimSpace(strings.Join([]string{question.Header, question.Question}, " "))
				labels := make([]string, 0, len(question.Options))
				for _, option := range question.Options {
					if label := strings.TrimSpace(option.Label); label != "" {
						labels = append(labels, label)
					}
				}
				if len(labels) > 0 {
					prompt += " [" + strings.Join(labels, ", ") + "]"
				}
				lines = append(lines, "\n   "+question.ID+": "+truncateWorkspaceChatText(prompt, 300))
			}
			return strings.Join(lines, "")
		}
	}
	var payload any
	if json.Unmarshal(interaction.Payload, &payload) != nil {
		return ""
	}
	for _, key := range []string{"message", "reason", "question"} {
		if detail := strings.TrimSpace(findJSONString(payload, key)); detail != "" {
			return "\n   " + truncateWorkspaceChatText(detail, 300)
		}
	}
	return ""
}

func (s *WorkspaceChatService) replyWorkspaceChat(platform Platform, message *Message, content string) {
	if err := platform.Reply(s.ctx, message.ReplyCtx, content); err != nil {
		slog.Error("workspace chat reply failed", "platform", platform.Name(), "error", err)
	}
}

func (s *WorkspaceChatService) replyWorkspaceChatError(platform Platform, message *Message, err error) {
	switch {
	case errors.Is(err, errWorkspaceMenuInvalidIndex):
		s.replyWorkspaceChat(platform, message, s.engine.i18n.T(MsgWorkspaceChatInvalidIndex))
	case errors.Is(err, errWorkspacePositiveNumber):
		s.replyWorkspaceChat(platform, message, s.engine.i18n.T(MsgWorkspaceChatInvalidNumber))
	case errors.Is(err, errWorkspacePageOutOfRange):
		s.replyWorkspaceChat(platform, message, s.engine.i18n.T(MsgWorkspaceChatPageOutOfRange))
	case errors.Is(err, errWorkspaceMenuItemNotFound):
		s.replyWorkspaceChat(platform, message, s.engine.i18n.T(MsgWorkspaceChatMenuExpired))
	case errors.Is(err, errWorkspaceChatAttachmentSaveFailed):
		s.replyWorkspaceChat(platform, message, s.engine.i18n.T(MsgWorkspaceChatAttachmentSaveFailed))
	case errors.Is(err, errWorkspaceChatEmptyMessage):
		s.replyWorkspaceChat(platform, message, s.engine.i18n.T(MsgWorkspaceChatEmptyMessage))
	default:
		s.replyWorkspaceChat(platform, message, s.engine.i18n.Tf(MsgError, err))
	}
}
