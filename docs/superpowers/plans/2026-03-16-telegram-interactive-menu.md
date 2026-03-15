# Telegram 交互式菜单系统实施计划

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 cc-connect Telegram Bot 实现完全基于按钮点击的 /menu 交互式菜单系统，支持两级导航、列表翻页和个性化自定义。

**Architecture:** 在 `core/interfaces.go` 新增 `MenuNavigable` 接口（模仿现有 `CardNavigable` 模式），engine 注册 `handleMenuNavigation` handler，Telegram 平台收到 `menu:` 回调后调用 handler 获取 `*MenuPage` 并 edit 原消息。导航和选择操作由 engine 处理（复用 `executeCardAction`），命令执行在平台层构造 synthetic message 调用 `p.handler()`。

**Tech Stack:** Go, go-telegram-bot-api/v5, JSON 文件持久化（无新增依赖）

**Spec:** `docs/superpowers/specs/2026-03-16-telegram-interactive-menu-design.md`

---

## 文件结构

| 文件 | 操作 | 职责 |
|------|------|------|
| `core/interfaces.go` | 修改 | 新增 MenuState, MenuPage, MenuNavigationHandler, MenuNavigable |
| `core/i18n.go` | 修改 | 新增菜单相关 MsgKey 常量和所有语言翻译 |
| `core/engine.go` | 修改 | Start() 注册 MenuNavigable；新增 handleMenuNavigation + cmdMenu；builtinCommands 加 "menu" |
| `core/engine_test.go` (或新建 `core/menu_test.go`) | 修改/新建 | handleMenuNavigation 单元测试 |
| `platform/telegram/menu.go` | 新建 | MenuPage → tgbotapi.InlineKeyboardMarkup 渲染；menuPageToKeyboard |
| `platform/telegram/menu_config.go` | 新建 | JSON 菜单配置持久化；MenuConfig CRUD |
| `platform/telegram/telegram.go` | 修改 | menuMsgIDs sync.Map；menuHandler；pendingInputs；SetMenuNavigationHandler；SendMenuPage；handleMenuCallback |
| `platform/telegram/menu_test.go` | 新建 | 渲染函数单元测试 |
| `platform/telegram/menu_config_test.go` | 新建 | 配置持久化单元测试 |

---

## Chunk 1: 核心接口与引擎层

### Task 1: 在 core/interfaces.go 新增 MenuPage / MenuNavigable

**Files:**
- Modify: `core/interfaces.go`

- [ ] **Step 1.1: 在 `core/interfaces.go` 末尾追加新接口**

在文件末尾（`ChannelNameResolver` 接口之后）添加：

```go
// MenuState holds current agent state for rendering the menu header.
type MenuState struct {
	Model   string
	Mode    string
	Project string
}

// MenuPage is the fully rendered menu content returned by MenuNavigationHandler.
type MenuPage struct {
	Title    string         // main message text (supports HTML)
	Subtitle string         // secondary info line
	Buttons  [][]ButtonOption // inline keyboard rows; empty = no buttons
}

// MenuNavigationHandler is called by the platform when a menu: callback arrives.
// action is the callback data string (e.g. "menu:main", "menu:cat:ai").
// sessionKey identifies the chat session for state lookup.
// Returns nil to suppress any message update (e.g. for menu:noop).
type MenuNavigationHandler func(action string, sessionKey string) *MenuPage

// MenuNavigable is an optional interface for platforms that support
// the interactive /menu panel with in-place message updates.
type MenuNavigable interface {
	// SetMenuNavigationHandler registers the handler called on menu: callbacks.
	SetMenuNavigationHandler(h MenuNavigationHandler)
	// SendMenuPage sends (or edits) the menu message for the given chat context.
	// On first call per chat, sends a new message and remembers its ID.
	// On subsequent calls (callback-triggered), edits the existing message.
	SendMenuPage(ctx context.Context, replyCtx any, page *MenuPage) error
}
```

- [ ] **Step 1.2: 构建验证**

```bash
cd /root/cc-connect-src && go build ./core/...
```

Expected: 无错误输出

- [ ] **Step 1.3: Commit**

```bash
git add core/interfaces.go
git commit -m "feat(core): add MenuPage/MenuNavigable interface for interactive menu"
```

---

### Task 2: 在 core/i18n.go 新增菜单 MsgKey 和翻译

**Files:**
- Modify: `core/i18n.go`

- [ ] **Step 2.1: 在 MsgKey 常量块末尾（MsgWsCloneFailed 之后）添加菜单常量**

```go
	// Interactive menu (/menu panel)
	MsgMenuTitle             MsgKey = "menu_title"
	MsgMenuSubtitle          MsgKey = "menu_subtitle"
	MsgMenuCatSession        MsgKey = "menu_cat_session"
	MsgMenuCatAI             MsgKey = "menu_cat_ai"
	MsgMenuCatTask           MsgKey = "menu_cat_task"
	MsgMenuCatSystem         MsgKey = "menu_cat_system"
	MsgMenuCatAdvanced       MsgKey = "menu_cat_advanced"
	MsgMenuCatCustom         MsgKey = "menu_cat_custom"
	MsgMenuBack              MsgKey = "menu_back"
	MsgMenuPageInfo          MsgKey = "menu_page_info"
	MsgMenuPagePrev          MsgKey = "menu_page_prev"
	MsgMenuPageNext          MsgKey = "menu_page_next"
	MsgMenuSelectModel       MsgKey = "menu_select_model"
	MsgMenuSelectSession     MsgKey = "menu_select_session"
	MsgMenuSelectProvider    MsgKey = "menu_select_provider"
	MsgMenuSelectMode        MsgKey = "menu_select_mode"
	MsgMenuSelectLang        MsgKey = "menu_select_lang"
	MsgMenuSelectReasoning   MsgKey = "menu_select_reasoning"
	MsgMenuEmpty             MsgKey = "menu_empty"
	MsgMenuCustomTitle       MsgKey = "menu_custom_title"
	MsgMenuCustomPin         MsgKey = "menu_custom_pin"
	MsgMenuCustomHide        MsgKey = "menu_custom_hide"
	MsgMenuCustomAdd         MsgKey = "menu_custom_add"
	MsgMenuCustomDesc        MsgKey = "menu_custom_desc"
	MsgMenuCustomReset       MsgKey = "menu_custom_reset"
	MsgMenuCmdNew            MsgKey = "menu_cmd_new"
	MsgMenuCmdList           MsgKey = "menu_cmd_list"
	MsgMenuCmdSwitch         MsgKey = "menu_cmd_switch"
	MsgMenuCmdDelete         MsgKey = "menu_cmd_delete"
	MsgMenuCmdSearch         MsgKey = "menu_cmd_search"
	MsgMenuCmdRename         MsgKey = "menu_cmd_rename"
	MsgMenuCmdCurrent        MsgKey = "menu_cmd_current"
	MsgMenuCmdHistory        MsgKey = "menu_cmd_history"
	MsgMenuCmdModel          MsgKey = "menu_cmd_model"
	MsgMenuCmdMode           MsgKey = "menu_cmd_mode"
	MsgMenuCmdLang           MsgKey = "menu_cmd_lang"
	MsgMenuCmdReasoning      MsgKey = "menu_cmd_reasoning"
	MsgMenuCmdProvider       MsgKey = "menu_cmd_provider"
	MsgMenuCmdQuiet          MsgKey = "menu_cmd_quiet"
	MsgMenuCmdMemory         MsgKey = "menu_cmd_memory"
	MsgMenuCmdCron           MsgKey = "menu_cmd_cron"
	MsgMenuCmdCompress       MsgKey = "menu_cmd_compress"
	MsgMenuCmdTTS            MsgKey = "menu_cmd_tts"
	MsgMenuCmdStop           MsgKey = "menu_cmd_stop"
	MsgMenuCmdStatus         MsgKey = "menu_cmd_status"
	MsgMenuCmdUsage          MsgKey = "menu_cmd_usage"
	MsgMenuCmdHelp           MsgKey = "menu_cmd_help"
	MsgMenuCmdVersion        MsgKey = "menu_cmd_version"
	MsgMenuCmdConfig         MsgKey = "menu_cmd_config"
	MsgMenuCmdDoctor         MsgKey = "menu_cmd_doctor"
	MsgMenuCmdUpgrade        MsgKey = "menu_cmd_upgrade"
	MsgMenuCmdRestart        MsgKey = "menu_cmd_restart"
	MsgMenuCmdShell          MsgKey = "menu_cmd_shell"
	MsgMenuCmdBind           MsgKey = "menu_cmd_bind"
	MsgMenuCmdWorkspace      MsgKey = "menu_cmd_workspace"
	MsgMenuCmdSkills         MsgKey = "menu_cmd_skills"
	MsgMenuCmdCommands       MsgKey = "menu_cmd_commands"
	MsgMenuCmdAlias          MsgKey = "menu_cmd_alias"
	MsgMenuCmdAllow          MsgKey = "menu_cmd_allow"
	MsgMenuCmdHeartbeat      MsgKey = "menu_cmd_heartbeat"
```

- [ ] **Step 2.2: 在 `var messages` map 末尾（`}` 之前）添加翻译**

