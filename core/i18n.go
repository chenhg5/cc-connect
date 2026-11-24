package core

import (
	"fmt"
	"sync"
)

// Language represents a supported language
type Language string

const (
	LangAuto    Language = "" // auto-detect from user messages
	LangEnglish Language = "en"
	LangChinese Language = "zh"
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
		if isChinese(r) {
			return LangChinese
		}
	}
	return LangEnglish
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

// IsZhLike reports whether the current language is Chinese.
func (i *I18n) IsZhLike() bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.currentLang() == LangChinese
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
	MsgStarting                    MsgKey = "starting"
	MsgLoading                     MsgKey = "loading"
	MsgThinking                    MsgKey = "thinking"
	MsgTool                        MsgKey = "tool"
	MsgToolResult                  MsgKey = "tool_result"
	MsgToolResultFmtStatus         MsgKey = "tool_result_fmt_status"
	MsgToolResultFmtExit           MsgKey = "tool_result_fmt_exit"
	MsgToolResultFmtNoOutput       MsgKey = "tool_result_fmt_no_output"
	MsgToolResultFmtOk             MsgKey = "tool_result_fmt_ok"
	MsgToolResultFmtFailed         MsgKey = "tool_result_fmt_failed"
	MsgExecutionStopped            MsgKey = "execution_stopped"
	MsgTaskCardRunning             MsgKey = "task_card_running"
	MsgTaskCardDone                MsgKey = "task_card_done"
	MsgTaskCardStopped             MsgKey = "task_card_stopped"
	MsgTaskCardFailed              MsgKey = "task_card_failed"
	MsgTaskCardPreparing           MsgKey = "task_card_preparing"
	MsgTaskCardStep                MsgKey = "task_card_step"
	MsgTaskCardLastStep            MsgKey = "task_card_last_step"
	MsgTaskCardQueue               MsgKey = "task_card_queue"
	MsgTaskCardStopBtn             MsgKey = "task_card_stop_btn"
	MsgTaskCardWaitingConfirm      MsgKey = "task_card_waiting_confirm"
	MsgTaskCardDegraded            MsgKey = "task_card_degraded"
	MsgSteerAck                    MsgKey = "steer_ack"
	MsgAckProcessing               MsgKey = "ack_processing"
	MsgNoExecution                 MsgKey = "no_execution"
	MsgPreviousProcessing          MsgKey = "previous_processing"
	MsgQueueFull                   MsgKey = "queue_full"
	MsgMessageQueued               MsgKey = "message_queued"
	MsgNoToolsAllowed              MsgKey = "no_tools_allowed"
	MsgCurrentTools                MsgKey = "current_tools"
	MsgCurrentSession              MsgKey = "current_session"
	MsgToolAuthNotSupported        MsgKey = "tool_auth_not_supported"
	MsgToolAllowFailed             MsgKey = "tool_allow_failed"
	MsgToolAllowedNew              MsgKey = "tool_allowed_new"
	MsgError                       MsgKey = "error"
	MsgImageTooLargeSingle         MsgKey = "image_too_large_single"
	MsgImageTooLargeMulti          MsgKey = "image_too_large_multi"
	MsgQuotedImageTooLarge         MsgKey = "quoted_image_too_large"
	MsgSessionAutoResetError       MsgKey = "session_auto_reset_error"
	MsgSessionAutoResetRedispatch  MsgKey = "session_auto_reset_redispatch"
	MsgRedispatchFailed            MsgKey = "redispatch_failed"
	MsgRedispatchImagesSkipped     MsgKey = "redispatch_images_skipped"
	MsgRedispatchOnlyImagesSkipped MsgKey = "redispatch_only_images_skipped"
	MsgSnippetUnknown              MsgKey = "snippet_unknown"
	MsgSnippetImage                MsgKey = "snippet_image"
	MsgSnippetFile                 MsgKey = "snippet_file"
	MsgAgentErrorImageTooLarge     MsgKey = "agent_error_image_too_large"
	MsgAgentErrorAuth              MsgKey = "agent_error_auth"
	MsgAgentErrorRateLimit         MsgKey = "agent_error_rate_limit"
	MsgAgentErrorTimeout           MsgKey = "agent_error_timeout"
	MsgAgentErrorContext           MsgKey = "agent_error_context"
	MsgAgentErrorGeneric           MsgKey = "agent_error_generic"
	MsgAgentSendFailed             MsgKey = "agent_send_failed"
	MsgQueuedMessageDropped        MsgKey = "queued_message_dropped"
	MsgFinalDeliveryFailed         MsgKey = "final_delivery_failed"
	MsgFinalDeliveryPartial        MsgKey = "final_delivery_partial"
	MsgPreviousTurnFailedRestart   MsgKey = "previous_turn_failed_restart"
	MsgSteerAfterTurnEnd           MsgKey = "steer_after_turn_end"
	MsgSessionNotFound             MsgKey = "session_not_found"
	MsgFailedToStartAgentSession   MsgKey = "failed_to_start_agent_session"
	MsgFailedToDeleteSession       MsgKey = "failed_to_delete_session"
	MsgEmptyResponse               MsgKey = "empty_response"
	MsgPermissionPrompt            MsgKey = "permission_prompt"
	MsgPermissionAllowed           MsgKey = "permission_allowed"
	MsgPermissionApproveAll        MsgKey = "permission_approve_all"
	MsgPermissionDenied            MsgKey = "permission_denied_msg"
	MsgPermissionHint              MsgKey = "permission_hint"
	MsgQuietOn                     MsgKey = "quiet_on"
	MsgQuietOff                    MsgKey = "quiet_off"
	MsgDisplayModeCompact          MsgKey = "display_mode_compact"
	MsgQuietGlobalOn               MsgKey = "quiet_global_on"
	MsgQuietGlobalOff              MsgKey = "quiet_global_off"
	MsgModeChanged                 MsgKey = "mode_changed"
	MsgModeNotSupported            MsgKey = "mode_not_supported"
	MsgSessionRestarting           MsgKey = "session_restarting"
	MsgSessionNotStarted           MsgKey = "session_not_started"
	MsgUntitled                    MsgKey = "untitled"
	MsgLangChanged                 MsgKey = "lang_changed"
	MsgLangInvalid                 MsgKey = "lang_invalid"
	MsgLangCurrent                 MsgKey = "lang_current"
	MsgUnknownCommand              MsgKey = "unknown_command"
	MsgWelcome                     MsgKey = "welcome"
	MsgHelp                        MsgKey = "message_help" // change from "help", which is used now for builtin command help
	MsgHelpTitle                   MsgKey = "help_title"
	MsgHelpSessionSection          MsgKey = "help_session_section"
	MsgHelpAgentSection            MsgKey = "help_agent_section"
	MsgHelpToolsSection            MsgKey = "help_tools_section"
	MsgHelpSystemSection           MsgKey = "help_system_section"
	MsgHelpTip                     MsgKey = "help_tip"
	MsgListTitle                   MsgKey = "list_title"
	MsgListTitlePaged              MsgKey = "list_title_paged"
	MsgListEmpty                   MsgKey = "list_empty"
	MsgListMore                    MsgKey = "list_more"
	MsgListPageHint                MsgKey = "list_page_hint"
	MsgListSwitchHint              MsgKey = "list_switch_hint"
	MsgListError                   MsgKey = "list_error"
	MsgHistoryEmpty                MsgKey = "history_empty"
	MsgNameUsage                   MsgKey = "name_usage"
	MsgNameSet                     MsgKey = "name_set"
	MsgNameNoSession               MsgKey = "name_no_session"
	MsgProviderNotSupported        MsgKey = "provider_not_supported"
	MsgProviderNone                MsgKey = "provider_none"
	MsgProviderCurrent             MsgKey = "provider_current"
	MsgProviderListTitle           MsgKey = "provider_list_title"
	MsgProviderListEmpty           MsgKey = "provider_list_empty"
	MsgProviderSwitchHint          MsgKey = "provider_switch_hint"
	MsgProviderNotFound            MsgKey = "provider_not_found"
	MsgProviderSwitched            MsgKey = "provider_switched"
	MsgProviderCleared             MsgKey = "provider_cleared"
	MsgProviderAdded               MsgKey = "provider_added"
	MsgProviderAddUsage            MsgKey = "provider_add_usage"
	MsgProviderAddFailed           MsgKey = "provider_add_failed"
	MsgProviderRemoved             MsgKey = "provider_removed"
	MsgProviderRemoveFailed        MsgKey = "provider_remove_failed"
	MsgCardTitleProviderAdd        MsgKey = "card_title_provider_add"
	MsgProviderAddPickHint         MsgKey = "provider_add_pick_hint"
	MsgProviderAddOther            MsgKey = "provider_add_other"
	MsgProviderAddApiKeyPrompt     MsgKey = "provider_add_api_key_prompt"
	MsgProviderAddInviteHint       MsgKey = "provider_add_invite_hint"
	MsgProviderLinkGlobal          MsgKey = "provider_link_global"
	MsgProviderLinked              MsgKey = "provider_linked"

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

	MsgCronNotAvailable MsgKey = "cron_not_available"
	MsgCronUsage        MsgKey = "cron_usage"
	MsgCronAddUsage     MsgKey = "cron_add_usage"
	MsgCronAdded        MsgKey = "cron_added"
	MsgCronAddedExec    MsgKey = "cron_added_exec"
	MsgCronAddExecUsage MsgKey = "cron_addexec_usage"
	MsgCronEmpty        MsgKey = "cron_empty"
	MsgCronListTitle    MsgKey = "cron_list_title"
	MsgCronListFooter   MsgKey = "cron_list_footer"
	MsgCronDelUsage     MsgKey = "cron_del_usage"
	MsgCronDeleted      MsgKey = "cron_deleted"
	MsgCronNotFound     MsgKey = "cron_not_found"
	MsgCronEnabled      MsgKey = "cron_enabled"
	MsgCronDisabled     MsgKey = "cron_disabled"
	MsgCronMuted        MsgKey = "cron_muted"
	MsgCronUnmuted      MsgKey = "cron_unmuted"
	MsgCronCardHint     MsgKey = "cron_card_hint"
	MsgCronNextShort    MsgKey = "cron_next_short"
	MsgCronLastShort    MsgKey = "cron_last_short"
	MsgCronBtnEnable    MsgKey = "cron_btn_enable"
	MsgCronBtnDisable   MsgKey = "cron_btn_disable"
	MsgCronBtnMute      MsgKey = "cron_btn_mute"
	MsgCronBtnUnmute    MsgKey = "cron_btn_unmute"
	MsgCronBtnDelete    MsgKey = "cron_btn_delete"

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
	MsgCommandsTagAgent          MsgKey = "commands_tag_agent"
	MsgCommandsTagShell          MsgKey = "commands_tag_shell"
	MsgUpgradeTimeoutSuffix      MsgKey = "upgrade_timeout_suffix"

	MsgCronScheduleLabel MsgKey = "cron_schedule_label"
	MsgCronNextRunLabel  MsgKey = "cron_next_run_label"
	MsgCronLastRunLabel  MsgKey = "cron_last_run_label"

	MsgPermBtnAllow    MsgKey = "perm_btn_allow"
	MsgPermBtnDeny     MsgKey = "perm_btn_deny"
	MsgPermBtnAllowAll MsgKey = "perm_btn_allow_all"
	MsgPermCardTitle   MsgKey = "perm_card_title"
	MsgPermCardBody    MsgKey = "perm_card_body"
	MsgPermCardNote    MsgKey = "perm_card_note"

	MsgAskQuestionTitle    MsgKey = "ask_question_title"
	MsgAskQuestionNote     MsgKey = "ask_question_note"
	MsgAskQuestionMulti    MsgKey = "ask_question_multi"
	MsgAskQuestionPrompt   MsgKey = "ask_question_prompt"
	MsgAskQuestionAnswered MsgKey = "ask_question_answered"

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

	MsgSkillsTitle MsgKey = "skills_title"
	MsgSkillsEmpty MsgKey = "skills_empty"
	MsgSkillsHint  MsgKey = "skills_hint"

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
)

