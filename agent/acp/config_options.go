package acp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// errEmptyACPSessionID is returned by absorbSessionResult when session/new
// omits sessionId. session/load may legally omit it; the caller decides.
var errEmptyACPSessionID = errors.New("empty sessionId")

// acpConfigOption is one entry of the ACP configOptions array.
type acpConfigOption struct {
	ID           string                  `json:"id"`
	Name         string                  `json:"name"`
	Description  string                  `json:"description,omitempty"`
	Category     string                  `json:"category,omitempty"`
	Type         string                  `json:"type,omitempty"`
	CurrentValue flexString              `json:"currentValue,omitempty"`
	Options      []acpConfigOptionChoice `json:"options,omitempty"`
}

type acpConfigOptionChoice struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// acpLegacyModel is one entry of the unstable ACP models.availableModels list.
type acpLegacyModel struct {
	ModelID string `json:"modelId"`
	Name    string `json:"name"`
}

// acpModelsBlock is the older unstable ACP `models` object.
type acpModelsBlock struct {
	CurrentModelID  string           `json:"currentModelId"`
	AvailableModels []acpLegacyModel `json:"availableModels"`
}

// acpNewSessionResult is the shared session/new and session/load payload.
// session/load often omits sessionId; session/new must not.
type acpNewSessionResult struct {
	SessionID     string            `json:"sessionId"`
	Modes         *acpModesBlock    `json:"modes"`
	ConfigOptions []acpConfigOption `json:"configOptions"`
	Models        *acpModelsBlock   `json:"models"`
}

// flexString accepts a JSON string, number, or bool so a non-string
// currentValue does not fail the whole configOptions decode.
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		*f = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexString(s)
		return nil
	}
	*f = flexString(b)
	return nil
}

func (f flexString) String() string { return strings.TrimSpace(string(f)) }

func (s *acpSession) absorbSessionResult(raw json.RawMessage, requireID bool) error {
	var sn acpNewSessionResult
	if err := json.Unmarshal(raw, &sn); err != nil {
		return err
	}
	if sn.SessionID == "" && requireID {
		return errEmptyACPSessionID
	}
	if sn.SessionID != "" {
		s.setACPSessionID(sn.SessionID)
	}
	s.absorbModes(sn.Modes)
	s.absorbModelsBlock(sn.Models)
	s.absorbConfigOptions(sn.ConfigOptions)
	return nil
}

func (s *acpSession) absorbConfigOptions(opts []acpConfigOption) {
	if len(opts) == 0 {
		return
	}
	copied := append([]acpConfigOption(nil), opts...)
	s.configMu.Lock()
	s.configOptions = copied
	if opt, ok := findModelConfigOption(copied); ok && opt.CurrentValue.String() != "" {
		s.reportedModel = opt.CurrentValue.String()
	}
	s.configMu.Unlock()
	s.publishModelConfig(copied)
	s.publishThoughtLevel(copied)
	s.publishModeDescriptions(copied)
}

func (s *acpSession) publishModelConfig(opts []acpConfigOption) {
	if s.callbacks == nil {
		return
	}
	opt, ok := findModelConfigOption(opts)
	if !ok {
		return
	}
	s.callbacks.reportModelOptions(opt.CurrentValue.String(), modelOptionsFromConfig(opt))
}

func (s *acpSession) absorbModelsBlock(block *acpModelsBlock) {
	if block == nil {
		return
	}
	cp := *block
	cp.AvailableModels = append([]acpLegacyModel(nil), block.AvailableModels...)
	s.configMu.Lock()
	s.legacyModels = &cp
	_, hasOpt := findModelConfigOption(s.configOptions)
	if !hasOpt && strings.TrimSpace(block.CurrentModelID) != "" && s.reportedModel == "" {
		s.reportedModel = strings.TrimSpace(block.CurrentModelID)
	}
	s.configMu.Unlock()
	if hasOpt || s.callbacks == nil {
		return
	}
	if opts := modelOptionsFromLegacy(&cp); len(opts) > 0 {
		s.callbacks.reportModelOptions(strings.TrimSpace(block.CurrentModelID), opts)
	}
}

// GetModel returns the model id most recently reported by the agent
// (configOptions currentValue, or the legacy currentModelId).
func (s *acpSession) GetModel() string {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.reportedModel
}