```go
	MsgMenuTitle: {
		LangEnglish:            "🤖 <b>cc-connect Control Panel</b>",
		LangChinese:            "🤖 <b>cc-connect 控制面板</b>",
		LangTraditionalChinese: "🤖 <b>cc-connect 控制面板</b>",
		LangJapanese:           "🤖 <b>cc-connect コントロールパネル</b>",
		LangSpanish:            "🤖 <b>Panel de Control cc-connect</b>",
	},
	MsgMenuSubtitle: {
		LangEnglish:            "Model: %s · Mode: %s",
		LangChinese:            "模型：%s · 权限：%s",
		LangTraditionalChinese: "模型：%s · 權限：%s",
		LangJapanese:           "モデル：%s · モード：%s",
		LangSpanish:            "Modelo: %s · Modo: %s",
	},
	MsgMenuCatSession:  {LangEnglish: "💬 Sessions", LangChinese: "💬 会话", LangTraditionalChinese: "💬 對話", LangJapanese: "💬 セッション", LangSpanish: "💬 Sesiones"},
	MsgMenuCatAI:       {LangEnglish: "🤖 AI Settings", LangChinese: "🤖 AI设置", LangTraditionalChinese: "🤖 AI設定", LangJapanese: "🤖 AI設定", LangSpanish: "🤖 Ajustes IA"},
	MsgMenuCatTask:     {LangEnglish: "📋 Tasks", LangChinese: "📋 任务", LangTraditionalChinese: "📋 任務", LangJapanese: "📋 タスク", LangSpanish: "📋 Tareas"},
	MsgMenuCatSystem:   {LangEnglish: "🔧 System", LangChinese: "🔧 系统", LangTraditionalChinese: "🔧 系統", LangJapanese: "🔧 システム", LangSpanish: "🔧 Sistema"},
	MsgMenuCatAdvanced: {LangEnglish: "⚡ Advanced", LangChinese: "⚡ 高级功能", LangTraditionalChinese: "⚡ 進階功能", LangJapanese: "⚡ 高度な機能", LangSpanish: "⚡ Avanzado"},
	MsgMenuCatCustom:   {LangEnglish: "⚙️ Customize", LangChinese: "⚙️ 自定义菜单", LangTraditionalChinese: "⚙️ 自訂選單", LangJapanese: "⚙️ カスタマイズ", LangSpanish: "⚙️ Personalizar"},
	MsgMenuBack:        {LangEnglish: "◀ Back", LangChinese: "◀ 返回主菜单", LangTraditionalChinese: "◀ 返回主選單", LangJapanese: "◀ メインメニューへ", LangSpanish: "◀ Menú Principal"},
	MsgMenuPageInfo:    {LangEnglish: "Page %d/%d", LangChinese: "第 %d/%d 页", LangTraditionalChinese: "第 %d/%d 頁", LangJapanese: "%d/%d ページ", LangSpanish: "Pág. %d/%d"},
	MsgMenuPagePrev:    {LangEnglish: "◀ Prev", LangChinese: "◀ 上页", LangTraditionalChinese: "◀ 上頁", LangJapanese: "◀ 前へ", LangSpanish: "◀ Anterior"},
	MsgMenuPageNext:    {LangEnglish: "Next ▶", LangChinese: "下页 ▶", LangTraditionalChinese: "下頁 ▶", LangJapanese: "次へ ▶", LangSpanish: "Siguiente ▶"},
	MsgMenuEmpty:       {LangEnglish: "(no items)", LangChinese: "（暂无可选项）", LangTraditionalChinese: "（暫無選項）", LangJapanese: "（項目なし）", LangSpanish: "(sin opciones)"},
	MsgMenuSelectModel:     {LangEnglish: "🧠 Select Model", LangChinese: "🧠 选择模型", LangTraditionalChinese: "🧠 選擇模型", LangJapanese: "🧠 モデル選択", LangSpanish: "🧠 Seleccionar Modelo"},
	MsgMenuSelectSession:   {LangEnglish: "🔀 Select Session", LangChinese: "🔀 选择会话", LangTraditionalChinese: "🔀 選擇對話", LangJapanese: "🔀 セッション選択", LangSpanish: "🔀 Seleccionar Sesión"},
	MsgMenuSelectProvider:  {LangEnglish: "🔌 Select Provider", LangChinese: "🔌 选择提供商", LangTraditionalChinese: "🔌 選擇提供商", LangJapanese: "🔌 プロバイダー選択", LangSpanish: "🔌 Seleccionar Proveedor"},
	MsgMenuSelectMode:      {LangEnglish: "🔒 Select Mode", LangChinese: "🔒 选择权限模式", LangTraditionalChinese: "🔒 選擇權限模式", LangJapanese: "🔒 モード選択", LangSpanish: "🔒 Seleccionar Modo"},
	MsgMenuSelectLang:      {LangEnglish: "🌐 Select Language", LangChinese: "🌐 选择语言", LangTraditionalChinese: "🌐 選擇語言", LangJapanese: "🌐 言語選択", LangSpanish: "🌐 Seleccionar Idioma"},
	MsgMenuSelectReasoning: {LangEnglish: "💡 Select Reasoning Effort", LangChinese: "💡 选择推理强度", LangTraditionalChinese: "💡 選擇推理強度", LangJapanese: "💡 推論強度選択", LangSpanish: "💡 Nivel de Razonamiento"},
	MsgMenuCustomTitle:  {LangEnglish: "⚙️ Customize Menu", LangChinese: "⚙️ 自定义菜单", LangTraditionalChinese: "⚙️ 自訂選單", LangJapanese: "⚙️ メニュー設定", LangSpanish: "⚙️ Personalizar Menú"},
	MsgMenuCustomPin:    {LangEnglish: "📌 Pin Shortcuts", LangChinese: "📌 固定快捷命令", LangTraditionalChinese: "📌 固定捷徑", LangJapanese: "📌 ショートカット固定", LangSpanish: "📌 Anclar Accesos"},
	MsgMenuCustomHide:   {LangEnglish: "👁️ Show/Hide Categories", LangChinese: "👁️ 显示/隐藏分类", LangTraditionalChinese: "👁️ 顯示/隱藏分類", LangJapanese: "👁️ カテゴリ表示切替", LangSpanish: "👁️ Mostrar/Ocultar"},
	MsgMenuCustomAdd:    {LangEnglish: "➕ Show Custom Commands", LangChinese: "➕ 展示自定义命令", LangTraditionalChinese: "➕ 顯示自訂命令", LangJapanese: "➕ カスタムコマンド", LangSpanish: "➕ Mostrar Comandos"},
	MsgMenuCustomDesc:   {LangEnglish: "🔤 Edit Command Descriptions", LangChinese: "🔤 修改命令列表描述", LangTraditionalChinese: "🔤 修改命令描述", LangJapanese: "🔤 コマンド説明編集", LangSpanish: "🔤 Editar Descripciones"},
	MsgMenuCustomReset:  {LangEnglish: "↺ Reset to Defaults", LangChinese: "↺ 恢复默认设置", LangTraditionalChinese: "↺ 恢復預設", LangJapanese: "↺ デフォルトに戻す", LangSpanish: "↺ Restablecer"},
	MsgMenuCmdNew:       {LangEnglish: "➕ New Session", LangChinese: "➕ 新建会话"},
	MsgMenuCmdList:      {LangEnglish: "📋 Session List", LangChinese: "📋 会话列表"},
	MsgMenuCmdSwitch:    {LangEnglish: "🔀 Switch Session", LangChinese: "🔀 切换会话"},
	MsgMenuCmdDelete:    {LangEnglish: "🗑️ Delete Session", LangChinese: "🗑️ 删除会话"},
	MsgMenuCmdSearch:    {LangEnglish: "🔍 Search", LangChinese: "🔍 搜索会话"},
	MsgMenuCmdRename:    {LangEnglish: "✏️ Rename Session", LangChinese: "✏️ 重命名会话"},
	MsgMenuCmdCurrent:   {LangEnglish: "📍 Current Session", LangChinese: "📍 当前会话"},
	MsgMenuCmdHistory:   {LangEnglish: "📜 History", LangChinese: "📜 对话历史"},
	MsgMenuCmdModel:     {LangEnglish: "🧠 Switch Model", LangChinese: "🧠 切换模型"},
	MsgMenuCmdMode:      {LangEnglish: "🔒 Permission Mode", LangChinese: "🔒 权限模式"},
	MsgMenuCmdLang:      {LangEnglish: "🌐 Language", LangChinese: "🌐 切换语言"},
	MsgMenuCmdReasoning: {LangEnglish: "💡 Reasoning Effort", LangChinese: "💡 推理强度"},
	MsgMenuCmdProvider:  {LangEnglish: "🔌 Switch Provider", LangChinese: "🔌 切换提供商"},
	MsgMenuCmdQuiet:     {LangEnglish: "🔕 Quiet Mode", LangChinese: "🔕 静默模式"},
	MsgMenuCmdMemory:    {LangEnglish: "🧠 Memory File", LangChinese: "🧠 记忆文件"},
	MsgMenuCmdCron:      {LangEnglish: "⏰ Scheduled Tasks", LangChinese: "⏰ 定时任务"},
	MsgMenuCmdCompress:  {LangEnglish: "🗜️ Compress Context", LangChinese: "🗜️ 压缩上下文"},
	MsgMenuCmdTTS:       {LangEnglish: "🔊 Text-to-Speech", LangChinese: "🔊 语音合成"},
	MsgMenuCmdStop:      {LangEnglish: "⏹ Stop", LangChinese: "⏹ 停止执行"},
	MsgMenuCmdStatus:    {LangEnglish: "📊 Status", LangChinese: "📊 当前状态"},
	MsgMenuCmdUsage:     {LangEnglish: "📈 Usage", LangChinese: "📈 用量统计"},
	MsgMenuCmdHelp:      {LangEnglish: "❓ Help", LangChinese: "❓ 帮助说明"},
	MsgMenuCmdVersion:   {LangEnglish: "🏷️ Version", LangChinese: "🏷️ 版本信息"},
	MsgMenuCmdConfig:    {LangEnglish: "⚙️ Config", LangChinese: "⚙️ 配置管理"},
	MsgMenuCmdDoctor:    {LangEnglish: "🩺 Doctor", LangChinese: "🩺 健康诊断"},
	MsgMenuCmdUpgrade:   {LangEnglish: "⬆️ Upgrade", LangChinese: "⬆️ 升级更新"},
	MsgMenuCmdRestart:   {LangEnglish: "🔄 Restart", LangChinese: "🔄 重启服务"},
	MsgMenuCmdShell:     {LangEnglish: "💻 Shell", LangChinese: "💻 Shell 命令"},
	MsgMenuCmdBind:      {LangEnglish: "🔗 Bind Relay", LangChinese: "🔗 绑定中继"},
	MsgMenuCmdWorkspace: {LangEnglish: "📁 Workspace", LangChinese: "📁 工作区"},
	MsgMenuCmdSkills:    {LangEnglish: "🎯 Skills", LangChinese: "🎯 技能列表"},
	MsgMenuCmdCommands:  {LangEnglish: "📝 Commands", LangChinese: "📝 自定义命令"},
	MsgMenuCmdAlias:     {LangEnglish: "🏷️ Aliases", LangChinese: "🏷️ 命令别名"},
	MsgMenuCmdAllow:     {LangEnglish: "✅ Allow Tool", LangChinese: "✅ 允许工具"},
	MsgMenuCmdHeartbeat: {LangEnglish: "💓 Heartbeat", LangChinese: "💓 心跳配置"},
```

- [ ] **Step 2.2b: 为 `MsgMenuCmd*` 补充 ZH-TW / JA / ES 翻译**

在 messages map 中，将所有 `MsgMenuCmd*` 条目扩展为5语言（示例格式，其余按此规律补全）：

