package feishu

import (
	"fmt"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// Keep command rows to one static Markdown component.
// The native quote supplies the prototype's left rule without layout columns.
func executionCommandPreview(b core.ExecutionBlock) string {
	command := strings.Join(strings.Fields(b.Command), " ")
	if command == "" {
		command = b.Tool
	}
	return executionClip(command, 160)
}

func executionInlineText(s string) string {
	return strings.NewReplacer("\\", "\\\\", "`", "ˋ", "<", "‹", ">", "›", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]").Replace(s)
}

func (c *queryCard) executionCommandLine(b core.ExecutionBlock) string {
	icon, color := "◷", "blue"
	if b.Done {
		icon, color = "✓", "green"
	}
	if b.Status == "failed" || b.Status == "error" {
		icon, color = "✗", "red"
	}
	if b.Status == "interrupted" {
		icon, color = "⏹", "grey"
	}
	meta := strings.TrimPrefix(c.executionStatus(b), icon+" ")
	command := strings.NewReplacer("`", "ˋ", "<", "‹", ">", "›").Replace(executionCommandPreview(b))
	return fmt.Sprintf("> <font color='%s'>%s</font> **%s** — `%s`\n> <font color='grey'>%s</font>",
		color, icon, executionInlineText(b.Tool), command, meta)
}

func (c *queryCard) executionDetailText() string {
	b := c.view.Snapshot
	if b.Tool == "" {
		return executionBlockText(b)
	}
	i := c.p.cardI18n()
	output := b.Output
	if output == "" {
		output = i.T(core.MsgExecNoOutput)
	}
	return i.T(core.MsgExecCommandLabel) + "\n" + b.Command + "\n\n" + i.T(core.MsgExecOutputLabel) + "\n" + output
}

func executionCaption(text, color string) map[string]any {
	return map[string]any{"tag": "markdown", "content": "<font color='" + color + "'>" + text + "</font>", "text_size": "notation"}
}

func executionDecisionPanel(elements []map[string]any) map[string]any {
	return map[string]any{
		"tag": "column_set", "element_id": "execution_decision", "flex_mode": "none", "background_style": "orange-50",
		"columns": []map[string]any{{"tag": "column", "width": "weighted", "weight": 1, "padding": "10px", "vertical_spacing": "8px", "elements": executionActionRows(elements)}},
	}
}

// Place controls on one wrapping row. Large/long labels wrap on narrow clients;
// command entries remain static full-width rows.
func executionActionRows(elements []map[string]any) []map[string]any {
	var out []map[string]any
	for n := 0; n < len(elements); {
		if elements[n]["tag"] != "button" {
			out = append(out, elements[n])
			n++
			continue
		}
		var columns []map[string]any
		for n < len(elements) && elements[n]["tag"] == "button" {
			columns = append(columns, map[string]any{"tag": "column", "width": "auto", "elements": []map[string]any{elements[n]}})
			n++
		}
		if len(columns) == 1 {
			out = append(out, columns[0]["elements"].([]map[string]any)[0])
		} else {
			out = append(out, map[string]any{"tag": "column_set", "flex_mode": "flow", "horizontal_spacing": "8px", "columns": columns})
		}
	}
	return out
}

// All controls nested in the question form must submit the draft, including
// buttons now placed in columns. Only question_submit actually answers it.
func (c *queryCard) executionDraftControls(elements []map[string]any, next *int) {
	for n, e := range elements {
		if e["tag"] == "interactive_container" {
			behavior := e["behaviors"].([]map[string]any)
			action := behavior[0]["value"].(map[string]string)["action"]
			parts := strings.Split(action, ":")
			label := c.p.cardI18n().T(core.MsgExecFullExplanation)
			if len(parts) == 5 {
				b, ok := c.records[parts[3]]
				if !ok {
					for _, block := range c.blocks {
						if block.Key == parts[3] {
							b = block
							break
						}
					}
				}
				if b.Tool != "" {
					label = c.executionStatus(b) + " · " + executionCommandPreview(b)
				}
			}
			e = map[string]any{"tag": "button", "text": plainText(label), "type": "primary_text", "width": "fill", "behaviors": behavior}
			elements[n] = e
		}
		if e["tag"] == "button" {
			e["form_action_type"] = "submit"
			e["name"] = fmt.Sprintf("exec_draft_%d", *next)
			*next++
		}
		for _, key := range []string{"elements", "columns"} {
			if nested, ok := e[key].([]map[string]any); ok {
				c.executionDraftControls(nested, next)
			}
		}
	}
}