func (s *acpSession) setReportedModel(model string) {
	s.configMu.Lock()
	s.reportedModel = strings.TrimSpace(model)
	s.configMu.Unlock()
}

func (s *acpSession) publishThoughtLevel(opts []acpConfigOption) {
	if s.callbacks == nil {
		return
	}
	opt, ok := findConfigOption(opts, "thought_level")
	if !ok {
		return
	}
	s.callbacks.reportThoughtLevel(opt.CurrentValue.String(), configChoiceValues(opt))
}

func (s *acpSession) publishModeDescriptions(opts []acpConfigOption) {
	if s.callbacks == nil {
		return
	}
	descs := modeDescriptionsFromConfig(opts)
	if len(descs) == 0 {
		return
	}
	s.callbacks.reportModeDescriptions(descs)
}

// applyStartupPreferences applies mode, then model, then thought_level.
// A failed model or effort change is shown to the user and does not
// fail the session.
func (s *acpSession) applyStartupPreferences(mode, model, effort string) {
	if strings.TrimSpace(mode) != "" {
		if ok := s.SetLiveMode(mode); !ok {
			slog.Warn("acp: initial mode could not be applied",
				"mode", mode,
				"session_id", s.currentACPSessionID(),
			)
		}
	}
	implied := ""
	if strings.TrimSpace(model) != "" {
		implied = s.applyInitialModel(model)
	}
	effective := strings.TrimSpace(effort)
	if effective == "" {
		effective = implied
	}
	s.applyThoughtLevel(effective)
}

func (s *acpSession) applyInitialModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" || s.tr == nil {
		return ""
	}
	sid := s.currentACPSessionID()
	if sid == "" {
		return ""
	}
	opt, hasOpt, legacy, legacyCurrent := s.snapshotModelControls()
	if hasOpt {
		return s.applyModelViaConfigOption(sid, model, opt)
	}
	if legacy {
		s.applyModelViaSetModel(sid, model, legacyCurrent)
		return ""
	}
	s.addStartupWarning(fmt.Sprintf("model %q was not applied: agent advertised no model option", model))
	slog.Warn("acp: model not applied, agent advertised neither configOptions nor models",
		"model", model, "session_id", sid)
	return ""
}

func (s *acpSession) snapshotModelControls() (acpConfigOption, bool, bool, string) {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	opt, hasOpt := findModelConfigOption(s.configOptions)
	var legacy bool
	var legacyCurrent string
	if s.legacyModels != nil && (s.legacyModels.CurrentModelID != "" || len(s.legacyModels.AvailableModels) > 0) {
		legacy = true
		legacyCurrent = strings.TrimSpace(s.legacyModels.CurrentModelID)
	}
	return opt, hasOpt, legacy, legacyCurrent
}

func (s *acpSession) applyModelViaConfigOption(sid, model string, opt acpConfigOption) string {
	values := configChoiceValues(opt)
	value, implied := s.resolveModelValue(model, values)
	if value == "" {
		s.addStartupWarning(fmt.Sprintf("model %q is not an advertised option and was not applied", model))
		slog.Warn("acp: model not applied", "model", model, "session_id", sid, "allowed", strings.Join(values, ","))
		return ""
	}
	if opt.CurrentValue.String() == value {
		s.setReportedModel(value)
		return implied
	}
	if err := s.setConfigOption(sid, opt.ID, value); err != nil {
		s.addStartupWarning(fmt.Sprintf("model %q was not applied: %s", value, err.Error()))
		slog.Warn("acp: session/set_config_option failed",
			"config_id", opt.ID, "model", value, "session_id", sid, "error", err)
		return ""
	}
	slog.Info("acp: model applied", "model", value, "session_id", sid, "config_id", opt.ID)
	return implied
}

// resolveModelValue returns an option value the agent accepts.
// An exact configured id skips the resolver. impliedEffort is set only
// when the resolver produced that value.
func (s *acpSession) resolveModelValue(model string, values []string) (string, string) {
	if exactConfigChoice(values, model) {
		return model, ""
	}
	if s.modelResolver == nil {
		return "", ""
	}
	value, implied := s.modelResolver(model, values)
	value = strings.TrimSpace(value)
	implied = strings.TrimSpace(implied)
	if !exactConfigChoice(values, value) {
		return "", ""
	}
	return value, implied
}