```go
	MsgMenuCmdNew:       {LangEnglish: "➕ New Session",     LangChinese: "➕ 新建会话",     LangTraditionalChinese: "➕ 新建對話",     LangJapanese: "➕ 新しいセッション", LangSpanish: "➕ Nueva Sesión"},
	MsgMenuCmdList:      {LangEnglish: "📋 Session List",    LangChinese: "📋 会话列表",     LangTraditionalChinese: "📋 對話列表",     LangJapanese: "📋 セッション一覧",   LangSpanish: "📋 Lista Sesiones"},
	MsgMenuCmdSwitch:    {LangEnglish: "🔀 Switch Session",  LangChinese: "🔀 切换会话",     LangTraditionalChinese: "🔀 切換對話",     LangJapanese: "🔀 セッション切替",   LangSpanish: "🔀 Cambiar Sesión"},
	MsgMenuCmdDelete:    {LangEnglish: "🗑️ Delete Session",  LangChinese: "🗑️ 删除会话",     LangTraditionalChinese: "🗑️ 刪除對話",     LangJapanese: "🗑️ セッション削除",   LangSpanish: "🗑️ Eliminar Sesión"},
	MsgMenuCmdSearch:    {LangEnglish: "🔍 Search",          LangChinese: "🔍 搜索会话",     LangTraditionalChinese: "🔍 搜尋對話",     LangJapanese: "🔍 検索",             LangSpanish: "🔍 Buscar"},
	MsgMenuCmdRename:    {LangEnglish: "✏️ Rename Session",  LangChinese: "✏️ 重命名会话",   LangTraditionalChinese: "✏️ 重新命名",     LangJapanese: "✏️ 名前変更",         LangSpanish: "✏️ Renombrar"},
	MsgMenuCmdCurrent:   {LangEnglish: "📍 Current Session", LangChinese: "📍 当前会话",     LangTraditionalChinese: "📍 目前對話",     LangJapanese: "📍 現在のセッション", LangSpanish: "📍 Sesión Actual"},
	MsgMenuCmdHistory:   {LangEnglish: "📜 History",         LangChinese: "📜 对话历史",     LangTraditionalChinese: "📜 對話記錄",     LangJapanese: "📜 履歴",             LangSpanish: "📜 Historial"},
	MsgMenuCmdModel:     {LangEnglish: "🧠 Switch Model",    LangChinese: "🧠 切换模型",     LangTraditionalChinese: "🧠 切換模型",     LangJapanese: "🧠 モデル切替",       LangSpanish: "🧠 Cambiar Modelo"},
	MsgMenuCmdMode:      {LangEnglish: "🔒 Permission Mode", LangChinese: "🔒 权限模式",     LangTraditionalChinese: "🔒 權限模式",     LangJapanese: "🔒 権限モード",       LangSpanish: "🔒 Modo Permiso"},
	MsgMenuCmdLang:      {LangEnglish: "🌐 Language",        LangChinese: "🌐 切换语言",     LangTraditionalChinese: "🌐 切換語言",     LangJapanese: "🌐 言語",             LangSpanish: "🌐 Idioma"},
	MsgMenuCmdReasoning: {LangEnglish: "💡 Reasoning",       LangChinese: "💡 推理强度",     LangTraditionalChinese: "💡 推理強度",     LangJapanese: "💡 推論強度",         LangSpanish: "💡 Razonamiento"},
	MsgMenuCmdProvider:  {LangEnglish: "🔌 Provider",        LangChinese: "🔌 切换提供商",   LangTraditionalChinese: "🔌 切換提供商",   LangJapanese: "🔌 プロバイダー",     LangSpanish: "🔌 Proveedor"},
	MsgMenuCmdQuiet:     {LangEnglish: "🔕 Quiet Mode",      LangChinese: "🔕 静默模式",     LangTraditionalChinese: "🔕 靜音模式",     LangJapanese: "🔕 静音モード",       LangSpanish: "🔕 Modo Silencio"},
	MsgMenuCmdMemory:    {LangEnglish: "🧠 Memory File",     LangChinese: "🧠 记忆文件",     LangTraditionalChinese: "🧠 記憶檔案",     LangJapanese: "🧠 メモリファイル",   LangSpanish: "🧠 Archivo Memoria"},
	MsgMenuCmdCron:      {LangEnglish: "⏰ Cron Tasks",      LangChinese: "⏰ 定时任务",     LangTraditionalChinese: "⏰ 定時任務",     LangJapanese: "⏰ 定期タスク",       LangSpanish: "⏰ Tareas Cron"},
	MsgMenuCmdCompress:  {LangEnglish: "🗜️ Compress",        LangChinese: "🗜️ 压缩上下文",   LangTraditionalChinese: "🗜️ 壓縮上下文",   LangJapanese: "🗜️ コンテキスト圧縮", LangSpanish: "🗜️ Comprimir"},
	MsgMenuCmdTTS:       {LangEnglish: "🔊 TTS",             LangChinese: "🔊 语音合成",     LangTraditionalChinese: "🔊 語音合成",     LangJapanese: "🔊 音声合成",         LangSpanish: "🔊 Texto a Voz"},
	MsgMenuCmdStop:      {LangEnglish: "⏹ Stop",             LangChinese: "⏹ 停止执行",     LangTraditionalChinese: "⏹ 停止執行",     LangJapanese: "⏹ 停止",             LangSpanish: "⏹ Detener"},
	MsgMenuCmdStatus:    {LangEnglish: "📊 Status",          LangChinese: "📊 当前状态",     LangTraditionalChinese: "📊 目前狀態",     LangJapanese: "📊 ステータス",       LangSpanish: "📊 Estado"},
	MsgMenuCmdUsage:     {LangEnglish: "📈 Usage",           LangChinese: "📈 用量统计",     LangTraditionalChinese: "📈 用量統計",     LangJapanese: "📈 使用量",           LangSpanish: "📈 Uso"},
	MsgMenuCmdHelp:      {LangEnglish: "❓ Help",            LangChinese: "❓ 帮助说明",     LangTraditionalChinese: "❓ 說明",         LangJapanese: "❓ ヘルプ",           LangSpanish: "❓ Ayuda"},
	MsgMenuCmdVersion:   {LangEnglish: "🏷️ Version",         LangChinese: "🏷️ 版本信息",     LangTraditionalChinese: "🏷️ 版本資訊",     LangJapanese: "🏷️ バージョン",       LangSpanish: "🏷️ Versión"},
	MsgMenuCmdConfig:    {LangEnglish: "⚙️ Config",          LangChinese: "⚙️ 配置管理",     LangTraditionalChinese: "⚙️ 組態管理",     LangJapanese: "⚙️ 設定",             LangSpanish: "⚙️ Configuración"},
	MsgMenuCmdDoctor:    {LangEnglish: "🩺 Doctor",          LangChinese: "🩺 健康诊断",     LangTraditionalChinese: "🩺 健康診斷",     LangJapanese: "🩺 診断",             LangSpanish: "🩺 Diagnóstico"},
	MsgMenuCmdUpgrade:   {LangEnglish: "⬆️ Upgrade",         LangChinese: "⬆️ 升级更新",     LangTraditionalChinese: "⬆️ 升級更新",     LangJapanese: "⬆️ アップグレード",   LangSpanish: "⬆️ Actualizar"},
	MsgMenuCmdRestart:   {LangEnglish: "🔄 Restart",         LangChinese: "🔄 重启服务",     LangTraditionalChinese: "🔄 重新啟動",     LangJapanese: "🔄 再起動",           LangSpanish: "🔄 Reiniciar"},
	MsgMenuCmdShell:     {LangEnglish: "💻 Shell",           LangChinese: "💻 Shell 命令",   LangTraditionalChinese: "💻 Shell 指令",   LangJapanese: "💻 シェル",           LangSpanish: "💻 Shell"},
	MsgMenuCmdBind:      {LangEnglish: "🔗 Bind Relay",      LangChinese: "🔗 绑定中继",     LangTraditionalChinese: "🔗 綁定中繼",     LangJapanese: "🔗 リレーバインド",   LangSpanish: "🔗 Enlazar Relay"},
	MsgMenuCmdWorkspace: {LangEnglish: "📁 Workspace",       LangChinese: "📁 工作区",       LangTraditionalChinese: "📁 工作區",       LangJapanese: "📁 ワークスペース",   LangSpanish: "📁 Espacio Trabajo"},
	MsgMenuCmdSkills:    {LangEnglish: "🎯 Skills",          LangChinese: "🎯 技能列表",     LangTraditionalChinese: "🎯 技能列表",     LangJapanese: "🎯 スキル",           LangSpanish: "🎯 Habilidades"},
	MsgMenuCmdCommands:  {LangEnglish: "📝 Commands",        LangChinese: "📝 自定义命令",   LangTraditionalChinese: "📝 自訂命令",     LangJapanese: "📝 カスタムコマンド", LangSpanish: "📝 Comandos"},
	MsgMenuCmdAlias:     {LangEnglish: "🏷️ Aliases",         LangChinese: "🏷️ 命令别名",     LangTraditionalChinese: "🏷️ 命令別名",     LangJapanese: "🏷️ エイリアス",       LangSpanish: "🏷️ Alias"},
	MsgMenuCmdAllow:     {LangEnglish: "✅ Allow Tool",       LangChinese: "✅ 允许工具",     LangTraditionalChinese: "✅ 允許工具",     LangJapanese: "✅ ツール許可",       LangSpanish: "✅ Permitir Tool"},
	MsgMenuCmdHeartbeat: {LangEnglish: "💓 Heartbeat",       LangChinese: "💓 心跳配置",     LangTraditionalChinese: "💓 心跳設定",     LangJapanese: "💓 ハートビート",     LangSpanish: "💓 Latido"},
```

注意：以上代码**替换** Step 2.2 中原来的 `MsgMenuCmd*` 条目（只有 EN/ZH 的那些），保持相同的 key 名称。

- [ ] **Step 2.3: 构建验证**

```bash
go build ./core/...
```

Expected: 无错误

- [ ] **Step 2.4: Commit**

```bash
git add core/i18n.go
git commit -m "feat(core): add i18n keys for interactive menu system"
```

---

### Task 3: 在 core/engine.go 实现 handleMenuNavigation

**Files:**
- Modify: `core/engine.go`

- [ ] **Step 3.1: 在文件末尾添加菜单分类定义和 buildMenuPage 辅助函数**

在 `engine.go` 末尾添加（`ChannelNameResolver` 相关代码之后）：

