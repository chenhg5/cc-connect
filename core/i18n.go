package core

import "fmt"

// Language represents a supported language
type Language string

const (
	LangAuto    Language = "" // auto-detect from user messages
	LangEnglish Language = "en"
	LangChinese Language = "zh"
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
	MsgModeChanged          MsgKey = "mode_changed"
	MsgModeNotSupported     MsgKey = "mode_not_supported"
	MsgSessionRestarting    MsgKey = "session_restarting"
	MsgLangChanged          MsgKey = "lang_changed"
	MsgLangInvalid          MsgKey = "lang_invalid"
	MsgLangCurrent          MsgKey = "lang_current"
	MsgHelp                 MsgKey = "help"
	MsgListTitle            MsgKey = "list_title"
	MsgListEmpty            MsgKey = "list_empty"
	MsgListMore             MsgKey = "list_more"
	MsgListSwitchHint       MsgKey = "list_switch_hint"
	MsgListError            MsgKey = "list_error"
	MsgHistoryEmpty         MsgKey = "history_empty"
	MsgProviderNotSupported MsgKey = "provider_not_supported"
	MsgProviderNone         MsgKey = "provider_none"
	MsgProviderCurrent      MsgKey = "provider_current"
	MsgProviderListTitle    MsgKey = "provider_list_title"
	MsgProviderListEmpty    MsgKey = "provider_list_empty"
	MsgProviderSwitchHint   MsgKey = "provider_switch_hint"
	MsgProviderNotFound     MsgKey = "provider_not_found"
	MsgProviderSwitched     MsgKey = "provider_switched"
	MsgProviderAdded        MsgKey = "provider_added"
	MsgProviderAddUsage     MsgKey = "provider_add_usage"
	MsgProviderAddFailed    MsgKey = "provider_add_failed"
	MsgProviderRemoved      MsgKey = "provider_removed"
	MsgProviderRemoveFailed MsgKey = "provider_remove_failed"

	MsgVoiceNotEnabled      MsgKey = "voice_not_enabled"
	MsgVoiceNoFFmpeg        MsgKey = "voice_no_ffmpeg"
	MsgVoiceTranscribing    MsgKey = "voice_transcribing"
	MsgVoiceTranscribed     MsgKey = "voice_transcribed"
	MsgVoiceTranscribeFailed MsgKey = "voice_transcribe_failed"
	MsgVoiceEmpty           MsgKey = "voice_empty"

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
)

