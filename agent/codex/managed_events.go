package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

func (s *managedSession) handleMessage(m daemonMessage) error {
	if m.Method == "account/rateLimits/updated" {
		return s.handleUsageUpdate(m.Params)
	}

	var scope struct {
		ThreadID string `json:"threadId"`
	}
	if len(m.Params) == 0 {
		return nil
	}
	if err := json.Unmarshal(m.Params, &scope); err != nil {
		return err
	}
	if scope.ThreadID != s.CurrentSessionID() && !(m.Method == "warning" && scope.ThreadID == "") {
		return nil
	}
	if len(m.ID) > 0 {
		return s.handleRequest(m)
	}
	switch m.Method {
	case "thread/settings/updated":
		var p struct {
			ThreadSettings struct {
				Model  string `json:"model"`
				Effort string `json:"effort"`
				Cwd    string `json:"cwd"`
			} `json:"threadSettings"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return err
		}
		if p.ThreadSettings.Cwd != "" && !sameManagedCwd(s.agent.GetWorkDir(), p.ThreadSettings.Cwd) {
			s.mu.Lock()
			s.canInput = false
			s.mu.Unlock()
			return fmt.Errorf("codex managed thread moved outside configured work_dir; direct input disabled")
		}
		s.decoder.runtimeMu.Lock()
		s.decoder.model = p.ThreadSettings.Model
		s.decoder.effort = p.ThreadSettings.Effort
		s.decoder.runtimeMu.Unlock()
	case "turn/started":
		var p turnNotification
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return err
		}
		s.mu.Lock()
		if s.completed[p.Turn.ID] {
			s.mu.Unlock()
			return nil
		}
		if s.turn != p.Turn.ID {
			s.turn = p.Turn.ID
			s.finalText = nil
		}
		s.mu.Unlock()
		s.decoder.storeContextUsage(nil)
		s.emit(core.Event{Type: core.EventTurnStarted, TurnID: p.Turn.ID})
	case "turn/completed":
		return s.handleTurnCompleted(m.Params)
	case "serverRequest/resolved":
		var p struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return err
		}
		id, err := managedRequestID(p.RequestID)
		if err != nil {
			return err
		}
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		s.emit(core.Event{Type: core.EventPermissionResolved, RequestID: id})
	case "item/started", "item/completed":
		return s.handleItem(m)
	case "item/agentMessage/delta":
		return s.handleAgentTextDelta(m.Params)
	case "item/commandExecution/outputDelta":
		var p struct {
			TurnID string `json:"turnId"`
			ItemID string `json:"itemId"`
			Delta  string `json:"delta"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return err
		}
		s.emit(core.Event{Type: core.EventToolOutput, TurnID: p.TurnID, ItemID: p.ItemID, ToolName: "Bash", Content: p.Delta})
	case "thread/tokenUsage/updated":
		var p appServerThreadTokenUsageNotification
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return err
		}
		s.decoder.storeContextUsage(mapAppServerTokenUsage(p))
	case "warning":
		var p struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return err
		}
		s.emitManagedNotice("warning:"+scope.ThreadID, "", &core.AgentNotice{Kind: "warning", Message: p.Message})
	case "model/rerouted":
		var p struct {
			TurnID    string `json:"turnId"`
			FromModel string `json:"fromModel"`
			ToModel   string `json:"toModel"`
			Reason    string `json:"reason"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return err
		}
		s.decoder.runtimeMu.Lock()
		s.decoder.model = p.ToModel
		s.decoder.runtimeMu.Unlock()
		s.emitManagedNotice("rerouted:"+p.TurnID, p.TurnID, &core.AgentNotice{Kind: "model_rerouted", FromModel: p.FromModel, ToModel: p.ToModel, Code: p.Reason})
	case "error":
		var p struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
			WillRetry bool   `json:"willRetry"`
			TurnID    string `json:"turnId"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return err
		}
		if p.Error.Message != "" && p.WillRetry {
			s.emitManagedNotice("retry:"+p.TurnID, p.TurnID, &core.AgentNotice{Kind: "retry", Message: p.Error.Message})
		}
		if p.Error.Message != "" && !p.WillRetry {
			s.emit(core.Event{Type: core.EventError, Error: fmt.Errorf("codex managed: %s", p.Error.Message)})
		}
	}
	return nil
}

