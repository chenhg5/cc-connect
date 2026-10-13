package core

import "context"

// Personal labels organize the authenticated user's conversation list.
// Application credentials alone cannot modify another user's labels.
type PersonalLabel struct {
	ID   string `json:"group_id"`
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
}

type PersonalLabelManager interface {
	PersonalLabelsAuthorized(user string) bool
	AuthorizePersonalLabels(context.Context, string, func(string) error) error
	ListPersonalLabels(context.Context, string) ([]PersonalLabel, error)
	CreatePersonalLabel(context.Context, string, string) (PersonalLabel, error)
	AddPersonalLabel(context.Context, string, string, string) error
}

// The bot uses its own app-scoped owner ID to open its conversation. The
// manager's user token is used separately to file the resulting chat.
type PersonalLabelConversation interface {
	OpenLabelConversation(context.Context, string, string, string) (string, error)
}

type PersonalLabelError struct{ Code string }

func (e *PersonalLabelError) Error() string { return "personal labels: " + e.Code }