var messages = map[MsgKey]map[Language]string{
	MsgStarting: {
		LangEnglish: "⏳ Processing...",
		LangChinese: "⏳ 处理中...",
	},
	MsgThinking: {
		LangEnglish: "💭 %s",
		LangChinese: "💭 %s",
	},
	MsgTool: {
		LangEnglish: "🔧 Tool #%d: **%s**\n`%s`",
		LangChinese: "🔧 工具 #%d: **%s**\n`%s`",
	},
	MsgExecutionStopped: {
		LangEnglish: "⏹ Execution stopped.",
		LangChinese: "⏹ 执行已停止。",
	},
	MsgNoExecution: {
		LangEnglish: "No execution in progress.",
		LangChinese: "没有正在执行的任务。",
	},
	MsgPreviousProcessing: {
		LangEnglish: "⏳ Previous request still processing, please wait...",
		LangChinese: "⏳ 上一个请求仍在处理中，请稍候...",
	},
	MsgNoToolsAllowed: {
		LangEnglish: "No tools pre-allowed.\nUsage: `/allow <tool_name>`\nExample: `/allow Bash`",
		LangChinese: "尚未预授权任何工具。\n用法: `/allow <工具名>`\n示例: `/allow Bash`",
	},
	MsgCurrentTools: {
		LangEnglish: "Pre-allowed tools: %s",
		LangChinese: "预授权的工具: %s",
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
	MsgEmptyResponse: {
		LangEnglish: "(empty response)",
		LangChinese: "(空响应)",
	},
	MsgPermissionPrompt: {
		LangEnglish: "⚠️ **Permission Request**\n\nAgent wants to use **%s**:\n\n`%s`\n\nReply **allow** / **deny** / **allow all** (skip all future prompts this session).",
		LangChinese: "⚠️ **权限请求**\n\nAgent 想要使用 **%s**:\n\n`%s`\n\n回复 **允许** / **拒绝** / **允许所有**（本次会话不再提醒）。",
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
	MsgLangChanged: {
		LangEnglish: "🌐 Language switched to **%s**.",
		LangChinese: "🌐 语言已切换为 **%s**。",
	},
	MsgLangInvalid: {
		LangEnglish: "Unknown language. Supported: `en` (English), `zh` (中文), `auto` (auto-detect).",
		LangChinese: "未知语言。支持: `en` (English), `zh` (中文), `auto` (自动检测)。",
	},
	MsgLangCurrent: {
		LangEnglish: "🌐 Current language: **%s**\n\nUsage: /lang <en|zh|auto>",
		LangChinese: "🌐 当前语言: **%s**\n\n用法: /lang <en|zh|auto>",
	},
	MsgHelp: {
		LangEnglish: "📖 Available Commands\n\n" +
			"/new [name]\n  Start a new session\n\n" +
			"/list\n  List agent sessions\n\n" +
			"/switch <id>\n  Resume an existing session\n\n" +
			"/current\n  Show current active session\n\n" +
			"/history [n]\n  Show last n messages (default 10)\n\n" +
			"/provider [list|add|remove|switch]\n  Manage API providers\n\n" +
			"/allow <tool>\n  Pre-allow a tool (next session)\n\n" +
			"/mode [name]\n  View/switch permission mode\n\n" +
			"/lang [en|zh|auto]\n  View/switch language\n\n" +
			"/quiet\n  Toggle thinking/tool progress\n\n" +
			"/stop\n  Stop current execution\n\n" +
			"/cron [add|list|del|enable|disable]\n  Manage scheduled tasks\n\n" +
			"/version\n  Show cc-connect version\n\n" +
			"/help\n  Show this help\n\n" +
			"Permission modes: default / edit / plan / yolo",
		LangChinese: "📖 可用命令\n\n" +
			"/new [名称]\n  创建新会话\n\n" +
			"/list\n  列出 Agent 会话列表\n\n" +
			"/switch <id>\n  恢复已有会话\n\n" +
			"/current\n  查看当前活跃会话\n\n" +
			"/history [n]\n  查看最近 n 条消息（默认 10）\n\n" +
			"/provider [list|add|remove|switch]\n  管理 API Provider\n\n" +
			"/allow <工具名>\n  预授权工具（下次会话生效）\n\n" +
			"/mode [名称]\n  查看/切换权限模式\n\n" +
			"/lang [en|zh|auto]\n  查看/切换语言\n\n" +
			"/quiet\n  开关思考和工具进度消息\n\n" +
			"/stop\n  停止当前执行\n\n" +
			"/cron [add|list|del|enable|disable]\n  管理定时任务\n\n" +
			"/version\n  查看 cc-connect 版本\n\n" +
			"/help\n  显示此帮助\n\n" +
			"权限模式：default / edit / plan / yolo",
	},
	MsgListTitle: {
		LangEnglish: "**%s Sessions** (%d)\n\n",
		LangChinese: "**%s 会话列表** (%d)\n\n",
	},
	MsgListEmpty: {
		LangEnglish: "No sessions found for this project.",
		LangChinese: "未找到此项目的会话。",
	},
	MsgListMore: {
		LangEnglish: "\n... and %d more\n",
		LangChinese: "\n... 还有 %d 条\n",
	},
	MsgListSwitchHint: {
		LangEnglish: "\n`/switch <id>` to switch session",
		LangChinese: "\n`/switch <id>` 切换会话",
	},
	MsgListError: {
		LangEnglish: "❌ Failed to list sessions: %v",
		LangChinese: "❌ 获取会话列表失败: %v",
	},
	MsgHistoryEmpty: {
		LangEnglish: "No history in current session.",
		LangChinese: "当前会话暂无历史消息。",
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
		LangEnglish: "📡 **Providers**\n\n",
		LangChinese: "📡 **Provider 列表**\n\n",
	},
	MsgProviderListEmpty: {
		LangEnglish: "No providers configured.\n\nAdd providers in `config.toml` or via `cc-connect provider add`.",
		LangChinese: "未配置 Provider。\n\n可在 `config.toml` 中添加或使用 `cc-connect provider add` 命令。",
	},
	MsgProviderSwitchHint: {
		LangEnglish: "`/provider switch <name>` to switch",
		LangChinese: "`/provider switch <名称>` 切换",
	},
	MsgProviderNotFound: {
		LangEnglish: "❌ Provider %q not found. Use `/provider list` to see available providers.",
		LangChinese: "❌ 未找到 Provider %q。使用 `/provider list` 查看可用列表。",
	},
	MsgProviderSwitched: {
		LangEnglish: "✅ Provider switched to **%s**. New sessions will use this provider.",
		LangChinese: "✅ Provider 已切换为 **%s**，新会话将使用此 Provider。",
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
	MsgVoiceNotEnabled: {
		LangEnglish: "🎙 Voice messages are not enabled. Please configure `[speech]` in config.toml.",
		LangChinese: "🎙 语音消息未启用，请在 config.toml 中配置 `[speech]` 部分。",
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
	MsgCronNotAvailable: {
		LangEnglish: "Cron scheduler is not available.",
		LangChinese: "定时任务调度器未启用。",
	},
	MsgCronUsage: {
		LangEnglish: "Usage:\n/cron add <min> <hour> <day> <month> <weekday> <prompt>\n/cron list\n/cron del <id>\n/cron enable <id>\n/cron disable <id>",
		LangChinese: "用法：\n/cron add <分> <时> <日> <月> <周> <任务描述>\n/cron list\n/cron del <id>\n/cron enable <id>\n/cron disable <id>",
	},
	MsgCronAddUsage: {
		LangEnglish: "Usage: /cron add <min> <hour> <day> <month> <weekday> <prompt>\nExample: /cron add 0 6 * * * Collect GitHub trending data and send me a summary",
		LangChinese: "用法：/cron add <分> <时> <日> <月> <周> <任务描述>\n示例：/cron add 0 6 * * * 收集 GitHub Trending 数据整理成简报发给我",
	},
	MsgCronAdded: {
		LangEnglish: "✅ Cron job created\nID: `%s`\nSchedule: `%s`\nPrompt: %s",
		LangChinese: "✅ 定时任务已创建\nID: `%s`\n调度: `%s`\n内容: %s",
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
		LangEnglish: "`/cron del <id>` to remove · `/cron enable/disable <id>` to toggle",
		LangChinese: "`/cron del <id>` 删除 · `/cron enable/disable <id>` 启停",
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
}

func (i *I18n) T(key MsgKey) string {
	lang := i.currentLang()
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
