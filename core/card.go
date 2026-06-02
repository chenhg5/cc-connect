package core

import (
	"fmt"
	"strings"
)

// Card represents a structured rich message that can be rendered as
// platform-specific cards (Feishu Interactive Card, Telegram message, etc.)
// or degraded to plain text for platforms without card support.
type Card struct {
	Header   *CardHeader
	Elements []CardElement
}

// CardHeader is the optional colored title bar of a card.
type CardHeader struct {
	Title string
	Color string // blue, green, red, orange, purple, grey, turquoise, violet, indigo, wathet, yellow, carmine
}

// CardElement is the interface satisfied by all card content elements.
type CardElement interface {
	cardElement()
}

// CardMarkdown renders markdown-formatted text.
type CardMarkdown struct{ Content string }

// CardDivider renders a horizontal rule.
type CardDivider struct{}

// CardActions renders a row of clickable buttons.
type CardActions struct {
	Buttons []CardButton
	Layout  CardActionLayout
}

// CardNote renders small footnote text at the bottom.
// Tag is an optional machine-readable identifier (not displayed) used by
// platform renderers to recognize and handle specific notes programmatically.
type CardNote struct {
	Text string
	Tag  string
}

// CardListItem renders a row with description text on the left and a button on the right.
// On Feishu this maps to div+extra; on other platforms it degrades to a text line.
type CardListItem struct {
	Text     string            // left-side description
	BtnText  string            // button label
	BtnType  string            // "primary", "default", "danger"
	BtnValue string            // callback data
	Extra    map[string]string // additional key-value pairs carried in the callback
}

// CardSelect renders a dropdown selector.
// On Feishu this maps to select_static; on other platforms it degrades to text.
type CardSelect struct {
	Placeholder string
	Options     []CardSelectOption
	InitValue   string // pre-selected option value (empty = none)
}

// CardSelectOption is one item in a CardSelect dropdown.
type CardSelectOption struct {
	Text  string
	Value string
}

// CardAskQuestionMultiSelect renders a multi-select form for AskUserQuestion.
// On Feishu this maps to a form with checker elements; on other platforms it
// degrades to numbered text instructions.
type CardAskQuestionMultiSelect struct {
	Question     string              // the question text
	Options      []CardSelectOption  // available options
	QuestionIdx  int                 // question index (for callback routing)
	SubmitText   string              // submit button text
	CancelText   string              // cancel button text (optional)
}

func (CardMarkdown) cardElement()                {}
func (CardDivider) cardElement()                 {}
func (CardActions) cardElement()                 {}
func (CardNote) cardElement()                    {}
func (CardListItem) cardElement()                {}
func (CardSelect) cardElement()                  {}
func (CardAskQuestionMultiSelect) cardElement()  {}

// CardButton represents a clickable button inside a CardActions element.
type CardButton struct {
	Text  string            // display label
	Type  string            // "primary", "default", "danger"
	Value string            // callback data, e.g. "cmd:/new", "cmd:/switch 3"
	Extra map[string]string // additional key-value pairs carried in the callback (platform-specific)
}

// CardActionLayout controls how a CardActions row should be rendered by
// platforms with richer layout capabilities.
type CardActionLayout string

const (
	CardActionLayoutRow          CardActionLayout = "row"
	CardActionLayoutEqualColumns CardActionLayout = "equal_columns"
)

// Btn is a shorthand constructor for CardButton.
func Btn(text, typ, value string) CardButton {
	return CardButton{Text: text, Type: typ, Value: value}
}

// PrimaryBtn creates a primary-styled button.
func PrimaryBtn(text, value string) CardButton {
	return CardButton{Text: text, Type: "primary", Value: value}
}

// DefaultBtn creates a default-styled button.
func DefaultBtn(text, value string) CardButton {
	return CardButton{Text: text, Type: "default", Value: value}
}

// DangerBtn creates a danger-styled button.
func DangerBtn(text, value string) CardButton {
	return CardButton{Text: text, Type: "danger", Value: value}
}

// --- Builder API ---

// CardBuilder provides a fluent API for constructing Card instances.
type CardBuilder struct {
	card Card
}

// NewCard returns a new CardBuilder.
func NewCard() *CardBuilder {
	return &CardBuilder{}
}

// Title sets the card header with a title and color.
func (b *CardBuilder) Title(title, color string) *CardBuilder {
	b.card.Header = &CardHeader{Title: title, Color: color}
	return b
}

// Markdown appends a markdown text element.
func (b *CardBuilder) Markdown(content string) *CardBuilder {
	if content != "" {
		b.card.Elements = append(b.card.Elements, CardMarkdown{Content: content})
	}
	return b
}

// Markdownf appends a formatted markdown text element.
func (b *CardBuilder) Markdownf(format string, args ...any) *CardBuilder {
	return b.Markdown(fmt.Sprintf(format, args...))
}

// Divider appends a horizontal divider.
func (b *CardBuilder) Divider() *CardBuilder {
	b.card.Elements = append(b.card.Elements, CardDivider{})
	return b
}

// Buttons appends an action row with the given buttons.
func (b *CardBuilder) Buttons(buttons ...CardButton) *CardBuilder {
	if len(buttons) > 0 {
		b.card.Elements = append(b.card.Elements, CardActions{Buttons: buttons, Layout: CardActionLayoutRow})
	}
	return b
}

// ButtonsEqual appends an action row where each button should take equal width
// on platforms that support richer layouts.
func (b *CardBuilder) ButtonsEqual(buttons ...CardButton) *CardBuilder {
	if len(buttons) > 0 {
		b.card.Elements = append(b.card.Elements, CardActions{Buttons: buttons, Layout: CardActionLayoutEqualColumns})
	}
	return b
}