```go
// ── Interactive Menu (/menu panel) ──────────────────────────────────────────

// menuCategory defines a category in the /menu panel.
type menuCategory struct {
	key      string  // used in callback data: "session", "ai", "task", "system", "advanced"
	labelKey MsgKey  // i18n label for the category button
}

// menuCategories is the ordered list of categories shown in the main menu.
var menuCategories = []menuCategory{
	{"session",  MsgMenuCatSession},
	{"ai",       MsgMenuCatAI},
	{"task",     MsgMenuCatTask},
	{"system",   MsgMenuCatSystem},
	{"advanced", MsgMenuCatAdvanced},
}

// menuCategoryCommands maps category key → ordered list of (callbackSuffix, labelKey).
// callbackSuffix is appended to "menu:exec:" or "menu:list:" depending on command type.
var menuCategoryCommands = map[string][]struct {
	action   string // full callback data: "menu:exec:new" or "menu:list:session:0"
	labelKey MsgKey
}{
	"session": {
		{"menu:list:session:0", MsgMenuCmdSwitch},
		{"menu:exec:new",       MsgMenuCmdNew},
		{"menu:exec:current",   MsgMenuCmdCurrent},
		{"menu:exec:history",   MsgMenuCmdHistory},
		{"menu:exec:name",      MsgMenuCmdRename},
		{"menu:exec:delete",    MsgMenuCmdDelete}, // dispatches /delete to engine's interactive delete-mode
		{"menu:exec:search",    MsgMenuCmdSearch},
	},
	"ai": {
		{"menu:list:model:0",     MsgMenuCmdModel},
		{"menu:list:mode:0",      MsgMenuCmdMode},
		{"menu:list:lang:0",      MsgMenuCmdLang},
		{"menu:list:reasoning:0", MsgMenuCmdReasoning},
		{"menu:list:provider:0",  MsgMenuCmdProvider},
		{"menu:exec:quiet",       MsgMenuCmdQuiet},
	},
	"task": {
		{"menu:exec:memory",   MsgMenuCmdMemory},
		{"menu:exec:cron",     MsgMenuCmdCron},
		{"menu:exec:compress", MsgMenuCmdCompress},
		{"menu:exec:tts",      MsgMenuCmdTTS},
		{"menu:exec:stop",     MsgMenuCmdStop},
	},
	"system": {
		{"menu:exec:status",  MsgMenuCmdStatus},
		{"menu:exec:usage",   MsgMenuCmdUsage},
		{"menu:exec:help",    MsgMenuCmdHelp},
		{"menu:exec:version", MsgMenuCmdVersion},
		{"menu:exec:config",  MsgMenuCmdConfig},
		{"menu:exec:doctor",  MsgMenuCmdDoctor},
		{"menu:exec:upgrade", MsgMenuCmdUpgrade},
		{"menu:exec:restart", MsgMenuCmdRestart},
	},
	"advanced": {
		{"menu:exec:shell",     MsgMenuCmdShell},
		{"menu:exec:bind",      MsgMenuCmdBind},
		{"menu:exec:workspace", MsgMenuCmdWorkspace},
		{"menu:exec:skills",    MsgMenuCmdSkills},
		{"menu:exec:commands",  MsgMenuCmdCommands},
		{"menu:exec:alias",     MsgMenuCmdAlias},
		{"menu:exec:allow",     MsgMenuCmdAllow},
		{"menu:exec:heartbeat", MsgMenuCmdHeartbeat},
	},
}

// menuPageSize is the number of items per page in list-type menus.
const menuPageSize = 5

// buildMenuButtons arranges a flat list of ButtonOption into rows of 2,
// with a back button as the final single-item row.
func buildMenuButtons(items []ButtonOption, backAction string, backLabel string) [][]ButtonOption {
	var rows [][]ButtonOption
	for i := 0; i < len(items); i += 2 {
		if i+1 < len(items) {
			rows = append(rows, []ButtonOption{items[i], items[i+1]})
		} else {
			rows = append(rows, []ButtonOption{items[i]})
		}
	}
	rows = append(rows, []ButtonOption{{Text: backLabel, Data: backAction}})
	return rows
}

// handleMenuNavigation is the MenuNavigationHandler registered with MenuNavigable platforms.
// It receives a menu: action and returns the MenuPage to display.
func (e *Engine) handleMenuNavigation(action string, sessionKey string) *MenuPage {
	switch {
	case action == "menu:noop":
		return nil

	case action == "menu:main":
		return e.buildMainMenuPage(sessionKey)

	case strings.HasPrefix(action, "menu:cat:"):
		cat := strings.TrimPrefix(action, "menu:cat:")
		return e.buildCategoryPage(cat)

	case strings.HasPrefix(action, "menu:list:"):
		// format: menu:list:{cmd}:{page}
		parts := strings.SplitN(strings.TrimPrefix(action, "menu:list:"), ":", 2)
		if len(parts) != 2 {
			return e.buildMainMenuPage(sessionKey)
		}
		cmd := parts[0]
		page, _ := strconv.Atoi(parts[1])
		return e.buildListPage(cmd, page, sessionKey)

	case strings.HasPrefix(action, "menu:sel:"):
		// format: menu:sel:{cmd}:{idx}  (idx is 1-based global index)
		parts := strings.SplitN(strings.TrimPrefix(action, "menu:sel:"), ":", 2)
		if len(parts) != 2 {
			return e.buildMainMenuPage(sessionKey)
		}
		cmd := parts[0]
		idxStr := parts[1]
		e.executeCardAction("/"+cmd, idxStr, sessionKey)
		return e.buildMainMenuPage(sessionKey)

	case action == "menu:cat:custom":
		return e.buildCustomMenuPage()

	case strings.HasPrefix(action, "menu:custom:"):
		return e.buildCustomSubPage(strings.TrimPrefix(action, "menu:custom:"), sessionKey)
	}
	return e.buildMainMenuPage(sessionKey)
}

// buildMainMenuPage constructs the top-level /menu page.
func (e *Engine) buildMainMenuPage(sessionKey string) *MenuPage {
	agent, _ := e.sessionContextForKey(sessionKey)
	state := MenuState{Project: e.name}
	if ms, ok := agent.(ModelSwitcher); ok {
		state.Model = ms.GetModel()
	}
	if ms, ok := agent.(ModeSwitcher); ok {
		state.Mode = ms.GetMode()
	}

	title := e.i18n.T(MsgMenuTitle)
	subtitle := ""
	if state.Model != "" || state.Mode != "" {
		model := state.Model
		if model == "" {
			model = "default"
		}
		mode := state.Mode
		if mode == "" {
			mode = "default"
		}
		subtitle = e.i18n.Tf(MsgMenuSubtitle, model, mode)
	}

	// Build category buttons (2 per row)
	var catBtns []ButtonOption
	for _, cat := range menuCategories {
		catBtns = append(catBtns, ButtonOption{
			Text: e.i18n.T(cat.labelKey),
			Data: "menu:cat:" + cat.key,
		})
	}
	var rows [][]ButtonOption
	for i := 0; i+1 < len(catBtns); i += 2 {
		rows = append(rows, []ButtonOption{catBtns[i], catBtns[i+1]})
	}
	if len(catBtns)%2 != 0 {
		rows = append(rows, []ButtonOption{catBtns[len(catBtns)-1]})
	}
	// Custom button as its own row
	rows = append(rows, []ButtonOption{{
		Text: e.i18n.T(MsgMenuCatCustom),
		Data: "menu:cat:custom",
	}})

	return &MenuPage{Title: title, Subtitle: subtitle, Buttons: rows}
}

// buildCategoryPage constructs a sub-menu page for the given category key.
func (e *Engine) buildCategoryPage(cat string) *MenuPage {
	cmds, ok := menuCategoryCommands[cat]
	if !ok {
		return e.buildMainMenuPage("")
	}

	var labelKey MsgKey
	for _, c := range menuCategories {
		if c.key == cat {
			labelKey = c.labelKey
			break
		}
	}

	var items []ButtonOption
	for _, c := range cmds {
		items = append(items, ButtonOption{
			Text: e.i18n.T(c.labelKey),
			Data: c.action,
		})
	}
	rows := buildMenuButtons(items, "menu:main", e.i18n.T(MsgMenuBack))

	title := e.i18n.T(labelKey)
	return &MenuPage{Title: title, Buttons: rows}
}

// buildListPage constructs a paginated list page for list-type commands.
// page is 0-based.
func (e *Engine) buildListPage(cmd string, page int, sessionKey string) *MenuPage {
	type listItem struct{ label, selAction string }

	var items []listItem
	var titleKey MsgKey
	var backAction string

	fetchCtx, cancel := context.WithTimeout(e.ctx, 3*time.Second)
	defer cancel()

	switch cmd {
	case "model":
		titleKey = MsgMenuSelectModel
		backAction = "menu:cat:ai"
		agent, _ := e.sessionContextForKey(sessionKey)
		if sw, ok := agent.(ModelSwitcher); ok {
			models := sw.AvailableModels(fetchCtx)
			current := sw.GetModel()
			for i, m := range models {
				label := m.Name
				if m.Name == current {
					label = "✅ " + label
				}
				items = append(items, listItem{label, fmt.Sprintf("menu:sel:model:%d", i+1)})
			}
		}

	case "mode":
		titleKey = MsgMenuSelectMode
		backAction = "menu:cat:ai"
		agent, _ := e.sessionContextForKey(sessionKey)
		if sw, ok := agent.(ModeSwitcher); ok {
			current := sw.GetMode()
			for i, m := range sw.PermissionModes() {
				label := m.Name
				if m.NameZh != "" && e.i18n.CurrentLang() == LangChinese {
					label = m.NameZh
				}
				if m.Key == current {
					label = "✅ " + label
				}
				items = append(items, listItem{label, fmt.Sprintf("menu:sel:mode:%d", i+1)})
			}
		}

	case "reasoning":
		titleKey = MsgMenuSelectReasoning
		backAction = "menu:cat:ai"
		agent, _ := e.sessionContextForKey(sessionKey)
		if sw, ok := agent.(ReasoningEffortSwitcher); ok {
			current := sw.GetReasoningEffort()
			for i, effort := range sw.AvailableReasoningEfforts() {
				label := effort
				if effort == current {
					label = "✅ " + label
				}
				items = append(items, listItem{label, fmt.Sprintf("menu:sel:reasoning:%d", i+1)})
			}
		}

	case "lang":
		titleKey = MsgMenuSelectLang
		backAction = "menu:cat:ai"
		langs := []struct{ code, name string }{
			{"zh", "🇨🇳 中文"},
			{"en", "🇺🇸 English"},
			{"zh-TW", "🇹🇼 繁體中文"}, // must match LangTraditionalChinese = "zh-TW"
			{"ja", "🇯🇵 日本語"},
			{"es", "🇪🇸 Español"},
		}
		current := string(e.i18n.CurrentLang())
		for _, l := range langs {
			label := l.name
			if l.code == current {
				label = "✅ " + label
			}
			// Use language code directly as selector arg (not integer index)
			items = append(items, listItem{label, "menu:sel:lang:" + l.code})
		}

	case "provider":
		titleKey = MsgMenuSelectProvider
		backAction = "menu:cat:ai"
		agent, _ := e.sessionContextForKey(sessionKey)
		if sw, ok := agent.(ProviderSwitcher); ok {
			active := sw.GetActiveProvider()
			for i, p := range sw.ListProviders() {
				label := p.Name
				if active != nil && p.Name == active.Name {
					label = "✅ " + label
				}
				items = append(items, listItem{label, fmt.Sprintf("menu:sel:provider:%d", i+1)})
			}
		}

	case "session":
		titleKey = MsgMenuSelectSession
		backAction = "menu:cat:session"
		agent, sessions := e.sessionContextForKey(sessionKey)
		agentSessions, err := agent.ListSessions(fetchCtx)
		if err == nil {
			active := sessions.ActiveSessionID(sessionKey)
			for i, s := range agentSessions {
				name := sessions.GetSessionName(s.ID)
				if name == "" {
					name = s.ID
				}
				if len(name) > 20 {
					name = name[:20] + "…"
				}
				label := name
				if s.ID == active {
					label = "✅ " + label
				}
				items = append(items, listItem{label, fmt.Sprintf("menu:sel:session:%d", i+1)})
			}
		}

	default:
		return e.buildMainMenuPage(sessionKey)
	}

	// Paginate
	total := len(items)
	if total == 0 {
		emptyBtn := ButtonOption{Text: e.i18n.T(MsgMenuEmpty), Data: "menu:noop"}
		backBtn := ButtonOption{Text: e.i18n.T(MsgMenuBack), Data: backAction}
		title := e.i18n.T(titleKey)
		return &MenuPage{Title: title, Buttons: [][]ButtonOption{{emptyBtn}, {backBtn}}}
	}

	totalPages := (total + menuPageSize - 1) / menuPageSize
	if page < 0 {
		page = 0
	}
	if page >= totalPages {
		page = totalPages - 1
	}
	start := page * menuPageSize
	end := start + menuPageSize
	if end > total {
		end = total
	}
	pageItems := items[start:end]

	pageInfo := e.i18n.Tf(MsgMenuPageInfo, page+1, totalPages)
	title := e.i18n.T(titleKey) + "  " + pageInfo

	var rows [][]ButtonOption
	for _, item := range pageItems {
		rows = append(rows, []ButtonOption{{Text: item.label, Data: item.selAction}})
	}

	// Navigation row: prev | back | next
	prevData := "menu:noop"
	if page > 0 {
		prevData = fmt.Sprintf("menu:list:%s:%d", cmd, page-1)
	}
	nextData := "menu:noop"
	if page < totalPages-1 {
		nextData = fmt.Sprintf("menu:list:%s:%d", cmd, page+1)
	}
	rows = append(rows, []ButtonOption{
		{Text: e.i18n.T(MsgMenuPagePrev), Data: prevData},
		{Text: e.i18n.T(MsgMenuBack), Data: backAction},
		{Text: e.i18n.T(MsgMenuPageNext), Data: nextData},
	})

	return &MenuPage{Title: title, Buttons: rows}
}

// buildCustomMenuPage constructs the ⚙️ customize panel.
func (e *Engine) buildCustomMenuPage() *MenuPage {
	items := []ButtonOption{
		{Text: e.i18n.T(MsgMenuCustomPin),   Data: "menu:custom:pin"},
		{Text: e.i18n.T(MsgMenuCustomHide),  Data: "menu:custom:hide"},
		{Text: e.i18n.T(MsgMenuCustomAdd),   Data: "menu:custom:add"},
		{Text: e.i18n.T(MsgMenuCustomDesc),  Data: "menu:custom:desc"},
		{Text: e.i18n.T(MsgMenuCustomReset), Data: "menu:custom:reset"},
	}
	rows := buildMenuButtons(items, "menu:main", e.i18n.T(MsgMenuBack))
	return &MenuPage{Title: e.i18n.T(MsgMenuCustomTitle), Buttons: rows}
}

// buildCustomSubPage handles sub-pages of the custom panel.
// For P1: returns a placeholder page redirecting back.
func (e *Engine) buildCustomSubPage(sub string, sessionKey string) *MenuPage {
	_ = sub
	_ = sessionKey
	return e.buildCustomMenuPage()
}
```