func (s *acpSession) applyThoughtLevel(effort string) {
	effort = strings.TrimSpace(effort)
	if effort == "" || s.tr == nil {
		return
	}
	sid := s.currentACPSessionID()
	if sid == "" {
		return
	}
	opt, ok := s.thoughtLevelOption()
	if !ok {
		s.addStartupWarning(fmt.Sprintf("reasoning effort %q was not applied: agent did not advertise thought_level", effort))
		slog.Warn("acp: thought_level missing", "effort", effort, "session_id", sid)
		return
	}
	matched, allowed := matchConfigChoice(opt, effort)
	if matched == "" {
		s.addStartupWarning(fmt.Sprintf("reasoning effort %q is not allowed for this model (allowed: %s)", effort, strings.Join(allowed, ", ")))
		slog.Warn("acp: thought_level rejected", "effort", effort, "session_id", sid, "allowed", strings.Join(allowed, ","))
		return
	}
	if opt.CurrentValue.String() == matched {
		return
	}
	if err := s.setConfigOption(sid, opt.ID, matched); err != nil {
		s.addStartupWarning(fmt.Sprintf("reasoning effort %q was not applied: %s", matched, err.Error()))
		slog.Warn("acp: session/set_config_option failed",
			"config_id", opt.ID, "effort", matched, "session_id", sid, "error", err)
	}
}

func (s *acpSession) thoughtLevelOption() (acpConfigOption, bool) {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return findConfigOption(s.configOptions, "thought_level")
}

func (s *acpSession) setConfigOption(sid, configID, value string) error {
	res, err := s.tr.call(s.ctx, "session/set_config_option", map[string]any{
		"sessionId": sid,
		"configId":  configID,
		"value":     value,
	})
	if err != nil {
		return err
	}
	var parsed struct {
		ConfigOptions []acpConfigOption `json:"configOptions"`
	}
	if json.Unmarshal(res, &parsed) == nil && len(parsed.ConfigOptions) > 0 {
		s.absorbConfigOptions(parsed.ConfigOptions)
		return nil
	}
	slog.Debug("acp: set_config_option response had no configOptions; assuming value applied",
		"config_id", configID, "value", value)
	s.noteConfigValue(configID, value)
	return nil
}

// noteConfigValue is the fallback for agents whose session/set_config_option
// response omits configOptions. The normal path is absorbConfigOptions(parsed.ConfigOptions).
func (s *acpSession) noteConfigValue(configID, value string) {
	value = strings.TrimSpace(value)
	s.configMu.Lock()
	for i := range s.configOptions {
		o := &s.configOptions[i]
		if o.ID != configID && !strings.EqualFold(strings.TrimSpace(o.Category), configID) {
			continue
		}
		o.CurrentValue = flexString(value)
	}
	opts := append([]acpConfigOption(nil), s.configOptions...)
	if strings.EqualFold(configID, "model") {
		s.reportedModel = value
	}
	s.configMu.Unlock()
	s.publishModelConfig(opts)
	s.publishThoughtLevel(opts)
}

func (s *acpSession) applyModelViaSetModel(sid, model, current string) {
	if current == model {
		s.setReportedModel(model)
		return
	}
	if _, err := s.tr.call(s.ctx, "session/set_model", map[string]any{
		"sessionId": sid,
		"modelId":   model,
	}); err != nil {
		s.addStartupWarning(fmt.Sprintf("model %q was not applied: %s", model, err.Error()))
		slog.Warn("acp: session/set_model failed",
			"model", model, "session_id", sid, "error", err)
		return
	}
	s.setReportedModel(model)
	slog.Info("acp: model applied via session/set_model", "model", model, "session_id", sid)
}

func (s *acpSession) addStartupWarning(msg string) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}
	s.warnMu.Lock()
	s.startupWarnings = append(s.startupWarnings, msg)
	s.warnMu.Unlock()
}

// StartupWarning returns startup model and effort warnings joined for
// the IM user. Empty when the session started cleanly.
func (s *acpSession) StartupWarning() string {
	s.warnMu.Lock()
	defer s.warnMu.Unlock()
	return strings.Join(s.startupWarnings, "\n")
}

