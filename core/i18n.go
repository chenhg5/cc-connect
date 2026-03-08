package core

import "fmt"

// Language represents a supported language
type Language string

const (
	LangAuto               Language = "" // auto-detect from user messages
	LangEnglish            Language = "en"
	LangChinese            Language = "zh"
	LangTraditionalChinese Language = "zh-TW"
	LangJapanese           Language = "ja"
	LangSpanish            Language = "es"
)

// I18n provides internationalized messages
type I18n struct {
	lang     Language
	detected Language
	saveFunc func(Language) error
}

func NewI18n(lang Language) *I18n {
	return &I18n{lang: lang}
}

func (i *I18n) SetSaveFunc(fn func(Language) error) {
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
	if i.lang != LangAuto {
		return
	}
	detected := DetectLanguage(text)
	if i.detected != detected {
		i.detected = detected
		if i.saveFunc != nil {
			if err := i.saveFunc(detected); err != nil {
				fmt.Printf("failed to save language: %v\n", err)
			}
		}
	}
}

func (i *I18n) currentLang() Language {
	if i.lang == LangAuto {
		if i.detected != "" {
			return i.detected
		}
		return LangEnglish
	}
	return i.lang
}

// CurrentLang returns the resolved language (exported for mode display).
func (i *I18n) CurrentLang() Language { return i.currentLang() }

// IsZhLike returns true for Simplified and Traditional Chinese.
func (i *I18n) IsZhLike() bool {
	l := i.currentLang()
	return l == LangChinese || l == LangTraditionalChinese
}

// SetLang overrides the language (disabling auto-detect).
func (i *I18n) SetLang(lang Language) {
	i.lang = lang
	i.detected = ""
}

// Message keys
type MsgKey string

