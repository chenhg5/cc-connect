package discord

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"
	"github.com/chenhg5/cc-connect/core"
)

const (
	maxDiscordEmbedTitleLen     = 256
	maxDiscordProgressDescLen   = 3500
	maxDiscordProgressLineLen   = 220
	maxDiscordProgressFooterLen = 512
)

func buildDiscordPreviewMessage(content string) *discordgo.MessageSend {
	if payload, ok := core.ParseProgressCardPayload(content); ok {
		return &discordgo.MessageSend{
			Embeds: []*discordgo.MessageEmbed{buildDiscordProgressEmbed(payload)},
		}
	}
	return &discordgo.MessageSend{
		Content: trimDiscordRunes(content, maxDiscordLen),
	}
}

func buildDiscordPreviewEdit(channelID, messageID, content string) *discordgo.MessageEdit {
	edit := discordgo.NewMessageEdit(channelID, messageID)
	if payload, ok := core.ParseProgressCardPayload(content); ok {
		edit.SetContent("")
		edit.SetEmbeds([]*discordgo.MessageEmbed{buildDiscordProgressEmbed(payload)})
		return edit
	}
	edit.SetContent(trimDiscordRunes(content, maxDiscordLen))
	edit.SetEmbeds([]*discordgo.MessageEmbed{})
	return edit
}

func buildDiscordProgressEmbed(payload *core.ProgressCardPayload) *discordgo.MessageEmbed {
	if payload == nil {
		return &discordgo.MessageEmbed{Description: " "}
	}
	agent := discordProgressAgentLabel(payload.Agent)
	title, color, footer := discordProgressStateMeta(payload.State, payload.Lang, agent)
	embed := &discordgo.MessageEmbed{
		Title:       trimDiscordRunes(title, maxDiscordEmbedTitleLen),
		Description: buildDiscordProgressDescription(payload),
		Color:       color,
	}
	if footer = strings.TrimSpace(footer); footer != "" {
		embed.Footer = &discordgo.MessageEmbedFooter{
			Text: trimDiscordRunes(footer, maxDiscordProgressFooterLen),
		}
	}
	return embed
}

func buildDiscordProgressDescription(payload *core.ProgressCardPayload) string {
	if payload == nil {
		return " "
	}
	items := discordProgressItems(payload)
	lines := make([]string, 0, len(items)+1)
	if payload.Truncated {
		lines = append(lines, "ℹ️ "+discordProgressLatestOnlyText(payload.Lang))
	}
	for _, item := range items {
		if line := buildDiscordProgressLine(item, payload.Lang); line != "" {
			lines = append(lines, line)
		}
	}
	desc := strings.Join(lines, "\n")
	desc = trimDiscordRunes(desc, maxDiscordProgressDescLen)
	if strings.TrimSpace(desc) == "" {
		return " "
	}
	return desc
}

func discordProgressItems(payload *core.ProgressCardPayload) []core.ProgressCardEntry {
	if payload == nil {
		return nil
	}
	if len(payload.Items) > 0 {
		return payload.Items
	}
	out := make([]core.ProgressCardEntry, 0, len(payload.Entries))
	for _, entry := range payload.Entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		out = append(out, core.ProgressCardEntry{
			Kind: core.ProgressEntryInfo,
			Text: entry,
		})
	}
	return out
}

func buildDiscordProgressLine(item core.ProgressCardEntry, lang string) string {
	text := compactDiscordProgressText(item.Text)
	switch item.Kind {
	case core.ProgressEntryThinking:
		return "💭 " + trimDiscordRunes(text, maxDiscordProgressLineLen-2)
	case core.ProgressEntryToolUse:
		return "🔧 " + trimDiscordRunes(buildDiscordToolUseSummary(item, lang), maxDiscordProgressLineLen-2)
	case core.ProgressEntryToolResult:
		return "🧾 " + trimDiscordRunes(buildDiscordToolResultSummary(item, lang), maxDiscordProgressLineLen-2)
	case core.ProgressEntryError:
		return "❌ " + trimDiscordRunes(text, maxDiscordProgressLineLen-2)
	default:
		return "• " + trimDiscordRunes(text, maxDiscordProgressLineLen-2)
	}
}

func buildDiscordToolUseSummary(item core.ProgressCardEntry, lang string) string {
	toolName := strings.TrimSpace(item.Tool)
	if toolName == "" {
		toolName = discordProgressToolLabel(lang)
	}
	text := compactDiscordProgressText(item.Text)
	if text == "" {
		return toolName
	}
	return toolName + " — " + text
}

func buildDiscordToolResultSummary(item core.ProgressCardEntry, lang string) string {
	toolName := strings.TrimSpace(item.Tool)
	text := compactDiscordProgressText(item.Text)
	meta := make([]string, 0, 3)

	if status := discordProgressStatusText(item, lang); status != "" {
		meta = append(meta, status)
	}
	if item.ExitCode != nil {
		meta = append(meta, fmt.Sprintf("exit %d", *item.ExitCode))
	}
	if text != "" {
		duplicate := false
		for _, part := range meta {
			if strings.EqualFold(part, text) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			meta = append(meta, text)
		}
	}
	if len(meta) == 0 {
		meta = append(meta, discordProgressNoOutputText(lang))
	}
	if toolName == "" {
		return strings.Join(meta, " · ")
	}
	return toolName + " — " + strings.Join(meta, " · ")
}

