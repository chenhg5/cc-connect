package workspacechat

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestDraftSettingsSurviveRepositoryRestart(t *testing.T) {
	dataDir := t.TempDir()
	repository, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	draft := core.WorkspaceChatDraft{
		ID: "draft-settings", OwnerClientID: "web:admin", WorkspaceRef: "workspace-a", State: "draft",
		CreatedAt: now, UpdatedAt: now,
	}
	selection := core.WorkspaceChatSelection{
		ClientID: "web:admin", WorkspaceRef: "workspace-a",
		Conversation: core.ConversationRef{Kind: core.ConversationKindDraft, ID: draft.ID}, UpdatedAt: now,
	}
	if err := repository.CreateDraftAndSelect(ctx, draft, selection); err != nil {
		t.Fatal(err)
	}
	mode, effort := "plan", "high"
	patch := core.NativeThreadSettingsPatch{Mode: &mode, PlanEffort: &effort}
	if err := repository.UpdateDraftSettings(ctx, draft.ID, draft.OwnerClientID, draft.WorkspaceRef, patch, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}

	repository, err = Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := repository.Close(); err != nil {
			t.Errorf("close repository: %v", err)
		}
	}()
	restored, err := repository.GetDraft(ctx, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored == nil || restored.SettingsPatch.Mode == nil || *restored.SettingsPatch.Mode != mode ||
		restored.SettingsPatch.PlanEffort == nil || *restored.SettingsPatch.PlanEffort != effort {
		t.Fatalf("restored draft settings = %#v", restored)
	}
}

func TestDraftMaterializationUncertainSurvivesRepositoryRestart(t *testing.T) {
	dataDir := t.TempDir()
	repository, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	draft := core.WorkspaceChatDraft{
		ID: "draft-uncertain", OwnerClientID: "web:admin", WorkspaceRef: "workspace-a", State: "draft",
		CreatedAt: now, UpdatedAt: now,
	}
	selection := core.WorkspaceChatSelection{
		ClientID: "web:admin", WorkspaceRef: draft.WorkspaceRef,
		Conversation: core.ConversationRef{Kind: core.ConversationKindDraft, ID: draft.ID}, UpdatedAt: now,
	}
	if err := repository.CreateDraftAndSelect(ctx, draft, selection); err != nil {
		t.Fatal(err)
	}
	if err := repository.MarkDraftMaterializationUncertain(ctx, draft.ID); err != nil {
		t.Fatal(err)
	}
	var firstUpdatedAt int64
	if err := repository.db.QueryRow(`SELECT updated_at_ms FROM conversation_drafts WHERE draft_id = ?`, draft.ID).Scan(&firstUpdatedAt); err != nil {
		t.Fatal(err)
	}
	if err := repository.MarkDraftMaterializationUncertain(ctx, draft.ID); err != nil {
		t.Fatalf("idempotent uncertain transition: %v", err)
	}
	var replayedUpdatedAt int64
	if err := repository.db.QueryRow(`SELECT updated_at_ms FROM conversation_drafts WHERE draft_id = ?`, draft.ID).Scan(&replayedUpdatedAt); err != nil {
		t.Fatal(err)
	}
	if replayedUpdatedAt != firstUpdatedAt {
		t.Fatalf("idempotent uncertain transition changed updated_at: got %d, want %d", replayedUpdatedAt, firstUpdatedAt)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}

	repository, err = Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := repository.Close(); err != nil {
			t.Errorf("close repository: %v", err)
		}
	}()
	restored, err := repository.GetDraft(ctx, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored == nil || restored.State != "materialization_uncertain" || restored.ThreadID != "" {
		t.Fatalf("restored uncertain draft = %#v", restored)
	}
	if err := repository.MarkDraftMaterializationUncertain(ctx, "missing-draft"); err == nil || !strings.Contains(err.Error(), "target not found") {
		t.Fatalf("missing uncertain draft error = %v", err)
	}
	if _, err := repository.db.Exec(`UPDATE conversation_drafts SET state = 'materialized', thread_id = 'thread' WHERE draft_id = ?`, draft.ID); err != nil {
		t.Fatal(err)
	}
	if err := repository.MarkDraftMaterializationUncertain(ctx, draft.ID); err == nil || !strings.Contains(err.Error(), `from state "materialized"`) {
		t.Fatalf("materialized draft uncertain transition error = %v", err)
	}
}