const (
	MsgStarting             MsgKey = "starting"
	MsgThinking             MsgKey = "thinking"
	MsgTool                 MsgKey = "tool"
	MsgExecutionStopped     MsgKey = "execution_stopped"
	MsgNoExecution          MsgKey = "no_execution"
	MsgPreviousProcessing   MsgKey = "previous_processing"
	MsgNoToolsAllowed       MsgKey = "no_tools_allowed"
	MsgCurrentTools         MsgKey = "current_tools"
	MsgCurrentSession       MsgKey = "current_session"
	MsgToolAuthNotSupported MsgKey = "tool_auth_not_supported"
	MsgToolAllowFailed      MsgKey = "tool_allow_failed"
	MsgToolAllowedNew       MsgKey = "tool_allowed_new"
	MsgError                MsgKey = "error"
	MsgEmptyResponse        MsgKey = "empty_response"
	MsgPermissionPrompt     MsgKey = "permission_prompt"
	MsgPermissionAllowed    MsgKey = "permission_allowed"
	MsgPermissionApproveAll MsgKey = "permission_approve_all"
	MsgPermissionDenied     MsgKey = "permission_denied_msg"
	MsgPermissionHint       MsgKey = "permission_hint"
	MsgQuietOn              MsgKey = "quiet_on"
	MsgQuietOff             MsgKey = "quiet_off"
	MsgQuietGlobalOn        MsgKey = "quiet_global_on"
	MsgQuietGlobalOff       MsgKey = "quiet_global_off"
	MsgModeChanged          MsgKey = "mode_changed"
	MsgModeNotSupported     MsgKey = "mode_not_supported"
	MsgSessionRestarting    MsgKey = "session_restarting"
	MsgSessionNotStarted    MsgKey = "session_not_started"
	MsgLangChanged          MsgKey = "lang_changed"
	MsgLangInvalid          MsgKey = "lang_invalid"
	MsgLangCurrent          MsgKey = "lang_current"
	MsgUnknownCommand       MsgKey = "unknown_command"
	MsgHelp                 MsgKey = "message_help" // change from "help", which is used now for builtin command help
	MsgListTitle            MsgKey = "list_title"
	MsgListTitlePaged       MsgKey = "list_title_paged"
	MsgListEmpty            MsgKey = "list_empty"
	MsgListMore             MsgKey = "list_more"
	MsgListPageHint         MsgKey = "list_page_hint"
	MsgListSwitchHint       MsgKey = "list_switch_hint"
	MsgListError            MsgKey = "list_error"
	MsgHistoryEmpty         MsgKey = "history_empty"
	MsgNameUsage            MsgKey = "name_usage"
	MsgNameSet              MsgKey = "name_set"
	MsgNameNoSession        MsgKey = "name_no_session"
	MsgProviderNotSupported MsgKey = "provider_not_supported"
	MsgProviderNone         MsgKey = "provider_none"
	MsgProviderCurrent      MsgKey = "provider_current"
	MsgProviderListTitle    MsgKey = "provider_list_title"
	MsgProviderListEmpty    MsgKey = "provider_list_empty"
	MsgProviderSwitchHint   MsgKey = "provider_switch_hint"
	MsgProviderNotFound     MsgKey = "provider_not_found"
	MsgProviderSwitched     MsgKey = "provider_switched"
	MsgProviderCleared      MsgKey = "provider_cleared"
	MsgProviderAdded        MsgKey = "provider_added"
	MsgProviderAddUsage     MsgKey = "provider_add_usage"
	MsgProviderAddFailed    MsgKey = "provider_add_failed"
	MsgProviderRemoved      MsgKey = "provider_removed"
	MsgProviderRemoveFailed MsgKey = "provider_remove_failed"

	MsgVoiceNotEnabled       MsgKey = "voice_not_enabled"
	MsgVoiceNoFFmpeg         MsgKey = "voice_no_ffmpeg"
	MsgVoiceTranscribing     MsgKey = "voice_transcribing"
	MsgVoiceTranscribed      MsgKey = "voice_transcribed"
	MsgVoiceTranscribeFailed MsgKey = "voice_transcribe_failed"
	MsgVoiceEmpty            MsgKey = "voice_empty"

	MsgCronNotAvailable MsgKey = "cron_not_available"
	MsgCronUsage        MsgKey = "cron_usage"
	MsgCronAddUsage     MsgKey = "cron_add_usage"
	MsgCronAdded        MsgKey = "cron_added"
	MsgCronEmpty        MsgKey = "cron_empty"
	MsgCronListTitle    MsgKey = "cron_list_title"
	MsgCronListFooter   MsgKey = "cron_list_footer"
	MsgCronDelUsage     MsgKey = "cron_del_usage"
	MsgCronDeleted      MsgKey = "cron_deleted"
	MsgCronNotFound     MsgKey = "cron_not_found"
	MsgCronEnabled      MsgKey = "cron_enabled"
	MsgCronDisabled     MsgKey = "cron_disabled"

	MsgStatusTitle MsgKey = "status_title"

	MsgModelCurrent      MsgKey = "model_current"
	MsgModelChanged      MsgKey = "model_changed"
	MsgModelNotSupported MsgKey = "model_not_supported"

	MsgCompressNotSupported MsgKey = "compress_not_supported"
	MsgCompressing          MsgKey = "compressing"
	MsgCompressNoSession    MsgKey = "compress_no_session"

	MsgMemoryNotSupported MsgKey = "memory_not_supported"
	MsgMemoryShowProject  MsgKey = "memory_show_project"
	MsgMemoryShowGlobal   MsgKey = "memory_show_global"
	MsgMemoryEmpty        MsgKey = "memory_empty"
	MsgMemoryAdded        MsgKey = "memory_added"
	MsgMemoryAddFailed    MsgKey = "memory_add_failed"
	MsgMemoryAddUsage     MsgKey = "memory_add_usage"

	// Inline strings previously hardcoded in engine.go
	MsgStatusMode    MsgKey = "status_mode"
	MsgStatusSession MsgKey = "status_session"
	MsgStatusCron    MsgKey = "status_cron"
	MsgStatusQuiet   MsgKey = "status_quiet"
	MsgQuietOnShort  MsgKey = "quiet_on_short"
	MsgQuietOffShort MsgKey = "quiet_off_short"

	MsgModelDefault   MsgKey = "model_default"
	MsgModelListTitle MsgKey = "model_list_title"
	MsgModelUsage     MsgKey = "model_usage"

	MsgModeUsage MsgKey = "mode_usage"

	MsgCronScheduleLabel MsgKey = "cron_schedule_label"
	MsgCronNextRunLabel  MsgKey = "cron_next_run_label"
	MsgCronLastRunLabel  MsgKey = "cron_last_run_label"

	MsgPermBtnAllow    MsgKey = "perm_btn_allow"
	MsgPermBtnDeny     MsgKey = "perm_btn_deny"
	MsgPermBtnAllowAll MsgKey = "perm_btn_allow_all"

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

	MsgAliasEmpty      MsgKey = "alias_empty"
	MsgAliasListHeader MsgKey = "alias_list_header"
	MsgAliasAdded      MsgKey = "alias_added"
	MsgAliasDeleted    MsgKey = "alias_deleted"
	MsgAliasNotFound   MsgKey = "alias_not_found"
	MsgAliasUsage      MsgKey = "alias_usage"

	MsgNewSessionCreated     MsgKey = "new_session_created"
	MsgNewSessionCreatedName MsgKey = "new_session_created_name"

	MsgDeleteUsage        MsgKey = "delete_usage"
	MsgDeleteSuccess      MsgKey = "delete_success"
	MsgDeleteActiveDenied MsgKey = "delete_active_denied"
	MsgDeleteNotSupported MsgKey = "delete_not_supported"

	MsgSwitchSuccess MsgKey = "switch_success"

	MsgBannedWordBlocked MsgKey = "banned_word_blocked"
	MsgCommandDisabled   MsgKey = "command_disabled"
	MsgRateLimited       MsgKey = "rate_limited"

	MsgRelayNoBinding    MsgKey = "relay_no_binding"
	MsgRelayBound        MsgKey = "relay_bound"
	MsgRelayBindOK       MsgKey = "relay_bind_ok"
	MsgRelayUsage        MsgKey = "relay_usage"
	MsgRelayNotAvailable MsgKey = "relay_not_available"
	MsgRelayUnbound      MsgKey = "relay_unbound"
	MsgRelayBindSelf     MsgKey = "relay_bind_self"
	MsgRelayNotFound     MsgKey = "relay_not_found"
	MsgRelayNoTarget     MsgKey = "relay_no_target"

	MsgSearchUsage    MsgKey = "search_usage"
	MsgSearchError    MsgKey = "search_error"
	MsgSearchNoResult MsgKey = "search_no_result"
	MsgSearchResult   MsgKey = "search_result"
	MsgSearchHint     MsgKey = "search_hint"

	MsgBuiltinCmdNew      MsgKey = "new"
	MsgBuiltinCmdList     MsgKey = "list"
	MsgBuiltinCmdSearch   MsgKey = "search"
	MsgBuiltinCmdSwitch   MsgKey = "switch"
	MsgBuiltinCmdDelete   MsgKey = "delete"
	MsgBuiltinCmdName     MsgKey = "name"
	MsgBuiltinCmdCurrent  MsgKey = "current"
	MsgBuiltinCmdHistory  MsgKey = "history"
	MsgBuiltinCmdProvider MsgKey = "provider"
	MsgBuiltinCmdMemory   MsgKey = "memory"
	MsgBuiltinCmdAllow    MsgKey = "allow"
	MsgBuiltinCmdModel    MsgKey = "model"
	MsgBuiltinCmdMode     MsgKey = "mode"
	MsgBuiltinCmdLang     MsgKey = "lang"
	MsgBuiltinCmdQuiet    MsgKey = "quiet"
	MsgBuiltinCmdCompress MsgKey = "compress"
	MsgBuiltinCmdStop     MsgKey = "stop"
	MsgBuiltinCmdCron     MsgKey = "cron"
	MsgBuiltinCmdCommands MsgKey = "commands"
	MsgBuiltinCmdAlias    MsgKey = "alias"
	MsgBuiltinCmdSkills   MsgKey = "skills"
	MsgBuiltinCmdConfig   MsgKey = "config"
	MsgBuiltinCmdDoctor   MsgKey = "doctor"
	MsgBuiltinCmdUpgrade  MsgKey = "upgrade"
	MsgBuiltinCmdRestart  MsgKey = "restart"
	MsgBuiltinCmdStatus   MsgKey = "status"
	MsgBuiltinCmdVersion  MsgKey = "version"
	MsgBuiltinCmdHelp     MsgKey = "help"
	MsgBuiltinCmdBind     MsgKey = "bind"
)

