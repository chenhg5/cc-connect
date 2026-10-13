package core

import (
	"fmt"
	"strings"
	"sync"
)

// Language represents a supported language
type Language string

const (
	MsgModelRepositoryPolicyDenied MsgKey = "MsgModelRepositoryPolicyDenied"
	MsgTeamRoleCreationContext     MsgKey = "MsgTeamRoleCreationContext"
	MsgTeamRoleAuthorize           MsgKey = "MsgTeamRoleAuthorize"
	MsgTeamRoleReasonRequired      MsgKey = "MsgTeamRoleReasonRequired"
	MsgTeamRoleProvisionFailed     MsgKey = "MsgTeamRoleProvisionFailed"
	MsgTeamRoleMembershipFailed    MsgKey = "MsgTeamRoleMembershipFailed"
	MsgRolePermissionsApplyFailed  MsgKey = "MsgRolePermissionsApplyFailed"
	MsgRolePermissionsReady        MsgKey = "MsgRolePermissionsReady"
	MsgRolePermissionsPending      MsgKey = "MsgRolePermissionsPending"
	MsgRolePermissionsSubmitted    MsgKey = "MsgRolePermissionsSubmitted"
	MsgRolePermissionsUnknown      MsgKey = "MsgRolePermissionsUnknown"
	MsgRolePermissionsUser         MsgKey = "MsgRolePermissionsUser"
	MsgRolePermissionsUsage        MsgKey = "MsgRolePermissionsUsage"
	MsgRolePermissionsCreationHint MsgKey = "MsgRolePermissionsCreationHint"

	MsgLabelTitle        MsgKey = "MsgLabelTitle"
	MsgLabelHint         MsgKey = "MsgLabelHint"
	MsgLabelNew          MsgKey = "MsgLabelNew"
	MsgLabelNewHint      MsgKey = "MsgLabelNewHint"
	MsgLabelAuthNeeded   MsgKey = "MsgLabelAuthNeeded"
	MsgLabelWrongUser    MsgKey = "MsgLabelWrongUser"
	MsgLabelBusy         MsgKey = "MsgLabelBusy"
	MsgLabelFailed       MsgKey = "MsgLabelFailed"
	MsgLabelUnsupported  MsgKey = "MsgLabelUnsupported"
	MsgLabelPrivate      MsgKey = "MsgLabelPrivate"
	MsgLabelAuthorize    MsgKey = "MsgLabelAuthorize"
	MsgLabelAuthorized   MsgKey = "MsgLabelAuthorized"
	MsgLabelUsage        MsgKey = "MsgLabelUsage"
	MsgLabelAuthReady    MsgKey = "MsgLabelAuthReady"
	MsgLabelAuthButton   MsgKey = "MsgLabelAuthButton"
	MsgLabelRetry        MsgKey = "MsgLabelRetry"
	MsgLabelPending      MsgKey = "MsgLabelPending"
	MsgLabelSave         MsgKey = "MsgLabelSave"
	MsgLabelBots         MsgKey = "MsgLabelBots"
	MsgLabelProjects     MsgKey = "MsgLabelProjects"
	MsgLabelSaved        MsgKey = "MsgLabelSaved"
	MsgLabelSelection    MsgKey = "MsgLabelSelection"
	MsgLabelCreationHint MsgKey = "MsgLabelCreationHint"
	MsgLabelWelcome      MsgKey = "MsgLabelWelcome"
	MsgLabelApplied      MsgKey = "MsgLabelApplied"
	MsgLabelText         MsgKey = "MsgLabelText"
)

const (
	MsgRoundIdle                     MsgKey = "MsgRoundIdle"
	MsgRoundEnding                   MsgKey = "MsgRoundEnding"
	MsgRoundEnded                    MsgKey = "MsgRoundEnded"
	MsgRoundArchived                 MsgKey = "MsgRoundArchived"
	MsgRoundStopFailed               MsgKey = "MsgRoundStopFailed"
	MsgRoundRetry                    MsgKey = "MsgRoundRetry"
	MsgRoundCurrent                  MsgKey = "MsgRoundCurrent"
	MsgDashboardRefresh              MsgKey = "MsgDashboardRefresh"
	MsgDashboardHistory              MsgKey = "MsgDashboardHistory"
	MsgDashboardCurrent              MsgKey = "MsgDashboardCurrent"
	MsgDashboardNew                  MsgKey = "MsgDashboardNew"
	MsgDashboardUpdated              MsgKey = "MsgDashboardUpdated"
	MsgDashboardFailed               MsgKey = "MsgDashboardFailed"
	MsgDashboardCounts               MsgKey = "MsgDashboardCounts"
	MsgDashboardPage                 MsgKey = "MsgDashboardPage"
	MsgDashboardPending              MsgKey = "MsgDashboardPending"
	MsgDashboardHistoryEntry         MsgKey = "MsgDashboardHistoryEntry"
	MsgDashboardNoHistory            MsgKey = "MsgDashboardNoHistory"
	MsgDashboardPrevious             MsgKey = "MsgDashboardPrevious"
	MsgDashboardNext                 MsgKey = "MsgDashboardNext"
	MsgDashboardRoundRecords         MsgKey = "MsgDashboardRoundRecords"
	MsgDashboardTopic                MsgKey = "MsgDashboardTopic"
	MsgDashboardLinkFailed           MsgKey = "MsgDashboardLinkFailed"
	MsgRoundPause                    MsgKey = "MsgRoundPause"
	MsgRoundResume                   MsgKey = "MsgRoundResume"
	MsgWorkflowBoard                 MsgKey = "MsgWorkflowBoard"
	MsgWorkflowSummary               MsgKey = "MsgWorkflowSummary"
	MsgWorkflowTask                  MsgKey = "MsgWorkflowTask"
	MsgProjectFinish                 MsgKey = "MsgProjectFinish"
	MsgProjectStatusEnded            MsgKey = "MsgProjectStatusEnded"
	MsgProjectEnded                  MsgKey = "MsgProjectEnded"
	MsgProjectFinishPreview          MsgKey = "MsgProjectFinishPreview"
	MsgProjectFinishConsequences     MsgKey = "MsgProjectFinishConsequences"
	MsgProjectFinishConfirm          MsgKey = "MsgProjectFinishConfirm"
	MsgProjectFinishCancel           MsgKey = "MsgProjectFinishCancel"
	MsgProjectFinishChanged          MsgKey = "MsgProjectFinishChanged"
	MsgProjectFinishTextCommand      MsgKey = "MsgProjectFinishTextCommand"
	MsgProjectEndedAt                MsgKey = "MsgProjectEndedAt"
	MsgWorkflowHelp                  MsgKey = "MsgWorkflowHelp"
	MsgWorkflowPaused                MsgKey = "MsgWorkflowPaused"
	MsgWorkflowResumed               MsgKey = "MsgWorkflowResumed"
	MsgWorkflowProgress              MsgKey = "MsgWorkflowProgress"
	MsgWorkflowMissing               MsgKey = "MsgWorkflowMissing"
	MsgWorkflowLogs                  MsgKey = "MsgWorkflowLogs"
	MsgWorkflowEmpty                 MsgKey = "MsgWorkflowEmpty"
	MsgWorkflowCurrent               MsgKey = "MsgWorkflowCurrent"
	MsgWorkflowSaved                 MsgKey = "MsgWorkflowSaved"
	MsgWorkflowSubmitted             MsgKey = "MsgWorkflowSubmitted"
	MsgWorkflowThread                MsgKey = "MsgWorkflowThread"
	MsgWorkflowStatusActive          MsgKey = "MsgWorkflowStatusActive"
	MsgWorkflowStatusPaused          MsgKey = "MsgWorkflowStatusPaused"
	MsgWorkflowStatusPlanned         MsgKey = "MsgWorkflowStatusPlanned"
	MsgWorkflowStatusCancelled       MsgKey = "MsgWorkflowStatusCancelled"
	MsgWorkflowStatusQueued          MsgKey = "MsgWorkflowStatusQueued"
	MsgWorkflowStatusRunning         MsgKey = "MsgWorkflowStatusRunning"
	MsgWorkflowStatusWaitingApproval MsgKey = "MsgWorkflowStatusWaitingApproval"
	MsgWorkflowStatusWaitingInput    MsgKey = "MsgWorkflowStatusWaitingInput"
	MsgWorkflowStatusSubmitted       MsgKey = "MsgWorkflowStatusSubmitted"
	MsgWorkflowStatusAccepted        MsgKey = "MsgWorkflowStatusAccepted"
	MsgWorkflowStatusFailed          MsgKey = "MsgWorkflowStatusFailed"
	MsgWorkflowStatusBlocked         MsgKey = "MsgWorkflowStatusBlocked"
	MsgWorkflowStatusStopped         MsgKey = "MsgWorkflowStatusStopped"
	MsgWorkflowStatusInterrupted     MsgKey = "MsgWorkflowStatusInterrupted"
)

const MsgProjectCallbackUnsupported MsgKey = "project_callback_unsupported"

const (
	LangAuto               Language = "" // auto-detect from user messages
	LangEnglish            Language = "en"
	LangChinese            Language = "zh"
	LangTraditionalChinese Language = "zh-TW"
	LangJapanese           Language = "ja"
	LangSpanish            Language = "es"
)

// I18n provides internationalized messages.
//
// All exported methods are safe to call from multiple goroutines: cc-connect
// fans out platform message handlers concurrently, all of which can call
// DetectAndSet (writes `detected`) and T / CurrentLang (read `lang`/`detected`)
// at the same time. Without the mutex `go test -race` flags real data races
// on the language fields.
type I18n struct {
	mu       sync.RWMutex
	lang     Language
	detected Language
	saveFunc func(Language) error
}

func NewI18n(lang Language) *I18n {
	return &I18n{lang: lang}
}

func (i *I18n) SetSaveFunc(fn func(Language) error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.saveFunc = fn
}

func DetectLanguage(text string) Language {
	for _, r := range text {
		if isJapanese(r) {
			return LangJapanese
		}
	}
	for _, r := range text {
		if isChinese(r) {
			return LangChinese
		}
	}
	if isSpanishHint(text) {
		return LangSpanish
	}
	return LangEnglish
}

// NormalizeLanguageString parses a configuration string (e.g. from
// opts["language"] or [projects].language) into a Language constant. The
// accepted spellings mirror the ones documented in config.example.toml:
// "en"/"english", "zh"/"chinese", "zh-TW"/"zh_TW"/"zhtw", "ja"/"japanese",
// "es"/"spanish", and "" / "auto" for auto-detect. Unknown values return
// LangAuto so the engine falls back to detection rather than silently
// snapping to English — operators who set language = "klingon" should see
// "auto" behaviour, not a hard English default they didn't ask for.
//
// Issue #1655 introduced this helper so the Claude Code agent (which
// receives the language via opts["language"]) can decode the string
// without duplicating the switch statement that cmd/cc-connect/main.go
// uses for engine construction.
func NormalizeLanguageString(s string) Language {
	switch strings.ToLower(s) {
	case "en", "english":
		return LangEnglish
	case "zh", "chinese":
		return LangChinese
	case "zh-tw", "zh_tw", "zhtw":
		return LangTraditionalChinese
	case "ja", "japanese":
		return LangJapanese
	case "es", "spanish":
		return LangSpanish
	case "", "auto":
		return LangAuto
	default:
		return LangAuto
	}
}

func isChinese(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) ||
		(r >= 0x3400 && r <= 0x4DBF) ||
		(r >= 0x20000 && r <= 0x2A6DF) ||
		(r >= 0x2A700 && r <= 0x2B73F) ||
		(r >= 0x2B740 && r <= 0x2B81F) ||
		(r >= 0x2B820 && r <= 0x2CEAF) ||
		(r >= 0xF900 && r <= 0xFAFF) ||
		(r >= 0x2F800 && r <= 0x2FA1F)
}

func isJapanese(r rune) bool {
	return (r >= 0x3040 && r <= 0x309F) || // Hiragana
		(r >= 0x30A0 && r <= 0x30FF) || // Katakana
		(r >= 0x31F0 && r <= 0x31FF) || // Katakana Phonetic Extensions
		(r >= 0xFF65 && r <= 0xFF9F) // Half-width Katakana
}

// isSpanishHint checks for characters common in Spanish but not English (ñ, ¿, ¡, accented vowels).
func isSpanishHint(text string) bool {
	for _, r := range text {
		switch r {
		case 'ñ', 'Ñ', '¿', '¡', 'á', 'é', 'í', 'ó', 'ú', 'ü':
			return true
		}
	}
	return false
}

func (i *I18n) DetectAndSet(text string) {
	i.mu.RLock()
	if i.lang != LangAuto {
		i.mu.RUnlock()
		return
	}
	currentDetected := i.detected
	i.mu.RUnlock()

	detected := DetectLanguage(text)
	if currentDetected == detected {
		return
	}

	i.mu.Lock()
	// Re-check under the write lock — another goroutine may have updated
	// i.detected between our RUnlock above and the Lock here.
	if i.lang != LangAuto || i.detected == detected {
		i.mu.Unlock()
		return
	}
	i.detected = detected
	saveFunc := i.saveFunc
	i.mu.Unlock()

	if saveFunc != nil {
		if err := saveFunc(detected); err != nil {
			fmt.Printf("failed to save language: %v\n", err)
		}
	}
}

func (i *I18n) currentLang() Language {
	// Caller holds either no lock (most public methods take RLock and call
	// this) or RLock; this helper just reads the protected fields. All
	// public methods that call currentLang() acquire i.mu.RLock first.
	if i.lang == LangAuto {
		if i.detected != "" {
			return i.detected
		}
		return LangEnglish
	}
	return i.lang
}

// CurrentLang returns the resolved language (exported for mode display).
func (i *I18n) CurrentLang() Language {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.currentLang()
}

// IsZhLike returns true for Simplified and Traditional Chinese.
func (i *I18n) IsZhLike() bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	l := i.currentLang()
	return l == LangChinese || l == LangTraditionalChinese
}

// SetLang overrides the language (disabling auto-detect).
func (i *I18n) SetLang(lang Language) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.lang = lang
	i.detected = ""
}

// Message keys
type MsgKey string

const (
	MsgStarting                  MsgKey = "starting"
	MsgQueryDone                 MsgKey = "query_done"
	MsgQueryFailed               MsgKey = "query_failed"
	MsgQueryLatestPage           MsgKey = "query_latest_page"
	MsgQueryProcess              MsgKey = "query_process"
	MsgQueryAnswer               MsgKey = "query_answer"
	MsgQueryPageExpired          MsgKey = "query_page_expired"
	MsgQueryPageFailed           MsgKey = "query_page_failed"
	MsgQueryPageBusy             MsgKey = "query_page_busy"
	MsgQueryRestored             MsgKey = "query_restored"
	MsgQuerySuperseded           MsgKey = "query_superseded"
	MsgDecisionDeadline          MsgKey = "decision_deadline"
	MsgWorkflowReworkInvalid     MsgKey = "workflow_rework_invalid"
	MsgWorkflowReworkTitle       MsgKey = "workflow_rework_title"
	MsgWorkflowReworkInGroup     MsgKey = "workflow_rework_in_group"
	MsgWorkflowResultFile        MsgKey = "workflow_result_file"
	MsgDecisionAnswer            MsgKey = "decision_answer"
	MsgDecisionSubmit            MsgKey = "decision_submit"
	MsgDecisionExpired           MsgKey = "decision_expired"
	MsgExecEmptyAnswer           MsgKey = "exec_empty_answer"
	MsgExecHistory               MsgKey = "exec_history"
	MsgExecReadArchive           MsgKey = "exec_read_archive"
	MsgExecArchived              MsgKey = "exec_archived"
	MsgExecRunning               MsgKey = "exec_running"
	MsgExecDone                  MsgKey = "exec_done"
	MsgExecFailed                MsgKey = "exec_failed"
	MsgExecStopped               MsgKey = "exec_stopped"
	MsgExecExitCode              MsgKey = "exec_exit_code"
	MsgExecFullExplanation       MsgKey = "exec_full_explanation"
	MsgExecBack                  MsgKey = "exec_back"
	MsgExecPrevious              MsgKey = "exec_previous"
	MsgExecNext                  MsgKey = "exec_next"
	MsgExecRefreshCurrent        MsgKey = "exec_refresh_current"
	MsgExecRefresh               MsgKey = "exec_refresh"
	MsgExecNoOutput              MsgKey = "exec_no_output"
	MsgExecNeedsInput            MsgKey = "exec_needs_input"
	MsgExecNeedsApproval         MsgKey = "exec_needs_approval"
	MsgExecReminderFailed        MsgKey = "exec_reminder_failed"
	MsgExecRetryReminder         MsgKey = "exec_retry_reminder"
	MsgExecOpenOriginal          MsgKey = "exec_open_original"
	MsgExecSummaryHint           MsgKey = "exec_summary_hint"
	MsgExecCommandLabel          MsgKey = "exec_command_label"
	MsgExecOutputLabel           MsgKey = "exec_output_label"
	MsgNativeExecutionFile       MsgKey = "native_execution_file"
	MsgNativeExecutionBuffering  MsgKey = "native_execution_buffering"
	MsgQueryDeliveryIncomplete   MsgKey = "query_delivery_incomplete"
	MsgThinking                  MsgKey = "thinking"
	MsgTool                      MsgKey = "tool"
	MsgToolResult                MsgKey = "tool_result"
	MsgToolResultFmtStatus       MsgKey = "tool_result_fmt_status"
	MsgToolResultFmtExit         MsgKey = "tool_result_fmt_exit"
	MsgToolResultFmtNoOutput     MsgKey = "tool_result_fmt_no_output"
	MsgToolResultFmtOk           MsgKey = "tool_result_fmt_ok"
	MsgToolResultFmtFailed       MsgKey = "tool_result_fmt_failed"
	MsgExecutionStopped          MsgKey = "execution_stopped"
	MsgSessionCloseFailed        MsgKey = "session_close_failed"
	MsgSessionResumeUnsafe       MsgKey = "session_resume_unsafe"
	MsgSessionCancelled          MsgKey = "session_cancelled"
	MsgNoExecution               MsgKey = "no_execution"
	MsgPreviousProcessing        MsgKey = "previous_processing"
	MsgQueueFull                 MsgKey = "queue_full"
	MsgMessageQueued             MsgKey = "message_queued"
	MsgNoToolsAllowed            MsgKey = "no_tools_allowed"
	MsgCurrentTools              MsgKey = "current_tools"
	MsgCurrentSession            MsgKey = "current_session"
	MsgToolAuthNotSupported      MsgKey = "tool_auth_not_supported"
	MsgToolAllowFailed           MsgKey = "tool_allow_failed"
	MsgToolAllowedNew            MsgKey = "tool_allowed_new"
	MsgProjectUsage              MsgKey = "project_usage"
	MsgProjectSelected           MsgKey = "project_selected"
	MsgProjectRequired           MsgKey = "project_required"
	MsgProjectCleared            MsgKey = "project_cleared"
	MsgError                     MsgKey = "error"
	MsgSessionNotFound           MsgKey = "session_not_found"
	MsgFailedToStartAgentSession MsgKey = "failed_to_start_agent_session"
	MsgFailedToDeleteSession     MsgKey = "failed_to_delete_session"
	MsgEmptyResponse             MsgKey = "empty_response"
	MsgPermissionPrompt          MsgKey = "permission_prompt"
	MsgPermissionAllowed         MsgKey = "permission_allowed"
	MsgPermissionApproveAll      MsgKey = "permission_approve_all"
	MsgPermissionDenied          MsgKey = "permission_denied_msg"
	MsgPermissionHint            MsgKey = "permission_hint"
	MsgQuietOn                   MsgKey = "quiet_on"
	MsgQuietOff                  MsgKey = "quiet_off"
	MsgDisplayModeCompact        MsgKey = "display_mode_compact"
	MsgQuietGlobalOn             MsgKey = "quiet_global_on"
	MsgQuietGlobalOff            MsgKey = "quiet_global_off"
	MsgModeChanged               MsgKey = "mode_changed"
	MsgModeNotSupported          MsgKey = "mode_not_supported"
	MsgSessionRestarting         MsgKey = "session_restarting"
	MsgSessionNotStarted         MsgKey = "session_not_started"
	MsgUntitled                  MsgKey = "untitled"
	MsgLangChanged               MsgKey = "lang_changed"
	MsgLangInvalid               MsgKey = "lang_invalid"
	MsgLangCurrent               MsgKey = "lang_current"
	MsgUnknownCommand            MsgKey = "unknown_command"
	MsgWelcome                   MsgKey = "welcome"
	MsgHelp                      MsgKey = "message_help" // change from "help", which is used now for builtin command help
	MsgHelpTitle                 MsgKey = "help_title"
	MsgHelpSessionSection        MsgKey = "help_session_section"
	MsgHelpAgentSection          MsgKey = "help_agent_section"
	MsgHelpToolsSection          MsgKey = "help_tools_section"
	MsgHelpSystemSection         MsgKey = "help_system_section"
	MsgHelpTip                   MsgKey = "help_tip"
	MsgListTitle                 MsgKey = "list_title"
	MsgListTitlePaged            MsgKey = "list_title_paged"
	MsgListEmpty                 MsgKey = "list_empty"
	MsgListMore                  MsgKey = "list_more"
	MsgListPageHint              MsgKey = "list_page_hint"
	MsgListSwitchHint            MsgKey = "list_switch_hint"
	MsgListError                 MsgKey = "list_error"
	MsgHistoryEmpty              MsgKey = "history_empty"
	MsgNameUsage                 MsgKey = "name_usage"
	MsgNameSet                   MsgKey = "name_set"
	MsgNameNoSession             MsgKey = "name_no_session"
	MsgProviderNotSupported      MsgKey = "provider_not_supported"
	MsgProviderNone              MsgKey = "provider_none"
	MsgProviderCurrent           MsgKey = "provider_current"
	MsgProviderListTitle         MsgKey = "provider_list_title"
	MsgProviderListEmpty         MsgKey = "provider_list_empty"
	MsgProviderSwitchHint        MsgKey = "provider_switch_hint"
	MsgProviderNotFound          MsgKey = "provider_not_found"
	MsgProviderSwitched          MsgKey = "provider_switched"
	MsgProviderCleared           MsgKey = "provider_cleared"
	MsgProviderAdded             MsgKey = "provider_added"
	MsgProviderAddUsage          MsgKey = "provider_add_usage"
	MsgProviderAddFailed         MsgKey = "provider_add_failed"
	MsgProviderRemoved           MsgKey = "provider_removed"
	MsgProviderRemoveFailed      MsgKey = "provider_remove_failed"
	MsgCardTitleProviderAdd      MsgKey = "card_title_provider_add"
	MsgProviderAddPickHint       MsgKey = "provider_add_pick_hint"
	MsgProviderAddOther          MsgKey = "provider_add_other"
	MsgProviderAddApiKeyPrompt   MsgKey = "provider_add_api_key_prompt"
	MsgProviderAddInviteHint     MsgKey = "provider_add_invite_hint"
	MsgProviderLinkGlobal        MsgKey = "provider_link_global"
	MsgProviderLinked            MsgKey = "provider_linked"

	MsgVoiceNotEnabled               MsgKey = "voice_not_enabled"
	MsgVoiceUsingPlatformRecognition MsgKey = "voice_using_platform_recognition"
	MsgVoiceNoFFmpeg                 MsgKey = "voice_no_ffmpeg"
	MsgVoiceTranscribing             MsgKey = "voice_transcribing"
	MsgVoiceTranscribed              MsgKey = "voice_transcribed"
	MsgVoiceTranscribeFailed         MsgKey = "voice_transcribe_failed"
	MsgVoiceEmpty                    MsgKey = "voice_empty"

	MsgTTSNotEnabled MsgKey = "tts_not_enabled"
	MsgTTSStatus     MsgKey = "tts_status"
	MsgTTSSwitched   MsgKey = "tts_switched"
	MsgTTSUsage      MsgKey = "tts_usage"

	MsgHeartbeatNotAvailable MsgKey = "heartbeat_not_available"
	MsgHeartbeatStatus       MsgKey = "heartbeat_status"
	MsgHeartbeatPaused       MsgKey = "heartbeat_paused"
	MsgHeartbeatResumed      MsgKey = "heartbeat_resumed"
	MsgHeartbeatInterval     MsgKey = "heartbeat_interval"
	MsgHeartbeatTriggered    MsgKey = "heartbeat_triggered"
	MsgHeartbeatUsage        MsgKey = "heartbeat_usage"
	MsgHeartbeatInvalidMins  MsgKey = "heartbeat_invalid_mins"

	MsgCronNotAvailable       MsgKey = "cron_not_available"
	MsgCronUsage              MsgKey = "cron_usage"
	MsgCronAddUsage           MsgKey = "cron_add_usage"
	MsgCronAdded              MsgKey = "cron_added"
	MsgCronAddedExec          MsgKey = "cron_added_exec"
	MsgCronAddExecUsage       MsgKey = "cron_addexec_usage"
	MsgCronEmpty              MsgKey = "cron_empty"
	MsgCronListTitle          MsgKey = "cron_list_title"
	MsgCronListFooter         MsgKey = "cron_list_footer"
	MsgCronExecUsage          MsgKey = "cron_exec_usage"
	MsgCronTriggered          MsgKey = "cron_triggered"
	MsgCronProjectUnavailable MsgKey = "cron_project_unavailable"
	MsgCronDelUsage           MsgKey = "cron_del_usage"
	MsgCronDeleted            MsgKey = "cron_deleted"
	MsgCronNotFound           MsgKey = "cron_not_found"
	MsgCronEnabled            MsgKey = "cron_enabled"
	MsgCronDisabled           MsgKey = "cron_disabled"
	MsgCronMuted              MsgKey = "cron_muted"
	MsgCronUnmuted            MsgKey = "cron_unmuted"
	MsgCronCardHint           MsgKey = "cron_card_hint"
	MsgCronNextShort          MsgKey = "cron_next_short"
	MsgCronLastShort          MsgKey = "cron_last_short"
	MsgCronBtnEnable          MsgKey = "cron_btn_enable"
	MsgCronBtnDisable         MsgKey = "cron_btn_disable"
	MsgCronBtnMute            MsgKey = "cron_btn_mute"
	MsgCronBtnUnmute          MsgKey = "cron_btn_unmute"
	MsgCronBtnDelete          MsgKey = "cron_btn_delete"

	MsgStatusTitle           MsgKey = "status_title"
	MsgReplyFooterRemaining  MsgKey = "reply_footer_remaining"
	MsgModelCurrent          MsgKey = "model_current"
	MsgModelChanged          MsgKey = "model_changed"
	MsgModelChangeFailed     MsgKey = "model_change_failed"
	MsgModelCardSwitching    MsgKey = "model_card_switching"
	MsgModelCardSwitched     MsgKey = "model_card_switched"
	MsgModelCardSwitchFailed MsgKey = "model_card_switch_failed"
	MsgModelNotSupported     MsgKey = "model_not_supported"
	MsgReasoningCurrent      MsgKey = "reasoning_current"
	MsgReasoningChanged      MsgKey = "reasoning_changed"
	MsgAgentConfigFailed     MsgKey = "agent_config_failed"
	MsgReasoningNotSupported MsgKey = "reasoning_not_supported"

	MsgCompressNotSupported MsgKey = "compress_not_supported"
	MsgCompressing          MsgKey = "compressing"
	MsgCompressNoSession    MsgKey = "compress_no_session"
	MsgCompressDone         MsgKey = "compress_done"

	MsgMemoryNotSupported MsgKey = "memory_not_supported"
	MsgMemoryShowProject  MsgKey = "memory_show_project"
	MsgMemoryShowGlobal   MsgKey = "memory_show_global"
	MsgMemoryEmpty        MsgKey = "memory_empty"
	MsgMemoryAdded        MsgKey = "memory_added"
	MsgMemoryAddFailed    MsgKey = "memory_add_failed"
	MsgMemoryAddUsage     MsgKey = "memory_add_usage"
	MsgUsageNotSupported  MsgKey = "usage_not_supported"
	MsgUsageFetchFailed   MsgKey = "usage_fetch_failed"

	// Inline strings previously hardcoded in engine.go
	MsgStatusMode             MsgKey = "status_mode"
	MsgStatusSession          MsgKey = "status_session"
	MsgStatusCron             MsgKey = "status_cron"
	MsgStatusThinkingMessages MsgKey = "status_thinking_messages"
	MsgStatusToolMessages     MsgKey = "status_tool_messages"
	MsgStatusSessionKey       MsgKey = "status_session_key"
	MsgStatusAgentSID         MsgKey = "status_agent_sid"
	MsgStatusUserID           MsgKey = "status_user_id"
	MsgEnabledShort           MsgKey = "enabled_short"
	MsgDisabledShort          MsgKey = "disabled_short"

	MsgModelDefault               MsgKey = "model_default"
	MsgModelListTitle             MsgKey = "model_list_title"
	MsgModelUsage                 MsgKey = "model_usage"
	MsgReasoningDefault           MsgKey = "reasoning_default"
	MsgReasoningListTitle         MsgKey = "reasoning_list_title"
	MsgReasoningUsage             MsgKey = "reasoning_usage"
	MsgReasoningSelectPlaceholder MsgKey = "reasoning_select_placeholder"

	MsgModeUsage                 MsgKey = "mode_usage"
	MsgLangSelectPlaceholder     MsgKey = "lang_select_placeholder"
	MsgModelSelectPlaceholder    MsgKey = "model_select_placeholder"
	MsgModeSelectPlaceholder     MsgKey = "mode_select_placeholder"
	MsgProviderSelectPlaceholder MsgKey = "provider_select_placeholder"
	MsgProviderClearOption       MsgKey = "provider_clear_option"
	MsgCardBack                  MsgKey = "card_back"
	MsgCardPrev                  MsgKey = "card_prev"
	MsgCardNext                  MsgKey = "card_next"
	MsgCardTitleStatus           MsgKey = "card_title_status"
	MsgCardTitleLanguage         MsgKey = "card_title_language"
	MsgCardTitleModel            MsgKey = "card_title_model"
	MsgCardTitleReasoning        MsgKey = "card_title_reasoning"
	MsgCardTitleMode             MsgKey = "card_title_mode"
	MsgCardTitleSessions         MsgKey = "card_title_sessions"
	MsgCardTitleSessionsPaged    MsgKey = "card_title_sessions_paged"
	MsgCardTitleCurrentSession   MsgKey = "card_title_current_session"
	MsgCardTitleHistory          MsgKey = "card_title_history"
	MsgCardTitleHistoryLast      MsgKey = "card_title_history_last"
	MsgCardTitleProvider         MsgKey = "card_title_provider"
	MsgCardTitleCron             MsgKey = "card_title_cron"
	MsgCardTitleTimer            MsgKey = "card_title_timer"
	MsgCardTitleHeartbeat        MsgKey = "card_title_heartbeat"
	MsgCardTitleCommands         MsgKey = "card_title_commands"
	MsgCardTitleAlias            MsgKey = "card_title_alias"
	MsgCardTitleConfig           MsgKey = "card_title_config"
	MsgCardTitleSkills           MsgKey = "card_title_skills"
	MsgCardTitleDoctor           MsgKey = "card_title_doctor"
	MsgCardTitleVersion          MsgKey = "card_title_version"
	MsgCardTitleUpgrade          MsgKey = "card_title_upgrade"
	MsgListItem                  MsgKey = "list_item"
	MsgListEmptySummary          MsgKey = "list_empty_summary"
	MsgCronIDLabel               MsgKey = "cron_id_label"
	MsgCronFailedSuffix          MsgKey = "cron_failed_suffix"

	MsgTimerNotAvailable    MsgKey = "timer_not_available"
	MsgTimerUsage           MsgKey = "timer_usage"
	MsgTimerAddUsage        MsgKey = "timer_add_usage"
	MsgTimerAdded           MsgKey = "timer_added"
	MsgTimerAddedExec       MsgKey = "timer_added_exec"
	MsgTimerAddExecUsage    MsgKey = "timer_addexec_usage"
	MsgTimerEmpty           MsgKey = "timer_empty"
	MsgTimerListTitle       MsgKey = "timer_list_title"
	MsgTimerListFooter      MsgKey = "timer_list_footer"
	MsgTimerDelUsage        MsgKey = "timer_del_usage"
	MsgTimerMuteUsage       MsgKey = "timer_mute_usage"
	MsgTimerDeleted         MsgKey = "timer_deleted"
	MsgTimerNotFound        MsgKey = "timer_not_found"
	MsgTimerMuted           MsgKey = "timer_muted"
	MsgTimerUnmuted         MsgKey = "timer_unmuted"
	MsgTimerCardHint        MsgKey = "timer_card_hint"
	MsgTimerBtnMute         MsgKey = "timer_btn_mute"
	MsgTimerBtnUnmute       MsgKey = "timer_btn_unmute"
	MsgTimerBtnDelete       MsgKey = "timer_btn_delete"
	MsgTimerIDLabel         MsgKey = "timer_id_label"
	MsgTimerScheduledLabel  MsgKey = "timer_scheduled_label"
	MsgTimerFailedSuffix    MsgKey = "timer_failed_suffix"
	MsgCommandsTagAgent     MsgKey = "commands_tag_agent"
	MsgCommandsTagShell     MsgKey = "commands_tag_shell"
	MsgUpgradeTimeoutSuffix MsgKey = "upgrade_timeout_suffix"

	MsgCronScheduleLabel MsgKey = "cron_schedule_label"
	MsgCronNextRunLabel  MsgKey = "cron_next_run_label"
	MsgCronLastRunLabel  MsgKey = "cron_last_run_label"

	MsgPermBtnAllow    MsgKey = "perm_btn_allow"
	MsgPermBtnDeny     MsgKey = "perm_btn_deny"
	MsgPermBtnAllowAll MsgKey = "perm_btn_allow_all"
	MsgPermCardTitle   MsgKey = "perm_card_title"
	MsgPermCardBody    MsgKey = "perm_card_body"
	MsgPermCardNote    MsgKey = "perm_card_note"

	MsgAskQuestionTitle     MsgKey = "ask_question_title"
	MsgAskQuestionNote      MsgKey = "ask_question_note"
	MsgAskQuestionNoteMulti MsgKey = "ask_question_note_multi"
	MsgAskQuestionMulti     MsgKey = "ask_question_multi"
	MsgAskQuestionPrompt    MsgKey = "ask_question_prompt"
	MsgAskQuestionAnswered  MsgKey = "ask_question_answered"

	MsgCommandsTitle        MsgKey = "commands_title"
	MsgCommandsEmpty        MsgKey = "commands_empty"
	MsgCommandsHint         MsgKey = "commands_hint"
	MsgCommandsUsage        MsgKey = "commands_usage"
	MsgCommandsAddUsage     MsgKey = "commands_add_usage"
	MsgCommandsAddExecUsage MsgKey = "commands_addexec_usage"
	MsgCommandsAdded        MsgKey = "commands_added"
	MsgCommandsExecAdded    MsgKey = "commands_exec_added"
	MsgCommandsAddExists    MsgKey = "commands_add_exists"
	MsgCommandsDelUsage     MsgKey = "commands_del_usage"
	MsgCommandsDeleted      MsgKey = "commands_deleted"
	MsgCommandsNotFound     MsgKey = "commands_not_found"

	MsgCommandExecTimeout MsgKey = "command_exec_timeout"
	MsgCommandExecError   MsgKey = "command_exec_error"
	MsgCommandExecSuccess MsgKey = "command_exec_success"

	MsgSkillsTitle            MsgKey = "skills_title"
	MsgSkillsEmpty            MsgKey = "skills_empty"
	MsgSkillsHint             MsgKey = "skills_hint"
	MsgSkillsTelegramMenuHint MsgKey = "skills_telegram_menu_hint"

	MsgConfigTitle       MsgKey = "config_title"
	MsgConfigHint        MsgKey = "config_hint"
	MsgConfigGetUsage    MsgKey = "config_get_usage"
	MsgConfigSetUsage    MsgKey = "config_set_usage"
	MsgConfigUpdated     MsgKey = "config_updated"
	MsgConfigKeyNotFound MsgKey = "config_key_not_found"
	MsgConfigReloaded    MsgKey = "config_reloaded"

	MsgDoctorRunning MsgKey = "doctor_running"
	MsgDoctorTitle   MsgKey = "doctor_title"
	MsgDoctorSummary MsgKey = "doctor_summary"

	MsgRestarting     MsgKey = "restarting"
	MsgRestartSuccess MsgKey = "restart_success"

	MsgUpgradeChecking    MsgKey = "upgrade_checking"
	MsgUpgradeUpToDate    MsgKey = "upgrade_up_to_date"
	MsgUpgradeAvailable   MsgKey = "upgrade_available"
	MsgUpgradeDownloading MsgKey = "upgrade_downloading"
	MsgUpgradeSuccess     MsgKey = "upgrade_success"
	MsgUpgradeDevBuild    MsgKey = "upgrade_dev_build"

	MsgWebNotSupported MsgKey = "web_not_supported"
	MsgWebNotEnabled   MsgKey = "web_not_enabled"
	MsgWebSetupSuccess MsgKey = "web_setup_success"
	MsgWebNeedRestart  MsgKey = "web_need_restart"
	MsgWebStatus       MsgKey = "web_status"

	MsgAliasEmpty      MsgKey = "alias_empty"
	MsgAliasListHeader MsgKey = "alias_list_header"
	MsgAliasAdded      MsgKey = "alias_added"
	MsgAliasDeleted    MsgKey = "alias_deleted"
	MsgAliasNotFound   MsgKey = "alias_not_found"
	MsgAliasUsage      MsgKey = "alias_usage"

	MsgNewSessionCreated      MsgKey = "new_session_created"
	MsgNewSessionCreatedName  MsgKey = "new_session_created_name"
	MsgSessionAutoResetIdle   MsgKey = "session_auto_reset_idle"
	MsgSessionClosingGraceful MsgKey = "session_closing_graceful"

	MsgDeleteUsage              MsgKey = "delete_usage"
	MsgDeleteSuccess            MsgKey = "delete_success"
	MsgDeleteActiveDenied       MsgKey = "delete_active_denied"
	MsgDeleteNotSupported       MsgKey = "delete_not_supported"
	MsgDeleteModeTitle          MsgKey = "delete_mode_title"
	MsgDeleteModeSelect         MsgKey = "delete_mode_select"
	MsgDeleteModeSelected       MsgKey = "delete_mode_selected"
	MsgDeleteModeSelectedCount  MsgKey = "delete_mode_selected_count"
	MsgDeleteModeDeleteSelected MsgKey = "delete_mode_delete_selected"
	MsgDeleteModeCancel         MsgKey = "delete_mode_cancel"
	MsgDeleteModeConfirmTitle   MsgKey = "delete_mode_confirm_title"
	MsgDeleteModeConfirmButton  MsgKey = "delete_mode_confirm_button"
	MsgDeleteModeBackButton     MsgKey = "delete_mode_back_button"
	MsgDeleteModeEmptySelection MsgKey = "delete_mode_empty_selection"
	MsgDeleteModeResultTitle    MsgKey = "delete_mode_result_title"
	MsgDeleteModeDeletingTitle  MsgKey = "delete_mode_deleting_title"
	MsgDeleteModeDeletingBody   MsgKey = "delete_mode_deleting_body"
	MsgDeleteModeMissingSession MsgKey = "delete_mode_missing_session"

	MsgSwitchSuccess   MsgKey = "switch_success"
	MsgSwitchNoMatch   MsgKey = "switch_no_match"
	MsgSwitchNoSession MsgKey = "switch_no_session"

	MsgCommandTimeout MsgKey = "command_timeout"

	MsgBannedWordBlocked MsgKey = "banned_word_blocked"
	MsgCommandDisabled   MsgKey = "command_disabled"
	MsgAdminRequired     MsgKey = "admin_required"
	MsgRateLimited       MsgKey = "rate_limited"
	MsgPsSent            MsgKey = "ps_sent"
	MsgPsSendFailed      MsgKey = "ps_send_failed"
	MsgPsEmpty           MsgKey = "ps_empty"
	MsgPsNoSession       MsgKey = "ps_no_session"

	MsgWhoamiTitle     MsgKey = "whoami_title"
	MsgWhoamiCardTitle MsgKey = "whoami_card_title"
	MsgWhoamiName      MsgKey = "whoami_name"
	MsgWhoamiPlatform  MsgKey = "whoami_platform"
	MsgWhoamiUsage     MsgKey = "whoami_usage"

	MsgRelayNoBinding     MsgKey = "relay_no_binding"
	MsgRelayBound         MsgKey = "relay_bound"
	MsgRelayBindRemoved   MsgKey = "relay_bind_removed"
	MsgRelayBindNotFound  MsgKey = "relay_bind_not_found"
	MsgRelayBindSuccess   MsgKey = "relay_bind_success"
	MsgRelayUsage         MsgKey = "relay_usage"
	MsgRelayNotAvailable  MsgKey = "relay_not_available"
	MsgRelayUnbound       MsgKey = "relay_unbound"
	MsgRelayBindSelf      MsgKey = "relay_bind_self"
	MsgRelayNotFound      MsgKey = "relay_not_found"
	MsgRelayNoTarget      MsgKey = "relay_no_target"
	MsgRelaySetupHint     MsgKey = "relay_setup_hint"
	MsgRelaySetupOK       MsgKey = "relay_setup_ok"
	MsgRelaySetupExists   MsgKey = "relay_setup_exists"
	MsgRelaySetupNoMemory MsgKey = "relay_setup_no_memory"
	MsgSetupNative        MsgKey = "setup_native"
	MsgCronSetupOK        MsgKey = "cron_setup_ok"

	MsgSearchUsage    MsgKey = "search_usage"
	MsgSearchError    MsgKey = "search_error"
	MsgSearchNoResult MsgKey = "search_no_result"
	MsgSearchResult   MsgKey = "search_result"
	MsgSearchHint     MsgKey = "search_hint"

	MsgBuiltinCmdNew       MsgKey = "new"
	MsgBuiltinCmdList      MsgKey = "list"
	MsgBuiltinCmdSearch    MsgKey = "search"
	MsgBuiltinCmdSwitch    MsgKey = "switch"
	MsgBuiltinCmdDelete    MsgKey = "delete"
	MsgBuiltinCmdName      MsgKey = "name"
	MsgBuiltinCmdCurrent   MsgKey = "current"
	MsgBuiltinCmdHistory   MsgKey = "history"
	MsgBuiltinCmdProvider  MsgKey = "provider"
	MsgBuiltinCmdMemory    MsgKey = "memory"
	MsgBuiltinCmdAllow     MsgKey = "allow"
	MsgBuiltinCmdModel     MsgKey = "model"
	MsgBuiltinCmdReasoning MsgKey = "reasoning"
	MsgBuiltinCmdMode      MsgKey = "mode"
	MsgBuiltinCmdLang      MsgKey = "lang"
	MsgBuiltinCmdQuiet     MsgKey = "quiet"
	MsgBuiltinCmdCompress  MsgKey = "compress"
	MsgBuiltinCmdStop      MsgKey = "stop"
	MsgBuiltinCmdCron      MsgKey = "cron"
	MsgBuiltinCmdCommands  MsgKey = "commands"
	MsgBuiltinCmdAlias     MsgKey = "alias"
	MsgBuiltinCmdSkills    MsgKey = "skills"
	MsgBuiltinCmdConfig    MsgKey = "config"
	MsgBuiltinCmdDoctor    MsgKey = "doctor"
	MsgBuiltinCmdUpgrade   MsgKey = "upgrade"
	MsgBuiltinCmdRestart   MsgKey = "restart"
	MsgBuiltinCmdStatus    MsgKey = "status"
	MsgBuiltinCmdUsage     MsgKey = "usage"
	MsgBuiltinCmdVersion   MsgKey = "version"
	MsgBuiltinCmdHelp      MsgKey = "help"
	MsgBuiltinCmdBind      MsgKey = "bind"
	MsgBuiltinCmdShell     MsgKey = "shell"
	MsgBuiltinCmdDir       MsgKey = "dir"
	MsgBuiltinCmdDiff      MsgKey = "diff"
	MsgBuiltinCmdPs        MsgKey = "ps"

	MsgDiffEmpty       MsgKey = "diff_empty"
	MsgDiffNoDiff2HTML MsgKey = "diff_no_diff2html"

	MsgDirChanged          MsgKey = "dir_changed"
	MsgDirCurrent          MsgKey = "dir_current"
	MsgDirReset            MsgKey = "dir_reset"
	MsgDirUsage            MsgKey = "dir_usage"
	MsgDirNotSupported     MsgKey = "dir_not_supported"
	MsgDirInvalidPath      MsgKey = "dir_invalid_path"
	MsgDirHistoryTitle     MsgKey = "dir_history_title"
	MsgDirHistoryHint      MsgKey = "dir_history_hint"
	MsgDirInvalidIndex     MsgKey = "dir_invalid_index"
	MsgDirNoHistory        MsgKey = "dir_no_history"
	MsgDirNoPrevious       MsgKey = "dir_no_previous"
	MsgDirCardTitle        MsgKey = "dir_card_title"
	MsgDirCardPageHint     MsgKey = "dir_card_page_hint"
	MsgDirCardEmptyHistory MsgKey = "dir_card_empty_history"
	MsgDirCardReset        MsgKey = "dir_card_reset"
	MsgDirCardPrev         MsgKey = "dir_card_prev"
	MsgShow                MsgKey = "show"
	MsgShowUsage           MsgKey = "show_usage"
	MsgShowParseError      MsgKey = "show_parse_error"
	MsgShowNotFound        MsgKey = "show_not_found"
	MsgShowDirWithLocation MsgKey = "show_dir_with_location"
	MsgShowReadFailed      MsgKey = "show_read_failed"

	// Multi-workspace messages
	MsgWsNotEnabled             MsgKey = "ws_not_enabled"
	MsgWsNoBinding              MsgKey = "ws_no_binding"
	MsgWsInfo                   MsgKey = "ws_info"
	MsgWsInfoShared             MsgKey = "ws_info_shared"
	MsgWsUsage                  MsgKey = "ws_usage"
	MsgWsInitUsage              MsgKey = "ws_init_usage"
	MsgWsBindUsage              MsgKey = "ws_bind_usage"
	MsgWsBindSuccess            MsgKey = "ws_bind_success"
	MsgWsBindNotFound           MsgKey = "ws_bind_not_found"
	MsgWsRouteUsage             MsgKey = "ws_route_usage"
	MsgWsRouteSuccess           MsgKey = "ws_route_success"
	MsgWsRouteAbsoluteRequired  MsgKey = "ws_route_absolute_required"
	MsgWsRouteNotFound          MsgKey = "ws_route_not_found"
	MsgWsRouteNotDirectory      MsgKey = "ws_route_not_directory"
	MsgWsUnbindSuccess          MsgKey = "ws_unbind_success"
	MsgWsListEmpty              MsgKey = "ws_list_empty"
	MsgWsListTitle              MsgKey = "ws_list_title"
	MsgWsSharedNoBinding        MsgKey = "ws_shared_no_binding"
	MsgWsSharedUsage            MsgKey = "ws_shared_usage"
	MsgWsSharedBindSuccess      MsgKey = "ws_shared_bind_success"
	MsgWsSharedRouteSuccess     MsgKey = "ws_shared_route_success"
	MsgWsSharedUnbindSuccess    MsgKey = "ws_shared_unbind_success"
	MsgWsSharedListEmpty        MsgKey = "ws_shared_list_empty"
	MsgWsSharedListTitle        MsgKey = "ws_shared_list_title"
	MsgWsSharedOnlyHint         MsgKey = "ws_shared_only_hint"
	MsgWsNotFoundHint           MsgKey = "ws_not_found_hint"
	MsgWsNotFoundHintGitOnly    MsgKey = "ws_not_found_hint_git_only"
	MsgWsResolutionError        MsgKey = "ws_resolution_error"
	MsgWsCloneProgress          MsgKey = "ws_clone_progress"
	MsgWsCloneSuccess           MsgKey = "ws_clone_success"
	MsgWsCloneFailed            MsgKey = "ws_clone_failed"
	MsgWsInitDirNotFound        MsgKey = "ws_init_dir_not_found"
	MsgWsInitInvalidTarget      MsgKey = "ws_init_invalid_target"
	MsgWsInitLocalPathsDisabled MsgKey = "ws_init_local_paths_disabled"
	MsgBackgroundAutoDenied     MsgKey = "background_auto_denied"

	// Agent system-prompt tool sections (Issue #1655). These are appended
	// to the agent's own system prompt by core/interfaces.go AgentSystemPromptForLang
	// so that operators running cc-connect with language="zh" see the
	// send / cron / timer / relay tool documentation in their native
	// language. Translation coverage is en + zh for this PR; additional
	// languages fall back to en automatically.
	MsgAgentSendToolPrompt  MsgKey = "agent_send_tool_prompt"
	MsgAgentCronToolPrompt  MsgKey = "agent_cron_tool_prompt"
	MsgAgentTimerToolPrompt MsgKey = "agent_timer_tool_prompt"
	MsgAgentRelayToolPrompt MsgKey = "agent_relay_tool_prompt"
)

const (
	MsgBotUsage                    MsgKey = "bot_usage"
	MsgBotPrivateOnly              MsgKey = "bot_privateonly"
	MsgBotUnsupported              MsgKey = "bot_unsupported"
	MsgBotBusy                     MsgKey = "bot_busy"
	MsgBotCancelled                MsgKey = "bot_cancelled"
	MsgBotAuthorize                MsgKey = "bot_authorize"
	MsgBotFailed                   MsgKey = "bot_failed"
	MsgBotExists                   MsgKey = "bot_exists"
	MsgBotReady                    MsgKey = "bot_ready"
	MsgBotSavedOffline             MsgKey = "bot_savedoffline"
	MsgBotSaveFailed               MsgKey = "bot_savefailed"
	MsgBotUnclaimed                MsgKey = "bot_unclaimed"
	MsgBotClaimInvalid             MsgKey = "bot_claiminvalid"
	MsgBotClaimed                  MsgKey = "bot_claimed"
	MsgBotOwnerRequired            MsgKey = "bot_ownerrequired"
	MsgTeamUsage                   MsgKey = "team_usage"
	MsgTeamInitScan                MsgKey = "team_init_scan"
	MsgTeamInitReady               MsgKey = "team_init_ready"
	MsgTeamInitStarting            MsgKey = "team_init_starting"
	MsgTeamInitNoStart             MsgKey = "team_init_no_start"
	MsgTeamInitAutoReady           MsgKey = "team_init_auto_ready"
	MsgBotAutoReady                MsgKey = "bot_auto_ready"
	MsgBotAutoSavedOffline         MsgKey = "bot_auto_saved_offline"
	MsgBotRegistrationOwnerMissing MsgKey = "bot_registration_owner_missing"
)

const (
	MsgTeamUnavailable      MsgKey = "MsgTeamUnavailable"
	MsgTeamProjectPending   MsgKey = "MsgTeamProjectPending"
	MsgTeamManagerOnly      MsgKey = "MsgTeamManagerOnly"
	MsgTeamGroupUnsupported MsgKey = "MsgTeamGroupUnsupported"
	MsgTeamProjects         MsgKey = "MsgTeamProjects"
	MsgTeamProjectReady     MsgKey = "MsgTeamProjectReady"
	MsgTeamChatUsage        MsgKey = "MsgTeamChatUsage"
)

const MsgTeamMemberSaved MsgKey = "team_member_saved"

const (
	MsgFormModify            MsgKey = "MsgFormModify"
	MsgBotProfileEditTitle   MsgKey = "MsgBotProfileEditTitle"
	MsgBotProfileEditHint    MsgKey = "MsgBotProfileEditHint"
	MsgBotAuthorizeAutomatic MsgKey = "MsgBotAuthorizeAutomatic"
	MsgBotNameReadFailed     MsgKey = "MsgBotNameReadFailed"
)

const (
	MsgTeamMembersTitle          MsgKey = "MsgTeamMembersTitle"
	MsgTeamMemberEdit            MsgKey = "MsgTeamMemberEdit"
	MsgTeamMemberFormTitle       MsgKey = "MsgTeamMemberFormTitle"
	MsgTeamMemberFormHint        MsgKey = "MsgTeamMemberFormHint"
	MsgTeamMemberNotFound        MsgKey = "MsgTeamMemberNotFound"
	MsgTeamMemberFormUnsupported MsgKey = "MsgTeamMemberFormUnsupported"
	MsgFormSave                  MsgKey = "MsgFormSave"
	MsgFormBotIDHint             MsgKey = "MsgFormBotIDHint"
)

const (
	MsgFormBotTitle      MsgKey = "MsgFormBotTitle"
	MsgFormProjectTitle  MsgKey = "MsgFormProjectTitle"
	MsgFormBotName       MsgKey = "MsgFormBotName"
	MsgFormIDHint        MsgKey = "MsgFormIDHint"
	MsgFormRole          MsgKey = "MsgFormRole"
	MsgFormBackend       MsgKey = "MsgFormBackend"
	MsgFormPrompt        MsgKey = "MsgFormPrompt"
	MsgFormProjectName   MsgKey = "MsgFormProjectName"
	MsgFormDirectory     MsgKey = "MsgFormDirectory"
	MsgFormMembers       MsgKey = "MsgFormMembers"
	MsgFormGoal          MsgKey = "MsgFormGoal"
	MsgFormCreate        MsgKey = "MsgFormCreate"
	MsgFormBotHint       MsgKey = "MsgFormBotHint"
	MsgFormProjectHint   MsgKey = "MsgFormProjectHint"
	MsgFormInvalidField  MsgKey = "MsgFormInvalidField"
	MsgFormExpired       MsgKey = "MsgFormExpired"
	MsgFormSubmitted     MsgKey = "MsgFormSubmitted"
	MsgFormRoleManager   MsgKey = "MsgFormRoleManager"
	MsgFormRolePlanner   MsgKey = "MsgFormRolePlanner"
	MsgFormRoleDeveloper MsgKey = "MsgFormRoleDeveloper"
	MsgFormRoleTester    MsgKey = "MsgFormRoleTester"
	MsgFormRoleReviewer  MsgKey = "MsgFormRoleReviewer"
)

const (
	MsgHelpTeamSection    MsgKey = "help_team_section"
	MsgHelpProjectSection MsgKey = "help_project_section"
	MsgProjectGroupUsage  MsgKey = "project_group_usage"
	MsgProjectCLIUsage    MsgKey = "project_cli_usage"
	MsgProjectManagerOnly MsgKey = "project_manager_only"
)

const (
	MsgProjectSettingsTitle          MsgKey = "MsgProjectSettingsTitle"
	MsgProjectSettingsBody           MsgKey = "MsgProjectSettingsBody"
	MsgProjectDirectoryScope         MsgKey = "MsgProjectDirectoryScope"
	MsgProjectEditInfo               MsgKey = "MsgProjectEditInfo"
	MsgProjectPrimaryDirectory       MsgKey = "MsgProjectPrimaryDirectory"
	MsgProjectAdditionalDirectories  MsgKey = "MsgProjectAdditionalDirectories"
	MsgProjectDirectoriesUnsupported MsgKey = "MsgProjectDirectoriesUnsupported"
	MsgProjectDirectoriesHelp        MsgKey = "MsgProjectDirectoriesHelp"
	MsgProjectEditDirectory          MsgKey = "MsgProjectEditDirectory"
	MsgProjectViewMembers            MsgKey = "MsgProjectViewMembers"
	MsgProjectBotSettings            MsgKey = "MsgProjectBotSettings"
	MsgProjectSettingsUsage          MsgKey = "MsgProjectSettingsUsage"
	MsgProjectGroupRequired          MsgKey = "MsgProjectGroupRequired"
	MsgProjectBusy                   MsgKey = "MsgProjectBusy"
	MsgProjectDirectoryConflict      MsgKey = "MsgProjectDirectoryConflict"
	MsgProjectSettingsSaved          MsgKey = "MsgProjectSettingsSaved"
	MsgProjectMembersBody            MsgKey = "MsgProjectMembersBody"
	MsgProjectModelInherited         MsgKey = "MsgProjectModelInherited"
	MsgProjectModelOverride          MsgKey = "MsgProjectModelOverride"
	MsgProjectBotSettingsBody        MsgKey = "MsgProjectBotSettingsBody"
	MsgProjectChangeModel            MsgKey = "MsgProjectChangeModel"
	MsgProjectResetModel             MsgKey = "MsgProjectResetModel"
	MsgProjectSharedSettings         MsgKey = "MsgProjectSharedSettings"
)

const MsgProjectModelScope MsgKey = "project_model_scope"
const MsgProjectModelChanged MsgKey = "project_model_changed"

const (
	MsgProgressQueue      MsgKey = "progress_queue"
	MsgProgressResponse   MsgKey = "progress_response"
	MsgProgressResponding MsgKey = "progress_responding"
	MsgProgressTool       MsgKey = "progress_tool"
	MsgProgressTiming     MsgKey = "progress_timing"
	MsgProgressPosition   MsgKey = "progress_position"
	MsgProgressPlan       MsgKey = "progress_plan"
	MsgProgressNext       MsgKey = "progress_next"
	MsgProgressNoPlan     MsgKey = "progress_noplan"
	MsgProgressReview     MsgKey = "progress_review"
	MsgProgressRecent     MsgKey = "progress_recent"
	MsgProgressSummary    MsgKey = "progress_summary"
	MsgProgressQueueSlow  MsgKey = "progress_queueslow"
	MsgProgressSilent     MsgKey = "progress_silent"
	MsgProgressControls   MsgKey = "progress_controls"
	MsgProgressOwner      MsgKey = "progress_owner"
)

const (
	MsgProgressRead       MsgKey = "progress_read"
	MsgProgressEdit       MsgKey = "progress_edit"
	MsgProgressSearch     MsgKey = "progress_search"
	MsgProgressTest       MsgKey = "progress_test"
	MsgProgressBuild      MsgKey = "progress_build"
	MsgProgressCommand    MsgKey = "progress_command"
	MsgProgressPlanning   MsgKey = "progress_planning"
	MsgProgressNoCurrent  MsgKey = "progress_nocurrent"
	MsgProgressFinishStep MsgKey = "progress_finishstep"
)

const (
	MsgProgressBrief    MsgKey = "progress_brief"
	MsgProgressExpand   MsgKey = "progress_expand"
	MsgProgressFoldHint MsgKey = "progress_foldhint"
	MsgProgressFullPlan MsgKey = "progress_fullplan"
)

var messages = map[MsgKey]map[Language]string{
	MsgProgressBrief:                 {LangEnglish: "Status: %s · elapsed: %s", LangChinese: "状态：%s · 已用时 %s", LangTraditionalChinese: "狀態：%s · 已用時 %s", LangJapanese: "状態: %s · 経過: %s", LangSpanish: "Estado: %s · duración: %s"},
	MsgProgressExpand:                {LangEnglish: "Execution details · click to expand/collapse", LangChinese: "执行详情 · 点击展开 / 收起", LangTraditionalChinese: "執行詳情 · 點擊展開 / 收起", LangJapanese: "実行詳細 · クリックで開閉", LangSpanish: "Detalles · clic para expandir/contraer"},
	MsgProgressFoldHint:              {LangEnglish: "Execution details are folded below. Click to view.", LangChinese: "执行详情已折叠，点击下方查看。", LangTraditionalChinese: "執行詳情已摺疊，點擊下方查看。", LangJapanese: "実行詳細は折りたたまれています。クリックで表示。", LangSpanish: "Los detalles están contraídos. Haz clic para verlos."},
	MsgProgressFullPlan:              {LangEnglish: "Execution plan", LangChinese: "执行计划", LangTraditionalChinese: "執行計畫", LangJapanese: "実行計画", LangSpanish: "Plan de ejecución"},
	MsgProgressRead:                  {LangEnglish: "Reading files", LangChinese: "读取文件", LangTraditionalChinese: "讀取檔案", LangJapanese: "ファイル読み取り", LangSpanish: "Leer archivos"},
	MsgProgressEdit:                  {LangEnglish: "Editing files", LangChinese: "修改文件", LangTraditionalChinese: "修改檔案", LangJapanese: "ファイル編集", LangSpanish: "Editar archivos"},
	MsgProgressSearch:                {LangEnglish: "Searching code or information", LangChinese: "检索代码或资料", LangTraditionalChinese: "檢索程式碼或資料", LangJapanese: "コード・情報検索", LangSpanish: "Buscar código o información"},
	MsgProgressTest:                  {LangEnglish: "Running test command", LangChinese: "运行测试命令", LangTraditionalChinese: "執行測試命令", LangJapanese: "テストコマンド実行", LangSpanish: "Ejecutar pruebas"},
	MsgProgressBuild:                 {LangEnglish: "Running build command", LangChinese: "运行构建命令", LangTraditionalChinese: "執行建置命令", LangJapanese: "ビルドコマンド実行", LangSpanish: "Ejecutar compilación"},
	MsgProgressCommand:               {LangEnglish: "Running command", LangChinese: "执行命令", LangTraditionalChinese: "執行命令", LangJapanese: "コマンド実行", LangSpanish: "Ejecutar comando"},
	MsgProgressPlanning:              {LangEnglish: "Updating execution plan", LangChinese: "更新执行计划", LangTraditionalChinese: "更新執行計畫", LangJapanese: "実行計画更新", LangSpanish: "Actualizar plan"},
	MsgProgressNoCurrent:             {LangEnglish: "No current step reported", LangChinese: "未报告当前步骤", LangTraditionalChinese: "未回報目前步驟", LangJapanese: "現在のステップ未報告", LangSpanish: "Paso actual no indicado"},
	MsgProgressFinishStep:            {LangEnglish: "Finish the current plan step", LangChinese: "完成当前计划步骤", LangTraditionalChinese: "完成目前計畫步驟", LangJapanese: "現在のステップを完了", LangSpanish: "Terminar el paso actual"},
	MsgProgressQueue:                 {LangEnglish: "Waiting in model queue", LangChinese: "模型服务排队中", LangTraditionalChinese: "模型服務排隊中", LangJapanese: "モデルの順番待ち", LangSpanish: "En cola del modelo"},
	MsgProgressResponse:              {LangEnglish: "Waiting for model response", LangChinese: "等待模型响应", LangTraditionalChinese: "等待模型回應", LangJapanese: "モデル応答待ち", LangSpanish: "Esperando respuesta del modelo"},
	MsgProgressResponding:            {LangEnglish: "Preparing response", LangChinese: "正在整理回复", LangTraditionalChinese: "正在整理回覆", LangJapanese: "回答を作成中", LangSpanish: "Preparando respuesta"},
	MsgProgressTool:                  {LangEnglish: "Executing tool", LangChinese: "工具执行中", LangTraditionalChinese: "工具執行中", LangJapanese: "ツール実行中", LangSpanish: "Ejecutando herramienta"},
	MsgProgressTiming:                {LangEnglish: "Stage: %s · stage elapsed: %s · total: %s\nLast work update: %s ago · last agent event: %s ago", LangChinese: "阶段：%s · 本阶段已用 %s · 总用时 %s\n距上次工作进展：%s · 距最后执行事件：%s", LangTraditionalChinese: "階段：%s · 本階段已用 %s · 總用時 %s\n距上次工作進展：%s · 距最後執行事件：%s", LangJapanese: "段階: %s · 経過: %s · 合計: %s\n最後の進捗から: %s · 最後のイベントから: %s", LangSpanish: "Etapa: %s · duración: %s · total: %s\nÚltimo avance hace: %s · último evento hace: %s"},
	MsgProgressPosition:              {LangEnglish: "Latest reported queue position: %d (not an ETA)", LangChinese: "最近上报排队位置：%d（不代表剩余时间）", LangTraditionalChinese: "最近回報排隊位置：%d（不代表剩餘時間）", LangJapanese: "最後に通知された待機順位: %d（残り時間ではありません）", LangSpanish: "Última posición recibida: %d (sin tiempo estimado)"},
	MsgProgressPlan:                  {LangEnglish: "Plan: %d/%d steps reported complete · current: %s", LangChinese: "计划：已报告完成 %d/%d 步 · 当前：%s", LangTraditionalChinese: "計畫：已回報完成 %d/%d 步 · 目前：%s", LangJapanese: "計画: %d/%d 完了報告 · 現在: %s", LangSpanish: "Plan: %d/%d pasos terminados según agente · actual: %s"},
	MsgProgressNext:                  {LangEnglish: "Next: %s", LangChinese: "下一步：%s", LangTraditionalChinese: "下一步：%s", LangJapanese: "次: %s", LangSpanish: "Siguiente: %s"},
	MsgProgressNoPlan:                {LangEnglish: "No execution plan reported yet", LangChinese: "尚未上报执行计划", LangTraditionalChinese: "尚未回報執行計畫", LangJapanese: "実行計画は未報告です", LangSpanish: "Aún no se ha recibido un plan"},
	MsgProgressReview:                {LangEnglish: "No pending plan step; delivery still needs review", LangChinese: "无待办计划步骤，交付仍需验收", LangTraditionalChinese: "無待辦計畫步驟，交付仍需驗收", LangJapanese: "未完了の計画なし。成果物の確認が必要です", LangSpanish: "Sin pasos pendientes; entrega pendiente de revisión"},
	MsgProgressRecent:                {LangEnglish: "Recent tool results (not task acceptance): %s", LangChinese: "最近工具结果（不等于任务验收）：%s", LangTraditionalChinese: "最近工具結果（不等於任務驗收）：%s", LangJapanese: "最近のツール結果（タスク承認ではありません）: %s", LangSpanish: "Resultados recientes (no aceptación de tarea): %s"},
	MsgProgressSummary:               {LangEnglish: "Latest public update: %s", LangChinese: "最近进展说明：%s", LangTraditionalChinese: "最近進展說明：%s", LangJapanese: "最新の公開更新: %s", LangSpanish: "Última actualización pública: %s"},
	MsgProgressQueueSlow:             {LangEnglish: "Queue wait exceeded 5 minutes. You can keep waiting, or stop and select another model. No automatic reassignment.", LangChinese: "排队已超过 5 分钟。可继续等待，或停止后切换模型；不会自动重复派工。", LangTraditionalChinese: "排隊已超過 5 分鐘。可繼續等待，或停止後切換模型；不會自動重複派工。", LangJapanese: "待機が5分を超えました。待機継続、または停止してモデル変更が可能です。自動再割当はしません。", LangSpanish: "Más de 5 minutos en cola. Puedes esperar o detener y cambiar modelo. Sin reasignación automática."},
	MsgProgressSilent:                {LangEnglish: "No new agent event for 2 minutes. Status is unconfirmed; the process may still be running.", LangChinese: "超过 2 分钟未收到新执行事件，当前进展待确认；进程可能仍在运行。", LangTraditionalChinese: "超過 2 分鐘未收到新執行事件，目前進展待確認；程序可能仍在執行。", LangJapanese: "2分間新しいイベントがありません。進捗は未確認で、処理は継続中の可能性があります。", LangSpanish: "Sin eventos nuevos durante 2 minutos. Progreso sin confirmar; el proceso puede seguir activo."},
	MsgProgressControls:              {LangEnglish: "/status: current run · /stop: stop this conversation\n/project board: project overview · /project pause: stop all project work", LangChinese: "/status 查看当前执行 · /stop 停止本会话\n/project board 查看项目总览 · /project pause 停止整个项目", LangTraditionalChinese: "/status 查看目前執行 · /stop 停止本會話\n/project board 查看專案總覽 · /project pause 停止整個專案", LangJapanese: "/status: 状態 · /stop: この会話を停止\n/project board: 概要 · /project pause: プロジェクト全体を停止", LangSpanish: "/status: estado · /stop: detener conversación\n/project board: resumen · /project pause: detener proyecto"},
	MsgProgressOwner:                 {LangEnglish: "Owner: %s\nExpected deliverable: %s", LangChinese: "负责人：%s\n预期交付：%s", LangTraditionalChinese: "負責人：%s\n預期交付：%s", LangJapanese: "担当: %s\n成果物: %s", LangSpanish: "Responsable: %s\nEntrega esperada: %s"},
	MsgRoundIdle:                     {LangEnglish: "Waiting for a new instruction. Mention the manager in this group to start another round; member context and history are retained.", LangChinese: "等待新指令。在本群 @ 管理者即可开始下一轮，成员上下文和历史保留。", LangTraditionalChinese: "等待新指令。在本群 @ 管理者即可開始下一輪，成員上下文和歷史保留。", LangJapanese: "次の指示を待っています。このグループで管理者にメンションすると次の作業を開始します。コンテキストと履歴は保持されます。", LangSpanish: "Esperando instrucciones. Menciona al gestor en este grupo para iniciar otra ronda; se conserva el contexto y el historial."},
	MsgRoundEnding:                   {LangEnglish: "Ending this round; waiting for member processes to stop. New work has not started.", LangChinese: "正在结束本轮，等待成员执行端停止；尚未开始新任务。", LangTraditionalChinese: "正在結束本輪，等待成員執行端停止；尚未開始新任務。", LangJapanese: "このラウンドを終了中です。メンバーの停止を待っています。", LangSpanish: "Finalizando esta ronda; esperando la parada de los miembros. No se ha iniciado trabajo nuevo."},
	MsgRoundEnded:                    {LangEnglish: "Round ended. Ending does not mean all tasks were accepted.", LangChinese: "本轮已结束。结束不代表所有任务都已验收。", LangTraditionalChinese: "本輪已結束。結束不代表所有任務都已驗收。", LangJapanese: "ラウンド終了。全タスクの検収完了を意味しません。", LangSpanish: "Ronda finalizada. No significa que todas las tareas estén aceptadas."},
	MsgRoundArchived:                 {LangEnglish: "This message belongs to an earlier round. Send a new instruction mentioning the manager in the group.", LangChinese: "这条消息属于已结束的轮次，请在群里另发新指令 @ 管理者。", LangTraditionalChinese: "這條訊息屬於已結束的輪次，請在群裡另發新指令 @ 管理者。", LangJapanese: "過去のラウンドです。グループで管理者宛てに新しい指示を送ってください。", LangSpanish: "Este mensaje pertenece a una ronda anterior. Envía nuevas instrucciones al gestor en el grupo."},
	MsgRoundStopFailed:               {LangEnglish: "Could not confirm member shutdown: %s. This round remains blocked; review and retry ending.", LangChinese: "尚未确认成员停止：%s。本轮仍禁止派工，请检查后重试结束。", LangTraditionalChinese: "尚未確認成員停止：%s。本輪仍禁止派工，請檢查後重試結束。", LangJapanese: "メンバーの停止を確認できません：%s。新規作業はブロックされています。再試行してください。", LangSpanish: "No se pudo confirmar la parada: %s. El trabajo sigue bloqueado; revisa y reintenta."},
	MsgRoundRetry:                    {LangEnglish: "Review and retry", LangChinese: "查看并重试", LangTraditionalChinese: "查看並重試", LangJapanese: "確認して再試行", LangSpanish: "Revisar y reintentar"},
	MsgRoundCurrent:                  {LangEnglish: "Round %d · %s", LangChinese: "第 %d 轮 · %s", LangTraditionalChinese: "第 %d 輪 · %s", LangJapanese: "ラウンド %d · %s", LangSpanish: "Ronda %d · %s"},
	MsgDashboardRefresh:              {LangEnglish: "Refresh panel", LangChinese: "刷新面板", LangTraditionalChinese: "重新整理面板", LangJapanese: "更新", LangSpanish: "Actualizar panel"},
	MsgDashboardHistory:              {LangEnglish: "Round history", LangChinese: "历史轮次", LangTraditionalChinese: "歷史輪次", LangJapanese: "履歴", LangSpanish: "Historial"},
	MsgDashboardCurrent:              {LangEnglish: "Current tasks", LangChinese: "当前任务", LangTraditionalChinese: "目前任務", LangJapanese: "現在のタスク", LangSpanish: "Tareas actuales"},
	MsgDashboardNew:                  {LangEnglish: "Write a new instruction", LangChinese: "写新指令", LangTraditionalChinese: "寫新指令", LangJapanese: "新しい指示", LangSpanish: "Nueva instrucción"},
	MsgDashboardUpdated:              {LangEnglish: "Updated: %s", LangChinese: "更新时间：%s", LangTraditionalChinese: "更新時間：%s", LangJapanese: "更新日時：%s", LangSpanish: "Actualizado: %s"},
	MsgDashboardFailed:               {LangEnglish: "Could not refresh the panel: %s. No new state was loaded; retry.", LangChinese: "面板刷新失败：%s。尚未获取最新状态，请重试。", LangTraditionalChinese: "面板重新整理失敗：%s。尚未取得最新狀態，請重試。", LangJapanese: "更新できませんでした：%s。最新状態は未取得です。再試行してください。", LangSpanish: "No se pudo actualizar: %s. No hay datos nuevos; reintenta."},
	MsgDashboardCounts:               {LangEnglish: "Accepted %d · Active/waiting %d · Unassigned %d · Other unaccepted %d", LangChinese: "已验收 %d · 执行或等待 %d · 尚未派发 %d · 其他未验收 %d", LangTraditionalChinese: "已驗收 %d · 執行或等待 %d · 尚未派發 %d · 其他未驗收 %d", LangJapanese: "検収済 %d · 実行/待機 %d · 未割当 %d · その他未検収 %d", LangSpanish: "Aceptadas %d · Activas/en espera %d · Sin asignar %d · Otras sin aceptar %d"},
	MsgDashboardPage:                 {LangEnglish: "Page %d/%d", LangChinese: "第 %d/%d 页", LangTraditionalChinese: "第 %d/%d 頁", LangJapanese: "ページ %d/%d", LangSpanish: "Página %d/%d"},
	MsgDashboardPending:              {LangEnglish: "Needs your decision: %s", LangChinese: "需要你处理：%s", LangTraditionalChinese: "需要你處理：%s", LangJapanese: "確認が必要：%s", LangSpanish: "Requiere tu decisión: %s"},
	MsgDashboardHistoryEntry:         {LangEnglish: "Round %d · %s\nEnded: %s · By: %s", LangChinese: "第 %d 轮 · %s\n结束时间：%s · 操作者：%s", LangTraditionalChinese: "第 %d 輪 · %s\n結束時間：%s · 操作者：%s", LangJapanese: "ラウンド %d · %s\n終了：%s · 操作者：%s", LangSpanish: "Ronda %d · %s\nFinalizada: %s · Por: %s"},
	MsgDashboardNoHistory:            {LangEnglish: "No ended rounds yet.", LangChinese: "尚无已结束轮次。", LangTraditionalChinese: "尚無已結束輪次。", LangJapanese: "終了したラウンドはありません。", LangSpanish: "Todavía no hay rondas finalizadas."},
	MsgDashboardPrevious:             {LangEnglish: "Previous page", LangChinese: "上一页", LangTraditionalChinese: "上一頁", LangJapanese: "前へ", LangSpanish: "Anterior"},
	MsgDashboardNext:                 {LangEnglish: "Next page", LangChinese: "下一页", LangTraditionalChinese: "下一頁", LangJapanese: "次へ", LangSpanish: "Siguiente"},
	MsgDashboardRoundRecords:         {LangEnglish: "View records", LangChinese: "查看记录", LangTraditionalChinese: "查看記錄", LangJapanese: "記録を表示", LangSpanish: "Ver registros"},
	MsgDashboardTopic:                {LangEnglish: "Open source thread", LangChinese: "打开来源话题", LangTraditionalChinese: "開啟來源話題", LangJapanese: "元のスレッド", LangSpanish: "Abrir conversación"},
	MsgDashboardLinkFailed:           {LangEnglish: "Could not load the source link: %s", LangChinese: "来源话题链接获取失败：%s", LangTraditionalChinese: "來源話題連結取得失敗：%s", LangJapanese: "リンクを取得できません：%s", LangSpanish: "No se pudo obtener el enlace: %s"},
	MsgRoundPause:                    {LangEnglish: "Pause this round", LangChinese: "暂停本轮", LangTraditionalChinese: "暫停本輪", LangJapanese: "ラウンドを一時停止", LangSpanish: "Pausar ronda"},
	MsgRoundResume:                   {LangEnglish: "Resume this round", LangChinese: "恢复本轮", LangTraditionalChinese: "恢復本輪", LangJapanese: "ラウンドを再開", LangSpanish: "Reanudar ronda"},
	MsgWorkflowBoard:                 {LangEnglish: "Project overview · %s", LangChinese: "项目总览 · %s", LangTraditionalChinese: "專案總覽 · %s", LangJapanese: "プロジェクト概要 · %s", LangSpanish: "Resumen del proyecto · %s"},
	MsgWorkflowSummary:               {LangEnglish: "Goal: %s\nState: %s · Display: %s\nTasks: %d · Accepted: %d\n\n%s", LangChinese: "目标：%s\n状态：%s · 显示：%s\n任务：%d · 已验收：%d\n\n%s", LangTraditionalChinese: "目標：%s\n狀態：%s · 顯示：%s\n任務：%d · 已驗收：%d\n\n%s", LangJapanese: "目標: %s\n状態: %s · 表示: %s\nタスク: %d · 承認済: %d\n\n%s", LangSpanish: "Objetivo: %s\nEstado: %s · Vista: %s\nTareas: %d · Aceptadas: %d\n\n%s"},
	MsgWorkflowTask:                  {LangEnglish: "Owner: %s\nWhy now: %s\nExpected deliverable: %s\nDependencies: %s\nStatus: %s\nUpdated: %s\n\n%s", LangChinese: "负责人：%s\n派工原因：%s\n预期交付：%s\n前置任务：%s\n状态：%s\n更新时间：%s\n\n%s", LangTraditionalChinese: "負責人：%s\n派工原因：%s\n預期交付：%s\n前置任務：%s\n狀態：%s\n更新時間：%s\n\n%s", LangJapanese: "担当: %s\n割当理由: %s\n成果物: %s\n依存タスク: %s\n状態: %s\n更新: %s\n\n%s", LangSpanish: "Responsable: %s\nMotivo: %s\nEntrega esperada: %s\nDependencias: %s\nEstado: %s\nActualizado: %s\n\n%s"},
	MsgProjectFinish:                 {LangEnglish: "End this round", LangChinese: "结束本轮任务", LangTraditionalChinese: "結束本輪任務", LangJapanese: "このラウンドを終了", LangSpanish: "Finalizar esta ronda"},
	MsgProjectStatusEnded:            {LangEnglish: "Ended", LangChinese: "已结束", LangTraditionalChinese: "已結束", LangJapanese: "終了済み", LangSpanish: "Finalizado"},
	MsgProjectEnded:                  {LangEnglish: "Project ended. New tasks, rework and resume are disabled. Deliverables and history remain available via /project board and /project tasks. Ending does not mean all tasks were accepted.", LangChinese: "项目已结束，不再接收新任务或返工，不能通过恢复继续。交付和历史记录保留，可用 /project board 和 /project tasks 查看。结束不代表所有任务都已验收。", LangTraditionalChinese: "專案已結束，不再接收新任務或返工，不能透過恢復繼續。交付及歷史保留，可用 /project board 和 /project tasks 查看。結束不代表所有任務已驗收。", LangJapanese: "プロジェクトは終了しました。新規タスク、手直し、再開はできません。成果物と履歴は /project board、/project tasks で確認できます。終了は全タスクの検収を意味しません。", LangSpanish: "Proyecto finalizado. No admite nuevas tareas, revisiones ni reanudación. Entregables e historial disponibles en /project board y /project tasks. Finalizar no significa que todas las tareas estén aceptadas."},
	MsgProjectFinishPreview:          {LangEnglish: "Project: %s\nAccepted: %d/%d · Active/waiting: %d · Unassigned plans: %d · Other unaccepted: %d", LangChinese: "项目：%s\n已验收：%d/%d · 执行或等待中：%d · 尚未派发：%d · 其他未验收：%d", LangTraditionalChinese: "專案：%s\n已驗收：%d/%d · 執行或等待中：%d · 尚未派發：%d · 其他未驗收：%d", LangJapanese: "プロジェクト：%s\n検収済み：%d/%d · 実行/待機中：%d · 未割当：%d · その他未検収：%d", LangSpanish: "Proyecto: %s\nAceptadas: %d/%d · Activas/en espera: %d · Sin asignar: %d · Otras sin aceptar: %d"},
	MsgProjectFinishConsequences:     {LangEnglish: "Stop this round's members and queued input, expire pending questions/approvals and cancel unassigned plans. Results keep their acceptance states. The group, project settings and member context remain. Send a new instruction in this group after stopping completes.", LangChinese: "停止本轮所有成员执行及排队输入，关闭待回答/审批请求，取消未派发待办。结果保留原验收状态。群、项目设置与成员上下文保留，停止完成后直接在本群发送新指令。", LangTraditionalChinese: "停止本輪所有成員執行及排隊輸入，關閉待回答/審批請求，取消未派發待辦。結果保留原驗收狀態。群、專案設定與成員上下文保留，停止完成後直接在本群發送新指令。", LangJapanese: "今回の実行と入力待ちを停止し、質問・承認待ちと未割当予定を無効化します。成果物、検収状態、グループ、設定、コンテキストは保持されます。停止後、このグループで新しい指示を送れます。", LangSpanish: "Detiene esta ronda y sus entradas en cola, caduca preguntas/aprobaciones y cancela planes sin asignar. Se conservan resultados, grupo, ajustes y contexto. Tras la parada, envía nuevas instrucciones aquí."},
	MsgProjectFinishConfirm:          {LangEnglish: "Confirm end of round", LangChinese: "确认结束本轮", LangTraditionalChinese: "確認結束本輪", LangJapanese: "ラウンド終了を確認", LangSpanish: "Confirmar fin de ronda"},
	MsgProjectFinishCancel:           {LangEnglish: "Keep this round open", LangChinese: "暂不结束", LangTraditionalChinese: "暫不結束", LangJapanese: "終了しない", LangSpanish: "Mantener ronda abierta"},
	MsgProjectFinishChanged:          {LangEnglish: "Project work changed since the preview. Review the updated state before confirming.", LangChinese: "项目任务状态已变化，请查看最新情况后重新确认。", LangTraditionalChinese: "專案任務狀態已變更，請查看最新情況後重新確認。", LangJapanese: "確認後にタスク状態が変わりました。最新の状態を確認してください。", LangSpanish: "El estado ha cambiado. Revisa los datos actuales antes de confirmar."},
	MsgProjectFinishTextCommand:      {LangEnglish: "Text confirmation: %s", LangChinese: "文字确认命令：%s", LangTraditionalChinese: "文字確認指令：%s", LangJapanese: "テキストで確認：%s", LangSpanish: "Confirmación por texto: %s"},
	MsgProjectEndedAt:                {LangEnglish: "Ended at: %s", LangChinese: "结束时间：%s", LangTraditionalChinese: "結束時間：%s", LangJapanese: "終了日時：%s", LangSpanish: "Finalizado el: %s"},
	MsgWorkflowHelp:                  {LangEnglish: "/project dashboard (board) · /project tasks\n/project task T001 · /project task T001 logs\n/project display compact|standard|debug\n/project pause · /project resume · /project finish\nProgress refreshes every 15s: stage, plan, next step and wait reason. /status checks the current run; /stop stops this conversation.\nPause stops all project members and queued work. Resume enables new requests; it does not replay stopped tasks.", LangChinese: "/project dashboard（或 board）：项目面板 · /project tasks：任务清单\n/project task T001：任务详情 · /project task T001 logs：工具日志\n/project display compact|standard|debug：简洁/标准/调试\n/project pause：暂停整个项目 · /project resume：恢复接收任务\n/project finish：结束本轮任务（需确认）\n任务卡每 15 秒更新阶段、计划、下一步和等待原因。/status 查看当前执行；/stop 停止本会话。\n暂停会停止所有成员及排队任务；恢复不会自动重派已停止的任务。", LangTraditionalChinese: "/project dashboard（board）：專案面板 · /project tasks：任務清單\n/project task T001：詳情 · /project task T001 logs：工具日誌\n/project display compact|standard|debug：簡潔/標準/除錯\n/project pause：暫停專案 · /project resume：恢復接收\n/project finish：結束本輪（需確認）\n任務卡每 15 秒更新階段、計畫和等待原因。/status 查看執行；/stop 停止本會話。\n恢復不會自動重派已停止的任務。", LangJapanese: "/project dashboard (board) · /project tasks\n/project task T001 · /project task T001 logs\n/project display compact|standard|debug\n/project pause · /project resume · /project finish\n進捗は15秒ごとに更新。/status で確認、/stop でこの会話を停止。\n一時停止は全メンバーを停止します。再開は停止済みタスクを再実行しません。", LangSpanish: "/project dashboard (board) · /project tasks\n/project task T001 · /project task T001 logs\n/project display compact|standard|debug\n/project pause · /project resume · /project finish\nProgreso cada 15s. /status consulta la ejecución; /stop detiene esta conversación.\nPausar detiene todos los miembros. Reanudar no repite tareas detenidas."},
	MsgWorkflowPaused:                {LangEnglish: "Project paused. Use /project resume to enable new work.", LangChinese: "项目已暂停。使用 /project resume 后才能接收新任务。", LangTraditionalChinese: "專案已暫停。使用 /project resume 恢復接收新任務。", LangJapanese: "プロジェクトは一時停止中です。/project resume で再開します。", LangSpanish: "Proyecto pausado. Usa /project resume para habilitar trabajo."},
	MsgWorkflowResumed:               {LangEnglish: "Project resumed; stopped tasks have not been dispatched again.", LangChinese: "项目已恢复接收任务，已停止的任务不会自动重派。", LangTraditionalChinese: "專案已恢復接收任務，已停止的任務不會自動重派。", LangJapanese: "新しいタスクを受付中。停止済みタスクは再実行されません。", LangSpanish: "Proyecto habilitado; no se han repetido tareas detenidas."},
	MsgWorkflowProgress:              {LangEnglish: "%s\nStatus: %s\nTool operations: %d\n%s", LangChinese: "%s\n状态：%s\n工具操作：%d 次\n%s", LangTraditionalChinese: "%s\n狀態：%s\n工具操作：%d 次\n%s", LangJapanese: "%s\n状態: %s\nツール操作: %d\n%s", LangSpanish: "%s\nEstado: %s\nOperaciones: %d\n%s"},
	MsgWorkflowMissing:               {LangEnglish: "Not specified", LangChinese: "未填写", LangTraditionalChinese: "未填寫", LangJapanese: "未指定", LangSpanish: "Sin especificar"},
	MsgWorkflowLogs:                  {LangEnglish: "Tool log · %s (bounded, excludes internal reasoning). Use /project task %s logs to download.", LangChinese: "工具日志 · %s（限量保留，不含模型内部推理）。使用 /project task %s logs 下载。", LangTraditionalChinese: "工具日誌 · %s（限量保留，不含模型內部推理）。使用 /project task %s logs 下載。", LangJapanese: "ツールログ · %s（上限あり、内部推論なし）。/project task %s logs で取得。", LangSpanish: "Registro · %s (limitado, sin razonamiento interno). /project task %s logs para descargar."},
	MsgWorkflowEmpty:                 {LangEnglish: "No tasks assigned.", LangChinese: "尚未派工。", LangTraditionalChinese: "尚未派工。", LangJapanese: "割当済みタスクはありません。", LangSpanish: "No hay tareas asignadas."},
	MsgWorkflowCurrent:               {LangEnglish: "Current operation: %s", LangChinese: "当前操作：%s", LangTraditionalChinese: "目前操作：%s", LangJapanese: "現在の操作: %s", LangSpanish: "Operación actual: %s"},
	MsgWorkflowSaved:                 {LangEnglish: "Display mode saved: %s. Applies to subsequent turns.", LangChinese: "显示模式已设为 %s，下次执行生效。", LangTraditionalChinese: "顯示模式已設為 %s，下次執行生效。", LangJapanese: "表示モードを %s に保存。次のターンから適用。", LangSpanish: "Vista guardada: %s. Se aplica al siguiente turno."},
	MsgWorkflowSubmitted:             {LangEnglish: "Submitted for review; acceptance is still pending.", LangChinese: "已提交交付，等待管理者验收。", LangTraditionalChinese: "已提交交付，等待管理者驗收。", LangJapanese: "成果物を提出済み。承認待ちです。", LangSpanish: "Entrega enviada; pendiente de aceptación."},
	MsgWorkflowThread:                {LangEnglish: "Open task", LangChinese: "查看任务", LangTraditionalChinese: "查看任務", LangJapanese: "タスクを開く", LangSpanish: "Abrir tarea"},
	MsgWorkflowStatusActive:          {LangEnglish: "Active", LangChinese: "运行中", LangTraditionalChinese: "執行中", LangJapanese: "実行中", LangSpanish: "Activo"},
	MsgWorkflowStatusPaused:          {LangEnglish: "Paused", LangChinese: "已暂停", LangTraditionalChinese: "已暫停", LangJapanese: "一時停止", LangSpanish: "Pausado"},
	MsgWorkflowStatusPlanned:         {LangEnglish: "Planned · not assigned", LangChinese: "待安排·尚未派发", LangTraditionalChinese: "待安排·尚未派發", LangJapanese: "計画済み・未割当", LangSpanish: "Planificada · sin asignar"},
	MsgWorkflowStatusCancelled:       {LangEnglish: "Cancelled", LangChinese: "已取消", LangTraditionalChinese: "已取消", LangJapanese: "キャンセル済み", LangSpanish: "Cancelada"},
	MsgWorkflowStatusQueued:          {LangEnglish: "Queued", LangChinese: "已派工·待开始", LangTraditionalChinese: "已派工·待開始", LangJapanese: "開始待ち", LangSpanish: "En cola"},
	MsgWorkflowStatusRunning:         {LangEnglish: "Running", LangChinese: "执行中", LangTraditionalChinese: "執行中", LangJapanese: "実行中", LangSpanish: "En ejecución"},
	MsgWorkflowStatusWaitingApproval: {LangEnglish: "Awaiting approval", LangChinese: "等待审批", LangTraditionalChinese: "等待審批", LangJapanese: "承認待ち", LangSpanish: "Esperando aprobación"},
	MsgWorkflowStatusWaitingInput:    {LangEnglish: "Awaiting input", LangChinese: "等待补充信息", LangTraditionalChinese: "等待補充資訊", LangJapanese: "入力待ち", LangSpanish: "Esperando información"},
	MsgWorkflowStatusSubmitted:       {LangEnglish: "Submitted · review needed", LangChinese: "已提交·待验收", LangTraditionalChinese: "已提交·待驗收", LangJapanese: "提出済み・確認待ち", LangSpanish: "Entregada · pendiente de revisión"},
	MsgWorkflowStatusAccepted:        {LangEnglish: "Accepted", LangChinese: "已验收", LangTraditionalChinese: "已驗收", LangJapanese: "承認済み", LangSpanish: "Aceptada"},
	MsgWorkflowStatusFailed:          {LangEnglish: "Failed", LangChinese: "失败·需处理", LangTraditionalChinese: "失敗·需處理", LangJapanese: "失敗・要対応", LangSpanish: "Fallida"},
	MsgWorkflowStatusBlocked:         {LangEnglish: "Blocked", LangChinese: "阻塞", LangTraditionalChinese: "阻塞", LangJapanese: "ブロック中", LangSpanish: "Bloqueada"},
	MsgWorkflowStatusStopped:         {LangEnglish: "Stopped", LangChinese: "已停止", LangTraditionalChinese: "已停止", LangJapanese: "停止済み", LangSpanish: "Detenida"},
	MsgWorkflowStatusInterrupted:     {LangEnglish: "Interrupted", LangChinese: "已中断", LangTraditionalChinese: "已中斷", LangJapanese: "中断", LangSpanish: "Interrumpida"},

	MsgQueryDone:       {LangEnglish: "Completed", LangChinese: "已完成", LangTraditionalChinese: "已完成", LangJapanese: "完了", LangSpanish: "Completado"},
	MsgQueryFailed:     {LangEnglish: "Failed", LangChinese: "执行失败", LangTraditionalChinese: "執行失敗", LangJapanese: "失敗", LangSpanish: "Error"},
	MsgQueryLatestPage: {LangEnglish: "Latest page", LangChinese: "最新一页", LangTraditionalChinese: "最新一頁", LangJapanese: "最新ページ", LangSpanish: "Última página"},
	MsgQueryProcess:    {LangEnglish: "Execution progress", LangChinese: "执行进度", LangTraditionalChinese: "執行進度", LangJapanese: "実行状況", LangSpanish: "Progreso de ejecución"},
	MsgQueryAnswer:     {LangEnglish: "Conclusion", LangChinese: "结论", LangTraditionalChinese: "結論", LangJapanese: "結論", LangSpanish: "Conclusión"},
	MsgRolePermissionsApplyFailed: {
		LangEnglish:            "Role %s: permissions were checked, but automatic application could not finish. Confirm them here or retry from /team permissions:\n%s",
		LangChinese:            "角色 %s：权限已检查，但自动申请未完成。可通过以下入口确认，或从 /team permissions 重试：\n%s",
		LangTraditionalChinese: "角色 %s：權限已檢查，但自動申請未完成。可透過以下入口確認，或從 /team permissions 重試：\n%s",
		LangJapanese:           "ロール %s：権限の確認は完了しましたが、自動申請できませんでした。以下で確認するか /team permissions から再試行してください：\n%s",
		LangSpanish:            "Rol %s: permisos comprobados, pero la solicitud automática no terminó. Confirma aquí o reintenta desde /team permissions:\n%s",
	},

	MsgRolePermissionsReady: {
		LangEnglish:            "Role %s: required application permissions are active.",
		LangChinese:            "角色 %s：所需应用权限已生效。",
		LangTraditionalChinese: "角色 %s：所需應用權限已生效。",
		LangJapanese:           "ロール %s：必要なアプリ権限は有効です。",
		LangSpanish:            "Rol %s: los permisos necesarios están activos.",
	},
	MsgRolePermissionsPending: {
		LangEnglish:            "Role %s: missing permissions were selected automatically. Confirm them here, then recheck with /team permissions:\n%s",
		LangChinese:            "角色 %s：已自动选好缺失权限，请打开以下入口批量确认，完成后用 /team permissions 复查：\n%s",
		LangTraditionalChinese: "角色 %s：已自動選好缺少的權限，請開啟以下入口批次確認，完成後用 /team permissions 複查：\n%s",
		LangJapanese:           "ロール %s：不足する権限を選択しました。以下で確認後、/team permissions で再確認してください：\n%s",
		LangSpanish:            "Rol %s: permisos faltantes seleccionados. Confírmalos aquí y comprueba con /team permissions:\n%s",
	},
	MsgRolePermissionsSubmitted: {
		LangEnglish:            "Role %s: an administrator permission request was submitted; it is not yet granted. Confirm any remaining permissions here:\n%s",
		LangChinese:            "角色 %s：已提交管理员权限申请，尚未生效。仍需确认的权限入口：\n%s",
		LangTraditionalChinese: "角色 %s：已提交管理員權限申請，尚未生效。仍需確認的權限入口：\n%s",
		LangJapanese:           "ロール %s：管理者に権限を申請しました。承認待ちです。残りの権限はこちら：\n%s",
		LangSpanish:            "Rol %s: solicitud enviada al administrador, aún pendiente. Confirma los permisos restantes aquí:\n%s",
	},
	MsgRolePermissionsUnknown: {
		LangEnglish:            "Role %s: permissions could not be verified. The bot is saved; review its default permissions here or retry /team permissions:\n%s",
		LangChinese:            "角色 %s：暂时无法核验权限，Bot 已保存。可在以下入口确认默认权限，或用 /team permissions 重试：\n%s",
		LangTraditionalChinese: "角色 %s：暫時無法核驗權限，Bot 已儲存。可在以下入口確認預設權限，或用 /team permissions 重試：\n%s",
		LangJapanese:           "ロール %s：権限を確認できませんでした。Bot は保存済みです。以下で確認するか /team permissions で再試行してください：\n%s",
		LangSpanish:            "Rol %s: no se pudieron verificar los permisos. Bot guardado. Revisa aquí o reintenta /team permissions:\n%s",
	},
	MsgRolePermissionsUser: {
		LangEnglish:            "To use personal labels, authorize your account if not already authorized: /team labels in the manager private chat.",
		LangChinese:            "个人标签需本人授权；尚未授权时，请在管理者私聊使用 /team labels。",
		LangTraditionalChinese: "個人標籤需本人授權；尚未授權時，請在管理者私聊使用 /team labels。",
		LangJapanese:           "個人ラベルは管理者との個別チャットで /team labels を実行して承認してください。",
		LangSpanish:            "Las etiquetas personales requieren tu autorización en el chat privado del gestor: /team labels.",
	},
	MsgRolePermissionsUsage: {
		LangEnglish:            "Use /team permissions [bot] in the manager private chat.",
		LangChinese:            "请在管理者私聊使用 /team permissions [bot]。",
		LangTraditionalChinese: "請在管理者私聊使用 /team permissions [bot]。",
		LangJapanese:           "管理者との個別チャットで /team permissions [bot] を使用してください。",
		LangSpanish:            "Usa /team permissions [bot] en el chat privado del gestor.",
	},
	MsgRolePermissionsCreationHint: {
		LangEnglish:            "Creation automatically checks and requests permissions for the selected role. You confirm permissions that require approval; personal OAuth is separate.",
		LangChinese:            "创建时自动检查并申请所选角色的默认权限；需审批的权限由你确认，个人身份授权单独完成。",
		LangTraditionalChinese: "建立時自動檢查並申請所選角色的預設權限；需審批的權限由你確認，個人身分授權另行完成。",
		LangJapanese:           "作成時にロールの権限を確認・申請します。承認が必要な権限はご確認ください。個人認証は別途必要です。",
		LangSpanish:            "Al crear se comprueban y solicitan los permisos del rol. Tú confirmas los que requieran aprobación; OAuth personal es independiente.",
	},

	"team permissions": {LangEnglish: "Role permissions", LangChinese: "角色权限", LangTraditionalChinese: "角色權限", LangJapanese: "ロールの権限", LangSpanish: "Permisos del rol"},

	MsgLabelTitle: {
		LangEnglish:            "Label settings",
		LangChinese:            "标签设置",
		LangTraditionalChinese: "標籤設定",
		LangJapanese:           "ラベル設定",
		LangSpanish:            "Ajustes de etiquetas",
	},

	MsgLabelHint: {
		LangEnglish:            "Labels belong to your conversation list. Save defaults for future bots and project groups; existing conversations are unchanged.",
		LangChinese:            "标签只影响你自己的会话列表。默认值用于以后新建的 Bot 和任务群，已有会话保持原状。",
		LangTraditionalChinese: "標籤只影響你自己的會話列表。預設值用於以後新建的 Bot 和任務群，已有會話保持原狀。",
		LangJapanese:           "ラベルは自分の会話一覧に適用されます。今後作成する Bot とプロジェクトグループの既定値を保存します。",
		LangSpanish:            "Las etiquetas pertenecen a tu lista de conversaciones. Los valores guardados se aplican a futuros bots y grupos.",
	},

	MsgLabelNew: {
		LangEnglish:            "New labels (one per line)",
		LangChinese:            "新建标签（每行一个）",
		LangTraditionalChinese: "新建標籤（每行一個）",
		LangJapanese:           "新しいラベル（1 行に 1 つ）",
		LangSpanish:            "Etiquetas nuevas (una por línea)",
	},

	MsgLabelNewHint: {
		LangEnglish:            "Leave blank to add no new label; matching names are reused.",
		LangChinese:            "留空表示不新建；同名标签会复用。",
		LangTraditionalChinese: "留空表示不新建；同名標籤會復用。",
		LangJapanese:           "空欄なら追加なし。同名のラベルは再利用します。",
		LangSpanish:            "Vacío para no crear; se reutilizan nombres existentes.",
	},

	MsgLabelAuthNeeded: {
		LangEnglish:            "User authorization is required. In private chat with the manager, open /team labels and authorize.",
		LangChinese:            "需要用户身份授权。请私聊管理者，打开 /team labels 后授权。",
		LangTraditionalChinese: "需要使用者身分授權。請私聊管理者，打開 /team labels 後授權。",
		LangJapanese:           "ユーザー認証が必要です。管理 Bot の個別チャットで /team labels を開いて認証してください。",
		LangSpanish:            "Se requiere autorización de usuario. Abre /team labels en privado con el administrador.",
	},

	MsgLabelWrongUser: {
		LangEnglish:            "The authorized account does not match the requester. Use your own account.",
		LangChinese:            "授权账号与发起人不一致，请使用你自己的账号授权。",
		LangTraditionalChinese: "授權帳號與發起人不一致，請使用你自己的帳號授權。",
		LangJapanese:           "認証したアカウントが依頼者と異なります。自分のアカウントを使用してください。",
		LangSpanish:            "La cuenta autorizada no coincide con el solicitante. Usa tu propia cuenta.",
	},

	MsgLabelBusy: {
		LangEnglish:            "Label settings or authorization are in progress. Try again after they finish.",
		LangChinese:            "标签设置或授权正在进行，请完成后重试。",
		LangTraditionalChinese: "標籤設定或授權正在進行，請完成後重試。",
		LangJapanese:           "ラベル設定または認証中です。完了後に再試行してください。",
		LangSpanish:            "La configuración o autorización está en curso. Reintenta al terminar.",
	},

	MsgLabelFailed: {
		LangEnglish:            "Label operation failed (%s). Check user scopes im:feed_group_v1:read and im:feed_group_v1:write; retry in /team labels.",
		LangChinese:            "标签操作失败（%s）。请检查用户身份权限 im:feed_group_v1:read、im:feed_group_v1:write，并在 /team labels 重试。",
		LangTraditionalChinese: "標籤操作失敗（%s）。請檢查使用者身分權限 im:feed_group_v1:read、im:feed_group_v1:write，並在 /team labels 重試。",
		LangJapanese:           "ラベル操作失敗（%s）。ユーザー権限 im:feed_group_v1:read / im:feed_group_v1:write を確認し /team labels で再試行してください。",
		LangSpanish:            "Error de etiquetas (%s). Revisa permisos de usuario im:feed_group_v1:read e im:feed_group_v1:write; reintenta en /team labels.",
	},

	MsgLabelUnsupported: {
		LangEnglish:            "This platform does not support personal labels.",
		LangChinese:            "当前平台不支持个人会话标签。",
		LangTraditionalChinese: "目前平台不支援個人會話標籤。",
		LangJapanese:           "このプラットフォームは個人ラベルに未対応です。",
		LangSpanish:            "Esta plataforma no admite etiquetas personales.",
	},

	MsgLabelPrivate: {
		LangEnglish:            "Open /team labels in private chat with the manager to configure or authorize your personal labels.",
		LangChinese:            "请在与管理者的私聊中打开 /team labels，配置和授权个人标签。",
		LangTraditionalChinese: "請在與管理者的私聊中打開 /team labels，設定和授權個人標籤。",
		LangJapanese:           "管理 Bot の個別チャットで /team labels を開き、設定・認証してください。",
		LangSpanish:            "Abre /team labels en privado con el administrador para configurar y autorizar.",
	},

	MsgLabelAuthorize: {
		LangEnglish:            "Open this link to authorize your conversation labels:\n%s\nWaiting for authorization…",
		LangChinese:            "请打开以下链接授权管理会话标签：\n%s\n正在等待你完成授权…",
		LangTraditionalChinese: "請打開以下連結授權管理會話標籤：\n%s\n正在等待你完成授權…",
		LangJapanese:           "会話ラベルの管理を許可してください：\n%s\n認証を待っています…",
		LangSpanish:            "Abre este enlace para autorizar las etiquetas:\n%s\nEsperando autorización…",
	},

	MsgLabelAuthorized: {
		LangEnglish:            "Authorization saved. Open /team labels to choose defaults or retry pending assignments.",
		LangChinese:            "授权已保存。打开 /team labels 选择默认标签或重试待添加的标签。",
		LangTraditionalChinese: "授權已保存。打開 /team labels 選擇預設標籤或重試待添加的標籤。",
		LangJapanese:           "認証を保存しました。/team labels で既定値を選ぶか保留分を再試行してください。",
		LangSpanish:            "Autorización guardada. Abre /team labels para configurar o reintentar pendientes.",
	},

	MsgLabelUsage: {
		LangEnglish:            "/team labels — settings\n/team labels authorize — authorize in private chat\n/team labels retry — retry label assignments only",
		LangChinese:            "/team labels：标签设置\n/team labels authorize：私聊授权\n/team labels retry：仅重试标签添加",
		LangTraditionalChinese: "/team labels：標籤設定\n/team labels authorize：私聊授權\n/team labels retry：僅重試標籤添加",
		LangJapanese:           "/team labels — 設定\n/team labels authorize — 個別認証\n/team labels retry — ラベルのみ再試行",
		LangSpanish:            "/team labels — ajustes\n/team labels authorize — autorizar en privado\n/team labels retry — reintentar etiquetas",
	},

	MsgLabelAuthReady: {
		LangEnglish:            "User authorization saved",
		LangChinese:            "用户身份已授权",
		LangTraditionalChinese: "使用者身分已授權",
		LangJapanese:           "ユーザー認証済み",
		LangSpanish:            "Usuario autorizado",
	},

	MsgLabelAuthButton: {
		LangEnglish:            "Authorize labels",
		LangChinese:            "授权标签管理",
		LangTraditionalChinese: "授權標籤管理",
		LangJapanese:           "ラベル管理を認証",
		LangSpanish:            "Autorizar etiquetas",
	},

	MsgLabelRetry: {
		LangEnglish:            "Retry pending labels",
		LangChinese:            "重试待添加标签",
		LangTraditionalChinese: "重試待添加標籤",
		LangJapanese:           "保留ラベルを再試行",
		LangSpanish:            "Reintentar pendientes",
	},

	MsgLabelPending: {
		LangEnglish:            "Pending labels for %s: %s. Creation is preserved; /team labels retry will only retry labeling.",
		LangChinese:            "%s 的标签待添加：%s。已保留创建结果；/team labels retry 只重试标签。",
		LangTraditionalChinese: "%s 的標籤待添加：%s。已保留建立結果；/team labels retry 只重試標籤。",
		LangJapanese:           "%s のラベルは保留中：%s。作成済みの対象は保持されます。/team labels retry でラベルだけ再試行できます。",
		LangSpanish:            "Etiquetas pendientes para %s: %s. Se conserva lo creado; /team labels retry solo reintenta etiquetas.",
	},

	MsgLabelSave: {
		LangEnglish:            "Save defaults",
		LangChinese:            "保存默认设置",
		LangTraditionalChinese: "保存預設設定",
		LangJapanese:           "既定値を保存",
		LangSpanish:            "Guardar valores",
	},

	MsgLabelBots: {
		LangEnglish:            "New bot labels",
		LangChinese:            "新建 Bot 的标签",
		LangTraditionalChinese: "新建 Bot 的標籤",
		LangJapanese:           "新規 Bot のラベル",
		LangSpanish:            "Etiquetas de bots nuevos",
	},

	MsgLabelProjects: {
		LangEnglish:            "New project group labels",
		LangChinese:            "新建任务群的标签",
		LangTraditionalChinese: "新建任務群的標籤",
		LangJapanese:           "新規プロジェクトグループのラベル",
		LangSpanish:            "Etiquetas de grupos nuevos",
	},

	MsgLabelSaved: {
		LangEnglish:            "Default labels saved. New names will be created or reused when a conversation is labeled.",
		LangChinese:            "默认标签已保存。给新会话添加标签时会创建或复用填写的标签名。",
		LangTraditionalChinese: "預設標籤已保存。給新會話添加標籤時會建立或復用填寫的標籤名。",
		LangJapanese:           "既定のラベルを保存しました。会話への追加時にラベル名を作成または再利用します。",
		LangSpanish:            "Valores guardados. Los nombres se crearán o reutilizarán al etiquetar una conversación.",
	},

	MsgLabelSelection: {
		LangEnglish:            "Labels for this creation",
		LangChinese:            "本次创建的标签",
		LangTraditionalChinese: "本次建立的標籤",
		LangJapanese:           "今回のラベル",
		LangSpanish:            "Etiquetas para esta creación",
	},

	MsgLabelCreationHint: {
		LangEnglish:            "Defaults come from /team labels. Clear selections and new names to skip labeling this time.",
		LangChinese:            "默认值来自 /team labels。清空选择和新标签名称，即可取消本次自动添加。",
		LangTraditionalChinese: "預設值來自 /team labels。清空選擇和新標籤名稱，即可取消本次自動添加。",
		LangJapanese:           "既定値は /team labels で設定します。選択と新規名を空にすると今回は追加しません。",
		LangSpanish:            "Valores de /team labels. Vacía selecciones y nombres nuevos para omitir etiquetas esta vez.",
	},

	MsgLabelWelcome: {
		LangEnglish:            "Your bot is ready. This conversation was opened for your selected labels.",
		LangChinese:            "Bot 已创建，此会话用于归入你选择的标签。",
		LangTraditionalChinese: "Bot 已建立，此會話用於歸入你選擇的標籤。",
		LangJapanese:           "Bot を作成しました。選択したラベルに追加するため、この会話を開きました。",
		LangSpanish:            "Bot creado. Esta conversación se abrió para añadirla a tus etiquetas elegidas.",
	},

	MsgLabelApplied: {
		LangEnglish:            "Labels added to your conversation list.",
		LangChinese:            "已添加到你会话列表中的标签。",
		LangTraditionalChinese: "已添加到你會話列表中的標籤。",
		LangJapanese:           "会話一覧のラベルに追加しました。",
		LangSpanish:            "Etiquetas añadidas a tu lista de conversaciones.",
	},

	MsgLabelText: {
		LangEnglish:            "%s",
		LangChinese:            "%s",
		LangTraditionalChinese: "%s",
		LangJapanese:           "%s",
		LangSpanish:            "%s",
	},

	"team labels": {
		LangEnglish:            "Label settings",
		LangChinese:            "标签设置",
		LangTraditionalChinese: "標籤設定",
		LangJapanese:           "ラベル設定",
		LangSpanish:            "Ajustes de etiquetas",
	},

	MsgQueryPageExpired:         {LangEnglish: "Pagination data is no longer available. Ask the bot to show the result again.", LangChinese: "此卡的分页数据已失效，请让机器人重新展示结果。", LangTraditionalChinese: "此卡的分頁資料已失效，請讓機器人重新顯示結果。", LangJapanese: "ページデータが失効しました。ボットに結果の再表示を依頼してください。", LangSpanish: "Los datos de paginación ya no están disponibles. Pide al bot que muestre el resultado de nuevo."},
	MsgQueryPageFailed:          {LangEnglish: "Could not change page. Please try again.", LangChinese: "翻页更新失败，请重试。", LangTraditionalChinese: "翻頁更新失敗，請重試。", LangJapanese: "ページの更新に失敗しました。再試行してください。", LangSpanish: "No se pudo cambiar de página. Inténtalo de nuevo."},
	MsgQueryPageBusy:            {LangEnglish: "The card is updating. Please try again shortly.", LangChinese: "卡片正在更新，请稍后再点一次。", LangTraditionalChinese: "卡片正在更新，請稍後再點一次。", LangJapanese: "カードを更新中です。少し待って再試行してください。", LangSpanish: "La tarjeta se está actualizando. Inténtalo en unos instantes."},
	MsgDecisionDeadline:         {LangEnglish: "Valid until %s. No response never grants authorization.", LangChinese: "有效至 %s；未回答不会视为授权。", LangTraditionalChinese: "有效至 %s；未回答不會視為授權。", LangJapanese: "有効期限：%s。無回答は許可と見なしません。", LangSpanish: "Válida hasta %s. La ausencia de respuesta nunca autoriza la acción."},
	MsgWorkflowReworkInvalid:    {LangEnglish: "Include one task ID assigned to this member. Only the original requester or project owner can request rework directly; otherwise ask the manager in the group.", LangChinese: "请注明一项属于该成员的任务编号。直接返工需由原请求人或项目负责人发起；其他情况请在群里 @ 管理者处理。", LangTraditionalChinese: "請註明一項屬於該成員的任務編號。直接返工需由原請求人或專案負責人發起；其他情況請在群組 @ 管理者處理。", LangJapanese: "この担当者のタスク ID を一つ指定してください。直接の再作業依頼は元の依頼者または責任者のみ可能です。それ以外はグループの管理者に依頼してください。", LangSpanish: "Indica una tarea de este miembro. Solo el solicitante original o el propietario pueden pedir una revisión directamente; en otros casos contacta al responsable en el grupo."},
	MsgWorkflowReworkTitle:      {LangEnglish: "%s · Rework (attempt %d)", LangChinese: "%s · 返工（第 %d 轮）", LangTraditionalChinese: "%s · 返工（第 %d 輪）", LangJapanese: "%s · 再作業（%d 回目）", LangSpanish: "%s · Revisión (intento %d)"},
	MsgWorkflowReworkInGroup:    {LangEnglish: "This is an earlier task thread. Send rework in the main group, mentioning the manager and including the task ID.", LangChinese: "这是之前一轮的任务话题。请在群主聊天 @ 管理者，带上任务编号提出返工。", LangTraditionalChinese: "這是之前一輪的任務話題。請在群主聊天 @ 管理者，附上任務編號提出返工。", LangJapanese: "これは以前の作業スレッドです。グループで管理者をメンションし、タスク ID と再作業内容を送信してください。", LangSpanish: "Este es un hilo de un intento anterior. Solicita la revisión en el grupo mencionando al responsable e indicando el ID de tarea."},
	MsgWorkflowResultFile:       {LangEnglish: "The complete result is in the group file: %s", LangChinese: "完整结果已保存为群内文件：%s", LangTraditionalChinese: "完整結果已儲存為群組檔案：%s", LangJapanese: "完全な結果はグループのファイルに保存しました：%s", LangSpanish: "El resultado completo está en el archivo del grupo: %s"},
	MsgDecisionAnswer:           {LangEnglish: "Your answer", LangChinese: "补充回答", LangTraditionalChinese: "補充回答", LangJapanese: "回答", LangSpanish: "Tu respuesta"},
	MsgDecisionSubmit:           {LangEnglish: "Submit answer", LangChinese: "提交回答", LangTraditionalChinese: "提交回答", LangJapanese: "回答を送信", LangSpanish: "Enviar respuesta"},
	MsgDecisionExpired:          {LangEnglish: "This request expired or execution stopped. No authorization was granted; these controls are inactive.", LangChinese: "请求已过期或任务已停止，未授予权限，此处操作已失效。", LangTraditionalChinese: "請求已過期或任務已停止，未授予權限，此處操作已失效。", LangJapanese: "要求が期限切れか実行が停止しました。権限は付与されず、操作は無効です。", LangSpanish: "La solicitud caducó o se detuvo la ejecución. No se concedió autorización; los controles están inactivos."},
	MsgExecEmptyAnswer:          {LangEnglish: "Enter an answer before submitting.", LangChinese: "请填写回答后再提交。", LangTraditionalChinese: "請填寫回答後再提交。", LangJapanese: "回答を入力してから送信してください。", LangSpanish: "Escribe una respuesta antes de enviarla."},
	MsgExecHistory:              {LangEnglish: "Execution history", LangChinese: "执行历史", LangTraditionalChinese: "執行歷史", LangJapanese: "実行履歴", LangSpanish: "Historial de ejecución"},
	MsgExecReadArchive:          {LangEnglish: "Read saved details", LangChinese: "查看留存详情", LangTraditionalChinese: "查看留存詳情", LangJapanese: "保存済みの詳細", LangSpanish: "Ver detalles guardados"},
	MsgExecArchived:             {LangEnglish: "Saved execution details. Read-only; prior approval controls are inactive.", LangChinese: "留存的执行详情，仅供阅读，历史授权操作已失效。", LangTraditionalChinese: "留存的執行詳情，僅供閱讀，歷史授權操作已失效。", LangJapanese: "保存済みの実行詳細です。閲覧専用で、以前の許可操作は無効です。", LangSpanish: "Detalles guardados, solo lectura. Los controles de autorización anteriores están inactivos."},
	MsgExecRunning:              {LangEnglish: "Running", LangChinese: "执行中", LangTraditionalChinese: "執行中", LangJapanese: "実行中", LangSpanish: "En curso"},
	MsgExecDone:                 {LangEnglish: "Completed", LangChinese: "已完成", LangTraditionalChinese: "已完成", LangJapanese: "完了", LangSpanish: "Completado"},
	MsgExecFailed:               {LangEnglish: "Failed", LangChinese: "失败", LangTraditionalChinese: "失敗", LangJapanese: "失敗", LangSpanish: "Error"},
	MsgExecStopped:              {LangEnglish: "Stopped", LangChinese: "已停止", LangTraditionalChinese: "已停止", LangJapanese: "停止", LangSpanish: "Detenido"},
	MsgExecExitCode:             {LangEnglish: "Exit code %d", LangChinese: "退出码 %d", LangTraditionalChinese: "結束碼 %d", LangJapanese: "終了コード %d", LangSpanish: "Código de salida %d"},
	MsgExecFullExplanation:      {LangEnglish: "Read full explanation", LangChinese: "查看完整说明", LangTraditionalChinese: "查看完整說明", LangJapanese: "説明をすべて表示", LangSpanish: "Ver explicación completa"},
	MsgExecBack:                 {LangEnglish: "Back to progress", LangChinese: "返回执行进度", LangTraditionalChinese: "返回執行進度", LangJapanese: "進捗に戻る", LangSpanish: "Volver al progreso"},
	MsgExecPrevious:             {LangEnglish: "Previous command", LangChinese: "上一条命令", LangTraditionalChinese: "上一條命令", LangJapanese: "前のコマンド", LangSpanish: "Comando anterior"},
	MsgExecNext:                 {LangEnglish: "Next command", LangChinese: "下一条命令", LangTraditionalChinese: "下一條命令", LangJapanese: "次のコマンド", LangSpanish: "Siguiente comando"},
	MsgExecRefreshCurrent:       {LangEnglish: "Refresh", LangChinese: "刷新", LangTraditionalChinese: "重新整理", LangJapanese: "更新", LangSpanish: "Actualizar"},
	MsgExecRefresh:              {LangEnglish: "New progress · refresh", LangChinese: "有新进度 · 刷新", LangTraditionalChinese: "有新進度 · 重新整理", LangJapanese: "新しい進捗 · 更新", LangSpanish: "Nuevo progreso · actualizar"},
	MsgExecNoOutput:             {LangEnglish: "No output yet", LangChinese: "尚无输出", LangTraditionalChinese: "尚無輸出", LangJapanese: "出力はまだありません", LangSpanish: "Todavía no hay salida"},
	MsgExecNeedsInput:           {LangEnglish: "Your input is needed on the execution card.", LangChinese: "执行卡需要你补充信息。", LangTraditionalChinese: "執行卡需要你補充資訊。", LangJapanese: "実行カードで追加情報が必要です。", LangSpanish: "La tarjeta de ejecución necesita tu respuesta."},
	MsgExecNeedsApproval:        {LangEnglish: "An operation on the execution card needs your authorization.", LangChinese: "执行卡有一项操作需要你授权。", LangTraditionalChinese: "執行卡有一項操作需要你授權。", LangJapanese: "実行カードの操作に許可が必要です。", LangSpanish: "Una operación en la tarjeta necesita tu autorización."},
	MsgExecReminderFailed:       {LangEnglish: "Private reminder not delivered. Handle this request here or retry the reminder.", LangChinese: "私聊提醒未送达，可在此处理或重试提醒。", LangTraditionalChinese: "私聊提醒未送達，可在此處理或重試提醒。", LangJapanese: "個別通知が未送信です。ここで処理するか再試行してください。", LangSpanish: "Aviso privado no entregado. Responde aquí o reintenta el aviso."},
	MsgExecRetryReminder:        {LangEnglish: "Retry private reminder", LangChinese: "重试私聊提醒", LangTraditionalChinese: "重試私聊提醒", LangJapanese: "個別通知を再送信", LangSpanish: "Reintentar aviso privado"},
	MsgExecOpenOriginal:         {LangEnglish: "Open execution card", LangChinese: "返回原执行卡", LangTraditionalChinese: "返回原執行卡", LangJapanese: "元の実行カードを開く", LangSpanish: "Abrir tarjeta de ejecución"},
	MsgExecSummaryHint:          {LangEnglish: "%d commands · Click a command for details", LangChinese: "%d 条命令 · 点击命令查看详情", LangTraditionalChinese: "%d 條命令 · 點擊命令查看詳情", LangJapanese: "%d 件のコマンド · クリックして詳細を表示", LangSpanish: "%d comandos · Haz clic para ver detalles"},
	MsgExecCommandLabel:         {LangEnglish: "Full command", LangChinese: "完整命令", LangTraditionalChinese: "完整命令", LangJapanese: "完全なコマンド", LangSpanish: "Comando completo"},
	MsgExecOutputLabel:          {LangEnglish: "Execution output", LangChinese: "执行结果", LangTraditionalChinese: "執行結果", LangJapanese: "実行結果", LangSpanish: "Resultado de ejecución"},
	MsgNativeExecutionFile:      {LangEnglish: "Complete output in this thread: %s", LangChinese: "完整内容已保存在本话题文件：%s", LangTraditionalChinese: "完整內容已儲存在本話題檔案：%s", LangJapanese: "完全な出力はこのスレッドのファイルにあります：%s", LangSpanish: "Contenido completo en el archivo de este hilo: %s"},
	MsgNativeExecutionBuffering: {LangEnglish: "Receiving a large output; the complete file will follow in this thread.", LangChinese: "正在接收较长内容，完成后将完整文件发送到本话题。", LangTraditionalChinese: "正在接收較長內容，完成後將完整檔案傳送到本話題。", LangJapanese: "長い出力を受信中です。完了後、このスレッドに完全なファイルを送信します。", LangSpanish: "Recibiendo contenido extenso; el archivo completo se enviará a este hilo."},
	MsgQueryDeliveryIncomplete:  {LangEnglish: "Some answer content could not be delivered because creating continuation cards failed. Please retry later.", LangChinese: "续卡发送失败，部分结论尚未送达，请稍后重试。", LangTraditionalChinese: "續卡傳送失敗，部分結論尚未送達，請稍後重試。", LangJapanese: "続きのカードを送信できず、回答の一部が未配信です。後でもう一度お試しください。", LangSpanish: "No se pudieron enviar las tarjetas siguientes; falta parte de la respuesta. Inténtelo de nuevo más tarde."},
	MsgQuerySuperseded:          {LangEnglish: "Earlier output retained for reference. See the updated result.", LangChinese: "此前输出保留供回看，请以更新后的结果为准。", LangTraditionalChinese: "先前輸出保留供回看，請以更新後的結果為準。", LangJapanese: "以前の出力を参照用に保存しています。更新後の結果をご確認ください。", LangSpanish: "Salida anterior conservada como referencia. Consulte el resultado actualizado."},
	MsgQueryRestored:            {LangEnglish: "Content saved before the service restarted. This card is for viewing only; previous approval controls are inactive.", LangChinese: "这是服务重启前保存的内容，仅供查看；原审批按钮已失效。", LangTraditionalChinese: "這是服務重新啟動前儲存的內容，僅供查看；原審批按鈕已失效。", LangJapanese: "サービス再起動前に保存された閲覧用の内容です。以前の承認ボタンは無効です。", LangSpanish: "Contenido guardado antes de reiniciar el servicio, solo para consulta. Los controles de aprobación anteriores están inactivos."},
	MsgProjectModelScope: {
		LangEnglish:            "Scope: this bot in the current project only. Use /project bot to view the model source and restore the bot default.",
		LangChinese:            "作用范围：当前 Bot 在此项目中的会话。/project bot 可查看模型来源并恢复 Bot 默认值。",
		LangTraditionalChinese: "作用範圍：目前 Bot 在此專案中的會話。/project bot 可查看模型來源並恢復 Bot 預設值。",
		LangJapanese:           "対象：現在のプロジェクトのこの Bot のみ。/project bot で設定元の確認とデフォルトの復元ができます。",
		LangSpanish:            "Ámbito: este bot en el proyecto actual. /project bot muestra el origen y permite restaurar el modelo del bot.",
	},
	MsgProjectModelChanged: {
		LangEnglish:            "Model switched to `%s` for this bot in the current project only.",
		LangChinese:            "模型已切换为 `%s`，仅影响当前 Bot 在此项目中的会话。",
		LangTraditionalChinese: "模型已切換為 `%s`，僅影響目前 Bot 在此專案中的會話。",
		LangJapanese:           "現在のプロジェクトのこの Bot のモデルを `%s` に変更しました。",
		LangSpanish:            "Modelo cambiado a `%s` solo para este bot en el proyecto actual.",
	},
	MsgProjectSettingsTitle: {
		LangEnglish:            "Project settings · %s",
		LangChinese:            "项目设置 · %s",
		LangTraditionalChinese: "專案設定 · %s",
		LangJapanese:           "プロジェクト設定 · %s",
		LangSpanish:            "Configuración del proyecto · %s",
	},
	MsgProjectSettingsBody: {
		LangEnglish:            "Scope: current project group\nProject: %s\nGoal: %s\nTeam: %s\nManager: %s\nMembers: %s\nCode directories:\n%s",
		LangChinese:            "作用范围：当前项目群\n项目 ID：%s\n项目目标：%s\n所属团队：%s\n管理者：%s\n参与成员：%s\n代码目录：\n%s",
		LangTraditionalChinese: "作用範圍：目前專案群\n專案 ID：%s\n專案目標：%s\n所屬團隊：%s\n管理者：%s\n參與成員：%s\n程式碼目錄：\n%s",
		LangJapanese:           "対象：現在のプロジェクトグループ\nID：%s\n目的：%s\nチーム：%s\n管理者：%s\nメンバー：%s\nコードディレクトリ：\n%s",
		LangSpanish:            "Ámbito: grupo del proyecto actual\nProyecto: %s\nObjetivo: %s\nEquipo: %s\nAdministrador: %s\nMiembros: %s\nDirectorios de código:\n%s",
	},
	MsgProjectDirectoryScope: {
		LangEnglish:            "All project bots use these directories. Changing directories or the goal starts a new context; previous history stays saved.",
		LangChinese:            "所有参与 Bot 共用此项目目录配置。修改目录范围或目标后开启新上下文，原历史保留。",
		LangTraditionalChinese: "所有參與 Bot 共用此專案目錄設定。修改目錄範圍或目標後開啟新上下文，原歷史保留。",
		LangJapanese:           "全メンバーに共通のディレクトリ設定です。ディレクトリや目的を変更すると新しいコンテキストになります。履歴は保存されます。",
		LangSpanish:            "Los bots comparten estos directorios. Cambiar los directorios u objetivo inicia un contexto nuevo y conserva el historial anterior.",
	},
	MsgProjectEditInfo: {
		LangEnglish:            "Edit project info",
		LangChinese:            "编辑项目信息",
		LangTraditionalChinese: "編輯專案資訊",
		LangJapanese:           "プロジェクト情報を編集",
		LangSpanish:            "Editar información",
	},
	MsgProjectPrimaryDirectory: {
		LangEnglish:            "Primary directory",
		LangChinese:            "主工作目录",
		LangTraditionalChinese: "主工作目錄",
		LangJapanese:           "主ディレクトリ",
		LangSpanish:            "Directorio principal",
	},
	MsgProjectAdditionalDirectories: {
		LangEnglish:            "Additional directories (one absolute path per line)",
		LangChinese:            "附加目录（每行一个绝对路径）",
		LangTraditionalChinese: "附加目錄（每行一個絕對路徑）",
		LangJapanese:           "追加ディレクトリ（絶対パスを1行に1つ）",
		LangSpanish:            "Directorios adicionales (una ruta absoluta por línea)",
	},
	MsgProjectDirectoriesUnsupported: {
		LangEnglish:            "Bot %s is unavailable or its backend does not support multiple project directories.",
		LangChinese:            "Bot %s 未就绪，或其后端不支持项目多目录。",
		LangTraditionalChinese: "Bot %s 尚未就緒，或其後端不支援專案多目錄。",
		LangJapanese:           "Bot %s が未起動か、複数ディレクトリに非対応です。",
		LangSpanish:            "El bot %s no está disponible o no admite varios directorios.",
	},
	MsgProjectDirectoriesHelp: {
		LangEnglish:            "Set one primary working directory and any additional directories. Use existing absolute paths; leave additional directories empty to remove them. Approval mode stays unchanged. Text commands: /project directory <primary>; /project directories add <path>; /project directories remove <additional path>; /project directories clear.",
		LangChinese:            "设置一个主工作目录和多个附加目录。请填写已有目录的绝对路径；清空附加目录可移除全部附加目录，审批模式保持原设置。文字命令：/project directory <主目录>；/project directories add <路径>；/project directories remove <附加路径>；/project directories clear。",
		LangTraditionalChinese: "設定一個主工作目錄及多個附加目錄。請填寫現有目錄的絕對路徑；清空附加目錄可移除所有附加目錄，審批模式保持原設定。文字指令：/project directory <主目錄>；/project directories add <路徑>；/project directories remove <附加路徑>；/project directories clear。",
		LangJapanese:           "主ディレクトリと追加ディレクトリを既存の絶対パスで設定します。追加欄を空にすると削除します。承認モードは維持されます。コマンド：/project directory <主パス>、/project directories add <パス>、/project directories remove <追加パス>、/project directories clear。",
		LangSpanish:            "Define un directorio principal y rutas absolutas adicionales existentes. Vacía el campo adicional para quitarlas. Se mantiene el modo de aprobación. Comandos: /project directory <principal>; /project directories add <ruta>; /project directories remove <ruta adicional>; /project directories clear.",
	},
	MsgProjectEditDirectory: {
		LangEnglish:            "Change code directory",
		LangChinese:            "修改代码目录",
		LangTraditionalChinese: "修改程式碼目錄",
		LangJapanese:           "コードディレクトリを変更",
		LangSpanish:            "Cambiar directorio",
	},
	MsgProjectViewMembers: {
		LangEnglish:            "View members",
		LangChinese:            "查看成员",
		LangTraditionalChinese: "查看成員",
		LangJapanese:           "メンバーを表示",
		LangSpanish:            "Ver miembros",
	},
	MsgProjectBotSettings: {
		LangEnglish:            "Bot settings",
		LangChinese:            "Bot 设置",
		LangTraditionalChinese: "Bot 設定",
		LangJapanese:           "Bot 設定",
		LangSpanish:            "Configuración del bot",
	},
	MsgProjectSettingsUsage: {
		LangEnglish:            "In this project group:\n/project settings\n/project name <name>\n/project goal <goal>\n/project directory\n/project directories add|remove <path>\n/project directories clear\n/project directory <existing absolute directory>\n/project bot\n/project model reset\n/workspace and /dir open these same settings. Extra code directories remain unchanged.",
		LangChinese:            "在当前项目群中使用：\n/project settings：查看项目设置\n/project name <项目名称>\n/project goal <项目目标>\n/project directory\n/project directories add|remove <path>\n/project directories clear\n/project directory <已有目录的绝对路径>\n/project bot：查看当前 Bot 设置\n/project model reset：恢复 Bot 默认模型\n/workspace、/dir 打开同一设置。修改主目录时，附加目录保持不变。",
		LangTraditionalChinese: "在目前專案群中使用：\n/project settings：查看專案設定\n/project name <專案名稱>\n/project goal <專案目標>\n/project directory\n/project directories add|remove <path>\n/project directories clear\n/project directory <現有目錄的絕對路徑>\n/project bot：查看目前 Bot 設定\n/project model reset：恢復 Bot 預設模型\n/workspace、/dir 開啟相同設定。修改主目錄時，附加目錄保持不變。",
		LangJapanese:           "このプロジェクトグループで：\n/project settings\n/project name <名前>\n/project goal <目的>\n/project directory\n/project directories add|remove <path>\n/project directories clear\n/project directory <既存ディレクトリの絶対パス>\n/project bot\n/project model reset\n/workspace と /dir も同じ設定を開きます。追加ディレクトリは変わりません。",
		LangSpanish:            "En el grupo actual:\n/project settings\n/project name <nombre>\n/project goal <objetivo>\n/project directory\n/project directories add|remove <path>\n/project directories clear\n/project directory <directorio absoluto existente>\n/project bot\n/project model reset\n/workspace y /dir abren la misma configuración. Los directorios adicionales no cambian.",
	},
	MsgProjectGroupRequired: {
		LangEnglish:            "Open the project group to configure it. Use /project list to find your projects.",
		LangChinese:            "请进入对应项目群修改设置。使用 /project list 查看项目。",
		LangTraditionalChinese: "請進入對應專案群修改設定。使用 /project list 查看專案。",
		LangJapanese:           "対象のプロジェクトグループで設定してください。/project list で一覧を確認できます。",
		LangSpanish:            "Abre el grupo del proyecto para configurarlo. Usa /project list para ver tus proyectos.",
	},
	MsgProjectBusy: {
		LangEnglish:            "A bot is working on this project. Finish or stop its task before changing settings.",
		LangChinese:            "此项目有 Bot 正在处理任务。请等待完成或停止任务后再修改设置。",
		LangTraditionalChinese: "此專案有 Bot 正在處理任務。請等待完成或停止任務後再修改設定。",
		LangJapanese:           "このプロジェクトで Bot が作業中です。完了または停止してから設定を変更してください。",
		LangSpanish:            "Un bot está trabajando en este proyecto. Termina o detén la tarea antes de cambiar la configuración.",
	},
	MsgProjectDirectoryConflict: {
		LangEnglish:            "Directory settings conflict. Project directory: %s\nLegacy directories:\n%s\nChoose explicitly with /project directory <absolute directory>.",
		LangChinese:            "目录配置存在冲突。项目目录：%s\n原有覆盖目录：\n%s\n请用 /project directory <绝对目录> 明确选择一次。",
		LangTraditionalChinese: "目錄設定存在衝突。專案目錄：%s\n原有覆寫目錄：\n%s\n請用 /project directory <絕對目錄> 明確選擇一次。",
		LangJapanese:           "ディレクトリ設定が競合しています。プロジェクト：%s\n従来の設定：\n%s\n/project directory <絶対パス> で選択してください。",
		LangSpanish:            "Conflicto de directorios. Proyecto: %s\nDirectorios anteriores:\n%s\nElige con /project directory <directorio absoluto>.",
	},
	MsgProjectSettingsSaved: {
		LangEnglish:            "Project settings saved.",
		LangChinese:            "项目设置已保存。",
		LangTraditionalChinese: "專案設定已儲存。",
		LangJapanese:           "プロジェクト設定を保存しました。",
		LangSpanish:            "Configuración del proyecto guardada.",
	},
	MsgProjectMembersBody: {
		LangEnglish:            "Manager: %s\nProject members: %s\n/team members shows team roles. Team role changes affect all projects of that bot.",
		LangChinese:            "管理者：%s\n当前项目成员：%s\n/team members 查看团队身份。团队身份修改会影响该 Bot 参与的所有项目。",
		LangTraditionalChinese: "管理者：%s\n目前專案成員：%s\n/team members 查看團隊身分。團隊身分修改會影響該 Bot 參與的所有專案。",
		LangJapanese:           "管理者：%s\nプロジェクトメンバー：%s\n/team members で役割を確認できます。役割変更は Bot の全プロジェクトに適用されます。",
		LangSpanish:            "Administrador: %s\nMiembros del proyecto: %s\n/team members muestra los roles. Cambiar un rol afecta a todos los proyectos del bot.",
	},
	MsgProjectModelInherited: {
		LangEnglish:            "Inherited from bot default",
		LangChinese:            "继承 Bot 默认值",
		LangTraditionalChinese: "繼承 Bot 預設值",
		LangJapanese:           "Bot のデフォルトを継承",
		LangSpanish:            "Heredado del bot",
	},
	MsgProjectModelOverride: {
		LangEnglish:            "Current project override",
		LangChinese:            "当前项目覆盖",
		LangTraditionalChinese: "目前專案覆寫",
		LangJapanese:           "現在のプロジェクト設定",
		LangSpanish:            "Valor del proyecto actual",
	},
	MsgProjectBotSettingsBody: {
		LangEnglish:            "Bot: %s\nBackend: %s\nModel: %s\nSource: %s\nModel changes here affect this bot in the current project only.",
		LangChinese:            "Bot：%s\n执行平台：%s\n模型：%s\n来源：%s\n在这里修改模型，仅影响当前 Bot 在此项目中的会话。",
		LangTraditionalChinese: "Bot：%s\n執行平台：%s\n模型：%s\n來源：%s\n在此修改模型，僅影響目前 Bot 在此專案中的會話。",
		LangJapanese:           "Bot：%s\n実行環境：%s\nモデル：%s\n設定元：%s\nモデル変更は現在のプロジェクトのこの Bot のみに適用されます。",
		LangSpanish:            "Bot: %s\nMotor: %s\nModelo: %s\nOrigen: %s\nEl modelo solo cambia para este bot en el proyecto actual.",
	},
	MsgProjectChangeModel: {
		LangEnglish:            "Change project model",
		LangChinese:            "修改当前项目模型",
		LangTraditionalChinese: "修改目前專案模型",
		LangJapanese:           "プロジェクトのモデルを変更",
		LangSpanish:            "Cambiar modelo del proyecto",
	},
	MsgProjectResetModel: {
		LangEnglish:            "Restore bot default",
		LangChinese:            "恢复 Bot 默认值",
		LangTraditionalChinese: "恢復 Bot 預設值",
		LangJapanese:           "Bot のデフォルトに戻す",
		LangSpanish:            "Restaurar valor del bot",
	},
	MsgProjectSharedSettings: {
		LangEnglish:            "Shared bot settings: account, backend and team role affect all projects of this bot. Configure them in the manager private chat or existing bot configuration.",
		LangChinese:            "Bot 共享设置：账号、执行平台、团队身份会影响该 Bot 的所有项目。请在管理者私聊或原有 Bot 配置中修改。",
		LangTraditionalChinese: "Bot 共用設定：帳號、執行平台、團隊身分會影響該 Bot 的所有專案。請在管理者私聊或原有 Bot 設定中修改。",
		LangJapanese:           "共有設定：アカウント、実行環境、チームの役割は全プロジェクトに適用されます。管理者との個別チャットまたは Bot 設定で変更してください。",
		LangSpanish:            "Configuración compartida: cuenta, motor y rol afectan a todos los proyectos del bot. Cámbialos en el chat privado del administrador o en la configuración del bot.",
	},
	"project settings": {
		LangEnglish:            "View settings for the current project group",
		LangChinese:            "查看当前项目群的统一设置",
		LangTraditionalChinese: "查看目前專案群的統一設定",
		LangJapanese:           "現在のプロジェクト設定を表示",
		LangSpanish:            "Ver configuración del proyecto actual",
	},

	MsgHelpTeamSection: {
		LangEnglish:            "Team Management",
		LangChinese:            "团队管理",
		LangTraditionalChinese: "團隊管理",
		LangJapanese:           "チーム管理",
		LangSpanish:            "Gestión del equipo",
	},
	MsgHelpProjectSection: {
		LangEnglish:            "Project Management",
		LangChinese:            "项目管理",
		LangTraditionalChinese: "專案管理",
		LangJapanese:           "プロジェクト管理",
		LangSpanish:            "Gestión de proyectos",
	},
	"team": {
		LangEnglish:            "Manage bots and their roles",
		LangChinese:            "管理 Bot 成员与身份",
		LangTraditionalChinese: "管理 Bot 成員與身分",
		LangJapanese:           "Bot と役割を管理",
		LangSpanish:            "Gestionar bots y roles",
	},
	"project": {
		LangEnglish:            "Manage projects and project groups",
		LangChinese:            "管理项目与项目群",
		LangTraditionalChinese: "管理專案與專案群",
		LangJapanese:           "プロジェクトとグループを管理",
		LangSpanish:            "Gestionar proyectos y grupos",
	},
	"team members": {
		LangEnglish:            "View members and configure identities",
		LangChinese:            "查看成员并配置身份",
		LangTraditionalChinese: "查看成員並設定身分",
		LangJapanese:           "メンバーの確認と役割・指示の設定",
		LangSpanish:            "Ver miembros y configurar identidades",
	},
	"team bot create": {
		LangEnglish:            "Create a bot and configure its role and backend (private chat)",
		LangChinese:            "私聊扫码创建 Bot，配置角色与执行平台",
		LangTraditionalChinese: "私聊掃碼建立 Bot，設定角色與執行平台",
		LangJapanese:           "個別チャットで Bot を作成し役割と実行環境を設定",
		LangSpanish:            "Crear bot, rol y motor en un chat privado",
	},
	"project list": {
		LangEnglish:            "List projects and their groups",
		LangChinese:            "查看项目及对应群聊",
		LangTraditionalChinese: "查看專案及對應群聊",
		LangJapanese:           "プロジェクトとグループを一覧表示",
		LangSpanish:            "Ver proyectos y sus grupos",
	},
	"project create": {
		LangEnglish:            "Create a project group with its manager and working directory",
		LangChinese:            "创建项目群，关联管理者、成员和工作目录",
		LangTraditionalChinese: "建立專案群，關聯管理者、成員與工作目錄",
		LangJapanese:           "管理者と作業ディレクトリを含むプロジェクトを作成",
		LangSpanish:            "Crear proyecto con administrador y directorio de trabajo",
	},
	MsgProjectManagerOnly: {
		LangEnglish:            "Use the manager bot for project changes.",
		LangChinese:            "请通过管理者 Bot 创建或修改项目。",
		LangTraditionalChinese: "請透過管理者 Bot 建立或修改專案。",
		LangJapanese:           "プロジェクトの変更は管理者 Bot で行ってください。",
		LangSpanish:            "Usa el bot administrador para crear o modificar proyectos.",
	},
	MsgProjectGroupUsage: {
		LangEnglish:            "/project list — list projects\n/project create <id> <absolute-directory[,directory]> [bot,bot] [goal]\nEach project has its own group including the manager. Workspace is the code directory; the group selects it automatically.",
		LangChinese:            "/project list：查看项目\n/project create <项目ID> <绝对目录[,目录]> [bot,bot] [项目说明]\n每个项目有独立群聊，自动包含管理者。Workspace 表示代码工作目录，由所在项目群自动选择。",
		LangTraditionalChinese: "/project list：查看專案\n/project create <專案ID> <絕對目錄[,目錄]> [bot,bot] [專案說明]\n每個專案有獨立群聊，自動包含管理者。Workspace 表示程式碼工作目錄，由所在專案群自動選擇。",
		LangJapanese:           "/project list — プロジェクト一覧\n/project create <id> <絶対パス[,パス]> [bot,bot] [目的]\n各プロジェクトに管理者を含む専用グループを作成します。Workspace はコードの作業ディレクトリで、グループから自動選択されます。",
		LangSpanish:            "/project list — ver proyectos\n/project create <id> <ruta-absoluta[,ruta]> [bot,bot] [objetivo]\nCada proyecto tiene un grupo con su administrador. Workspace es el directorio de código, elegido automáticamente por el grupo.",
	},
	MsgProjectCLIUsage: {
		LangEnglish:            "Project management:\n  project --config <config.toml> --manager <bot> list\n  project --config <config.toml> --manager <bot> create <id> <absolute-directory> [bot,bot] [goal]",
		LangChinese:            "项目管理：\n  project --config <config.toml> --manager <bot> list\n  project --config <config.toml> --manager <bot> create <项目ID> <绝对目录> [bot,bot] [项目说明]",
		LangTraditionalChinese: "專案管理：\n  project --config <config.toml> --manager <bot> list\n  project --config <config.toml> --manager <bot> create <專案ID> <絕對目錄> [bot,bot] [專案說明]",
		LangJapanese:           "プロジェクト管理：\n  project --config <config.toml> --manager <bot> list\n  project --config <config.toml> --manager <bot> create <id> <絶対パス> [bot,bot] [目的]",
		LangSpanish:            "Gestión de proyectos:\n  project --config <config.toml> --manager <bot> list\n  project --config <config.toml> --manager <bot> create <id> <ruta-absoluta> [bot,bot] [objetivo]",
	},

	MsgTeamMembersTitle: {
		LangEnglish:            "Members",
		LangChinese:            "成员",
		LangTraditionalChinese: "成員",
		LangJapanese:           "メンバー",
		LangSpanish:            "Miembros",
	},
	MsgTeamMemberEdit: {
		LangEnglish:            "Modify",
		LangChinese:            "修改",
		LangTraditionalChinese: "修改",
		LangJapanese:           "変更",
		LangSpanish:            "Modificar",
	},
	MsgTeamMemberFormTitle: {
		LangEnglish:            "Edit identity: %s",
		LangChinese:            "配置身份：%s",
		LangTraditionalChinese: "設定身分：%s",
		LangJapanese:           "役割・指示を編集: %s",
		LangSpanish:            "Editar identidad: %s",
	},
	MsgTeamMemberFormHint: {
		LangEnglish:            "Changes apply to this bot in all its projects after a service restart. The team manager keeps the Manager role.",
		LangChinese:            "修改适用于该 Bot 参与的所有项目，保存后重启服务生效。团队管理者的角色固定为管理者。",
		LangTraditionalChinese: "修改適用於該 Bot 參與的所有專案，儲存後重啟服務生效。團隊管理者的角色固定為管理者。",
		LangJapanese:           "変更はサービス再起動後、この Bot の全プロジェクトに適用されます。チーム管理者の役割は固定です。",
		LangSpanish:            "Los cambios se aplican a todos los proyectos de este bot al reiniciar el servicio. El administrador del equipo conserva su rol.",
	},
	MsgTeamMemberNotFound: {
		LangEnglish:            "This bot is no longer a member of this team. Reopen Members.",
		LangChinese:            "当前团队中找不到该 Bot，请重新打开 Members。",
		LangTraditionalChinese: "目前團隊中找不到該 Bot，請重新開啟 Members。",
		LangJapanese:           "この Bot はチームに存在しません。メンバー一覧を開き直してください。",
		LangSpanish:            "Este bot no pertenece al equipo. Vuelve a abrir Miembros.",
	},
	MsgTeamMemberFormUnsupported: {
		LangEnglish:            "This platform does not support identity forms. Use Members on a platform with form support.",
		LangChinese:            "当前平台不支持身份表单，请在支持表单的平台通过 Members 配置。",
		LangTraditionalChinese: "目前平台不支援身分表單，請在支援表單的平台透過 Members 設定。",
		LangJapanese:           "このプラットフォームは編集フォームに対応していません。対応するプラットフォームでメンバー一覧を開いてください。",
		LangSpanish:            "Esta plataforma no admite formularios de identidad. Usa Miembros en una plataforma compatible.",
	},
	MsgFormSave: {
		LangEnglish:            "Save",
		LangChinese:            "保存",
		LangTraditionalChinese: "儲存",
		LangJapanese:           "保存",
		LangSpanish:            "Guardar",
	},
	MsgFormBotIDHint: {
		LangEnglish:            "Choose a unique internal name, such as developer-a, for team and project references. This is not the Feishu App ID or the bot display name.",
		LangChinese:            "给 Bot 起一个唯一的内部名称，例如 developer-a，团队和项目通过它引用 Bot。这里不是飞书 App ID，也不是飞书中展示的机器人名称。",
		LangTraditionalChinese: "為 Bot 取一個唯一的內部名稱，例如 developer-a，團隊與專案透過它引用 Bot。這不是飛書 App ID，也不是飛書中顯示的機器人名稱。",
		LangJapanese:           "developer-a など、チームやプロジェクトから参照する一意の内部名を指定します。Feishu App ID や Bot の表示名とは別です。",
		LangSpanish:            "Elige un nombre interno único, como developer-a, para identificar al bot en equipos y proyectos. Es distinto del App ID de Feishu y del nombre visible del bot.",
	},
	MsgFormModify: {
		LangEnglish:            "Modify",
		LangChinese:            "修改",
		LangTraditionalChinese: "修改",
		LangJapanese:           "変更",
		LangSpanish:            "Modificar",
	},
	MsgBotProfileEditTitle: {
		LangEnglish:            "Modify bot",
		LangChinese:            "修改 Bot",
		LangTraditionalChinese: "修改 Bot",
		LangJapanese:           "Bot を変更",
		LangSpanish:            "Modificar bot",
	},
	MsgBotProfileEditHint: {
		LangEnglish:            "The Feishu name is read-only. Role, backend and instructions take effect across its projects after a service restart. The team manager keeps the Manager role.",
		LangChinese:            "飞书名称仅展示，不可在此修改。角色、执行后端和身份说明保存后重启服务生效，适用于该 Bot 的所有项目。管理者角色固定。",
		LangTraditionalChinese: "飛書名稱僅供顯示，無法在此修改。角色、執行後端與身分說明儲存後重啟服務生效，適用於該 Bot 的所有專案。管理者角色固定。",
		LangJapanese:           "Feishu の名前は表示専用です。役割・実行環境・指示はサービス再起動後、全プロジェクトに適用されます。管理者の役割は固定です。",
		LangSpanish:            "El nombre de Feishu es de solo lectura. El rol, motor e instrucciones se aplican a todos sus proyectos tras reiniciar el servicio. El rol del administrador es fijo.",
	},
	MsgModelRepositoryPolicyDenied: {
		LangEnglish:            "The selected model is not allowed for this repository's classification (4037). Select an organization-approved model supporting L4 repositories before retrying. Waiting or repeatedly retrying will not resolve this restriction.",
		LangChinese:            "所选模型不支持当前仓库的密级（错误码 4037）。请切换到组织批准、支持 L4 仓库的模型后再重试。单纯等待或重复重试无法解除此限制。",
		LangTraditionalChinese: "所選模型不支援目前儲存庫的密級（錯誤碼 4037）。請切換至組織核准、支援 L4 儲存庫的模型後再重試。單純等待或重複重試無法解除此限制。",
		LangJapanese:           "選択したモデルはこのリポジトリの機密区分に対応していません（4037）。組織が承認した L4 対応モデルに切り替えてから再試行してください。待機や繰り返しの再試行では解決しません。",
		LangSpanish:            "El modelo seleccionado no está permitido para la clasificación de este repositorio (4037). Selecciona un modelo aprobado por la organización que admita repositorios L4 antes de reintentar. Esperar o repetir la solicitud no elimina esta restricción.",
	},
	MsgTeamRoleCreationContext: {
		LangEnglish:            "Project: %s (%s)\nRequested role: %s\nExecution backend: %s\nReason and expected deliverable: %s",
		LangChinese:            "所属项目：%s（%s）\n拟创建角色：%s\n执行后端：%s\n创建原因与预期交付：%s",
		LangTraditionalChinese: "所屬專案：%s（%s）\n擬建立角色：%s\n執行後端：%s\n建立原因與預期交付：%s",
		LangJapanese:           "プロジェクト：%s（%s）\n作成する役割：%s\n実行環境：%s\n作成理由と期待する成果物：%s",
		LangSpanish:            "Proyecto: %s (%s)\nRol solicitado: %s\nMotor: %s\nMotivo y entrega esperada: %s",
	},
	MsgTeamRoleAuthorize: {
		LangEnglish:            "No existing bot with this role can be reused in this team. Open the link to register a new Feishu bot; choose its display name on the registration page. The service will request the role's messaging, group access and card permissions. After authorization, permission checks and startup succeed, it will join the project's existing group and become available for task assignment. Only continue if the purpose above is correct; no authorization means no new bot joins the project. This link expires after 10 minutes.\n%s",
		LangChinese:            "当前团队没有可复用的同角色 Bot。请打开链接注册新的飞书 Bot，并在注册页设置它的显示名称。系统会按角色申请消息收发、群访问和卡片相关权限；完成授权、权限核验和启动后，该 Bot 会加入上述项目现有群，等待管理者派工。请核对以上用途后再授权；不授权则不会有新 Bot 加入项目。链接等待时间为 10 分钟。\n%s",
		LangTraditionalChinese: "目前團隊沒有可重用的同角色 Bot。請開啟連結註冊新的飛書 Bot，並在註冊頁設定顯示名稱。系統會依角色申請訊息收發、群組存取與卡片相關權限；授權、權限核驗及啟動成功後，Bot 會加入上述專案現有群組，等待管理者派工。請確認以上用途後再授權；不授權就不會有新 Bot 加入專案。連結等待時間為 10 分鐘。\n%s",
		LangJapanese:           "チーム内に再利用できる同じ役割の Bot がありません。リンクから新しい Feishu Bot を登録し、登録画面で表示名を設定してください。役割に応じたメッセージ・グループアクセス・カード権限が申請されます。認証、権限確認、起動の成功後、既存のプロジェクトグループに参加してタスク割り当てを待ちます。上記の用途を確認してから認証してください。認証しなければ新しい Bot はプロジェクトに参加しません。待機時間は 10 分です。\n%s",
		LangSpanish:            "El equipo no tiene un bot de este rol que se pueda reutilizar. Abre el enlace para registrar uno nuevo en Feishu y elegir su nombre. Se solicitarán permisos de mensajes, acceso a grupos y tarjetas según el rol. Tras autorizar, verificar permisos e iniciar, se unirá al grupo existente del proyecto y podrá recibir tareas. Autoriza solo si el propósito indicado es correcto; sin autorización no se añadirá un bot al proyecto. La espera caduca en 10 minutos.\n%s",
	},
	MsgTeamRoleReasonRequired: {
		LangEnglish:            "Creating a new role requires --reason (1–2000 characters): explain why it is needed, its concrete task and expected deliverable to the owner.",
		LangChinese:            "新建角色必须提供 --reason（1–2000 字），向所有者说明为什么需要该角色、具体任务和预期交付。",
		LangTraditionalChinese: "建立角色必須提供 --reason（1–2000 字），向所有者說明需要該角色的原因、具體任務與預期交付。",
		LangJapanese:           "新しい役割には --reason（1〜2000 文字）が必要です。必要な理由、具体的なタスク、期待する成果物を所有者に説明してください。",
		LangSpanish:            "Crear un rol requiere --reason (1–2000 caracteres): explica al propietario por qué es necesario, su tarea concreta y la entrega esperada.",
	},
	MsgTeamRoleMembershipFailed: {
		LangEnglish:            "Project group membership confirmation failed for bot %s (application %s)",
		LangChinese:            "Bot %s（应用 %s）的项目群入群确认失败",
		LangTraditionalChinese: "Bot %s（應用程式 %s）的專案群組加入確認失敗",
		LangJapanese:           "Bot %s（アプリ %s）のプロジェクトグループ参加確認に失敗しました",
		LangSpanish:            "No se pudo confirmar la pertenencia al grupo del proyecto del bot %s (aplicación %s)",
	},
	MsgTeamRoleProvisionFailed: {
		LangEnglish:            "This role was not confirmed ready in the project. Registration may have expired, or authorization, permissions, startup or group admission may have failed. The manager has been notified; wait for a new request with its reason before authorizing again.",
		LangChinese:            "该角色尚未在项目中就绪。可能是注册等待超时，或授权、权限核验、启动、入群环节未完成。已通知管理者处理；再次授权前，请以附有原因的新请求为准。",
		LangTraditionalChinese: "該角色尚未在專案中就緒。可能是註冊等待逾時，或授權、權限核驗、啟動、入群環節未完成。已通知管理者處理；再次授權前，請以附有原因的新請求為準。",
		LangJapanese:           "この役割はプロジェクトで準備完了になっていません。登録待機の期限切れ、認証・権限確認・起動・参加の未完了が考えられます。管理者に通知しました。再認証は理由を含む新しい依頼を確認してから行ってください。",
		LangSpanish:            "No se confirmó que este rol esté listo en el proyecto. El registro puede haber caducado o pueden faltar autorización, permisos, inicio o admisión al grupo. Se notificó al administrador; espera una nueva solicitud con su motivo antes de autorizar otra vez.",
	},
	MsgBotAuthorizeAutomatic: {
		LangEnglish:            "Open the link to complete the Feishu bot registration.\n%s",
		LangChinese:            "请打开链接，完成飞书 Bot 注册。\n%s",
		LangTraditionalChinese: "請開啟連結，完成飛書 Bot 註冊。\n%s",
		LangJapanese:           "リンクを開いて Feishu Bot の登録を完了してください。\n%s",
		LangSpanish:            "Abre el enlace para completar el registro del bot en Feishu.\n%s",
	},
	MsgBotNameReadFailed: {
		LangEnglish:            "The Feishu name could not be verified. Reopen Members later to refresh it.",
		LangChinese:            "暂时未能核验飞书名称，稍后重新打开 Members 刷新。",
		LangTraditionalChinese: "暫時未能核驗飛書名稱，稍後重新開啟 Members 重新整理。",
		LangJapanese:           "Feishu の名前を確認できませんでした。後で一覧を開き直してください。",
		LangSpanish:            "No se pudo verificar el nombre de Feishu. Vuelve a abrir Miembros más tarde.",
	},
	MsgFormBotTitle: {
		LangEnglish:            "Create bot",
		LangChinese:            "创建 Bot",
		LangTraditionalChinese: "建立 Bot",
		LangJapanese:           "Bot を作成",
		LangSpanish:            "Crear bot",
	},
	MsgFormProjectTitle: {
		LangEnglish:            "Create project",
		LangChinese:            "创建项目",
		LangTraditionalChinese: "建立專案",
		LangJapanese:           "プロジェクトを作成",
		LangSpanish:            "Crear proyecto",
	},
	MsgFormBotName: {
		LangEnglish:            "Bot name in Feishu (read-only)",
		LangChinese:            "Bot 飞书名称（只读）",
		LangTraditionalChinese: "Bot 飛書名稱（唯讀）",
		LangJapanese:           "Feishu の Bot 名（表示専用）",
		LangSpanish:            "Nombre del bot en Feishu (solo lectura)",
	},
	MsgFormIDHint: {
		LangEnglish:            "Lowercase letters, digits, - or _; e.g. developer-a",
		LangChinese:            "小写字母、数字、- 或 _，例如 developer-a",
		LangTraditionalChinese: "小寫字母、數字、- 或 _，例如 developer-a",
		LangJapanese:           "小文字・数字・-・_（例: developer-a）",
		LangSpanish:            "Minúsculas, números, - o _; p. ej. developer-a",
	},
	MsgFormRole: {
		LangEnglish:            "Role",
		LangChinese:            "角色",
		LangTraditionalChinese: "角色",
		LangJapanese:           "役割",
		LangSpanish:            "Rol",
	},
	MsgFormBackend: {
		LangEnglish:            "Execution backend",
		LangChinese:            "执行后端",
		LangTraditionalChinese: "執行後端",
		LangJapanese:           "実行環境",
		LangSpanish:            "Motor de ejecución",
	},
	MsgFormPrompt: {
		LangEnglish:            "Additional instructions (optional)",
		LangChinese:            "身份说明（选填）",
		LangTraditionalChinese: "身分說明（選填）",
		LangJapanese:           "追加の指示（任意）",
		LangSpanish:            "Instrucciones adicionales (opcional)",
	},
	MsgFormProjectName: {
		LangEnglish:            "Project name (also used as the group name)",
		LangChinese:            "项目名称（同时作为群名）",
		LangTraditionalChinese: "專案名稱（同時作為群組名稱）",
		LangJapanese:           "プロジェクト名（グループ名にも使用）",
		LangSpanish:            "Nombre del proyecto (también del grupo)",
	},
	MsgFormDirectory: {
		LangEnglish:            "Project directory (absolute path)",
		LangChinese:            "项目目录（绝对路径）",
		LangTraditionalChinese: "專案目錄（絕對路徑）",
		LangJapanese:           "プロジェクトディレクトリ（絶対パス）",
		LangSpanish:            "Directorio del proyecto (ruta absoluta)",
	},
	MsgFormMembers: {
		LangEnglish:            "Other members (optional)",
		LangChinese:            "其他成员（可选）",
		LangTraditionalChinese: "其他成員（可選）",
		LangJapanese:           "追加メンバー（任意）",
		LangSpanish:            "Otros miembros (opcional)",
	},
	MsgFormGoal: {
		LangEnglish:            "Project goal (optional)",
		LangChinese:            "项目目标（选填）",
		LangTraditionalChinese: "專案目標（選填）",
		LangJapanese:           "プロジェクトの目的（任意）",
		LangSpanish:            "Objetivo del proyecto (opcional)",
	},
	MsgFormCreate: {
		LangEnglish:            "Create",
		LangChinese:            "创建",
		LangTraditionalChinese: "建立",
		LangJapanese:           "作成",
		LangSpanish:            "Crear",
	},
	MsgFormBotHint: {
		LangEnglish:            "Choose a role and backend; identity instructions are optional. After submitting, complete the Feishu registration to configure and start the bot automatically.",
		LangChinese:            "选择角色和执行后端，身份说明选填。提交后完成飞书扫码注册，随后自动配置并启动 Bot。",
		LangTraditionalChinese: "選擇角色與執行後端，身分說明選填。提交後完成飛書掃碼註冊，隨後自動設定並啟動 Bot。",
		LangJapanese:           "役割と実行環境を選択します。指示の入力は任意です。送信後に Feishu の登録を完了すると、Bot を自動で設定・起動します。",
		LangSpanish:            "Elige un rol y un motor; las instrucciones son opcionales. Tras enviar, completa el registro en Feishu para configurar e iniciar el bot automáticamente.",
	},
	MsgFormProjectHint: {
		LangEnglish:            "Manager %s is always included. Other team members are selected by default; you can change the selection.",
		LangChinese:            "管理者 %s 自动加入。其他团队成员默认选中，可按需调整。",
		LangTraditionalChinese: "管理者 %s 自動加入。其他團隊成員預設選取，可依需調整。",
		LangJapanese:           "管理者 %s は必ず参加します。他のメンバーは初期選択済みで変更できます。",
		LangSpanish:            "El administrador %s siempre participa. Los demás miembros están preseleccionados y puedes cambiarlos.",
	},
	MsgFormInvalidField: {
		LangEnglish:            "Check the field: %s",
		LangChinese:            "请检查字段：%s",
		LangTraditionalChinese: "請檢查欄位：%s",
		LangJapanese:           "入力を確認してください: %s",
		LangSpanish:            "Revisa el campo: %s",
	},
	MsgFormExpired: {
		LangEnglish:            "This form has expired or was already submitted. Open a new form.",
		LangChinese:            "此表单已失效或已提交，请重新打开表单。",
		LangTraditionalChinese: "此表單已失效或已提交，請重新開啟表單。",
		LangJapanese:           "フォームは期限切れまたは送信済みです。開き直してください。",
		LangSpanish:            "El formulario caducó o ya se envió. Abre uno nuevo.",
	},
	MsgFormSubmitted: {
		LangEnglish:            "Submitted. Please wait for the result.",
		LangChinese:            "已提交，请等待处理结果。",
		LangTraditionalChinese: "已提交，請等待處理結果。",
		LangJapanese:           "送信しました。結果をお待ちください。",
		LangSpanish:            "Enviado. Espera el resultado.",
	},
	MsgFormRoleManager: {
		LangEnglish:            "Manager",
		LangChinese:            "管理者",
		LangTraditionalChinese: "管理者",
		LangJapanese:           "管理者",
		LangSpanish:            "Administrador",
	},
	MsgFormRolePlanner: {
		LangEnglish:            "Planner",
		LangChinese:            "规划者",
		LangTraditionalChinese: "規劃者",
		LangJapanese:           "計画担当",
		LangSpanish:            "Planificador",
	},
	MsgFormRoleDeveloper: {
		LangEnglish:            "Developer",
		LangChinese:            "开发者",
		LangTraditionalChinese: "開發者",
		LangJapanese:           "開発担当",
		LangSpanish:            "Desarrollador",
	},
	MsgFormRoleTester: {
		LangEnglish:            "Tester",
		LangChinese:            "测试者",
		LangTraditionalChinese: "測試者",
		LangJapanese:           "テスト担当",
		LangSpanish:            "Probador",
	},
	MsgFormRoleReviewer: {
		LangEnglish:            "Reviewer",
		LangChinese:            "审查者",
		LangTraditionalChinese: "審查者",
		LangJapanese:           "レビュー担当",
		LangSpanish:            "Revisor",
	},
	MsgTeamMemberSaved: {
		LangEnglish:            "Bot settings saved. Restart the service to apply role, backend and instructions.",
		LangChinese:            "Bot 配置已保存，重启服务后应用角色、执行后端和身份说明。",
		LangTraditionalChinese: "Bot 設定已儲存，重啟服務後套用角色、執行後端與身分說明。",
		LangJapanese:           "Bot 設定を保存しました。役割・実行環境・指示を適用するにはサービスを再起動してください。",
		LangSpanish:            "Configuración guardada. Reinicia el servicio para aplicar rol, motor e instrucciones.",
	},

	MsgTeamUnavailable: {
		LangEnglish:            "This bot has no Team configuration.",
		LangChinese:            "当前 Bot 未配置 Team。",
		LangTraditionalChinese: "目前 Bot 未設定 Team。",
		LangJapanese:           "この Bot に Team 設定がありません。",
		LangSpanish:            "Este bot no tiene configuración de Team.",
	},
	MsgTeamProjectPending: {
		LangEnglish:            "Project group setup is incomplete. Ask the manager to retry the creation command.",
		LangChinese:            "项目群尚未配置完成，请让管理者重试创建命令。",
		LangTraditionalChinese: "專案群尚未設定完成，請讓管理者重試建立命令。",
		LangJapanese:           "プロジェクトの設定は未完了です。管理者に作成の再試行を依頼してください。",
		LangSpanish:            "La configuración del grupo está incompleta. Pide al administrador que reintente.",
	},
	MsgTeamManagerOnly: {
		LangEnglish:            "Use the manager bot for Team changes.",
		LangChinese:            "请通过管理者 Bot 修改 Team。",
		LangTraditionalChinese: "請透過管理者 Bot 修改 Team。",
		LangJapanese:           "Team の変更は管理者 Bot で行ってください。",
		LangSpanish:            "Usa el bot administrador para modificar el Team.",
	},
	MsgTeamGroupUnsupported: {
		LangEnglish:            "This platform does not support project group creation.",
		LangChinese:            "当前平台未接入项目建群。",
		LangTraditionalChinese: "目前平台未支援專案建群。",
		LangJapanese:           "このプラットフォームではグループ作成に対応していません。",
		LangSpanish:            "Esta plataforma no admite crear grupos de proyecto.",
	},
	MsgTeamProjects: {
		LangEnglish:            "Project groups:",
		LangChinese:            "项目群：",
		LangTraditionalChinese: "專案群：",
		LangJapanese:           "プロジェクトグループ：",
		LangSpanish:            "Grupos de proyecto:",
	},
	MsgTeamProjectReady: {
		LangEnglish:            "Project %s is ready: %s",
		LangChinese:            "项目 %s 已就绪，进入项目群：%s",
		LangTraditionalChinese: "專案 %s 已就緒，進入專案群：%s",
		LangJapanese:           "プロジェクト %s の準備ができました：%s",
		LangSpanish:            "El proyecto %s está listo: %s",
	},
	MsgTeamChatUsage: {
		LangEnglish:            "/team permissions — Role permissions\n/team labels — Label settings\n/team members — view members and edit identities using forms\n/team bot create — open a creation form in private chat\n/team bot create <name> <role> <codex|claudecode|trae> [instructions]",
		LangChinese:            "/team permissions — 角色权限\n/team labels — 标签设置\n/team members：查看成员，通过表单配置身份\n/team bot create：在私聊打开创建表单\n/team bot create <名称> <角色> <codex|claudecode|trae> [身份说明]",
		LangTraditionalChinese: "/team permissions — 角色權限\n/team labels — 標籤設定\n/team members：查看成員，透過表單設定身分\n/team bot create：在私聊開啟建立表單\n/team bot create <名稱> <角色> <codex|claudecode|trae> [身分說明]",
		LangJapanese:           "/team permissions — ロールの権限\n/team labels — ラベル設定\n/team members — メンバー一覧と役割・指示の編集フォーム\n/team bot create — 個別チャットで作成フォームを開く\n/team bot create <名前> <役割> <codex|claudecode|trae> [指示]",
		LangSpanish:            "/team permissions — Permisos del rol\n/team labels — Ajustes de etiquetas\n/team members — ver miembros y editar identidades mediante formularios\n/team bot create — abrir formulario en chat privado\n/team bot create <nombre> <rol> <codex|claudecode|trae> [instrucciones]",
	},

	MsgTeamInitAutoReady: {
		LangEnglish:            "Manager Bot configuration saved: %s\nThe scanning account is automatically bound as administrator. Open the bot after the service connects: %s\nYou can chat directly; no pairing command is needed.",
		LangChinese:            "管理者 Bot 配置已保存：%s\n已自动将扫码账号绑定为管理员。服务连接成功后打开机器人：%s\n直接开始对话即可，无需发送认领命令。",
		LangTraditionalChinese: "管理者 Bot 設定已儲存：%s\n已自動將掃碼帳號綁定為管理員。服務連線成功後開啟機器人：%s\n直接開始對話即可，無須傳送認領指令。",
		LangJapanese:           "管理ボット設定を保存しました：%s\nスキャンしたアカウントを管理者に自動登録しました。サービス接続後にボットを開いてください：%s\n登録コマンドなしで会話を始められます。",
		LangSpanish:            "Configuración guardada: %s\nLa cuenta que escaneó el código ya es administradora. Abre el bot cuando se conecte el servicio: %s\nPuedes conversar directamente, sin comando de vinculación.",
	},
	MsgBotAutoReady: {
		LangEnglish:            "Bot %s was saved and its connection started. The scanning account is its administrator. Open %s to chat directly.",
		LangChinese:            "机器人 %s 已保存并启动连接，扫码账号已自动绑定为管理员。打开 %s 即可直接对话。",
		LangTraditionalChinese: "機器人 %s 已儲存並啟動連線，掃碼帳號已自動綁定為管理員。開啟 %s 即可直接對話。",
		LangJapanese:           "ボット %s を保存し接続を開始しました。スキャンしたアカウントが管理者です。%s を開いて会話できます。",
		LangSpanish:            "Se guardó el bot %s y se inició su conexión. La cuenta que escaneó es administradora. Abre %s para conversar.",
	},
	MsgBotAutoSavedOffline: {
		LangEnglish:            "Bot %s and its administrator were saved, but the connection failed. Fix the connection and restart cc-connect without creating another app. Bot: %s",
		LangChinese:            "机器人 %s 和管理员绑定已保存，但连接启动失败。排除连接问题后重启 cc-connect 即可，无需重复创建或认领。机器人：%s",
		LangTraditionalChinese: "機器人 %s 與管理員綁定已儲存，但連線啟動失敗。排除連線問題後重啟 cc-connect 即可，無須重複建立或認領。機器人：%s",
		LangJapanese:           "ボット %s と管理者を保存しましたが、接続できません。接続問題を解決して cc-connect を再起動してください。再作成や登録は不要です。ボット：%s",
		LangSpanish:            "Se guardaron el bot %s y su administrador, pero falló la conexión. Soluciona la conexión y reinicia cc-connect sin crear otra aplicación. Bot: %s",
	},
	MsgBotRegistrationOwnerMissing: {
		LangEnglish:            "The app registration returned no valid scanning user identity. The registration result is saved, but the bot was not activated. No first-message ownership or manual claim was enabled; do not create a duplicate app.",
		LangChinese:            "应用注册未返回有效的扫码用户身份。注册结果已保存，机器人未启用；不会让首个发消息的人自动成为管理员，也未启用手动认领。请勿重复创建应用。",
		LangTraditionalChinese: "應用註冊未回傳有效的掃碼使用者身分。註冊結果已儲存，機器人未啟用；不會讓首個傳訊息的人自動成為管理員，也未啟用手動認領。請勿重複建立應用。",
		LangJapanese:           "登録結果に有効なスキャンユーザーの情報がありません。結果は保存済みですが、ボットは有効化していません。最初の送信者の自動登録や手動登録は行いません。アプリを重複作成しないでください。",
		LangSpanish:            "El registro no devolvió una identidad válida de quien escaneó. Se guardó el resultado, pero no se activó el bot. No se habilitó la vinculación al primer remitente ni la manual. No crees otra aplicación.",
	},
	MsgTeamUsage: {
		LangEnglish:            "Agent Team setup:\n  team init --agent <codex|claudecode|trae> --work-dir <directory> [--config <new.toml>]\n  team status\n  team plan --to <bot> --title <title> --deliverable <output> --message <task> [--depends-on T001]\n  team dispatch --task-id T002\n  team cancel --task-id T002\n  team dispatch --to <bot> --title <title> --deliverable <output> --message <task>\n  team ensure-role --role tester --backend codex --reason <task-and-deliverable>\n  team adopt --input <v2.toml> --output <new.toml>\n  team migrate --input <old.toml> --output <new.toml>\nInit creates a manager Bot via QR authorization and starts the service. Use --no-start for configuration only; no manual App Key is needed. Use team init --help for options.",
		LangChinese:            "Agent Team 配置：\n  team init --agent <codex|claudecode|trae> --work-dir <目录> [--config <新配置.toml>]\n  team status\n  team plan --to <bot> --title <title> --deliverable <output> --message <task> [--depends-on T001]\n  team dispatch --task-id T002\n  team cancel --task-id T002\n  team dispatch --to <bot> --title <title> --deliverable <output> --message <task>\n  team ensure-role --role tester --backend codex --reason <task-and-deliverable>\n  team adopt --input <v2.toml> --output <new.toml>\n  team migrate --input <旧配置.toml> --output <新配置.toml>\ninit 通过扫码授权创建管理者 Bot 并自动启动服务；--no-start 可仅保存配置，无需手填 App Key。更多选项见 team init --help。",
		LangTraditionalChinese: "Agent Team 設定：\n  team init --agent <codex|claudecode|trae> --work-dir <目錄> [--config <新設定.toml>]\n  team status\n  team plan --to <bot> --title <title> --deliverable <output> --message <task> [--depends-on T001]\n  team dispatch --task-id T002\n  team cancel --task-id T002\n  team dispatch --to <bot> --title <title> --deliverable <output> --message <task>\n  team ensure-role --role tester --backend codex --reason <task-and-deliverable>\n  team adopt --input <v2.toml> --output <new.toml>\n  team migrate --input <舊設定.toml> --output <新設定.toml>\ninit 透過掃碼授權建立管理者 Bot 並自動啟動服務；--no-start 可僅儲存設定，無須手填 App Key。更多選項見 team init --help。",
		LangJapanese:           "Agent Team 設定：\n  team init --agent <codex|claudecode|trae> --work-dir <ディレクトリ> [--config <new.toml>]\n  team status\n  team plan --to <bot> --title <title> --deliverable <output> --message <task> [--depends-on T001]\n  team dispatch --task-id T002\n  team cancel --task-id T002\n  team dispatch --to <bot> --title <title> --deliverable <output> --message <task>\n  team ensure-role --role tester --backend codex --reason <task-and-deliverable>\n  team adopt --input <v2.toml> --output <new.toml>\n  team migrate --input <old.toml> --output <new.toml>\ninit は QR 認証で管理ボットを作成し、サービスを起動します。設定のみ保存するには --no-start を指定します。App Key の手入力は不要です。詳細は team init --help。",
		LangSpanish:            "Configuración de Agent Team:\n  team init --agent <codex|claudecode|trae> --work-dir <directorio> [--config <new.toml>]\n  team status\n  team plan --to <bot> --title <title> --deliverable <output> --message <task> [--depends-on T001]\n  team dispatch --task-id T002\n  team cancel --task-id T002\n  team dispatch --to <bot> --title <title> --deliverable <output> --message <task>\n  team ensure-role --role tester --backend codex --reason <task-and-deliverable>\n  team adopt --input <v2.toml> --output <new.toml>\n  team migrate --input <old.toml> --output <new.toml>\ninit crea un bot administrador mediante autorización QR e inicia el servicio, sin introducir App Key. Usa --no-start para guardar solo la configuración. Consulta team init --help.",
	},
	MsgTeamInitScan: {
		LangEnglish:            "Scan with Feishu/Lark to create and authorize your manager Bot:\n%s",
		LangChinese:            "请使用飞书/Lark 扫码，完成管理者 Bot 的创建和授权：\n%s",
		LangTraditionalChinese: "請使用飛書/Lark 掃碼，完成管理者 Bot 的建立和授權：\n%s",
		LangJapanese:           "Feishu/Lark でスキャンし、管理ボットを作成・認証してください：\n%s",
		LangSpanish:            "Escanea con Feishu/Lark para crear y autorizar tu bot administrador:\n%s",
	},
	MsgTeamInitReady: {
		LangEnglish:            "Manager Bot configuration saved: %s\nOpen the bot after the service connects: %s\nSend this one-time command in a private chat with the bot to bind your account as its administrator:\n/new claim %s\nThis is needed only once, not after every restart. No manual open_id or business project is needed. If messages do not arrive, check the app publication status and availability.",
		LangChinese:            "管理者 Bot 配置已保存：%s\n服务连接成功后打开机器人：%s\n在与该机器人的私聊中发送以下一次性命令，将你的账号绑定为管理员：\n/new claim %s\n只需首次认领，重启后不用重复；无需手填 open_id，也无需先创建业务项目。如收不到消息，请核验应用发布状态和可用范围。",
		LangTraditionalChinese: "管理者 Bot 設定已儲存：%s\n服務連線成功後開啟機器人：%s\n在與該機器人的私聊中傳送以下一次性指令，將你的帳號綁定為管理員：\n/new claim %s\n只需首次認領，重新啟動後不必重複；無須手填 open_id，也無須先建立業務專案。若收不到訊息，請檢查應用發布狀態與可用範圍。",
		LangJapanese:           "管理ボット設定を保存しました：%s\nサービス接続後にボットを開いてください：%s\n個人チャットで次のワンタイムコマンドを送信し、管理者アカウントを紐付けてください：\n/new claim %s\n初回のみ必要で、再起動後は不要です。open_id の入力や業務プロジェクトは不要です。受信できない場合はアプリの公開状態と利用範囲を確認してください。",
		LangSpanish:            "Configuración guardada: %s\nAbre el bot cuando se conecte el servicio: %s\nEnvía este comando de un solo uso en un chat privado para vincular tu cuenta como administrador:\n/new claim %s\nSolo hace falta la primera vez, no tras reiniciar. No necesitas introducir open_id ni crear un proyecto. Si no llegan mensajes, comprueba la publicación y disponibilidad de la aplicación.",
	},
	MsgTeamInitStarting: {
		LangEnglish:            "Starting the service automatically in this terminal. Keep it open; press Ctrl+C to stop.",
		LangChinese:            "正在自动启动服务。请保持当前终端运行；按 Ctrl+C 停止。",
		LangTraditionalChinese: "正在自動啟動服務。請保持目前終端執行；按 Ctrl+C 停止。",
		LangJapanese:           "このターミナルでサービスを自動起動します。開いたままにしてください。Ctrl+C で停止します。",
		LangSpanish:            "Iniciando el servicio automáticamente en esta terminal. Mantenla abierta; pulsa Ctrl+C para detenerlo.",
	},
	MsgTeamInitNoStart: {
		LangEnglish:            "Configuration only (--no-start). Start this binary later with --config pointing to the saved file.",
		LangChinese:            "已按 --no-start 仅保存配置。稍后使用当前程序加 --config 指向上述配置文件启动。",
		LangTraditionalChinese: "已依 --no-start 僅儲存設定。稍後使用目前程式加 --config 指向上述設定檔啟動。",
		LangJapanese:           "--no-start により設定のみ保存しました。後で --config に保存先を指定して起動してください。",
		LangSpanish:            "Solo se guardó la configuración (--no-start). Inicia el programa después con --config apuntando al archivo guardado.",
	},
	MsgBotUsage: {
		LangEnglish:            "Use /new bot <name> (1–48 lowercase letters, digits, - or _), or /new bot cancel.",
		LangChinese:            "用 /new bot <名称> 创建机器人（1–48 位小写字母、数字、- 或 _）；用 /new bot cancel 取消。",
		LangTraditionalChinese: "用 /new bot <名稱> 建立機器人（1–48 位小寫字母、數字、- 或 _）；用 /new bot cancel 取消。",
		LangJapanese:           "/new bot <名前>（1～48文字の英小文字・数字・-・_）で作成、/new bot cancel で中止します。",
		LangSpanish:            "Usa /new bot <nombre> (1–48 letras minúsculas, números, - o _), o /new bot cancel.",
	},
	MsgBotPrivateOnly: {
		LangEnglish:            "Create bots in a private chat with me; registration links must stay private.",
		LangChinese:            "请在与我的私聊中创建机器人，创建链接仅在私聊发送。",
		LangTraditionalChinese: "請在與我的私聊中建立機器人，建立連結僅在私聊傳送。",
		LangJapanese:           "ボットの作成は私との個人チャットで行ってください。",
		LangSpanish:            "Crea bots en un chat privado conmigo; el enlace debe mantenerse privado.",
	},
	MsgBotUnsupported: {
		LangEnglish:            "Bot creation is unavailable on this platform.",
		LangChinese:            "当前平台暂不支持在聊天中创建机器人。",
		LangTraditionalChinese: "目前平台暫不支援在聊天中建立機器人。",
		LangJapanese:           "このプラットフォームではチャットからボットを作成できません。",
		LangSpanish:            "Esta plataforma no permite crear bots desde el chat.",
	},
	MsgBotBusy: {
		LangEnglish:            "A bot creation is already pending. Finish it or use /new bot cancel.",
		LangChinese:            "已有一个机器人正在创建，请先完成或用 /new bot cancel 取消。",
		LangTraditionalChinese: "已有一個機器人正在建立，請先完成或用 /new bot cancel 取消。",
		LangJapanese:           "作成処理が進行中です。完了するか /new bot cancel で中止してください。",
		LangSpanish:            "Ya hay una creación pendiente. Complétala o usa /new bot cancel.",
	},
	MsgBotCancelled: {
		LangEnglish:            "Bot creation cancellation requested. A bot already created on the platform is not deleted.",
		LangChinese:            "已请求取消创建；如果飞书端已经创建成功，不会删除该机器人。",
		LangTraditionalChinese: "已請求取消建立；如果平台端已經建立成功，不會刪除該機器人。",
		LangJapanese:           "作成の中止を要求しました。既に作成済みのボットは削除されません。",
		LangSpanish:            "Se solicitó cancelar. No se eliminará un bot ya creado en la plataforma.",
	},
	MsgBotAuthorize: {
		LangEnglish:            "Open this link in the mobile app to create and authorize bot %s (expires in about 10 minutes):\n%s\nYou can continue chatting while waiting.",
		LangChinese:            "请在手机飞书/Lark 中打开此链接，创建并授权机器人 %s（约 10 分钟有效）：\n%s\n等待期间可以继续聊天。",
		LangTraditionalChinese: "請在手機飛書/Lark 中開啟此連結，建立並授權機器人 %s（約 10 分鐘有效）：\n%s\n等待期間可以繼續聊天。",
		LangJapanese:           "モバイルアプリでリンクを開き、ボット %s を作成・承認してください（約10分有効）：\n%s\n待機中も会話できます。",
		LangSpanish:            "Abre este enlace en la aplicación móvil para crear y autorizar el bot %s (unos 10 minutos):\n%s\nPuedes seguir conversando.",
	},
	MsgBotFailed: {
		LangEnglish:            "Bot creation failed or expired. No existing identity was replaced. Retry /new bot <name>.",
		LangChinese:            "机器人创建失败或超时，未替换已有身份。可重试 /new bot <名称>。",
		LangTraditionalChinese: "機器人建立失敗或逾時，未替換已有身分。可重試 /new bot <名稱>。",
		LangJapanese:           "作成に失敗したか期限切れです。既存のボットは変更されていません。/new bot <名前> で再試行できます。",
		LangSpanish:            "La creación falló o caducó. No se sustituyó ninguna identidad. Reintenta /new bot <nombre>.",
	},
	MsgBotExists: {
		LangEnglish:            "That bot name already exists or is being created. Choose another name.",
		LangChinese:            "该机器人名称已存在或正在创建，请换一个名称。",
		LangTraditionalChinese: "該機器人名稱已存在或正在建立，請換一個名稱。",
		LangJapanese:           "その名前は既に存在するか作成中です。別の名前を選んでください。",
		LangSpanish:            "Ese nombre ya existe o está en proceso de creación. Elige otro.",
	},
	MsgBotReady: {
		LangEnglish:            "Bot %s has been saved and its connection started. Open %s and send this one-time command privately:\n/new claim %s\nThen it can chat with you and create other bots.",
		LangChinese:            "机器人 %s 已保存并启动连接。打开 %s，在与新机器人的私聊中发送一次性绑定命令：\n/new claim %s\n绑定后即可聊天和继续创建机器人。",
		LangTraditionalChinese: "機器人 %s 已儲存並啟動連線。開啟 %s，在與新機器人的私聊中傳送一次性綁定命令：\n/new claim %s\n綁定後即可聊天和繼續建立機器人。",
		LangJapanese:           "ボット %s を保存し接続を開始しました。%s を開き、個人チャットで次の一度限りのコマンドを送信してください：\n/new claim %s\n登録後に会話やボットの作成ができます。",
		LangSpanish:            "Se guardó el bot %s y se inició su conexión. Abre %s y envía en privado este comando de un solo uso:\n/new claim %s\nDespués podrás conversar y crear otros bots.",
	},
	MsgBotSavedOffline: {
		LangEnglish:            "Bot %s was saved, but its connection could not start. Restart cc-connect after fixing the connection; do not create a duplicate. Open %s and keep this private pairing command:\n/new claim %s",
		LangChinese:            "机器人 %s 已保存，但连接启动失败。排除连接问题后重启 cc-connect 即可，无需重复创建。打开 %s，并保管好私聊绑定命令：\n/new claim %s",
		LangTraditionalChinese: "機器人 %s 已儲存，但連線啟動失敗。排除連線問題後重啟 cc-connect 即可，無需重複建立。開啟 %s，並保管好私聊綁定命令：\n/new claim %s",
		LangJapanese:           "ボット %s は保存済みですが接続できません。接続問題を解決して cc-connect を再起動してください。再作成は不要です。%s を開き、この登録コマンドを保管してください：\n/new claim %s",
		LangSpanish:            "El bot %s se guardó pero no pudo conectarse. Soluciona la conexión y reinicia cc-connect sin crear otro. Abre %s y conserva este comando privado:\n/new claim %s",
	},
	MsgBotSaveFailed: {
		LangEnglish:            "Could not save the identity configuration. Retry after fixing config permissions.",
		LangChinese:            "无法保存身份配置，请修复配置文件权限后重试。",
		LangTraditionalChinese: "無法儲存身分設定，請修復設定檔權限後重試。",
		LangJapanese:           "設定を保存できません。設定ファイルの権限を確認してください。",
		LangSpanish:            "No se pudo guardar la configuración. Revisa los permisos y reintenta.",
	},
	MsgBotUnclaimed: {
		LangEnglish:            "This bot is awaiting owner binding. Send the /new claim command provided by the creating bot in a private chat.",
		LangChinese:            "该机器人尚未绑定管理员，请在私聊中发送创建机器人提供的 /new claim 命令。",
		LangTraditionalChinese: "該機器人尚未綁定管理員，請在私聊中傳送建立機器人提供的 /new claim 命令。",
		LangJapanese:           "所有者の登録待ちです。作成元のボットから受け取った /new claim コマンドを個人チャットで送信してください。",
		LangSpanish:            "Este bot espera la vinculación del propietario. Envía en privado el comando /new claim del bot creador.",
	},
	MsgBotClaimInvalid: {
		LangEnglish:            "Invalid pairing code. Use the command provided by the creating bot.",
		LangChinese:            "绑定码无效，请使用创建机器人提供的完整命令。",
		LangTraditionalChinese: "綁定碼無效，請使用建立機器人提供的完整命令。",
		LangJapanese:           "登録コードが無効です。作成元から受け取ったコマンドを使用してください。",
		LangSpanish:            "Código de vinculación inválido. Usa el comando del bot creador.",
	},
	MsgBotClaimed: {
		LangEnglish:            "Owner bound. You can now chat, or create another bot with /new bot <name>.",
		LangChinese:            "管理员绑定成功。现在可以聊天，或用 /new bot <名称> 创建其他机器人。",
		LangTraditionalChinese: "管理員綁定成功。現在可以聊天，或用 /new bot <名稱> 建立其他機器人。",
		LangJapanese:           "所有者を登録しました。会話、または /new bot <名前> で別のボットを作成できます。",
		LangSpanish:            "Propietario vinculado. Ya puedes conversar o crear otro bot con /new bot <nombre>.",
	},
	MsgBotOwnerRequired: {
		LangEnglish:            "This bot currently accepts messages only from its bound owner.",
		LangChinese:            "该机器人当前仅接受已绑定管理员的消息。",
		LangTraditionalChinese: "該機器人目前僅接受已綁定管理員的訊息。",
		LangJapanese:           "現在このボットは登録済み所有者からのメッセージのみ受け付けます。",
		LangSpanish:            "Este bot solo acepta mensajes de su propietario vinculado.",
	},
	MsgStarting: {
		LangEnglish:            "⏳ Processing...",
		LangChinese:            "⏳ 处理中...",
		LangTraditionalChinese: "⏳ 處理中...",
		LangJapanese:           "⏳ 処理中...",
		LangSpanish:            "⏳ Procesando...",
	},
	MsgThinking: {
		LangEnglish: "💭 %s",
		LangChinese: "💭 %s",
	},
	MsgTool: {
		LangEnglish:            "🔧 **Tool #%d: %s**\n---\n%s",
		LangChinese:            "🔧 **工具 #%d: %s**\n---\n%s",
		LangTraditionalChinese: "🔧 **工具 #%d: %s**\n---\n%s",
		LangJapanese:           "🔧 **ツール #%d: %s**\n---\n%s",
		LangSpanish:            "🔧 **Herramienta #%d: %s**\n---\n%s",
	},
	MsgToolResult: {
		LangEnglish:            "📤 **%s**\n---\n%s",
		LangChinese:            "📤 **%s**\n---\n%s",
		LangTraditionalChinese: "📤 **%s**\n---\n%s",
		LangJapanese:           "📤 **%s**\n---\n%s",
		LangSpanish:            "📤 **%s**\n---\n%s",
	},
	MsgToolResultFmtStatus: {
		LangEnglish:            "Status",
		LangChinese:            "状态",
		LangTraditionalChinese: "狀態",
		LangJapanese:           "ステータス",
		LangSpanish:            "Estado",
	},
	MsgToolResultFmtExit: {
		LangEnglish:            "Exit",
		LangChinese:            "退出码",
		LangTraditionalChinese: "結束代碼",
		LangJapanese:           "終了コード",
		LangSpanish:            "Salida",
	},
	MsgToolResultFmtNoOutput: {
		LangEnglish:            "No output",
		LangChinese:            "无输出",
		LangTraditionalChinese: "無輸出",
		LangJapanese:           "出力なし",
		LangSpanish:            "Sin salida",
	},
	MsgToolResultFmtOk: {
		LangEnglish:            "ok",
		LangChinese:            "ok",
		LangTraditionalChinese: "ok",
		LangJapanese:           "ok",
		LangSpanish:            "ok",
	},
	MsgToolResultFmtFailed: {
		LangEnglish:            "failed",
		LangChinese:            "failed",
		LangTraditionalChinese: "failed",
		LangJapanese:           "failed",
		LangSpanish:            "fallido",
	},
	MsgExecutionStopped: {
		LangEnglish:            "⏹ Execution stopped.",
		LangChinese:            "⏹ 执行已停止。",
		LangTraditionalChinese: "⏹ 執行已停止。",
		LangJapanese:           "⏹ 実行を停止しました。",
		LangSpanish:            "⏹ Ejecución detenida.",
	},
	MsgSessionCloseFailed: {
		LangEnglish:            "⚠️ Warning: the stopped session's background process could not be confirmed killed. It may still be running and using its old credentials.",
		LangChinese:            "⚠️ 警告：已停止会话的后台进程未能确认已终止，可能仍在运行并占用原有凭证。",
		LangTraditionalChinese: "⚠️ 警告：已停止會話的後台進程未能確認已終止，可能仍在運行並佔用原有憑證。",
		LangJapanese:           "⚠️ 警告：停止したセッションのバックグラウンドプロセスの終了を確認できませんでした。まだ実行中で、以前の認証情報を使用している可能性があります。",
		LangSpanish:            "⚠️ Advertencia: no se pudo confirmar que el proceso en segundo plano de la sesión detenida haya finalizado. Podría seguir en ejecución usando sus credenciales anteriores.",
	},
	MsgSessionResumeUnsafe: {
		LangEnglish:            "⚠️ The previous process could not be confirmed stopped, so this conversation was NOT resumed — a brand-new session was started instead (earlier context is not carried over). This avoids two agents acting on the same conversation.",
		LangChinese:            "⚠️ 上一个进程未能确认已停止，因此没有续接原会话，已为你开启全新会话（此前上下文未带入）。这是为了避免两个智能体同时操作同一个会话。",
		LangTraditionalChinese: "⚠️ 上一個行程未能確認已停止，因此沒有續接原會話，已為你開啟全新會話（先前上下文未帶入）。這是為了避免兩個智能體同時操作同一個會話。",
		LangJapanese:           "⚠️ 前のプロセスの停止を確認できなかったため、会話を再開せず新しいセッションを開始しました（以前の文脈は引き継がれません）。同じ会話を2つのエージェントが操作するのを防ぐためです。",
		LangSpanish:            "⚠️ No se pudo confirmar que el proceso anterior se detuviera, así que no se reanudó esta conversación: se inició una sesión nueva (sin el contexto anterior). Así se evita que dos agentes actúen sobre la misma conversación.",
	},
	MsgSessionCancelled: {
		LangEnglish:            "Session cancelled. Ready for new instructions.",
		LangChinese:            "会话已取消。可以继续新的对话。",
		LangTraditionalChinese: "會話已取消。可以繼續新的對話。",
		LangJapanese:           "セッションをキャンセルしました。新しい指示を受け付けます。",
		LangSpanish:            "Sesion cancelada. Listo para nuevas instrucciones.",
	},
	MsgNoExecution: {
		LangEnglish:            "No execution in progress.",
		LangChinese:            "没有正在执行的任务。",
		LangTraditionalChinese: "沒有正在執行的任務。",
		LangJapanese:           "実行中のタスクはありません。",
		LangSpanish:            "No hay ejecución en progreso.",
	},
	MsgPreviousProcessing: {
		LangEnglish:            "⏳ Previous request still processing. Use `/ps <message>` to send a P.S. to the running task.",
		LangChinese:            "⏳ 上一个请求仍在处理中。使用 `/ps <消息>` 可向正在执行的任务追加补充信息。",
		LangTraditionalChinese: "⏳ 上一個請求仍在處理中。使用 `/ps <訊息>` 可向正在執行的任務追加補充資訊。",
		LangJapanese:           "⏳ 前のリクエストを処理中です。`/ps <メッセージ>` で実行中のタスクに補足情報を送れます。",
		LangSpanish:            "⏳ La solicitud anterior aún se está procesando. Use `/ps <mensaje>` para enviar un P.S. a la tarea en curso.",
	},
	MsgMessageQueued: {
		LangEnglish:            "📬 Message received — will process after the current task finishes.",
		LangChinese:            "📬 消息已收到，将在当前任务完成后处理。",
		LangTraditionalChinese: "📬 訊息已收到，將在目前任務完成後處理。",
		LangJapanese:           "📬 メッセージを受信しました。現在のタスク完了後に処理します。",
		LangSpanish:            "📬 Mensaje recibido — se procesará después de que termine la tarea actual.",
	},
	MsgQueueFull: {
		LangEnglish:            "📬 Message queue is full (%d pending). Please wait for current tasks to complete.",
		LangChinese:            "📬 消息队列已满（%d 条待处理）。请等待当前任务完成。",
		LangTraditionalChinese: "📬 訊息佇列已滿（%d 則待處理）。請等待目前任務完成。",
		LangJapanese:           "📬 メッセージキューが満杯です（%d 件待ち）。現在のタスク完了をお待ちください。",
		LangSpanish:            "📬 La cola de mensajes está llena (%d pendientes). Espere a que las tareas actuales se completen.",
	},
	MsgNoToolsAllowed: {
		LangEnglish:            "No tools pre-allowed.\nUsage: `/allow <tool_name>`\nExample: `/allow Bash`",
		LangChinese:            "尚未预授权任何工具。\n用法: `/allow <工具名>`\n示例: `/allow Bash`",
		LangTraditionalChinese: "尚未預授權任何工具。\n用法: `/allow <工具名>`\n範例: `/allow Bash`",
		LangJapanese:           "事前許可されたツールはありません。\n使い方: `/allow <ツール名>`\n例: `/allow Bash`",
		LangSpanish:            "No hay herramientas pre-autorizadas.\nUso: `/allow <nombre_herramienta>`\nEjemplo: `/allow Bash`",
	},
	MsgCurrentTools: {
		LangEnglish:            "Pre-allowed tools: %s",
		LangChinese:            "预授权的工具: %s",
		LangTraditionalChinese: "預授權的工具: %s",
		LangJapanese:           "事前許可済みツール: %s",
		LangSpanish:            "Herramientas pre-autorizadas: %s",
	},
	MsgCurrentSession: {
		LangEnglish:            "📌 Current session\nName: %s\nSession ID: %s\nLocal messages: %d",
		LangChinese:            "📌 当前会话\n名称: %s\n会话 ID: %s\n本地消息数: %d",
		LangTraditionalChinese: "📌 目前工作階段\n名稱: %s\n工作階段 ID: %s\n本機訊息數: %d",
		LangJapanese:           "📌 現在のセッション\n名前: %s\nセッション ID: %s\nローカルメッセージ数: %d",
		LangSpanish:            "📌 Sesión actual\nNombre: %s\nID de sesión: %s\nMensajes locales: %d",
	},
	MsgToolAuthNotSupported: {
		LangEnglish:            "This agent does not support tool authorization.",
		LangChinese:            "此代理不支持工具授权。",
		LangTraditionalChinese: "此代理不支援工具授權。",
		LangJapanese:           "このエージェントはツール認可をサポートしていません。",
		LangSpanish:            "Este agente no soporta la autorización de herramientas.",
	},
	MsgToolAllowFailed: {
		LangEnglish:            "Failed to allow tool: %v",
		LangChinese:            "授权工具失败: %v",
		LangTraditionalChinese: "授權工具失敗: %v",
		LangJapanese:           "ツール許可に失敗しました: %v",
		LangSpanish:            "Error al autorizar herramienta: %v",
	},
	MsgToolAllowedNew: {
		LangEnglish:            "✅ Tool `%s` pre-allowed. Takes effect on next session.",
		LangChinese:            "✅ 工具 `%s` 已预授权。将在下次会话生效。",
		LangTraditionalChinese: "✅ 工具 `%s` 已預授權。將在下次會話生效。",
		LangJapanese:           "✅ ツール `%s` を事前許可しました。次のセッションから有効になります。",
		LangSpanish:            "✅ Herramienta `%s` pre-autorizada. Se aplicará en la próxima sesión.",
	},
	MsgProjectUsage: {
		LangEnglish:            "Projects: /project list | /project use <id> | /project clear",
		LangChinese:            "项目：/project list 查看；/project use <id> 切换；/project clear 返回未选择状态",
		LangTraditionalChinese: "專案：/project list 查看；/project use <id> 切換；/project clear 清除選擇",
		LangJapanese:           "プロジェクト: /project list | /project use <id> | /project clear",
		LangSpanish:            "Proyectos: /project list | /project use <id> | /project clear",
	},
	MsgProjectSelected: {
		LangEnglish:            "Active project: %s. Its independent context is ready.",
		LangChinese:            "已切换到项目 %s，使用该项目的独立上下文。",
		LangTraditionalChinese: "已切換至專案 %s，使用該專案的獨立上下文。",
		LangJapanese:           "プロジェクト %s の独立したコンテキストを使用します。",
		LangSpanish:            "Proyecto activo: %s. Se usa su contexto independiente.",
	},
	MsgProjectRequired: {
		LangEnglish:            "Select a project with /project use <id>. Use /project list to see available projects.",
		LangChinese:            "请先用 /project use <id> 选择项目；/project list 查看可用项目。",
		LangTraditionalChinese: "請先用 /project use <id> 選擇專案；/project list 查看可用專案。",
		LangJapanese:           "/project use <id> でプロジェクトを選択してください。",
		LangSpanish:            "Selecciona un proyecto con /project use <id>.",
	},
	MsgProjectCleared: {
		LangEnglish:            "Project selection cleared. Existing project contexts are preserved.",
		LangChinese:            "已清除项目选择，各项目的上下文仍然保留。",
		LangTraditionalChinese: "已清除專案選擇，各專案的上下文仍然保留。",
		LangJapanese:           "選択を解除しました。各プロジェクトのコンテキストは保持されます。",
		LangSpanish:            "Selección borrada. Los contextos de los proyectos se conservan.",
	},
	MsgProjectCallbackUnsupported: {
		LangEnglish:            "Project card callbacks are not available yet. Select the project and respond using text.",
		LangChinese:            "项目卡片回调尚未接通，请选择对应项目后用文字处理。",
		LangTraditionalChinese: "專案卡片回呼尚未接通，請選擇對應專案後用文字處理。",
		LangJapanese:           "プロジェクトを選択してテキストで応答してください。",
		LangSpanish:            "Selecciona el proyecto y responde por texto.",
	},
	MsgError: {
		LangEnglish:            "❌ Error: %v",
		LangChinese:            "❌ 错误: %v",
		LangTraditionalChinese: "❌ 錯誤: %v",
		LangJapanese:           "❌ エラー: %v",
		LangSpanish:            "❌ Error: %v",
	},
	MsgBackgroundAutoDenied: {
		LangEnglish:            "⚠️ Background task requested permission for `%s` but was auto-denied (no active user turn). Send a message or use `/yolo` to approve future requests.",
		LangChinese:            "⚠️ 后台任务请求使用工具 `%s` 的权限，但已自动拒绝（当前无活跃会话）。请发送消息或使用 `/yolo` 授权后续请求。",
		LangTraditionalChinese: "⚠️ 後台任務請求使用工具 `%s` 的權限，但已自動拒絕（目前無活躍會話）。請發送訊息或使用 `/yolo` 授權後續請求。",
		LangJapanese:           "⚠️ バックグラウンドタスクがツール `%s` の権限を要求しましたが、自動的に拒否されました（アクティブなユーザーターンなし）。メッセージを送信するか `/yolo` を使用して今後のリクエストを承認してください。",
		LangSpanish:            "⚠️ Una tarea en segundo plano solicitó permiso para `%s` pero se denegó automáticamente (sin turno de usuario activo). Envía un mensaje o usa `/yolo` para aprobar solicitudes futuras.",
	},
	MsgSessionNotFound: {
		LangEnglish:            "⚠️ Session expired. Use /new to start a fresh conversation.",
		LangChinese:            "⚠️ 会话已过期，请发送 /new 开始新会话",
		LangTraditionalChinese: "⚠️ 會話已過期，請發送 /new 開始新會話",
		LangJapanese:           "⚠️ セッションが期限切れです。/new で新しい会話を開始してください。",
		LangSpanish:            "⚠️ Sesión expirada. Usa /new para iniciar una nueva conversación.",
	},
	MsgFailedToStartAgentSession: {
		LangEnglish:            "❌ Error: failed to start agent session",
		LangChinese:            "❌ 错误: 启动 Agent 会话失败",
		LangTraditionalChinese: "❌ 錯誤: 啟動 Agent 會話失敗",
		LangJapanese:           "❌ エラー: Agentセッションの起動に失敗しました",
		LangSpanish:            "❌ Error: error al iniciar la sesión del agente",
	},
	MsgFailedToDeleteSession: {
		LangEnglish:            "❌ %s: %v",
		LangChinese:            "❌ %s: %v",
		LangTraditionalChinese: "❌ %s: %v",
		LangJapanese:           "❌ %s: %v",
		LangSpanish:            "❌ %s: %v",
	},
	MsgEmptyResponse: {
		LangEnglish:            "(empty response)",
		LangChinese:            "(空响应)",
		LangTraditionalChinese: "(空回應)",
		LangJapanese:           "（空のレスポンス）",
		LangSpanish:            "(respuesta vacía)",
	},
	MsgPermissionPrompt: {
		LangEnglish:            "⚠️ **Permission Request**\n\nAgent wants to use **%s**:\n\n```\n%s\n```\n\nReply **allow** / **deny** / **allow all** (skip all future prompts this session).",
		LangChinese:            "⚠️ **权限请求**\n\nAgent 想要使用 **%s**:\n\n```\n%s\n```\n\n回复 **允许** / **拒绝** / **允许所有**（本次会话不再提醒）。",
		LangTraditionalChinese: "⚠️ **權限請求**\n\nAgent 想要使用 **%s**:\n\n```\n%s\n```\n\n回覆 **允許** / **拒絕** / **允許所有**（本次會話不再提醒）。",
		LangJapanese:           "⚠️ **権限リクエスト**\n\nエージェントが **%s** を使用しようとしています:\n\n```\n%s\n```\n\n**allow** / **deny** / **allow all**（このセッション中は全て自動許可）で返信してください。",
		LangSpanish:            "⚠️ **Solicitud de permiso**\n\nEl agente quiere usar **%s**:\n\n```\n%s\n```\n\nResponda **allow** / **deny** / **allow all** (omitir futuras solicitudes en esta sesión).",
	},
	MsgPermissionAllowed: {
		LangEnglish:            "✅ Allowed, continuing...",
		LangChinese:            "✅ 已允许，继续执行...",
		LangTraditionalChinese: "✅ 已允許，繼續執行...",
		LangJapanese:           "✅ 許可しました。続行中...",
		LangSpanish:            "✅ Permitido, continuando...",
	},
	MsgPermissionApproveAll: {
		LangEnglish:            "✅ All permissions auto-approved for this session.",
		LangChinese:            "✅ 本次会话已开启自动批准，后续权限请求将自动允许。",
		LangTraditionalChinese: "✅ 本次會話已開啟自動批准，後續權限請求將自動允許。",
		LangJapanese:           "✅ このセッションの全ての権限を自動承認に設定しました。",
		LangSpanish:            "✅ Todos los permisos se aprobarán automáticamente en esta sesión.",
	},
	MsgPermissionDenied: {
		LangEnglish:            "❌ Denied. Agent will stop this tool use.",
		LangChinese:            "❌ 已拒绝。Agent 将停止此工具使用。",
		LangTraditionalChinese: "❌ 已拒絕。Agent 將停止此工具使用。",
		LangJapanese:           "❌ 拒否しました。エージェントはこのツールの使用を中止します。",
		LangSpanish:            "❌ Denegado. El agente detendrá el uso de esta herramienta.",
	},
	MsgPermissionHint: {
		LangEnglish:            "⚠️ Waiting for permission response. Reply **allow** / **deny** / **allow all**.",
		LangChinese:            "⚠️ 等待权限响应。请回复 **允许** / **拒绝** / **允许所有**。",
		LangTraditionalChinese: "⚠️ 等待權限回應。請回覆 **允許** / **拒絕** / **允許所有**。",
		LangJapanese:           "⚠️ 権限の応答を待っています。**allow** / **deny** / **allow all** で返信してください。",
		LangSpanish:            "⚠️ Esperando respuesta de permiso. Responda **allow** / **deny** / **allow all**.",
	},
	MsgQuietOn: {
		LangEnglish:            "🔇 Quiet mode ON — thinking and tool progress messages will be hidden.",
		LangChinese:            "🔇 安静模式已开启 — 将不再推送思考和工具调用进度消息。",
		LangTraditionalChinese: "🔇 安靜模式已開啟 — 將不再推送思考和工具調用進度訊息。",
		LangJapanese:           "🔇 静音モード ON — 思考とツール実行の進捗メッセージを非表示にします。",
		LangSpanish:            "🔇 Modo silencioso activado — los mensajes de progreso se ocultarán.",
	},
	MsgQuietOff: {
		LangEnglish:            "🔔 Quiet mode OFF — thinking and tool progress messages will be shown.",
		LangChinese:            "🔔 安静模式已关闭 — 将恢复推送思考和工具调用进度消息。",
		LangTraditionalChinese: "🔔 安靜模式已關閉 — 將恢復推送思考和工具調用進度訊息。",
		LangJapanese:           "🔔 静音モード OFF — 思考とツール実行の進捗メッセージを表示します。",
		LangSpanish:            "🔔 Modo silencioso desactivado — los mensajes de progreso se mostrarán.",
	},
	MsgDisplayModeCompact: {
		LangEnglish:            "📋 Compact mode — thinking/tool hidden, each text segment sent separately.",
		LangChinese:            "📋 紧凑模式 — 隐藏思考和工具消息，每段文本独立发送。",
		LangTraditionalChinese: "📋 緊湊模式 — 隱藏思考和工具訊息，每段文字獨立發送。",
		LangJapanese:           "📋 コンパクトモード — 思考・ツール非表示、テキストは個別に送信。",
		LangSpanish:            "📋 Modo compacto — pensamiento/herramientas ocultos, cada segmento de texto enviado por separado.",
	},
	MsgQuietGlobalOn: {
		LangEnglish:            "🔇 Global quiet mode ON — all sessions will hide thinking and tool progress.",
		LangChinese:            "🔇 全局安静模式已开启 — 所有会话将不再推送思考和工具调用进度消息。",
		LangTraditionalChinese: "🔇 全域安靜模式已開啟 — 所有會話將不再推送思考和工具調用進度訊息。",
		LangJapanese:           "🔇 グローバル静音モード ON — 全セッションで思考とツール進捗を非表示にします。",
		LangSpanish:            "🔇 Modo silencioso global activado — todas las sesiones ocultarán los mensajes de progreso.",
	},
	MsgQuietGlobalOff: {
		LangEnglish:            "🔔 Global quiet mode OFF — all sessions will show thinking and tool progress.",
		LangChinese:            "🔔 全局安静模式已关闭 — 所有会话将恢复推送思考和工具调用进度消息。",
		LangTraditionalChinese: "🔔 全域安靜模式已關閉 — 所有會話將恢復推送思考和工具調用進度訊息。",
		LangJapanese:           "🔔 グローバル静音モード OFF — 全セッションで思考とツール進捗を表示します。",
		LangSpanish:            "🔔 Modo silencioso global desactivado — todas las sesiones mostrarán los mensajes de progreso.",
	},
	MsgModeChanged: {
		LangEnglish:            "🔄 Permission mode switched to **%s**. New sessions will use this mode.",
		LangChinese:            "🔄 权限模式已切换为 **%s**，新会话将使用此模式。",
		LangTraditionalChinese: "🔄 權限模式已切換為 **%s**，新會話將使用此模式。",
		LangJapanese:           "🔄 権限モードを **%s** に切り替えました。新しいセッションで有効になります。",
		LangSpanish:            "🔄 Modo de permisos cambiado a **%s**. Las nuevas sesiones usarán este modo.",
	},
	MsgModeNotSupported: {
		LangEnglish:            "This agent does not support permission mode switching.",
		LangChinese:            "当前 Agent 不支持权限模式切换。",
		LangTraditionalChinese: "當前 Agent 不支援權限模式切換。",
		LangJapanese:           "このエージェントは権限モードの切り替えをサポートしていません。",
		LangSpanish:            "Este agente no soporta el cambio de modo de permisos.",
	},
	MsgSessionRestarting: {
		LangEnglish:            "🔄 Session process exited, restarting...",
		LangChinese:            "🔄 会话进程已退出，正在重启...",
		LangTraditionalChinese: "🔄 會話進程已退出，正在重啟...",
		LangJapanese:           "🔄 セッションプロセスが終了しました。再起動中...",
		LangSpanish:            "🔄 El proceso de sesión finalizó, reiniciando...",
	},
	MsgSessionNotStarted: {
		LangEnglish:            "(new — not yet started)",
		LangChinese:            "(新会话 — 尚未开始)",
		LangTraditionalChinese: "(新會話 — 尚未開始)",
		LangJapanese:           "(新規 — まだ開始されていません)",
		LangSpanish:            "(nuevo — aún no iniciado)",
	},
	MsgUntitled: {
		LangEnglish:            "(untitled)",
		LangChinese:            "(未命名)",
		LangTraditionalChinese: "(未命名)",
		LangJapanese:           "(無題)",
		LangSpanish:            "(sin título)",
	},
	MsgLangChanged: {
		LangEnglish:            "🌐 Language switched to **%s**.",
		LangChinese:            "🌐 语言已切换为 **%s**。",
		LangTraditionalChinese: "🌐 語言已切換為 **%s**。",
		LangJapanese:           "🌐 言語を **%s** に切り替えました。",
		LangSpanish:            "🌐 Idioma cambiado a **%s**.",
	},
	MsgLangInvalid: {
		LangEnglish:            "Unknown language. Supported: `en`, `zh`, `zh-TW`, `ja`, `es`, `auto`.",
		LangChinese:            "未知语言。支持: `en`, `zh`, `zh-TW`, `ja`, `es`, `auto`。",
		LangTraditionalChinese: "未知語言。支援: `en`, `zh`, `zh-TW`, `ja`, `es`, `auto`。",
		LangJapanese:           "不明な言語です。対応: `en`, `zh`, `zh-TW`, `ja`, `es`, `auto`。",
		LangSpanish:            "Idioma desconocido. Soportados: `en`, `zh`, `zh-TW`, `ja`, `es`, `auto`.",
	},
	MsgLangCurrent: {
		LangEnglish:            "🌐 Current language: **%s**\n\nUsage: /lang <en|zh|zh-TW|ja|es|auto>",
		LangChinese:            "🌐 当前语言: **%s**\n\n用法: /lang <en|zh|zh-TW|ja|es|auto>",
		LangTraditionalChinese: "🌐 當前語言: **%s**\n\n用法: /lang <en|zh|zh-TW|ja|es|auto>",
		LangJapanese:           "🌐 現在の言語: **%s**\n\n使い方: /lang <en|zh|zh-TW|ja|es|auto>",
		LangSpanish:            "🌐 Idioma actual: **%s**\n\nUso: /lang <en|zh|zh-TW|ja|es|auto>",
	},
	MsgUnknownCommand: {
		LangEnglish:            "`%s` is not a cc-connect command, forwarding to agent...",
		LangChinese:            "`%s` 不是 cc-connect 命令，已转发给 Agent 处理...",
		LangTraditionalChinese: "`%s` 不是 cc-connect 命令，已轉發給 Agent 處理...",
		LangJapanese:           "`%s` は cc-connect のコマンドではありません。エージェントに転送します...",
		LangSpanish:            "`%s` no es un comando de cc-connect, reenviando al agente...",
	},
	MsgWelcome: {
		LangEnglish:            "👋 Hi! I'm cc-connect, bridging you to **%s**.\n\nJust send a message to chat with the agent. Type /help to see built-in commands.",
		LangChinese:            "👋 你好！我是 cc-connect，已为你连接到 **%s**。\n\n直接发送消息即可与 Agent 对话。输入 /help 查看内置命令。",
		LangTraditionalChinese: "👋 你好！我是 cc-connect，已為你連接到 **%s**。\n\n直接發送訊息即可與 Agent 對話。輸入 /help 查看內建命令。",
		LangJapanese:           "👋 こんにちは！cc-connect が **%s** に接続しました。\n\nメッセージを送信すればエージェントと会話できます。/help で組み込みコマンド一覧を確認できます。",
		LangSpanish:            "👋 ¡Hola! Soy cc-connect, conectándote con **%s**.\n\nEnvía un mensaje para chatear con el agente. Usa /help para ver los comandos integrados.",
	},
	MsgHelp: {
		LangEnglish: "📖 Available Commands\n\n" +
			"/new [name]\n  Start a new session\n/new bot <name>\n  Create a bot (private chat, admin only)\n\n" +
			"/list\n  List agent sessions\n\n" +
			"/search <keyword>\n  Search sessions by name or ID\n\n" +
			"/switch <number>\n  Resume a session by its list number\n\n" +
			"/delete <number>|1,2,3|3-7|1,3-5,8\n  Delete sessions by list number(s)\n\n" +
			"/name [number] <text>\n  Name a session for easy identification\n\n" +
			"/current\n  Show current active session\n\n" +
			"/history [n]\n  Show last n messages (default 10)\n\n" +
			"/provider [list|add|remove|switch|clear]\n  Manage API providers\n\n" +
			"/memory [add|global|global add]\n  View/edit agent memory files\n\n" +
			"/allow <tool>\n  Pre-allow a tool (next session)\n\n" +
			"/model [switch <name>]\n  View/switch model\n\n" +
			"/reasoning [level]\n  View/switch reasoning effort\n\n" +
			"/mode [name]\n  View/switch permission mode\n\n" +
			"/lang [en|zh|zh-TW|ja|es|auto]\n  View/switch language\n\n" +
			"/compress\n  Compress conversation context\n\n" +
			"/tts [always|voice_only]\n  View/switch text-to-speech mode\n\n" +
			"/shell [--timeout <sec>] <command>\n  Run a shell command and return the output (! prefix shortcut: !cmd)\n\n" +
			"/show <ref>\n  View a file, directory, or code snippet by reference\n\n" +
			"/dir [path|reset]\n  Show, switch, or reset agent working directory\n\n" +
			"/stop\n  Stop current execution\n\n" +
			"/cron [add|list|exec|del|enable|disable]\n  Manage scheduled tasks\n\n" +
			"/timer [add|list|del|mute|unmute]\n  Manage one-shot timers\n\n" +
			"/heartbeat [status|pause|resume|run|interval]\n  Manage heartbeat\n\n" +
			"/commands [add|del]\n  Manage custom slash commands\n\n" +
			"/alias [add|del]\n  Manage command aliases (e.g. 帮助 → /help)\n\n" +
			"/skills\n  List agent skills (from SKILL.md)\n\n" +
			"/config [get|set|reload] [key] [value]\n  View/update runtime configuration\n\n" +
			"/bind [project|remove]\n  Manage relay binding in group chats\n\n" +
			"/workspace [init]\n  Manage workspace\n\n" +
			"/doctor\n  Run system diagnostics\n\n" +
			"/usage\n  Show account/model quota usage\n\n" +
			"/upgrade\n  Check for updates and self-update\n\n" +
			"/restart\n  Restart cc-connect service\n\n" +
			"/status\n  Show system status\n\n" +
			"/version\n  Show cc-connect version\n\n" +
			"/whoami\n  Show your User ID (for allow_from / admin_from)\n\n" +
			"/help\n  Show this help\n\n" +
			"Tip: Commands support prefix matching, e.g. `/pro l` = `/provider list`, `/sw 2` = `/switch 2`.\n\n" +
			"Custom commands: define via `/commands add` or `[[commands]]` in config.toml.\n\n" +
			"Command aliases: use `/alias add <trigger> <command>` or `[[aliases]]` in config.toml.\n\n" +
			"Agent skills: auto-discovered from .claude/skills/<name>/SKILL.md etc.\n\n" +
			"Permission modes: default / edit / plan / yolo",
		LangChinese: "📖 可用命令\n\n" +
			"/new [名称]\n  创建新会话\n/new bot <名称>\n  创建机器人（管理员私聊）\n\n" +
			"/list\n  列出 Agent 会话列表\n\n" +
			"/search <关键词>\n  搜索会话名称或 ID\n\n" +
			"/switch <序号>\n  按列表序号切换会话\n\n" +
			"/delete <序号>|1,2,3|3-7|1,3-5,8\n  按列表序号批量/单个删除会话\n\n" +
			"/name [序号] <名称>\n  给会话命名，方便识别\n\n" +
			"/current\n  查看当前活跃会话\n\n" +
			"/history [n]\n  查看最近 n 条消息（默认 10）\n\n" +
			"/provider [list|add|remove|switch|clear]\n  管理 API Provider\n\n" +
			"/memory [add|global|global add]\n  查看/编辑 Agent 记忆文件\n\n" +
			"/allow <工具名>\n  预授权工具（下次会话生效）\n\n" +
			"/model [switch <名称>]\n  查看/切换模型\n\n" +
			"/reasoning [级别]\n  查看/切换推理强度\n\n" +
			"/mode [名称]\n  查看/切换权限模式\n\n" +
			"/lang [en|zh|zh-TW|ja|es|auto]\n  查看/切换语言\n\n" +
			"/compress\n  压缩会话上下文\n\n" +
			"/tts [always|voice_only]\n  查看/切换语音合成模式\n\n" +
			"/shell [--timeout <秒>] <命令>\n  执行 Shell 命令并返回结果（快捷方式：!命令）\n\n" +
			"/show <引用>\n  按引用查看文件、目录或代码片段\n\n" +
			"/dir [路径|reset]\n  查看、切换或重置 Agent 工作目录\n\n" +
			"/stop\n  停止当前执行\n\n" +
			"/cron [add|list|exec|del|enable|disable]\n  管理定时任务\n\n" +
			"/timer [add|list|del|mute|unmute]\n  管理一次性定时器\n\n" +
			"/heartbeat [status|pause|resume|run|interval]\n  管理心跳\n\n" +
			"/commands [add|del]\n  管理自定义命令\n\n" +
			"/alias [add|del]\n  管理命令别名（如 帮助 → /help）\n\n" +
			"/skills\n  列出 Agent Skills（来自 SKILL.md）\n\n" +
			"/config [get|set|reload] [key] [value]\n  查看/修改运行时配置\n\n" +
			"/bind [项目名|remove]\n  管理群聊中继绑定\n\n" +
			"/workspace [init]\n  管理工作区\n\n" +
			"/doctor\n  运行系统诊断\n\n" +
			"/usage\n  查看账号/模型限额使用情况\n\n" +
			"/upgrade\n  检查更新并自动升级\n\n" +
			"/restart\n  重启 cc-connect 服务\n\n" +
			"/status\n  查看系统状态\n\n" +
			"/version\n  查看 cc-connect 版本\n\n" +
			"/whoami\n  查看你的 User ID（用于 allow_from / admin_from 配置）\n\n" +
			"/help\n  显示此帮助\n\n" +
			"提示：命令支持前缀匹配，如 `/pro l` = `/provider list`，`/sw 2` = `/switch 2`。\n\n" +
			"自定义命令：通过 `/commands add` 添加，或在 config.toml 中配置 `[[commands]]`。\n\n" +
			"命令别名：使用 `/alias add <触发词> <命令>` 或在 config.toml 中配置 `[[aliases]]`。\n\n" +
			"Agent Skills：自动发现自 .claude/skills/<name>/SKILL.md 等目录。\n\n" +
			"权限模式：default / edit / plan / yolo",
		LangTraditionalChinese: "📖 可用命令\n\n" +
			"/new [名稱]\n  建立新會話\n/new bot <名稱>\n  建立機器人（管理員私聊）\n\n" +
			"/list\n  列出 Agent 會話列表\n\n" +
			"/search <關鍵詞>\n  搜尋會話名稱或 ID\n\n" +
			"/switch <序號>\n  按列表序號切換會話\n\n" +
			"/delete <序號>|1,2,3|3-7|1,3-5,8\n  按列表序號批量/單筆刪除會話\n\n" +
			"/name [序號] <名稱>\n  為會話命名，方便辨識\n\n" +
			"/current\n  查看當前活躍會話\n\n" +
			"/history [n]\n  查看最近 n 條訊息（預設 10）\n\n" +
			"/provider [list|add|remove|switch|clear]\n  管理 API Provider\n\n" +
			"/memory [add|global|global add]\n  查看/編輯 Agent 記憶檔案\n\n" +
			"/allow <工具名>\n  預授權工具（下次會話生效）\n\n" +
			"/model [switch <名稱>]\n  查看/切換模型\n\n" +
			"/reasoning [級別]\n  查看/切換推理強度\n\n" +
			"/mode [名稱]\n  查看/切換權限模式\n\n" +
			"/lang [en|zh|zh-TW|ja|es|auto]\n  查看/切換語言\n\n" +
			"/compress\n  壓縮會話上下文\n\n" +
			"/tts [always|voice_only]\n  查看/切換語音合成模式\n\n" +
			"/shell [--timeout <秒>] <命令>\n  執行 Shell 命令並返回結果（快捷方式：!命令）\n\n" +
			"/dir [路徑|reset]\n  查看、切換或重置 Agent 工作目錄\n\n" +
			"/stop\n  停止當前執行\n\n" +
			"/cron [add|list|exec|del|enable|disable]\n  管理定時任務\n\n" +
			"/timer [add|list|del|mute|unmute]\n  管理一次性定時器\n\n" +
			"/heartbeat [status|pause|resume|run|interval]\n  管理心跳\n\n" +
			"/commands [add|del]\n  管理自訂命令\n\n" +
			"/alias [add|del]\n  管理命令別名（如 幫助 → /help）\n\n" +
			"/skills\n  列出 Agent Skills（來自 SKILL.md）\n\n" +
			"/config [get|set|reload] [key] [value]\n  查看/修改執行階段配置\n\n" +
			"/bind [項目名|remove]\n  管理群聊中繼綁定\n\n" +
			"/workspace [init]\n  管理工作區\n\n" +
			"/doctor\n  執行系統診斷\n\n" +
			"/usage\n  查看帳號/模型限額使用情況\n\n" +
			"/upgrade\n  檢查更新並自動升級\n\n" +
			"/restart\n  重啟 cc-connect 服務\n\n" +
			"/status\n  查看系統狀態\n\n" +
			"/version\n  查看 cc-connect 版本\n\n" +
			"/whoami\n  查看你的 User ID（用於 allow_from / admin_from 設定）\n\n" +
			"/help\n  顯示此說明\n\n" +
			"提示：命令支持前綴匹配，如 `/pro l` = `/provider list`，`/sw 2` = `/switch 2`。\n\n" +
			"自訂命令：透過 `/commands add` 新增，或在 config.toml 中配置 `[[commands]]`。\n\n" +
			"命令別名：使用 `/alias add <觸發詞> <命令>` 或在 config.toml 中配置 `[[aliases]]`。\n\n" +
			"Agent Skills：自動發現自 .claude/skills/<name>/SKILL.md 等目錄。\n\n" +
			"權限模式：default / edit / plan / yolo",
		LangJapanese: "📖 利用可能なコマンド\n\n" +
			"/new [名前]\n  新しいセッションを開始\n/new bot <名前>\n  ボットを作成（管理者の個人チャット）\n\n" +
			"/list\n  エージェントセッション一覧\n\n" +
			"/switch <番号>\n  リスト番号でセッションを切り替え\n\n" +
			"/delete <番号>|1,2,3|3-7|1,3-5,8\n  リスト番号でセッションを単体/複数削除\n\n" +
			"/name [番号] <名前>\n  セッションに名前を付ける\n\n" +
			"/current\n  現在のアクティブセッションを表示\n\n" +
			"/history [n]\n  直近 n 件のメッセージを表示（デフォルト 10）\n\n" +
			"/provider [list|add|remove|switch|clear]\n  API プロバイダ管理\n\n" +
			"/memory [add|global|global add]\n  エージェントメモリの表示/編集\n\n" +
			"/allow <ツール名>\n  ツールを事前許可（次のセッションで有効）\n\n" +
			"/model [switch <名前>]\n  モデルの表示/切り替え\n\n" +
			"/reasoning [レベル]\n  推論レベルの表示/切り替え\n\n" +
			"/mode [名前]\n  権限モードの表示/切り替え\n\n" +
			"/lang [en|zh|zh-TW|ja|es|auto]\n  言語の表示/切り替え\n\n" +
			"/compress\n  会話コンテキストを圧縮\n\n" +
			"/tts [always|voice_only]\n  音声合成モードの表示/切り替え\n\n" +
			"/shell [--timeout <秒>] <コマンド>\n  シェルコマンドを実行して結果を返す（ショートカット：!コマンド）\n\n" +
			"/dir [パス|reset]\n  エージェントの作業ディレクトリを表示/切り替え/リセット\n\n" +
			"/stop\n  現在の実行を停止\n\n" +
			"/cron [add|list|exec|del|enable|disable]\n  スケジュールタスク管理\n\n" +
			"/timer [add|list|del|mute|unmute]\n  ワンショットタイマー管理\n\n" +
			"/heartbeat [status|pause|resume|run|interval]\n  ハートビート管理\n\n" +
			"/commands [add|del]\n  カスタムコマンド管理\n\n" +
			"/alias [add|del]\n  コマンドエイリアス管理（例: ヘルプ → /help）\n\n" +
			"/skills\n  エージェントスキル一覧（SKILL.md から）\n\n" +
			"/config [get|set|reload] [key] [value]\n  ランタイム設定の表示/変更\n\n" +
			"/bind [プロジェクト|remove]\n  グループチャットのリレー管理\n\n" +
			"/workspace [init]\n  ワークスペース管理\n\n" +
			"/doctor\n  システム診断を実行\n\n" +
			"/usage\n  アカウント/モデル使用量を表示\n\n" +
			"/upgrade\n  アップデートを確認して自動更新\n\n" +
			"/restart\n  cc-connect サービスを再起動\n\n" +
			"/status\n  システム状態を表示\n\n" +
			"/version\n  cc-connect のバージョンを表示\n\n" +
			"/whoami\n  あなたの User ID を表示（allow_from / admin_from 設定用）\n\n" +
			"/help\n  このヘルプを表示\n\n" +
			"ヒント：コマンドはプレフィックスマッチに対応しています。例: `/pro l` = `/provider list`、`/sw 2` = `/switch 2`。\n\n" +
			"カスタムコマンド: `/commands add` または config.toml の `[[commands]]` で定義。\n\n" +
			"コマンドエイリアス: `/alias add <トリガー> <コマンド>` または config.toml の `[[aliases]]` で定義。\n\n" +
			"エージェントスキル: .claude/skills/<name>/SKILL.md などから自動検出。\n\n" +
			"権限モード: default / edit / plan / yolo",
		LangSpanish: "📖 Comandos disponibles\n\n" +
			"/new [nombre]\n  Iniciar una nueva sesión\n/new bot <nombre>\n  Crear bot (chat privado, administrador)\n\n" +
			"/list\n  Listar sesiones del agente\n\n" +
			"/switch <número>\n  Reanudar sesión por su número en la lista\n\n" +
			"/delete <número>|1,2,3|3-7|1,3-5,8\n  Eliminar una o varias sesiones por número de lista\n\n" +
			"/name [número] <texto>\n  Nombrar una sesión para fácil identificación\n\n" +
			"/current\n  Mostrar sesión activa actual\n\n" +
			"/history [n]\n  Mostrar últimos n mensajes (por defecto 10)\n\n" +
			"/provider [list|add|remove|switch|clear]\n  Gestionar proveedores API\n\n" +
			"/memory [add|global|global add]\n  Ver/editar archivos de memoria del agente\n\n" +
			"/allow <herramienta>\n  Pre-autorizar herramienta (próxima sesión)\n\n" +
			"/model [switch <nombre>]\n  Ver/cambiar modelo\n\n" +
			"/reasoning [nivel]\n  Ver/cambiar nivel de razonamiento\n\n" +
			"/mode [nombre]\n  Ver/cambiar modo de permisos\n\n" +
			"/lang [en|zh|zh-TW|ja|es|auto]\n  Ver/cambiar idioma\n\n" +
			"/compress\n  Comprimir contexto de conversación\n\n" +
			"/tts [always|voice_only]\n  Ver/cambiar modo de síntesis de voz\n\n" +
			"/shell [--timeout <seg>] <comando>\n  Ejecutar un comando shell y devolver la salida (atajo: !comando)\n\n" +
			"/dir [ruta|reset]\n  Ver, cambiar o restablecer el directorio de trabajo del agente\n\n" +
			"/stop\n  Detener ejecución actual\n\n" +
			"/cron [add|list|exec|del|enable|disable]\n  Gestionar tareas programadas\n\n" +
			"/timer [add|list|del|mute|unmute]\n  Gestionar temporizadores de uso único\n\n" +
			"/heartbeat [status|pause|resume|run|interval]\n  Gestionar heartbeat\n\n" +
			"/commands [add|del]\n  Gestionar comandos personalizados\n\n" +
			"/alias [add|del]\n  Gestionar alias de comandos (ej. ayuda → /help)\n\n" +
			"/skills\n  Listar skills del agente (desde SKILL.md)\n\n" +
			"/config [get|set|reload] [key] [value]\n  Ver/actualizar configuración en tiempo de ejecución\n\n" +
			"/bind [proyecto|remove]\n  Gestionar retransmisión en chats de grupo\n\n" +
			"/workspace [init]\n  Gestionar workspace\n\n" +
			"/doctor\n  Ejecutar diagnósticos del sistema\n\n" +
			"/usage\n  Mostrar uso de cuota de cuenta/modelo\n\n" +
			"/upgrade\n  Buscar actualizaciones y auto-actualizar\n\n" +
			"/restart\n  Reiniciar el servicio cc-connect\n\n" +
			"/status\n  Mostrar estado del sistema\n\n" +
			"/version\n  Mostrar versión de cc-connect\n\n" +
			"/whoami\n  Mostrar tu User ID (para allow_from / admin_from)\n\n" +
			"/help\n  Mostrar esta ayuda\n\n" +
			"Consejo: Los comandos admiten coincidencia por prefijo, ej. `/pro l` = `/provider list`, `/sw 2` = `/switch 2`.\n\n" +
			"Comandos personalizados: use `/commands add` o defina `[[commands]]` en config.toml.\n\n" +
			"Alias de comandos: use `/alias add <trigger> <comando>` o `[[aliases]]` en config.toml.\n\n" +
			"Skills del agente: descubiertos de .claude/skills/<name>/SKILL.md etc.\n\n" +
			"Modos de permisos: default / edit / plan / yolo",
	},
	MsgHelpTitle: {
		LangEnglish:            "cc-connect Help",
		LangChinese:            "cc-connect 帮助",
		LangTraditionalChinese: "cc-connect 說明",
		LangJapanese:           "cc-connect ヘルプ",
		LangSpanish:            "cc-connect Ayuda",
	},
	MsgHelpSessionSection: {
		LangEnglish: "**Session Management**\n" +
			"/new [name] — Start a new session\n" +
			"/list — List agent sessions\n" +
			"/search <keyword> — Search sessions\n" +
			"/switch <number> — Resume a session\n" +
			"/delete <number>|1,2,3|3-7|1,3-5,8 — Delete session(s)\n" +
			"/name [number] <text> — Name a session\n" +
			"/current — Show active session\n" +
			"/history [n] — Show last n messages",
		LangChinese: "**会话管理**\n" +
			"/new [名称] — 创建新会话\n" +
			"/list — 列出会话列表\n" +
			"/search <关键词> — 搜索会话\n" +
			"/switch <序号> — 切换会话\n" +
			"/delete <序号>|1,2,3|3-7|1,3-5,8 — 删除会话\n" +
			"/name [序号] <名称> — 命名会话\n" +
			"/current — 查看当前会话\n" +
			"/history [n] — 查看最近 n 条消息",
		LangTraditionalChinese: "**會話管理**\n" +
			"/new [名稱] — 建立新會話\n" +
			"/list — 列出會話列表\n" +
			"/search <關鍵詞> — 搜尋會話\n" +
			"/switch <序號> — 切換會話\n" +
			"/delete <序號>|1,2,3|3-7|1,3-5,8 — 刪除會話\n" +
			"/name [序號] <名稱> — 命名會話\n" +
			"/current — 查看當前會話\n" +
			"/history [n] — 查看最近 n 條訊息",
		LangJapanese: "**セッション管理**\n" +
			"/new [名前] — 新しいセッションを開始\n" +
			"/list — セッション一覧\n" +
			"/search <キーワード> — セッション検索\n" +
			"/switch <番号> — セッション切り替え\n" +
			"/delete <番号>|1,2,3|3-7|1,3-5,8 — セッション削除\n" +
			"/name [番号] <名前> — セッションに名前を付ける\n" +
			"/current — 現在のセッションを表示\n" +
			"/history [n] — 直近 n 件のメッセージを表示",
		LangSpanish: "**Gestión de sesiones**\n" +
			"/new [nombre] — Iniciar nueva sesión\n" +
			"/list — Listar sesiones\n" +
			"/search <keyword> — Buscar sesiones\n" +
			"/switch <número> — Reanudar sesión\n" +
			"/delete <número>|1,2,3|3-7|1,3-5,8 — Eliminar sesión(es)\n" +
			"/name [número] <texto> — Nombrar sesión\n" +
			"/current — Mostrar sesión activa\n" +
			"/history [n] — Mostrar últimos n mensajes",
	},
	MsgHelpAgentSection: {
		LangEnglish: "**Agent Configuration**\n" +
			"/model [switch <name>] — View/switch model\n" +
			"/mode [name] — View/switch permission mode\n" +
			"/provider [list|add|...] — Manage API providers\n" +
			"/memory [add|global|...] — View/edit memory files\n" +
			"/allow <tool> — Pre-allow a tool\n" +
			"/lang [en|zh|...] — View/switch language",
		LangChinese: "**Agent 配置**\n" +
			"/model [switch <名称>] — 查看/切换模型\n" +
			"/mode [名称] — 查看/切换权限模式\n" +
			"/provider [list|add|...] — 管理 API Provider\n" +
			"/memory [add|global|...] — 查看/编辑记忆文件\n" +
			"/allow <工具名> — 预授权工具\n" +
			"/lang [en|zh|...] — 查看/切换语言",
		LangTraditionalChinese: "**Agent 配置**\n" +
			"/model [switch <名稱>] — 查看/切換模型\n" +
			"/mode [名稱] — 查看/切換權限模式\n" +
			"/provider [list|add|...] — 管理 API Provider\n" +
			"/memory [add|global|...] — 查看/編輯記憶檔案\n" +
			"/allow <工具名> — 預授權工具\n" +
			"/lang [en|zh|...] — 查看/切換語言",
		LangJapanese: "**エージェント設定**\n" +
			"/model [switch <名前>] — モデルの表示/切り替え\n" +
			"/mode [名前] — 権限モードの表示/切り替え\n" +
			"/provider [list|add|...] — API プロバイダ管理\n" +
			"/memory [add|global|...] — メモリの表示/編集\n" +
			"/allow <ツール名> — ツールを事前許可\n" +
			"/lang [en|zh|...] — 言語の表示/切り替え",
		LangSpanish: "**Configuración del agente**\n" +
			"/model [switch <nombre>] — Ver/cambiar modelo\n" +
			"/mode [nombre] — Ver/cambiar modo de permisos\n" +
			"/provider [list|add|...] — Gestionar proveedores\n" +
			"/memory [add|global|...] — Ver/editar memoria\n" +
			"/allow <herramienta> — Pre-autorizar herramienta\n" +
			"/lang [en|zh|...] — Ver/cambiar idioma",
	},
	MsgHelpToolsSection: {
		LangEnglish: "**Tools & Automation**\n" +
			"/shell <command> — Run a shell command (! shortcut)\n" +
			"/show <ref> — View file / directory / snippet by reference\n" +
			"/dir [path|reset] — Show, switch, or reset work directory\n" +
			"/cron [add|list|exec|del|...] — Scheduled tasks\n" +
			"/timer [add|list|del|...] — One-shot timers\n" +
			"/commands [add|del] — Custom commands\n" +
			"/alias [add|del] — Command aliases\n" +
			"/skills — List agent skills\n" +
			"/compress — Compress context\n" +
			"/stop — Stop current execution",
		LangChinese: "**工具与自动化**\n" +
			"/shell <命令> — 执行 Shell 命令（!快捷方式）\n" +
			"/show <引用> — 按引用查看文件、目录或代码片段\n" +
			"/dir [路径|reset] — 查看、切换或重置工作目录\n" +
			"/cron [add|list|exec|del|...] — 定时任务\n" +
			"/timer [add|list|del|...] — 一次性定时器\n" +
			"/commands [add|del] — 自定义命令\n" +
			"/alias [add|del] — 命令别名\n" +
			"/skills — 列出 Agent Skills\n" +
			"/compress — 压缩上下文\n" +
			"/stop — 停止当前执行",
		LangTraditionalChinese: "**工具與自動化**\n" +
			"/shell <命令> — 執行 Shell 命令（!快捷方式）\n" +
			"/show <引用> — 按引用查看檔案、目錄或程式碼片段\n" +
			"/dir [路徑|reset] — 查看、切換或重置工作目錄\n" +
			"/cron [add|list|exec|del|...] — 定時任務\n" +
			"/timer [add|list|del|...] — 一次性定時器\n" +
			"/commands [add|del] — 自訂命令\n" +
			"/alias [add|del] — 命令別名\n" +
			"/skills — 列出 Agent Skills\n" +
			"/compress — 壓縮上下文\n" +
			"/stop — 停止當前執行",
		LangJapanese: "**ツール・自動化**\n" +
			"/shell <コマンド> — シェルコマンド実行（!ショートカット）\n" +
			"/show <参照> — ファイル/ディレクトリ/スニペットを参照で表示\n" +
			"/dir [パス|reset] — 作業ディレクトリの表示/切り替え/リセット\n" +
			"/cron [add|list|exec|del|...] — スケジュールタスク\n" +
			"/timer [add|list|del|...] — ワンショットタイマー\n" +
			"/commands [add|del] — カスタムコマンド\n" +
			"/alias [add|del] — コマンドエイリアス\n" +
			"/skills — エージェントスキル一覧\n" +
			"/compress — コンテキスト圧縮\n" +
			"/stop — 現在の実行を停止",
		LangSpanish: "**Herramientas y automatización**\n" +
			"/shell <comando> — Ejecutar comando shell (! atajo)\n" +
			"/show <ref> — Ver archivo/directorio/fragmento por referencia\n" +
			"/dir [ruta|reset] — Ver, cambiar o restablecer directorio de trabajo\n" +
			"/cron [add|list|exec|del|...] — Tareas programadas\n" +
			"/timer [add|list|del|...] — Temporizadores de uso único\n" +
			"/commands [add|del] — Comandos personalizados\n" +
			"/alias [add|del] — Alias de comandos\n" +
			"/skills — Listar skills del agente\n" +
			"/compress — Comprimir contexto\n" +
			"/stop — Detener ejecución actual",
	},
	MsgHelpSystemSection: {
		LangEnglish: "**System**\n" +
			"/config [get|set|reload] — Runtime configuration\n" +
			"/doctor — System diagnostics\n" +
			"/usage — Account/model quota usage\n" +
			"/whoami — Show your User ID\n" +
			"/upgrade — Check for updates\n" +
			"/restart — Restart service\n" +
			"/status — System status\n" +
			"/version — Show version",
		LangChinese: "**系统**\n" +
			"/config [get|set|reload] — 运行时配置\n" +
			"/doctor — 系统诊断\n" +
			"/usage — 账号/模型限额\n" +
			"/whoami — 查看你的 User ID\n" +
			"/upgrade — 检查更新\n" +
			"/restart — 重启服务\n" +
			"/status — 系统状态\n" +
			"/version — 查看版本",
		LangTraditionalChinese: "**系統**\n" +
			"/config [get|set|reload] — 執行階段配置\n" +
			"/doctor — 系統診斷\n" +
			"/usage — 帳號/模型限額\n" +
			"/whoami — 查看你的 User ID\n" +
			"/upgrade — 檢查更新\n" +
			"/restart — 重啟服務\n" +
			"/status — 系統狀態\n" +
			"/version — 查看版本",
		LangJapanese: "**システム**\n" +
			"/config [get|set|reload] — ランタイム設定\n" +
			"/doctor — システム診断\n" +
			"/usage — アカウント/モデル使用量\n" +
			"/whoami — User ID を表示\n" +
			"/upgrade — アップデート確認\n" +
			"/restart — サービス再起動\n" +
			"/status — システム状態\n" +
			"/version — バージョン表示",
		LangSpanish: "**Sistema**\n" +
			"/config [get|set|reload] — Configuración\n" +
			"/doctor — Diagnósticos del sistema\n" +
			"/usage — Uso de cuota de cuenta/modelo\n" +
			"/whoami — Mostrar tu User ID\n" +
			"/upgrade — Buscar actualizaciones\n" +
			"/restart — Reiniciar servicio\n" +
			"/status — Estado del sistema\n" +
			"/version — Mostrar versión",
	},
	MsgHelpTip: {
		LangEnglish:            "Tip: Commands support prefix matching, e.g. /pro l = /provider list",
		LangChinese:            "提示：命令支持前缀匹配，如 /pro l = /provider list",
		LangTraditionalChinese: "提示：命令支持前綴匹配，如 /pro l = /provider list",
		LangJapanese:           "ヒント：コマンドはプレフィックスマッチに対応、例: /pro l = /provider list",
		LangSpanish:            "Consejo: Los comandos admiten coincidencia por prefijo, ej. /pro l = /provider list",
	},
	MsgListTitle: {
		LangEnglish:            "**%s Sessions** (%d)\n\n",
		LangChinese:            "**%s 会话列表** (%d)\n\n",
		LangTraditionalChinese: "**%s 會話列表** (%d)\n\n",
		LangJapanese:           "**%s セッション** (%d)\n\n",
		LangSpanish:            "**Sesiones de %s** (%d)\n\n",
	},
	MsgListTitlePaged: {
		LangEnglish:            "**%s Sessions** (%d) · Page %d/%d\n\n",
		LangChinese:            "**%s 会话列表** (%d) · 第 %d/%d 页\n\n",
		LangTraditionalChinese: "**%s 會話列表** (%d) · 第 %d/%d 頁\n\n",
		LangJapanese:           "**%s セッション** (%d) · %d/%d ページ\n\n",
		LangSpanish:            "**Sesiones de %s** (%d) · Página %d/%d\n\n",
	},
	MsgListEmpty: {
		LangEnglish:            "No sessions found for this project.",
		LangChinese:            "未找到此项目的会话。",
		LangTraditionalChinese: "未找到此項目的會話。",
		LangJapanese:           "このプロジェクトのセッションが見つかりません。",
		LangSpanish:            "No se encontraron sesiones para este proyecto.",
	},
	MsgListMore: {
		LangEnglish:            "\n... and %d more\n",
		LangChinese:            "\n... 还有 %d 条\n",
		LangTraditionalChinese: "\n... 還有 %d 條\n",
		LangJapanese:           "\n... 他 %d 件\n",
		LangSpanish:            "\n... y %d más\n",
	},
	MsgListPageHint: {
		LangEnglish:            "\n\nPage %d/%d \n\n`/list <page>` for more\n",
		LangChinese:            "\n\n第 %d/%d 页 \n\n`/list <页码>` 翻页\n",
		LangTraditionalChinese: "\n\n第 %d/%d 頁 \n\n`/list <頁碼>` 翻頁\n",
		LangJapanese:           "\n\n%d/%d ページ \n\n`/list <ページ>` で移動\n",
		LangSpanish:            "\n\nPágina %d/%d \n\n`/list <página>` para más\n",
	},
	MsgListSwitchHint: {
		LangEnglish:            "\n`/switch <number>` to switch session",
		LangChinese:            "\n`/switch <序号>` 切换会话",
		LangTraditionalChinese: "\n`/switch <序號>` 切換會話",
		LangJapanese:           "\n`/switch <番号>` でセッション切替",
		LangSpanish:            "\n`/switch <número>` para cambiar sesión",
	},
	MsgListError: {
		LangEnglish:            "❌ Failed to list sessions: %v",
		LangChinese:            "❌ 获取会话列表失败: %v",
		LangTraditionalChinese: "❌ 取得會話列表失敗: %v",
		LangJapanese:           "❌ セッション一覧の取得に失敗しました: %v",
		LangSpanish:            "❌ Error al listar sesiones: %v",
	},
	MsgHistoryEmpty: {
		LangEnglish:            "No history in current session.",
		LangChinese:            "当前会话暂无历史消息。",
		LangTraditionalChinese: "當前會話暫無歷史訊息。",
		LangJapanese:           "現在のセッションに履歴がありません。",
		LangSpanish:            "No hay historial en la sesión actual.",
	},
	MsgNameUsage: {
		LangEnglish:            "Usage:\n`/name <text>` — name the current session\n`/name <number> <text>` — name a session by list number",
		LangChinese:            "用法：\n`/name <名称>` — 命名当前会话\n`/name <序号> <名称>` — 按列表序号命名会话",
		LangTraditionalChinese: "用法：\n`/name <名稱>` — 命名當前會話\n`/name <序號> <名稱>` — 按列表序號命名會話",
		LangJapanese:           "使い方：\n`/name <名前>` — 現在のセッションに名前を付ける\n`/name <番号> <名前>` — リスト番号でセッションに名前を付ける",
		LangSpanish:            "Uso:\n`/name <texto>` — nombrar la sesión actual\n`/name <número> <texto>` — nombrar una sesión por número de lista",
	},
	MsgNameSet: {
		LangEnglish:            "✅ Session named: **%s** (%s)",
		LangChinese:            "✅ 会话已命名：**%s** (%s)",
		LangTraditionalChinese: "✅ 會話已命名：**%s** (%s)",
		LangJapanese:           "✅ セッション名設定：**%s** (%s)",
		LangSpanish:            "✅ Sesión nombrada: **%s** (%s)",
	},
	MsgNameNoSession: {
		LangEnglish:            "❌ No active session. Send a message first or switch to a session.",
		LangChinese:            "❌ 没有活跃会话，请先发送消息或切换到一个会话。",
		LangTraditionalChinese: "❌ 沒有活躍會話，請先傳送訊息或切換到一個會話。",
		LangJapanese:           "❌ アクティブなセッションがありません。メッセージを送信するかセッションに切り替えてください。",
		LangSpanish:            "❌ No hay sesión activa. Envía un mensaje primero o cambia a una sesión.",
	},
	MsgProviderNotSupported: {
		LangEnglish:            "This agent does not support provider switching.",
		LangChinese:            "当前 Agent 不支持 Provider 切换。",
		LangTraditionalChinese: "當前 Agent 不支援 Provider 切換。",
		LangJapanese:           "このエージェントはプロバイダの切り替えをサポートしていません。",
		LangSpanish:            "Este agente no soporta el cambio de proveedor.",
	},
	MsgProviderNone: {
		LangEnglish:            "No provider configured. Using agent's default environment.\n\nAdd providers in `config.toml` or via `cc-connect provider add`.",
		LangChinese:            "未配置 Provider，使用 Agent 默认环境。\n\n可在 `config.toml` 中添加或使用 `cc-connect provider add` 命令。",
		LangTraditionalChinese: "未配置 Provider，使用 Agent 預設環境。\n\n可在 `config.toml` 中新增或使用 `cc-connect provider add` 命令。",
		LangJapanese:           "プロバイダが設定されていません。エージェントのデフォルト環境を使用します。\n\n`config.toml` または `cc-connect provider add` でプロバイダを追加してください。",
		LangSpanish:            "No hay proveedor configurado. Usando el entorno predeterminado del agente.\n\nAgregue proveedores en `config.toml` o mediante `cc-connect provider add`.",
	},
	MsgProviderCurrent: {
		LangEnglish:            "📡 Active provider: **%s**\n\nUse `/provider list` to see all, `/provider switch <name>` to switch.",
		LangChinese:            "📡 当前 Provider: **%s**\n\n使用 `/provider list` 查看全部，`/provider switch <名称>` 切换。",
		LangTraditionalChinese: "📡 當前 Provider: **%s**\n\n使用 `/provider list` 查看全部，`/provider switch <名稱>` 切換。",
		LangJapanese:           "📡 現在のプロバイダ: **%s**\n\n`/provider list` で一覧、`/provider switch <名前>` で切り替え。",
		LangSpanish:            "📡 Proveedor activo: **%s**\n\nUse `/provider list` para ver todos, `/provider switch <nombre>` para cambiar.",
	},
	MsgProviderListTitle: {
		LangEnglish:            "📡 Providers\n\n",
		LangChinese:            "📡 Provider 列表\n\n",
		LangTraditionalChinese: "📡 Provider 列表\n\n",
		LangJapanese:           "📡 プロバイダ一覧\n\n",
		LangSpanish:            "📡 Proveedores\n\n",
	},
	MsgProviderListEmpty: {
		LangEnglish:            "No providers configured.\n\nAdd providers in `config.toml` or via `cc-connect provider add`.",
		LangChinese:            "未配置 Provider。\n\n可在 `config.toml` 中添加或使用 `cc-connect provider add` 命令。",
		LangTraditionalChinese: "未配置 Provider。\n\n可在 `config.toml` 中新增或使用 `cc-connect provider add` 命令。",
		LangJapanese:           "プロバイダが設定されていません。\n\n`config.toml` または `cc-connect provider add` で追加してください。",
		LangSpanish:            "No hay proveedores configurados.\n\nAgregue proveedores en `config.toml` o mediante `cc-connect provider add`.",
	},
	MsgProviderSwitchHint: {
		LangEnglish:            "`/provider switch <name>` to switch | `/provider clear` to reset",
		LangChinese:            "`/provider switch <名称>` 切换 | `/provider clear` 清除",
		LangTraditionalChinese: "`/provider switch <名稱>` 切換 | `/provider clear` 清除",
		LangJapanese:           "`/provider switch <名前>` で切り替え | `/provider clear` でリセット",
		LangSpanish:            "`/provider switch <nombre>` para cambiar | `/provider clear` para restablecer",
	},
	MsgProviderNotFound: {
		LangEnglish:            "❌ Provider %q not found. Use `/provider list` to see available providers.",
		LangChinese:            "❌ 未找到 Provider %q。使用 `/provider list` 查看可用列表。",
		LangTraditionalChinese: "❌ 未找到 Provider %q。使用 `/provider list` 查看可用列表。",
		LangJapanese:           "❌ プロバイダ %q が見つかりません。`/provider list` で一覧を確認してください。",
		LangSpanish:            "❌ Proveedor %q no encontrado. Use `/provider list` para ver los disponibles.",
	},
	MsgProviderSwitched: {
		LangEnglish:            "✅ Provider switched to **%s**. New sessions will use this provider.",
		LangChinese:            "✅ Provider 已切换为 **%s**，新会话将使用此 Provider。",
		LangTraditionalChinese: "✅ Provider 已切換為 **%s**，新會話將使用此 Provider。",
		LangJapanese:           "✅ プロバイダを **%s** に切り替えました。新しいセッションで使用されます。",
		LangSpanish:            "✅ Proveedor cambiado a **%s**. Las nuevas sesiones usarán este proveedor.",
	},
	MsgProviderCleared: {
		LangEnglish:            "✅ Provider cleared. New sessions will use the default provider.",
		LangChinese:            "✅ Provider 已清除，新会话将使用默认 Provider。",
		LangTraditionalChinese: "✅ Provider 已清除，新會話將使用預設 Provider。",
		LangJapanese:           "✅ プロバイダをクリアしました。新しいセッションではデフォルトのプロバイダが使用されます。",
		LangSpanish:            "✅ Proveedor eliminado. Las nuevas sesiones usarán el proveedor predeterminado.",
	},
	MsgProviderAdded: {
		LangEnglish:            "✅ Provider **%s** added.\n\nUse `/provider switch %s` to activate.",
		LangChinese:            "✅ Provider **%s** 已添加。\n\n使用 `/provider switch %s` 激活。",
		LangTraditionalChinese: "✅ Provider **%s** 已新增。\n\n使用 `/provider switch %s` 啟用。",
		LangJapanese:           "✅ プロバイダ **%s** を追加しました。\n\n`/provider switch %s` で有効化してください。",
		LangSpanish:            "✅ Proveedor **%s** agregado.\n\nUse `/provider switch %s` para activarlo.",
	},
	MsgProviderAddUsage: {
		LangEnglish: "Usage:\n\n" +
			"`/provider add <name> <api_key> [base_url] [model]`\n\n" +
			"Or JSON:\n" +
			"`/provider add {\"name\":\"relay\",\"api_key\":\"sk-xxx\",\"base_url\":\"https://...\",\"model\":\"...\"}`",
		LangChinese: "用法:\n\n" +
			"`/provider add <名称> <api_key> [base_url] [model]`\n\n" +
			"或 JSON:\n" +
			"`/provider add {\"name\":\"relay\",\"api_key\":\"sk-xxx\",\"base_url\":\"https://...\",\"model\":\"...\"}`",
		LangTraditionalChinese: "用法:\n\n" +
			"`/provider add <名稱> <api_key> [base_url] [model]`\n\n" +
			"或 JSON:\n" +
			"`/provider add {\"name\":\"relay\",\"api_key\":\"sk-xxx\",\"base_url\":\"https://...\",\"model\":\"...\"}`",
		LangJapanese: "使い方:\n\n" +
			"`/provider add <名前> <api_key> [base_url] [model]`\n\n" +
			"または JSON:\n" +
			"`/provider add {\"name\":\"relay\",\"api_key\":\"sk-xxx\",\"base_url\":\"https://...\",\"model\":\"...\"}`",
		LangSpanish: "Uso:\n\n" +
			"`/provider add <nombre> <api_key> [base_url] [model]`\n\n" +
			"O JSON:\n" +
			"`/provider add {\"name\":\"relay\",\"api_key\":\"sk-xxx\",\"base_url\":\"https://...\",\"model\":\"...\"}`",
	},
	MsgProviderAddFailed: {
		LangEnglish:            "❌ Failed to add provider: %v",
		LangChinese:            "❌ 添加 Provider 失败: %v",
		LangTraditionalChinese: "❌ 新增 Provider 失敗: %v",
		LangJapanese:           "❌ プロバイダの追加に失敗しました: %v",
		LangSpanish:            "❌ Error al agregar proveedor: %v",
	},
	MsgProviderRemoved: {
		LangEnglish:            "✅ Provider **%s** removed.",
		LangChinese:            "✅ Provider **%s** 已移除。",
		LangTraditionalChinese: "✅ Provider **%s** 已移除。",
		LangJapanese:           "✅ プロバイダ **%s** を削除しました。",
		LangSpanish:            "✅ Proveedor **%s** eliminado.",
	},
	MsgProviderRemoveFailed: {
		LangEnglish:            "❌ Failed to remove provider: %v",
		LangChinese:            "❌ 移除 Provider 失败: %v",
		LangTraditionalChinese: "❌ 移除 Provider 失敗: %v",
		LangJapanese:           "❌ プロバイダの削除に失敗しました: %v",
		LangSpanish:            "❌ Error al eliminar proveedor: %v",
	},
	MsgCardTitleProviderAdd: {
		LangEnglish: "Add Provider", LangChinese: "添加服务商", LangTraditionalChinese: "新增服務商",
		LangJapanese: "プロバイダーを追加", LangSpanish: "Añadir proveedor",
	},
	MsgProviderAddPickHint: {
		LangEnglish:            "Pick a provider below, or choose **Other** to enter manually.\nAfter selecting, send your API key to complete.",
		LangChinese:            "选择一个服务商，或选择 **自定义** 手动填写。\n选择后，请发送你的 API Key 来完成添加。",
		LangTraditionalChinese: "選擇一個服務商，或選擇 **自訂** 手動填寫。\n選擇後，請傳送你的 API Key 來完成新增。",
		LangJapanese:           "プロバイダーを選択するか、**その他** を選んで手動入力してください。\n選択後、API キーを送信して完了します。",
		LangSpanish:            "Elige un proveedor o selecciona **Otro** para ingresar manualmente.\nDespués de seleccionar, envía tu API Key para completar.",
	},
	MsgProviderAddOther: {
		LangEnglish: "Other (manual)", LangChinese: "自定义 (手动)", LangTraditionalChinese: "自訂 (手動)",
		LangJapanese: "その他 (手動)", LangSpanish: "Otro (manual)",
	},
	MsgProviderAddApiKeyPrompt: {
		LangEnglish:            "✅ Selected **%s**.\n\nPlease send your **API Key** for this provider.\nFormat: just the key, e.g. `sk-xxxxxxxx`",
		LangChinese:            "✅ 已选择 **%s**。\n\n请发送你的 **API Key**。\n格式：直接发送密钥即可，如 `sk-xxxxxxxx`",
		LangTraditionalChinese: "✅ 已選擇 **%s**。\n\n請傳送你的 **API Key**。\n格式：直接傳送金鑰即可，如 `sk-xxxxxxxx`",
		LangJapanese:           "✅ **%s** を選択しました。\n\n**API キー** を送信してください。\n形式: キーをそのまま送信（例: `sk-xxxxxxxx`）",
		LangSpanish:            "✅ Seleccionado **%s**.\n\nPor favor envía tu **API Key** para este proveedor.\nFormato: solo la clave, por ejemplo `sk-xxxxxxxx`",
	},
	MsgProviderAddInviteHint: {
		LangEnglish:            "🔑 Don't have a key? Register here: %s",
		LangChinese:            "🔑 还没有 Key？点击注册获取：%s",
		LangTraditionalChinese: "🔑 還沒有 Key？點擊註冊取得：%s",
		LangJapanese:           "🔑 キーをお持ちでない場合はこちらから登録: %s",
		LangSpanish:            "🔑 ¿No tienes una clave? Regístrate aquí: %s",
	},
	MsgProviderLinkGlobal: {
		LangEnglish: "Link existing provider", LangChinese: "关联已有服务商", LangTraditionalChinese: "關聯已有服務商",
		LangJapanese: "既存プロバイダーをリンク", LangSpanish: "Vincular proveedor existente",
	},
	MsgProviderLinked: {
		LangEnglish:            "✅ Provider **%s** linked to this project.",
		LangChinese:            "✅ 已关联服务商 **%s** 到当前项目。",
		LangTraditionalChinese: "✅ 已關聯服務商 **%s** 到目前專案。",
		LangJapanese:           "✅ プロバイダー **%s** をこのプロジェクトにリンクしました。",
		LangSpanish:            "✅ Proveedor **%s** vinculado a este proyecto.",
	},
	MsgVoiceNotEnabled: {
		LangEnglish:            "🎙 Voice messages are not enabled. Please configure `[speech]` in config.toml.",
		LangChinese:            "🎙 语音消息未启用，请在 config.toml 中配置 `[speech]` 部分。",
		LangTraditionalChinese: "🎙 語音訊息未啟用，請在 config.toml 中配置 `[speech]` 部分。",
		LangJapanese:           "🎙 音声メッセージは有効になっていません。config.toml で `[speech]` を設定してください。",
		LangSpanish:            "🎙 Los mensajes de voz no están habilitados. Configure `[speech]` en config.toml.",
	},
	MsgVoiceUsingPlatformRecognition: {
		LangEnglish:            "⚠️ Voice transcription not configured, using %s built-in recognition",
		LangChinese:            "⚠️ 未配置语音转录，使用 %s 内置语音识别",
		LangTraditionalChinese: "⚠️ 未配置語音轉錄，使用 %s 內置語音識別",
		LangJapanese:           "⚠️ 音声転写が設定されていないため、%s の組み込み認識を使用",
		LangSpanish:            "⚠️ Transcripción de voz no configurada, usando reconocimiento integrado de %s",
	},
	MsgVoiceNoFFmpeg: {
		LangEnglish:            "🎙 Voice message requires `ffmpeg` for format conversion. Please install ffmpeg.",
		LangChinese:            "🎙 语音消息需要 `ffmpeg` 进行格式转换，请安装 ffmpeg。",
		LangTraditionalChinese: "🎙 語音訊息需要 `ffmpeg` 進行格式轉換，請安裝 ffmpeg。",
		LangJapanese:           "🎙 音声メッセージのフォーマット変換に `ffmpeg` が必要です。ffmpeg をインストールしてください。",
		LangSpanish:            "🎙 Los mensajes de voz requieren `ffmpeg` para la conversión de formato. Instale ffmpeg.",
	},
	MsgVoiceTranscribing: {
		LangEnglish:            "🎙 Transcribing voice message...",
		LangChinese:            "🎙 正在转录语音消息...",
		LangTraditionalChinese: "🎙 正在轉錄語音訊息...",
		LangJapanese:           "🎙 音声メッセージを文字起こし中...",
		LangSpanish:            "🎙 Transcribiendo mensaje de voz...",
	},
	MsgVoiceTranscribed: {
		LangEnglish:            "🎙 [Voice] %s",
		LangChinese:            "🎙 [语音] %s",
		LangTraditionalChinese: "🎙 [語音] %s",
		LangJapanese:           "🎙 [音声] %s",
		LangSpanish:            "🎙 [Voz] %s",
	},
	MsgVoiceTranscribeFailed: {
		LangEnglish:            "🎙 Voice transcription failed: %v",
		LangChinese:            "🎙 语音转文字失败: %v",
		LangTraditionalChinese: "🎙 語音轉文字失敗: %v",
		LangJapanese:           "🎙 音声の文字起こしに失敗しました: %v",
		LangSpanish:            "🎙 Error en la transcripción de voz: %v",
	},
	MsgVoiceEmpty: {
		LangEnglish:            "🎙 Voice message was empty or could not be recognized.",
		LangChinese:            "🎙 语音消息为空或无法识别。",
		LangTraditionalChinese: "🎙 語音訊息為空或無法識別。",
		LangJapanese:           "🎙 音声メッセージが空か、認識できませんでした。",
		LangSpanish:            "🎙 El mensaje de voz estaba vacío o no se pudo reconocer.",
	},
	MsgTTSNotEnabled: {
		LangEnglish:            "TTS is not enabled. Please configure `[tts]` in config.toml.",
		LangChinese:            "TTS 未启用，请在 config.toml 中配置 `[tts]` 部分。",
		LangTraditionalChinese: "TTS 未啟用，請在 config.toml 中配置 `[tts]` 部分。",
		LangJapanese:           "TTS は有効になっていません。config.toml で `[tts]` を設定してください。",
		LangSpanish:            "TTS no está habilitado. Configure `[tts]` en config.toml.",
	},
	MsgTTSStatus: {
		LangEnglish:            "TTS status: enabled=true, mode=%s, provider=%s",
		LangChinese:            "TTS 状态：enabled=true，mode=%s，provider=%s",
		LangTraditionalChinese: "TTS 狀態：enabled=true，mode=%s，provider=%s",
		LangJapanese:           "TTS 状態: enabled=true, mode=%s, provider=%s",
		LangSpanish:            "Estado TTS: enabled=true, mode=%s, provider=%s",
	},
	MsgTTSSwitched: {
		LangEnglish:            "TTS mode switched to: %s",
		LangChinese:            "TTS 已切换为 %s 模式",
		LangTraditionalChinese: "TTS 已切換為 %s 模式",
		LangJapanese:           "TTS モードを %s に切り替えました",
		LangSpanish:            "Modo TTS cambiado a: %s",
	},
	MsgTTSUsage: {
		LangEnglish:            "Usage: /tts [always|voice_only]",
		LangChinese:            "用法：/tts [always|voice_only]",
		LangTraditionalChinese: "用法：/tts [always|voice_only]",
		LangJapanese:           "使い方: /tts [always|voice_only]",
		LangSpanish:            "Uso: /tts [always|voice_only]",
	},
	MsgHeartbeatNotAvailable: {
		LangEnglish:            "Heartbeat is not configured for this project.",
		LangChinese:            "当前项目未配置心跳。",
		LangTraditionalChinese: "當前項目未配置心跳。",
		LangJapanese:           "このプロジェクトにはハートビートが設定されていません。",
		LangSpanish:            "El heartbeat no está configurado para este proyecto.",
	},
	MsgHeartbeatStatus: {
		LangEnglish: "💓 Heartbeat Status\n\n" +
			"State: %s\n" +
			"Interval: %d min\n" +
			"Only when idle: %s\n" +
			"Silent: %s\n" +
			"Runs: %d\n" +
			"Errors: %d\n" +
			"Skipped (busy): %d\n" +
			"%s",
		LangChinese: "💓 心跳状态\n\n" +
			"状态: %s\n" +
			"间隔: %d 分钟\n" +
			"仅空闲时: %s\n" +
			"静默: %s\n" +
			"执行次数: %d\n" +
			"失败次数: %d\n" +
			"跳过 (忙碌): %d\n" +
			"%s",
		LangTraditionalChinese: "💓 心跳狀態\n\n" +
			"狀態: %s\n" +
			"間隔: %d 分鐘\n" +
			"僅空閒時: %s\n" +
			"靜默: %s\n" +
			"執行次數: %d\n" +
			"失敗次數: %d\n" +
			"跳過 (忙碌): %d\n" +
			"%s",
		LangJapanese: "💓 ハートビート状態\n\n" +
			"状態: %s\n" +
			"間隔: %d 分\n" +
			"アイドル時のみ: %s\n" +
			"サイレント: %s\n" +
			"実行回数: %d\n" +
			"エラー: %d\n" +
			"スキップ (ビジー): %d\n" +
			"%s",
		LangSpanish: "💓 Estado del Heartbeat\n\n" +
			"Estado: %s\n" +
			"Intervalo: %d min\n" +
			"Solo cuando inactivo: %s\n" +
			"Silencioso: %s\n" +
			"Ejecuciones: %d\n" +
			"Errores: %d\n" +
			"Omitidos (ocupado): %d\n" +
			"%s",
	},
	MsgHeartbeatPaused: {
		LangEnglish:            "💓 Heartbeat paused.",
		LangChinese:            "💓 心跳已暂停。",
		LangTraditionalChinese: "💓 心跳已暫停。",
		LangJapanese:           "💓 ハートビートを一時停止しました。",
		LangSpanish:            "💓 Heartbeat pausado.",
	},
	MsgHeartbeatResumed: {
		LangEnglish:            "💓 Heartbeat resumed.",
		LangChinese:            "💓 心跳已恢复。",
		LangTraditionalChinese: "💓 心跳已恢復。",
		LangJapanese:           "💓 ハートビートを再開しました。",
		LangSpanish:            "💓 Heartbeat reanudado.",
	},
	MsgHeartbeatInterval: {
		LangEnglish:            "💓 Heartbeat interval changed to %d minutes.",
		LangChinese:            "💓 心跳间隔已调整为 %d 分钟。",
		LangTraditionalChinese: "💓 心跳間隔已調整為 %d 分鐘。",
		LangJapanese:           "💓 ハートビート間隔を %d 分に変更しました。",
		LangSpanish:            "💓 Intervalo del heartbeat cambiado a %d minutos.",
	},
	MsgHeartbeatTriggered: {
		LangEnglish:            "💓 Heartbeat triggered.",
		LangChinese:            "💓 心跳已触发。",
		LangTraditionalChinese: "💓 心跳已觸發。",
		LangJapanese:           "💓 ハートビートをトリガーしました。",
		LangSpanish:            "💓 Heartbeat activado.",
	},
	MsgHeartbeatUsage: {
		LangEnglish:            "Usage: /heartbeat [status|pause|resume|run|interval <mins>]",
		LangChinese:            "用法: /heartbeat [status|pause|resume|run|interval <分钟>]",
		LangTraditionalChinese: "用法: /heartbeat [status|pause|resume|run|interval <分鐘>]",
		LangJapanese:           "使い方: /heartbeat [status|pause|resume|run|interval <分>]",
		LangSpanish:            "Uso: /heartbeat [status|pause|resume|run|interval <minutos>]",
	},
	MsgHeartbeatInvalidMins: {
		LangEnglish:            "Invalid interval. Please provide a positive number of minutes.",
		LangChinese:            "无效的间隔。请输入正整数的分钟数。",
		LangTraditionalChinese: "無效的間隔。請輸入正整數的分鐘數。",
		LangJapanese:           "無効な間隔です。正の整数を分で指定してください。",
		LangSpanish:            "Intervalo inválido. Proporcione un número positivo de minutos.",
	},
	MsgCronNotAvailable: {
		LangEnglish:            "Cron scheduler is not available.",
		LangChinese:            "定时任务调度器未启用。",
		LangTraditionalChinese: "定時任務調度器未啟用。",
		LangJapanese:           "スケジューラは利用できません。",
		LangSpanish:            "El programador de tareas no está disponible.",
	},
	MsgCronUsage: {
		LangEnglish:            "Usage:\n/cron add <min> <hour> <day> <month> <weekday> <prompt>\n/cron list\n/cron exec <id>\n/cron del <id>\n/cron enable <id> · /cron disable <id>\n/cron mute <id> · /cron unmute <id>\n/cron setup — write cc-connect instructions to agent memory file",
		LangChinese:            "用法：\n/cron add <分> <时> <日> <月> <周> <任务描述>\n/cron list\n/cron exec <id> 立即执行\n/cron del <id>\n/cron enable <id> · /cron disable <id>\n/cron mute <id> · /cron unmute <id> 静音/取消静音\n/cron setup — 将 cc-connect 指令写入 agent 记忆文件",
		LangTraditionalChinese: "用法：\n/cron add <分> <時> <日> <月> <週> <任務描述>\n/cron list\n/cron exec <id> 立即執行\n/cron del <id>\n/cron enable <id> · /cron disable <id>\n/cron mute <id> · /cron unmute <id> 靜音/取消靜音\n/cron setup — 將 cc-connect 指令寫入 agent 記憶檔案",
		LangJapanese:           "使い方:\n/cron add <分> <時> <日> <月> <曜日> <タスク内容>\n/cron list\n/cron exec <id> 今すぐ実行\n/cron del <id>\n/cron enable <id> · /cron disable <id>\n/cron mute <id> · /cron unmute <id> ミュート/解除\n/cron setup — cc-connect の指示をエージェントのメモリファイルに書き込む",
		LangSpanish:            "Uso:\n/cron add <min> <hora> <día> <mes> <día_semana> <tarea>\n/cron list\n/cron exec <id>\n/cron del <id>\n/cron enable <id> · /cron disable <id>\n/cron mute <id> · /cron unmute <id>\n/cron setup — escribir las instrucciones de cc-connect en el archivo de memoria del agente",
	},
	MsgCronAddUsage: {
		LangEnglish:            "Usage: /cron add <min> <hour> <day> <month> <weekday> <prompt>\nExample: /cron add 0 6 * * * Collect GitHub trending data and send me a summary",
		LangChinese:            "用法：/cron add <分> <时> <日> <月> <周> <任务描述>\n示例：/cron add 0 6 * * * 收集 GitHub Trending 数据整理成简报发给我",
		LangTraditionalChinese: "用法：/cron add <分> <時> <日> <月> <週> <任務描述>\n範例：/cron add 0 6 * * * 收集 GitHub Trending 資料整理成簡報發給我",
		LangJapanese:           "使い方: /cron add <分> <時> <日> <月> <曜日> <タスク内容>\n例: /cron add 0 6 * * * GitHub Trending を収集してまとめを送って",
		LangSpanish:            "Uso: /cron add <min> <hora> <día> <mes> <día_semana> <tarea>\nEjemplo: /cron add 0 6 * * * Recopilar datos de GitHub Trending y enviarme un resumen",
	},
	MsgCronAdded: {
		LangEnglish:            "✅ Cron job created\nID: `%s`\nSchedule: `%s`\nPrompt: %s",
		LangChinese:            "✅ 定时任务已创建\nID: `%s`\n调度: `%s`\n内容: %s",
		LangTraditionalChinese: "✅ 定時任務已建立\nID: `%s`\n調度: `%s`\n內容: %s",
		LangJapanese:           "✅ スケジュールタスクを作成しました\nID: `%s`\nスケジュール: `%s`\n内容: %s",
		LangSpanish:            "✅ Tarea programada creada\nID: `%s`\nProgramación: `%s`\nContenido: %s",
	},
	MsgCronAddedExec: {
		LangEnglish:            "✅ Shell cron job created\nID: `%s`\nSchedule: `%s`\nCommand: `%s`",
		LangChinese:            "✅ Shell 定时任务已创建\nID: `%s`\n调度: `%s`\n命令: `%s`",
		LangTraditionalChinese: "✅ Shell 定時任務已建立\nID: `%s`\n調度: `%s`\n命令: `%s`",
		LangJapanese:           "✅ Shell スケジュールタスクを作成しました\nID: `%s`\nスケジュール: `%s`\nコマンド: `%s`",
		LangSpanish:            "✅ Tarea shell programada creada\nID: `%s`\nProgramación: `%s`\nComando: `%s`",
	},
	MsgCronAddExecUsage: {
		LangEnglish:            "Usage: /cron addexec <min> <hour> <day> <month> <weekday> <shell command>\nExample: /cron addexec 0 6 * * * df -h",
		LangChinese:            "用法：/cron addexec <分> <时> <日> <月> <周> <shell 命令>\n示例：/cron addexec 0 6 * * * df -h",
		LangTraditionalChinese: "用法：/cron addexec <分> <時> <日> <月> <週> <shell 命令>\n範例：/cron addexec 0 6 * * * df -h",
		LangJapanese:           "使い方: /cron addexec <分> <時> <日> <月> <曜日> <シェルコマンド>\n例: /cron addexec 0 6 * * * df -h",
		LangSpanish:            "Uso: /cron addexec <min> <hora> <día> <mes> <día_semana> <comando shell>\nEjemplo: /cron addexec 0 6 * * * df -h",
	},
	MsgCronEmpty: {
		LangEnglish:            "No recurring tasks.\n(For one-shot reminders/delays, use /timer)",
		LangChinese:            "暂无周期任务。\n（一次性提醒/延迟任务请用 /timer 查看）",
		LangTraditionalChinese: "暫無週期任務。\n（一次性提醒/延遲任務請用 /timer 查看）",
		LangJapanese:           "繰り返しタスクはありません。\n（ワンショットのリマインダーは /timer をご利用ください）",
		LangSpanish:            "No hay tareas recurrentes.\n(Para recordatorios únicos use /timer)",
	},
	MsgCronListTitle: {
		LangEnglish:            "⏰ Scheduled Tasks (%d)",
		LangChinese:            "⏰ 定时任务 (%d)",
		LangTraditionalChinese: "⏰ 定時任務 (%d)",
		LangJapanese:           "⏰ スケジュールタスク (%d)",
		LangSpanish:            "⏰ Tareas programadas (%d)",
	},
	MsgCronListFooter: {
		LangEnglish:            "`/cron exec <id>` trigger now · `/cron del <id>` remove · `/cron enable/disable <id>` toggle · `/cron mute/unmute <id>` mute",
		LangChinese:            "`/cron exec <id>` 立即触发 · `/cron del <id>` 删除 · `/cron enable/disable <id>` 启停 · `/cron mute/unmute <id>` 静音",
		LangTraditionalChinese: "`/cron exec <id>` 立即觸發 · `/cron del <id>` 刪除 · `/cron enable/disable <id>` 啟停 · `/cron mute/unmute <id>` 靜音",
		LangJapanese:           "`/cron exec <id>` 今すぐ実行 · `/cron del <id>` 削除 · `/cron enable/disable <id>` 切替 · `/cron mute/unmute <id>` ミュート",
		LangSpanish:            "`/cron exec <id>` ejecutar ahora · `/cron del <id>` eliminar · `/cron enable/disable <id>` activar/desactivar · `/cron mute/unmute <id>` silenciar",
	},
	MsgCronExecUsage: {
		LangEnglish:            "Usage: /cron exec <id>",
		LangChinese:            "用法：/cron exec <id>",
		LangTraditionalChinese: "用法：/cron exec <id>",
		LangJapanese:           "使い方: /cron exec <id>",
		LangSpanish:            "Uso: /cron exec <id>",
	},
	MsgCronTriggered: {
		LangEnglish:            "▶️ Cron job `%s` triggered.",
		LangChinese:            "▶️ 定时任务 `%s` 已触发。",
		LangTraditionalChinese: "▶️ 定時任務 `%s` 已觸發。",
		LangJapanese:           "▶️ スケジュールタスク `%s` を実行しました。",
		LangSpanish:            "▶️ Tarea programada `%s` ejecutada.",
	},
	MsgCronProjectUnavailable: {
		LangEnglish:            "❌ This cron job cannot be triggered because its project is no longer available.",
		LangChinese:            "❌ 该定时任务关联的项目已不可用，无法触发。",
		LangTraditionalChinese: "❌ 該定時任務關聯的專案已不可用，無法觸發。",
		LangJapanese:           "❌ このスケジュールタスクは、関連するプロジェクトが利用できないため実行できません。",
		LangSpanish:            "❌ Esta tarea programada no puede ejecutarse porque su proyecto ya no está disponible.",
	},
	MsgCronDelUsage: {
		LangEnglish:            "Usage: /cron del <id>",
		LangChinese:            "用法：/cron del <id>",
		LangTraditionalChinese: "用法：/cron del <id>",
		LangJapanese:           "使い方: /cron del <id>",
		LangSpanish:            "Uso: /cron del <id>",
	},
	MsgCronDeleted: {
		LangEnglish:            "✅ Cron job `%s` deleted.",
		LangChinese:            "✅ 定时任务 `%s` 已删除。",
		LangTraditionalChinese: "✅ 定時任務 `%s` 已刪除。",
		LangJapanese:           "✅ スケジュールタスク `%s` を削除しました。",
		LangSpanish:            "✅ Tarea programada `%s` eliminada.",
	},
	MsgCronNotFound: {
		LangEnglish:            "❌ Cron job `%s` not found.",
		LangChinese:            "❌ 定时任务 `%s` 未找到。",
		LangTraditionalChinese: "❌ 定時任務 `%s` 未找到。",
		LangJapanese:           "❌ スケジュールタスク `%s` が見つかりません。",
		LangSpanish:            "❌ Tarea programada `%s` no encontrada.",
	},
	MsgCronEnabled: {
		LangEnglish:            "✅ Cron job `%s` enabled.",
		LangChinese:            "✅ 定时任务 `%s` 已启用。",
		LangTraditionalChinese: "✅ 定時任務 `%s` 已啟用。",
		LangJapanese:           "✅ スケジュールタスク `%s` を有効にしました。",
		LangSpanish:            "✅ Tarea programada `%s` habilitada.",
	},
	MsgCronDisabled: {
		LangEnglish:            "⏸ Cron job `%s` disabled.",
		LangChinese:            "⏸ 定时任务 `%s` 已暂停。",
		LangTraditionalChinese: "⏸ 定時任務 `%s` 已暫停。",
		LangJapanese:           "⏸ スケジュールタスク `%s` を無効にしました。",
		LangSpanish:            "⏸ Tarea programada `%s` deshabilitada.",
	},
	MsgCronMuted: {
		LangEnglish:            "🔇 Cron job `%s` muted (all messages suppressed).",
		LangChinese:            "🔇 定时任务 `%s` 已静音（所有消息均不发送）。",
		LangTraditionalChinese: "🔇 定時任務 `%s` 已靜音（所有訊息均不發送）。",
		LangJapanese:           "🔇 スケジュールタスク `%s` をミュートしました（全メッセージ抑制）。",
		LangSpanish:            "🔇 Tarea programada `%s` silenciada (todos los mensajes suprimidos).",
	},
	MsgCronUnmuted: {
		LangEnglish:            "🔔 Cron job `%s` unmuted.",
		LangChinese:            "🔔 定时任务 `%s` 已取消静音。",
		LangTraditionalChinese: "🔔 定時任務 `%s` 已取消靜音。",
		LangJapanese:           "🔔 スケジュールタスク `%s` のミュートを解除しました。",
		LangSpanish:            "🔔 Tarea programada `%s` reactivada.",
	},
	MsgCronCardHint: {
		LangEnglish:            "💡 `/cron add` · `/cron exec <id>` · `/cron del <id>` · `/cron enable/disable <id>` · `/cron mute/unmute <id>`",
		LangChinese:            "💡 `/cron add` 添加 · `/cron exec <id>` 触发 · `/cron del <id>` 删除 · `/cron enable/disable <id>` 启停 · `/cron mute/unmute <id>` 静音",
		LangTraditionalChinese: "💡 `/cron add` 新增 · `/cron exec <id>` 觸發 · `/cron del <id>` 刪除 · `/cron enable/disable <id>` 啟停 · `/cron mute/unmute <id>` 靜音",
		LangJapanese:           "💡 `/cron add` 追加 · `/cron exec <id>` 実行 · `/cron del <id>` 削除 · `/cron enable/disable <id>` 切替 · `/cron mute/unmute <id>` ミュート",
		LangSpanish:            "💡 `/cron add` · `/cron exec <id>` · `/cron del <id>` · `/cron enable/disable <id>` · `/cron mute/unmute <id>`",
	},
	MsgCronBtnEnable: {
		LangEnglish:            "Enable",
		LangChinese:            "启用",
		LangTraditionalChinese: "啟用",
		LangJapanese:           "有効",
		LangSpanish:            "Activar",
	},
	MsgCronBtnDisable: {
		LangEnglish:            "Disable",
		LangChinese:            "暂停",
		LangTraditionalChinese: "暫停",
		LangJapanese:           "無効",
		LangSpanish:            "Desactivar",
	},
	MsgCronBtnMute: {
		LangEnglish:            "Mute",
		LangChinese:            "静音",
		LangTraditionalChinese: "靜音",
		LangJapanese:           "ミュート",
		LangSpanish:            "Silenciar",
	},
	MsgCronBtnUnmute: {
		LangEnglish:            "Unmute",
		LangChinese:            "取消静音",
		LangTraditionalChinese: "取消靜音",
		LangJapanese:           "ミュート解除",
		LangSpanish:            "Reactivar",
	},
	MsgCronBtnDelete: {
		LangEnglish:            "Delete",
		LangChinese:            "删除",
		LangTraditionalChinese: "刪除",
		LangJapanese:           "削除",
		LangSpanish:            "Eliminar",
	},
	MsgCronNextShort: {
		LangEnglish:            "Next",
		LangChinese:            "下次",
		LangTraditionalChinese: "下次",
		LangJapanese:           "次回",
		LangSpanish:            "Prox",
	},
	MsgCronLastShort: {
		LangEnglish:            "Last",
		LangChinese:            "上次",
		LangTraditionalChinese: "上次",
		LangJapanese:           "前回",
		LangSpanish:            "Últ",
	},

	// ── Timer (one-shot) ──────────────────────────────────────

	MsgCardTitleTimer: {
		LangEnglish:            "One-Shot Timer",
		LangChinese:            "一次性定时器",
		LangTraditionalChinese: "一次性定時器",
		LangJapanese:           "ワンショットタイマー",
		LangSpanish:            "Temporizador único",
	},
	MsgTimerNotAvailable: {
		LangEnglish:            "Timer scheduler is not available.",
		LangChinese:            "定时器调度器未启用。",
		LangTraditionalChinese: "定時器調度器未啟用。",
		LangJapanese:           "タイマースケジューラは利用できません。",
		LangSpanish:            "El programador de temporizador no está disponible.",
	},
	MsgTimerUsage: {
		LangEnglish:            "Usage:\n/timer add <delay|time> <prompt>\n/timer addexec <delay|time> <command>\n/timer list\n/timer del <id>\n/timer mute <id> · /timer unmute <id>\n\nDelay: 30m, 2h, 1h30m. Or absolute time: 2026-05-16T09:00\nTime without timezone uses system local time.",
		LangChinese:            "用法：\n/timer add <延迟|时间> <任务描述>\n/timer addexec <延迟|时间> <命令>\n/timer list\n/timer del <id>\n/timer mute <id> · /timer unmute <id>\n\n延迟：30m、2h、1h30m。或绝对时间：2026-05-16T09:00\n不带时区的时间按系统本地时区解析。",
		LangTraditionalChinese: "用法：\n/timer add <延遲|時間> <任務描述>\n/timer addexec <延遲|時間> <命令>\n/timer list\n/timer del <id>\n/timer mute <id> · /timer unmute <id>\n\n延遲：30m、2h、1h30m。或絕對時間：2026-05-16T09:00\n不帶時區的時間按系統本地時區解析。",
		LangJapanese:           "使い方:\n/timer add <遅延|時刻> <タスク内容>\n/timer addexec <遅延|時刻> <コマンド>\n/timer list\n/timer del <id>\n/timer mute <id> · /timer unmute <id>\n\n遅延: 30m, 2h, 1h30m。または絶対時刻: 2026-05-16T09:00\nタイムゾーンなしの時刻はシステムのローカルタイムゾーンで解釈されます。",
		LangSpanish:            "Uso:\n/timer add <retraso|hora> <tarea>\n/timer addexec <retraso|hora> <comando>\n/timer list\n/timer del <id>\n/timer mute <id> · /timer unmute <id>\n\nRetraso: 30m, 2h, 1h30m. O hora absoluta: 2026-05-16T09:00\nHora sin zona horaria usa la hora local del sistema.",
	},
	MsgTimerAddUsage: {
		LangEnglish:            "Usage: /timer add <delay|time> <prompt>\nExamples:\n  /timer add 2h Check PR status\n  /timer add 2026-05-16T09:00 Morning standup reminder\nDelay: 30m, 2h, 1h30m. Time: ISO format (2026-05-16T09:00)\nTime without timezone uses system local time.",
		LangChinese:            "用法：/timer add <延迟|时间> <任务描述>\n示例：\n  /timer add 2h 检查PR状态\n  /timer add 2026-05-16T09:00 早会提醒\n延迟：30m、2h、1h30m。时间：ISO格式（2026-05-16T09:00）\n不带时区的时间按系统本地时区解析。",
		LangTraditionalChinese: "用法：/timer add <延遲|時間> <任務描述>\n範例：\n  /timer add 2h 檢查PR狀態\n  /timer add 2026-05-16T09:00 早會提醒\n延遲：30m、2h、1h30m。時間：ISO格式（2026-05-16T09:00）\n不帶時區的時間按系統本地時區解析。",
		LangJapanese:           "使い方: /timer add <遅延|時刻> <タスク内容>\n例:\n  /timer add 2h PRの状態を確認\n  /timer add 2026-05-16T09:00 朝会リマインダー\n遅延: 30m, 2h, 1h30m。時刻: ISO形式（2026-05-16T09:00）\nタイムゾーンなしの時刻はシステムのローカルタイムゾーンで解釈されます。",
		LangSpanish:            "Uso: /timer add <retraso|hora> <tarea>\nEjemplos:\n  /timer add 2h Verificar estado del PR\n  /timer add 2026-05-16T09:00 Recordatorio de reunión\nRetraso: 30m, 2h, 1h30m. Hora: formato ISO (2026-05-16T09:00)\nHora sin zona horaria usa la hora local del sistema.",
	},
	MsgTimerAdded: {
		LangEnglish:            "⏰ Reminder set (one-shot)\nID: `%s`\nFires in: %s\nPrompt: %s\n(use /timer to view, /cron for recurring tasks)",
		LangChinese:            "⏰ 提醒已设定（一次性）\nID: `%s`\n将在 %s 后触发\n内容: %s\n（用 /timer 查看，周期任务请用 /cron）",
		LangTraditionalChinese: "⏰ 提醒已設定（一次性）\nID: `%s`\n將在 %s 後觸發\n內容: %s\n（用 /timer 查看，週期任務請用 /cron）",
		LangJapanese:           "⏰ リマインダーを設定しました（ワンショット）\nID: `%s`\n%s 後に実行\n内容: %s\n（/timer で確認、繰り返しは /cron）",
		LangSpanish:            "⏰ Recordatorio creado (único)\nID: `%s`\nSe ejecuta en: %s\nContenido: %s\n(use /timer para verlos, /cron para tareas recurrentes)",
	},
	MsgTimerAddedExec: {
		LangEnglish:            "⏰ Shell reminder set (one-shot)\nID: `%s`\nFires in: %s\nCommand: `%s`\n(use /timer to view, /cron for recurring tasks)",
		LangChinese:            "⏰ Shell 提醒已设定（一次性）\nID: `%s`\n将在 %s 后触发\n命令: `%s`\n（用 /timer 查看，周期任务请用 /cron）",
		LangTraditionalChinese: "⏰ Shell 提醒已設定（一次性）\nID: `%s`\n將在 %s 後觸發\n命令: `%s`\n（用 /timer 查看，週期任務請用 /cron）",
		LangJapanese:           "⏰ Shell リマインダーを設定しました（ワンショット）\nID: `%s`\n%s 後に実行\nコマンド: `%s`\n（/timer で確認、繰り返しは /cron）",
		LangSpanish:            "⏰ Recordatorio shell creado (único)\nID: `%s`\nSe ejecuta en: %s\nComando: `%s`\n(use /timer para verlos, /cron para tareas recurrentes)",
	},
	MsgTimerAddExecUsage: {
		LangEnglish:            "Usage: /timer addexec <delay> <shell command>\nExample: /timer addexec 30m df -h",
		LangChinese:            "用法：/timer addexec <延迟> <shell 命令>\n示例：/timer addexec 30m df -h",
		LangTraditionalChinese: "用法：/timer addexec <延遲> <shell 命令>\n範例：/timer addexec 30m df -h",
		LangJapanese:           "使い方: /timer addexec <遅延> <シェルコマンド>\n例: /timer addexec 30m df -h",
		LangSpanish:            "Uso: /timer addexec <retraso> <comando shell>\nEjemplo: /timer addexec 30m df -h",
	},
	MsgTimerEmpty: {
		LangEnglish:            "No pending reminders.\n(For recurring tasks, use /cron)",
		LangChinese:            "暂无待执行的提醒。\n（周期任务请用 /cron 查看）",
		LangTraditionalChinese: "暫無待執行的提醒。\n（週期任務請用 /cron 查看）",
		LangJapanese:           "保留中のリマインダーはありません。\n（繰り返しタスクは /cron をご利用ください）",
		LangSpanish:            "No hay recordatorios pendientes.\n(Para tareas recurrentes use /cron)",
	},
	MsgTimerListTitle: {
		LangEnglish:            "⏰ Pending Timers (%d)",
		LangChinese:            "⏰ 待执行定时器 (%d)",
		LangTraditionalChinese: "⏰ 待執行定時器 (%d)",
		LangJapanese:           "⏰ 保留中のタイマー (%d)",
		LangSpanish:            "⏰ Temporizadores pendientes (%d)",
	},
	MsgTimerListFooter: {
		LangEnglish:            "`/timer del <id>` remove · `/timer mute/unmute <id>` mute",
		LangChinese:            "`/timer del <id>` 删除 · `/timer mute/unmute <id>` 静音",
		LangTraditionalChinese: "`/timer del <id>` 刪除 · `/timer mute/unmute <id>` 靜音",
		LangJapanese:           "`/timer del <id>` 削除 · `/timer mute/unmute <id>` ミュート",
		LangSpanish:            "`/timer del <id>` eliminar · `/timer mute/unmute <id>` silenciar",
	},
	MsgTimerDelUsage: {
		LangEnglish:            "Usage: /timer del <id>",
		LangChinese:            "用法：/timer del <id>",
		LangTraditionalChinese: "用法：/timer del <id>",
		LangJapanese:           "使い方: /timer del <id>",
		LangSpanish:            "Uso: /timer del <id>",
	},
	MsgTimerMuteUsage: {
		LangEnglish:            "Usage: /timer mute <id> · /timer unmute <id>",
		LangChinese:            "用法：/timer mute <id> · /timer unmute <id>",
		LangTraditionalChinese: "用法：/timer mute <id> · /timer unmute <id>",
		LangJapanese:           "使い方: /timer mute <id> · /timer unmute <id>",
		LangSpanish:            "Uso: /timer mute <id> · /timer unmute <id>",
	},
	MsgTimerDeleted: {
		LangEnglish:            "✅ Timer `%s` cancelled.",
		LangChinese:            "✅ 定时器 `%s` 已取消。",
		LangTraditionalChinese: "✅ 定時器 `%s` 已取消。",
		LangJapanese:           "✅ タイマー `%s` をキャンセルしました。",
		LangSpanish:            "✅ Temporizador `%s` cancelado.",
	},
	MsgTimerNotFound: {
		LangEnglish:            "❌ Timer `%s` not found.",
		LangChinese:            "❌ 定时器 `%s` 未找到。",
		LangTraditionalChinese: "❌ 定時器 `%s` 未找到。",
		LangJapanese:           "❌ タイマー `%s` が見つかりません。",
		LangSpanish:            "❌ Temporizador `%s` no encontrado.",
	},
	MsgTimerMuted: {
		LangEnglish:            "🔇 Timer `%s` muted.",
		LangChinese:            "🔇 定时器 `%s` 已静音。",
		LangTraditionalChinese: "🔇 定時器 `%s` 已靜音。",
		LangJapanese:           "🔇 タイマー `%s` をミュートしました。",
		LangSpanish:            "🔇 Temporizador `%s` silenciado.",
	},
	MsgTimerUnmuted: {
		LangEnglish:            "🔔 Timer `%s` unmuted.",
		LangChinese:            "🔔 定时器 `%s` 已取消静音。",
		LangTraditionalChinese: "🔔 定時器 `%s` 已取消靜音。",
		LangJapanese:           "🔔 タイマー `%s` のミュートを解除しました。",
		LangSpanish:            "🔔 Temporizador `%s` reactivado.",
	},
	MsgTimerCardHint: {
		LangEnglish:            "💡 `/timer add <delay> <prompt>` · `/timer del <id>` · `/timer mute/unmute <id>`",
		LangChinese:            "💡 `/timer add <延迟> <内容>` 添加 · `/timer del <id>` 删除 · `/timer mute/unmute <id>` 静音",
		LangTraditionalChinese: "💡 `/timer add <延遲> <內容>` 新增 · `/timer del <id>` 刪除 · `/timer mute/unmute <id>` 靜音",
		LangJapanese:           "💡 `/timer add <遅延> <内容>` 追加 · `/timer del <id>` 削除 · `/timer mute/unmute <id>` ミュート",
		LangSpanish:            "💡 `/timer add <retraso> <tarea>` · `/timer del <id>` · `/timer mute/unmute <id>`",
	},
	MsgTimerBtnMute: {
		LangEnglish:            "Mute",
		LangChinese:            "静音",
		LangTraditionalChinese: "靜音",
		LangJapanese:           "ミュート",
		LangSpanish:            "Silenciar",
	},
	MsgTimerBtnUnmute: {
		LangEnglish:            "Unmute",
		LangChinese:            "取消静音",
		LangTraditionalChinese: "取消靜音",
		LangJapanese:           "ミュート解除",
		LangSpanish:            "Reactivar",
	},
	MsgTimerBtnDelete: {
		LangEnglish:            "Cancel Timer",
		LangChinese:            "取消定时器",
		LangTraditionalChinese: "取消定時器",
		LangJapanese:           "タイマーをキャンセル",
		LangSpanish:            "Cancelar temporizador",
	},
	MsgTimerIDLabel: {
		LangEnglish: "ID: %s\n", LangChinese: "ID：%s\n", LangTraditionalChinese: "ID：%s\n",
		LangJapanese: "ID: %s\n", LangSpanish: "ID: %s\n",
	},
	MsgTimerScheduledLabel: {
		LangEnglish:            "Scheduled: %s (%s remaining)\n",
		LangChinese:            "计划: %s（剩余 %s）\n",
		LangTraditionalChinese: "計劃: %s（剩餘 %s）\n",
		LangJapanese:           "予定: %s（残り %s）\n",
		LangSpanish:            "Programado: %s (%s restante)\n",
	},
	MsgTimerFailedSuffix: {
		LangEnglish: " (failed: %s)", LangChinese: "（失败：%s）", LangTraditionalChinese: "（失敗：%s）",
		LangJapanese: "（失敗: %s）", LangSpanish: " (falló: %s)",
	},

	MsgStatusTitle: {
		LangEnglish: "cc-connect Status\n\n" +
			"Project: %s\n" +
			"Agent: %s\n" +
			"Work Dir: %s\n" +
			"Platforms: %s\n" +
			"Uptime: %s\n" +
			"Language: %s\n" +
			"%s" + "%s" + "%s" + "%s" + "%s" + "%s",
		LangChinese: "cc-connect 状态\n\n" +
			"项目: %s\n" +
			"Agent: %s\n" +
			"工作目录: %s\n" +
			"平台: %s\n" +
			"运行时间: %s\n" +
			"语言: %s\n" +
			"%s" + "%s" + "%s" + "%s" + "%s" + "%s",
		LangTraditionalChinese: "cc-connect 狀態\n\n" +
			"項目: %s\n" +
			"Agent: %s\n" +
			"工作目錄: %s\n" +
			"平台: %s\n" +
			"運行時間: %s\n" +
			"語言: %s\n" +
			"%s" + "%s" + "%s" + "%s" + "%s" + "%s",
		LangJapanese: "cc-connect ステータス\n\n" +
			"プロジェクト: %s\n" +
			"エージェント: %s\n" +
			"作業ディレクトリ: %s\n" +
			"プラットフォーム: %s\n" +
			"稼働時間: %s\n" +
			"言語: %s\n" +
			"%s" + "%s" + "%s" + "%s" + "%s" + "%s",
		LangSpanish: "Estado de cc-connect\n\n" +
			"Proyecto: %s\n" +
			"Agente: %s\n" +
			"Directorio: %s\n" +
			"Plataformas: %s\n" +
			"Tiempo activo: %s\n" +
			"Idioma: %s\n" +
			"%s" + "%s" + "%s" + "%s" + "%s" + "%s",
	},
	MsgReplyFooterRemaining: {
		LangEnglish:            "%d%% left",
		LangChinese:            "剩余 %d%%",
		LangTraditionalChinese: "剩餘 %d%%",
		LangJapanese:           "残り %d%%",
		LangSpanish:            "%d%% restante",
	},
	MsgModelCurrent: {
		LangEnglish:            "Current model: %s",
		LangChinese:            "当前模型: %s",
		LangTraditionalChinese: "當前模型: %s",
		LangJapanese:           "現在のモデル: %s",
		LangSpanish:            "Modelo actual: %s",
	},
	MsgModelChanged: {
		LangEnglish:            "Model switched to `%s`. This session and all future sessions will use it.",
		LangChinese:            "模型已切换为 `%s`，当前会话与后续会话均使用此模型。",
		LangTraditionalChinese: "模型已切換為 `%s`，當前會話與後續會話均使用此模型。",
		LangJapanese:           "モデルを `%s` に切り替えました。このセッションと今後のセッションで使用されます。",
		LangSpanish:            "Modelo cambiado a `%s`. Esta sesión y las futuras usarán este modelo.",
	},
	MsgModelChangeFailed: {
		LangEnglish:            "❌ Failed to change model: %v",
		LangChinese:            "❌ 切换模型失败: %v",
		LangTraditionalChinese: "❌ 切換模型失敗: %v",
		LangJapanese:           "❌ モデルの切り替えに失敗しました: %v",
		LangSpanish:            "❌ Error al cambiar el modelo: %v",
	},
	MsgModelCardSwitching: {
		LangEnglish:            "Switching model to `%s`...",
		LangChinese:            "正在切换模型为 `%s`...",
		LangTraditionalChinese: "正在切換模型為 `%s`...",
		LangJapanese:           "モデルを `%s` に切り替えています...",
		LangSpanish:            "Cambiando el modelo a `%s`...",
	},
	MsgModelCardSwitched: {
		LangEnglish:            "Model switched to `%s`.",
		LangChinese:            "模型已切换为 `%s`。",
		LangTraditionalChinese: "模型已切換為 `%s`。",
		LangJapanese:           "モデルを `%s` に切り替えました。",
		LangSpanish:            "Modelo cambiado a `%s`.",
	},
	MsgModelCardSwitchFailed: {
		LangEnglish:            "Failed to switch model: %v",
		LangChinese:            "切换模型失败: %v",
		LangTraditionalChinese: "切換模型失敗: %v",
		LangJapanese:           "モデルの切り替えに失敗しました: %v",
		LangSpanish:            "Error al cambiar el modelo: %v",
	},
	MsgModelNotSupported: {
		LangEnglish:            "This agent does not support model switching.",
		LangChinese:            "当前 Agent 不支持模型切换。",
		LangTraditionalChinese: "當前 Agent 不支援模型切換。",
		LangJapanese:           "このエージェントはモデルの切り替えをサポートしていません。",
		LangSpanish:            "Este agente no soporta el cambio de modelo.",
	},
	MsgReasoningCurrent: {
		LangEnglish:            "Current reasoning effort: %s",
		LangChinese:            "当前推理强度: %s",
		LangTraditionalChinese: "當前推理強度: %s",
		LangJapanese:           "現在の推論強度: %s",
		LangSpanish:            "Esfuerzo de razonamiento actual: %s",
	},
	MsgReasoningChanged: {
		LangEnglish:            "Reasoning effort switched to `%s`. New sessions will use this setting.",
		LangChinese:            "推理强度已切换为 `%s`，新会话将使用此设置。",
		LangTraditionalChinese: "推理強度已切換為 `%s`，新會話將使用此設定。",
		LangJapanese:           "推論強度を `%s` に切り替えました。新しいセッションで使用されます。",
		LangSpanish:            "Esfuerzo de razonamiento cambiado a `%s`. Las nuevas sesiones usarán esta configuración.",
	},
	MsgAgentConfigFailed: {
		LangEnglish:            "Could not read or update agent settings: %s",
		LangChinese:            "读取或修改 Agent 配置失败：%s",
		LangTraditionalChinese: "讀取或修改 Agent 設定失敗：%s",
		LangJapanese:           "エージェント設定の取得または変更に失敗しました: %s",
		LangSpanish:            "No se pudo leer o cambiar la configuración del agente: %s",
	},
	MsgReasoningNotSupported: {
		LangEnglish:            "This agent does not support reasoning effort switching.",
		LangChinese:            "当前 Agent 不支持推理强度切换。",
		LangTraditionalChinese: "當前 Agent 不支援推理強度切換。",
		LangJapanese:           "このエージェントは推論強度の切り替えをサポートしていません。",
		LangSpanish:            "Este agente no soporta el cambio de esfuerzo de razonamiento.",
	},
	MsgMemoryNotSupported: {
		LangEnglish:            "This agent does not support memory files.",
		LangChinese:            "当前 Agent 不支持记忆文件。",
		LangTraditionalChinese: "當前 Agent 不支援記憶檔案。",
		LangJapanese:           "このエージェントはメモリファイルをサポートしていません。",
		LangSpanish:            "Este agente no soporta archivos de memoria.",
	},
	MsgMemoryShowProject: {
		LangEnglish:            "📝 **Project Memory** (`%s`)\n\n%s",
		LangChinese:            "📝 **项目记忆** (`%s`)\n\n%s",
		LangTraditionalChinese: "📝 **項目記憶** (`%s`)\n\n%s",
		LangJapanese:           "📝 **プロジェクトメモリ** (`%s`)\n\n%s",
		LangSpanish:            "📝 **Memoria del proyecto** (`%s`)\n\n%s",
	},
	MsgMemoryShowGlobal: {
		LangEnglish:            "📝 **Global Memory** (`%s`)\n\n%s",
		LangChinese:            "📝 **全局记忆** (`%s`)\n\n%s",
		LangTraditionalChinese: "📝 **全域記憶** (`%s`)\n\n%s",
		LangJapanese:           "📝 **グローバルメモリ** (`%s`)\n\n%s",
		LangSpanish:            "📝 **Memoria global** (`%s`)\n\n%s",
	},
	MsgMemoryEmpty: {
		LangEnglish:            "📝 `%s`\n\n(empty — no content yet)",
		LangChinese:            "📝 `%s`\n\n（空 — 尚无内容）",
		LangTraditionalChinese: "📝 `%s`\n\n（空 — 尚無內容）",
		LangJapanese:           "📝 `%s`\n\n（空 — まだ内容がありません）",
		LangSpanish:            "📝 `%s`\n\n(vacío — aún sin contenido)",
	},
	MsgMemoryAdded: {
		LangEnglish:            "✅ Added to `%s`",
		LangChinese:            "✅ 已追加到 `%s`",
		LangTraditionalChinese: "✅ 已追加到 `%s`",
		LangJapanese:           "✅ `%s` に追加しました",
		LangSpanish:            "✅ Agregado a `%s`",
	},
	MsgMemoryAddFailed: {
		LangEnglish:            "❌ Failed to write memory file: %v",
		LangChinese:            "❌ 写入记忆文件失败: %v",
		LangTraditionalChinese: "❌ 寫入記憶檔案失敗: %v",
		LangJapanese:           "❌ メモリファイルの書き込みに失敗しました: %v",
		LangSpanish:            "❌ Error al escribir archivo de memoria: %v",
	},
	MsgUsageNotSupported: {
		LangEnglish:            "Current agent does not support `/usage`.",
		LangChinese:            "当前 Agent 不支持 `/usage`。",
		LangTraditionalChinese: "目前 Agent 不支援 `/usage`。",
		LangJapanese:           "現在のエージェントは `/usage` をサポートしていません。",
		LangSpanish:            "El agente actual no admite `/usage`.",
	},
	MsgUsageFetchFailed: {
		LangEnglish:            "Failed to fetch usage: %v",
		LangChinese:            "获取 usage 失败：%v",
		LangTraditionalChinese: "取得 usage 失敗：%v",
		LangJapanese:           "usage の取得に失敗しました: %v",
		LangSpanish:            "No se pudo obtener usage: %v",
	},
	MsgMemoryAddUsage: {
		LangEnglish: "Usage:\n" +
			"`/memory` — show project memory\n" +
			"`/memory add <text>` — add to project memory\n" +
			"`/memory global` — show global memory\n" +
			"`/memory global add <text>` — add to global memory",
		LangChinese: "用法：\n" +
			"`/memory` — 查看项目记忆\n" +
			"`/memory add <文本>` — 追加到项目记忆\n" +
			"`/memory global` — 查看全局记忆\n" +
			"`/memory global add <文本>` — 追加到全局记忆",
		LangTraditionalChinese: "用法：\n" +
			"`/memory` — 查看項目記憶\n" +
			"`/memory add <文字>` — 追加到項目記憶\n" +
			"`/memory global` — 查看全域記憶\n" +
			"`/memory global add <文字>` — 追加到全域記憶",
		LangJapanese: "使い方:\n" +
			"`/memory` — プロジェクトメモリを表示\n" +
			"`/memory add <テキスト>` — プロジェクトメモリに追加\n" +
			"`/memory global` — グローバルメモリを表示\n" +
			"`/memory global add <テキスト>` — グローバルメモリに追加",
		LangSpanish: "Uso:\n" +
			"`/memory` — ver memoria del proyecto\n" +
			"`/memory add <texto>` — agregar a memoria del proyecto\n" +
			"`/memory global` — ver memoria global\n" +
			"`/memory global add <texto>` — agregar a memoria global",
	},
	MsgCompressNotSupported: {
		LangEnglish:            "This agent does not support context compression.",
		LangChinese:            "当前 Agent 不支持上下文压缩。可以使用 `/new` 开始新会话。",
		LangTraditionalChinese: "當前 Agent 不支援上下文壓縮。可以使用 `/new` 開始新會話。",
		LangJapanese:           "このエージェントはコンテキスト圧縮をサポートしていません。`/new` で新しいセッションを開始できます。",
		LangSpanish:            "Este agente no soporta la compresión de contexto. Puede usar `/new` para iniciar una nueva sesión.",
	},
	MsgCompressing: {
		LangEnglish:            "🗜 Compressing context...",
		LangChinese:            "🗜 正在压缩上下文...",
		LangTraditionalChinese: "🗜 正在壓縮上下文...",
		LangJapanese:           "🗜 コンテキストを圧縮中...",
		LangSpanish:            "🗜 Comprimiendo contexto...",
	},
	MsgCompressNoSession: {
		LangEnglish:            "No active session to compress. Send a message first.",
		LangChinese:            "没有活跃的会话可以压缩。请先发送一条消息。",
		LangTraditionalChinese: "沒有活躍的會話可以壓縮。請先發送一條訊息。",
		LangJapanese:           "圧縮するアクティブなセッションがありません。まずメッセージを送信してください。",
		LangSpanish:            "No hay sesión activa para comprimir. Envíe un mensaje primero.",
	},
	MsgCompressDone: {
		LangEnglish:            "✅ Context compressed.",
		LangChinese:            "✅ 上下文压缩完成。",
		LangTraditionalChinese: "✅ 上下文壓縮完成。",
		LangJapanese:           "✅ コンテキスト圧縮完了。",
		LangSpanish:            "✅ Contexto comprimido.",
	},

	// Inline strings for engine.go commands
	MsgStatusMode: {
		LangEnglish:            "Mode: %s\n",
		LangChinese:            "权限模式: %s\n",
		LangTraditionalChinese: "權限模式: %s\n",
		LangJapanese:           "権限モード: %s\n",
		LangSpanish:            "Modo: %s\n",
	},
	MsgStatusSession: {
		LangEnglish:            "Session: %s (messages: %d)\n",
		LangChinese:            "当前会话: %s (消息: %d)\n",
		LangTraditionalChinese: "當前會話: %s (訊息: %d)\n",
		LangJapanese:           "セッション: %s (メッセージ: %d)\n",
		LangSpanish:            "Sesión: %s (mensajes: %d)\n",
	},
	MsgStatusCron: {
		LangEnglish:            "Cron jobs: %d (enabled: %d)\n",
		LangChinese:            "定时任务: %d (启用: %d)\n",
		LangTraditionalChinese: "定時任務: %d (啟用: %d)\n",
		LangJapanese:           "スケジュールタスク: %d (有効: %d)\n",
		LangSpanish:            "Tareas programadas: %d (habilitadas: %d)\n",
	},
	MsgStatusThinkingMessages: {
		LangEnglish:            "Thinking messages: %s\n",
		LangChinese:            "思考消息: %s\n",
		LangTraditionalChinese: "思考訊息: %s\n",
		LangJapanese:           "思考メッセージ: %s\n",
		LangSpanish:            "Mensajes de razonamiento: %s\n",
	},
	MsgStatusToolMessages: {
		LangEnglish:            "Tool progress: %s\n",
		LangChinese:            "工具进度: %s\n",
		LangTraditionalChinese: "工具進度: %s\n",
		LangJapanese:           "ツール進捗: %s\n",
		LangSpanish:            "Progreso de herramientas: %s\n",
	},
	MsgStatusSessionKey: {
		LangEnglish:            "Session Key: `%s`\n",
		LangChinese:            "会话 Key: `%s`\n",
		LangTraditionalChinese: "會話 Key: `%s`\n",
		LangJapanese:           "セッションキー: `%s`\n",
		LangSpanish:            "Clave de sesión: `%s`\n",
	},
	MsgStatusAgentSID: {
		LangEnglish:            "Agent SID: `%s`\n",
		LangChinese:            "Agent SID: `%s`\n",
		LangTraditionalChinese: "Agent SID: `%s`\n",
		LangJapanese:           "Agent SID: `%s`\n",
		LangSpanish:            "Agent SID: `%s`\n",
	},
	MsgStatusUserID: {
		LangEnglish:            "User ID: `%s`\n",
		LangChinese:            "User ID: `%s`\n",
		LangTraditionalChinese: "User ID: `%s`\n",
		LangJapanese:           "ユーザーID: `%s`\n",
		LangSpanish:            "ID de usuario: `%s`\n",
	},
	MsgEnabledShort: {
		LangEnglish:            "ON",
		LangChinese:            "开启",
		LangTraditionalChinese: "開啟",
		LangJapanese:           "ON",
		LangSpanish:            "Activado",
	},
	MsgDisabledShort: {
		LangEnglish:            "OFF",
		LangChinese:            "关闭",
		LangTraditionalChinese: "關閉",
		LangJapanese:           "OFF",
		LangSpanish:            "Desactivado",
	},
	MsgModelDefault: {
		LangEnglish:            "Current model: (not set, using agent default)\n",
		LangChinese:            "当前模型: (未设置，使用 Agent 默认值)\n",
		LangTraditionalChinese: "當前模型: (未設置，使用 Agent 預設值)\n",
		LangJapanese:           "現在のモデル: (未設定、エージェントのデフォルトを使用)\n",
		LangSpanish:            "Modelo actual: (no configurado, usando predeterminado del agente)\n",
	},
	MsgModelListTitle: {
		LangEnglish:            "Available models:\n",
		LangChinese:            "可用模型:\n",
		LangTraditionalChinese: "可用模型:\n",
		LangJapanese:           "利用可能なモデル:\n",
		LangSpanish:            "Modelos disponibles:\n",
	},
	MsgModelUsage: {
		LangEnglish:            "Usage: `/model switch <number>` or `/model switch <model_name>`",
		LangChinese:            "用法: `/model switch <序号>` 或 `/model switch <模型名>`",
		LangTraditionalChinese: "用法: `/model switch <序號>` 或 `/model switch <模型名>`",
		LangJapanese:           "使い方: `/model switch <番号>` または `/model switch <モデル名>`",
		LangSpanish:            "Uso: `/model switch <número>` o `/model switch <nombre_modelo>`",
	},
	MsgReasoningDefault: {
		LangEnglish:            "Current reasoning effort: (not set, using Codex default)\n",
		LangChinese:            "当前推理强度: (未设置，使用 Codex 默认值)\n",
		LangTraditionalChinese: "當前推理強度: (未設置，使用 Codex 預設值)\n",
		LangJapanese:           "現在の推論強度: (未設定、Codex のデフォルトを使用)\n",
		LangSpanish:            "Esfuerzo de razonamiento actual: (no configurado, usando el valor predeterminado de Codex)\n",
	},
	MsgReasoningListTitle: {
		LangEnglish:            "Available reasoning levels:\n",
		LangChinese:            "可用推理强度:\n",
		LangTraditionalChinese: "可用推理強度:\n",
		LangJapanese:           "利用可能な推論強度:\n",
		LangSpanish:            "Niveles de razonamiento disponibles:\n",
	},
	MsgReasoningUsage: {
		LangEnglish:            "Usage: `/reasoning <number>` or `/reasoning <%s>`",
		LangChinese:            "用法: `/reasoning <序号>` 或 `/reasoning <%s>`",
		LangTraditionalChinese: "用法: `/reasoning <序號>` 或 `/reasoning <%s>`",
		LangJapanese:           "使い方: `/reasoning <番号>` または `/reasoning <%s>`",
		LangSpanish:            "Uso: `/reasoning <número>` o `/reasoning <%s>`",
	},
	MsgModeUsage: {
		LangEnglish:            "\nUse `/mode <name>` to switch.\nAvailable: %s",
		LangChinese:            "\n使用 `/mode <名称>` 切换模式\n可用值: %s",
		LangTraditionalChinese: "\n使用 `/mode <名稱>` 切換模式\n可用值: %s",
		LangJapanese:           "\n`/mode <名前>` で切り替え\n選択肢: %s",
		LangSpanish:            "\nUse `/mode <nombre>` para cambiar.\nDisponibles: %s",
	},
	MsgLangSelectPlaceholder: {
		LangEnglish: "Select language", LangChinese: "选择语言", LangTraditionalChinese: "選擇語言",
		LangJapanese: "言語を選択", LangSpanish: "Seleccionar idioma",
	},
	MsgModelSelectPlaceholder: {
		LangEnglish: "Select model", LangChinese: "选择模型", LangTraditionalChinese: "選擇模型",
		LangJapanese: "モデルを選択", LangSpanish: "Seleccionar modelo",
	},
	MsgReasoningSelectPlaceholder: {
		LangEnglish: "Select reasoning level", LangChinese: "选择推理强度", LangTraditionalChinese: "選擇推理強度",
		LangJapanese: "推論強度を選択", LangSpanish: "Seleccionar nivel de razonamiento",
	},
	MsgModeSelectPlaceholder: {
		LangEnglish: "Select mode", LangChinese: "选择模式", LangTraditionalChinese: "選擇模式",
		LangJapanese: "モードを選択", LangSpanish: "Seleccionar modo",
	},
	MsgProviderSelectPlaceholder: {
		LangEnglish: "Select provider", LangChinese: "选择 Provider", LangTraditionalChinese: "選擇 Provider",
		LangJapanese: "プロバイダーを選択", LangSpanish: "Seleccionar proveedor",
	},
	MsgProviderClearOption: {
		LangEnglish: "Do not use provider", LangChinese: "不使用服务商", LangTraditionalChinese: "不使用服務商",
		LangJapanese: "プロバイダーを使用しない", LangSpanish: "No usar proveedor",
	},
	MsgCardBack: {
		LangEnglish: "← Back", LangChinese: "← 返回", LangTraditionalChinese: "← 返回",
		LangJapanese: "← 戻る", LangSpanish: "← Volver",
	},
	MsgCardPrev: {
		LangEnglish: "← Prev", LangChinese: "← 上一页", LangTraditionalChinese: "← 上一頁",
		LangJapanese: "← 前へ", LangSpanish: "← Anterior",
	},
	MsgCardNext: {
		LangEnglish: "Next →", LangChinese: "下一页 →", LangTraditionalChinese: "下一頁 →",
		LangJapanese: "次へ →", LangSpanish: "Siguiente →",
	},
	MsgCardTitleStatus: {
		LangEnglish: "cc-connect Status", LangChinese: "cc-connect 状态", LangTraditionalChinese: "cc-connect 狀態",
		LangJapanese: "cc-connect ステータス", LangSpanish: "Estado de cc-connect",
	},
	MsgCardTitleLanguage: {
		LangEnglish: "Language", LangChinese: "语言", LangTraditionalChinese: "語言",
		LangJapanese: "言語", LangSpanish: "Idioma",
	},
	MsgCardTitleModel: {
		LangEnglish: "Model", LangChinese: "模型", LangTraditionalChinese: "模型",
		LangJapanese: "モデル", LangSpanish: "Modelo",
	},
	MsgCardTitleReasoning: {
		LangEnglish: "Reasoning", LangChinese: "推理强度", LangTraditionalChinese: "推理強度",
		LangJapanese: "推論強度", LangSpanish: "Razonamiento",
	},
	MsgCardTitleMode: {
		LangEnglish: "Permission Mode", LangChinese: "权限模式", LangTraditionalChinese: "權限模式",
		LangJapanese: "権限モード", LangSpanish: "Modo de permisos",
	},
	MsgCardTitleSessions: {
		LangEnglish: "%s Sessions (%d)", LangChinese: "%s 会话列表 (%d)", LangTraditionalChinese: "%s 會話列表 (%d)",
		LangJapanese: "%s セッション (%d)", LangSpanish: "Sesiones de %s (%d)",
	},
	MsgCardTitleSessionsPaged: {
		LangEnglish: "%s Sessions (%d) — %d/%d", LangChinese: "%s 会话列表 (%d) · 第 %d/%d 页", LangTraditionalChinese: "%s 會話列表 (%d) · 第 %d/%d 頁",
		LangJapanese: "%s セッション (%d) · %d/%d ページ", LangSpanish: "Sesiones de %s (%d) · Página %d/%d",
	},
	MsgCardTitleCurrentSession: {
		LangEnglish: "Current Session", LangChinese: "当前会话", LangTraditionalChinese: "當前會話",
		LangJapanese: "現在のセッション", LangSpanish: "Sesión actual",
	},
	MsgCardTitleHistory: {
		LangEnglish: "History", LangChinese: "历史记录", LangTraditionalChinese: "歷史記錄",
		LangJapanese: "履歴", LangSpanish: "Historial",
	},
	MsgCardTitleHistoryLast: {
		LangEnglish: "History (last %d)", LangChinese: "历史记录（最近 %d 条）", LangTraditionalChinese: "歷史記錄（最近 %d 條）",
		LangJapanese: "履歴（直近 %d 件）", LangSpanish: "Historial (últimos %d)",
	},
	MsgCardTitleProvider: {
		LangEnglish: "Provider", LangChinese: "Provider", LangTraditionalChinese: "Provider",
		LangJapanese: "プロバイダー", LangSpanish: "Proveedor",
	},
	MsgCardTitleCron: {
		LangEnglish: "Cron", LangChinese: "定时任务", LangTraditionalChinese: "定時任務",
		LangJapanese: "スケジュールタスク", LangSpanish: "Tareas programadas",
	},
	MsgCardTitleHeartbeat: {
		LangEnglish: "Heartbeat", LangChinese: "心跳", LangTraditionalChinese: "心跳",
		LangJapanese: "ハートビート", LangSpanish: "Heartbeat",
	},
	MsgCardTitleCommands: {
		LangEnglish: "Commands", LangChinese: "命令", LangTraditionalChinese: "命令",
		LangJapanese: "コマンド", LangSpanish: "Comandos",
	},
	MsgCardTitleAlias: {
		LangEnglish: "Alias", LangChinese: "别名", LangTraditionalChinese: "別名",
		LangJapanese: "エイリアス", LangSpanish: "Alias",
	},
	MsgCardTitleConfig: {
		LangEnglish: "Config", LangChinese: "配置", LangTraditionalChinese: "配置",
		LangJapanese: "設定", LangSpanish: "Configuración",
	},
	MsgCardTitleSkills: {
		LangEnglish: "Skills", LangChinese: "Skills", LangTraditionalChinese: "Skills",
		LangJapanese: "スキル", LangSpanish: "Skills",
	},
	MsgCardTitleDoctor: {
		LangEnglish: "Doctor", LangChinese: "系统诊断", LangTraditionalChinese: "系統診斷",
		LangJapanese: "診断", LangSpanish: "Diagnóstico",
	},
	MsgCardTitleVersion: {
		LangEnglish: "Version", LangChinese: "版本", LangTraditionalChinese: "版本",
		LangJapanese: "バージョン", LangSpanish: "Versión",
	},
	MsgCardTitleUpgrade: {
		LangEnglish: "Upgrade", LangChinese: "升级", LangTraditionalChinese: "升級",
		LangJapanese: "アップグレード", LangSpanish: "Actualización",
	},
	MsgListItem: {
		LangEnglish:            "%s **%d.** %s · **%d** msgs · %s",
		LangChinese:            "%s **%d.** %s · **%d** 条消息 · %s",
		LangTraditionalChinese: "%s **%d.** %s · **%d** 則訊息 · %s",
		LangJapanese:           "%s **%d.** %s · **%d** 件のメッセージ · %s",
		LangSpanish:            "%s **%d.** %s · **%d** mensajes · %s",
	},
	MsgListEmptySummary: {
		LangEnglish: "(empty)", LangChinese: "（空）", LangTraditionalChinese: "（空）",
		LangJapanese: "（空）", LangSpanish: "(vacío)",
	},
	MsgCronIDLabel: {
		LangEnglish: "ID: %s\n", LangChinese: "ID：%s\n", LangTraditionalChinese: "ID：%s\n",
		LangJapanese: "ID: %s\n", LangSpanish: "ID: %s\n",
	},
	MsgCronFailedSuffix: {
		LangEnglish: " (failed: %s)", LangChinese: "（失败：%s）", LangTraditionalChinese: "（失敗：%s）",
		LangJapanese: "（失敗: %s）", LangSpanish: " (falló: %s)",
	},
	MsgCommandsTagAgent: {
		LangEnglish: " [agent]", LangChinese: " [代理]", LangTraditionalChinese: " [代理]",
		LangJapanese: " [エージェント]", LangSpanish: " [agente]",
	},
	MsgCommandsTagShell: {
		LangEnglish: " [shell]", LangChinese: " [终端]", LangTraditionalChinese: " [終端]",
		LangJapanese: " [シェル]", LangSpanish: " [shell]",
	},
	MsgUpgradeTimeoutSuffix: {
		LangEnglish: " (timeout)", LangChinese: "（超时）", LangTraditionalChinese: "（逾時）",
		LangJapanese: "（タイムアウト）", LangSpanish: " (tiempo de espera agotado)",
	},
	MsgCronScheduleLabel: {
		LangEnglish:            "Schedule: %s `%s`\n",
		LangChinese:            "调度: %s `%s`\n",
		LangTraditionalChinese: "調度: %s `%s`\n",
		LangJapanese:           "スケジュール: %s `%s`\n",
		LangSpanish:            "Programación: %s `%s`\n",
	},
	MsgCronNextRunLabel: {
		LangEnglish:            "Next run: %s\n",
		LangChinese:            "下次执行: %s\n",
		LangTraditionalChinese: "下次執行: %s\n",
		LangJapanese:           "次回実行: %s\n",
		LangSpanish:            "Próxima ejecución: %s\n",
	},
	MsgCronLastRunLabel: {
		LangEnglish:            "Last run: %s",
		LangChinese:            "上次执行: %s",
		LangTraditionalChinese: "上次執行: %s",
		LangJapanese:           "前回実行: %s",
		LangSpanish:            "Última ejecución: %s",
	},
	MsgPermBtnAllow: {
		LangEnglish:            "Allow",
		LangChinese:            "允许",
		LangTraditionalChinese: "允許",
		LangJapanese:           "許可",
		LangSpanish:            "Permitir",
	},
	MsgPermBtnDeny: {
		LangEnglish:            "Deny",
		LangChinese:            "拒绝",
		LangTraditionalChinese: "拒絕",
		LangJapanese:           "拒否",
		LangSpanish:            "Denegar",
	},
	MsgPermBtnAllowAll: {
		LangEnglish:            "Allow All (this session)",
		LangChinese:            "允许所有 (本次会话)",
		LangTraditionalChinese: "允許所有 (本次會話)",
		LangJapanese:           "すべて許可 (このセッション)",
		LangSpanish:            "Permitir todo (esta sesión)",
	},
	MsgPermCardTitle: {
		LangEnglish:            "Permission Request",
		LangChinese:            "权限请求",
		LangTraditionalChinese: "權限請求",
		LangJapanese:           "権限リクエスト",
		LangSpanish:            "Solicitud de permiso",
	},
	MsgPermCardBody: {
		LangEnglish:            "Agent wants to use **%s**:\n\n```\n%s\n```",
		LangChinese:            "Agent 想要使用 **%s**:\n\n```\n%s\n```",
		LangTraditionalChinese: "Agent 想要使用 **%s**:\n\n```\n%s\n```",
		LangJapanese:           "エージェントが **%s** を使用しようとしています:\n\n```\n%s\n```",
		LangSpanish:            "El agente quiere usar **%s**:\n\n```\n%s\n```",
	},
	MsgPermCardNote: {
		LangEnglish:            "If buttons are unresponsive, reply: allow / deny / allow all",
		LangChinese:            "如果按钮无响应，请直接回复：允许 / 拒绝 / 允许所有",
		LangTraditionalChinese: "若按鈕無回應，請直接回覆：允許 / 拒絕 / 允許所有",
		LangJapanese:           "ボタンが反応しない場合は直接返信: allow / deny / allow all",
		LangSpanish:            "Si los botones no responden, responda: allow / deny / allow all",
	},
	MsgAskQuestionTitle: {
		LangEnglish:            "Agent Question",
		LangChinese:            "Agent 提问",
		LangTraditionalChinese: "Agent 提問",
		LangJapanese:           "エージェントの質問",
		LangSpanish:            "Pregunta del agente",
	},
	MsgAskQuestionNote: {
		LangEnglish:            "If buttons are unresponsive, reply with the option number (e.g. 1) or type your answer",
		LangChinese:            "如果按钮无响应，请回复选项编号（如 1）或直接输入你的回答",
		LangTraditionalChinese: "若按鈕無回應，請回覆選項編號（如 1）或直接輸入你的回答",
		LangJapanese:           "ボタンが反応しない場合は、番号（例: 1）で返信するか、直接回答を入力してください",
		LangSpanish:            "Si los botones no responden, responda con el número de opción (ej. 1) o escriba su respuesta",
	},
	MsgAskQuestionNoteMulti: {
		LangEnglish:            "Reply with comma-separated option numbers (e.g. 1,3) or type your answer",
		LangChinese:            "请回复逗号分隔的选项编号（如 1,3）或直接输入你的回答",
		LangTraditionalChinese: "請回覆逗號分隔的選項編號（如 1,3）或直接輸入你的回答",
		LangJapanese:           "カンマ区切りの番号（例: 1,3）で返信するか、直接回答を入力してください",
		LangSpanish:            "Responda con los números de opción separados por comas (ej. 1,3) o escriba su respuesta",
	},
	MsgAskQuestionMulti: {
		LangEnglish:            " (multiple selections allowed, separate with commas)",
		LangChinese:            "（可多选，用逗号分隔）",
		LangTraditionalChinese: "（可多選，用逗號分隔）",
		LangJapanese:           "（複数選択可、カンマで区切る）",
		LangSpanish:            " (selección múltiple permitida, separe con comas)",
	},
	MsgAskQuestionPrompt: {
		LangEnglish:            "❓ **%s**\n\n%s\n\nReply with the option number or type your answer.",
		LangChinese:            "❓ **%s**\n\n%s\n\n请回复选项编号或直接输入你的回答。",
		LangTraditionalChinese: "❓ **%s**\n\n%s\n\n請回覆選項編號或直接輸入你的回答。",
		LangJapanese:           "❓ **%s**\n\n%s\n\n番号で返信するか、回答を直接入力してください。",
		LangSpanish:            "❓ **%s**\n\n%s\n\nResponda con el número de opción o escriba su respuesta.",
	},
	MsgAskQuestionAnswered: {
		LangEnglish:            "Answer",
		LangChinese:            "已回答",
		LangTraditionalChinese: "已回答",
		LangJapanese:           "回答済み",
		LangSpanish:            "Respondido",
	},
	MsgCommandsTitle: {
		LangEnglish:            "🔧 **Custom Commands** (%d)\n\n",
		LangChinese:            "🔧 **自定义命令** (%d)\n\n",
		LangTraditionalChinese: "🔧 **自訂命令** (%d)\n\n",
		LangJapanese:           "🔧 **カスタムコマンド** (%d)\n\n",
		LangSpanish:            "🔧 **Comandos personalizados** (%d)\n\n",
	},
	MsgCommandsEmpty: {
		LangEnglish:            "No custom commands configured.\n\nUse `/commands add <name> <prompt>` or add `[[commands]]` in config.toml.",
		LangChinese:            "未配置自定义命令。\n\n使用 `/commands add <名称> <prompt>` 添加，或在 config.toml 中配置 `[[commands]]`。",
		LangTraditionalChinese: "未配置自訂命令。\n\n使用 `/commands add <名稱> <prompt>` 新增，或在 config.toml 中配置 `[[commands]]`。",
		LangJapanese:           "カスタムコマンドが設定されていません。\n\n`/commands add <名前> <プロンプト>` で追加するか、config.toml に `[[commands]]` を追加してください。",
		LangSpanish:            "No hay comandos personalizados configurados.\n\nUse `/commands add <nombre> <prompt>` o agregue `[[commands]]` en config.toml.",
	},
	MsgCommandsHint: {
		LangEnglish:            "Type `/<name> [args]` to use.\n`/commands add <name> <prompt>` to add prompt command\n`/commands addexec <name> <shell>` to add exec command\n`/commands del <name>` to remove",
		LangChinese:            "输入 `/<名称> [参数]` 使用。\n`/commands add <名称> <prompt>` 添加 prompt 命令\n`/commands addexec <名称> <shell命令>` 添加 exec 命令\n`/commands del <名称>` 删除",
		LangTraditionalChinese: "輸入 `/<名稱> [參數]` 使用。\n`/commands add <名稱> <prompt>` 新增 prompt 命令\n`/commands addexec <名稱> <shell命令>` 新增 exec 命令\n`/commands del <名稱>` 刪除",
		LangJapanese:           "`/<名前> [引数]` で使用。\n`/commands add <名前> <プロンプト>` プロンプトコマンド追加\n`/commands addexec <名前> <シェルコマンド>` execコマンド追加\n`/commands del <名前>` 削除",
		LangSpanish:            "Escriba `/<nombre> [args]` para usar.\n`/commands add <nombre> <prompt>` agregar comando prompt\n`/commands addexec <nombre> <shell>` agregar comando exec\n`/commands del <nombre>` eliminar",
	},
	MsgCommandsUsage: {
		LangEnglish:            "Usage:\n`/commands` — list all custom commands\n`/commands add <name> <prompt>` — add prompt command\n`/commands addexec <name> <shell>` — add exec command\n`/commands del <name>` — remove a command",
		LangChinese:            "用法：\n`/commands` — 列出所有自定义命令\n`/commands add <名称> <prompt>` — 添加 prompt 命令\n`/commands addexec <名称> <shell命令>` — 添加 exec 命令\n`/commands del <名称>` — 删除命令",
		LangTraditionalChinese: "用法：\n`/commands` — 列出所有自訂命令\n`/commands add <名稱> <prompt>` — 新增 prompt 命令\n`/commands addexec <名稱> <shell命令>` — 新增 exec 命令\n`/commands del <名稱>` — 刪除命令",
		LangJapanese:           "使い方:\n`/commands` — カスタムコマンド一覧\n`/commands add <名前> <プロンプト>` — プロンプトコマンド追加\n`/commands addexec <名前> <シェルコマンド>` — execコマンド追加\n`/commands del <名前>` — コマンド削除",
		LangSpanish:            "Uso:\n`/commands` — listar comandos personalizados\n`/commands add <nombre> <prompt>` — agregar comando prompt\n`/commands addexec <nombre> <shell>` — agregar comando exec\n`/commands del <nombre>` — eliminar comando",
	},
	MsgCommandsAddUsage: {
		LangEnglish:            "Usage: `/commands add <name> <prompt template>`\n\nExample: `/commands add finduser Search the database for user「{{1}}」`",
		LangChinese:            "用法：`/commands add <名称> <prompt 模板>`\n\n示例：`/commands add finduser 在数据库中查找用户「{{1}}」`",
		LangTraditionalChinese: "用法：`/commands add <名稱> <prompt 模板>`\n\n範例：`/commands add finduser 在資料庫中查找用戶「{{1}}」`",
		LangJapanese:           "使い方: `/commands add <名前> <プロンプトテンプレート>`\n\n例: `/commands add finduser データベースでユーザー「{{1}}」を検索`",
		LangSpanish:            "Uso: `/commands add <nombre> <plantilla prompt>`\n\nEjemplo: `/commands add finduser Buscar en la base de datos al usuario「{{1}}」`",
	},
	MsgCommandsAddExecUsage: {
		LangEnglish:            "Usage: `/commands addexec <name> <shell command>`\n         `/commands addexec --work-dir <dir> <name> <shell command>`\n\nExamples:\n`/commands addexec push git push`\n`/commands addexec status git status {{args}}`",
		LangChinese:            "用法：`/commands addexec <名称> <shell 命令>`\n      `/commands addexec --work-dir <目录> <名称> <shell 命令>`\n\n示例：\n`/commands addexec push git push`\n`/commands addexec status git status {{args}}`",
		LangTraditionalChinese: "用法：`/commands addexec <名稱> <shell 命令>`\n      `/commands addexec --work-dir <目錄> <名稱> <shell 命令>`\n\n範例：\n`/commands addexec push git push`\n`/commands addexec status git status {{args}}`",
		LangJapanese:           "使い方: `/commands addexec <名前> <シェルコマンド>`\n         `/commands addexec --work-dir <ディレクトリ> <名前> <シェルコマンド>`\n\n例:\n`/commands addexec push git push`\n`/commands addexec status git status {{args}}`",
		LangSpanish:            "Uso: `/commands addexec <nombre> <comando shell>`\n      `/commands addexec --work-dir <dir> <nombre> <comando shell>`\n\nEjemplos:\n`/commands addexec push git push`\n`/commands addexec status git status {{args}}`",
	},
	MsgCommandsAdded: {
		LangEnglish:            "✅ Command `/%s` added.\nPrompt: %s",
		LangChinese:            "✅ 命令 `/%s` 已添加。\nPrompt: %s",
		LangTraditionalChinese: "✅ 命令 `/%s` 已新增。\nPrompt: %s",
		LangJapanese:           "✅ コマンド `/%s` を追加しました。\nプロンプト: %s",
		LangSpanish:            "✅ Comando `/%s` agregado.\nPrompt: %s",
	},
	MsgCommandsAddExists: {
		LangEnglish:            "❌ Command `/%s` already exists. Remove it first with `/commands del %s`.",
		LangChinese:            "❌ 命令 `/%s` 已存在。请先使用 `/commands del %s` 删除。",
		LangTraditionalChinese: "❌ 命令 `/%s` 已存在。請先使用 `/commands del %s` 刪除。",
		LangJapanese:           "❌ コマンド `/%s` は既に存在します。`/commands del %s` で削除してから追加してください。",
		LangSpanish:            "❌ El comando `/%s` ya existe. Elimínelo primero con `/commands del %s`.",
	},
	MsgCommandsDelUsage: {
		LangEnglish:            "Usage: `/commands del <name>`",
		LangChinese:            "用法：`/commands del <名称>`",
		LangTraditionalChinese: "用法：`/commands del <名稱>`",
		LangJapanese:           "使い方: `/commands del <名前>`",
		LangSpanish:            "Uso: `/commands del <nombre>`",
	},
	MsgCommandsDeleted: {
		LangEnglish:            "✅ Command `/%s` removed.",
		LangChinese:            "✅ 命令 `/%s` 已删除。",
		LangTraditionalChinese: "✅ 命令 `/%s` 已刪除。",
		LangJapanese:           "✅ コマンド `/%s` を削除しました。",
		LangSpanish:            "✅ Comando `/%s` eliminado.",
	},
	MsgCommandsNotFound: {
		LangEnglish:            "❌ Command `/%s` not found. Use `/commands` to see available commands.",
		LangChinese:            "❌ 命令 `/%s` 未找到。使用 `/commands` 查看可用命令。",
		LangTraditionalChinese: "❌ 命令 `/%s` 未找到。使用 `/commands` 查看可用命令。",
		LangJapanese:           "❌ コマンド `/%s` が見つかりません。`/commands` で一覧を確認してください。",
		LangSpanish:            "❌ Comando `/%s` no encontrado. Use `/commands` para ver los comandos disponibles.",
	},
	MsgCommandsExecAdded: {
		LangEnglish:            "✅ Exec command `/%s` added.\nCommand: %s",
		LangChinese:            "✅ Exec 命令 `/%s` 已添加。\n命令: %s",
		LangTraditionalChinese: "✅ Exec 命令 `/%s` 已新增。\n命令: %s",
		LangJapanese:           "✅ Exec コマンド `/%s` を追加しました。\nコマンド: %s",
		LangSpanish:            "✅ Comando exec `/%s` agregado.\nComando: %s",
	},
	MsgCommandExecTimeout: {
		LangEnglish:            "⏱️ Command `/%s` timed out (60s limit).",
		LangChinese:            "⏱️ 命令 `/%s` 超时（60秒限制）。",
		LangTraditionalChinese: "⏱️ 命令 `/%s` 超時（60秒限制）。",
		LangJapanese:           "⏱️ コマンド `/%s` がタイムアウトしました（60秒制限）。",
		LangSpanish:            "⏱️ Comando `/%s` agotó el tiempo (límite 60s).",
	},
	MsgCommandExecError: {
		LangEnglish:            "❌ Command `/%s` failed:\n%s",
		LangChinese:            "❌ 命令 `/%s` 执行失败：\n%s",
		LangTraditionalChinese: "❌ 命令 `/%s` 執行失敗：\n%s",
		LangJapanese:           "❌ コマンド `/%s` が失敗しました：\n%s",
		LangSpanish:            "❌ Comando `/%s` falló:\n%s",
	},
	MsgCommandExecSuccess: {
		LangEnglish:            "✅ Command executed successfully (no output).",
		LangChinese:            "✅ 命令执行成功（无输出）。",
		LangTraditionalChinese: "✅ 命令執行成功（無輸出）。",
		LangJapanese:           "✅ コマンドが正常に実行されました（出力なし）。",
		LangSpanish:            "✅ Comando ejecutado exitosamente (sin salida).",
	},
	MsgSkillsTitle: {
		LangEnglish:            "📋 Available Skills (%s) — %d skill(s)\n\n",
		LangChinese:            "📋 可用 Skills (%s) — %d 个\n\n",
		LangTraditionalChinese: "📋 可用 Skills (%s) — %d 個\n\n",
		LangJapanese:           "📋 利用可能なスキル (%s) — %d 個\n\n",
		LangSpanish:            "📋 Skills disponibles (%s) — %d skill(s)\n\n",
	},
	MsgSkillsEmpty: {
		LangEnglish:            "No skills found.\nSkills are discovered from agent directories (e.g. .claude/skills/<name>/SKILL.md).",
		LangChinese:            "未发现任何 Skill。\nSkill 从 Agent 目录自动发现（如 .claude/skills/<name>/SKILL.md）。",
		LangTraditionalChinese: "未發現任何 Skill。\nSkill 從 Agent 目錄自動發現（如 .claude/skills/<name>/SKILL.md）。",
		LangJapanese:           "スキルが見つかりません。\nスキルはエージェントのディレクトリから自動検出されます（例: .claude/skills/<name>/SKILL.md）。",
		LangSpanish:            "No se encontraron skills.\nLos skills se descubren de los directorios del agente (ej. .claude/skills/<name>/SKILL.md).",
	},
	MsgSkillsHint: {
		LangEnglish:            "Usage: /<skill-name> [args...] to invoke a skill.",
		LangChinese:            "用法：/<skill名称> [参数...] 来调用 Skill。",
		LangTraditionalChinese: "用法：/<skill名稱> [參數...] 來調用 Skill。",
		LangJapanese:           "使い方：/<スキル名> [引数...] でスキルを実行します。",
		LangSpanish:            "Uso: /<nombre-skill> [args...] para invocar un skill.",
	},
	MsgSkillsTelegramMenuHint: {
		LangEnglish:            "Telegram's command menu is full, so skill commands are not listed there. You can still invoke them by typing /<skill-name> manually.",
		LangChinese:            "Telegram 的命令菜单已满，因此 Skill 不会显示在那里。你仍然可以手动输入 /<skill名称> 来调用它们。",
		LangTraditionalChinese: "Telegram 的命令選單已滿，因此 Skill 不會顯示在那裡。你仍然可以手動輸入 /<skill名稱> 來調用它們。",
		LangJapanese:           "Telegram のコマンドメニューがいっぱいのため、スキルコマンドはそこに表示されません。手動で /<スキル名> と入力すれば実行できます。",
		LangSpanish:            "El menú de comandos de Telegram está lleno, así que los skills no aparecen allí. Aun así puedes invocarlos escribiendo /<nombre-skill> manualmente.",
	},

	MsgConfigTitle: {
		LangEnglish:            "⚙️ **Runtime Configuration**\n\n",
		LangChinese:            "⚙️ **运行时配置**\n\n",
		LangTraditionalChinese: "⚙️ **執行階段配置**\n\n",
		LangJapanese:           "⚙️ **ランタイム設定**\n\n",
		LangSpanish:            "⚙️ **Configuración en tiempo de ejecución**\n\n",
	},
	MsgConfigHint: {
		LangEnglish: "Usage:\n" +
			"`/config` — show all\n" +
			"`/config thinking_max_len 200` — update\n" +
			"`/config get thinking_max_len` — view single\n\n" +
			"Set to `0` to disable truncation.",
		LangChinese: "用法：\n" +
			"`/config` — 查看所有配置\n" +
			"`/config thinking_max_len 200` — 修改配置\n" +
			"`/config get thinking_max_len` — 查看单项\n\n" +
			"设为 `0` 表示不截断。",
		LangTraditionalChinese: "用法：\n" +
			"`/config` — 查看所有配置\n" +
			"`/config thinking_max_len 200` — 修改配置\n" +
			"`/config get thinking_max_len` — 查看單項\n\n" +
			"設為 `0` 表示不截斷。",
		LangJapanese: "使い方:\n" +
			"`/config` — 全設定を表示\n" +
			"`/config thinking_max_len 200` — 変更\n" +
			"`/config get thinking_max_len` — 単一確認\n\n" +
			"`0` = 切り捨てなし",
		LangSpanish: "Uso:\n" +
			"`/config` — ver todo\n" +
			"`/config thinking_max_len 200` — actualizar\n" +
			"`/config get thinking_max_len` — ver uno\n\n" +
			"Establecer `0` para no truncar.",
	},
	MsgConfigGetUsage: {
		LangEnglish:            "Usage: `/config get thinking_max_len`",
		LangChinese:            "用法：`/config get thinking_max_len`",
		LangTraditionalChinese: "用法：`/config get thinking_max_len`",
		LangJapanese:           "使い方: `/config get thinking_max_len`",
		LangSpanish:            "Uso: `/config get thinking_max_len`",
	},
	MsgConfigSetUsage: {
		LangEnglish:            "Usage: `/config set thinking_max_len 200`",
		LangChinese:            "用法：`/config set thinking_max_len 200`",
		LangTraditionalChinese: "用法：`/config set thinking_max_len 200`",
		LangJapanese:           "使い方: `/config set thinking_max_len 200`",
		LangSpanish:            "Uso: `/config set thinking_max_len 200`",
	},
	MsgConfigUpdated: {
		LangEnglish:            "✅ `%s` → `%s`",
		LangChinese:            "✅ `%s` → `%s`",
		LangTraditionalChinese: "✅ `%s` → `%s`",
		LangJapanese:           "✅ `%s` → `%s`",
		LangSpanish:            "✅ `%s` → `%s`",
	},
	MsgConfigKeyNotFound: {
		LangEnglish:            "❌ Unknown config key `%s`. Use `/config` to see available keys.",
		LangChinese:            "❌ 未知配置项 `%s`。使用 `/config` 查看可用配置。",
		LangTraditionalChinese: "❌ 未知配置項 `%s`。使用 `/config` 查看可用配置。",
		LangJapanese:           "❌ 不明な設定キー `%s`。`/config` で一覧を確認してください。",
		LangSpanish:            "❌ Clave de configuración desconocida `%s`. Use `/config` para ver las disponibles.",
	},
	MsgConfigReloaded: {
		LangEnglish:            "✅ Config reloaded\n\nDisplay updated: %v\nProviders synced: %d\nCommands synced: %d",
		LangChinese:            "✅ 配置已重新加载\n\n显示设置已更新：%v\nProvider 已同步：%d 个\n自定义命令已同步：%d 个",
		LangTraditionalChinese: "✅ 配置已重新載入\n\n顯示設定已更新：%v\nProvider 已同步：%d 個\n自訂命令已同步：%d 個",
		LangJapanese:           "✅ 設定をリロードしました\n\n表示設定更新: %v\nプロバイダ同期: %d 件\nコマンド同期: %d 件",
		LangSpanish:            "✅ Configuración recargada\n\nPantalla actualizada: %v\nProveedores sincronizados: %d\nComandos sincronizados: %d",
	},
	MsgDoctorRunning: {
		LangEnglish:            "🏥 Running diagnostics...",
		LangChinese:            "🏥 正在运行系统诊断...",
		LangTraditionalChinese: "🏥 正在執行系統診斷...",
		LangJapanese:           "🏥 診断を実行中...",
		LangSpanish:            "🏥 Ejecutando diagnósticos...",
	},
	MsgDoctorTitle: {
		LangEnglish:            "🏥 **System Diagnostic Report**\n\n",
		LangChinese:            "🏥 **系统诊断报告**\n\n",
		LangTraditionalChinese: "🏥 **系統診斷報告**\n\n",
		LangJapanese:           "🏥 **システム診断レポート**\n\n",
		LangSpanish:            "🏥 **Informe de diagnóstico del sistema**\n\n",
	},
	MsgDoctorSummary: {
		LangEnglish:            "\n✅ %d passed  ⚠️ %d warnings  ❌ %d failed",
		LangChinese:            "\n✅ %d 项通过  ⚠️ %d 项警告  ❌ %d 项失败",
		LangTraditionalChinese: "\n✅ %d 項通過  ⚠️ %d 項警告  ❌ %d 項失敗",
		LangJapanese:           "\n✅ %d 合格  ⚠️ %d 警告  ❌ %d 失敗",
		LangSpanish:            "\n✅ %d aprobados  ⚠️ %d advertencias  ❌ %d fallidos",
	},
	MsgRestarting: {
		LangEnglish:            "🔄 Restarting cc-connect...",
		LangChinese:            "🔄 正在重启 cc-connect...",
		LangTraditionalChinese: "🔄 正在重啟 cc-connect...",
		LangJapanese:           "🔄 cc-connect を再起動中...",
		LangSpanish:            "🔄 Reiniciando cc-connect...",
	},
	MsgRestartSuccess: {
		LangEnglish:            "✅ cc-connect restarted successfully.",
		LangChinese:            "✅ cc-connect 重启成功。",
		LangTraditionalChinese: "✅ cc-connect 重啟成功。",
		LangJapanese:           "✅ cc-connect の再起動が完了しました。",
		LangSpanish:            "✅ cc-connect se reinició correctamente.",
	},
	MsgUpgradeChecking: {
		LangEnglish:            "🔍 Checking for updates...",
		LangChinese:            "🔍 正在检查更新...",
		LangTraditionalChinese: "🔍 正在檢查更新...",
		LangJapanese:           "🔍 アップデートを確認中...",
		LangSpanish:            "🔍 Buscando actualizaciones...",
	},
	MsgUpgradeUpToDate: {
		LangEnglish:            "✅ Already up to date (%s)",
		LangChinese:            "✅ 已是最新版本 (%s)",
		LangTraditionalChinese: "✅ 已是最新版本 (%s)",
		LangJapanese:           "✅ 最新バージョンです (%s)",
		LangSpanish:            "✅ Ya está actualizado (%s)",
	},
	MsgUpgradeAvailable: {
		LangEnglish: "🆕 New version available!\n\n\n" +
			"Current: **%s**\n" +
			"Latest:  **%s**\n\n\n" +
			"%s\n\n\n" +
			"Run `/upgrade confirm` to install.",
		LangChinese: "🆕 发现新版本！\n\n\n" +
			"当前版本：**%s**\n" +
			"最新版本：**%s**\n\n\n" +
			"%s\n\n\n" +
			"执行 `/upgrade confirm` 进行更新。",
		LangTraditionalChinese: "🆕 發現新版本！\n\n\n" +
			"當前版本：**%s**\n" +
			"最新版本：**%s**\n\n\n" +
			"%s\n\n\n" +
			"執行 `/upgrade confirm` 進行更新。",
		LangJapanese: "🆕 新しいバージョンがあります！\n\n\n" +
			"現在: **%s**\n" +
			"最新: **%s**\n\n\n" +
			"%s\n\n" +
			"`/upgrade confirm` でインストール。",
		LangSpanish: "🆕 ¡Nueva versión disponible!\n\n\n" +
			"Actual: **%s**\n" +
			"Última: **%s**\n\n\n" +
			"%s\n\n\n" +
			"Ejecute `/upgrade confirm` para instalar.",
	},
	MsgUpgradeDownloading: {
		LangEnglish:            "⬇️ Downloading %s ...",
		LangChinese:            "⬇️ 正在下载 %s ...",
		LangTraditionalChinese: "⬇️ 正在下載 %s ...",
		LangJapanese:           "⬇️ ダウンロード中 %s ...",
		LangSpanish:            "⬇️ Descargando %s ...",
	},
	MsgUpgradeSuccess: {
		LangEnglish:            "✅ Updated to **%s** successfully! Restarting...",
		LangChinese:            "✅ 已成功更新到 **%s**！正在重启...",
		LangTraditionalChinese: "✅ 已成功更新到 **%s**！正在重啟...",
		LangJapanese:           "✅ **%s** に更新しました！再起動中...",
		LangSpanish:            "✅ ¡Actualizado a **%s** con éxito! Reiniciando...",
	},
	MsgUpgradeDevBuild: {
		LangEnglish:            "⚠️ Running a dev build — version check is not available. Please build from source or install a release version.",
		LangChinese:            "⚠️ 当前为开发版本，无法检查更新。请从源码构建或安装正式发布版本。",
		LangTraditionalChinese: "⚠️ 當前為開發版本，無法檢查更新。請從源碼構建或安裝正式發佈版本。",
		LangJapanese:           "⚠️ 開発ビルドのため、バージョン確認ができません。ソースからビルドするか、リリース版をインストールしてください。",
		LangSpanish:            "⚠️ Compilación de desarrollo — la verificación de versión no está disponible. Compile desde el código fuente o instale una versión publicada.",
	},
	MsgWebNotSupported: {
		LangEnglish:            "⚠️ Web admin is not available in this build. Rebuild without the `no_web` tag to enable it.",
		LangChinese:            "⚠️ 当前版本未包含 Web 管理后台。请去掉 `no_web` 标签重新编译以启用。",
		LangTraditionalChinese: "⚠️ 目前版本未包含 Web 管理後台。請移除 `no_web` 標籤重新編譯以啟用。",
		LangJapanese:           "⚠️ このビルドにはWeb管理画面が含まれていません。`no_web` タグなしで再ビルドしてください。",
		LangSpanish:            "⚠️ La administración web no está incluida en esta compilación. Recompile sin la etiqueta `no_web`.",
	},
	MsgWebNotEnabled: {
		LangEnglish:            "ℹ️ Web admin is not enabled.\n\nUse `/web setup` to configure and enable it.",
		LangChinese:            "ℹ️ Web 管理后台未启用。\n\n使用 `/web setup` 配置并启用。",
		LangTraditionalChinese: "ℹ️ Web 管理後台未啟用。\n\n使用 `/web setup` 設定並啟用。",
		LangJapanese:           "ℹ️ Web管理画面は有効になっていません。\n\n`/web setup` で設定して有効にしてください。",
		LangSpanish:            "ℹ️ La administración web no está habilitada.\n\nUsa `/web setup` para configurarla.",
	},
	MsgWebSetupSuccess: {
		LangEnglish: "✅ Web admin configured!\n\n" +
			"🌐 URL: %s\n🔑 Token: `%s`\n\n" +
			"Open the URL in your browser and use the token to log in.",
		LangChinese: "✅ Web 管理后台配置完成！\n\n" +
			"🌐 地址：%s\n🔑 令牌：`%s`\n\n" +
			"在浏览器打开地址，使用令牌登录。",
		LangTraditionalChinese: "✅ Web 管理後台設定完成！\n\n" +
			"🌐 網址：%s\n🔑 權杖：`%s`\n\n" +
			"在瀏覽器開啟網址，使用權杖登入。",
		LangJapanese: "✅ Web管理画面の設定が完了しました！\n\n" +
			"🌐 URL: %s\n🔑 トークン: `%s`\n\n" +
			"ブラウザでURLを開き、トークンでログインしてください。",
		LangSpanish: "✅ Administración web configurada!\n\n" +
			"🌐 URL: %s\n🔑 Token: `%s`\n\n" +
			"Abre la URL en tu navegador y usa el token para iniciar sesión.",
	},
	MsgWebNeedRestart: {
		LangEnglish:            "🔄 Restart the service with `/restart` to activate the web admin.",
		LangChinese:            "🔄 请使用 `/restart` 重启服务以激活 Web 管理后台。",
		LangTraditionalChinese: "🔄 請使用 `/restart` 重新啟動服務以啟動 Web 管理後台。",
		LangJapanese:           "🔄 `/restart` でサービスを再起動して、Web管理画面を有効にしてください。",
		LangSpanish:            "🔄 Reinicia el servicio con `/restart` para activar la administración web.",
	},
	MsgWebStatus: {
		LangEnglish:            "🌐 **Web Admin**\n\nURL: %s",
		LangChinese:            "🌐 **Web 管理后台**\n\n地址：%s",
		LangTraditionalChinese: "🌐 **Web 管理後台**\n\n網址：%s",
		LangJapanese:           "🌐 **Web管理画面**\n\nURL: %s",
		LangSpanish:            "🌐 **Administración Web**\n\nURL: %s",
	},
	MsgAliasEmpty: {
		LangEnglish:            "No aliases configured. Use `/alias add <trigger> <command>` to create one.",
		LangChinese:            "暂无别名配置。使用 `/alias add <触发词> <命令>` 创建别名。",
		LangTraditionalChinese: "尚無別名配置。使用 `/alias add <觸發詞> <命令>` 建立別名。",
		LangJapanese:           "エイリアスは設定されていません。`/alias add <トリガー> <コマンド>` で作成してください。",
		LangSpanish:            "No hay alias configurados. Use `/alias add <trigger> <comando>` para crear uno.",
	},
	MsgAliasListHeader: {
		LangEnglish:            "📎 Aliases (%d)",
		LangChinese:            "📎 命令别名 (%d)",
		LangTraditionalChinese: "📎 命令別名 (%d)",
		LangJapanese:           "📎 エイリアス (%d)",
		LangSpanish:            "📎 Alias (%d)",
	},
	MsgAliasAdded: {
		LangEnglish:            "✅ Alias added: %s → %s",
		LangChinese:            "✅ 别名已添加：%s → %s",
		LangTraditionalChinese: "✅ 別名已新增：%s → %s",
		LangJapanese:           "✅ エイリアス追加：%s → %s",
		LangSpanish:            "✅ Alias añadido: %s → %s",
	},
	MsgAliasDeleted: {
		LangEnglish:            "✅ Alias removed: %s",
		LangChinese:            "✅ 别名已删除：%s",
		LangTraditionalChinese: "✅ 別名已刪除：%s",
		LangJapanese:           "✅ エイリアス削除：%s",
		LangSpanish:            "✅ Alias eliminado: %s",
	},
	MsgAliasNotFound: {
		LangEnglish:            "❌ Alias `%s` not found.",
		LangChinese:            "❌ 别名 `%s` 不存在。",
		LangTraditionalChinese: "❌ 別名 `%s` 不存在。",
		LangJapanese:           "❌ エイリアス `%s` が見つかりません。",
		LangSpanish:            "❌ Alias `%s` no encontrado.",
	},
	MsgAliasUsage: {
		LangEnglish:            "Usage:\n  `/alias` — list all aliases\n  `/alias add <trigger> <command>` — add alias\n  `/alias del <trigger>` — remove alias\n\nExample: `/alias add 帮助 /help`",
		LangChinese:            "用法：\n  `/alias` — 列出所有别名\n  `/alias add <触发词> <命令>` — 添加别名\n  `/alias del <触发词>` — 删除别名\n\n示例：`/alias add 帮助 /help`",
		LangTraditionalChinese: "用法：\n  `/alias` — 列出所有別名\n  `/alias add <觸發詞> <命令>` — 新增別名\n  `/alias del <觸發詞>` — 刪除別名\n\n範例：`/alias add 幫助 /help`",
		LangJapanese:           "使い方：\n  `/alias` — エイリアス一覧\n  `/alias add <トリガー> <コマンド>` — 追加\n  `/alias del <トリガー>` — 削除\n\n例: `/alias add ヘルプ /help`",
		LangSpanish:            "Uso:\n  `/alias` — listar aliases\n  `/alias add <trigger> <comando>` — añadir alias\n  `/alias del <trigger>` — eliminar alias\n\nEjemplo: `/alias add ayuda /help`",
	},
	MsgNewSessionCreated: {
		LangEnglish:            "✅ New session created",
		LangChinese:            "✅ 新会话已创建",
		LangTraditionalChinese: "✅ 新會話已建立",
		LangJapanese:           "✅ 新しいセッションを作成しました",
		LangSpanish:            "✅ Nueva sesión creada",
	},
	MsgNewSessionCreatedName: {
		LangEnglish:            "✅ New session created: **%s**",
		LangChinese:            "✅ 新会话已创建：**%s**",
		LangTraditionalChinese: "✅ 新會話已建立：**%s**",
		LangJapanese:           "✅ 新しいセッションを作成しました：**%s**",
		LangSpanish:            "✅ Nueva sesión creada: **%s**",
	},
	MsgSessionAutoResetIdle: {
		LangEnglish:            "⏰ Session auto-reset after %d minute(s) of inactivity.",
		LangChinese:            "⏰ 因空闲超过 %d 分钟，已自动切换到新会话。",
		LangTraditionalChinese: "⏰ 因閒置超過 %d 分鐘，已自動切換到新會話。",
		LangJapanese:           "⏰ %d 分以上操作がなかったため、新しいセッションに自動切り替えました。",
		LangSpanish:            "⏰ La sesión se reinició automáticamente tras %d minuto(s) de inactividad.",
	},
	MsgSessionClosingGraceful: {
		LangEnglish:            "⏳ Wrapping up your previous session (usually a few seconds, up to 2 minutes). Your new session will start automatically.",
		LangChinese:            "⏳ 正在结束上一个会话（通常几秒钟，最多2分钟）。新会话将自动启动。",
		LangTraditionalChinese: "⏳ 正在結束上一個會話（通常幾秒鐘，最多2分鐘）。新會話將自動啟動。",
		LangJapanese:           "⏳ 前のセッションを終了中です（通常は数秒、最大2分）。新しいセッションは自動的に開始されます。",
		LangSpanish:            "⏳ Cerrando la sesión anterior (normalmente unos segundos, hasta 2 minutos). La nueva sesión se iniciará automáticamente.",
	},
	MsgDeleteUsage: {
		LangEnglish:            "Usage: `/delete <number>` or `/delete 1,2,3` or `/delete 3-7` or `/delete 1,3-5,8`.\nUse `/list` to see session numbers.",
		LangChinese:            "用法：`/delete <序号>`，或 `/delete 1,2,3`，或 `/delete 3-7`，或 `/delete 1,3-5,8`。\n使用 `/list` 查看会话序号。",
		LangTraditionalChinese: "用法：`/delete <序號>`，或 `/delete 1,2,3`，或 `/delete 3-7`，或 `/delete 1,3-5,8`。\n使用 `/list` 查看會話序號。",
		LangJapanese:           "使い方：`/delete <番号>`、または `/delete 1,2,3`、または `/delete 3-7`、または `/delete 1,3-5,8`。\n`/list` で番号を確認できます。",
		LangSpanish:            "Uso: `/delete <número>` o `/delete 1,2,3` o `/delete 3-7` o `/delete 1,3-5,8`.\nUse `/list` para ver los números.",
	},
	MsgDeleteSuccess: {
		LangEnglish:            "🗑️ Session deleted: %s",
		LangChinese:            "🗑️ 会话已删除：%s",
		LangTraditionalChinese: "🗑️ 會話已刪除：%s",
		LangJapanese:           "🗑️ セッション削除：%s",
		LangSpanish:            "🗑️ Sesión eliminada: %s",
	},
	MsgSwitchSuccess: {
		LangEnglish:            "✅ Switched to: %s (%s, %d msgs)",
		LangChinese:            "✅ 已切换到：%s（%s，%d 条消息）",
		LangTraditionalChinese: "✅ 已切換到：%s（%s，%d 則訊息）",
		LangJapanese:           "✅ 切り替え：%s（%s、%d件）",
		LangSpanish:            "✅ Cambiado a: %s (%s, %d mensajes)",
	},
	MsgSwitchNoMatch: {
		LangEnglish:            "❌ No session matching %q",
		LangChinese:            "❌ 没有找到匹配 %q 的会话",
		LangTraditionalChinese: "❌ 沒有找到匹配 %q 的會話",
		LangJapanese:           "❌ %q に一致するセッションが見つかりません",
		LangSpanish:            "❌ No hay sesión que coincida con %q",
	},
	MsgSwitchNoSession: {
		LangEnglish:            "❌ No session #%d",
		LangChinese:            "❌ 没有第 %d 个会话",
		LangTraditionalChinese: "❌ 沒有第 %d 個會話",
		LangJapanese:           "❌ セッション #%d が見つかりません",
		LangSpanish:            "❌ No hay sesión #%d",
	},
	MsgCommandTimeout: {
		LangEnglish:            "⏰ Command timed out (60s): `%s`",
		LangChinese:            "⏰ 命令超时 (60秒): `%s`",
		LangTraditionalChinese: "⏰ 命令逾時 (60秒): `%s`",
		LangJapanese:           "⏰ コマンドがタイムアウトしました (60秒): `%s`",
		LangSpanish:            "⏰ Comando agotado (60s): `%s`",
	},
	MsgDeleteActiveDenied: {
		LangEnglish:            "❌ Cannot delete the currently active session. Switch to another session first.",
		LangChinese:            "❌ 不能删除当前活跃会话，请先切换到其他会话。",
		LangTraditionalChinese: "❌ 不能刪除當前活躍會話，請先切換到其他會話。",
		LangJapanese:           "❌ 現在アクティブなセッションは削除できません。先に別のセッションに切り替えてください。",
		LangSpanish:            "❌ No se puede eliminar la sesión activa. Cambie a otra sesión primero.",
	},
	MsgDeleteNotSupported: {
		LangEnglish:            "❌ This agent does not support session deletion.",
		LangChinese:            "❌ 当前 Agent 不支持删除会话。",
		LangTraditionalChinese: "❌ 當前 Agent 不支持刪除會話。",
		LangJapanese:           "❌ このエージェントはセッション削除をサポートしていません。",
		LangSpanish:            "❌ Este agente no admite la eliminación de sesiones.",
	},
	MsgDeleteModeTitle: {
		LangEnglish:            "Delete Sessions",
		LangChinese:            "删除会话",
		LangTraditionalChinese: "刪除會話",
		LangJapanese:           "セッション削除",
		LangSpanish:            "Eliminar sesiones",
	},
	MsgDeleteModeSelect: {
		LangEnglish:            "Select",
		LangChinese:            "选择",
		LangTraditionalChinese: "選擇",
		LangJapanese:           "選択",
		LangSpanish:            "Seleccionar",
	},
	MsgDeleteModeSelected: {
		LangEnglish:            "Selected",
		LangChinese:            "已选",
		LangTraditionalChinese: "已選",
		LangJapanese:           "選択済み",
		LangSpanish:            "Seleccionado",
	},
	MsgDeleteModeSelectedCount: {
		LangEnglish:            "%d selected",
		LangChinese:            "已选 %d 项",
		LangTraditionalChinese: "已選 %d 項",
		LangJapanese:           "%d 件を選択中",
		LangSpanish:            "%d seleccionadas",
	},
	MsgDeleteModeDeleteSelected: {
		LangEnglish:            "Delete Selected",
		LangChinese:            "删除已选",
		LangTraditionalChinese: "刪除已選",
		LangJapanese:           "選択項目を削除",
		LangSpanish:            "Eliminar seleccionadas",
	},
	MsgDeleteModeCancel: {
		LangEnglish:            "Cancel",
		LangChinese:            "取消",
		LangTraditionalChinese: "取消",
		LangJapanese:           "キャンセル",
		LangSpanish:            "Cancelar",
	},
	MsgDeleteModeConfirmTitle: {
		LangEnglish:            "Confirm Delete",
		LangChinese:            "确认删除",
		LangTraditionalChinese: "確認刪除",
		LangJapanese:           "削除確認",
		LangSpanish:            "Confirmar eliminación",
	},
	MsgDeleteModeConfirmButton: {
		LangEnglish:            "Confirm Delete",
		LangChinese:            "确认删除",
		LangTraditionalChinese: "確認刪除",
		LangJapanese:           "削除を確認",
		LangSpanish:            "Confirmar eliminación",
	},
	MsgDeleteModeBackButton: {
		LangEnglish:            "Back",
		LangChinese:            "返回继续选择",
		LangTraditionalChinese: "返回繼續選擇",
		LangJapanese:           "選択に戻る",
		LangSpanish:            "Volver",
	},
	MsgDeleteModeEmptySelection: {
		LangEnglish:            "Select at least one session.",
		LangChinese:            "请至少选择一个会话。",
		LangTraditionalChinese: "請至少選擇一個會話。",
		LangJapanese:           "少なくとも 1 つのセッションを選択してください。",
		LangSpanish:            "Seleccione al menos una sesión.",
	},
	MsgDeleteModeResultTitle: {
		LangEnglish:            "Delete Result",
		LangChinese:            "删除结果",
		LangTraditionalChinese: "刪除結果",
		LangJapanese:           "削除結果",
		LangSpanish:            "Resultado de eliminación",
	},
	MsgDeleteModeDeletingTitle: {
		LangEnglish:            "Deleting Sessions...",
		LangChinese:            "正在删除会话...",
		LangTraditionalChinese: "正在刪除會話...",
		LangJapanese:           "セッションを削除中...",
		LangSpanish:            "Eliminando sesiones...",
	},
	MsgDeleteModeDeletingBody: {
		LangEnglish:            "Deleting %d session(s), please wait...",
		LangChinese:            "正在删除 %d 个会话，请稍候...",
		LangTraditionalChinese: "正在刪除 %d 個會話，請稍候...",
		LangJapanese:           "%d 件のセッションを削除中、お待ちください...",
		LangSpanish:            "Eliminando %d sesión(es), por favor espere...",
	},
	MsgDeleteModeMissingSession: {
		LangEnglish:            "❌ Missing selected session: %s",
		LangChinese:            "❌ 已选会话不存在：%s",
		LangTraditionalChinese: "❌ 已選會話不存在：%s",
		LangJapanese:           "❌ 選択したセッションが見つかりません: %s",
		LangSpanish:            "❌ Falta la sesión seleccionada: %s",
	},
	MsgBannedWordBlocked: {
		LangEnglish:            "⚠️ Your message was blocked because it contains a prohibited word.",
		LangChinese:            "⚠️ 消息已被拦截，包含违禁词。",
		LangTraditionalChinese: "⚠️ 訊息已被攔截，包含違禁詞。",
		LangJapanese:           "⚠️ 禁止ワードが含まれているため、メッセージがブロックされました。",
		LangSpanish:            "⚠️ Su mensaje fue bloqueado porque contiene una palabra prohibida.",
	},
	MsgCommandDisabled: {
		LangEnglish:            "🚫 Command `%s` is disabled for this project.",
		LangChinese:            "🚫 命令 `%s` 在当前项目中已被禁用。",
		LangTraditionalChinese: "🚫 命令 `%s` 在當前專案中已被停用。",
		LangJapanese:           "🚫 コマンド `%s` はこのプロジェクトで無効化されています。",
		LangSpanish:            "🚫 El comando `%s` está deshabilitado para este proyecto.",
	},
	MsgAdminRequired: {
		LangEnglish:            "🔒 Command `%s` requires admin privilege. Set `admin_from` in config to authorize users.",
		LangChinese:            "🔒 命令 `%s` 需要管理员权限。请在配置中设置 `admin_from` 来授权用户。",
		LangTraditionalChinese: "🔒 命令 `%s` 需要管理員權限。請在配置中設定 `admin_from` 來授權使用者。",
		LangJapanese:           "🔒 コマンド `%s` には管理者権限が必要です。設定で `admin_from` を設定してユーザーを承認してください。",
		LangSpanish:            "🔒 El comando `%s` requiere privilegios de administrador. Configure `admin_from` en la configuración.",
	},
	MsgRateLimited: {
		LangEnglish:            "⏳ You are sending messages too fast. Please wait a moment.",
		LangChinese:            "⏳ 消息发送过快，请稍后再试。",
		LangTraditionalChinese: "⏳ 訊息發送過快，請稍後再試。",
		LangJapanese:           "⏳ メッセージの送信が速すぎます。しばらくお待ちください。",
		LangSpanish:            "⏳ Estás enviando mensajes demasiado rápido. Espera un momento.",
	},
	MsgPsSent: {
		LangEnglish:            "✅ P.S. delivered.",
		LangChinese:            "✅ P.S. 已送达。",
		LangTraditionalChinese: "✅ P.S. 已送達。",
		LangJapanese:           "✅ P.S. を送信しました。",
		LangSpanish:            "✅ P.S. entregado.",
	},
	MsgPsSendFailed: {
		LangEnglish:            "❌ Failed to deliver P.S.",
		LangChinese:            "❌ P.S. 发送失败。",
		LangTraditionalChinese: "❌ P.S. 傳送失敗。",
		LangJapanese:           "❌ P.S. の送信に失敗しました。",
		LangSpanish:            "❌ Error al entregar el P.S.",
	},
	MsgPsEmpty: {
		LangEnglish:            "Usage: `/ps <message>`",
		LangChinese:            "用法：`/ps <消息>`",
		LangTraditionalChinese: "用法：`/ps <訊息>`",
		LangJapanese:           "使い方：`/ps <メッセージ>`",
		LangSpanish:            "Uso: `/ps <mensaje>`",
	},
	MsgPsNoSession: {
		LangEnglish:            "No task is currently running.",
		LangChinese:            "当前没有正在执行的任务。",
		LangTraditionalChinese: "目前沒有正在執行的任務。",
		LangJapanese:           "現在実行中のタスクはありません。",
		LangSpanish:            "No hay ninguna tarea en ejecución.",
	},
	MsgWhoamiTitle: {
		LangEnglish:            "🪪 **Your Identity**",
		LangChinese:            "🪪 **你的身份信息**",
		LangTraditionalChinese: "🪪 **你的身分資訊**",
		LangJapanese:           "🪪 **あなたの身元情報**",
		LangSpanish:            "🪪 **Tu identidad**",
	},
	MsgWhoamiCardTitle: {
		LangEnglish:            "Your Identity",
		LangChinese:            "你的身份信息",
		LangTraditionalChinese: "你的身分資訊",
		LangJapanese:           "あなたの身元情報",
		LangSpanish:            "Tu identidad",
	},
	MsgWhoamiName: {
		LangEnglish:            "Name",
		LangChinese:            "名称",
		LangTraditionalChinese: "名稱",
		LangJapanese:           "名前",
		LangSpanish:            "Nombre",
	},
	MsgWhoamiPlatform: {
		LangEnglish:            "Platform",
		LangChinese:            "平台",
		LangTraditionalChinese: "平台",
		LangJapanese:           "プラットフォーム",
		LangSpanish:            "Plataforma",
	},
	MsgWhoamiUsage: {
		LangEnglish:            "💡 Use the `User ID` above for `allow_from` and `admin_from` in your `config.toml`.",
		LangChinese:            "💡 可将上方 `User ID` 填入 `config.toml` 的 `allow_from` 或 `admin_from` 中。",
		LangTraditionalChinese: "💡 可將上方 `User ID` 填入 `config.toml` 的 `allow_from` 或 `admin_from` 中。",
		LangJapanese:           "💡 上記の `User ID` を `config.toml` の `allow_from` や `admin_from` に設定してください。",
		LangSpanish:            "💡 Usa el `User ID` de arriba para `allow_from` y `admin_from` en tu `config.toml`.",
	},
	MsgRelayNoBinding: {
		LangEnglish: "No relay binding in this chat.\nUse `/bind <project>` to bind another bot.\nThe <project> is the project name from your config.toml.",
		LangChinese: "当前群聊没有中继绑定。\n使用 `/bind <项目名>` 绑定另一个机器人。\n<项目名> 是 config.toml 中 [[projects]] 的 name 字段。",
	},
	MsgRelayBound: {
		LangEnglish: "Current relay binding: %s",
		LangChinese: "当前中继绑定: %s",
	},
	MsgRelayUsage: {
		LangEnglish: "Usage:\n  /bind <project>  — bind with another bot in this group\n  /bind remove     — remove binding\n  /bind            — show current binding\n\n<project> is the project name from config.toml [[projects]].",
		LangChinese: "用法:\n  /bind <项目名>  — 绑定群聊中的另一个机器人\n  /bind remove    — 解除绑定\n  /bind           — 查看当前绑定\n\n<项目名> 是 config.toml 中 [[projects]] 的 name 字段。",
	},
	MsgRelayNotAvailable: {
		LangEnglish: "Relay is not available. Make sure you have multiple projects configured.",
		LangChinese: "中继功能不可用。请确保配置了多个项目。",
	},
	MsgRelayUnbound: {
		LangEnglish: "Relay binding removed.",
		LangChinese: "中继绑定已解除。",
	},
	MsgRelayBindSelf: {
		LangEnglish: "Cannot bind to yourself. Specify a different project.",
		LangChinese: "不能绑定自己，请指定另一个项目。",
	},
	MsgRelayNotFound: {
		LangEnglish: "Project %q not found. Available projects: %s",
		LangChinese: "项目 %q 不存在。可用的项目: %s",
	},
	MsgRelayNoTarget: {
		LangEnglish: "Project %q not found. No other projects are configured.",
		LangChinese: "项目 %q 不存在。没有配置其他项目。",
	},
	MsgRelayBindRemoved: {
		LangEnglish:            "✅ Removed %s from binding",
		LangChinese:            "✅ 已从绑定中移除 %s",
		LangTraditionalChinese: "✅ 已從綁定中移除 %s",
		LangJapanese:           "✅ %s をバインドから削除しました",
		LangSpanish:            "✅ Eliminado %s del enlace",
	},
	MsgRelayBindNotFound: {
		LangEnglish:            "❌ %s is not bound or binding does not exist",
		LangChinese:            "❌ %s 未绑定或绑定不存在",
		LangTraditionalChinese: "❌ %s 未綁定或綁定不存在",
		LangJapanese:           "❌ %s はバインドされていないか、バインドが存在しません",
		LangSpanish:            "❌ %s no está vinculado o el enlace no existe",
	},
	MsgRelayBindSuccess: {
		LangEnglish:            "✅ Bind successful! Current group bound: %s\n\nYou can now ask this bot to communicate with %s.\nExample: \"Ask %s about ...\"",
		LangChinese:            "✅ 绑定成功！当前群组已绑定: %s\n\n你现在可以让本机器人去询问 %s。\n示例：\"帮我问一下 %s ...\"",
		LangTraditionalChinese: "✅ 綁定成功！當前群組已綁定: %s\n\n你現在可以讓本機器人去詢問 %s。\n示例：\"幫我問一下 %s ...\"",
		LangJapanese:           "✅ バインド成功！現在のグループ: %s\n\nこのボットに %s への問い合わせを依頼できます。\n例：「%s に...を聞いて」",
		LangSpanish:            "✅ ¡Enlace exitoso! Grupo actual: %s\n\nAhora puede pedir a este bot que consulte a %s.\nEjemplo: \"Pregunta a %s sobre ...\"",
	},
	MsgRelaySetupHint: {
		LangEnglish:            "\n\n⚠️ This agent does not auto-inject cc-connect instructions.\nPlease run `/bind setup` or `/cron setup` to write instructions to %s.",
		LangChinese:            "\n\n⚠️ 当前 agent 不会自动注入 cc-connect 指令。\n请运行 `/bind setup` 或 `/cron setup` 将指令写入 %s。",
		LangTraditionalChinese: "\n\n⚠️ 當前 agent 不會自動注入 cc-connect 指令。\n請執行 `/bind setup` 或 `/cron setup` 將指令寫入 %s。",
		LangJapanese:           "\n\n⚠️ このエージェントは cc-connect の指示を自動注入しません。\n`/bind setup` または `/cron setup` を実行して %s に指示を書き込んでください。",
		LangSpanish:            "\n\n⚠️ Este agente no inyecta automáticamente las instrucciones de cc-connect.\nEjecute `/bind setup` o `/cron setup` para escribirlas en %s.",
	},
	MsgRelaySetupOK: {
		LangEnglish:            "✅ cc-connect instructions written to %s\nThe agent can now use relay, cron, and attachment send-back.",
		LangChinese:            "✅ cc-connect 指令已写入 %s\nagent 现在可以使用中继、定时任务和附件回传功能了。",
		LangTraditionalChinese: "✅ cc-connect 指令已寫入 %s\nagent 現在可以使用中繼、定時任務和附件回傳功能了。",
		LangJapanese:           "✅ cc-connect の指示を %s に書き込みました。\nエージェントがリレー、cron、添付ファイル返送を使えるようになりました。",
		LangSpanish:            "✅ Instrucciones de cc-connect escritas en %s\nEl agente ahora puede usar relay, cron y reenvío de adjuntos.",
	},
	MsgRelaySetupExists: {
		LangEnglish:            "ℹ️ cc-connect instructions already exist in %s — no changes made.",
		LangChinese:            "ℹ️ cc-connect 指令已存在于 %s 中，无需重复写入。",
		LangTraditionalChinese: "ℹ️ cc-connect 指令已存在於 %s 中，無需重複寫入。",
		LangJapanese:           "ℹ️ cc-connect の指示は既に %s に存在します。変更はありません。",
		LangSpanish:            "ℹ️ Las instrucciones de cc-connect ya existen en %s — sin cambios.",
	},
	MsgRelaySetupNoMemory: {
		LangEnglish:            "❌ This agent does not support instruction files.",
		LangChinese:            "❌ 当前 agent 不支持指令文件。",
		LangTraditionalChinese: "❌ 當前 agent 不支持指令檔案。",
		LangJapanese:           "❌ このエージェントは指示ファイルをサポートしていません。",
		LangSpanish:            "❌ Este agente no soporta archivos de instrucciones.",
	},
	MsgSetupNative: {
		LangEnglish:            "✅ This agent natively supports cc-connect instructions — no setup needed.",
		LangChinese:            "✅ 当前 agent 已原生支持 cc-connect 指令，无需额外配置。",
		LangTraditionalChinese: "✅ 當前 agent 已原生支持 cc-connect 指令，無需額外配置。",
		LangJapanese:           "✅ このエージェントは cc-connect の指示をネイティブサポートしています。セットアップ不要です。",
		LangSpanish:            "✅ Este agente soporta nativamente las instrucciones de cc-connect — no se necesita configuración.",
	},
	MsgCronSetupOK: {
		LangEnglish:            "✅ cc-connect instructions written to %s\nThe agent can now use relay, cron, and attachment send-back.",
		LangChinese:            "✅ cc-connect 指令已写入 %s\nagent 现在可以使用中继、定时任务和附件回传功能了。",
		LangTraditionalChinese: "✅ cc-connect 指令已寫入 %s\nagent 現在可以使用中繼、定時任務和附件回傳功能了。",
		LangJapanese:           "✅ cc-connect の指示を %s に書き込みました。\nエージェントがリレー、cron、添付ファイル返送を使えるようになりました。",
		LangSpanish:            "✅ Instrucciones de cc-connect escritas en %s\nEl agente ahora puede usar relay, cron y reenvío de adjuntos.",
	},
	MsgSearchUsage: {
		LangEnglish:            "Usage: /search <keyword>\nSearch sessions by name or ID.",
		LangChinese:            "用法: /search <关键词>\n搜索会话名称或 ID。",
		LangTraditionalChinese: "用法: /search <關鍵詞>\n搜尋會話名稱或 ID。",
		LangJapanese:           "使い方: /search <キーワード>\nセッション名またはIDで検索。",
		LangSpanish:            "Uso: /search <palabra_clave>\nBuscar sesiones por nombre o ID.",
	},
	MsgSearchError: {
		LangEnglish:            "❌ Search error: %v",
		LangChinese:            "❌ 搜索失败: %v",
		LangTraditionalChinese: "❌ 搜尋失敗: %v",
		LangJapanese:           "❌ 検索エラー: %v",
		LangSpanish:            "❌ Error de búsqueda: %v",
	},
	MsgSearchNoResult: {
		LangEnglish:            "No sessions found matching %q",
		LangChinese:            "没有找到匹配 %q 的会话",
		LangTraditionalChinese: "沒有找到匹配 %q 的會話",
		LangJapanese:           "%q に一致するセッションが見つかりません",
		LangSpanish:            "No se encontraron sesiones que coincidan con %q",
	},
	MsgSearchResult: {
		LangEnglish:            "🔍 Found %d session(s) matching %q:",
		LangChinese:            "🔍 找到 %d 个匹配 %q 的会话:",
		LangTraditionalChinese: "🔍 找到 %d 個匹配 %q 的會話:",
		LangJapanese:           "🔍 %q に一致する %d 件のセッション:",
		LangSpanish:            "🔍 Se encontraron %d sesiones que coinciden con %q:",
	},
	MsgSearchHint: {
		LangEnglish:            "Use /switch <id> to switch to a session.",
		LangChinese:            "使用 /switch <id> 切换到对应会话。",
		LangTraditionalChinese: "使用 /switch <id> 切換到對應會話。",
		LangJapanese:           "/switch <id> でセッションを切り替え。",
		LangSpanish:            "Usa /switch <id> para cambiar a una sesión.",
	},
	// Builtin command descriptions
	MsgBuiltinCmdNew: {
		LangEnglish:            "Start a new session, arg: [name]",
		LangChinese:            "创建新会话，参数: [名称]",
		LangTraditionalChinese: "建立新會話，參數: [名稱]",
		LangJapanese:           "新しいセッションを開始、引数: [名前]",
		LangSpanish:            "Iniciar una nueva sesión, arg: [nombre]",
	},
	MsgBuiltinCmdList: {
		LangEnglish:            "List agent sessions",
		LangChinese:            "列出 Agent 会话列表",
		LangTraditionalChinese: "列出 Agent 會話列表",
		LangJapanese:           "エージェントセッション一覧",
		LangSpanish:            "Listar sesiones del agente",
	},
	MsgBuiltinCmdSearch: {
		LangEnglish:            "Search sessions by name or ID, arg: <keyword>",
		LangChinese:            "搜索会话名称或 ID，参数: <关键词>",
		LangTraditionalChinese: "搜尋會話名稱或 ID，參數: <關鍵詞>",
		LangJapanese:           "セッションを名前またはIDで検索、引数: <キーワード>",
		LangSpanish:            "Buscar sesiones por nombre o ID, arg: <palabra_clave>",
	},
	MsgBuiltinCmdSwitch: {
		LangEnglish:            "Resume a session by its list number, arg: <number>",
		LangChinese:            "按列表序号切换会话，参数: <序号>",
		LangTraditionalChinese: "按列表序號切換會話，參數: <序號>",
		LangJapanese:           "リスト番号でセッションを切り替え、引数: <番号>",
		LangSpanish:            "Reanudar sesión por su número en la lista, arg: <número>",
	},
	MsgBuiltinCmdDelete: {
		LangEnglish:            "Delete session(s) by list number, args: <number> | 1,2,3 | 3-7 | 1,3-5,8",
		LangChinese:            "按列表序号删除会话，参数: <序号> | 1,2,3 | 3-7 | 1,3-5,8",
		LangTraditionalChinese: "按列表序號刪除會話，參數: <序號> | 1,2,3 | 3-7 | 1,3-5,8",
		LangJapanese:           "リスト番号でセッションを削除、引数: <番号> | 1,2,3 | 3-7 | 1,3-5,8",
		LangSpanish:            "Eliminar sesión(es) por número de lista, args: <número> | 1,2,3 | 3-7 | 1,3-5,8",
	},
	MsgBuiltinCmdName: {
		LangEnglish:            "Name a session for easy identification, arg: [number] <text>",
		LangChinese:            "给会话命名，方便识别，参数: [序号] <名称>",
		LangTraditionalChinese: "為會話命名，方便辨識，參數: [序號] <名稱>",
		LangJapanese:           "セッションに名前を付ける、引数: [番号] <名前>",
		LangSpanish:            "Nombrar una sesión para fácil identificación, arg: [número] <texto>",
	},
	MsgBuiltinCmdCurrent: {
		LangEnglish:            "Show current active session",
		LangChinese:            "查看当前活跃会话",
		LangTraditionalChinese: "查看當前活躍會話",
		LangJapanese:           "現在のアクティブセッションを表示",
		LangSpanish:            "Mostrar sesión activa actual",
	},
	MsgBuiltinCmdHistory: {
		LangEnglish:            "Show last n messages, arg: [n] (default 10)",
		LangChinese:            "查看最近 n 条消息，参数: [n]（默认 10）",
		LangTraditionalChinese: "查看最近 n 條訊息，參數: [n]（預設 10）",
		LangJapanese:           "直近 n 件のメッセージを表示、引数: [n]（デフォルト 10）",
		LangSpanish:            "Mostrar últimos n mensajes, arg: [n] (por defecto 10)",
	},
	MsgBuiltinCmdProvider: {
		LangEnglish:            "Manage API providers, arg: [list|add|remove|switch|clear]",
		LangChinese:            "管理 API Provider，参数: [list|add|remove|switch|clear]",
		LangTraditionalChinese: "管理 API Provider，參數: [list|add|remove|switch|clear]",
		LangJapanese:           "API プロバイダ管理、引数: [list|add|remove|switch|clear]",
		LangSpanish:            "Gestionar proveedores API, arg: [list|add|remove|switch|clear]",
	},
	MsgBuiltinCmdMemory: {
		LangEnglish:            "View/edit agent memory files, arg: [add|global|global add]",
		LangChinese:            "查看/编辑 Agent 记忆文件，参数: [add|global|global add]",
		LangTraditionalChinese: "查看/編輯 Agent 記憶檔案，參數: [add|global|global add]",
		LangJapanese:           "エージェントメモリの表示/編集、引数: [add|global|global add]",
		LangSpanish:            "Ver/editar archivos de memoria del agente, arg: [add|global|global add]",
	},
	MsgBuiltinCmdAllow: {
		LangEnglish:            "Pre-allow a tool (next session), arg: <tool>",
		LangChinese:            "预授权工具（下次会话生效），参数: <工具名>",
		LangTraditionalChinese: "預授權工具（下次會話生效），參數: <工具名>",
		LangJapanese:           "ツールを事前許可（次のセッションで有効）、引数: <ツール>",
		LangSpanish:            "Pre-autorizar herramienta (próxima sesión), arg: <herramienta>",
	},
	MsgBuiltinCmdModel: {
		LangEnglish:            "View/switch model, arg: [name]",
		LangChinese:            "查看/切换模型，参数: [名称]",
		LangTraditionalChinese: "查看/切換模型，參數: [名稱]",
		LangJapanese:           "モデルの表示/切り替え、引数: [名前]",
		LangSpanish:            "Ver/cambiar modelo, arg: [nombre]",
	},
	MsgBuiltinCmdReasoning: {
		LangEnglish:            "View/switch reasoning effort, arg: [level]",
		LangChinese:            "查看/切换推理强度，参数: [等级]",
		LangTraditionalChinese: "查看/切換推理強度，參數: [等級]",
		LangJapanese:           "推論強度の表示/切り替え、引数: [レベル]",
		LangSpanish:            "Ver/cambiar esfuerzo de razonamiento, arg: [nivel]",
	},
	MsgBuiltinCmdMode: {
		LangEnglish:            "View/switch permission mode, arg: [name]",
		LangChinese:            "查看/切换权限模式，参数: [名称]",
		LangTraditionalChinese: "查看/切換權限模式，參數: [名稱]",
		LangJapanese:           "権限モードの表示/切り替え、引数: [名前]",
		LangSpanish:            "Ver/cambiar modo de permisos, arg: [nombre]",
	},
	MsgBuiltinCmdLang: {
		LangEnglish:            "View/switch language, arg: [en|zh|zh-TW|ja|es|auto]",
		LangChinese:            "查看/切换语言，参数: [en|zh|zh-TW|ja|es|auto]",
		LangTraditionalChinese: "查看/切換語言，參數: [en|zh|zh-TW|ja|es|auto]",
		LangJapanese:           "言語の表示/切り替え、引数: [en|zh|zh-TW|ja|es|auto]",
		LangSpanish:            "Ver/cambiar idioma, arg: [en|zh|zh-TW|ja|es|auto]",
	},
	MsgBuiltinCmdQuiet: {
		LangEnglish:            "Toggle thinking/tool progress, arg: [global]",
		LangChinese:            "开关思考和工具进度消息, 参数: [global]",
		LangTraditionalChinese: "開關思考和工具進度訊息, 參數: [global]",
		LangJapanese:           "思考/ツール進捗メッセージの表示切替, 引数: [global]",
		LangSpanish:            "Alternar mensajes de progreso, arg: [global]",
	},
	MsgBuiltinCmdCompress: {
		LangEnglish:            "Compress conversation context",
		LangChinese:            "压缩会话上下文",
		LangTraditionalChinese: "壓縮會話上下文",
		LangJapanese:           "会話コンテキストを圧縮",
		LangSpanish:            "Comprimir contexto de conversación",
	},
	MsgBuiltinCmdStop: {
		LangEnglish:            "Stop current execution",
		LangChinese:            "停止当前执行",
		LangTraditionalChinese: "停止當前執行",
		LangJapanese:           "現在の実行を停止",
		LangSpanish:            "Detener ejecución actual",
	},
	MsgBuiltinCmdCron: {
		LangEnglish:            "Manage scheduled tasks, arg: [add|list|exec|del|enable|disable]",
		LangChinese:            "管理定时任务，参数: [add|list|exec|del|enable|disable]",
		LangTraditionalChinese: "管理定時任務，參數: [add|list|exec|del|enable|disable]",
		LangJapanese:           "スケジュールタスク管理、引数: [add|list|exec|del|enable|disable]",
		LangSpanish:            "Gestionar tareas programadas, arg: [add|list|exec|del|enable|disable]",
	},
	MsgBuiltinCmdCommands: {
		LangEnglish:            "Manage custom slash commands, arg: [add|del]",
		LangChinese:            "管理自定义命令，参数: [add|del]",
		LangTraditionalChinese: "管理自訂命令，參數: [add|del]",
		LangJapanese:           "カスタムコマンド管理、引数: [add|del]",
		LangSpanish:            "Gestionar comandos personalizados, arg: [add|del]",
	},
	MsgBuiltinCmdAlias: {
		LangEnglish:            "Manage command aliases, arg: [add|del]",
		LangChinese:            "管理命令别名，参数: [add|del]",
		LangTraditionalChinese: "管理命令別名，參數: [add|del]",
		LangJapanese:           "コマンドエイリアス管理、引数: [add|del]",
		LangSpanish:            "Gestionar alias de comandos, arg: [add|del]",
	},
	MsgBuiltinCmdSkills: {
		LangEnglish:            "List agent skills (from SKILL.md)",
		LangChinese:            "列出 Agent Skills（来自 SKILL.md）",
		LangTraditionalChinese: "列出 Agent Skills（來自 SKILL.md）",
		LangJapanese:           "エージェントスキル一覧（SKILL.md から）",
		LangSpanish:            "Listar skills del agente (desde SKILL.md)",
	},
	MsgBuiltinCmdConfig: {
		LangEnglish:            "View/update runtime configuration, arg: [get|set|reload] [key] [value]",
		LangChinese:            "查看/修改运行时配置，参数: [get|set|reload] [键] [值]",
		LangTraditionalChinese: "查看/修改執行階段配置，參數: [get|set|reload] [鍵] [值]",
		LangJapanese:           "ランタイム設定の表示/変更、引数: [get|set|reload] [キー] [値]",
		LangSpanish:            "Ver/actualizar configuración en tiempo de ejecución, arg: [get|set|reload] [clave] [valor]",
	},
	MsgBuiltinCmdDoctor: {
		LangEnglish:            "Run system diagnostics",
		LangChinese:            "运行系统诊断",
		LangTraditionalChinese: "執行系統診斷",
		LangJapanese:           "システム診断を実行",
		LangSpanish:            "Ejecutar diagnósticos del sistema",
	},
	MsgBuiltinCmdUpgrade: {
		LangEnglish:            "Check for updates and self-update",
		LangChinese:            "检查更新并自动升级",
		LangTraditionalChinese: "檢查更新並自動升級",
		LangJapanese:           "アップデートを確認して自動更新",
		LangSpanish:            "Buscar actualizaciones y auto-actualizar",
	},
	MsgBuiltinCmdRestart: {
		LangEnglish:            "Restart cc-connect service",
		LangChinese:            "重启 cc-connect 服务",
		LangTraditionalChinese: "重啟 cc-connect 服務",
		LangJapanese:           "cc-connect サービスを再起動",
		LangSpanish:            "Reiniciar el servicio cc-connect",
	},
	MsgBuiltinCmdStatus: {
		LangEnglish:            "Show system status",
		LangChinese:            "查看系统状态",
		LangTraditionalChinese: "查看系統狀態",
		LangJapanese:           "システム状態を表示",
		LangSpanish:            "Mostrar estado del sistema",
	},
	MsgBuiltinCmdUsage: {
		LangEnglish:            "Show account/model quota usage",
		LangChinese:            "查看账号/模型限额使用情况",
		LangTraditionalChinese: "查看帳號/模型限額使用情況",
		LangJapanese:           "アカウント/モデル使用量を表示",
		LangSpanish:            "Mostrar uso de cuota de cuenta/modelo",
	},
	MsgBuiltinCmdVersion: {
		LangEnglish:            "Show cc-connect version",
		LangChinese:            "查看 cc-connect 版本",
		LangTraditionalChinese: "查看 cc-connect 版本",
		LangJapanese:           "cc-connect のバージョンを表示",
		LangSpanish:            "Mostrar versión de cc-connect",
	},
	MsgBuiltinCmdHelp: {
		LangEnglish:            "Show this help",
		LangChinese:            "显示此帮助",
		LangTraditionalChinese: "顯示此說明",
		LangJapanese:           "このヘルプを表示",
		LangSpanish:            "Mostrar esta ayuda",
	},
	MsgBuiltinCmdBind: {
		LangEnglish:            "Bind current session to a target, arg: <target>",
		LangChinese:            "绑定当前会话到目标，参数: <目标>",
		LangTraditionalChinese: "綁定當前會話到目標，參數: <目標>",
		LangJapanese:           "現在のセッションをターゲットにバインド、引数: <ターゲット>",
		LangSpanish:            "Vincular sesión actual a un objetivo, arg: <objetivo>",
	},
	MsgBuiltinCmdShell: {
		LangEnglish:            "Run a shell command, arg: <command>",
		LangChinese:            "执行 Shell 命令，参数: <命令>",
		LangTraditionalChinese: "執行 Shell 命令，參數: <命令>",
		LangJapanese:           "シェルコマンドを実行、引数: <コマンド>",
		LangSpanish:            "Ejecutar un comando shell, arg: <comando>",
	},
	MsgBuiltinCmdDir: {
		LangEnglish:            "Show, switch, or reset agent working directory, arg: <path>",
		LangChinese:            "查看、切换或重置 Agent 工作目录，参数: <路径>",
		LangTraditionalChinese: "查看、切換或重置 Agent 工作目錄，參數: <路徑>",
		LangJapanese:           "エージェントの作業ディレクトリを表示/変更/リセット、引数: <パス>",
		LangSpanish:            "Ver, cambiar o restablecer el directorio de trabajo del agente, arg: <ruta>",
	},
	MsgBuiltinCmdDiff: {
		LangEnglish:            "Generate git diff as HTML file, arg: [target]",
		LangChinese:            "生成 git diff 并以 HTML 文件发送，参数: [目标]",
		LangTraditionalChinese: "產生 git diff 並以 HTML 檔案傳送，參數: [目標]",
		LangJapanese:           "git diff を HTML ファイルで生成、引数: [ターゲット]",
		LangSpanish:            "Generar git diff como archivo HTML, arg: [objetivo]",
	},
	MsgBuiltinCmdPs: {
		LangEnglish:            "Send a P.S. to the running task",
		LangChinese:            "向正在执行的任务追加补充信息",
		LangTraditionalChinese: "向正在執行的任務追加補充資訊",
		LangJapanese:           "実行中のタスクに補足情報を送信",
		LangSpanish:            "Enviar un P.S. a la tarea en curso",
	},
	MsgDiffEmpty: {
		LangEnglish:            "No diff — clean working tree (or no changes vs `%s`).",
		LangChinese:            "无差异 — 工作区干净（或与 `%s` 无变化）。",
		LangTraditionalChinese: "無差異 — 工作區乾淨（或與 `%s` 無變化）。",
		LangJapanese:           "差分なし — 作業ツリーはクリーン（または `%s` との差分なし）。",
		LangSpanish:            "Sin diferencias — árbol limpio (o sin cambios vs `%s`).",
	},
	MsgDiffNoDiff2HTML: {
		LangEnglish:            "`diff2html` is not installed, sending plain text diff.\nInstall: `npm install -g diff2html-cli`",
		LangChinese:            "未安装 `diff2html`，将以纯文本发送差异。\n安装命令: `npm install -g diff2html-cli`",
		LangTraditionalChinese: "未安裝 `diff2html`，將以純文字傳送差異。\n安裝指令: `npm install -g diff2html-cli`",
		LangJapanese:           "`diff2html` がインストールされていません。プレーンテキストで差分を送信します。\nインストール: `npm install -g diff2html-cli`",
		LangSpanish:            "`diff2html` no está instalado, enviando diff en texto plano.\nInstalar: `npm install -g diff2html-cli`",
	},
	MsgDirChanged: {
		LangEnglish:            "✅ Work directory changed to: `%s`\nThe next session will start in this directory.",
		LangChinese:            "✅ 工作目录已切换为: `%s`\n下次会话将在此目录下启动。",
		LangTraditionalChinese: "✅ 工作目錄已切換為: `%s`\n下次會話將在此目錄下啟動。",
		LangJapanese:           "✅ 作業ディレクトリを変更しました: `%s`\n次のセッションはこのディレクトリで起動します。",
		LangSpanish:            "✅ Directorio de trabajo cambiado a: `%s`\nLa próxima sesión iniciará en este directorio.",
	},
	MsgDirCurrent: {
		LangEnglish:            "📂 Current work directory: `%s`",
		LangChinese:            "📂 当前工作目录: `%s`",
		LangTraditionalChinese: "📂 當前工作目錄: `%s`",
		LangJapanese:           "📂 現在の作業ディレクトリ: `%s`",
		LangSpanish:            "📂 Directorio de trabajo actual: `%s`",
	},
	MsgDirReset: {
		LangEnglish:            "✅ Work directory reset to the configured default: `%s`",
		LangChinese:            "✅ 工作目录已重置为配置的默认目录: `%s`",
		LangTraditionalChinese: "✅ 工作目錄已重置為設定的預設目錄: `%s`",
		LangJapanese:           "✅ 作業ディレクトリを設定済みのデフォルトに戻しました: `%s`",
		LangSpanish:            "✅ El directorio de trabajo se restauró al valor predeterminado configurado: `%s`",
	},
	MsgDirUsage: {
		LangEnglish:            "Usage: `/dir <path>`\n       `/dir reset`\nExample: `/dir ../project`",
		LangChinese:            "用法: `/dir <路径>`\n      `/dir reset`\n示例: `/dir ../project`",
		LangTraditionalChinese: "用法: `/dir <路徑>`\n      `/dir reset`\n範例: `/dir ../project`",
		LangJapanese:           "使い方: `/dir <パス>`\n       `/dir reset`\n例: `/dir ../project`",
		LangSpanish:            "Uso: `/dir <ruta>`\n      `/dir reset`\nEjemplo: `/dir ../project`",
	},
	MsgDirNotSupported: {
		LangEnglish:            "This agent does not support dynamic work directory switching.",
		LangChinese:            "当前 Agent 不支持动态切换工作目录。",
		LangTraditionalChinese: "當前 Agent 不支援動態切換工作目錄。",
		LangJapanese:           "このエージェントは動的な作業ディレクトリの切り替えをサポートしていません。",
		LangSpanish:            "Este agente no soporta el cambio dinámico de directorio de trabajo.",
	},
	MsgDirInvalidPath: {
		LangEnglish:            "❌ Directory does not exist: `%s`",
		LangChinese:            "❌ 目录不存在: `%s`",
		LangTraditionalChinese: "❌ 目錄不存在: `%s`",
		LangJapanese:           "❌ ディレクトリが存在しません: `%s`",
		LangSpanish:            "❌ El directorio no existe: `%s`",
	},
	MsgDirHistoryTitle: {
		LangEnglish:            "📋 History:",
		LangChinese:            "📋 历史记录:",
		LangTraditionalChinese: "📋 歷史記錄:",
		LangJapanese:           "📋 履歴:",
		LangSpanish:            "📋 Historial:",
	},
	MsgDirHistoryHint: {
		LangEnglish:            "💡 Use `/dir <number>` to switch, or `/dir -` for previous.",
		LangChinese:            "💡 使用 `/dir <序号>` 切换，或 `/dir -` 返回上一个目录。",
		LangTraditionalChinese: "💡 使用 `/dir <序號>` 切換，或 `/dir -` 返回上一個目錄。",
		LangJapanese:           "💡 `/dir <番号>` で切り替え、`/dir -` で前のディレクトリに戻ります。",
		LangSpanish:            "💡 Usa `/dir <número>` para cambiar, o `/dir -` para el anterior.",
	},
	MsgDirInvalidIndex: {
		LangEnglish:            "❌ Invalid history index: %d",
		LangChinese:            "❌ 无效的历史序号: %d",
		LangTraditionalChinese: "❌ 無效的歷史序號: %d",
		LangJapanese:           "❌ 無効な履歴番号: %d",
		LangSpanish:            "❌ Índice de historial inválido: %d",
	},
	MsgDirNoHistory: {
		LangEnglish:            "❌ No directory history available.",
		LangChinese:            "❌ 暂无目录历史记录。",
		LangTraditionalChinese: "❌ 暫無目錄歷史記錄。",
		LangJapanese:           "❌ ディレクトリの履歴がありません。",
		LangSpanish:            "❌ No hay historial de directorios.",
	},
	MsgDirNoPrevious: {
		LangEnglish:            "❌ No previous directory in history.",
		LangChinese:            "❌ 没有上一个目录记录。",
		LangTraditionalChinese: "❌ 沒有上一個目錄記錄。",
		LangJapanese:           "❌ 前のディレクトリが履歴にありません。",
		LangSpanish:            "❌ No hay directorio anterior en el historial.",
	},
	MsgDirCardTitle: {
		LangEnglish:            "Working directory",
		LangChinese:            "工作目录",
		LangTraditionalChinese: "工作目錄",
		LangJapanese:           "作業ディレクトリ",
		LangSpanish:            "Directorio de trabajo",
	},
	MsgDirCardPageHint: {
		LangEnglish:            "Page %d/%d — use `/dir <page>` or the buttons below.",
		LangChinese:            "第 %d/%d 页 — 可用 `/dir <页码>` 或下方按钮翻页。",
		LangTraditionalChinese: "第 %d/%d 頁 — 可用 `/dir <頁碼>` 或下方按鈕翻頁。",
		LangJapanese:           "%d/%d ページ — `/dir <ページ>` または下のボタンで移動。",
		LangSpanish:            "Página %d/%d — usa `/dir <página>` o los botones.",
	},
	MsgDirCardEmptyHistory: {
		LangEnglish:            "No directory history yet. Type `/dir <path>` to switch, or use **Reset** to restore the default.",
		LangChinese:            "暂无目录历史。可发送 `/dir <路径>` 切换，或点 **重置** 恢复默认目录。",
		LangTraditionalChinese: "暫無目錄歷史。可傳送 `/dir <路徑>` 切換，或點 **重置** 恢復預設目錄。",
		LangJapanese:           "まだディレクトリ履歴がありません。`/dir <パス>` で切替えるか、**リセット** で既定に戻せます。",
		LangSpanish:            "Aún no hay historial de directorios. Usa `/dir <ruta>` o **Restablecer** al valor por defecto.",
	},
	MsgDirCardReset: {
		LangEnglish:            "Reset",
		LangChinese:            "重置",
		LangTraditionalChinese: "重置",
		LangJapanese:           "リセット",
		LangSpanish:            "Restablecer",
	},
	MsgDirCardPrev: {
		LangEnglish:            "Previous",
		LangChinese:            "上一目录",
		LangTraditionalChinese: "上一目錄",
		LangJapanese:           "前へ",
		LangSpanish:            "Anterior",
	},
	MsgShow: {
		LangEnglish:            "View file / directory / snippet by reference",
		LangChinese:            "按引用查看文件、目录或代码片段",
		LangTraditionalChinese: "按引用查看檔案、目錄或程式碼片段",
		LangJapanese:           "参照からファイル・ディレクトリ・コード断片を表示",
		LangSpanish:            "Ver archivo/directorio/fragmento por referencia",
	},
	MsgShowUsage: {
		LangEnglish:            "Usage: `/show <path|path:line|path:start-end|dir/>`\nExample: `/show svc/recovery_session_reconciler.go:12`",
		LangChinese:            "用法: `/show <路径|路径:行号|路径:起止行|目录/>`\n示例: `/show svc/recovery_session_reconciler.go:12`",
		LangTraditionalChinese: "用法: `/show <路徑|路徑:行號|路徑:起止行|目錄/>`\n範例: `/show svc/recovery_session_reconciler.go:12`",
		LangJapanese:           "使い方: `/show <パス|パス:行|パス:開始-終了|dir/>`\n例: `/show svc/recovery_session_reconciler.go:12`",
		LangSpanish:            "Uso: `/show <ruta|ruta:línea|ruta:inicio-fin|dir/>`\nEjemplo: `/show svc/recovery_session_reconciler.go:12`",
	},
	MsgShowParseError: {
		LangEnglish:            "❌ Cannot parse reference: `%s`",
		LangChinese:            "❌ 无法解析引用: `%s`",
		LangTraditionalChinese: "❌ 無法解析引用: `%s`",
		LangJapanese:           "❌ 参照を解析できません: `%s`",
		LangSpanish:            "❌ No se puede interpretar la referencia: `%s`",
	},
	MsgShowNotFound: {
		LangEnglish:            "❌ Referenced path does not exist: `%s`",
		LangChinese:            "❌ 引用路径不存在: `%s`",
		LangTraditionalChinese: "❌ 引用路徑不存在: `%s`",
		LangJapanese:           "❌ 参照パスが存在しません: `%s`",
		LangSpanish:            "❌ La ruta referenciada no existe: `%s`",
	},
	MsgShowDirWithLocation: {
		LangEnglish:            "❌ Directory references cannot include line information: `%s`",
		LangChinese:            "❌ 目录引用不能带行号信息: `%s`",
		LangTraditionalChinese: "❌ 目錄引用不能帶行號資訊: `%s`",
		LangJapanese:           "❌ ディレクトリ参照に行情報は指定できません: `%s`",
		LangSpanish:            "❌ Una referencia de directorio no puede incluir líneas: `%s`",
	},
	MsgShowReadFailed: {
		LangEnglish:            "❌ Failed to read reference: %s",
		LangChinese:            "❌ 读取引用失败: %s",
		LangTraditionalChinese: "❌ 讀取引用失敗: %s",
		LangJapanese:           "❌ 参照の読み取りに失敗しました: %s",
		LangSpanish:            "❌ Error al leer la referencia: %s",
	},

	// Multi-workspace messages
	MsgWsNotEnabled: {
		LangEnglish:            "Workspace commands are only available in multi-workspace mode.",
		LangChinese:            "工作区命令仅在多工作区模式下可用。",
		LangTraditionalChinese: "工作區命令僅在多工作區模式下可用。",
		LangJapanese:           "ワークスペースコマンドはマルチワークスペースモードでのみ使用できます。",
		LangSpanish:            "Los comandos de workspace solo están disponibles en modo multi-workspace.",
	},
	MsgWsNoBinding: {
		LangEnglish:            "No workspace bound to this channel.",
		LangChinese:            "此频道未绑定工作区。",
		LangTraditionalChinese: "此頻道未綁定工作區。",
		LangJapanese:           "このチャンネルにワークスペースがバインドされていません。",
		LangSpanish:            "No hay workspace vinculado a este canal.",
	},
	MsgWsInfo: {
		LangEnglish:            "Workspace: `%s`\nBound: %s",
		LangChinese:            "工作区: `%s`\n绑定时间: %s",
		LangTraditionalChinese: "工作區: `%s`\n綁定時間: %s",
		LangJapanese:           "ワークスペース: `%s`\nバインド: %s",
		LangSpanish:            "Workspace: `%s`\nVinculado: %s",
	},
	MsgWsInfoShared: {
		LangEnglish:            "Workspace: `%s`\nBound: %s\nSource: shared",
		LangChinese:            "工作区: `%s`\n绑定时间: %s\n来源: shared",
		LangTraditionalChinese: "工作區: `%s`\n綁定時間: %s\n來源: shared",
		LangJapanese:           "ワークスペース: `%s`\nバインド: %s\nソース: shared",
		LangSpanish:            "Workspace: `%s`\nVinculado: %s\nOrigen: shared",
	},
	MsgWsUsage: {
		LangEnglish:            "Usage: `/workspace [bind <name> | route <absolute-path> | init <url> | unbind | list | shared ...]`",
		LangChinese:            "用法: `/workspace [bind <名称> | route <绝对路径> | init <仓库地址> | unbind | list | shared ...]`",
		LangTraditionalChinese: "用法: `/workspace [bind <名稱> | route <絕對路徑> | init <倉庫地址> | unbind | list | shared ...]`",
		LangJapanese:           "使い方: `/workspace [bind <名前> | route <絶対パス> | init <url> | unbind | list | shared ...]`",
		LangSpanish:            "Uso: `/workspace [bind <nombre> | route <ruta-absoluta> | init <url> | unbind | list | shared ...]`",
	},
	MsgWsInitUsage: {
		LangEnglish:            "Usage: `/workspace init <git-url or directory-path>`",
		LangChinese:            "用法: `/workspace init <git仓库地址或目录路径>`",
		LangTraditionalChinese: "用法: `/workspace init <git倉庫地址或目錄路徑>`",
		LangJapanese:           "使い方: `/workspace init <git-urlまたはディレクトリパス>`",
		LangSpanish:            "Uso: `/workspace init <git-url o ruta-de-directorio>`",
	},
	MsgWsBindUsage: {
		LangEnglish:            "Usage: `/workspace bind <workspace-name>`",
		LangChinese:            "用法: `/workspace bind <工作区名称>`",
		LangTraditionalChinese: "用法: `/workspace bind <工作區名稱>`",
		LangJapanese:           "使い方: `/workspace bind <ワークスペース名>`",
		LangSpanish:            "Uso: `/workspace bind <nombre-workspace>`",
	},
	MsgWsBindSuccess: {
		LangEnglish:            "✅ Workspace bound: `%s`",
		LangChinese:            "✅ 工作区绑定成功: `%s`",
		LangTraditionalChinese: "✅ 工作區綁定成功: `%s`",
		LangJapanese:           "✅ ワークスペースをバインドしました: `%s`",
		LangSpanish:            "✅ Workspace vinculado: `%s`",
	},
	MsgWsBindNotFound: {
		LangEnglish:            "Workspace not found: `%s`",
		LangChinese:            "工作区不存在: `%s`",
		LangTraditionalChinese: "工作區不存在: `%s`",
		LangJapanese:           "ワークスペースが見つかりません: `%s`",
		LangSpanish:            "Workspace no encontrado: `%s`",
	},
	MsgWsRouteUsage: {
		LangEnglish:            "Usage: `/workspace route <absolute-path>`",
		LangChinese:            "用法: `/workspace route <绝对路径>`",
		LangTraditionalChinese: "用法: `/workspace route <絕對路徑>`",
		LangJapanese:           "使い方: `/workspace route <絶対パス>`",
		LangSpanish:            "Uso: `/workspace route <ruta-absoluta>`",
	},
	MsgWsRouteSuccess: {
		LangEnglish:            "✅ Workspace routed: `%s`",
		LangChinese:            "✅ 工作区路由成功: `%s`",
		LangTraditionalChinese: "✅ 工作區路由成功: `%s`",
		LangJapanese:           "✅ ワークスペースをルーティングしました: `%s`",
		LangSpanish:            "✅ Workspace enrutado: `%s`",
	},
	MsgWsRouteAbsoluteRequired: {
		LangEnglish:            "Workspace route must use an absolute path: `%s`",
		LangChinese:            "工作区路由必须使用绝对路径: `%s`",
		LangTraditionalChinese: "工作區路由必須使用絕對路徑: `%s`",
		LangJapanese:           "ワークスペースの route には絶対パスが必要です: `%s`",
		LangSpanish:            "La ruta del workspace debe ser absoluta: `%s`",
	},
	MsgWsRouteNotFound: {
		LangEnglish:            "Workspace path not found: `%s`",
		LangChinese:            "工作区路径不存在: `%s`",
		LangTraditionalChinese: "工作區路徑不存在: `%s`",
		LangJapanese:           "ワークスペースのパスが見つかりません: `%s`",
		LangSpanish:            "Ruta de workspace no encontrada: `%s`",
	},
	MsgWsRouteNotDirectory: {
		LangEnglish:            "Workspace route target is not a directory: `%s`",
		LangChinese:            "工作区路由目标不是目录: `%s`",
		LangTraditionalChinese: "工作區路由目標不是目錄: `%s`",
		LangJapanese:           "ワークスペースの route 先がディレクトリではありません: `%s`",
		LangSpanish:            "El destino de workspace route no es un directorio: `%s`",
	},
	MsgWsUnbindSuccess: {
		LangEnglish:            "✅ Workspace unbound.",
		LangChinese:            "✅ 已解除工作区绑定。",
		LangTraditionalChinese: "✅ 已解除工作區綁定。",
		LangJapanese:           "✅ ワークスペースのバインドを解除しました。",
		LangSpanish:            "✅ Workspace desvinculado.",
	},
	MsgWsListEmpty: {
		LangEnglish:            "No workspaces bound.",
		LangChinese:            "没有绑定的工作区。",
		LangTraditionalChinese: "沒有綁定的工作區。",
		LangJapanese:           "バインドされたワークスペースがありません。",
		LangSpanish:            "No hay workspaces vinculados.",
	},
	MsgWsListTitle: {
		LangEnglish:            "Bound workspaces:",
		LangChinese:            "已绑定的工作区：",
		LangTraditionalChinese: "已綁定的工作區：",
		LangJapanese:           "バインドされたワークスペース：",
		LangSpanish:            "Workspaces vinculados:",
	},
	MsgWsSharedNoBinding: {
		LangEnglish:            "No shared workspace bound to this channel.",
		LangChinese:            "此频道未绑定共享工作区。",
		LangTraditionalChinese: "此頻道未綁定共享工作區。",
		LangJapanese:           "このチャンネルに共有ワークスペースがバインドされていません。",
		LangSpanish:            "No hay workspace compartido vinculado a este canal.",
	},
	MsgWsSharedUsage: {
		LangEnglish:            "Usage: `/workspace shared [bind <name> | route <absolute-path> | init <url> | unbind | list]`",
		LangChinese:            "用法: `/workspace shared [bind <名称> | route <绝对路径> | init <仓库地址> | unbind | list]`",
		LangTraditionalChinese: "用法: `/workspace shared [bind <名稱> | route <絕對路徑> | init <倉庫地址> | unbind | list]`",
		LangJapanese:           "使い方: `/workspace shared [bind <名前> | route <絶対パス> | init <url> | unbind | list]`",
		LangSpanish:            "Uso: `/workspace shared [bind <nombre> | route <ruta-absoluta> | init <url> | unbind | list]`",
	},
	MsgWsSharedBindSuccess: {
		LangEnglish:            "✅ Shared workspace bound: `%s`",
		LangChinese:            "✅ 共享工作区绑定成功: `%s`",
		LangTraditionalChinese: "✅ 共享工作區綁定成功: `%s`",
		LangJapanese:           "✅ 共有ワークスペースをバインドしました: `%s`",
		LangSpanish:            "✅ Workspace compartido vinculado: `%s`",
	},
	MsgWsSharedRouteSuccess: {
		LangEnglish:            "✅ Shared workspace routed: `%s`",
		LangChinese:            "✅ 共享工作区路由成功: `%s`",
		LangTraditionalChinese: "✅ 共享工作區路由成功: `%s`",
		LangJapanese:           "✅ 共有ワークスペースをルーティングしました: `%s`",
		LangSpanish:            "✅ Workspace compartido enrutado: `%s`",
	},
	MsgWsSharedUnbindSuccess: {
		LangEnglish:            "✅ Shared workspace unbound.",
		LangChinese:            "✅ 已解除共享工作区绑定。",
		LangTraditionalChinese: "✅ 已解除共享工作區綁定。",
		LangJapanese:           "✅ 共有ワークスペースのバインドを解除しました。",
		LangSpanish:            "✅ Workspace compartido desvinculado.",
	},
	MsgWsSharedListEmpty: {
		LangEnglish:            "No shared workspaces bound.",
		LangChinese:            "没有绑定的共享工作区。",
		LangTraditionalChinese: "沒有綁定的共享工作區。",
		LangJapanese:           "バインドされた共有ワークスペースがありません。",
		LangSpanish:            "No hay workspaces compartidos vinculados.",
	},
	MsgWsSharedListTitle: {
		LangEnglish:            "Shared workspaces:",
		LangChinese:            "共享工作区：",
		LangTraditionalChinese: "共享工作區：",
		LangJapanese:           "共有ワークスペース：",
		LangSpanish:            "Workspaces compartidos:",
	},
	MsgWsSharedOnlyHint: {
		LangEnglish:            "The current effective workspace comes from the shared layer. Use `/workspace shared unbind` to remove it.",
		LangChinese:            "当前生效的工作区来自 shared 层。请使用 `/workspace shared unbind` 解除绑定。",
		LangTraditionalChinese: "當前生效的工作區來自 shared 層。請使用 `/workspace shared unbind` 解除綁定。",
		LangJapanese:           "現在有効なワークスペースは shared レイヤー由来です。解除するには `/workspace shared unbind` を使用してください。",
		LangSpanish:            "El workspace efectivo actual proviene de la capa shared. Usa `/workspace shared unbind` para quitarlo.",
	},
	MsgWsNotFoundHint: {
		LangEnglish:            "No workspace found for this channel. Send a git repo URL, a local directory path, or use `/workspace init <url-or-path>`.",
		LangChinese:            "此频道未找到工作区。请发送 git 仓库地址或本地目录路径，或使用 `/workspace init <仓库地址或目录路径>`。",
		LangTraditionalChinese: "此頻道未找到工作區。請發送 git 倉庫地址或本地目錄路徑，或使用 `/workspace init <倉庫地址或目錄路徑>`。",
		LangJapanese:           "このチャンネルにワークスペースが見つかりません。git URL またはローカルディレクトリパスを送信するか、`/workspace init <urlまたはパス>` を使用してください。",
		LangSpanish:            "No se encontró workspace para este canal. Envía una URL de repo git, una ruta de directorio local, o usa `/workspace init <url-o-ruta>`.",
	},
	MsgWsNotFoundHintGitOnly: {
		LangEnglish:            "No workspace found for this channel. Send a git repo URL or use `/workspace init <git-url>`.",
		LangChinese:            "此频道未找到工作区。请发送 git 仓库地址，或使用 `/workspace init <git仓库地址>`。",
		LangTraditionalChinese: "此頻道未找到工作區。請發送 git 倉庫地址，或使用 `/workspace init <git倉庫地址>`。",
		LangJapanese:           "このチャンネルにワークスペースが見つかりません。git URL を送信するか、`/workspace init <git-url>` を使用してください。",
		LangSpanish:            "No se encontró workspace para este canal. Envía una URL de repo git o usa `/workspace init <git-url>`.",
	},
	MsgWsResolutionError: {
		LangEnglish:            "Workspace resolution error: %v",
		LangChinese:            "工作区解析错误: %v",
		LangTraditionalChinese: "工作區解析錯誤: %v",
		LangJapanese:           "ワークスペース解決エラー: %v",
		LangSpanish:            "Error de resolución de workspace: %v",
	},
	MsgWsCloneProgress: {
		LangEnglish:            "🔄 Cloning repository: %s",
		LangChinese:            "🔄 正在克隆仓库: %s",
		LangTraditionalChinese: "🔄 正在克隆倉庫: %s",
		LangJapanese:           "🔄 リポジトリをクローン中: %s",
		LangSpanish:            "🔄 Clonando repositorio: %s",
	},
	MsgWsCloneSuccess: {
		LangEnglish:            "✅ Repository cloned successfully: `%s`",
		LangChinese:            "✅ 仓库克隆成功: `%s`",
		LangTraditionalChinese: "✅ 倉庫克隆成功: `%s`",
		LangJapanese:           "✅ リポジトリのクローンに成功しました: `%s`",
		LangSpanish:            "✅ Repositorio clonado exitosamente: `%s`",
	},
	MsgWsCloneFailed: {
		LangEnglish:            "❌ Failed to clone repository: %v",
		LangChinese:            "❌ 克隆仓库失败: %v",
		LangTraditionalChinese: "❌ 克隆倉庫失敗: %v",
		LangJapanese:           "❌ リポジトリのクローンに失敗しました: %v",
		LangSpanish:            "❌ Error al clonar repositorio: %v",
	},
	MsgWsInitDirNotFound: {
		LangEnglish:            "Directory not found: `%s`. Please provide a valid directory path or a git URL.",
		LangChinese:            "目录不存在: `%s`。请提供有效的目录路径或 git 仓库地址。",
		LangTraditionalChinese: "目錄不存在: `%s`。請提供有效的目錄路徑或 git 倉庫地址。",
		LangJapanese:           "ディレクトリが見つかりません: `%s`。有効なディレクトリパスまたは git URL を指定してください。",
		LangSpanish:            "Directorio no encontrado: `%s`. Proporcione una ruta de directorio válida o una URL de git.",
	},
	MsgWsInitInvalidTarget: {
		LangEnglish:            "Please provide a git URL (e.g. `https://github.com/org/repo`) or a local directory path.",
		LangChinese:            "请提供 git 仓库地址（如 `https://github.com/org/repo`）或本地目录路径。",
		LangTraditionalChinese: "請提供 git 倉庫地址（如 `https://github.com/org/repo`）或本地目錄路徑。",
		LangJapanese:           "git URL（例: `https://github.com/org/repo`）またはローカルディレクトリパスを指定してください。",
		LangSpanish:            "Proporcione una URL de git (ej. `https://github.com/org/repo`) o una ruta de directorio local.",
	},
	MsgWsInitLocalPathsDisabled: {
		LangEnglish:            "Local directory targets are disabled for `/workspace init`. Use a git URL, or enable `workspace_init_allow_local_paths = true` for this project.",
		LangChinese:            "`/workspace init` 未启用本地目录目标。请使用 git 仓库地址，或在此项目配置 `workspace_init_allow_local_paths = true`。",
		LangTraditionalChinese: "`/workspace init` 未啟用本機目錄目標。請使用 git 倉庫地址，或在此專案配置 `workspace_init_allow_local_paths = true`。",
		LangJapanese:           "`/workspace init` ではローカルディレクトリ対象が無効です。git URL を使うか、このプロジェクトで `workspace_init_allow_local_paths = true` を有効にしてください。",
		LangSpanish:            "Los destinos de directorio local están deshabilitados para `/workspace init`. Use una URL de git o habilite `workspace_init_allow_local_paths = true` para este proyecto.",
	},
	MsgAgentSendToolPrompt: {
		LangEnglish: `### Send generated images, files, or voice messages back to the user
When you generate a local image or file that should be sent to the user, use:

  cc-connect send --image /absolute/path/to/image.png
  cc-connect send --file /absolute/path/to/report.pdf
  cc-connect send --file /absolute/path/to/report.pdf --image /absolute/path/to/chart.png

You may repeat --image / --file multiple times. Use this only for generated attachments that need to be delivered to the user.
If you include --message, do not repeat the exact same sentence again in your normal reply, because your normal reply is also delivered automatically.

When sending an audio (mp3/wav/m4a/ogg/opus) or video (mp4/mov/webm) clip that should render inline as a native voice bubble or video player — instead of as a generic file download — use the dedicated flags:

  cc-connect send --audio /absolute/path/to/clip.mp3
  cc-connect send --video /absolute/path/to/demo.mp4

These render as native media on platforms that support it (e.g. Feishu voice bubbles, Telegram voice messages). cc-connect transparently transcodes audio to the platform's preferred codec (e.g. opus for Feishu). On platforms without dedicated audio/video support cc-connect automatically falls back to the file-attachment path so delivery is preserved. Do NOT downgrade the user's request to --file when they explicitly asked for audio or video.

When the user explicitly asks you to synthesize speech from text, use:

  cc-connect send --tts "text to speak"

After cc-connect send --tts (or --audio) succeeds, reply only with NO_REPLY unless the user also asked for a visible text confirmation. This prevents sending an extra text message after the voice message.`,
		LangChinese: `### 把生成的图片、文件、语音消息回发给用户
当你生成了需要发送给用户的本地图片或文件时,使用:

  cc-connect send --image /absolute/path/to/image.png
  cc-connect send --file /absolute/path/to/report.pdf
  cc-connect send --file /absolute/path/to/report.pdf --image /absolute/path/to/chart.png

可以重复使用 --image / --file。仅在需要把生成的附件投递到用户时使用这个命令。
如果同时使用了 --message,不要在正常回复里再说一遍完全相同的句子,因为正常回复本身也会自动发送给用户。

发送音频 (mp3/wav/m4a/ogg/opus) 或视频 (mp4/mov/webm) 片段、且希望它们以内联的原生语音气泡或视频播放器形态呈现(而不是作为普通文件下载)时,使用专用参数:

  cc-connect send --audio /absolute/path/to/clip.mp3
  cc-connect send --video /absolute/path/to/demo.mp4

这些参数会在支持原生媒体的平台上渲染为原生形态(例如飞书的语音气泡、Telegram 的语音消息)。cc-connect 会自动把音频转码为平台偏好的编码(例如飞书的 opus)。在不支持专用音视频的平台,cc-connect 会自动回退到文件附件路径以保证投递成功。当用户明确要求 audio/video 时,不要把请求降级为 --file。

当用户明确要求把文字合成为语音时,使用:

  cc-connect send --tts "要朗读的文字"

cc-connect send --tts(或 --audio)成功之后,除非用户同时要求可见的文字确认,否则只回复 NO_REPLY,避免在语音消息后再发一条文字消息。`,
	},
	MsgAgentCronToolPrompt: {
		LangEnglish: `### Scheduled tasks: when to use /cron vs /timer

cc-connect has TWO distinct scheduling commands. Picking the wrong one creates a confusing UX for the user.

  ┌──────────────────────────────┬─────────────────────────────┐
  │ Use cc-connect cron …        │ Use cc-connect timer …      │
  ├──────────────────────────────┼─────────────────────────────┤
  │ Recurring schedule           │ One-shot delay / one-time   │
  │ "每天/每周/每小时"            │ "X 分钟后/小时后/明天"        │
  │ "every day/week/Monday"      │ "in 30 min", "tomorrow 9am"  │
  │ "每天早上6点总结"             │ "3 分钟后检查负载"            │
  │ Lives forever until deleted  │ Auto-archives after firing  │
  │ Queried via /cron            │ Queried via /timer          │
  └──────────────────────────────┴─────────────────────────────┘

When telling the user the task is scheduled, tell them which command to use to view/manage it
(say "use /timer to view" for one-shot, "use /cron to view" for recurring).

### Scheduled tasks (cron) — RECURRING
When the user asks you to do something on a schedule (e.g. "每天早上6点帮我总结GitHub trending"), use the Bash tool to run:

  cc-connect cron add --cron "<min> <hour> <day> <month> <weekday>" --prompt "<task description>" --desc "<short label>"

Environment variables CC_PROJECT and CC_SESSION_KEY are already set, so you do NOT need to specify --project or --session-key.

Optional flags:
  --session-mode <mode>     reuse (default) or new-per-run (fresh session each trigger)
  --timeout-mins <n>        max wait per run in minutes (default 30, 0 = unlimited)
  --exec <command>          run a shell command directly instead of --prompt

Examples:
  cc-connect cron add --cron "0 6 * * *" --prompt "Collect GitHub trending repos and send a summary" --desc "Daily GitHub Trending"
  cc-connect cron add --cron "0 9 * * 1" --prompt "Generate a weekly project status report" --desc "Weekly Report"
  cc-connect cron add --cron "*/2 * * * *" --exec "ipconfig" --session-mode new-per-run --desc "Every 2 min ipconfig"

You can also list, inspect, run, edit, or delete cron jobs:
  cc-connect cron list
  cc-connect cron info <job-id> [field]
  cc-connect cron exec <job-id>
  cc-connect cron edit <job-id> <field> <value>
  cc-connect cron del <job-id>

When changing an existing job, first run ` + "`cc-connect cron info <job-id>`" + ` to inspect the current values, then use ` + "`cron edit`" + ` for only the field(s) the user asked to change.
Use ` + "`cron exec <job-id>`" + ` to run an existing scheduled task immediately; this is different from the ` + "`--exec <command>`" + ` flag used when creating a shell-command cron job.
Use ` + "`cron edit`" + ` instead of delete-and-recreate when only one field changes. Do not delete and recreate a job unless the user explicitly asks to replace it.
Common editable fields:
  cron_expr     new schedule, e.g. "0 9 * * *"
  prompt        new task prompt (or ` + "`exec`" + ` for shell command)
  description   short label
  enabled       true / false  (pause without deleting)
  mute          true / false  (silence all messages)
  timeout_mins  integer minutes (0 = unlimited)
Run ` + "`cc-connect cron edit --help`" + ` for the full field list.

Examples:
  cc-connect cron exec abc123
  cc-connect cron edit abc123 cron_expr "0 9 * * *"
  cc-connect cron edit abc123 enabled false
  cc-connect cron edit abc123 prompt "Updated daily summary task"`,
		LangChinese: `### 定时任务:什么时候用 /cron,什么时候用 /timer

cc-connect 有两个不同的调度命令。选错会让用户感到很困惑。

  ┌──────────────────────────────┬─────────────────────────────┐
  │ 使用 cc-connect cron …       │ 使用 cc-connect timer …     │
  ├──────────────────────────────┼─────────────────────────────┤
  │ 周期性调度                    │ 单次延时 / 一次性任务         │
  │ "每天/每周/每小时"            │ "X 分钟后/小时后/明天"        │
  │ "every day/week/Monday"      │ "in 30 min", "tomorrow 9am"  │
  │ "每天早上6点总结"             │ "3 分钟后检查负载"            │
  │ 一直存在直到被删除             │ 触发后自动归档                │
  │ 通过 /cron 查看               │ 通过 /timer 查看              │
  └──────────────────────────────┴─────────────────────────────┘

告诉用户任务已安排好后,顺手告诉他们用哪个命令查看/管理:
(单次任务说"用 /timer 查看",周期任务说"用 /cron 查看")。

### 周期任务 (cron) — RECURRING
当用户让你做周期性任务时(例如"每天早上6点帮我总结GitHub trending"),用 Bash 工具执行:

  cc-connect cron add --cron "<分> <时> <日> <月> <星期>" --prompt "<任务描述>" --desc "<简短标签>"

环境变量 CC_PROJECT 和 CC_SESSION_KEY 已经设置好,你不需要传 --project 或 --session-key。

可选参数:
  --session-mode <mode>     reuse(默认)或 new-per-run(每次触发用新会话)
  --timeout-mins <n>        每次运行最长等待分钟数(默认 30,0 = 不限)
  --exec <command>          直接跑 shell 命令而不是 --prompt

示例:
  cc-connect cron add --cron "0 6 * * *" --prompt "汇总 GitHub trending 仓库并发摘要" --desc "每日 GitHub Trending"
  cc-connect cron add --cron "0 9 * * 1" --prompt "生成本周项目状态报告" --desc "每周报告"
  cc-connect cron add --cron "*/2 * * * *" --exec "ipconfig" --session-mode new-per-run --desc "每 2 分钟跑 ipconfig"

你也可以列出、检查、立即执行、修改或删除 cron 任务:
  cc-connect cron list
  cc-connect cron info <job-id> [字段]
  cc-connect cron exec <job-id>
  cc-connect cron edit <job-id> <字段> <新值>
  cc-connect cron del <job-id>

修改现有任务时,先用 ` + "`cc-connect cron info <job-id>`" + ` 看当前值,再用 ` + "`cron edit`" + ` 只改用户要求改的字段。
用 ` + "`cron exec <job-id>`" + ` 立即执行已存在的调度任务;这和创建 shell 任务时用的 ` + "`--exec <command>`" + ` 参数不同。
只有修改一个字段时,用 ` + "`cron edit`" + ` 而不是删除后重建。除非用户明确要求替换任务,不要先删再建。
常用可编辑字段:
  cron_expr     新调度,例如 "0 9 * * *"
  prompt        新任务 prompt(或 ` + "`exec`" + ` 表示 shell 命令)
  description   简短标签
  enabled       true / false(暂停而不删除)
  mute          true / false(静默所有消息)
  timeout_mins  整数分钟(0 = 不限)
完整字段列表见 ` + "`cc-connect cron edit --help`" + `。

示例:
  cc-connect cron exec abc123
  cc-connect cron edit abc123 cron_expr "0 9 * * *"
  cc-connect cron edit abc123 enabled false
  cc-connect cron edit abc123 prompt "更新后的每日摘要任务"`,
	},
	MsgAgentTimerToolPrompt: {
		LangEnglish: `### One-shot timers (timer) — ONE-TIME DELAY
When the user asks you to do something AFTER A DELAY or AT A SPECIFIC FUTURE TIME
(e.g. "两小时后帮我检查PR", "3 分钟后看下系统负载", "明天早上 9 点提醒我"),
use the Bash tool to run:

  cc-connect timer add --delay <duration> --prompt "<task description>"

IMPORTANT: do NOT use cron for one-shot delays. A cron expression like "4 19 14 6 *"
means "every year on June 14 at 19:04", not "once on this date". Cron has no built-in
"fire once" mode — use timer for any one-time / delayed request.

Duration examples: 30m, 2h, 1h30m. Or use absolute time: --at "2026-05-16T09:00"
Absolute times without timezone (e.g. "2026-05-16T09:00") are interpreted as the
system's local timezone. When the user says "明天早上9点", use local time.
Environment variables CC_PROJECT and CC_SESSION_KEY are already set.

Optional flags:
  --exec <command>          run a shell command directly instead of --prompt
  --desc <text>             short description
  --session-mode <mode>     reuse (default) or new-per-run (fresh session each run)
  --timeout-mins <n>        max wait per run in minutes (default 30, 0 = unlimited)
  --mute                    suppress all messages (start notification + result)

Examples:
  cc-connect timer add --delay 2h --prompt "Check PR status" --desc "PR check"
  cc-connect timer add --delay 30m --exec "df -h" --desc "Disk check"
  cc-connect timer add --at "2026-05-16T09:00" --prompt "Morning standup reminder"

You can also list or cancel timers:
  cc-connect timer list
  cc-connect timer del <timer-id>`,
		LangChinese: `### 一次性延时 (timer) — ONE-TIME DELAY
当用户让你在一段延时之后或在某个未来时刻做某事时
(例如"两小时后帮我检查PR"、"3 分钟后看下系统负载"、"明天早上 9 点提醒我"),
用 Bash 工具执行:

  cc-connect timer add --delay <时长> --prompt "<任务描述>"

重要:不要用 cron 跑单次延时。形如 "4 19 14 6 *" 的 cron 表达式
意思是"每年 6 月 14 日 19:04"而不是"这一天跑一次"。cron 没有内建"只跑一次"模式 ——
所有一次性 / 延时任务都用 timer。

时长示例:30m、2h、1h30m。或者用绝对时间:--at "2026-05-16T09:00"
不带时区的绝对时间(例如 "2026-05-16T09:00")按系统本地时区解释。用户说"明天早上9点"时用本地时间。
环境变量 CC_PROJECT 和 CC_SESSION_KEY 已经设置好。

可选参数:
  --exec <command>          直接跑 shell 命令而不是 --prompt
  --desc <text>             简短描述
  --session-mode <mode>     reuse(默认)或 new-per-run(每次跑用新会话)
  --timeout-mins <n>        每次最长等待分钟数(默认 30,0 = 不限)
  --mute                    静默所有消息(开始通知和结果)

示例:
  cc-connect timer add --delay 2h --prompt "检查 PR 状态" --desc "PR 检查"
  cc-connect timer add --delay 30m --exec "df -h" --desc "磁盘检查"
  cc-connect timer add --at "2026-05-16T09:00" --prompt "早会提醒"

你也可以列出或取消 timer:
  cc-connect timer list
  cc-connect timer del <timer-id>`,
	},
	MsgAgentRelayToolPrompt: {
		LangEnglish: `### Bot-to-bot relay
When you need to communicate with another bot (e.g. ask another AI agent a question), use:

  cc-connect relay send --to <target_project> "<message>"

IMPORTANT: <target_project> must be the EXACT project name from the /bind command output.
Do NOT guess or modify the name — use it exactly as shown (e.g. "gemini", not "gemini-bot").

This sends a message to the target bot and waits for its response (printed to stdout).
The conversation is visible in the group chat and each bot maintains its own relay session.

Environment variables CC_PROJECT and CC_SESSION_KEY are already set, so the relay knows which group chat to use.`,
		LangChinese: `### Bot 之间转发 (relay)
当需要和另一个 bot 通信(例如向另一个 AI agent 提问)时,使用:

  cc-connect relay send --to <目标项目名> "<消息>"

重要:<目标项目名> 必须是 /bind 命令输出里的 EXACT 项目名。
不要猜测或修改名字 —— 完全照搬显示值(例如 "gemini" 而不是 "gemini-bot")。

这会把消息发给目标 bot 并等待它的回复(打印到 stdout)。
会话在群聊里可见,每个 bot 维护自己的 relay 会话。

环境变量 CC_PROJECT 和 CC_SESSION_KEY 已经设置好,relay 知道用哪个群聊。`,
	},
}

func (i *I18n) T(key MsgKey) string {
	i.mu.RLock()
	lang := i.currentLang()
	i.mu.RUnlock()
	if msg, ok := messages[key]; ok {
		if translated, ok := msg[lang]; ok {
			return translated
		}
		// Fallback: zh-TW → zh → en
		if lang == LangTraditionalChinese {
			if translated, ok := msg[LangChinese]; ok {
				return translated
			}
		}
		if msg[LangEnglish] != "" {
			return msg[LangEnglish]
		}
	}
	return string(key)
}

func (i *I18n) Tf(key MsgKey, args ...interface{}) string {
	template := i.T(key)
	return fmt.Sprintf(template, args...)
}
