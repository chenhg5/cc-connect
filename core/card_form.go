package core

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"
)

// FormCardSender is optional: ordinary card support does not imply that a
// platform can securely collect and dispatch structured form submissions.
type FormCardSender interface {
	ReplyForm(context.Context, *Message, *Card) error
}

type CardForm struct {
	ID         string // opaque platform-issued submission reference
	Command    string // server-owned management command, without interpolated values
	Language   Language
	SubmitText string
	BackAction string // navigation outside the form; never submits required fields
	Fields     []CardFormField
}

type CardFormField struct {
	Name, Label, Placeholder string
	Kind                     string // text, multiline, select, multiselect, display
	Required                 bool
	Options                  []CardSelectOption
	Defaults                 []string
	Actions                  []CardFormAction // draft updates shown after this field
}

// Draft actions collect the whole form without requiring completed fields.
// Their server-owned commands may navigate/update the form, never create it.
type CardFormAction struct {
	ID, Text, Command string
}

func (f *CardForm) HasDraftActions() bool {
	for _, field := range f.Fields {
		if len(field.Actions) > 0 {
			return true
		}
	}
	return false
}

func (f *CardForm) ValidateDraft(raw map[string]any) (map[string][]string, error) {
	copy := *f
	copy.Fields = append([]CardFormField(nil), f.Fields...)
	for i := range copy.Fields {
		copy.Fields[i].Required = false
	}
	return copy.ValidateValues(raw)
}

// ValidateValues checks untrusted platform callback values against the issued
// form. It returns only declared fields and preserves text without shell parsing.
func (f *CardForm) ValidateValues(raw map[string]any) (map[string][]string, error) {
	out := make(map[string][]string, len(f.Fields))
	i18n := NewI18n(f.Language)
	for _, field := range f.Fields {
		if field.Kind == "display" {
			continue
		}
		invalid := func() error { return fmt.Errorf("%s", i18n.Tf(MsgFormInvalidField, field.Label)) }
		var values []string
		value, exists := raw[field.Name]
		if !exists {
			values = append([]string(nil), field.Defaults...)
		} else if value == nil && field.Kind == "select" {
			// An explicitly empty selection must not restore its old default.
		} else if field.Kind == "multiselect" {
			switch v := value.(type) {
			case []string:
				values = append([]string(nil), v...)
			case []any:
				for _, item := range v {
					s, ok := item.(string)
					if !ok {
						return nil, invalid()
					}
					values = append(values, s)
				}
			case nil:
			default:
				return nil, invalid()
			}
		} else {
			s, ok := value.(string)
			if !ok {
				return nil, invalid()
			}
			values = []string{s}
		}
		var normalized []string
		seen := map[string]bool{}
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			if utf8.RuneCountInString(value) > 1000 || strings.ContainsRune(value, '\x00') {
				return nil, invalid()
			}
			if field.Kind == "select" || field.Kind == "multiselect" {
				valid := false
				for _, option := range field.Options {
					if value == option.Value {
						valid = true
						break
					}
				}
				if !valid {
					return nil, invalid()
				}
			}
			if !seen[value] {
				normalized = append(normalized, value)
				seen[value] = true
			}
		}
		if field.Required && len(normalized) == 0 {
			return nil, invalid()
		}
		if field.Kind != "multiselect" && len(normalized) > 1 {
			return nil, invalid()
		}
		out[field.Name] = normalized
	}
	return out, nil
}