func discordProgressStateMeta(state core.ProgressCardState, lang string, agent string) (title string, color int, footer string) {
	switch state {
	case core.ProgressCardStateCompleted:
		return fmt.Sprintf("%s · %s", agent, discordProgressCompletedText(lang)), 0x57F287, discordProgressCompletedFooter(lang)
	case core.ProgressCardStateFailed:
		return fmt.Sprintf("%s · %s", agent, discordProgressFailedText(lang)), 0xED4245, discordProgressFailedFooter(lang)
	default:
		return fmt.Sprintf("%s · %s", agent, discordProgressRunningText(lang)), 0x5865F2, ""
	}
}

func discordProgressAgentLabel(agent string) string {
	agent = strings.TrimSpace(agent)
	if agent == "" {
		return "Agent"
	}
	return agent
}

func discordProgressRunningText(lang string) string {
	switch normalizeDiscordProgressLang(lang) {
	case "zh":
		return "处理中"
	case "zh-tw":
		return "處理中"
	case "ja":
		return "処理中"
	case "es":
		return "Procesando"
	default:
		return "Processing"
	}
}

func discordProgressCompletedText(lang string) string {
	switch normalizeDiscordProgressLang(lang) {
	case "zh":
		return "已完成"
	case "zh-tw":
		return "已完成"
	case "ja":
		return "完了"
	case "es":
		return "Completado"
	default:
		return "Completed"
	}
}

func discordProgressFailedText(lang string) string {
	switch normalizeDiscordProgressLang(lang) {
	case "zh":
		return "失败"
	case "zh-tw":
		return "失敗"
	case "ja":
		return "失敗"
	case "es":
		return "Fallido"
	default:
		return "Failed"
	}
}

func discordProgressCompletedFooter(lang string) string {
	switch normalizeDiscordProgressLang(lang) {
	case "zh":
		return "本进度卡已停止更新，完整答复见下一条消息。"
	case "zh-tw":
		return "本進度卡已停止更新，完整答覆見下一條訊息。"
	case "ja":
		return "この進捗カードの更新は終了しました。完全な回答は次のメッセージにあります。"
	case "es":
		return "Esta tarjeta de progreso ya no se actualiza. La respuesta completa está en el siguiente mensaje."
	default:
		return "This progress card is no longer updating. Full response is in the next message."
	}
}

func discordProgressFailedFooter(lang string) string {
	switch normalizeDiscordProgressLang(lang) {
	case "zh":
		return "本进度卡已停止更新（失败），请查看下一条消息中的错误详情。"
	case "zh-tw":
		return "本進度卡已停止更新（失敗），請查看下一條訊息中的錯誤詳情。"
	case "ja":
		return "この進捗カードは失敗で停止しました。詳細は次のメッセージを確認してください。"
	case "es":
		return "Esta tarjeta de progreso se detuvo por error. Consulta el siguiente mensaje para ver los detalles."
	default:
		return "This progress card has stopped (failed). See the next message for details."
	}
}

func discordProgressLatestOnlyText(lang string) string {
	switch normalizeDiscordProgressLang(lang) {
	case "zh":
		return "仅显示最近更新。"
	case "zh-tw":
		return "僅顯示最近更新。"
	case "ja":
		return "最新の更新のみ表示しています。"
	case "es":
		return "Mostrando solo las actualizaciones más recientes."
	default:
		return "Showing latest updates only."
	}
}

func discordProgressNoOutputText(lang string) string {
	switch normalizeDiscordProgressLang(lang) {
	case "zh":
		return "无输出"
	case "zh-tw":
		return "無輸出"
	case "ja":
		return "出力なし"
	case "es":
		return "Sin salida"
	default:
		return "No output"
	}
}

func discordProgressToolLabel(lang string) string {
	switch normalizeDiscordProgressLang(lang) {
	case "zh":
		return "工具"
	case "zh-tw":
		return "工具"
	case "ja":
		return "ツール"
	case "es":
		return "Herramienta"
	default:
		return "Tool"
	}
}

func discordProgressOKText(lang string) string {
	switch normalizeDiscordProgressLang(lang) {
	case "zh":
		return "成功"
	case "zh-tw":
		return "成功"
	case "ja":
		return "成功"
	case "es":
		return "ok"
	default:
		return "ok"
	}
}

func discordProgressStatusText(item core.ProgressCardEntry, lang string) string {
	status := strings.ToLower(strings.TrimSpace(item.Status))
	switch status {
	case "completed", "complete", "success", "succeeded", "ok":
		if status == "completed" || status == "complete" {
			return strings.ToLower(discordProgressCompletedText(lang))
		}
		return discordProgressOKText(lang)
	case "failed", "failure", "error":
		return strings.ToLower(discordProgressFailedText(lang))
	}
	if item.Success != nil {
		if *item.Success {
			return discordProgressOKText(lang)
		}
		return strings.ToLower(discordProgressFailedText(lang))
	}
	return strings.TrimSpace(item.Status)
}

func normalizeDiscordProgressLang(lang string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	switch {
	case l == "zh-tw" || l == "zh-hk":
		return "zh-tw"
	case strings.HasPrefix(l, "zh"):
		return "zh"
	case strings.HasPrefix(l, "ja"):
		return "ja"
	case strings.HasPrefix(l, "es"):
		return "es"
	default:
		return "en"
	}
}

func compactDiscordProgressText(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.Join(strings.Fields(s), " ")
}

func trimDiscordRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	rs := []rune(s)
	if maxRunes == 1 {
		return string(rs[:1])
	}
	return string(rs[:maxRunes-1]) + "…"
}