- [ ] **Step 3.2: 在 `builtinCommands` 列表（`{[]string{"workspace", "ws"}, "workspace"}` 之后）追加 menu**

```go
	{[]string{"menu"}, "menu"},
```

- [ ] **Step 3.3: 在 `handleCommand` 的 switch 语句中添加 "menu" case**

找到 switch 中 `case "workspace":` 附近，在末尾 `default:` 之前添加：

```go
	case "menu":
		if mn, ok := p.(MenuNavigable); ok {
			page := e.handleMenuNavigation("menu:main", msg.SessionKey)
			if err := mn.SendMenuPage(e.ctx, msg.ReplyCtx, page); err != nil {
				slog.Warn("menu: failed to send menu page", "error", err)
			}
		} else {
			e.cmdHelp(p, msg)
		}
		return true
```

- [ ] **Step 3.4: 在 `Start()` 中注册 MenuNavigable（紧跟 CardNavigable 注册之后）**

找到：
```go
		if nav, ok := p.(CardNavigable); ok {
			nav.SetCardNavigationHandler(e.handleCardNav)
		}
```

在其后添加：
```go
		if mn, ok := p.(MenuNavigable); ok {
			mn.SetMenuNavigationHandler(e.handleMenuNavigation)
		}
```

- [ ] **Step 3.5: 验证 lang 选择使用语言代码（无需修改，已在 Step 3.1 内联正确版本）**

`buildListPage` 的 `case "lang"` 已直接使用语言代码作为 selAction（如 `menu:sel:lang:zh`）。`executeCardAction("/lang", "zh", sessionKey)` 已有对应处理。`menu:sel:` 的通用路由 `cmd=lang, idxStr=zh` → `executeCardAction("/lang", "zh", sessionKey)` 正确工作。

无需额外修改，此步骤为确认验证：

```bash
grep "menu:sel:lang" core/engine.go  # 应不存在硬编码数字索引
```

- [ ] **Step 3.6: 构建验证**

```bash
go build ./...
```

Expected: 无错误（Telegram 的 MenuNavigable 未实现，但 interface 满足是运行时的，编译不报错）

- [ ] **Step 3.7: Commit**

```bash
git add core/engine.go core/i18n.go
git commit -m "feat(core): implement handleMenuNavigation and menu command"
```

---

### Task 4: 为 core/engine.go handleMenuNavigation 写单元测试

**Files:**
- Create: `core/menu_test.go`

- [ ] **Step 4.1: 创建测试文件**

```go
package core

import (
	"context"
	"strings"
	"testing"
)

// stubMenuAgent implements ModelSwitcher and ModeSwitcher for testing.
type stubMenuAgent struct {
	model string
	mode  string
}

func (s *stubMenuAgent) Name() string { return "stub" }
func (s *stubMenuAgent) StartSession(_ context.Context, _ string) (AgentSession, error) {
	return nil, nil
}
func (s *stubMenuAgent) ListSessions(_ context.Context) ([]AgentSessionInfo, error) {
	return nil, nil
}
func (s *stubMenuAgent) Stop() error { return nil }

// ModelSwitcher
func (s *stubMenuAgent) GetModel() string { return s.model }
func (s *stubMenuAgent) SetModel(m string) { s.model = m }
func (s *stubMenuAgent) AvailableModels(_ context.Context) []ModelOption {
	return []ModelOption{{Name: "model-a"}, {Name: "model-b"}, {Name: "model-c"}}
}

// ModeSwitcher
func (s *stubMenuAgent) GetMode() string { return s.mode }
func (s *stubMenuAgent) SetMode(m string) { s.mode = m }
func (s *stubMenuAgent) PermissionModes() []PermissionModeInfo {
	return []PermissionModeInfo{
		{Key: "default", Name: "Default"},
		{Key: "yolo", Name: "YOLO"},
	}
}

func newMenuTestEngine(t *testing.T) *Engine {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	agent := &stubMenuAgent{model: "model-a", mode: "default"}
	e := &Engine{
		name:     "test",
		agent:    agent,
		sessions: NewSessionManager(""),
		i18n:     NewI18n(LangEnglish),
		ctx:      ctx,
	}
	e.interactiveStates = make(map[string]*interactiveState)
	return e
}

func TestHandleMenuNavigation_Main(t *testing.T) {
	e := newMenuTestEngine(t)
	page := e.handleMenuNavigation("menu:main", "telegram:123:456")
	if page == nil {
		t.Fatal("expected non-nil MenuPage for menu:main")
	}
	if page.Title == "" {
		t.Error("expected non-empty Title")
	}
	// Expect 6 rows: 2 rows of 2 category buttons + 1 advanced + 1 custom
	if len(page.Buttons) == 0 {
		t.Error("expected buttons in main menu")
	}
}

func TestHandleMenuNavigation_Category(t *testing.T) {
	e := newMenuTestEngine(t)
	for _, cat := range []string{"session", "ai", "task", "system", "advanced"} {
		page := e.handleMenuNavigation("menu:cat:"+cat, "telegram:123:456")
		if page == nil {
			t.Fatalf("expected non-nil page for category %q", cat)
		}
		// Last button row should be back button
		rows := page.Buttons
		if len(rows) == 0 {
			t.Fatalf("category %q: expected buttons", cat)
		}
		lastRow := rows[len(rows)-1]
		if lastRow[0].Data != "menu:main" {
			t.Errorf("category %q: last button should be back, got %q", cat, lastRow[0].Data)
		}
	}
}

func TestHandleMenuNavigation_ListModel(t *testing.T) {
	e := newMenuTestEngine(t)
	page := e.handleMenuNavigation("menu:list:model:0", "telegram:123:456")
	if page == nil {
		t.Fatal("expected non-nil page for model list")
	}
	// 3 models fit on 1 page; expect 3 item rows + 1 navigation row
	if len(page.Buttons) != 4 {
		t.Errorf("expected 4 button rows (3 items + nav), got %d", len(page.Buttons))
	}
	// First item should have ✅ since model-a is current
	if len(page.Buttons[0]) == 0 {
		t.Fatal("first row empty")
	}
	if !strings.Contains(page.Buttons[0][0].Text, "✅") {
		t.Error("expected ✅ on current model")
	}
}

func TestHandleMenuNavigation_Noop(t *testing.T) {
	e := newMenuTestEngine(t)
	page := e.handleMenuNavigation("menu:noop", "telegram:123:456")
	if page != nil {
		t.Error("expected nil page for menu:noop")
	}
}

func TestHandleMenuNavigation_SelModel(t *testing.T) {
	e := newMenuTestEngine(t)
	agent := e.agent.(*stubMenuAgent)
	// Select model-b (index 2)
	page := e.handleMenuNavigation("menu:sel:model:2", "telegram:123:456")
	if page == nil {
		t.Fatal("expected main menu page after selection")
	}
	if agent.model != "model-b" {
		t.Errorf("expected model-b after sel, got %q", agent.model)
	}
}
```

