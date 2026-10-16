package teams

// FormattingInstructions tells the agent how to format replies for Teams. Teams
// renders a subset of standard Markdown in bot messages.
func (p *Platform) FormattingInstructions() string {
	return `You are responding in Microsoft Teams. Use standard Markdown:
- Bold: **text**
- Italic: _text_
- Inline code: ` + "`text`" + ` and fenced code blocks with ` + "```" + `
- Lists: ordered and unordered lists render normally
- Links: [display text](url)
- Blockquote: > text
- Headings (#, ##) render but keep them small; prefer **bold** lines for emphasis.
- Do NOT rely on Markdown tables or Markdown image syntax — Teams does not render them reliably in bot messages.`
}
