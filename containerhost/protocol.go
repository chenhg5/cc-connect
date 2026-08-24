package containerhost

import (
	"encoding/json"
	"time"
)

const (
	ImageRepository = "ghcr.io/shusfun/cc-connect"
	DefaultSocket   = "/run/cc-connect-deploy/host.sock"
	ContractHash    = "cc-connect-container-host-v1"
	contractHeader  = "X-CC-Connect-Deploy-Contract"
)

type PendingOperation struct {
	RunID              string    `json:"run_id"`
	Kind               string    `json:"kind"`
	TargetTag          string    `json:"target_tag"`
	TargetImage        string    `json:"target_image"`
	PreviousTag        string    `json:"previous_tag"`
	PreviousImage      string    `json:"previous_image"`
	PriorPreviousTag   string    `json:"prior_previous_tag,omitempty"`
	PriorPreviousImage string    `json:"prior_previous_image,omitempty"`
	BackupName         string    `json:"backup_name"`
	Deadline           time.Time `json:"deadline"`
	Committed          bool      `json:"committed"`
}

type Status struct {
	CurrentTag    string            `json:"current_tag"`
	CurrentImage  string            `json:"current_image"`
	PreviousTag   string            `json:"previous_tag,omitempty"`
	PreviousImage string            `json:"previous_image,omitempty"`
	Pending       *PendingOperation `json:"pending,omitempty"`
	LastRunID     string            `json:"last_run_id,omitempty"`
	LastOutcome   string            `json:"last_outcome,omitempty"`
	LastError     string            `json:"last_error,omitempty"`
}

type Preparation struct {
	Tag      string          `json:"tag"`
	Image    string          `json:"image"`
	Manifest json.RawMessage `json:"manifest"`
}

type PrepareRequest struct {
	Tag string `json:"tag"`
}

type ActivateRequest struct {
	RunID       string `json:"run_id"`
	Kind        string `json:"kind"`
	TargetTag   string `json:"target_tag"`
	TargetImage string `json:"target_image"`
	BackupName  string `json:"backup_name"`
}

type RunRequest struct {
	RunID string `json:"run_id"`
}