- [ ] **Step 4.2: 运行测试**

```bash
go test ./core/ -run TestHandleMenuNavigation -v
```

Expected:
```
--- PASS: TestHandleMenuNavigation_Main
--- PASS: TestHandleMenuNavigation_Category
--- PASS: TestHandleMenuNavigation_ListModel
--- PASS: TestHandleMenuNavigation_Noop
--- PASS: TestHandleMenuNavigation_SelModel
PASS
```

- [ ] **Step 4.3: 运行全部 core 测试**

```bash
go test ./core/ -v 2>&1 | tail -20
```

Expected: 所有测试通过

- [ ] **Step 4.4: Commit**

```bash
git add core/menu_test.go
git commit -m "test(core): add unit tests for handleMenuNavigation"
```

---

## Chunk 2: Telegram 平台集成

### Task 5: 创建 platform/telegram/menu.go

**Files:**
- Create: `platform/telegram/menu.go`

- [ ] **Step 5.1: 创建文件**

```go
package telegram

import (
	"fmt"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/chenhg5/cc-connect/core"
)

// menuPageToKeyboard converts a core.MenuPage's Buttons into a Telegram InlineKeyboardMarkup.
func menuPageToKeyboard(page *core.MenuPage) tgbotapi.InlineKeyboardMarkup {
	var rows [][]tgbotapi.InlineKeyboardButton
	for _, row := range page.Buttons {
		var btns []tgbotapi.InlineKeyboardButton
		for _, b := range row {
			data := b.Data
			if len(data) > 64 {
				data = data[:64]
			}
			btns = append(btns, tgbotapi.NewInlineKeyboardButtonData(b.Text, data))
		}
		rows = append(rows, btns)
	}
	return tgbotapi.NewInlineKeyboardMarkup(rows...)
}

// menuMessageText formats a MenuPage's title and subtitle into a single HTML string.
func menuMessageText(page *core.MenuPage) string {
	if page.Subtitle == "" {
		return page.Title
	}
	return page.Title + "\n" + "<i>" + escapeHTML(page.Subtitle) + "</i>"
}

// escapeHTML escapes Telegram HTML special characters in s.
func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// formatMenuTitle builds the full menu message text, using HTML mode.
// Title may contain <b> tags; subtitle is escaped.
func formatMenuTitle(title, subtitle string) string {
	if subtitle == "" {
		return title
	}
	return fmt.Sprintf("%s\n<i>%s</i>", title, escapeHTML(subtitle))
}
```

- [ ] **Step 5.2: 构建验证**

```bash
go build ./platform/telegram/...
```

Expected: 无错误

- [ ] **Step 5.3: 创建 platform/telegram/menu_test.go 并验证渲染逻辑**

```go
package telegram

import (
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestMenuPageToKeyboard_RowsAndData(t *testing.T) {
	page := &core.MenuPage{
		Title: "Test Menu",
		Buttons: [][]core.ButtonOption{
			{{Text: "A", Data: "menu:cat:session"}, {Text: "B", Data: "menu:cat:ai"}},
			{{Text: "Back", Data: "menu:main"}},
		},
	}
	kb := menuPageToKeyboard(page)
	if len(kb.InlineKeyboard) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(kb.InlineKeyboard))
	}
	if len(kb.InlineKeyboard[0]) != 2 {
		t.Errorf("expected 2 buttons in row 0, got %d", len(kb.InlineKeyboard[0]))
	}
	btn := kb.InlineKeyboard[0][0]
	if btn.Text != "A" {
		t.Errorf("expected Text=A, got %q", btn.Text)
	}
	if btn.CallbackData == nil || *btn.CallbackData != "menu:cat:session" {
		t.Errorf("expected CallbackData=menu:cat:session")
	}
}

func TestMenuPageToKeyboard_TruncatesLongData(t *testing.T) {
	longData := "menu:sel:session:" + strings.Repeat("x", 50) // > 64 bytes
	page := &core.MenuPage{
		Buttons: [][]core.ButtonOption{{{Text: "X", Data: longData}}},
	}
	kb := menuPageToKeyboard(page)
	data := *kb.InlineKeyboard[0][0].CallbackData
	if len(data) > 64 {
		t.Errorf("callback data should be truncated to 64 bytes, got %d", len(data))
	}
}

func TestMenuMessageText_WithSubtitle(t *testing.T) {
	page := &core.MenuPage{Title: "<b>Title</b>", Subtitle: "sub & info"}
	text := menuMessageText(page)
	if !strings.Contains(text, "<b>Title</b>") {
		t.Error("title should be preserved")
	}
	if !strings.Contains(text, "&amp;") {
		t.Error("subtitle & should be escaped")
	}
}
```

- [ ] **Step 5.4: 运行测试**

```bash
go test ./platform/telegram/ -run TestMenuPage -v
```

Expected: 所有测试通过

- [ ] **Step 5.5: Commit**

```bash
git add platform/telegram/menu.go platform/telegram/menu_test.go
git commit -m "feat(telegram): add menu rendering helpers"
```

---

### Task 6: 在 telegram.go 集成 MenuNavigable

**Files:**
- Modify: `platform/telegram/telegram.go`

- [ ] **Step 6.1: 在 Platform 结构体中添加新字段**

找到 `type Platform struct {` 的定义，添加三个字段：

```go
	menuMsgIDs   sync.Map           // key: int64(chatID) → int(messageID)
	menuHandler  core.MenuNavigationHandler
	pendingInputs sync.Map          // key: int64(chatID) → pendingInputState
```

同时在文件顶部 import 中确认已有 `"sync"`（已有则无需修改）。

添加辅助结构体（在 `replyContext` 定义附近）：

```go
// pendingInputState records a one-shot user input request (e.g. editing a command description).
type pendingInputState struct {
	kind    string    // e.g. "cmd_desc_edit:menu"
	expires time.Time // auto-expire after 60 seconds
}
```

- [ ] **Step 6.2: 实现 SetMenuNavigationHandler 和 SendMenuPage 方法**

> **注意**：`MenuNavigable` 接口（Task 1 已定义）包含两个方法：`SetMenuNavigationHandler` 和 `SendMenuPage`。Telegram Platform 须同时实现二者才满足该接口。

在 `telegram.go` 末尾（`RegisterCommands` 函数之后）添加：

```go
// SetMenuNavigationHandler implements core.MenuNavigable.
func (p *Platform) SetMenuNavigationHandler(h core.MenuNavigationHandler) {
	p.menuHandler = h
}

// SendMenuPage implements core.MenuNavigable.
// On first call for a chat, sends a new message and remembers its ID.
// On subsequent callback-triggered calls, edits the existing message.
func (p *Platform) SendMenuPage(ctx context.Context, rctx any, page *core.MenuPage) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("telegram: SendMenuPage: unexpected replyCtx type %T", rctx)
	}
	chatID := rc.chatID
	text := menuMessageText(page)
	keyboard := menuPageToKeyboard(page)

	// Try to edit existing menu message
	if rawID, ok := p.menuMsgIDs.Load(chatID); ok {
		msgID := rawID.(int)
		edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
		edit.ParseMode = tgbotapi.ModeHTML
		edit.ReplyMarkup = &keyboard
		if _, err := p.bot.Send(edit); err != nil {
			// If edit fails (message deleted), fall through to send new
			if !strings.Contains(err.Error(), "message to edit not found") &&
				!strings.Contains(err.Error(), "message is not modified") {
				slog.Warn("telegram: menu edit failed, sending new", "error", err)
			}
			p.menuMsgIDs.Delete(chatID)
		} else {
			return nil
		}
	}

	// Send new message
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = tgbotapi.ModeHTML
	msg.ReplyMarkup = keyboard
	sent, err := p.bot.Send(msg)
	if err != nil {
		// Fallback: plain text
		msg.Text = page.Title
		if page.Subtitle != "" {
			msg.Text += "\n" + page.Subtitle
		}
		msg.ParseMode = ""
		sent, err = p.bot.Send(msg)
	}
	if err != nil {
		return fmt.Errorf("telegram: SendMenuPage send: %w", err)
	}
	p.menuMsgIDs.Store(chatID, sent.MessageID)
	return nil
}
```

- [ ] **Step 6.3: 在 handleCallbackQuery 中添加 menu: 前缀处理**

找到 `handleCallbackQuery` 函数中的 `// Command callbacks (cmd:/lang en, ...)` 注释之前，添加 menu: 处理块。

> **重要**：插入点必须在 `answerCallbackQuery` 调用（`answer := tgbotapi.NewCallback(cb.ID, ""); p.bot.Request(answer)` 这两行）**之后**，确保 `answerCallbackQuery` 先执行清除加载动画，再进入 `menu:` 处理。当前文件中 `answerCallbackQuery` 在 line 302-303，`// Command callbacks` 在 line 322，中间插入即可。

```go
	// Menu callbacks (menu:main, menu:cat:ai, menu:list:model:0, etc.)
	if strings.HasPrefix(data, "menu:") {
		p.handleMenuCallback(data, chatID, msgID, userID, userName, chatName, sessionKey)
		return
	}
```

- [ ] **Step 6.4: 实现 handleMenuCallback**

在 `SendMenuPage` 之后添加：

```go
// handleMenuCallback processes menu: prefixed callback data.
func (p *Platform) handleMenuCallback(data string, chatID int64, msgID int, userID, userName, chatName, sessionKey string) {
	rctx := replyContext{chatID: chatID, messageID: 0} // messageID=0: replies go as new messages

	// menu:exec:{cmd} — build synthetic message and dispatch to engine
	if strings.HasPrefix(data, "menu:exec:") {
		cmd := "/" + strings.TrimPrefix(data, "menu:exec:")
		p.handler(p, &core.Message{
			SessionKey: sessionKey,
			Platform:   "telegram",
			UserID:     userID,
			UserName:   userName,
			ChatName:   chatName,
			Content:    cmd,
			MessageID:  strconv.Itoa(msgID),
			ReplyCtx:   rctx,
		})
		// After executing, refresh the main menu
		p.refreshMenuPage(chatID, "menu:main", sessionKey)
		return
	}

	// All other menu: actions (navigation + selection) go through the engine handler
	if p.menuHandler == nil {
		return
	}
	page := p.menuHandler(data, sessionKey)
	if page == nil {
		return // menu:noop — do nothing
	}
	menuRctx := replyContext{chatID: chatID, messageID: msgID}
	if err := p.SendMenuPage(context.Background(), menuRctx, page); err != nil {
		slog.Warn("telegram: menu update failed", "error", err)
	}
}

// refreshMenuPage calls the menu handler for the given action and updates the menu message.
func (p *Platform) refreshMenuPage(chatID int64, action, sessionKey string) {
	if p.menuHandler == nil {
		return
	}
	page := p.menuHandler(action, sessionKey)
	if page == nil {
		return
	}
	rctx := replyContext{chatID: chatID, messageID: 0}
	if rawID, ok := p.menuMsgIDs.Load(chatID); ok {
		rctx.messageID = rawID.(int)
	}
	if err := p.SendMenuPage(context.Background(), rctx, page); err != nil {
		slog.Warn("telegram: menu refresh failed", "error", err)
	}
}
```

