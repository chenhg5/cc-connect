package acp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// elicitationOption is one selectable choice parsed from an elicitation
// form schema. Value is the enum `const` that must round-trip back to the
// agent verbatim; Label is the human-facing title the engine renders and
// echoes back in answers. For kimi acp the two are identical, but the ACP
// schema allows them to differ, so they are tracked separately.
type elicitationOption struct {
	Value       string
	Label       string
	Description string
}

// elicitationQuestion pairs a parsed question with its property key in the
// elicitation form schema (e.g. "q0"), which the response content map is
// keyed by.
type elicitationQuestion struct {
	PropertyKey string
	Question    core.UserQuestion
	Options     []elicitationOption
}

// elicitationState is cached per pending elicitation/create request so
// RespondPermission can translate engine answers back into the
// CreateElicitationResponse shape the agent expects.
type elicitationState struct {
	Questions []elicitationQuestion
}

// elicitationEnumOption matches the ACP EnumOption shape used by oneOf /
// items.anyOf entries in a form-mode requestedSchema.
type elicitationEnumOption struct {
	Const       string `json:"const"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type elicitationProperty struct {
	Type        string                  `json:"type"`
	Title       string                  `json:"title"`
	Description string                  `json:"description"`
	OneOf       []elicitationEnumOption `json:"oneOf"`
	Items       *struct {
		AnyOf []elicitationEnumOption `json:"anyOf"`
	} `json:"items"`
}

// parseElicitationCreateParams parses elicitation/create form-mode params
// into ordered questions. Single-select questions arrive as
// type:"string"+oneOf; multi-select as type:"array"+items.anyOf (kimi acp
// also sets minItems:1, making every question required). Property order is
// taken from requestedSchema.required (kimi emits q0, q1, ... in question
// order); without it, keys are sorted for determinism.
//
// Properties that carry no enum options (free-text string/number/boolean
// fields) are skipped: cc-connect's ask-question wizard only renders
// option lists.
func parseElicitationCreateParams(params json.RawMessage) (*elicitationState, error) {
	var p struct {
		Mode    string `json:"mode"`
		Message string `json:"message"`
		Schema  struct {
			Properties map[string]elicitationProperty `json:"properties"`
			Required   []string                       `json:"required"`
		} `json:"requestedSchema"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("acp: parse elicitation/create params: %w", err)
	}
	if p.Mode != "" && p.Mode != "form" {
		return nil, fmt.Errorf("acp: unsupported elicitation mode %q", p.Mode)
	}
	if len(p.Schema.Properties) == 0 {
		return nil, fmt.Errorf("acp: elicitation/create with empty requestedSchema.properties")
	}

	order := elicitationPropertyOrder(p.Schema.Properties, p.Schema.Required)
	st := &elicitationState{}
	for _, key := range order {
		prop := p.Schema.Properties[key]
		q, ok := elicitationPropertyToQuestion(prop)
		if !ok {
			continue
		}
		st.Questions = append(st.Questions, elicitationQuestion{
			PropertyKey: key,
			Question:    q,
			Options:     elicitationEnumOptions(prop),
		})
	}
	if len(st.Questions) == 0 {
		return nil, fmt.Errorf("acp: elicitation/create schema has no enumerable option fields")
	}
	return st, nil
}

// elicitationPropertyOrder returns property keys in `required` order,
// appending any non-required keys (sorted) afterwards.
func elicitationPropertyOrder(props map[string]elicitationProperty, required []string) []string {
	seen := make(map[string]bool, len(required))
	order := make([]string, 0, len(props))
	for _, key := range required {
		if _, ok := props[key]; ok && !seen[key] {
			seen[key] = true
			order = append(order, key)
		}
	}
	var rest []string
	for key := range props {
		if !seen[key] {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	return append(order, rest...)
}

func elicitationPropertyToQuestion(prop elicitationProperty) (core.UserQuestion, bool) {
	q := core.UserQuestion{Question: prop.Title}
	switch prop.Type {
	case "string":
		if len(prop.OneOf) == 0 {
			return core.UserQuestion{}, false
		}
	case "array":
		if prop.Items == nil || len(prop.Items.AnyOf) == 0 {
			return core.UserQuestion{}, false
		}
		q.MultiSelect = true
	default:
		return core.UserQuestion{}, false
	}
	for _, opt := range elicitationEnumOptions(prop) {
		q.Options = append(q.Options, core.UserQuestionOption{
			Label:       opt.Label,
			Description: opt.Description,
		})
	}
	if q.Question == "" || len(q.Options) == 0 {
		return core.UserQuestion{}, false
	}
	return q, true
}

func elicitationEnumOptions(prop elicitationProperty) []elicitationOption {
	raw := prop.OneOf
	if prop.Type == "array" && prop.Items != nil {
		raw = prop.Items.AnyOf
	}
	opts := make([]elicitationOption, 0, len(raw))
	for _, o := range raw {
		label := o.Title
		if label == "" {
			label = o.Const
		}
		opts = append(opts, elicitationOption{
			Value:       o.Const,
			Label:       label,
			Description: o.Description,
		})
	}
	return opts
}

// buildElicitationResponse translates an engine PermissionResult into an
// ACP CreateElicitationResponse. The engine reports answers as
// UpdatedInput["answers"], a map keyed by question text with string values
// (multi-select labels joined with ", " in declared order). Here they are
// re-keyed to the form's property keys and re-split into arrays for
// multi-select questions, with each label mapped back to its enum const.
//
// Anything other than an allow with at least one matched answer resolves
// to {"action":"decline"}, which kimi acp maps to the tool's canonical
// "user dismissed" branch (decline and cancel are equivalent there).
func buildElicitationResponse(st *elicitationState, result core.PermissionResult) map[string]any {
	decline := map[string]any{"action": "decline"}
	if !strings.EqualFold(result.Behavior, "allow") || st == nil {
		return decline
	}
	answersRaw, ok := result.UpdatedInput["answers"]
	if !ok {
		return decline
	}
	answers, ok := answersRaw.(map[string]any)
	if !ok || len(answers) == 0 {
		return decline
	}

	content := make(map[string]any, len(st.Questions))
	for _, q := range st.Questions {
		ans, _ := answers[q.Question.Question].(string)
		ans = strings.TrimSpace(ans)
		if ans == "" {
			continue
		}
		if q.Question.MultiSelect {
			parts := strings.Split(ans, ", ")
			picked := make([]string, 0, len(parts))
			for _, opt := range q.Options {
				for _, p := range parts {
					if p == opt.Label {
						picked = append(picked, opt.Value)
						break
					}
				}
			}
			if len(picked) > 0 {
				content[q.PropertyKey] = picked
			}
			continue
		}
		value := ans
		for _, opt := range q.Options {
			if ans == opt.Label {
				value = opt.Value
				break
			}
		}
		content[q.PropertyKey] = value
	}
	if len(content) == 0 {
		return decline
	}
	return map[string]any{
		"action":  "accept",
		"content": content,
	}
}
