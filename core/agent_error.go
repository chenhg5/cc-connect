package core

import "errors"

// AgentDiagnosticError preserves a classified backend failure without exposing
// raw process logs to the chat or the team coordinator.
type AgentDiagnosticError struct {
	Key   MsgKey
	Cause error
}

func (e *AgentDiagnosticError) Error() string {
	return NewI18n(LangEnglish).T(e.Key)
}

func (e *AgentDiagnosticError) Unwrap() error { return e.Cause }

func (i *I18n) ErrorText(err error) string {
	var diagnostic *AgentDiagnosticError
	if errors.As(err, &diagnostic) {
		return i.T(diagnostic.Key)
	}
	if err == nil {
		return ""
	}
	return err.Error()
}