var messages = map[MsgKey]map[Language]string{
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
	MsgExecutionStopped: {
		LangEnglish:            "⏹ Execution stopped.",
		LangChinese:            "⏹ 执行已停止。",
		LangTraditionalChinese: "⏹ 執行已停止。",
		LangJapanese:           "⏹ 実行を停止しました。",
		LangSpanish:            "⏹ Ejecución detenida.",
	},
	MsgNoExecution: {
		LangEnglish:            "No execution in progress.",
		LangChinese:            "没有正在执行的任务。",
		LangTraditionalChinese: "沒有正在執行的任務。",
		LangJapanese:           "実行中のタスクはありません。",
		LangSpanish:            "No hay ejecución en progreso.",
	},
	MsgPreviousProcessing: {
		LangEnglish:            "⏳ Previous request still processing, please wait...",
		LangChinese:            "⏳ 上一个请求仍在处理中，请稍候...",
		LangTraditionalChinese: "⏳ 上一個請求仍在處理中，請稍候...",
		LangJapanese:           "⏳ 前のリクエストを処理中です。お待ちください...",
		LangSpanish:            "⏳ La solicitud anterior aún se está procesando, por favor espere...",
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
	MsgError: {
		LangEnglish:            "❌ Error: %v",
		LangChinese:            "❌ 错误: %v",
		LangTraditionalChinese: "❌ 錯誤: %v",
		LangJapanese:           "❌ エラー: %v",
		LangSpanish:            "❌ Error: %v",
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
	MsgHelp: {
		LangEnglish: "📖 Available Commands\n\n" +
			"/new [name]\n  Start a new session\n\n" +
			"/list\n  List agent sessions\n\n" +
			"/search <keyword>\n  Search sessions by name or ID\n\n" +
			"/switch <number>\n  Resume a session by its list number\n\n" +
			"/delete <number>\n  Delete a session by its list number\n\n" +
			"/name [number] <text>\n  Name a session for easy identification\n\n" +
			"/current\n  Show current active session\n\n" +
			"/history [n]\n  Show last n messages (default 10)\n\n" +
			"/provider [list|add|remove|switch|clear]\n  Manage API providers\n\n" +
			"/memory [add|global|global add]\n  View/edit agent memory files\n\n" +
			"/allow <tool>\n  Pre-allow a tool (next session)\n\n" +
			"/model [name]\n  View/switch model\n\n" +
			"/mode [name]\n  View/switch permission mode\n\n" +
			"/lang [en|zh|zh-TW|ja|es|auto]\n  View/switch language\n\n" +
			"/quiet [global]\n  Toggle thinking/tool progress (global = all sessions)\n\n" +
			"/compress\n  Compress conversation context\n\n" +
			"/shell <command>\n  Run a shell command and return the output\n\n" +
			"/stop\n  Stop current execution\n\n" +
			"/cron [add|list|del|enable|disable]\n  Manage scheduled tasks\n\n" +
			"/commands [add|del]\n  Manage custom slash commands\n\n" +
			"/alias [add|del]\n  Manage command aliases (e.g. 帮助 → /help)\n\n" +
			"/skills\n  List agent skills (from SKILL.md)\n\n" +
			"/config [get|set|reload] [key] [value]\n  View/update runtime configuration\n\n" +
			"/doctor\n  Run system diagnostics\n\n" +
			"/upgrade\n  Check for updates and self-update\n\n" +
			"/restart\n  Restart cc-connect service\n\n" +
			"/status\n  Show system status\n\n" +
			"/version\n  Show cc-connect version\n\n" +
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
			"/delete <序号>\n  按列表序号删除会话\n\n" +
			"/name [序号] <名称>\n  给会话命名，方便识别\n\n" +
			"/current\n  查看当前活跃会话\n\n" +
			"/history [n]\n  查看最近 n 条消息（默认 10）\n\n" +
			"/provider [list|add|remove|switch|clear]\n  管理 API Provider\n\n" +
			"/memory [add|global|global add]\n  查看/编辑 Agent 记忆文件\n\n" +
			"/allow <工具名>\n  预授权工具（下次会话生效）\n\n" +
			"/model [名称]\n  查看/切换模型\n\n" +
			"/mode [名称]\n  查看/切换权限模式\n\n" +
			"/lang [en|zh|zh-TW|ja|es|auto]\n  查看/切换语言\n\n" +
			"/quiet [global]\n  开关思考和工具进度消息（global = 全部会话）\n\n" +
			"/compress\n  压缩会话上下文\n\n" +
			"/shell <命令>\n  执行 Shell 命令并返回结果\n\n" +
			"/stop\n  停止当前执行\n\n" +
			"/cron [add|list|del|enable|disable]\n  管理定时任务\n\n" +
			"/commands [add|del]\n  管理自定义命令\n\n" +
			"/alias [add|del]\n  管理命令别名（如 帮助 → /help）\n\n" +
			"/skills\n  列出 Agent Skills（来自 SKILL.md）\n\n" +
			"/config [get|set|reload] [key] [value]\n  查看/修改运行时配置\n\n" +
			"/doctor\n  运行系统诊断\n\n" +
			"/upgrade\n  检查更新并自动升级\n\n" +
			"/restart\n  重启 cc-connect 服务\n\n" +
			"/status\n  查看系统状态\n\n" +
			"/version\n  查看 cc-connect 版本\n\n" +
			"/help\n  显示此帮助\n\n" +
			"提示：命令支持前缀匹配，如 `/pro l` = `/provider list`，`/sw 2` = `/switch 2`。\n\n" +
			"自定义命令：通过 `/commands add` 添加，或在 config.toml 中配置 `[[commands]]`。\n\n" +
			"命令别名：使用 `/alias add <触发词> <命令>` 或在 config.toml 中配置 `[[aliases]]`。\n\n" +
			"Agent Skills：自动发现自 .claude/skills/<name>/SKILL.md 等目录。\n\n" +
			"权限模式：default / edit / plan / yolo",
		LangTraditionalChinese: "📖 可用命令\n\n" +
			"/new [名稱]\n  建立新會話\n\n" +
			"/list\n  列出 Agent 會話列表\n\n" +
			"/search <關鍵詞>\n  搜尋會話名稱或 ID\n\n" +
			"/switch <序號>\n  按列表序號切換會話\n\n" +
			"/delete <序號>\n  按列表序號刪除會話\n\n" +
			"/name [序號] <名稱>\n  為會話命名，方便辨識\n\n" +
			"/current\n  查看當前活躍會話\n\n" +
			"/history [n]\n  查看最近 n 條訊息（預設 10）\n\n" +
			"/provider [list|add|remove|switch|clear]\n  管理 API Provider\n\n" +
			"/memory [add|global|global add]\n  查看/編輯 Agent 記憶檔案\n\n" +
			"/allow <工具名>\n  預授權工具（下次會話生效）\n\n" +
			"/model [名稱]\n  查看/切換模型\n\n" +
			"/mode [名稱]\n  查看/切換權限模式\n\n" +
			"/lang [en|zh|zh-TW|ja|es|auto]\n  查看/切換語言\n\n" +
			"/quiet [global]\n  開關思考和工具進度訊息（global = 全部會話）\n\n" +
			"/compress\n  壓縮會話上下文\n\n" +
			"/shell <命令>\n  執行 Shell 命令並返回結果\n\n" +
			"/stop\n  停止當前執行\n\n" +
			"/cron [add|list|del|enable|disable]\n  管理定時任務\n\n" +
			"/commands [add|del]\n  管理自訂命令\n\n" +
			"/alias [add|del]\n  管理命令別名（如 幫助 → /help）\n\n" +
			"/skills\n  列出 Agent Skills（來自 SKILL.md）\n\n" +
			"/config [get|set|reload] [key] [value]\n  查看/修改執行階段配置\n\n" +
			"/doctor\n  執行系統診斷\n\n" +
			"/upgrade\n  檢查更新並自動升級\n\n" +
			"/restart\n  重啟 cc-connect 服務\n\n" +
			"/status\n  查看系統狀態\n\n" +
			"/version\n  查看 cc-connect 版本\n\n" +
			"/help\n  顯示此說明\n\n" +
			"提示：命令支持前綴匹配，如 `/pro l` = `/provider list`，`/sw 2` = `/switch 2`。\n\n" +
			"自訂命令：透過 `/commands add` 新增，或在 config.toml 中配置 `[[commands]]`。\n\n" +
			"命令別名：使用 `/alias add <觸發詞> <命令>` 或在 config.toml 中配置 `[[aliases]]`。\n\n" +
			"Agent Skills：自動發現自 .claude/skills/<name>/SKILL.md 等目錄。\n\n" +
			"權限模式：default / edit / plan / yolo",
		LangJapanese: "📖 利用可能なコマンド\n\n" +
			"/new [名前]\n  新しいセッションを開始\n\n" +
			"/list\n  エージェントセッション一覧\n\n" +
			"/switch <番号>\n  リスト番号でセッションを切り替え\n\n" +
			"/delete <番号>\n  リスト番号でセッションを削除\n\n" +
			"/name [番号] <名前>\n  セッションに名前を付ける\n\n" +
			"/current\n  現在のアクティブセッションを表示\n\n" +
			"/history [n]\n  直近 n 件のメッセージを表示（デフォルト 10）\n\n" +
			"/provider [list|add|remove|switch|clear]\n  API プロバイダ管理\n\n" +
			"/memory [add|global|global add]\n  エージェントメモリの表示/編集\n\n" +
			"/allow <ツール名>\n  ツールを事前許可（次のセッションで有効）\n\n" +
			"/model [名前]\n  モデルの表示/切り替え\n\n" +
			"/mode [名前]\n  権限モードの表示/切り替え\n\n" +
			"/lang [en|zh|zh-TW|ja|es|auto]\n  言語の表示/切り替え\n\n" +
			"/quiet [global]\n  思考/ツール進捗メッセージの表示切替（global = 全セッション）\n\n" +
			"/compress\n  会話コンテキストを圧縮\n\n" +
			"/shell <コマンド>\n  シェルコマンドを実行して結果を返す\n\n" +
			"/stop\n  現在の実行を停止\n\n" +
			"/cron [add|list|del|enable|disable]\n  スケジュールタスク管理\n\n" +
			"/commands [add|del]\n  カスタムコマンド管理\n\n" +
			"/alias [add|del]\n  コマンドエイリアス管理（例: ヘルプ → /help）\n\n" +
			"/skills\n  エージェントスキル一覧（SKILL.md から）\n\n" +
			"/config [get|set|reload] [key] [value]\n  ランタイム設定の表示/変更\n\n" +
			"/doctor\n  システム診断を実行\n\n" +
			"/upgrade\n  アップデートを確認して自動更新\n\n" +
			"/restart\n  cc-connect サービスを再起動\n\n" +
			"/status\n  システム状態を表示\n\n" +
			"/version\n  cc-connect のバージョンを表示\n\n" +
			"/help\n  このヘルプを表示\n\n" +
			"ヒント：コマンドはプレフィックスマッチに対応しています。例: `/pro l` = `/provider list`、`/sw 2` = `/switch 2`。\n\n" +
			"カスタムコマンド: `/commands add` または config.toml の `[[commands]]` で定義。\n\n" +
			"コマンドエイリアス: `/alias add <トリガー> <コマンド>` または config.toml の `[[aliases]]` で定義。\n\n" +
			"エージェントスキル: .claude/skills/<name>/SKILL.md などから自動検出。\n\n" +
			"権限モード: default / edit / plan / yolo",
		LangSpanish: "📖 Comandos disponibles\n\n" +
			"/new [nombre]\n  Iniciar una nueva sesión\n\n" +
			"/list\n  Listar sesiones del agente\n\n" +
			"/switch <número>\n  Reanudar sesión por su número en la lista\n\n" +
			"/delete <número>\n  Eliminar sesión por su número en la lista\n\n" +
			"/name [número] <texto>\n  Nombrar una sesión para fácil identificación\n\n" +
			"/current\n  Mostrar sesión activa actual\n\n" +
			"/history [n]\n  Mostrar últimos n mensajes (por defecto 10)\n\n" +
			"/provider [list|add|remove|switch|clear]\n  Gestionar proveedores API\n\n" +
			"/memory [add|global|global add]\n  Ver/editar archivos de memoria del agente\n\n" +
			"/allow <herramienta>\n  Pre-autorizar herramienta (próxima sesión)\n\n" +
			"/model [nombre]\n  Ver/cambiar modelo\n\n" +
			"/mode [nombre]\n  Ver/cambiar modo de permisos\n\n" +
			"/lang [en|zh|zh-TW|ja|es|auto]\n  Ver/cambiar idioma\n\n" +
			"/quiet [global]\n  Alternar mensajes de progreso (global = todas las sesiones)\n\n" +
			"/compress\n  Comprimir contexto de conversación\n\n" +
			"/shell <comando>\n  Ejecutar un comando shell y devolver la salida\n\n" +
			"/stop\n  Detener ejecución actual\n\n" +
			"/cron [add|list|del|enable|disable]\n  Gestionar tareas programadas\n\n" +
			"/commands [add|del]\n  Gestionar comandos personalizados\n\n" +
			"/alias [add|del]\n  Gestionar alias de comandos (ej. ayuda → /help)\n\n" +
			"/skills\n  Listar skills del agente (desde SKILL.md)\n\n" +
			"/config [get|set|reload] [key] [value]\n  Ver/actualizar configuración en tiempo de ejecución\n\n" +
			"/doctor\n  Ejecutar diagnósticos del sistema\n\n" +
			"/upgrade\n  Buscar actualizaciones y auto-actualizar\n\n" +
			"/restart\n  Reiniciar el servicio cc-connect\n\n" +
			"/status\n  Mostrar estado del sistema\n\n" +
			"/version\n  Mostrar versión de cc-connect\n\n" +
			"/help\n  Mostrar esta ayuda\n\n" +
			"Consejo: Los comandos admiten coincidencia por prefijo, ej. `/pro l` = `/provider list`, `/sw 2` = `/switch 2`.\n\n" +
			"Comandos personalizados: use `/commands add` o defina `[[commands]]` en config.toml.\n\n" +
			"Alias de comandos: use `/alias add <trigger> <comando>` o `[[aliases]]` en config.toml.\n\n" +
			"Skills del agente: descubiertos de .claude/skills/<name>/SKILL.md etc.\n\n" +
			"Modos de permisos: default / edit / plan / yolo",
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
	MsgVoiceNotEnabled: {
		LangEnglish:            "🎙 Voice messages are not enabled. Please configure `[speech]` in config.toml.",
		LangChinese:            "🎙 语音消息未启用，请在 config.toml 中配置 `[speech]` 部分。",
		LangTraditionalChinese: "🎙 語音訊息未啟用，請在 config.toml 中配置 `[speech]` 部分。",
		LangJapanese:           "🎙 音声メッセージは有効になっていません。config.toml で `[speech]` を設定してください。",
		LangSpanish:            "🎙 Los mensajes de voz no están habilitados. Configure `[speech]` en config.toml.",
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
	MsgCronNotAvailable: {
		LangEnglish:            "Cron scheduler is not available.",
		LangChinese:            "定时任务调度器未启用。",
		LangTraditionalChinese: "定時任務調度器未啟用。",
		LangJapanese:           "スケジューラは利用できません。",
		LangSpanish:            "El programador de tareas no está disponible.",
	},
	MsgCronUsage: {
		LangEnglish:            "Usage:\n/cron add <min> <hour> <day> <month> <weekday> <prompt>\n/cron list\n/cron del <id>\n/cron enable <id>\n/cron disable <id>",
		LangChinese:            "用法：\n/cron add <分> <时> <日> <月> <周> <任务描述>\n/cron list\n/cron del <id>\n/cron enable <id>\n/cron disable <id>",
		LangTraditionalChinese: "用法：\n/cron add <分> <時> <日> <月> <週> <任務描述>\n/cron list\n/cron del <id>\n/cron enable <id>\n/cron disable <id>",
		LangJapanese:           "使い方:\n/cron add <分> <時> <日> <月> <曜日> <タスク内容>\n/cron list\n/cron del <id>\n/cron enable <id>\n/cron disable <id>",
		LangSpanish:            "Uso:\n/cron add <min> <hora> <día> <mes> <día_semana> <tarea>\n/cron list\n/cron del <id>\n/cron enable <id>\n/cron disable <id>",
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
	MsgCronEmpty: {
		LangEnglish:            "No scheduled tasks.",
		LangChinese:            "暂无定时任务。",
		LangTraditionalChinese: "暫無定時任務。",
		LangJapanese:           "スケジュールタスクはありません。",
		LangSpanish:            "No hay tareas programadas.",
	},
	MsgCronListTitle: {
		LangEnglish:            "⏰ Scheduled Tasks (%d)",
		LangChinese:            "⏰ 定时任务 (%d)",
		LangTraditionalChinese: "⏰ 定時任務 (%d)",
		LangJapanese:           "⏰ スケジュールタスク (%d)",
		LangSpanish:            "⏰ Tareas programadas (%d)",
	},
	MsgCronListFooter: {
		LangEnglish:            "`/cron del <id>` to remove · `/cron enable/disable <id>` to toggle",
		LangChinese:            "`/cron del <id>` 删除 · `/cron enable/disable <id>` 启停",
		LangTraditionalChinese: "`/cron del <id>` 刪除 · `/cron enable/disable <id>` 啟停",
		LangJapanese:           "`/cron del <id>` で削除 · `/cron enable/disable <id>` で切替",
		LangSpanish:            "`/cron del <id>` para eliminar · `/cron enable/disable <id>` para activar/desactivar",
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
	MsgStatusTitle: {
		LangEnglish: "cc-connect Status\n\n" +
			"Project: %s\n" +
			"Agent: %s\n" +
			"Platforms: %s\n" +
			"Uptime: %s\n" +
			"Language: %s\n" +
			"%s" + "%s" + "%s",
		LangChinese: "cc-connect 状态\n\n" +
			"项目: %s\n" +
			"Agent: %s\n" +
			"平台: %s\n" +
			"运行时间: %s\n" +
			"语言: %s\n" +
			"%s" + "%s" + "%s",
		LangTraditionalChinese: "cc-connect 狀態\n\n" +
			"項目: %s\n" +
			"Agent: %s\n" +
			"平台: %s\n" +
			"運行時間: %s\n" +
			"語言: %s\n" +
			"%s" + "%s" + "%s",
		LangJapanese: "cc-connect ステータス\n\n" +
			"プロジェクト: %s\n" +
			"エージェント: %s\n" +
			"プラットフォーム: %s\n" +
			"稼働時間: %s\n" +
			"言語: %s\n" +
			"%s" + "%s" + "%s",
		LangSpanish: "Estado de cc-connect\n\n" +
			"Proyecto: %s\n" +
			"Agente: %s\n" +
			"Plataformas: %s\n" +
			"Tiempo activo: %s\n" +
			"Idioma: %s\n" +
			"%s" + "%s" + "%s",
	},
	MsgModelCurrent: {
		LangEnglish:            "Current model: %s",
		LangChinese:            "当前模型: %s",
		LangTraditionalChinese: "當前模型: %s",
		LangJapanese:           "現在のモデル: %s",
		LangSpanish:            "Modelo actual: %s",
	},
	MsgModelChanged: {
		LangEnglish:            "Model switched to `%s`. New sessions will use this model.",
		LangChinese:            "模型已切换为 `%s`，新会话将使用此模型。",
		LangTraditionalChinese: "模型已切換為 `%s`，新會話將使用此模型。",
		LangJapanese:           "モデルを `%s` に切り替えました。新しいセッションで使用されます。",
		LangSpanish:            "Modelo cambiado a `%s`. Las nuevas sesiones usarán este modelo.",
	},
	MsgModelNotSupported: {
		LangEnglish:            "This agent does not support model switching.",
		LangChinese:            "当前 Agent 不支持模型切换。",
		LangTraditionalChinese: "當前 Agent 不支援模型切換。",
		LangJapanese:           "このエージェントはモデルの切り替えをサポートしていません。",
		LangSpanish:            "Este agente no soporta el cambio de modelo.",
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
	MsgStatusQuiet: {
		LangEnglish:            "Quiet mode: %s\n",
		LangChinese:            "安静模式: %s\n",
		LangTraditionalChinese: "安靜模式: %s\n",
		LangJapanese:           "出力抑制モード: %s\n",
		LangSpanish:            "Modo silencioso: %s\n",
	},
	MsgQuietOnShort: {
		LangEnglish:            "ON",
		LangChinese:            "开启",
		LangTraditionalChinese: "開啟",
		LangJapanese:           "ON",
		LangSpanish:            "Activado",
	},
	MsgQuietOffShort: {
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
		LangEnglish:            "Usage: `/model <number>` or `/model <model_name>`",
		LangChinese:            "用法: `/model <序号>` 或 `/model <模型名>`",
		LangTraditionalChinese: "用法: `/model <序號>` 或 `/model <模型名>`",
		LangJapanese:           "使い方: `/model <番号>` または `/model <モデル名>`",
		LangSpanish:            "Uso: `/model <número>` o `/model <nombre_modelo>`",
	},
	MsgModeUsage: {
		LangEnglish:            "\nUse `/mode <name>` to switch.\nAvailable: `default` / `edit` / `plan` / `yolo`",
		LangChinese:            "\n使用 `/mode <名称>` 切换模式\n可用值: `default` / `edit` / `plan` / `yolo`",
		LangTraditionalChinese: "\n使用 `/mode <名稱>` 切換模式\n可用值: `default` / `edit` / `plan` / `yolo`",
		LangJapanese:           "\n`/mode <名前>` で切り替え\n選択肢: `default` / `edit` / `plan` / `yolo`",
		LangSpanish:            "\nUse `/mode <nombre>` para cambiar.\nDisponibles: `default` / `edit` / `plan` / `yolo`",
	},
	MsgCronScheduleLabel: {
		LangEnglish:            "Schedule: %s (%s)\n",
		LangChinese:            "调度: %s (%s)\n",
		LangTraditionalChinese: "調度: %s (%s)\n",
		LangJapanese:           "スケジュール: %s (%s)\n",
		LangSpanish:            "Programación: %s (%s)\n",
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
	MsgDeleteUsage: {
		LangEnglish:            "Usage: `/delete <number>` — delete a session by its list number.\nUse `/list` to see session numbers.",
		LangChinese:            "用法：`/delete <序号>` — 按列表序号删除会话。\n使用 `/list` 查看会话序号。",
		LangTraditionalChinese: "用法：`/delete <序號>` — 按列表序號刪除會話。\n使用 `/list` 查看會話序號。",
		LangJapanese:           "使い方：`/delete <番号>` — リスト番号でセッションを削除。\n`/list` で番号を確認できます。",
		LangSpanish:            "Uso: `/delete <número>` — eliminar sesión por número de lista.\nUse `/list` para ver los números.",
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
	MsgRateLimited: {
		LangEnglish:            "⏳ You are sending messages too fast. Please wait a moment.",
		LangChinese:            "⏳ 消息发送过快，请稍后再试。",
		LangTraditionalChinese: "⏳ 訊息發送過快，請稍後再試。",
		LangJapanese:           "⏳ メッセージの送信が速すぎます。しばらくお待ちください。",
		LangSpanish:            "⏳ Estás enviando mensajes demasiado rápido. Espera un momento.",
	},
	MsgRelayNoBinding: {
		LangEnglish: "No relay binding in this chat.\nUse `/bind <project>` to bind another bot.\nThe <project> is the project name from your config.toml.",
		LangChinese: "当前群聊没有中继绑定。\n使用 `/bind <项目名>` 绑定另一个机器人。\n<项目名> 是 config.toml 中 [[projects]] 的 name 字段。",
	},
	MsgRelayBound: {
		LangEnglish: "Current relay binding: %s",
		LangChinese: "当前中继绑定: %s",
	},
	MsgRelayBindOK: {
		LangEnglish: "Relay binding created: %s ↔ %s\n\nYou can now tell either bot to communicate with the other.\nExample: \"Ask %s about ...\"\n\nNote: the relay command uses the exact project name:\n  cc-connect relay send --to %s \"message\"",
		LangChinese: "中继绑定已创建: %s ↔ %s\n\n你现在可以让任一机器人与对方交流。\n示例：\"帮我问 %s ...\"\n\n注意：relay 命令使用的是精确的项目名:\n  cc-connect relay send --to %s \"message\"",
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
		LangEnglish:            "Delete a session by its list number, arg: <number>",
		LangChinese:            "按列表序号删除会话，参数: <序号>",
		LangTraditionalChinese: "按列表序號刪除會話，參數: <序號>",
		LangJapanese:           "リスト番号でセッションを削除、引数: <番号>",
		LangSpanish:            "Eliminar sesión por su número en la lista, arg: <número>",
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
		LangEnglish:            "Manage scheduled tasks, arg: [add|list|del|enable|disable]",
		LangChinese:            "管理定时任务，参数: [add|list|del|enable|disable]",
		LangTraditionalChinese: "管理定時任務，參數: [add|list|del|enable|disable]",
		LangJapanese:           "スケジュールタスク管理、引数: [add|list|del|enable|disable]",
		LangSpanish:            "Gestionar tareas programadas, arg: [add|list|del|enable|disable]",
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
}

func (i *I18n) T(key MsgKey) string {
	lang := i.currentLang()
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