func (s *managedSession) handleItem(m daemonMessage) error {
	var p itemNotification
	if err := json.Unmarshal(m.Params, &p); err != nil {
		return err
	}
	id, _ := p.Item["id"].(string)
	kind, _ := p.Item["type"].(string)
	if m.Method == "item/started" {
		if kind == "agentMessage" {
			s.startAgentText(p.TurnID, id, p.Item)
			return nil
		}
		s.decoder.handleItemStarted(p.Item)
		s.flushDecodedEvents(p.TurnID, id)
		return nil
	}
	s.mu.Lock()
	seen := s.seenItems[id]
	if id != "" {
		s.seenItems[id] = true
	}
	s.mu.Unlock()
	if seen {
		return nil
	}
	if kind == "userMessage" {
		var parts []string
		if content, ok := p.Item["content"].([]any); ok {
			for _, raw := range content {
				if part, ok := raw.(map[string]any); ok {
					if text, ok := part["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
		}
		s.emit(core.Event{Type: core.EventUserMessage, TurnID: p.TurnID, ItemID: id, Content: core.StripSharedBridgePrompt(strings.Join(parts, "\n"))})
		return nil
	}
	if kind == "agentMessage" {
		text, _ := p.Item["text"].(string)
		phase, _ := p.Item["phase"].(string)
		delivery, _ := p.Item["delivery"].(string)
		var questions []core.UserQuestion
		if delivery == "async" && s.agent.daemon.asyncQuestions {
			if raw, ok := p.Item["questions"].([]any); ok {
				for _, entry := range raw {
					q, ok := entry.(map[string]any)
					if !ok {
						continue
					}
					title, _ := q["title"].(string)
					if strings.TrimSpace(title) == "" {
						continue
					}
					question := core.UserQuestion{Question: title}
					if options, ok := q["options"].([]any); ok {
						for _, raw := range options {
							if label, ok := raw.(string); ok && strings.TrimSpace(label) != "" {
								question.Options = append(question.Options, core.UserQuestionOption{Label: label})
							}
						}
					}
					questions = append(questions, question)
				}
			}
		}
		content, replace := s.finishAgentText(p.TurnID, id, text)
		if phase == "final_answer" || phase == "" {
			s.mu.Lock()
			s.finalText = append(s.finalText, text)
			s.mu.Unlock()
		}
		if content != "" || replace || len(questions) > 0 {
			s.emit(core.Event{Type: core.EventText, TurnID: p.TurnID, ItemID: id, Content: content, Questions: questions, Metadata: map[string]any{"phase": phase, "delivery": delivery, "message_text": text, "replace_item_text": replace}})
		}
		return nil
	}
	s.decoder.handleItemCompleted(p.Item)
	s.flushDecodedEvents(p.TurnID, id)
	return nil
}

// Retain turn scope when reusing stdio's item-to-event conversion.
func (s *managedSession) flushDecodedEvents(turnID, itemID string) {
	for {
		select {
		case event := <-s.decoder.events:
			event.TurnID, event.ItemID = turnID, itemID
			s.emit(event)
		default:
			return
		}
	}
}

func (s *managedSession) handleTurnCompleted(raw json.RawMessage) error {
	var p turnNotification
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	errorText := ""
	if p.Turn.Error != nil {
		errorText = p.Turn.Error.Message
	}
	return s.completeManagedTurn(p.Turn.ID, p.Turn.Status, errorText, nil)
}

// Transport completion and reconnect reconciliation use the same exactly-once
// transition. A recovered old turn must not clear a newer active turn.
func (s *managedSession) completeManagedTurn(turnID, status, errorText string, recoveredContent *string) error {
	s.mu.Lock()
	if s.completed[turnID] {
		s.mu.Unlock()
		return nil
	}
	if len(s.completed) > 1024 {
		s.completed = map[string]bool{}
	}
	s.completed[turnID] = true
	for key, stream := range s.textStreams {
		if stream.turnID == turnID {
			delete(s.textStreams, key)
		}
	}
	content := ""
	if s.turn == turnID {
		s.turn = ""
		content = strings.Join(s.finalText, "\n\n")
		s.finalText = nil
	}
	if recoveredContent != nil {
		content = *recoveredContent
	}
	ids := []string{}
	for id, request := range s.pending {
		if request.turn == turnID {
			delete(s.pending, id)
			ids = append(ids, id)
		}
	}
	if len(s.seenItems) > 4096 {
		s.seenItems = map[string]bool{}
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.emit(core.Event{Type: core.EventPermissionResolved, RequestID: id})
	}
	if errorText != "" {
		s.emit(core.Event{Type: core.EventError, TurnID: turnID, Error: fmt.Errorf("codex managed turn failed: %s", errorText)})
	}
	s.emit(core.Event{Type: core.EventResult, SessionID: s.CurrentSessionID(), TurnID: turnID, Content: content, Done: true, Metadata: map[string]any{"turn_status": status, "result_authoritative": recoveredContent != nil}})
	return nil
}

// Preserve the JSON-RPC ID type: string "42" is different from numeric 42.
func managedRequestID(raw json.RawMessage) (string, error) {
	value := strings.TrimSpace(string(raw))
	if strings.HasPrefix(value, "\"") {
		var text string
		if json.Unmarshal(raw, &text) == nil {
			return "s:" + text, nil
		}
		return "", fmt.Errorf("codex managed invalid string request ID")
	}
	if json.Valid(raw) {
		if number, err := strconv.ParseInt(value, 10, 64); err == nil {
			return "n:" + strconv.FormatInt(number, 10), nil
		}
	}
	return "", fmt.Errorf("codex managed invalid server request ID")
}

func (s *managedSession) handleRequest(m daemonMessage) error {
	id, err := managedRequestID(m.ID)
	if err != nil {
		return err
	}
	var params map[string]any
	if err := json.Unmarshal(m.Params, &params); err != nil {
		return err
	}
	turn, _ := params["turnId"].(string)
	request := &managedRequest{rawID: append(json.RawMessage(nil), m.ID...), method: m.Method, turn: turn}
	event := core.Event{Type: core.EventPermissionRequest, RequestID: id, TurnID: turn, ToolInputRaw: params}
	switch m.Method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		if !s.agent.daemon.approvals {
			return nil
		}
		event.ToolName = "Bash"
		event.ToolInput, _ = params["command"].(string)
		if m.Method == "item/fileChange/requestApproval" {
			event.ToolName = "Patch"
			event.ToolInput, _ = params["reason"].(string)
		}
		event.Decisions = managedApprovalChoices(params, request)
		event.DecisionDetails = map[string]string{}
		for choice, payload := range request.choicePayloads {
			event.DecisionDetails[choice] = managedApprovalChoiceDetails(payload)
		}
	case "item/permissions/requestApproval":
		if !s.agent.daemon.approvals {
			return nil
		}
		request.permissions = params["permissions"]
		event.ToolName = "Permissions"
		event.ToolInput = appServerJSON(params["permissions"])
		event.Decisions = []string{"allow", "allow_session", "deny"}
	case "item/tool/requestUserInput":
		if !s.agent.daemon.questions {
			return nil
		}
		var p appServerRequestUserInputParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, q := range p.Questions {
			if q.ID == "" || seen[q.ID] {
				return fmt.Errorf("codex managed question IDs must be unique and non-empty")
			}
			seen[q.ID] = true
			question := core.UserQuestion{ID: q.ID, Header: q.Header, Question: q.Question, IsOther: q.IsOther, IsSecret: q.IsSecret}
			for _, opt := range q.Options {
				question.Options = append(question.Options, core.UserQuestionOption{Label: opt.Label, Description: opt.Description})
			}
			request.questions = append(request.questions, question)
		}
		event.ToolName = "AskUserQuestion"
		event.Questions = request.questions
	default:
		rpc, err := s.connection()
		if err != nil {
			return err
		}
		return rpc.write(s.ctx, map[string]any{"id": m.ID, "error": map[string]any{"code": -32601, "message": "method not supported by CC Connect managed transport"}})
	}
	s.mu.Lock()
	_, exists := s.pending[id]
	if !exists {
		s.pending[id] = request
	}
	s.mu.Unlock()
	if !exists {
		s.emit(event)
	}
	return nil
}

func containsManagedDecision(decisions []string, wanted string) bool {
	for _, decision := range decisions {
		if decision == wanted {
			return true
		}
	}
	return false
}

func (s *managedSession) RespondPermission(id string, result core.PermissionResult) error {
	s.mu.Lock()
	request := s.pending[id]
	if request == nil || request.responding {
		s.mu.Unlock()
		return fmt.Errorf("codex managed request is no longer pending or already being answered")
	}
	payload, err := managedPermissionPayload(request, result)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if !s.connected || s.closed {
		s.mu.Unlock()
		return fmt.Errorf("codex managed connection is unavailable")
	}
	rpc := s.rpc
	request.responding = true
	s.mu.Unlock()
	err = rpc.write(s.ctx, map[string]any{"id": request.rawID, "result": payload})
	s.mu.Lock()
	if s.pending[id] == request {
		if err == nil {
			delete(s.pending, id)
		} else {
			request.responding = false
		}
	}
	s.mu.Unlock()
	return err
}

func managedPermissionPayload(request *managedRequest, result core.PermissionResult) (any, error) {
	behavior := strings.ToLower(result.Behavior)
	if request.method == "item/tool/requestUserInput" {
		response := appServerRequestUserInputResponse{Answers: map[string]appServerRequestUserInputAnswer{}}
		if behavior == "skip" {
			return response, nil
		}
		if behavior != "allow" {
			return nil, fmt.Errorf("codex managed questions require answer or skip")
		}
		answers, _ := result.UpdatedInput["answers"].(map[string]any)
		if len(answers) != len(request.questions) {
			return nil, fmt.Errorf("codex managed answer requires every stable question ID")
		}
		for _, q := range request.questions {
			values := appServerRequestUserInputAnswerValues(answers[q.ID])
			if len(values) == 0 {
				return nil, fmt.Errorf("codex managed question answer is missing")
			}
			for _, value := range values {
				if strings.TrimSpace(value) == "" {
					return nil, fmt.Errorf("codex managed empty answer")
				}
			}
			response.Answers[q.ID] = appServerRequestUserInputAnswer{Answers: values}
		}
		return response, nil
	}
	if request.method == "item/permissions/requestApproval" {
		if behavior == "allow" || behavior == "allow_session" {
			permissions := request.permissions
			if permissions == nil {
				permissions = map[string]any{}
			}
			scope := "turn"
			if behavior == "allow_session" {
				scope = "session"
			}
			return map[string]any{"permissions": permissions, "scope": scope}, nil
		}
		if behavior == "deny" {
			return map[string]any{"permissions": map[string]any{}}, nil
		}
		return nil, fmt.Errorf("codex managed permissions support allow or deny")
	}
	if request.choicePayloads != nil {
		payload, offered := request.choicePayloads[behavior]
		if !offered {
			return nil, fmt.Errorf("codex managed decision is not offered by this request")
		}
		return map[string]any{"decision": payload}, nil
	}
	decision := ""
	switch behavior {
	case "allow":
		decision = "accept"
	case "deny":
		decision = "decline"
	case "cancel":
		decision = "cancel"
	}
	if decision == "" || !containsManagedDecision(request.decisions, decision) {
		return nil, fmt.Errorf("codex managed decision is not offered by this request")
	}
	return map[string]any{"decision": decision}, nil
}

func (s *managedSession) ListBackgroundTerminals(ctx context.Context) ([]core.BackgroundTerminal, error) {
	rpc, err := s.connection()
	if err != nil {
		return nil, err
	}
	cursor := ""
	seen := map[string]bool{}
	result := []core.BackgroundTerminal{}
	for {
		params := map[string]any{"threadId": s.CurrentSessionID(), "limit": 100}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var page struct {
			Data []struct {
				ID      string `json:"processId"`
				Command string `json:"command"`
				Cwd     string `json:"cwd"`
			} `json:"data"`
			NextCursor *string `json:"nextCursor"`
		}
		if err := rpc.request(ctx, "thread/backgroundTerminals/list", params, &page); err != nil {
			return nil, err
		}
		for _, terminal := range page.Data {
			result = append(result, core.BackgroundTerminal{ID: terminal.ID, Command: terminal.Command, Cwd: terminal.Cwd})
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			return result, nil
		}
		cursor = *page.NextCursor
		if seen[cursor] {
			return nil, fmt.Errorf("codex managed background terminals repeated cursor")
		}
		seen[cursor] = true
	}
}
func (s *managedSession) TerminateBackgroundTerminal(ctx context.Context, id string) error {
	if !s.agent.daemon.interrupt {
		return fmt.Errorf("codex managed terminal termination is disabled")
	}
	terminals, err := s.ListBackgroundTerminals(ctx)
	if err != nil {
		return err
	}
	found := false
	for _, terminal := range terminals {
		if terminal.ID == id {
			found = true
		}
	}
	if id == "" || !found {
		return fmt.Errorf("codex managed terminal does not belong to the attached thread")
	}
	rpc, err := s.connection()
	if err != nil {
		return err
	}
	var response struct {
		Terminated bool `json:"terminated"`
	}
	if err := rpc.request(ctx, "thread/backgroundTerminals/terminate", map[string]any{"threadId": s.CurrentSessionID(), "processId": id}, &response); err != nil {
		return err
	}
	if !response.Terminated {
		return fmt.Errorf("codex managed terminal already exited")
	}
	return nil
}