- [ ] **Step 6.5: 添加 handleMenuCallback 路由单元测试**

在 `platform/telegram/menu_test.go` 末尾追加以下测试（测试路由逻辑，无需真实 bot）：

```go
// --- handleMenuCallback routing tests ---

func TestHandleMenuCallback_NilHandler(t *testing.T) {
	p := &Platform{} // menuHandler is nil
	// Should not panic
	p.handleMenuCallback("menu:cat:ai", 123, 0, "u1", "user", "chat", "sk")
}

func TestHandleMenuCallback_Noop(t *testing.T) {
	var called bool
	p := &Platform{}
	p.menuHandler = func(action, sessionKey string) *core.MenuPage {
		called = true
		return nil // noop: no SendMenuPage call
	}
	p.handleMenuCallback("menu:noop", 123, 0, "u1", "user", "chat", "sk")
	if !called {
		t.Error("expected menuHandler to be called for menu:noop")
	}
	// No panic = SendMenuPage was not called (nil page triggers early return)
}

func TestHandleMenuCallback_Exec_DispatchesCommand(t *testing.T) {
	var got *core.Message
	p := &Platform{}
	p.handler = func(_ core.Platform, msg *core.Message) {
		got = msg
	}
	// menuHandler returns nil for refreshMenuPage (noop after exec)
	p.menuHandler = func(action, _ string) *core.MenuPage { return nil }
	p.handleMenuCallback("menu:exec:stop", 123, 456, "u1", "user", "chat", "sk")
	if got == nil {
		t.Fatal("expected handler to be called with synthetic message")
	}
	if got.Content != "/stop" {
		t.Errorf("expected Content=/stop, got %q", got.Content)
	}
	if got.SessionKey != "sk" {
		t.Errorf("expected SessionKey=sk, got %q", got.SessionKey)
	}
}
```

> **索引说明**：`executeCardAction` 使用 1-based 数字索引（`models[idx-1]`），与 `menu:sel:model:%d` 的 `i+1` 编号一致，不需要调整。

- [ ] **Step 6.6: 运行测试（含新增路由测试）**

```bash
go test ./platform/telegram/ -run TestHandleMenuCallback -v
```

Expected: 3 个测试全部通过

- [ ] **Step 6.7: 构建验证**

```bash
go build ./platform/telegram/...
```

Expected: 无错误

- [ ] **Step 6.8: 运行全量测试**

```bash
go test ./...
```

Expected: 所有测试通过

- [ ] **Step 6.9: Commit**

```bash
git add platform/telegram/telegram.go platform/telegram/menu_test.go
git commit -m "feat(telegram): integrate MenuNavigable — menuMsgIDs, handleMenuCallback, SendMenuPage"
```

---

### Task 7: 更新 Telegram 命令列表（中文描述 + /menu 置顶）

**Files:**
- Modify: `core/engine.go`
- Modify: `core/i18n.go`

- [ ] **Step 7.1: 修改 GetAllCommands 使 /menu 排第一**

找到 `func (e *Engine) GetAllCommands()` 函数，在函数开头（遍历 `builtinCommands` 之前）prepend menu 命令：

```go
func (e *Engine) GetAllCommands() []BotCommandInfo {
	var commands []BotCommandInfo
	seenCmds := make(map[string]bool)

	// /menu always comes first
	commands = append(commands, BotCommandInfo{
		Command:     "menu",
		Description: e.i18n.T(MsgBuiltinCmdMenu),
	})
	seenCmds["menu"] = true

	// ... rest of existing code unchanged
```

- [ ] **Step 7.2: 添加 MsgBuiltinCmdMenu 常量**

在 `core/i18n.go` 中 `MsgBuiltinCmdShell` 之后添加：

```go
	MsgBuiltinCmdMenu      MsgKey = "menu"
```

在 messages map 中找到 `"new"` key 附近，添加 "menu" 的翻译：

```go
	"menu": {
		LangEnglish:            "Open control panel",
		LangChinese:            "打开控制面板",
		LangTraditionalChinese: "開啟控制面板",
		LangJapanese:           "コントロールパネルを開く",
		LangSpanish:            "Abrir panel de control",
	},
```

同时更新现有的几个核心命令中文描述（在 messages map 中找到对应 key 并添加 `LangChinese`）：

```go
	"new":     {LangEnglish: "Start a new session",    LangChinese: "新建会话"},
	"list":    {LangEnglish: "List all sessions",      LangChinese: "查看会话列表"},
	"stop":    {LangEnglish: "Stop current execution", LangChinese: "停止当前任务"},
	"status":  {LangEnglish: "Show current status",    LangChinese: "查看当前状态"},
	"help":    {LangEnglish: "Show help",              LangChinese: "帮助说明"},
```

（注：只需确认这些 key 有 `LangChinese` 值，已有则跳过）

- [ ] **Step 7.3: 构建并运行测试**

```bash
go build ./... && go test ./...
```

Expected: 所有通过

- [ ] **Step 7.4: Commit**

```bash
git add core/engine.go core/i18n.go
git commit -m "feat(core): put /menu first in command list with Chinese descriptions"
```

---

## Chunk 3: P1 自定义面板与 JSON 持久化

### Task 8: 创建 platform/telegram/menu_config.go

**Files:**
- Create: `platform/telegram/menu_config.go`
- Create: `platform/telegram/menu_config_test.go`

- [ ] **Step 8.1: 创建 menu_config.go**

```go
package telegram

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// MenuConfig holds per-chat customization for the /menu panel.
type MenuConfig struct {
	mu              sync.RWMutex
	chatID          int64
	dataDir         string

	Pinned          []string          `json:"pinned,omitempty"`           // pinned command names (max 4)
	HiddenCats      []string          `json:"hidden_cats,omitempty"`      // hidden category keys
	CustomCmds      []string          `json:"custom_cmds,omitempty"`      // custom commands to show
	CmdDescriptions map[string]string `json:"cmd_descriptions,omitempty"` // command → description overrides
}

// menuConfigPath returns the JSON file path for a given chatID.
func menuConfigPath(dataDir string, chatID int64) string {
	return filepath.Join(dataDir, fmt.Sprintf("menu_config_%d.json", chatID))
}

// LoadMenuConfig loads (or creates empty) config for the given chatID.
func LoadMenuConfig(chatID int64, dataDir string) *MenuConfig {
	cfg := &MenuConfig{chatID: chatID, dataDir: dataDir}
	path := menuConfigPath(dataDir, chatID)
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg // file not found = default config
	}
	_ = json.Unmarshal(data, cfg)
	return cfg
}

// Save writes the config to disk.
func (c *MenuConfig) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := os.MkdirAll(c.dataDir, 0700); err != nil {
		return fmt.Errorf("menu_config: mkdir: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("menu_config: marshal: %w", err)
	}
	path := menuConfigPath(c.dataDir, c.chatID)
	return os.WriteFile(path, data, 0600)
}

// IsCatHidden returns true if the category key should be hidden.
func (c *MenuConfig) IsCatHidden(key string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, h := range c.HiddenCats {
		if h == key {
			return true
		}
	}
	return false
}

// ToggleHiddenCat toggles visibility of a category. "session" is always visible.
// Returns the new hidden state.
func (c *MenuConfig) ToggleHiddenCat(key string) bool {
	if key == "session" {
		return false // session cannot be hidden
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, h := range c.HiddenCats {
		if h == key {
			c.HiddenCats = append(c.HiddenCats[:i], c.HiddenCats[i+1:]...)
			return false // now visible
		}
	}
	c.HiddenCats = append(c.HiddenCats, key)
	return true // now hidden
}

// TogglePinned pins or unpins a command. Max 4 pinned commands.
// Returns (isPinned, error).
func (c *MenuConfig) TogglePinned(cmd string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, p := range c.Pinned {
		if p == cmd {
			c.Pinned = append(c.Pinned[:i], c.Pinned[i+1:]...)
			return false, nil
		}
	}
	if len(c.Pinned) >= 4 {
		return false, fmt.Errorf("max 4 pinned commands")
	}
	c.Pinned = append(c.Pinned, cmd)
	return true, nil
}

// SetCmdDescription overrides the Telegram command list description for cmd.
func (c *MenuConfig) SetCmdDescription(cmd, desc string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.CmdDescriptions == nil {
		c.CmdDescriptions = make(map[string]string)
	}
	c.CmdDescriptions[cmd] = desc
}

// Reset clears all customization.
func (c *MenuConfig) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Pinned = nil
	c.HiddenCats = nil
	c.CustomCmds = nil
	c.CmdDescriptions = nil
}
```

- [ ] **Step 8.2: 创建 menu_config_test.go**

```go
package telegram

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMenuConfig_PinUnpin(t *testing.T) {
	cfg := &MenuConfig{chatID: 1, dataDir: t.TempDir()}

	pinned, err := cfg.TogglePinned("new")
	if err != nil || !pinned {
		t.Fatalf("expected pinned=true err=nil, got pinned=%v err=%v", pinned, err)
	}
	if len(cfg.Pinned) != 1 || cfg.Pinned[0] != "new" {
		t.Error("expected Pinned=[new]")
	}

	pinned, err = cfg.TogglePinned("new")
	if err != nil || pinned {
		t.Fatalf("expected unpinned, got pinned=%v err=%v", pinned, err)
	}
	if len(cfg.Pinned) != 0 {
		t.Error("expected empty Pinned after unpin")
	}
}

func TestMenuConfig_PinMax4(t *testing.T) {
	cfg := &MenuConfig{chatID: 1, dataDir: t.TempDir()}
	for _, cmd := range []string{"a", "b", "c", "d"} {
		cfg.TogglePinned(cmd)
	}
	_, err := cfg.TogglePinned("e")
	if err == nil {
		t.Error("expected error when exceeding max 4 pinned commands")
	}
}

func TestMenuConfig_HideCat(t *testing.T) {
	cfg := &MenuConfig{chatID: 1, dataDir: t.TempDir()}

	hidden := cfg.ToggleHiddenCat("advanced")
	if !hidden {
		t.Error("expected hidden=true")
	}
	if !cfg.IsCatHidden("advanced") {
		t.Error("expected advanced to be hidden")
	}

	// session cannot be hidden
	hidden = cfg.ToggleHiddenCat("session")
	if hidden || cfg.IsCatHidden("session") {
		t.Error("session should never be hidden")
	}
}

func TestMenuConfig_SaveLoad(t *testing.T) {
	dir := t.TempDir()
	cfg := LoadMenuConfig(42, dir)
	cfg.TogglePinned("stop")
	cfg.ToggleHiddenCat("advanced")
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(filepath.Join(dir, "menu_config_42.json")); err != nil {
		t.Fatalf("config file not created: %v", err)
	}

	// Reload
	cfg2 := LoadMenuConfig(42, dir)
	if len(cfg2.Pinned) != 1 || cfg2.Pinned[0] != "stop" {
		t.Errorf("loaded Pinned mismatch: %v", cfg2.Pinned)
	}
	if !cfg2.IsCatHidden("advanced") {
		t.Error("loaded HiddenCats mismatch")
	}
}

func TestMenuConfig_Reset(t *testing.T) {
	cfg := LoadMenuConfig(1, t.TempDir())
	cfg.TogglePinned("new")
	cfg.ToggleHiddenCat("advanced")
	cfg.Reset()
	if len(cfg.Pinned) != 0 || len(cfg.HiddenCats) != 0 {
		t.Error("Reset should clear all config")
	}
}
```