var messages = map[MsgKey]map[Language]string{
	MsgStarting: {
		LangEnglish: "⏳ Processing...",
		LangChinese: "⏳ 处理中...",
	},
	MsgLoading: {
		LangEnglish: "Thinking...",
		LangChinese: "正在思考...",
	},
	MsgThinking: {
		LangEnglish: "💭 %s",
		LangChinese: "💭 %s",
	},
	MsgTool: {
		LangEnglish: "🔧 **Tool #%d: %s**\n---\n%s",
		LangChinese: "🔧 **工具 #%d: %s**\n---\n%s",
	},
	MsgToolResult: {
		LangEnglish: "📤 **%s**\n---\n%s",
		LangChinese: "📤 **%s**\n---\n%s",
	},
	MsgToolResultFmtStatus: {
		LangEnglish: "Status",
		LangChinese: "状态",
	},
	MsgToolResultFmtExit: {
		LangEnglish: "Exit",
		LangChinese: "退出码",
	},
	MsgToolResultFmtNoOutput: {
		LangEnglish: "No output",
		LangChinese: "无输出",
	},
	MsgToolResultFmtOk: {
		LangEnglish: "ok",
		LangChinese: "ok",
	},
	MsgToolResultFmtFailed: {
		LangEnglish: "failed",
		LangChinese: "failed",
	},
	MsgExecutionStopped: {
		LangEnglish: "⏹ Execution stopped.",
		LangChinese: "⏹ 执行已停止。",
	},
	MsgTaskCardRunning: {
		LangEnglish: "⏳ Working · %s",
		LangChinese: "⏳ 正在处理 · %s",
	},
	MsgTaskCardDone: {
		LangEnglish: "✅ Done · %s",
		LangChinese: "✅ 已完成 · 用时 %s",
	},
	MsgTaskCardStopped: {
		LangEnglish: "⏹ Stopped · %s",
		LangChinese: "⏹ 已停止 · 用时 %s",
	},
	MsgTaskCardFailed: {
		LangEnglish: "❌ Failed · %s",
		LangChinese: "❌ 出错了 · 用时 %s",
	},
	MsgTaskCardPreparing: {
		LangEnglish: "Preparing",
		LangChinese: "准备中",
	},
	MsgTaskCardStep: {
		LangEnglish: "**Current step:** %s",
		LangChinese: "**当前步骤：**%s",
	},
	MsgTaskCardLastStep: {
		LangEnglish: "**Last step:** %s",
		LangChinese: "**最后步骤：**%s",
	},
	MsgTaskCardQueue: {
		LangEnglish: "📋 Queued (%d)",
		LangChinese: "📋 排队中 (%d)",
	},
	MsgTaskCardStopBtn: {
		LangEnglish: "⏹ Stop",
		LangChinese: "⏹ 停止当前",
	},
	MsgAckProcessing: {
		LangEnglish: "Received, processing...",
		LangChinese: "收到，处理中...",
	},
	MsgSteerAck: {
		LangEnglish: "Received — sent into the running task, please wait…",
		LangChinese: "收到，已插话到当前任务，处理中...",
	},
	MsgTaskCardWaitingConfirm: {
		LangEnglish: "waiting for your confirmation",
		LangChinese: "等待你的确认",
	},
	MsgTaskCardDegraded: {
		LangEnglish: "⚠️ Progress-card updates keep failing (possibly throttled). The task is still running; the final result will be sent as a normal message.",
		LangChinese: "⚠️ 进度卡片连续更新失败（可能被限流），任务仍在继续；最终结果会以普通消息发送。",
	},
	MsgNoExecution: {
		LangEnglish: "No execution in progress.",
		LangChinese: "没有正在执行的任务。",
	},
	MsgPreviousProcessing: {
		LangEnglish: "⏳ Previous request still processing. Use `/ps <message>` to send a P.S. to the running task.",
		LangChinese: "⏳ 上一个请求仍在处理中。使用 `/ps <消息>` 可向正在执行的任务追加补充信息。",
	},
	MsgMessageQueued: {
		LangEnglish: "📬 Message received — will process after the current task finishes.",
		LangChinese: "收到，已排队，将在当前任务完成后处理...",
	},
	MsgQueueFull: {
		LangEnglish: "📬 Message queue is full (%d pending). Please wait for current tasks to complete.",
		LangChinese: "📬 消息队列已满（%d 条待处理）。请等待当前任务完成。",
	},
	MsgNoToolsAllowed: {
		LangEnglish: "No tools pre-allowed.\nUsage: `/allow <tool_name>`\nExample: `/allow Bash`",
		LangChinese: "尚未预授权任何工具。\n用法: `/allow <工具名>`\n示例: `/allow Bash`",
	},
	MsgCurrentTools: {
		LangEnglish: "Pre-allowed tools: %s",
		LangChinese: "预授权的工具: %s",
	},
	MsgCurrentSession: {
		LangEnglish: "📌 Current session\nName: %s\nSession ID: %s\nLocal messages: %d",
		LangChinese: "📌 当前会话\n名称: %s\n会话 ID: %s\n本地消息数: %d",
	},
	MsgToolAuthNotSupported: {
		LangEnglish: "This agent does not support tool authorization.",
		LangChinese: "此代理不支持工具授权。",
	},
	MsgToolAllowFailed: {
		LangEnglish: "Failed to allow tool: %v",
		LangChinese: "授权工具失败: %v",
	},
	MsgToolAllowedNew: {
		LangEnglish: "✅ Tool `%s` pre-allowed. Takes effect on next session.",
		LangChinese: "✅ 工具 `%s` 已预授权。将在下次会话生效。",
	},
	MsgError: {
		LangEnglish: "❌ Error: %v",
		LangChinese: "❌ 错误: %v",
	},
	MsgBackgroundAutoDenied: {
		LangEnglish: "⚠️ Background task requested permission for `%s` but was auto-denied (no active user turn). Send a message or use `/yolo` to approve future requests.",
		LangChinese: "⚠️ 后台任务请求使用工具 `%s` 的权限，但已自动拒绝（当前无活跃会话）。请发送消息或使用 `/yolo` 授权后续请求。",
	},
	MsgSessionNotFound: {
		LangEnglish: "⚠️ Session expired. Use /new to start a fresh conversation.",
		LangChinese: "⚠️ 会话已过期，请发送 /new 开始新会话",
	},
	MsgFailedToStartAgentSession: {
		LangEnglish: "❌ Error: failed to start agent session",
		LangChinese: "❌ 错误: 启动 Agent 会话失败",
	},
	MsgImageTooLargeSingle: {
		LangEnglish: "⚠️ The image is too large (%d×%d), exceeding the model's %d px per-side limit. Please split it and resend.",
		LangChinese: "⚠️ 图片过大（%d×%d），超过模型单边 %d px 上限。请切分后重发。",
	},
	MsgImageTooLargeMulti: {
		LangEnglish: "⚠️ Image %d of %d is too large (%d×%d), exceeding the model's %d px per-side limit. Please split it and resend.",
		LangChinese: "⚠️ 第 %d 张（共 %d 张）图片过大（%d×%d），超过模型单边 %d px 上限。请切分后重发。",
	},
	MsgQuotedImageTooLarge: {
		LangEnglish: "⚠️ The quoted message contains a %d×%d image, which exceeds the model's %d px per-side limit. Please quote a different message or ask the sender to split the image.",
		LangChinese: "⚠️ 引用消息中的图片过大（%d×%d），超过模型单边 %d px 上限。请改为引用其他消息，或让对方切分图片后重发。",
	},
	MsgSessionAutoResetError: {
		LangEnglish: "🔄 The session failed %d times in a row and has been reset (same as /new). Last message: “%s”. Please resend it.",
		LangChinese: "🔄 会话连续 %d 次处理失败，已自动重置（等同 /new）。上一条消息：『%s』。请重发这条消息。",
	},
	MsgSessionAutoResetRedispatch: {
		LangEnglish: "🔄 The session failed %d times in a row and has been reset (same as /new). Re-processing your last message: “%s”. No need to resend.",
		LangChinese: "🔄 会话连续 %d 次处理失败，已自动重置（等同 /new），正在重新处理你的上一条消息：『%s』，无需重发。",
	},
	MsgRedispatchFailed: {
		LangEnglish: "⚠️ The automatic retry also failed. Please resend your last message manually: “%s”.",
		LangChinese: "⚠️ 重置后的自动重试也失败了，请手动重发上一条消息：『%s』。",
	},
	MsgRedispatchImagesSkipped: {
		LangEnglish: "⚠️ Skipped %d image(s) that would fail the same way. Processing your text now; please split and resend the image separately.",
		LangChinese: "⚠️ 已跳过 %d 张会导致同样失败的图片，先重新处理你的文字；图片请切分后单独重发。",
	},
	MsgRedispatchOnlyImagesSkipped: {
		LangEnglish: "⚠️ This message contained only image(s) that would fail again. Please split the image and resend it.",
		LangChinese: "⚠️ 这条消息只有会导致同样失败的图片，已跳过；请把图片切分后重新发送。",
	},
	MsgSnippetUnknown: {
		LangEnglish: "(unknown content)",
		LangChinese: "（内容未知）",
	},
	MsgSnippetImage: {
		LangEnglish: "[image]",
		LangChinese: "[图片]",
	},
	MsgSnippetFile: {
		LangEnglish: "[file]",
		LangChinese: "[文件]",
	},
	MsgAgentErrorImageTooLarge: {
		LangEnglish: "⚠️ The image exceeds the model's size limit, so it could not be processed. The session will recover automatically; please split the image and resend it.",
		LangChinese: "⚠️ 图片超出模型尺寸上限，本次无法处理。会话会自动恢复；请把图片切分后重发。",
	},
	MsgAgentErrorAuth: {
		LangEnglish: "⚠️ Model authentication failed. Please retry; if it keeps happening, ask the administrator to check the model configuration.",
		LangChinese: "⚠️ 模型鉴权失败，请重试；若持续失败请联系管理员检查模型配置。",
	},
	MsgAgentErrorRateLimit: {
		LangEnglish: "⚠️ The model request was throttled. Please retry in a moment.",
		LangChinese: "⚠️ 模型请求被限流，请稍后重试。",
	},
	MsgAgentErrorTimeout: {
		LangEnglish: "⚠️ The model response timed out. Please retry; if it keeps happening, send /new to start a fresh session.",
		LangChinese: "⚠️ 模型响应超时，请重试；若持续失败可发送 /new 重开会话。",
	},
	MsgAgentErrorContext: {
		LangEnglish: "⚠️ The conversation is longer than the model's context limit. Send /new to start a fresh session, then retry.",
		LangChinese: "⚠️ 上下文已超出模型上限，请发送 /new 重开会话后重试。",
	},
	MsgAgentErrorGeneric: {
		LangEnglish: "❌ This turn failed. Please retry; if it keeps failing, send /new to start a fresh session.",
		LangChinese: "❌ 本轮处理失败，请重试；若持续失败，可发送 /new 重开会话。",
	},
	MsgAgentSendFailed: {
		LangEnglish: "❌ The message could not be sent to the assistant. Please retry; if it keeps failing, send /new.",
		LangChinese: "❌ 消息未能发送给助手，请重试；若持续失败可发送 /new。",
	},
	MsgQueuedMessageDropped: {
		LangEnglish: "⚠️ This message could not be processed. Please send it again.",
		LangChinese: "⚠️ 这条消息未能处理，请重新发送。",
	},
	MsgFinalDeliveryFailed: {
		LangEnglish: "⚠️ The final answer could not be delivered. Please retry, or send /new if it keeps happening.",
		LangChinese: "⚠️ 结论未能送达，请重试；若持续失败可发送 /new 重开会话。",
	},
	MsgFinalDeliveryPartial: {
		LangEnglish: "⚠️ Part of the answer could not be delivered. Please resend your question and I'll regenerate the full answer.",
		LangChinese: "⚠️ 部分内容未能送达，请重发你的问题，我会重新生成完整回答。",
	},
	MsgPreviousTurnFailedRestart: {
		LangEnglish: "⚠️ The previous turn had already failed; this message is being handled as a fresh turn.",
		LangChinese: "⚠️ 上一轮已异常结束，已重新开始处理这条消息。",
	},
	MsgSteerAfterTurnEnd: {
		LangEnglish: "✅ Received — the previous turn had just finished, so this message is being handled as a new turn.",
		LangChinese: "✅ 收到，上一轮刚好结束，这条消息已作为新一轮处理。",
	},
	MsgFailedToDeleteSession: {
		LangEnglish: "❌ %s: %v",
		LangChinese: "❌ %s: %v",
	},
	MsgEmptyResponse: {
		LangEnglish: "(empty response)",
		LangChinese: "(空响应)",
	},
	MsgPermissionPrompt: {
		LangEnglish: "⚠️ **Permission Request**\n\nAgent wants to use **%s**:\n\n```\n%s\n```\n\nReply **allow** / **deny** / **allow all** (skip all future prompts this session).",
		LangChinese: "⚠️ **权限请求**\n\nAgent 想要使用 **%s**:\n\n```\n%s\n```\n\n回复 **允许** / **拒绝** / **允许所有**（本次会话不再提醒）。",
	},
	MsgPermissionAllowed: {
		LangEnglish: "✅ Allowed, continuing...",
		LangChinese: "✅ 已允许，继续执行...",
	},
	MsgPermissionApproveAll: {
		LangEnglish: "✅ All permissions auto-approved for this session.",
		LangChinese: "✅ 本次会话已开启自动批准，后续权限请求将自动允许。",
	},
	MsgPermissionDenied: {
		LangEnglish: "❌ Denied. Agent will stop this tool use.",
		LangChinese: "❌ 已拒绝。Agent 将停止此工具使用。",
	},
	MsgPermissionHint: {
		LangEnglish: "⚠️ Waiting for permission response. Reply **allow** / **deny** / **allow all**.",
		LangChinese: "⚠️ 等待权限响应。请回复 **允许** / **拒绝** / **允许所有**。",
	},
	MsgQuietOn: {
		LangEnglish: "🔇 Quiet mode ON — thinking and tool progress messages will be hidden.",
		LangChinese: "🔇 安静模式已开启 — 将不再推送思考和工具调用进度消息。",
	},
	MsgQuietOff: {
		LangEnglish: "🔔 Quiet mode OFF — thinking and tool progress messages will be shown.",
		LangChinese: "🔔 安静模式已关闭 — 将恢复推送思考和工具调用进度消息。",
	},
	MsgDisplayModeCompact: {
		LangEnglish: "📋 Compact mode — thinking/tool hidden, each text segment sent separately.",
		LangChinese: "📋 紧凑模式 — 隐藏思考和工具消息，每段文本独立发送。",
	},
	MsgQuietGlobalOn: {
		LangEnglish: "🔇 Global quiet mode ON — all sessions will hide thinking and tool progress.",
		LangChinese: "🔇 全局安静模式已开启 — 所有会话将不再推送思考和工具调用进度消息。",
	},
	MsgQuietGlobalOff: {
		LangEnglish: "🔔 Global quiet mode OFF — all sessions will show thinking and tool progress.",
		LangChinese: "🔔 全局安静模式已关闭 — 所有会话将恢复推送思考和工具调用进度消息。",
	},
	MsgModeChanged: {
		LangEnglish: "🔄 Permission mode switched to **%s**. New sessions will use this mode.",
		LangChinese: "🔄 权限模式已切换为 **%s**，新会话将使用此模式。",
	},
	MsgModeNotSupported: {
		LangEnglish: "This agent does not support permission mode switching.",
		LangChinese: "当前 Agent 不支持权限模式切换。",
	},
	MsgSessionRestarting: {
		LangEnglish: "🔄 Session process exited, restarting...",
		LangChinese: "🔄 会话进程已退出，正在重启...",
	},
	MsgSessionNotStarted: {
		LangEnglish: "(new — not yet started)",
		LangChinese: "(新会话 — 尚未开始)",
	},
	MsgUntitled: {
		LangEnglish: "(untitled)",
		LangChinese: "(未命名)",
	},
	MsgLangChanged: {
		LangEnglish: "🌐 Language switched to **%s**.",
		LangChinese: "🌐 语言已切换为 **%s**。",
	},
	MsgLangInvalid: {
		LangEnglish: "Unknown language. Supported: `en`, `zh`, `auto`.",
		LangChinese: "未知语言。支持: `en`, `zh`, `auto`。",
	},
	MsgLangCurrent: {
		LangEnglish: "🌐 Current language: **%s**\n\nUsage: /lang <en|zh|auto>",
		LangChinese: "🌐 当前语言: **%s**\n\n用法: /lang <en|zh|auto>",
	},
	MsgUnknownCommand: {
		LangEnglish: "`%s` is not a cc-connect command, forwarding to agent...",
		LangChinese: "`%s` 不是 cc-connect 命令，已转发给 Agent 处理...",
	},
	MsgWelcome: {
		LangEnglish: "👋 Hi! I'm cc-connect, bridging you to **%s**.\n\nJust send a message to chat with the agent. Type /help to see built-in commands.",
		LangChinese: "👋 你好！我是 cc-connect，已为你连接到 **%s**。\n\n直接发送消息即可与 Agent 对话。输入 /help 查看内置命令。",
	},
	MsgHelp: {
		LangEnglish: "📖 Available Commands\n\n" +
			"/new [name]\n  Start a new session\n\n" +
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
			"/lang [en|zh|auto]\n  View/switch language\n\n" +
			"/compress\n  Compress conversation context\n\n" +
			"/tts [always|voice_only]\n  View/switch text-to-speech mode\n\n" +
			"/shell [--timeout <sec>] <command>\n  Run a shell command and return the output (! prefix shortcut: !cmd)\n\n" +
			"/show <ref>\n  View a file, directory, or code snippet by reference\n\n" +
			"/dir [path|reset]\n  Show, switch, or reset agent working directory\n\n" +
			"/stop\n  Stop current execution\n\n" +
			"/cron [add|list|del|enable|disable]\n  Manage scheduled tasks\n\n" +
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
			"/new [名称]\n  创建新会话\n\n" +
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
			"/lang [en|zh|auto]\n  查看/切换语言\n\n" +
			"/compress\n  压缩会话上下文\n\n" +
			"/tts [always|voice_only]\n  查看/切换语音合成模式\n\n" +
			"/shell [--timeout <秒>] <命令>\n  执行 Shell 命令并返回结果（快捷方式：!命令）\n\n" +
			"/show <引用>\n  按引用查看文件、目录或代码片段\n\n" +
			"/dir [路径|reset]\n  查看、切换或重置 Agent 工作目录\n\n" +
			"/stop\n  停止当前执行\n\n" +
			"/cron [add|list|del|enable|disable]\n  管理定时任务\n\n" +
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
	},
	MsgHelpTitle: {
		LangEnglish: "cc-connect Help",
		LangChinese: "cc-connect 帮助",
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
	},
	MsgHelpToolsSection: {
		LangEnglish: "**Tools & Automation**\n" +
			"/shell <command> — Run a shell command (! shortcut)\n" +
			"/show <ref> — View file / directory / snippet by reference\n" +
			"/dir [path|reset] — Show, switch, or reset work directory\n" +
			"/cron [add|list|del|...] — Scheduled tasks\n" +
			"/commands [add|del] — Custom commands\n" +
			"/alias [add|del] — Command aliases\n" +
			"/skills — List agent skills\n" +
			"/compress — Compress context\n" +
			"/stop — Stop current execution",
		LangChinese: "**工具与自动化**\n" +
			"/shell <命令> — 执行 Shell 命令（!快捷方式）\n" +
			"/show <引用> — 按引用查看文件、目录或代码片段\n" +
			"/dir [路径|reset] — 查看、切换或重置工作目录\n" +
			"/cron [add|list|del|...] — 定时任务\n" +
			"/commands [add|del] — 自定义命令\n" +
			"/alias [add|del] — 命令别名\n" +
			"/skills — 列出 Agent Skills\n" +
			"/compress — 压缩上下文\n" +
			"/stop — 停止当前执行",
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
	},
	MsgHelpTip: {
		LangEnglish: "Tip: Commands support prefix matching, e.g. /pro l = /provider list",
		LangChinese: "提示：命令支持前缀匹配，如 /pro l = /provider list",
	},
	MsgListTitle: {
		LangEnglish: "**%s Sessions** (%d)\n\n",
		LangChinese: "**%s 会话列表** (%d)\n\n",
	},
	MsgListTitlePaged: {
		LangEnglish: "**%s Sessions** (%d) · Page %d/%d\n\n",
		LangChinese: "**%s 会话列表** (%d) · 第 %d/%d 页\n\n",
	},
	MsgListEmpty: {
		LangEnglish: "No sessions found for this project.",
		LangChinese: "未找到此项目的会话。",
	},
	MsgListMore: {
		LangEnglish: "\n... and %d more\n",
		LangChinese: "\n... 还有 %d 条\n",
	},
	MsgListPageHint: {
		LangEnglish: "\n\nPage %d/%d \n\n`/list <page>` for more\n",
		LangChinese: "\n\n第 %d/%d 页 \n\n`/list <页码>` 翻页\n",
	},
	MsgListSwitchHint: {
		LangEnglish: "\n`/switch <number>` to switch session",
		LangChinese: "\n`/switch <序号>` 切换会话",
	},
	MsgListError: {
		LangEnglish: "❌ Failed to list sessions: %v",
		LangChinese: "❌ 获取会话列表失败: %v",
	},
	MsgHistoryEmpty: {
		LangEnglish: "No history in current session.",
		LangChinese: "当前会话暂无历史消息。",
	},
	MsgNameUsage: {
		LangEnglish: "Usage:\n`/name <text>` — name the current session\n`/name <number> <text>` — name a session by list number",
		LangChinese: "用法：\n`/name <名称>` — 命名当前会话\n`/name <序号> <名称>` — 按列表序号命名会话",
	},
	MsgNameSet: {
		LangEnglish: "✅ Session named: **%s** (%s)",
		LangChinese: "✅ 会话已命名：**%s** (%s)",
	},
	MsgNameNoSession: {
		LangEnglish: "❌ No active session. Send a message first or switch to a session.",
		LangChinese: "❌ 没有活跃会话，请先发送消息或切换到一个会话。",
	},
	MsgProviderNotSupported: {
		LangEnglish: "This agent does not support provider switching.",
		LangChinese: "当前 Agent 不支持 Provider 切换。",
	},
	MsgProviderNone: {
		LangEnglish: "No provider configured. Using agent's default environment.\n\nAdd providers in `config.toml` or via `cc-connect provider add`.",
		LangChinese: "未配置 Provider，使用 Agent 默认环境。\n\n可在 `config.toml` 中添加或使用 `cc-connect provider add` 命令。",
	},
	MsgProviderCurrent: {
		LangEnglish: "📡 Active provider: **%s**\n\nUse `/provider list` to see all, `/provider switch <name>` to switch.",
		LangChinese: "📡 当前 Provider: **%s**\n\n使用 `/provider list` 查看全部，`/provider switch <名称>` 切换。",
	},
	MsgProviderListTitle: {
		LangEnglish: "📡 Providers\n\n",
		LangChinese: "📡 Provider 列表\n\n",
	},
	MsgProviderListEmpty: {
		LangEnglish: "No providers configured.\n\nAdd providers in `config.toml` or via `cc-connect provider add`.",
		LangChinese: "未配置 Provider。\n\n可在 `config.toml` 中添加或使用 `cc-connect provider add` 命令。",
	},
	MsgProviderSwitchHint: {
		LangEnglish: "`/provider switch <name>` to switch | `/provider clear` to reset",
		LangChinese: "`/provider switch <名称>` 切换 | `/provider clear` 清除",
	},
	MsgProviderNotFound: {
		LangEnglish: "❌ Provider %q not found. Use `/provider list` to see available providers.",
		LangChinese: "❌ 未找到 Provider %q。使用 `/provider list` 查看可用列表。",
	},
	MsgProviderSwitched: {
		LangEnglish: "✅ Provider switched to **%s**. New sessions will use this provider.",
		LangChinese: "✅ Provider 已切换为 **%s**，新会话将使用此 Provider。",
	},
	MsgProviderCleared: {
		LangEnglish: "✅ Provider cleared. New sessions will use the default provider.",
		LangChinese: "✅ Provider 已清除，新会话将使用默认 Provider。",
	},
	MsgProviderAdded: {
		LangEnglish: "✅ Provider **%s** added.\n\nUse `/provider switch %s` to activate.",
		LangChinese: "✅ Provider **%s** 已添加。\n\n使用 `/provider switch %s` 激活。",
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
	},
	MsgProviderAddFailed: {
		LangEnglish: "❌ Failed to add provider: %v",
		LangChinese: "❌ 添加 Provider 失败: %v",
	},
	MsgProviderRemoved: {
		LangEnglish: "✅ Provider **%s** removed.",
		LangChinese: "✅ Provider **%s** 已移除。",
	},
	MsgProviderRemoveFailed: {
		LangEnglish: "❌ Failed to remove provider: %v",
		LangChinese: "❌ 移除 Provider 失败: %v",
	},
	MsgCardTitleProviderAdd: {
		LangEnglish: "Add Provider", LangChinese: "添加服务商",
	},
	MsgProviderAddPickHint: {
		LangEnglish: "Pick a provider below, or choose **Other** to enter manually.\nAfter selecting, send your API key to complete.",
		LangChinese: "选择一个服务商，或选择 **自定义** 手动填写。\n选择后，请发送你的 API Key 来完成添加。",
	},
	MsgProviderAddOther: {
		LangEnglish: "Other (manual)", LangChinese: "自定义 (手动)",
	},
	MsgProviderAddApiKeyPrompt: {
		LangEnglish: "✅ Selected **%s**.\n\nPlease send your **API Key** for this provider.\nFormat: just the key, e.g. `sk-xxxxxxxx`",
		LangChinese: "✅ 已选择 **%s**。\n\n请发送你的 **API Key**。\n格式：直接发送密钥即可，如 `sk-xxxxxxxx`",
	},
	MsgProviderAddInviteHint: {
		LangEnglish: "🔑 Don't have a key? Register here: %s",
		LangChinese: "🔑 还没有 Key？点击注册获取：%s",
	},
	MsgProviderLinkGlobal: {
		LangEnglish: "Link existing provider", LangChinese: "关联已有服务商",
	},
	MsgProviderLinked: {
		LangEnglish: "✅ Provider **%s** linked to this project.",
		LangChinese: "✅ 已关联服务商 **%s** 到当前项目。",
	},
	MsgVoiceNotEnabled: {
		LangEnglish: "🎙 Voice messages are not enabled. Please configure `[speech]` in config.toml.",
		LangChinese: "🎙 语音消息未启用，请在 config.toml 中配置 `[speech]` 部分。",
	},
	MsgVoiceUsingPlatformRecognition: {
		LangEnglish: "⚠️ Voice transcription not configured, using %s built-in recognition",
		LangChinese: "⚠️ 未配置语音转录，使用 %s 内置语音识别",
	},
	MsgVoiceNoFFmpeg: {
		LangEnglish: "🎙 Voice message requires `ffmpeg` for format conversion. Please install ffmpeg.",
		LangChinese: "🎙 语音消息需要 `ffmpeg` 进行格式转换，请安装 ffmpeg。",
	},
	MsgVoiceTranscribing: {
		LangEnglish: "🎙 Transcribing voice message...",
		LangChinese: "🎙 正在转录语音消息...",
	},
	MsgVoiceTranscribed: {
		LangEnglish: "🎙 [Voice] %s",
		LangChinese: "🎙 [语音] %s",
	},
	MsgVoiceTranscribeFailed: {
		LangEnglish: "🎙 Voice transcription failed: %v",
		LangChinese: "🎙 语音转文字失败: %v",
	},
	MsgVoiceEmpty: {
		LangEnglish: "🎙 Voice message was empty or could not be recognized.",
		LangChinese: "🎙 语音消息为空或无法识别。",
	},
	MsgTTSNotEnabled: {
		LangEnglish: "TTS is not enabled. Please configure `[tts]` in config.toml.",
		LangChinese: "TTS 未启用，请在 config.toml 中配置 `[tts]` 部分。",
	},
	MsgTTSStatus: {
		LangEnglish: "TTS status: enabled=true, mode=%s, provider=%s",
		LangChinese: "TTS 状态：enabled=true，mode=%s，provider=%s",
	},
	MsgTTSSwitched: {
		LangEnglish: "TTS mode switched to: %s",
		LangChinese: "TTS 已切换为 %s 模式",
	},
	MsgTTSUsage: {
		LangEnglish: "Usage: /tts [always|voice_only]",
		LangChinese: "用法：/tts [always|voice_only]",
	},
	MsgHeartbeatNotAvailable: {
		LangEnglish: "Heartbeat is not configured for this project.",
		LangChinese: "当前项目未配置心跳。",
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
	},
	MsgHeartbeatPaused: {
		LangEnglish: "💓 Heartbeat paused.",
		LangChinese: "💓 心跳已暂停。",
	},
	MsgHeartbeatResumed: {
		LangEnglish: "💓 Heartbeat resumed.",
		LangChinese: "💓 心跳已恢复。",
	},
	MsgHeartbeatInterval: {
		LangEnglish: "💓 Heartbeat interval changed to %d minutes.",
		LangChinese: "💓 心跳间隔已调整为 %d 分钟。",
	},
	MsgHeartbeatTriggered: {
		LangEnglish: "💓 Heartbeat triggered.",
		LangChinese: "💓 心跳已触发。",
	},
	MsgHeartbeatUsage: {
		LangEnglish: "Usage: /heartbeat [status|pause|resume|run|interval <mins>]",
		LangChinese: "用法: /heartbeat [status|pause|resume|run|interval <分钟>]",
	},
	MsgHeartbeatInvalidMins: {
		LangEnglish: "Invalid interval. Please provide a positive number of minutes.",
		LangChinese: "无效的间隔。请输入正整数的分钟数。",
	},
	MsgCronNotAvailable: {
		LangEnglish: "Cron scheduler is not available.",
		LangChinese: "定时任务调度器未启用。",
	},
	MsgCronUsage: {
		LangEnglish: "Usage:\n/cron add <min> <hour> <day> <month> <weekday> <prompt>\n/cron list\n/cron del <id>\n/cron enable <id> · /cron disable <id>\n/cron mute <id> · /cron unmute <id>\n/cron setup — write cc-connect instructions to agent memory file",
		LangChinese: "用法：\n/cron add <分> <时> <日> <月> <周> <任务描述>\n/cron list\n/cron del <id>\n/cron enable <id> · /cron disable <id>\n/cron mute <id> · /cron unmute <id> 静音/取消静音\n/cron setup — 将 cc-connect 指令写入 agent 记忆文件",
	},
	MsgCronAddUsage: {
		LangEnglish: "Usage: /cron add <min> <hour> <day> <month> <weekday> <prompt>\nExample: /cron add 0 6 * * * Collect GitHub trending data and send me a summary",
		LangChinese: "用法：/cron add <分> <时> <日> <月> <周> <任务描述>\n示例：/cron add 0 6 * * * 收集 GitHub Trending 数据整理成简报发给我",
	},
	MsgCronAdded: {
		LangEnglish: "✅ Cron job created\nID: `%s`\nSchedule: `%s`\nPrompt: %s",
		LangChinese: "✅ 定时任务已创建\nID: `%s`\n调度: `%s`\n内容: %s",
	},
	MsgCronAddedExec: {
		LangEnglish: "✅ Shell cron job created\nID: `%s`\nSchedule: `%s`\nCommand: `%s`",
		LangChinese: "✅ Shell 定时任务已创建\nID: `%s`\n调度: `%s`\n命令: `%s`",
	},
	MsgCronAddExecUsage: {
		LangEnglish: "Usage: /cron addexec <min> <hour> <day> <month> <weekday> <shell command>\nExample: /cron addexec 0 6 * * * df -h",
		LangChinese: "用法：/cron addexec <分> <时> <日> <月> <周> <shell 命令>\n示例：/cron addexec 0 6 * * * df -h",
	},
	MsgCronEmpty: {
		LangEnglish: "No scheduled tasks.",
		LangChinese: "暂无定时任务。",
	},
	MsgCronListTitle: {
		LangEnglish: "⏰ Scheduled Tasks (%d)",
		LangChinese: "⏰ 定时任务 (%d)",
	},
	MsgCronListFooter: {
		LangEnglish: "`/cron del <id>` remove · `/cron enable/disable <id>` toggle · `/cron mute/unmute <id>` mute",
		LangChinese: "`/cron del <id>` 删除 · `/cron enable/disable <id>` 启停 · `/cron mute/unmute <id>` 静音",
	},
	MsgCronDelUsage: {
		LangEnglish: "Usage: /cron del <id>",
		LangChinese: "用法：/cron del <id>",
	},
	MsgCronDeleted: {
		LangEnglish: "✅ Cron job `%s` deleted.",
		LangChinese: "✅ 定时任务 `%s` 已删除。",
	},
	MsgCronNotFound: {
		LangEnglish: "❌ Cron job `%s` not found.",
		LangChinese: "❌ 定时任务 `%s` 未找到。",
	},
	MsgCronEnabled: {
		LangEnglish: "✅ Cron job `%s` enabled.",
		LangChinese: "✅ 定时任务 `%s` 已启用。",
	},
	MsgCronDisabled: {
		LangEnglish: "⏸ Cron job `%s` disabled.",
		LangChinese: "⏸ 定时任务 `%s` 已暂停。",
	},
	MsgCronMuted: {
		LangEnglish: "🔇 Cron job `%s` muted (all messages suppressed).",
		LangChinese: "🔇 定时任务 `%s` 已静音（所有消息均不发送）。",
	},
	MsgCronUnmuted: {
		LangEnglish: "🔔 Cron job `%s` unmuted.",
		LangChinese: "🔔 定时任务 `%s` 已取消静音。",
	},
	MsgCronCardHint: {
		LangEnglish: "💡 `/cron add` · `/cron del <id>` · `/cron enable/disable <id>` · `/cron mute/unmute <id>`",
		LangChinese: "💡 `/cron add` 添加 · `/cron del <id>` 删除 · `/cron enable/disable <id>` 启停 · `/cron mute/unmute <id>` 静音",
	},
	MsgCronBtnEnable: {
		LangEnglish: "Enable",
		LangChinese: "启用",
	},
	MsgCronBtnDisable: {
		LangEnglish: "Disable",
		LangChinese: "暂停",
	},
	MsgCronBtnMute: {
		LangEnglish: "Mute",
		LangChinese: "静音",
	},
	MsgCronBtnUnmute: {
		LangEnglish: "Unmute",
		LangChinese: "取消静音",
	},
	MsgCronBtnDelete: {
		LangEnglish: "Delete",
		LangChinese: "删除",
	},
	MsgCronNextShort: {
		LangEnglish: "Next",
		LangChinese: "下次",
	},
	MsgCronLastShort: {
		LangEnglish: "Last",
		LangChinese: "上次",
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
	},
	MsgReplyFooterRemaining: {
		LangEnglish: "%d%% left",
		LangChinese: "剩余 %d%%",
	},
	MsgModelCurrent: {
		LangEnglish: "Current model: %s",
		LangChinese: "当前模型: %s",
	},
	MsgModelChanged: {
		LangEnglish: "Model switched to `%s`. New sessions will use this model.",
		LangChinese: "模型已切换为 `%s`，新会话将使用此模型。",
	},
	MsgModelChangeFailed: {
		LangEnglish: "❌ Failed to change model: %v",
		LangChinese: "❌ 切换模型失败: %v",
	},
	MsgModelCardSwitching: {
		LangEnglish: "Switching model to `%s`...",
		LangChinese: "正在切换模型为 `%s`...",
	},
	MsgModelCardSwitched: {
		LangEnglish: "Model switched to `%s`.",
		LangChinese: "模型已切换为 `%s`。",
	},
	MsgModelCardSwitchFailed: {
		LangEnglish: "Failed to switch model: %v",
		LangChinese: "切换模型失败: %v",
	},
	MsgModelNotSupported: {
		LangEnglish: "This agent does not support model switching.",
		LangChinese: "当前 Agent 不支持模型切换。",
	},
	MsgReasoningCurrent: {
		LangEnglish: "Current reasoning effort: %s",
		LangChinese: "当前推理强度: %s",
	},
	MsgReasoningChanged: {
		LangEnglish: "Reasoning effort switched to `%s`. New sessions will use this setting.",
		LangChinese: "推理强度已切换为 `%s`，新会话将使用此设置。",
	},
	MsgReasoningNotSupported: {
		LangEnglish: "This agent does not support reasoning effort switching.",
		LangChinese: "当前 Agent 不支持推理强度切换。",
	},
	MsgMemoryNotSupported: {
		LangEnglish: "This agent does not support memory files.",
		LangChinese: "当前 Agent 不支持记忆文件。",
	},
	MsgMemoryShowProject: {
		LangEnglish: "📝 **Project Memory** (`%s`)\n\n%s",
		LangChinese: "📝 **项目记忆** (`%s`)\n\n%s",
	},
	MsgMemoryShowGlobal: {
		LangEnglish: "📝 **Global Memory** (`%s`)\n\n%s",
		LangChinese: "📝 **全局记忆** (`%s`)\n\n%s",
	},
	MsgMemoryEmpty: {
		LangEnglish: "📝 `%s`\n\n(empty — no content yet)",
		LangChinese: "📝 `%s`\n\n（空 — 尚无内容）",
	},
	MsgMemoryAdded: {
		LangEnglish: "✅ Added to `%s`",
		LangChinese: "✅ 已追加到 `%s`",
	},
	MsgMemoryAddFailed: {
		LangEnglish: "❌ Failed to write memory file: %v",
		LangChinese: "❌ 写入记忆文件失败: %v",
	},
	MsgUsageNotSupported: {
		LangEnglish: "Current agent does not support `/usage`.",
		LangChinese: "当前 Agent 不支持 `/usage`。",
	},
	MsgUsageFetchFailed: {
		LangEnglish: "Failed to fetch usage: %v",
		LangChinese: "获取 usage 失败：%v",
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
	},
	MsgCompressNotSupported: {
		LangEnglish: "This agent does not support context compression.",
		LangChinese: "当前 Agent 不支持上下文压缩。可以使用 `/new` 开始新会话。",
	},
	MsgCompressing: {
		LangEnglish: "🗜 Compressing context...",
		LangChinese: "🗜 正在压缩上下文...",
	},
	MsgCompressNoSession: {
		LangEnglish: "No active session to compress. Send a message first.",
		LangChinese: "没有活跃的会话可以压缩。请先发送一条消息。",
	},
	MsgCompressDone: {
		LangEnglish: "✅ Context compressed.",
		LangChinese: "✅ 上下文压缩完成。",
	},

	// Inline strings for engine.go commands
	MsgStatusMode: {
		LangEnglish: "Mode: %s\n",
		LangChinese: "权限模式: %s\n",
	},
	MsgStatusSession: {
		LangEnglish: "Session: %s (messages: %d)\n",
		LangChinese: "当前会话: %s (消息: %d)\n",
	},
	MsgStatusCron: {
		LangEnglish: "Cron jobs: %d (enabled: %d)\n",
		LangChinese: "定时任务: %d (启用: %d)\n",
	},
	MsgStatusThinkingMessages: {
		LangEnglish: "Thinking messages: %s\n",
		LangChinese: "思考消息: %s\n",
	},
	MsgStatusToolMessages: {
		LangEnglish: "Tool progress: %s\n",
		LangChinese: "工具进度: %s\n",
	},
	MsgStatusSessionKey: {
		LangEnglish: "Session Key: `%s`\n",
		LangChinese: "会话 Key: `%s`\n",
	},
	MsgStatusAgentSID: {
		LangEnglish: "Agent SID: `%s`\n",
		LangChinese: "Agent SID: `%s`\n",
	},
	MsgStatusUserID: {
		LangEnglish: "User ID: `%s`\n",
		LangChinese: "User ID: `%s`\n",
	},
	MsgEnabledShort: {
		LangEnglish: "ON",
		LangChinese: "开启",
	},
	MsgDisabledShort: {
		LangEnglish: "OFF",
		LangChinese: "关闭",
	},
	MsgModelDefault: {
		LangEnglish: "Current model: (not set, using agent default)\n",
		LangChinese: "当前模型: (未设置，使用 Agent 默认值)\n",
	},
	MsgModelListTitle: {
		LangEnglish: "Available models:\n",
		LangChinese: "可用模型:\n",
	},
	MsgModelUsage: {
		LangEnglish: "Usage: `/model switch <number>` or `/model switch <model_name>`",
		LangChinese: "用法: `/model switch <序号>` 或 `/model switch <模型名>`",
	},
	MsgReasoningDefault: {
		LangEnglish: "Current reasoning effort: (not set, using Codex default)\n",
		LangChinese: "当前推理强度: (未设置，使用 Codex 默认值)\n",
	},
	MsgReasoningListTitle: {
		LangEnglish: "Available reasoning levels:\n",
		LangChinese: "可用推理强度:\n",
	},
	MsgReasoningUsage: {
		LangEnglish: "Usage: `/reasoning <number>` or `/reasoning <low|medium|high|xhigh>`",
		LangChinese: "用法: `/reasoning <序号>` 或 `/reasoning <low|medium|high|xhigh>`",
	},
	MsgModeUsage: {
		LangEnglish: "\nUse `/mode <name>` to switch.\nAvailable: %s",
		LangChinese: "\n使用 `/mode <名称>` 切换模式\n可用值: %s",
	},
	MsgLangSelectPlaceholder: {
		LangEnglish: "Select language", LangChinese: "选择语言",
	},
	MsgModelSelectPlaceholder: {
		LangEnglish: "Select model", LangChinese: "选择模型",
	},
	MsgReasoningSelectPlaceholder: {
		LangEnglish: "Select reasoning level", LangChinese: "选择推理强度",
	},
	MsgModeSelectPlaceholder: {
		LangEnglish: "Select mode", LangChinese: "选择模式",
	},
	MsgProviderSelectPlaceholder: {
		LangEnglish: "Select provider", LangChinese: "选择 Provider",
	},
	MsgProviderClearOption: {
		LangEnglish: "Do not use provider", LangChinese: "不使用服务商",
	},
	MsgCardBack: {
		LangEnglish: "← Back", LangChinese: "← 返回",
	},
	MsgCardPrev: {
		LangEnglish: "← Prev", LangChinese: "← 上一页",
	},
	MsgCardNext: {
		LangEnglish: "Next →", LangChinese: "下一页 →",
	},
	MsgCardTitleStatus: {
		LangEnglish: "cc-connect Status", LangChinese: "cc-connect 状态",
	},
	MsgCardTitleLanguage: {
		LangEnglish: "Language", LangChinese: "语言",
	},
	MsgCardTitleModel: {
		LangEnglish: "Model", LangChinese: "模型",
	},
	MsgCardTitleReasoning: {
		LangEnglish: "Reasoning", LangChinese: "推理强度",
	},
	MsgCardTitleMode: {
		LangEnglish: "Permission Mode", LangChinese: "权限模式",
	},
	MsgCardTitleSessions: {
		LangEnglish: "%s Sessions (%d)", LangChinese: "%s 会话列表 (%d)",
	},
	MsgCardTitleSessionsPaged: {
		LangEnglish: "%s Sessions (%d) — %d/%d", LangChinese: "%s 会话列表 (%d) · 第 %d/%d 页",
	},
	MsgCardTitleCurrentSession: {
		LangEnglish: "Current Session", LangChinese: "当前会话",
	},
	MsgCardTitleHistory: {
		LangEnglish: "History", LangChinese: "历史记录",
	},
	MsgCardTitleHistoryLast: {
		LangEnglish: "History (last %d)", LangChinese: "历史记录（最近 %d 条）",
	},
	MsgCardTitleProvider: {
		LangEnglish: "Provider", LangChinese: "Provider",
	},
	MsgCardTitleCron: {
		LangEnglish: "Cron", LangChinese: "定时任务",
	},
	MsgCardTitleHeartbeat: {
		LangEnglish: "Heartbeat", LangChinese: "心跳",
	},
	MsgCardTitleCommands: {
		LangEnglish: "Commands", LangChinese: "命令",
	},
	MsgCardTitleAlias: {
		LangEnglish: "Alias", LangChinese: "别名",
	},
	MsgCardTitleConfig: {
		LangEnglish: "Config", LangChinese: "配置",
	},
	MsgCardTitleSkills: {
		LangEnglish: "Skills", LangChinese: "Skills",
	},
	MsgCardTitleDoctor: {
		LangEnglish: "Doctor", LangChinese: "系统诊断",
	},
	MsgCardTitleVersion: {
		LangEnglish: "Version", LangChinese: "版本",
	},
	MsgCardTitleUpgrade: {
		LangEnglish: "Upgrade", LangChinese: "升级",
	},
	MsgListItem: {
		LangEnglish: "%s **%d.** %s · **%d** msgs · %s",
		LangChinese: "%s **%d.** %s · **%d** 条消息 · %s",
	},
	MsgListEmptySummary: {
		LangEnglish: "(empty)", LangChinese: "（空）",
	},
	MsgCronIDLabel: {
		LangEnglish: "ID: %s\n", LangChinese: "ID：%s\n",
	},
	MsgCronFailedSuffix: {
		LangEnglish: " (failed: %s)", LangChinese: "（失败：%s）",
	},
	MsgCommandsTagAgent: {
		LangEnglish: " [agent]", LangChinese: " [代理]",
	},
	MsgCommandsTagShell: {
		LangEnglish: " [shell]", LangChinese: " [终端]",
	},
	MsgUpgradeTimeoutSuffix: {
		LangEnglish: " (timeout)", LangChinese: "（超时）",
	},
	MsgCronScheduleLabel: {
		LangEnglish: "Schedule: %s `%s`\n",
		LangChinese: "调度: %s `%s`\n",
	},
	MsgCronNextRunLabel: {
		LangEnglish: "Next run: %s\n",
		LangChinese: "下次执行: %s\n",
	},
	MsgCronLastRunLabel: {
		LangEnglish: "Last run: %s",
		LangChinese: "上次执行: %s",
	},
	MsgPermBtnAllow: {
		LangEnglish: "Allow",
		LangChinese: "允许",
	},
	MsgPermBtnDeny: {
		LangEnglish: "Deny",
		LangChinese: "拒绝",
	},
	MsgPermBtnAllowAll: {
		LangEnglish: "Allow All (this session)",
		LangChinese: "允许所有 (本次会话)",
	},
	MsgPermCardTitle: {
		LangEnglish: "Permission Request",
		LangChinese: "权限请求",
	},
	MsgPermCardBody: {
		LangEnglish: "Agent wants to use **%s**:\n\n```\n%s\n```",
		LangChinese: "Agent 想要使用 **%s**:\n\n```\n%s\n```",
	},
	MsgPermCardNote: {
		LangEnglish: "If buttons are unresponsive, reply: allow / deny / allow all",
		LangChinese: "如果按钮无响应，请直接回复：允许 / 拒绝 / 允许所有",
	},
	MsgAskQuestionTitle: {
		LangEnglish: "Agent Question",
		LangChinese: "Agent 提问",
	},
	MsgAskQuestionNote: {
		LangEnglish: "If buttons are unresponsive, reply with the option number (e.g. 1) or type your answer",
		LangChinese: "如果按钮无响应，请回复选项编号（如 1）或直接输入你的回答",
	},
	MsgAskQuestionMulti: {
		LangEnglish: " (multiple selections allowed, separate with commas)",
		LangChinese: "（可多选，用逗号分隔）",
	},
	MsgAskQuestionPrompt: {
		LangEnglish: "❓ **%s**\n\n%s\n\nReply with the option number or type your answer.",
		LangChinese: "❓ **%s**\n\n%s\n\n请回复选项编号或直接输入你的回答。",
	},
	MsgAskQuestionAnswered: {
		LangEnglish: "Answer",
		LangChinese: "已回答",
	},
	MsgCommandsTitle: {
		LangEnglish: "🔧 **Custom Commands** (%d)\n\n",
		LangChinese: "🔧 **自定义命令** (%d)\n\n",
	},
	MsgCommandsEmpty: {
		LangEnglish: "No custom commands configured.\n\nUse `/commands add <name> <prompt>` or add `[[commands]]` in config.toml.",
		LangChinese: "未配置自定义命令。\n\n使用 `/commands add <名称> <prompt>` 添加，或在 config.toml 中配置 `[[commands]]`。",
	},
	MsgCommandsHint: {
		LangEnglish: "Type `/<name> [args]` to use.\n`/commands add <name> <prompt>` to add prompt command\n`/commands addexec <name> <shell>` to add exec command\n`/commands del <name>` to remove",
		LangChinese: "输入 `/<名称> [参数]` 使用。\n`/commands add <名称> <prompt>` 添加 prompt 命令\n`/commands addexec <名称> <shell命令>` 添加 exec 命令\n`/commands del <名称>` 删除",
	},
	MsgCommandsUsage: {
		LangEnglish: "Usage:\n`/commands` — list all custom commands\n`/commands add <name> <prompt>` — add prompt command\n`/commands addexec <name> <shell>` — add exec command\n`/commands del <name>` — remove a command",
		LangChinese: "用法：\n`/commands` — 列出所有自定义命令\n`/commands add <名称> <prompt>` — 添加 prompt 命令\n`/commands addexec <名称> <shell命令>` — 添加 exec 命令\n`/commands del <名称>` — 删除命令",
	},
	MsgCommandsAddUsage: {
		LangEnglish: "Usage: `/commands add <name> <prompt template>`\n\nExample: `/commands add finduser Search the database for user「{{1}}」`",
		LangChinese: "用法：`/commands add <名称> <prompt 模板>`\n\n示例：`/commands add finduser 在数据库中查找用户「{{1}}」`",
	},
	MsgCommandsAddExecUsage: {
		LangEnglish: "Usage: `/commands addexec <name> <shell command>`\n         `/commands addexec --work-dir <dir> <name> <shell command>`\n\nExamples:\n`/commands addexec push git push`\n`/commands addexec status git status {{args}}`",
		LangChinese: "用法：`/commands addexec <名称> <shell 命令>`\n      `/commands addexec --work-dir <目录> <名称> <shell 命令>`\n\n示例：\n`/commands addexec push git push`\n`/commands addexec status git status {{args}}`",
	},
	MsgCommandsAdded: {
		LangEnglish: "✅ Command `/%s` added.\nPrompt: %s",
		LangChinese: "✅ 命令 `/%s` 已添加。\nPrompt: %s",
	},
	MsgCommandsAddExists: {
		LangEnglish: "❌ Command `/%s` already exists. Remove it first with `/commands del %s`.",
		LangChinese: "❌ 命令 `/%s` 已存在。请先使用 `/commands del %s` 删除。",
	},
	MsgCommandsDelUsage: {
		LangEnglish: "Usage: `/commands del <name>`",
		LangChinese: "用法：`/commands del <名称>`",
	},
	MsgCommandsDeleted: {
		LangEnglish: "✅ Command `/%s` removed.",
		LangChinese: "✅ 命令 `/%s` 已删除。",
	},
	MsgCommandsNotFound: {
		LangEnglish: "❌ Command `/%s` not found. Use `/commands` to see available commands.",
		LangChinese: "❌ 命令 `/%s` 未找到。使用 `/commands` 查看可用命令。",
	},
	MsgCommandsExecAdded: {
		LangEnglish: "✅ Exec command `/%s` added.\nCommand: %s",
		LangChinese: "✅ Exec 命令 `/%s` 已添加。\n命令: %s",
	},
	MsgCommandExecTimeout: {
		LangEnglish: "⏱️ Command `/%s` timed out (60s limit).",
		LangChinese: "⏱️ 命令 `/%s` 超时（60秒限制）。",
	},
	MsgCommandExecError: {
		LangEnglish: "❌ Command `/%s` failed:\n%s",
		LangChinese: "❌ 命令 `/%s` 执行失败：\n%s",
	},
	MsgCommandExecSuccess: {
		LangEnglish: "✅ Command executed successfully (no output).",
		LangChinese: "✅ 命令执行成功（无输出）。",
	},
	MsgSkillsTitle: {
		LangEnglish: "📋 Available Skills (%s) — %d skill(s)\n\n",
		LangChinese: "📋 可用 Skills (%s) — %d 个\n\n",
	},
	MsgSkillsEmpty: {
		LangEnglish: "No skills found.\nSkills are discovered from agent directories (e.g. .claude/skills/<name>/SKILL.md).",
		LangChinese: "未发现任何 Skill。\nSkill 从 Agent 目录自动发现（如 .claude/skills/<name>/SKILL.md）。",
	},
	MsgSkillsHint: {
		LangEnglish: "Usage: /<skill-name> [args...] to invoke a skill.",
		LangChinese: "用法：/<skill名称> [参数...] 来调用 Skill。",
	},

	MsgConfigTitle: {
		LangEnglish: "⚙️ **Runtime Configuration**\n\n",
		LangChinese: "⚙️ **运行时配置**\n\n",
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
	},
	MsgConfigGetUsage: {
		LangEnglish: "Usage: `/config get thinking_max_len`",
		LangChinese: "用法：`/config get thinking_max_len`",
	},
	MsgConfigSetUsage: {
		LangEnglish: "Usage: `/config set thinking_max_len 200`",
		LangChinese: "用法：`/config set thinking_max_len 200`",
	},
	MsgConfigUpdated: {
		LangEnglish: "✅ `%s` → `%s`",
		LangChinese: "✅ `%s` → `%s`",
	},
	MsgConfigKeyNotFound: {
		LangEnglish: "❌ Unknown config key `%s`. Use `/config` to see available keys.",
		LangChinese: "❌ 未知配置项 `%s`。使用 `/config` 查看可用配置。",
	},
	MsgConfigReloaded: {
		LangEnglish: "✅ Config reloaded\n\nDisplay updated: %v\nProviders synced: %d\nCommands synced: %d",
		LangChinese: "✅ 配置已重新加载\n\n显示设置已更新：%v\nProvider 已同步：%d 个\n自定义命令已同步：%d 个",
	},
	MsgDoctorRunning: {
		LangEnglish: "🏥 Running diagnostics...",
		LangChinese: "🏥 正在运行系统诊断...",
	},
	MsgDoctorTitle: {
		LangEnglish: "🏥 **System Diagnostic Report**\n\n",
		LangChinese: "🏥 **系统诊断报告**\n\n",
	},
	MsgDoctorSummary: {
		LangEnglish: "\n✅ %d passed  ⚠️ %d warnings  ❌ %d failed",
		LangChinese: "\n✅ %d 项通过  ⚠️ %d 项警告  ❌ %d 项失败",
	},
	MsgRestarting: {
		LangEnglish: "🔄 Restarting cc-connect...",
		LangChinese: "🔄 正在重启 cc-connect...",
	},
	MsgRestartSuccess: {
		LangEnglish: "✅ cc-connect restarted successfully.",
		LangChinese: "✅ cc-connect 重启成功。",
	},
	MsgUpgradeChecking: {
		LangEnglish: "🔍 Checking for updates...",
		LangChinese: "🔍 正在检查更新...",
	},
	MsgUpgradeUpToDate: {
		LangEnglish: "✅ Already up to date (%s)",
		LangChinese: "✅ 已是最新版本 (%s)",
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
	},
	MsgUpgradeDownloading: {
		LangEnglish: "⬇️ Downloading %s ...",
		LangChinese: "⬇️ 正在下载 %s ...",
	},
	MsgUpgradeSuccess: {
		LangEnglish: "✅ Updated to **%s** successfully! Restarting...",
		LangChinese: "✅ 已成功更新到 **%s**！正在重启...",
	},
	MsgUpgradeDevBuild: {
		LangEnglish: "⚠️ Running a dev build — version check is not available. Please build from source or install a release version.",
		LangChinese: "⚠️ 当前为开发版本，无法检查更新。请从源码构建或安装正式发布版本。",
	},
	MsgWebNotSupported: {
		LangEnglish: "⚠️ Web admin is not available in this build. Rebuild without the `no_web` tag to enable it.",
		LangChinese: "⚠️ 当前版本未包含 Web 管理后台。请去掉 `no_web` 标签重新编译以启用。",
	},
	MsgWebNotEnabled: {
		LangEnglish: "ℹ️ Web admin is not enabled.\n\nUse `/web setup` to configure and enable it.",
		LangChinese: "ℹ️ Web 管理后台未启用。\n\n使用 `/web setup` 配置并启用。",
	},
	MsgWebSetupSuccess: {
		LangEnglish: "✅ Web admin configured!\n\n" +
			"🌐 URL: %s\n🔑 Token: `%s`\n\n" +
			"Open the URL in your browser and use the token to log in.",
		LangChinese: "✅ Web 管理后台配置完成！\n\n" +
			"🌐 地址：%s\n🔑 令牌：`%s`\n\n" +
			"在浏览器打开地址，使用令牌登录。",
	},
	MsgWebNeedRestart: {
		LangEnglish: "🔄 Restart the service with `/restart` to activate the web admin.",
		LangChinese: "🔄 请使用 `/restart` 重启服务以激活 Web 管理后台。",
	},
	MsgWebStatus: {
		LangEnglish: "🌐 **Web Admin**\n\nURL: %s",
		LangChinese: "🌐 **Web 管理后台**\n\n地址：%s",
	},
	MsgAliasEmpty: {
		LangEnglish: "No aliases configured. Use `/alias add <trigger> <command>` to create one.",
		LangChinese: "暂无别名配置。使用 `/alias add <触发词> <命令>` 创建别名。",
	},
	MsgAliasListHeader: {
		LangEnglish: "📎 Aliases (%d)",
		LangChinese: "📎 命令别名 (%d)",
	},
	MsgAliasAdded: {
		LangEnglish: "✅ Alias added: %s → %s",
		LangChinese: "✅ 别名已添加：%s → %s",
	},
	MsgAliasDeleted: {
		LangEnglish: "✅ Alias removed: %s",
		LangChinese: "✅ 别名已删除：%s",
	},
	MsgAliasNotFound: {
		LangEnglish: "❌ Alias `%s` not found.",
		LangChinese: "❌ 别名 `%s` 不存在。",
	},
	MsgAliasUsage: {
		LangEnglish: "Usage:\n  `/alias` — list all aliases\n  `/alias add <trigger> <command>` — add alias\n  `/alias del <trigger>` — remove alias\n\nExample: `/alias add 帮助 /help`",
		LangChinese: "用法：\n  `/alias` — 列出所有别名\n  `/alias add <触发词> <命令>` — 添加别名\n  `/alias del <触发词>` — 删除别名\n\n示例：`/alias add 帮助 /help`",
	},
	MsgNewSessionCreated: {
		LangEnglish: "✅ New session created",
		LangChinese: "✅ 新会话已创建",
	},
	MsgNewSessionCreatedName: {
		LangEnglish: "✅ New session created: **%s**",
		LangChinese: "✅ 新会话已创建：**%s**",
	},
	MsgSessionAutoResetIdle: {
		LangEnglish: "⏰ Session auto-reset after %d minute(s) of inactivity.",
		LangChinese: "⏰ 因空闲超过 %d 分钟，已自动切换到新会话。",
	},
	MsgSessionClosingGraceful: {
		LangEnglish: "⏳ Wrapping up your previous session (usually a few seconds, up to 2 minutes). Your new session will start automatically.",
		LangChinese: "⏳ 正在结束上一个会话（通常几秒钟，最多2分钟）。新会话将自动启动。",
	},
	MsgDeleteUsage: {
		LangEnglish: "Usage: `/delete <number>` or `/delete 1,2,3` or `/delete 3-7` or `/delete 1,3-5,8`.\nUse `/list` to see session numbers.",
		LangChinese: "用法：`/delete <序号>`，或 `/delete 1,2,3`，或 `/delete 3-7`，或 `/delete 1,3-5,8`。\n使用 `/list` 查看会话序号。",
	},
	MsgDeleteSuccess: {
		LangEnglish: "🗑️ Session deleted: %s",
		LangChinese: "🗑️ 会话已删除：%s",
	},
	MsgSwitchSuccess: {
		LangEnglish: "✅ Switched to: %s (%s, %d msgs)",
		LangChinese: "✅ 已切换到：%s（%s，%d 条消息）",
	},
	MsgSwitchNoMatch: {
		LangEnglish: "❌ No session matching %q",
		LangChinese: "❌ 没有找到匹配 %q 的会话",
	},
	MsgSwitchNoSession: {
		LangEnglish: "❌ No session #%d",
		LangChinese: "❌ 没有第 %d 个会话",
	},
	MsgCommandTimeout: {
		LangEnglish: "⏰ Command timed out (60s): `%s`",
		LangChinese: "⏰ 命令超时 (60秒): `%s`",
	},
	MsgDeleteActiveDenied: {
		LangEnglish: "❌ Cannot delete the currently active session. Switch to another session first.",
		LangChinese: "❌ 不能删除当前活跃会话，请先切换到其他会话。",
	},
	MsgDeleteNotSupported: {
		LangEnglish: "❌ This agent does not support session deletion.",
		LangChinese: "❌ 当前 Agent 不支持删除会话。",
	},
	MsgDeleteModeTitle: {
		LangEnglish: "Delete Sessions",
		LangChinese: "删除会话",
	},
	MsgDeleteModeSelect: {
		LangEnglish: "Select",
		LangChinese: "选择",
	},
	MsgDeleteModeSelected: {
		LangEnglish: "Selected",
		LangChinese: "已选",
	},
	MsgDeleteModeSelectedCount: {
		LangEnglish: "%d selected",
		LangChinese: "已选 %d 项",
	},
	MsgDeleteModeDeleteSelected: {
		LangEnglish: "Delete Selected",
		LangChinese: "删除已选",
	},
	MsgDeleteModeCancel: {
		LangEnglish: "Cancel",
		LangChinese: "取消",
	},
	MsgDeleteModeConfirmTitle: {
		LangEnglish: "Confirm Delete",
		LangChinese: "确认删除",
	},
	MsgDeleteModeConfirmButton: {
		LangEnglish: "Confirm Delete",
		LangChinese: "确认删除",
	},
	MsgDeleteModeBackButton: {
		LangEnglish: "Back",
		LangChinese: "返回继续选择",
	},
	MsgDeleteModeEmptySelection: {
		LangEnglish: "Select at least one session.",
		LangChinese: "请至少选择一个会话。",
	},
	MsgDeleteModeResultTitle: {
		LangEnglish: "Delete Result",
		LangChinese: "删除结果",
	},
	MsgDeleteModeDeletingTitle: {
		LangEnglish: "Deleting Sessions...",
		LangChinese: "正在删除会话...",
	},
	MsgDeleteModeDeletingBody: {
		LangEnglish: "Deleting %d session(s), please wait...",
		LangChinese: "正在删除 %d 个会话，请稍候...",
	},
	MsgDeleteModeMissingSession: {
		LangEnglish: "❌ Missing selected session: %s",
		LangChinese: "❌ 已选会话不存在：%s",
	},
	MsgBannedWordBlocked: {
		LangEnglish: "⚠️ Your message was blocked because it contains a prohibited word.",
		LangChinese: "⚠️ 消息已被拦截，包含违禁词。",
	},
	MsgCommandDisabled: {
		LangEnglish: "🚫 Command `%s` is disabled for this project.",
		LangChinese: "🚫 命令 `%s` 在当前项目中已被禁用。",
	},
	MsgAdminRequired: {
		LangEnglish: "🔒 Command `%s` requires admin privilege. Set `admin_from` in config to authorize users.",
		LangChinese: "🔒 命令 `%s` 需要管理员权限。请在配置中设置 `admin_from` 来授权用户。",
	},
	MsgRateLimited: {
		LangEnglish: "⏳ You are sending messages too fast. Please wait a moment.",
		LangChinese: "⏳ 消息发送过快，请稍后再试。",
	},
	MsgPsSent: {
		LangEnglish: "✅ P.S. delivered.",
		LangChinese: "✅ P.S. 已送达。",
	},
	MsgPsSendFailed: {
		LangEnglish: "❌ Failed to deliver P.S.",
		LangChinese: "❌ P.S. 发送失败。",
	},
	MsgPsEmpty: {
		LangEnglish: "Usage: `/ps <message>`",
		LangChinese: "用法：`/ps <消息>`",
	},
	MsgPsNoSession: {
		LangEnglish: "No task is currently running.",
		LangChinese: "当前没有正在执行的任务。",
	},
	MsgWhoamiTitle: {
		LangEnglish: "🪪 **Your Identity**",
		LangChinese: "🪪 **你的身份信息**",
	},
	MsgWhoamiCardTitle: {
		LangEnglish: "Your Identity",
		LangChinese: "你的身份信息",
	},
	MsgWhoamiName: {
		LangEnglish: "Name",
		LangChinese: "名称",
	},
	MsgWhoamiPlatform: {
		LangEnglish: "Platform",
		LangChinese: "平台",
	},
	MsgWhoamiUsage: {
		LangEnglish: "💡 Use the `User ID` above for `allow_from` and `admin_from` in your `config.toml`.",
		LangChinese: "💡 可将上方 `User ID` 填入 `config.toml` 的 `allow_from` 或 `admin_from` 中。",
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
		LangEnglish: "✅ Removed %s from binding",
		LangChinese: "✅ 已从绑定中移除 %s",
	},
	MsgRelayBindNotFound: {
		LangEnglish: "❌ %s is not bound or binding does not exist",
		LangChinese: "❌ %s 未绑定或绑定不存在",
	},
	MsgRelayBindSuccess: {
		LangEnglish: "✅ Bind successful! Current group bound: %s\n\nYou can now ask this bot to communicate with %s.\nExample: \"Ask %s about ...\"",
		LangChinese: "✅ 绑定成功！当前群组已绑定: %s\n\n你现在可以让本机器人去询问 %s。\n示例：\"帮我问一下 %s ...\"",
	},
	MsgRelaySetupHint: {
		LangEnglish: "\n\n⚠️ This agent does not auto-inject cc-connect instructions.\nPlease run `/bind setup` or `/cron setup` to write instructions to %s.",
		LangChinese: "\n\n⚠️ 当前 agent 不会自动注入 cc-connect 指令。\n请运行 `/bind setup` 或 `/cron setup` 将指令写入 %s。",
	},
	MsgRelaySetupOK: {
		LangEnglish: "✅ cc-connect instructions written to %s\nThe agent can now use relay, cron, and attachment send-back.",
		LangChinese: "✅ cc-connect 指令已写入 %s\nagent 现在可以使用中继、定时任务和附件回传功能了。",
	},
	MsgRelaySetupExists: {
		LangEnglish: "ℹ️ cc-connect instructions already exist in %s — no changes made.",
		LangChinese: "ℹ️ cc-connect 指令已存在于 %s 中，无需重复写入。",
	},
	MsgRelaySetupNoMemory: {
		LangEnglish: "❌ This agent does not support instruction files.",
		LangChinese: "❌ 当前 agent 不支持指令文件。",
	},
	MsgSetupNative: {
		LangEnglish: "✅ This agent natively supports cc-connect instructions — no setup needed.",
		LangChinese: "✅ 当前 agent 已原生支持 cc-connect 指令，无需额外配置。",
	},
	MsgCronSetupOK: {
		LangEnglish: "✅ cc-connect instructions written to %s\nThe agent can now use relay, cron, and attachment send-back.",
		LangChinese: "✅ cc-connect 指令已写入 %s\nagent 现在可以使用中继、定时任务和附件回传功能了。",
	},
	MsgSearchUsage: {
		LangEnglish: "Usage: /search <keyword>\nSearch sessions by name or ID.",
		LangChinese: "用法: /search <关键词>\n搜索会话名称或 ID。",
	},
	MsgSearchError: {
		LangEnglish: "❌ Search error: %v",
		LangChinese: "❌ 搜索失败: %v",
	},
	MsgSearchNoResult: {
		LangEnglish: "No sessions found matching %q",
		LangChinese: "没有找到匹配 %q 的会话",
	},
	MsgSearchResult: {
		LangEnglish: "🔍 Found %d session(s) matching %q:",
		LangChinese: "🔍 找到 %d 个匹配 %q 的会话:",
	},
	MsgSearchHint: {
		LangEnglish: "Use /switch <id> to switch to a session.",
		LangChinese: "使用 /switch <id> 切换到对应会话。",
	},
	// Builtin command descriptions
	MsgBuiltinCmdNew: {
		LangEnglish: "Start a new session, arg: [name]",
		LangChinese: "创建新会话，参数: [名称]",
	},
	MsgBuiltinCmdList: {
		LangEnglish: "List agent sessions",
		LangChinese: "列出 Agent 会话列表",
	},
	MsgBuiltinCmdSearch: {
		LangEnglish: "Search sessions by name or ID, arg: <keyword>",
		LangChinese: "搜索会话名称或 ID，参数: <关键词>",
	},
	MsgBuiltinCmdSwitch: {
		LangEnglish: "Resume a session by its list number, arg: <number>",
		LangChinese: "按列表序号切换会话，参数: <序号>",
	},
	MsgBuiltinCmdDelete: {
		LangEnglish: "Delete session(s) by list number, args: <number> | 1,2,3 | 3-7 | 1,3-5,8",
		LangChinese: "按列表序号删除会话，参数: <序号> | 1,2,3 | 3-7 | 1,3-5,8",
	},
	MsgBuiltinCmdName: {
		LangEnglish: "Name a session for easy identification, arg: [number] <text>",
		LangChinese: "给会话命名，方便识别，参数: [序号] <名称>",
	},
	MsgBuiltinCmdCurrent: {
		LangEnglish: "Show current active session",
		LangChinese: "查看当前活跃会话",
	},
	MsgBuiltinCmdHistory: {
		LangEnglish: "Show last n messages, arg: [n] (default 10)",
		LangChinese: "查看最近 n 条消息，参数: [n]（默认 10）",
	},
	MsgBuiltinCmdProvider: {
		LangEnglish: "Manage API providers, arg: [list|add|remove|switch|clear]",
		LangChinese: "管理 API Provider，参数: [list|add|remove|switch|clear]",
	},
	MsgBuiltinCmdMemory: {
		LangEnglish: "View/edit agent memory files, arg: [add|global|global add]",
		LangChinese: "查看/编辑 Agent 记忆文件，参数: [add|global|global add]",
	},
	MsgBuiltinCmdAllow: {
		LangEnglish: "Pre-allow a tool (next session), arg: <tool>",
		LangChinese: "预授权工具（下次会话生效），参数: <工具名>",
	},
	MsgBuiltinCmdModel: {
		LangEnglish: "View/switch model, arg: [name]",
		LangChinese: "查看/切换模型，参数: [名称]",
	},
	MsgBuiltinCmdReasoning: {
		LangEnglish: "View/switch reasoning effort, arg: [level]",
		LangChinese: "查看/切换推理强度，参数: [等级]",
	},
	MsgBuiltinCmdMode: {
		LangEnglish: "View/switch permission mode, arg: [name]",
		LangChinese: "查看/切换权限模式，参数: [名称]",
	},
	MsgBuiltinCmdLang: {
		LangEnglish: "View/switch language, arg: [en|zh|auto]",
		LangChinese: "查看/切换语言，参数: [en|zh|auto]",
	},
	MsgBuiltinCmdQuiet: {
		LangEnglish: "Toggle thinking/tool progress, arg: [global]",
		LangChinese: "开关思考和工具进度消息, 参数: [global]",
	},
	MsgBuiltinCmdCompress: {
		LangEnglish: "Compress conversation context",
		LangChinese: "压缩会话上下文",
	},
	MsgBuiltinCmdStop: {
		LangEnglish: "Stop current execution",
		LangChinese: "停止当前执行",
	},
	MsgBuiltinCmdCron: {
		LangEnglish: "Manage scheduled tasks, arg: [add|list|del|enable|disable]",
		LangChinese: "管理定时任务，参数: [add|list|del|enable|disable]",
	},
	MsgBuiltinCmdCommands: {
		LangEnglish: "Manage custom slash commands, arg: [add|del]",
		LangChinese: "管理自定义命令，参数: [add|del]",
	},
	MsgBuiltinCmdAlias: {
		LangEnglish: "Manage command aliases, arg: [add|del]",
		LangChinese: "管理命令别名，参数: [add|del]",
	},
	MsgBuiltinCmdSkills: {
		LangEnglish: "List agent skills (from SKILL.md)",
		LangChinese: "列出 Agent Skills（来自 SKILL.md）",
	},
	MsgBuiltinCmdConfig: {
		LangEnglish: "View/update runtime configuration, arg: [get|set|reload] [key] [value]",
		LangChinese: "查看/修改运行时配置，参数: [get|set|reload] [键] [值]",
	},
	MsgBuiltinCmdDoctor: {
		LangEnglish: "Run system diagnostics",
		LangChinese: "运行系统诊断",
	},
	MsgBuiltinCmdUpgrade: {
		LangEnglish: "Check for updates and self-update",
		LangChinese: "检查更新并自动升级",
	},
	MsgBuiltinCmdRestart: {
		LangEnglish: "Restart cc-connect service",
		LangChinese: "重启 cc-connect 服务",
	},
	MsgBuiltinCmdStatus: {
		LangEnglish: "Show system status",
		LangChinese: "查看系统状态",
	},
	MsgBuiltinCmdUsage: {
		LangEnglish: "Show account/model quota usage",
		LangChinese: "查看账号/模型限额使用情况",
	},
	MsgBuiltinCmdVersion: {
		LangEnglish: "Show cc-connect version",
		LangChinese: "查看 cc-connect 版本",
	},
	MsgBuiltinCmdHelp: {
		LangEnglish: "Show this help",
		LangChinese: "显示此帮助",
	},
	MsgBuiltinCmdBind: {
		LangEnglish: "Bind current session to a target, arg: <target>",
		LangChinese: "绑定当前会话到目标，参数: <目标>",
	},
	MsgBuiltinCmdShell: {
		LangEnglish: "Run a shell command, arg: <command>",
		LangChinese: "执行 Shell 命令，参数: <命令>",
	},
	MsgBuiltinCmdDir: {
		LangEnglish: "Show, switch, or reset agent working directory, arg: <path>",
		LangChinese: "查看、切换或重置 Agent 工作目录，参数: <路径>",
	},
	MsgBuiltinCmdDiff: {
		LangEnglish: "Generate git diff as HTML file, arg: [target]",
		LangChinese: "生成 git diff 并以 HTML 文件发送，参数: [目标]",
	},
	MsgBuiltinCmdPs: {
		LangEnglish: "Send a P.S. to the running task",
		LangChinese: "向正在执行的任务追加补充信息",
	},
	MsgDiffEmpty: {
		LangEnglish: "No diff — clean working tree (or no changes vs `%s`).",
		LangChinese: "无差异 — 工作区干净（或与 `%s` 无变化）。",
	},
	MsgDiffNoDiff2HTML: {
		LangEnglish: "`diff2html` is not installed, sending plain text diff.\nInstall: `npm install -g diff2html-cli`",
		LangChinese: "未安装 `diff2html`，将以纯文本发送差异。\n安装命令: `npm install -g diff2html-cli`",
	},
	MsgDirChanged: {
		LangEnglish: "✅ Work directory changed to: `%s`\nThe next session will start in this directory.",
		LangChinese: "✅ 工作目录已切换为: `%s`\n下次会话将在此目录下启动。",
	},
	MsgDirCurrent: {
		LangEnglish: "📂 Current work directory: `%s`",
		LangChinese: "📂 当前工作目录: `%s`",
	},
	MsgDirReset: {
		LangEnglish: "✅ Work directory reset to the configured default: `%s`",
		LangChinese: "✅ 工作目录已重置为配置的默认目录: `%s`",
	},
	MsgDirUsage: {
		LangEnglish: "Usage: `/dir <path>`\n       `/dir reset`\nExample: `/dir ../project`",
		LangChinese: "用法: `/dir <路径>`\n      `/dir reset`\n示例: `/dir ../project`",
	},
	MsgDirNotSupported: {
		LangEnglish: "This agent does not support dynamic work directory switching.",
		LangChinese: "当前 Agent 不支持动态切换工作目录。",
	},
	MsgDirInvalidPath: {
		LangEnglish: "❌ Directory does not exist: `%s`",
		LangChinese: "❌ 目录不存在: `%s`",
	},
	MsgDirHistoryTitle: {
		LangEnglish: "📋 History:",
		LangChinese: "📋 历史记录:",
	},
	MsgDirHistoryHint: {
		LangEnglish: "💡 Use `/dir <number>` to switch, or `/dir -` for previous.",
		LangChinese: "💡 使用 `/dir <序号>` 切换，或 `/dir -` 返回上一个目录。",
	},
	MsgDirInvalidIndex: {
		LangEnglish: "❌ Invalid history index: %d",
		LangChinese: "❌ 无效的历史序号: %d",
	},
	MsgDirNoHistory: {
		LangEnglish: "❌ No directory history available.",
		LangChinese: "❌ 暂无目录历史记录。",
	},
	MsgDirNoPrevious: {
		LangEnglish: "❌ No previous directory in history.",
		LangChinese: "❌ 没有上一个目录记录。",
	},
	MsgDirCardTitle: {
		LangEnglish: "Working directory",
		LangChinese: "工作目录",
	},
	MsgDirCardPageHint: {
		LangEnglish: "Page %d/%d — use `/dir <page>` or the buttons below.",
		LangChinese: "第 %d/%d 页 — 可用 `/dir <页码>` 或下方按钮翻页。",
	},
	MsgDirCardEmptyHistory: {
		LangEnglish: "No directory history yet. Type `/dir <path>` to switch, or use **Reset** to restore the default.",
		LangChinese: "暂无目录历史。可发送 `/dir <路径>` 切换，或点 **重置** 恢复默认目录。",
	},
	MsgDirCardReset: {
		LangEnglish: "Reset",
		LangChinese: "重置",
	},
	MsgDirCardPrev: {
		LangEnglish: "Previous",
		LangChinese: "上一目录",
	},
	MsgShow: {
		LangEnglish: "View file / directory / snippet by reference",
		LangChinese: "按引用查看文件、目录或代码片段",
	},
	MsgShowUsage: {
		LangEnglish: "Usage: `/show <path|path:line|path:start-end|dir/>`\nExample: `/show svc/recovery_session_reconciler.go:12`",
		LangChinese: "用法: `/show <路径|路径:行号|路径:起止行|目录/>`\n示例: `/show svc/recovery_session_reconciler.go:12`",
	},
	MsgShowParseError: {
		LangEnglish: "❌ Cannot parse reference: `%s`",
		LangChinese: "❌ 无法解析引用: `%s`",
	},
	MsgShowNotFound: {
		LangEnglish: "❌ Referenced path does not exist: `%s`",
		LangChinese: "❌ 引用路径不存在: `%s`",
	},
	MsgShowDirWithLocation: {
		LangEnglish: "❌ Directory references cannot include line information: `%s`",
		LangChinese: "❌ 目录引用不能带行号信息: `%s`",
	},
	MsgShowReadFailed: {
		LangEnglish: "❌ Failed to read reference: %s",
		LangChinese: "❌ 读取引用失败: %s",
	},

	// Multi-workspace messages
	MsgWsNotEnabled: {
		LangEnglish: "Workspace commands are only available in multi-workspace mode.",
		LangChinese: "工作区命令仅在多工作区模式下可用。",
	},
	MsgWsNoBinding: {
		LangEnglish: "No workspace bound to this channel.",
		LangChinese: "此频道未绑定工作区。",
	},
	MsgWsInfo: {
		LangEnglish: "Workspace: `%s`\nBound: %s",
		LangChinese: "工作区: `%s`\n绑定时间: %s",
	},
	MsgWsInfoShared: {
		LangEnglish: "Workspace: `%s`\nBound: %s\nSource: shared",
		LangChinese: "工作区: `%s`\n绑定时间: %s\n来源: shared",
	},
	MsgWsUsage: {
		LangEnglish: "Usage: `/workspace [bind <name> | route <absolute-path> | init <url> | unbind | list | shared ...]`",
		LangChinese: "用法: `/workspace [bind <名称> | route <绝对路径> | init <仓库地址> | unbind | list | shared ...]`",
	},
	MsgWsInitUsage: {
		LangEnglish: "Usage: `/workspace init <git-url or directory-path>`",
		LangChinese: "用法: `/workspace init <git仓库地址或目录路径>`",
	},
	MsgWsBindUsage: {
		LangEnglish: "Usage: `/workspace bind <workspace-name>`",
		LangChinese: "用法: `/workspace bind <工作区名称>`",
	},
	MsgWsBindSuccess: {
		LangEnglish: "✅ Workspace bound: `%s`",
		LangChinese: "✅ 工作区绑定成功: `%s`",
	},
	MsgWsBindNotFound: {
		LangEnglish: "Workspace not found: `%s`",
		LangChinese: "工作区不存在: `%s`",
	},
	MsgWsRouteUsage: {
		LangEnglish: "Usage: `/workspace route <absolute-path>`",
		LangChinese: "用法: `/workspace route <绝对路径>`",
	},
	MsgWsRouteSuccess: {
		LangEnglish: "✅ Workspace routed: `%s`",
		LangChinese: "✅ 工作区路由成功: `%s`",
	},
	MsgWsRouteAbsoluteRequired: {
		LangEnglish: "Workspace route must use an absolute path: `%s`",
		LangChinese: "工作区路由必须使用绝对路径: `%s`",
	},
	MsgWsRouteNotFound: {
		LangEnglish: "Workspace path not found: `%s`",
		LangChinese: "工作区路径不存在: `%s`",
	},
	MsgWsRouteNotDirectory: {
		LangEnglish: "Workspace route target is not a directory: `%s`",
		LangChinese: "工作区路由目标不是目录: `%s`",
	},
	MsgWsUnbindSuccess: {
		LangEnglish: "✅ Workspace unbound.",
		LangChinese: "✅ 已解除工作区绑定。",
	},
	MsgWsListEmpty: {
		LangEnglish: "No workspaces bound.",
		LangChinese: "没有绑定的工作区。",
	},
	MsgWsListTitle: {
		LangEnglish: "Bound workspaces:",
		LangChinese: "已绑定的工作区：",
	},
	MsgWsSharedNoBinding: {
		LangEnglish: "No shared workspace bound to this channel.",
		LangChinese: "此频道未绑定共享工作区。",
	},
	MsgWsSharedUsage: {
		LangEnglish: "Usage: `/workspace shared [bind <name> | route <absolute-path> | init <url> | unbind | list]`",
		LangChinese: "用法: `/workspace shared [bind <名称> | route <绝对路径> | init <仓库地址> | unbind | list]`",
	},
	MsgWsSharedBindSuccess: {
		LangEnglish: "✅ Shared workspace bound: `%s`",
		LangChinese: "✅ 共享工作区绑定成功: `%s`",
	},
	MsgWsSharedRouteSuccess: {
		LangEnglish: "✅ Shared workspace routed: `%s`",
		LangChinese: "✅ 共享工作区路由成功: `%s`",
	},
	MsgWsSharedUnbindSuccess: {
		LangEnglish: "✅ Shared workspace unbound.",
		LangChinese: "✅ 已解除共享工作区绑定。",
	},
	MsgWsSharedListEmpty: {
		LangEnglish: "No shared workspaces bound.",
		LangChinese: "没有绑定的共享工作区。",
	},
	MsgWsSharedListTitle: {
		LangEnglish: "Shared workspaces:",
		LangChinese: "共享工作区：",
	},
	MsgWsSharedOnlyHint: {
		LangEnglish: "The current effective workspace comes from the shared layer. Use `/workspace shared unbind` to remove it.",
		LangChinese: "当前生效的工作区来自 shared 层。请使用 `/workspace shared unbind` 解除绑定。",
	},
	MsgWsNotFoundHint: {
		LangEnglish: "No workspace found for this channel. Send a git repo URL, a local directory path, or use `/workspace init <url-or-path>`.",
		LangChinese: "此频道未找到工作区。请发送 git 仓库地址或本地目录路径，或使用 `/workspace init <仓库地址或目录路径>`。",
	},
	MsgWsNotFoundHintGitOnly: {
		LangEnglish: "No workspace found for this channel. Send a git repo URL or use `/workspace init <git-url>`.",
		LangChinese: "此频道未找到工作区。请发送 git 仓库地址，或使用 `/workspace init <git仓库地址>`。",
	},
	MsgWsResolutionError: {
		LangEnglish: "Workspace resolution error: %v",
		LangChinese: "工作区解析错误: %v",
	},
	MsgWsCloneProgress: {
		LangEnglish: "🔄 Cloning repository: %s",
		LangChinese: "🔄 正在克隆仓库: %s",
	},
	MsgWsCloneSuccess: {
		LangEnglish: "✅ Repository cloned successfully: `%s`",
		LangChinese: "✅ 仓库克隆成功: `%s`",
	},
	MsgWsCloneFailed: {
		LangEnglish: "❌ Failed to clone repository: %v",
		LangChinese: "❌ 克隆仓库失败: %v",
	},
	MsgWsInitDirNotFound: {
		LangEnglish: "Directory not found: `%s`. Please provide a valid directory path or a git URL.",
		LangChinese: "目录不存在: `%s`。请提供有效的目录路径或 git 仓库地址。",
	},
	MsgWsInitInvalidTarget: {
		LangEnglish: "Please provide a git URL (e.g. `https://github.com/org/repo`) or a local directory path.",
		LangChinese: "请提供 git 仓库地址（如 `https://github.com/org/repo`）或本地目录路径。",
	},
	MsgWsInitLocalPathsDisabled: {
		LangEnglish: "Local directory targets are disabled for `/workspace init`. Use a git URL, or enable `workspace_init_allow_local_paths = true` for this project.",
		LangChinese: "`/workspace init` 未启用本地目录目标。请使用 git 仓库地址，或在此项目配置 `workspace_init_allow_local_paths = true`。",
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
