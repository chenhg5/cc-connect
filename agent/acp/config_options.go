package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ACP session configuration is a complete snapshot, not a patch. Unknown
// categories/types remain available in the snapshot but are not exposed as
// model, thought-level or permission-mode controls.
type configChoice struct {
	Value       string         `json:"value"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Options     []configChoice `json:"options,omitempty"`
}
type configOption struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Category     string         `json:"category"`
	Type         string         `json:"type"`
	CurrentValue string         `json:"currentValue"`
	Options      []configChoice `json:"options"`
}
type modelsBlock struct {
	CurrentModelID  string `json:"currentModelId"`
	AvailableModels []struct {
		ModelID     string `json:"modelId"`
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"availableModels"`
}
type sessionConfigResult struct {
	SessionID     string          `json:"sessionId"`
	Modes         *acpModesBlock  `json:"modes"`
	Models        *modelsBlock    `json:"models"`
	ConfigOptions json.RawMessage `json:"configOptions"`
}

func flattenChoices(choices []configChoice) []configChoice {
	var out []configChoice
	for _, choice := range choices {
		if len(choice.Options) > 0 {
			out = append(out, flattenChoices(choice.Options)...)
		} else if choice.Value != "" {
			out = append(out, choice)
		}
	}
	return out
}

func (s *acpSession) absorbConfigOptions(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	// Decode individually: a future/unknown option type must not prevent known
	// select options from working (e.g. a boolean currentValue).
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return fmt.Errorf("acp: parse configOptions: %w", err)
	}
	options := make([]configOption, 0, len(entries))
	for _, entry := range entries {
		var option configOption
		if json.Unmarshal(entry, &option) != nil || option.Type != "select" {
			continue
		}
		option.Options = flattenChoices(option.Options)
		options = append(options, option)
	}
	s.configMu.Lock()
	s.configOptions = options
	s.configOptionsKnown = true
	s.configMu.Unlock()
	return nil
}
func (s *acpSession) absorbSessionConfig(result sessionConfigResult) error {
	s.absorbModes(result.Modes)
	s.configMu.Lock()
	s.legacyModels = result.Models
	s.configMu.Unlock()
	return s.absorbConfigOptions(result.ConfigOptions)
}
func (s *acpSession) option(category string) (configOption, bool) {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	for _, o := range s.configOptions {
		if o.Category == category {
			return o, true
		}
	}
	if s.configOptionsKnown {
		return configOption{}, false
	}
	if category == "model" && s.legacyModels != nil {
		o := configOption{Category: category, CurrentValue: s.legacyModels.CurrentModelID, Type: "select"}
		for _, m := range s.legacyModels.AvailableModels {
			o.Options = append(o.Options, configChoice{Value: m.ModelID, Name: m.Name, Description: m.Description})
		}
		return o, true
	}
	if category == "mode" {
		s.modesMu.RLock()
		defer s.modesMu.RUnlock()
		if len(s.availableModes) > 0 {
			o := configOption{Category: category, CurrentValue: s.currentMode, Type: "select"}
			for _, m := range s.availableModes {
				o.Options = append(o.Options, configChoice{Value: m.ID, Name: m.Name, Description: m.Description})
			}
			return o, true
		}
	}
	return configOption{}, false
}
func (s *acpSession) setConfig(ctx context.Context, category, value string) error {
	if !s.Alive() {
		return fmt.Errorf("acp: session is closed")
	}
	option, ok := s.option(category)
	if !ok {
		return fmt.Errorf("acp: %s is not advertised by this agent", category)
	}
	valid := false
	for _, choice := range option.Options {
		if choice.Value == value {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("acp: invalid %s selection %q", category, value)
	}
	if option.CurrentValue == value {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	params := map[string]any{"sessionId": s.currentACPSessionID()}
	method := "session/set_config_option"
	if option.ID != "" {
		params["configId"] = option.ID
		params["value"] = value
	} else if category == "model" {
		method = "session/set_model"
		params["modelId"] = value
	} else {
		method = "session/set_mode"
		params["modeId"] = value
	}
	raw, err := s.tr.call(ctx, method, params)
	if err != nil {
		return fmt.Errorf("acp: %s: %w", method, err)
	}
	if option.ID != "" {
		var result sessionConfigResult
		if err := json.Unmarshal(raw, &result); err != nil {
			return fmt.Errorf("acp: parse %s result: %w", method, err)
		}
		if len(result.ConfigOptions) == 0 {
			return fmt.Errorf("acp: %s returned no configOptions", method)
		}
		if err := s.absorbConfigOptions(result.ConfigOptions); err != nil {
			return err
		}
		updated, present := s.option(category)
		if !present || updated.CurrentValue != value {
			return fmt.Errorf("acp: %s selection was not applied", category)
		}
	} else if category == "model" {
		s.configMu.Lock()
		s.legacyModels.CurrentModelID = value
		s.configMu.Unlock()
	} else {
		s.modesMu.Lock()
		s.currentMode = value
		s.modesMu.Unlock()
	}
	return nil
}
func (s *acpSession) absorbConfigUpdate(params json.RawMessage) {
	var wrap struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			Kind          string          `json:"sessionUpdate"`
			ConfigOptions json.RawMessage `json:"configOptions"`
		} `json:"update"`
	}
	if json.Unmarshal(params, &wrap) != nil || wrap.Update.Kind != "config_option_update" {
		return
	}
	if wrap.SessionID != "" && wrap.SessionID != s.currentACPSessionID() {
		return
	}
	if err := s.absorbConfigOptions(wrap.Update.ConfigOptions); err != nil {
		return
	}
}

func (s *acpSession) applyPreferences(ctx context.Context, model, effort string) error {
	for _, pref := range []struct{ category, value string }{{"model", model}, {"thought_level", effort}} {
		if strings.TrimSpace(pref.value) == "" {
			continue
		}
		if err := s.setConfig(ctx, pref.category, pref.value); err != nil {
			return err
		}
	}
	return nil
}