- [ ] **Step 8.3: 运行测试**

```bash
go test ./platform/telegram/ -run TestMenuConfig -v
```

Expected: 全部通过

- [ ] **Step 8.4: Commit**

```bash
git add platform/telegram/menu_config.go platform/telegram/menu_config_test.go
git commit -m "feat(telegram): add MenuConfig JSON persistence"
```

---

### Task 9: 连接自定义面板到引擎与平台

**Files:**
- Modify: `platform/telegram/telegram.go`
- Modify: `core/engine.go`

- [ ] **Step 9.1: 在 Platform 结构体中添加 dataDir 和 menuConfigs 缓存**

在 Platform 结构体中（`menuHandler` 字段旁边）添加：

```go
	dataDir     string                // path for config file persistence
	menuConfigs sync.Map              // key: int64(chatID) → *MenuConfig
```

在 `New()` 函数中初始化：

```go
	dataDir, _ := opts["data_dir"].(string)
```

在 `return &Platform{...}` 中添加 `dataDir: dataDir,`

在 `config/config.go` 的 project 配置中，engine 启动 platform 时会传 `data_dir`（已有）。

- [ ] **Step 9.2: 添加 getMenuConfig 辅助方法**

```go
// getMenuConfig returns the cached MenuConfig for a chatID, loading from disk if needed.
func (p *Platform) getMenuConfig(chatID int64) *MenuConfig {
	if v, ok := p.menuConfigs.Load(chatID); ok {
		return v.(*MenuConfig)
	}
	cfg := LoadMenuConfig(chatID, p.dataDir)
	p.menuConfigs.Store(chatID, cfg)
	return cfg
}
```

- [ ] **Step 9.3: 在 handleMenuCallback 中处理 menu:custom:* 操作**

在 `handleMenuCallback` 中，在 `menu:exec:` 处理之后，添加：

```go
	// menu:custom:reset — confirm and reset
	if data == "menu:custom:reset" {
		cfg := p.getMenuConfig(chatID)
		cfg.Reset()
		_ = cfg.Save()
		p.refreshMenuPage(chatID, "menu:cat:custom", sessionKey)
		return
	}

	// menu:pin:toggle:{cmd}
	if strings.HasPrefix(data, "menu:pin:toggle:") {
		cmd := strings.TrimPrefix(data, "menu:pin:toggle:")
		cfg := p.getMenuConfig(chatID)
		cfg.TogglePinned(cmd)
		_ = cfg.Save()
		p.refreshMenuPage(chatID, "menu:custom:pin", sessionKey)
		return
	}

	// menu:hide:toggle:{cat}
	if strings.HasPrefix(data, "menu:hide:toggle:") {
		cat := strings.TrimPrefix(data, "menu:hide:toggle:")
		cfg := p.getMenuConfig(chatID)
		cfg.ToggleHiddenCat(cat)
		_ = cfg.Save()
		p.refreshMenuPage(chatID, "menu:custom:hide", sessionKey)
		return
	}
```

- [ ] **Step 9.4: 在 engine.go 的 buildCustomSubPage 实现 pin 和 hide 子页面**

将 `buildCustomSubPage` 中的 placeholder 替换为实际实现：

```go
func (e *Engine) buildCustomSubPage(sub string, sessionKey string) *MenuPage {
	switch sub {
	case "hide":
		// Show toggle buttons for each category (except session)
		var items []ButtonOption
		for _, cat := range menuCategories {
			if cat.key == "session" {
				continue // session cannot be hidden
			}
			label := "⬜ " + e.i18n.T(cat.labelKey)
			items = append(items, ButtonOption{
				Text: label,
				Data: "menu:hide:toggle:" + cat.key,
			})
		}
		rows := buildMenuButtons(items, "menu:cat:custom", e.i18n.T(MsgMenuBack))
		return &MenuPage{Title: e.i18n.T(MsgMenuCustomHide), Buttons: rows}

	case "pin":
		// Show toggle buttons for key commands
		pinnable := []struct{ cmd, label string }{
			{"new", "➕ " + e.i18n.T(MsgMenuCmdNew)},
			{"stop", "⏹ " + e.i18n.T(MsgMenuCmdStop)},
			{"status", "📊 " + e.i18n.T(MsgMenuCmdStatus)},
			{"model", "🧠 " + e.i18n.T(MsgMenuCmdModel)},
			{"mode", "🔒 " + e.i18n.T(MsgMenuCmdMode)},
			{"history", "📜 " + e.i18n.T(MsgMenuCmdHistory)},
			{"compress", "🗜️ " + e.i18n.T(MsgMenuCmdCompress)},
		}
		var items []ButtonOption
		for _, p := range pinnable {
			items = append(items, ButtonOption{
				Text: p.label,
				Data: "menu:pin:toggle:" + p.cmd,
			})
		}
		rows := buildMenuButtons(items, "menu:cat:custom", e.i18n.T(MsgMenuBack))
		return &MenuPage{Title: e.i18n.T(MsgMenuCustomPin), Buttons: rows}

	default:
		return e.buildCustomMenuPage()
	}
}
```

- [ ] **Step 9.5: 在平台层应用 MenuConfig 过滤（隐藏分类 + 固定快捷命令）**

由于 engine 不能引用 telegram 包，MenuConfig 过滤在平台层（`handleMenuCallback`）进行后处理。

在 `telegram.go` 中添加 `applyMenuConfig` 方法，同时处理：
1. 隐藏分类过滤（从主菜单移除被隐藏的分类按钮）
2. 固定快捷命令行（仅在主菜单顶部添加 pinned 命令行）

```go
// applyMenuConfig filters and augments menu page buttons based on user's MenuConfig.
// action is the callback data that produced this page (used to detect main menu).
func (p *Platform) applyMenuConfig(chatID int64, action string, page *core.MenuPage) *core.MenuPage {
	cfg := p.getMenuConfig(chatID)
	if len(cfg.HiddenCats) == 0 && len(cfg.Pinned) == 0 {
		return page // no customization, fast path
	}

	// Filter out hidden category buttons
	var filtered [][]core.ButtonOption
	for _, row := range page.Buttons {
		var newRow []core.ButtonOption
		for _, btn := range row {
			if strings.HasPrefix(btn.Data, "menu:cat:") {
				cat := strings.TrimPrefix(btn.Data, "menu:cat:")
				if cfg.IsCatHidden(cat) {
					continue
				}
			}
			newRow = append(newRow, btn)
		}
		if len(newRow) > 0 {
			filtered = append(filtered, newRow)
		}
	}

	// On main menu, prepend pinned shortcuts row
	if action == "menu:main" && len(cfg.Pinned) > 0 {
		var pinnedRow []core.ButtonOption
		for _, cmd := range cfg.Pinned {
			pinnedRow = append(pinnedRow, core.ButtonOption{
				Text: "📌 " + cmd,
				Data: "menu:exec:" + cmd,
			})
		}
		filtered = append([][]core.ButtonOption{pinnedRow}, filtered...)
	}

	return &core.MenuPage{Title: page.Title, Subtitle: page.Subtitle, Buttons: filtered}
}
```

在 `handleMenuCallback` 的非 `menu:exec:` 路径（Task 6 Step 6.4 中 `p.menuHandler` 调用之后），找到以下代码块并精确修改：

**原代码（Task 6 Step 6.4 末尾的 menuHandler 路径）：**
```go
	// All other menu: actions (navigation + selection) go through the engine handler
	if p.menuHandler == nil {
		return
	}
	page := p.menuHandler(data, sessionKey)
	if page == nil {
		return // menu:noop — do nothing
	}
	menuRctx := replyContext{chatID: chatID, messageID: msgID}
	if err := p.SendMenuPage(context.Background(), menuRctx, page); err != nil {
		slog.Warn("telegram: menu update failed", "error", err)
	}
```

**改为：**
```go
	// All other menu: actions (navigation + selection) go through the engine handler
	if p.menuHandler == nil {
		return
	}
	page := p.menuHandler(data, sessionKey)
	if page == nil {
		return // menu:noop — do nothing
	}
	page = p.applyMenuConfig(chatID, data, page) // apply hide/pin customization
	menuRctx := replyContext{chatID: chatID, messageID: msgID}
	if err := p.SendMenuPage(context.Background(), menuRctx, page); err != nil {
		slog.Warn("telegram: menu update failed", "error", err)
	}
```

- [ ] **Step 9.6: 构建并运行全量测试**

```bash
go build ./... && go test ./...
```

Expected: 所有测试通过

- [ ] **Step 9.7: Commit**

```bash
git add platform/telegram/telegram.go core/engine.go
git commit -m "feat(telegram): connect custom panel — pin/hide with JSON persistence"
```

---

## 验收测试步骤

运行完所有任务后，手动测试以下场景：

- [ ] 发送 `/menu` → 出现主菜单（5大类 + ⚙️自定义）
- [ ] 点击「🤖 AI设置」→ 子菜单原地更新
- [ ] 点击「🧠 切换模型」→ 模型列表（带翻页）
- [ ] 选择一个模型 → 主菜单刷新，副标题显示新模型名
- [ ] 点击「💬 会话 → 🔀 切换会话」→ 会话列表（带翻页）
- [ ] 点击「◀ 返回主菜单」→ 回到主菜单
- [ ] 进入「⚙️ 自定义 → 👁️ 显示/隐藏分类」→ 隐藏一个分类 → 主菜单不再显示该分类
- [ ] 重启 bot → 隐藏配置保留

```bash
# 最终全量测试
go test ./... -race
```

Expected: 全部通过，无 data race
