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
		LangEnglish: "⚠️ **Permission Request**\n\nClaude wants to use **%s**:\n\n`%s`\n\nReply **allow** / **deny** / **allow all** (skip all future prompts this session).",
		LangChinese: "⚠️ **权限请求**\n\nClaude 想要使用 **%s**:\n\n`%s`\n\n回复 **允许** / **拒绝** / **允许所有**（本次会话不再提醒）。",
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
		LangEnglish: "❌ Denied. Claude will stop this tool use.",
		LangChinese: "❌ 已拒绝。Claude 将停止此工具使用。",
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
			"/new [name]\n  Start a new Claude session\n\n" +
			"/list\n  List Claude Code sessions\n\n" +
			"/switch <id>\n  Resume an existing session\n\n" +
			"/current\n  Show current active session\n\n" +
			"/history [n]\n  Show last n messages (default 10)\n\n" +
			"/allow <tool>\n  Pre-allow a tool (next session)\n\n" +
			"/mode [name]\n  View/switch permission mode\n\n" +
			"/lang [en|zh|auto]\n  View/switch language\n\n" +
			"/quiet\n  Toggle thinking/tool progress\n\n" +
			"/stop\n  Stop current execution\n\n" +
			"/help\n  Show this help\n\n" +
			"Permission modes: default / edit / plan / yolo",
		LangChinese: "📖 可用命令\n\n" +
			"/new [名称]\n  创建新的 Claude 会话\n\n" +
			"/list\n  列出 Claude Code 会话列表\n\n" +
			"/switch <id>\n  恢复已有会话\n\n" +
			"/current\n  查看当前活跃会话\n\n" +
			"/history [n]\n  查看最近 n 条消息（默认 10）\n\n" +
			"/allow <工具名>\n  预授权工具（下次会话生效）\n\n" +
			"/mode [名称]\n  查看/切换权限模式\n\n" +
			"/lang [en|zh|auto]\n  查看/切换语言\n\n" +
			"/quiet\n  开关思考和工具进度消息\n\n" +
			"/stop\n  停止当前执行\n\n" +
			"/help\n  显示此帮助\n\n" +
			"权限模式：default / edit / plan / yolo",
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