func (s *acpSession) maybeAbsorbConfigOptionUpdate(params json.RawMessage) {
	kind, update, _ := peekSessionUpdate(params)
	if kind != "config_option_update" {
		return
	}
	var body struct {
		ConfigOptions []acpConfigOption `json:"configOptions"`
	}
	if json.Unmarshal(update, &body) != nil || len(body.ConfigOptions) == 0 {
		return
	}
	s.absorbConfigOptions(body.ConfigOptions)
}

// findModelConfigOption prefers category "model", then id "model".
func findModelConfigOption(opts []acpConfigOption) (acpConfigOption, bool) {
	return findConfigOption(opts, "model")
}

// findConfigOption prefers a matching category, then a matching id.
func findConfigOption(opts []acpConfigOption, name string) (acpConfigOption, bool) {
	var byID acpConfigOption
	var haveID bool
	for _, o := range opts {
		if strings.EqualFold(strings.TrimSpace(o.Category), name) {
			return o, true
		}
		if !haveID && strings.EqualFold(strings.TrimSpace(o.ID), name) {
			byID = o
			haveID = true
		}
	}
	return byID, haveID
}

func configChoiceValues(opt acpConfigOption) []string {
	var out []string
	for _, c := range opt.Options {
		v := strings.TrimSpace(c.Value)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func exactConfigChoice(values []string, want string) bool {
	want = strings.TrimSpace(want)
	if want == "" {
		return false
	}
	for _, v := range values {
		if strings.TrimSpace(v) == want {
			return true
		}
	}
	return false
}

func matchConfigChoice(opt acpConfigOption, want string) (string, []string) {
	values := configChoiceValues(opt)
	for _, v := range values {
		if strings.EqualFold(v, want) {
			return v, values
		}
	}
	return "", values
}

func modeDescriptionsFromConfig(opts []acpConfigOption) map[string]string {
	opt, ok := findConfigOption(opts, "mode")
	if !ok {
		return nil
	}
	out := make(map[string]string)
	for _, c := range opt.Options {
		id := strings.TrimSpace(c.Value)
		desc := strings.TrimSpace(c.Description)
		if id == "" || desc == "" {
			continue
		}
		out[id] = desc
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func modelOptionsFromConfig(opt acpConfigOption) []core.ModelOption {
	out := make([]core.ModelOption, 0, len(opt.Options))
	for _, c := range opt.Options {
		name := strings.TrimSpace(c.Value)
		if name == "" {
			continue
		}
		desc := strings.TrimSpace(c.Name)
		if desc == "" {
			desc = strings.TrimSpace(c.Description)
		}
		out = append(out, core.ModelOption{Name: name, Desc: desc})
	}
	return out
}

func modelOptionsFromLegacy(block *acpModelsBlock) []core.ModelOption {
	if block == nil {
		return nil
	}
	out := make([]core.ModelOption, 0, len(block.AvailableModels))
	for _, m := range block.AvailableModels {
		id := strings.TrimSpace(m.ModelID)
		if id == "" {
			continue
		}
		out = append(out, core.ModelOption{Name: id, Desc: strings.TrimSpace(m.Name)})
	}
	return out
}

// GetReasoningEffort reports the thought_level the agent advertises for this
// session, so the reply footer reflects what actually runs (the configured
// effort may have been rejected for the current model). Empty when unknown.
func (s *acpSession) GetReasoningEffort() string {
	opt, ok := s.thoughtLevelOption()
	if !ok {
		return ""
	}
	return opt.CurrentValue.String()
}

// GetModelDisplayName returns the agent's display name for the current model
// option value (e.g. "Claude Opus 5.5" for "claude-opus-5-5-medium"). Only
// enabled when the wrapper sets ShowModelDisplayName; empty otherwise.
func (s *acpSession) GetModelDisplayName() string {
	if !s.showModelDisplayName {
		return ""
	}
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	opt, ok := findModelConfigOption(s.configOptions)
	if !ok {
		return ""
	}
	current := opt.CurrentValue.String()
	for _, c := range opt.Options {
		if c.Value == current {
			return strings.TrimSpace(c.Name)
		}
	}
	return ""
}