// ListItem appends a list row: description on the left, button on the right.
func (b *CardBuilder) ListItem(desc, btnText, btnValue string) *CardBuilder {
	b.card.Elements = append(b.card.Elements, CardListItem{
		Text: desc, BtnText: btnText, BtnType: "default", BtnValue: btnValue,
	})
	return b
}

// ListItemBtn is like ListItem but allows specifying the button type.
func (b *CardBuilder) ListItemBtn(desc, btnText, btnType, btnValue string) *CardBuilder {
	b.card.Elements = append(b.card.Elements, CardListItem{
		Text: desc, BtnText: btnText, BtnType: btnType, BtnValue: btnValue,
	})
	return b
}

// ListItemBtnExtra is like ListItemBtn but with extra callback data.
func (b *CardBuilder) ListItemBtnExtra(desc, btnText, btnType, btnValue string, extra map[string]string) *CardBuilder {
	b.card.Elements = append(b.card.Elements, CardListItem{
		Text: desc, BtnText: btnText, BtnType: btnType, BtnValue: btnValue, Extra: extra,
	})
	return b
}

// Select appends a dropdown selector element.
func (b *CardBuilder) Select(placeholder string, options []CardSelectOption, initValue string) *CardBuilder {
	if len(options) > 0 {
		b.card.Elements = append(b.card.Elements, CardSelect{
			Placeholder: placeholder, Options: options, InitValue: initValue,
		})
	}
	return b
}

// AskQuestionMultiSelect appends a multi-select form element for AskUserQuestion.
// This is rendered as a form with checkboxes on Feishu, and as numbered text on other platforms.
func (b *CardBuilder) AskQuestionMultiSelect(question string, options []CardSelectOption, qIdx int, submitText, cancelText string) *CardBuilder {
	if len(options) > 0 {
		b.card.Elements = append(b.card.Elements, CardAskQuestionMultiSelect{
			Question:     question,
			Options:      options,
			QuestionIdx:  qIdx,
			SubmitText:   submitText,
			CancelText:   cancelText,
		})
	}
	return b
}

// Note appends a footnote element.
func (b *CardBuilder) Note(text string) *CardBuilder {
	if text != "" {
		b.card.Elements = append(b.card.Elements, CardNote{Text: text})
	}
	return b
}

func (b *CardBuilder) TaggedNote(tag, text string) *CardBuilder {
	if text != "" {
		b.card.Elements = append(b.card.Elements, CardNote{Text: text, Tag: tag})
	}
	return b
}

// Build returns the constructed Card.
func (b *CardBuilder) Build() *Card {
	c := b.card
	return &c
}

// --- Text Fallback ---

// RenderText converts the card to a plain-text representation for platforms
// that do not support rich cards.
func (c *Card) RenderText() string {
	var sb strings.Builder

	if c.Header != nil && c.Header.Title != "" {
		sb.WriteString("**")
		sb.WriteString(c.Header.Title)
		sb.WriteString("**\n\n")
	}

	for _, elem := range c.Elements {
		switch e := elem.(type) {
		case CardMarkdown:
			sb.WriteString(e.Content)
			sb.WriteString("\n\n")
		case CardDivider:
			sb.WriteString("---\n\n")
		case CardActions:
			// Render buttons as a hint line
			for i, btn := range e.Buttons {
				if i > 0 {
					sb.WriteString("  ")
				}
				sb.WriteString("[")
				sb.WriteString(btn.Text)
				sb.WriteString("]")
			}
			sb.WriteString("\n\n")
		case CardListItem:
			sb.WriteString(e.Text)
			sb.WriteString("  [")
			sb.WriteString(e.BtnText)
			sb.WriteString("]\n")
		case CardSelect:
			sb.WriteString(e.Placeholder)
			sb.WriteString(": ")
			for i, opt := range e.Options {
				if i > 0 {
					sb.WriteString(" | ")
				}
				sb.WriteString(opt.Text)
			}
			sb.WriteString("\n\n")
		case CardAskQuestionMultiSelect:
			sb.WriteString(e.Question)
			sb.WriteString("\n")
			for i, opt := range e.Options {
				sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, opt.Text))
			}
			sb.WriteString("\n")
		case CardNote:
			sb.WriteString(e.Text)
			sb.WriteString("\n")
		}
	}

	return strings.TrimRight(sb.String(), "\n")
}

// HasButtons returns true if the card contains any interactive elements.
func (c *Card) HasButtons() bool {
	for _, elem := range c.Elements {
		switch elem.(type) {
		case CardActions, CardListItem, CardSelect, CardAskQuestionMultiSelect:
			return true
		}
	}
	return false
}

// CollectButtons extracts all buttons from the card as a 2D slice
// (one inner slice per CardActions element), suitable for InlineButtonSender.
// CardListItem elements are collected as single-button rows.
func (c *Card) CollectButtons() [][]ButtonOption {
	var rows [][]ButtonOption
	for _, elem := range c.Elements {
		switch e := elem.(type) {
		case CardActions:
			var row []ButtonOption
			for _, btn := range e.Buttons {
				row = append(row, ButtonOption{Text: btn.Text, Data: btn.Value})
			}
			if len(row) > 0 {
				rows = append(rows, row)
			}
		case CardListItem:
			rows = append(rows, []ButtonOption{{Text: e.BtnText, Data: e.BtnValue}})
		}
	}
	return rows
}
