package core

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWorkspaceChatDraftSettingsPersistCatalogValues(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	draft, err := fixture.service.CreateDraft(context.Background(), workspaceWebClientID, fixture.workspaceA.Ref)
	if err != nil {
		t.Fatal(err)
	}
	mode, effort := "plan", "high"
	updated, err := fixture.service.UpdateDraftSettings(context.Background(), workspaceWebClientID, fixture.workspaceA.Ref, draft.ID, NativeThreadSettingsPatch{
		Mode: &mode, PlanEffort: &effort,
	})
	if err != nil {
		t.Fatalf("UpdateDraftSettings() error = %v", err)
	}
	if updated.SettingsPatch.Mode == nil || *updated.SettingsPatch.Mode != mode ||
		updated.SettingsPatch.PlanEffort == nil || *updated.SettingsPatch.PlanEffort != effort {
		t.Fatalf("updated draft settings = %#v", updated.SettingsPatch)
	}
	restored, err := fixture.service.ReadDraft(context.Background(), workspaceWebClientID, fixture.workspaceA.Ref, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.SettingsPatch.Mode == nil || *restored.SettingsPatch.Mode != mode ||
		restored.SettingsPatch.PlanEffort == nil || *restored.SettingsPatch.PlanEffort != effort {
		t.Fatalf("restored draft settings = %#v", restored.SettingsPatch)
	}
	unknown := "client-invented-model"
	if _, err := fixture.service.UpdateDraftSettings(context.Background(), workspaceWebClientID, fixture.workspaceA.Ref, draft.ID, NativeThreadSettingsPatch{Model: &unknown}); err == nil {
		t.Fatal("UpdateDraftSettings() accepted a model outside the native catalog")
	}
}

func TestValidateNativeSettingsPatchRequiresCanonicalModeID(t *testing.T) {
	modeDefault, modePlan := "default", "plan"
	catalog := NativeRuntimeCatalog{
		Models: []NativeModelOption{{ID: "gpt-5", Model: "gpt-5", Default: true}},
		Modes: []NativeCollaborationModeOption{
			{Name: "Default", Mode: &modeDefault},
			{Name: "Plan", Mode: &modePlan},
		},
	}
	if err := validateNativeSettingsPatch(catalog, NativeThreadSettings{Model: "gpt-5"}, NativeThreadSettingsPatch{Mode: &modePlan}); err != nil {
		t.Fatalf("canonical mode was rejected: %v", err)
	}
	for _, alias := range []string{"Plan", "PLAN"} {
		t.Run(alias, func(t *testing.T) {
			if err := validateNativeSettingsPatch(catalog, NativeThreadSettings{Model: "gpt-5"}, NativeThreadSettingsPatch{Mode: &alias}); err == nil {
				t.Fatalf("mode alias %q was accepted", alias)
			}
		})
	}
}

func TestValidateNativeSettingsPatchRejectsOtherCatalogModes(t *testing.T) {
	mode := "pair"
	model := "gpt-5"
	catalog := NativeRuntimeCatalog{
		Models: []NativeModelOption{{ID: model, Model: model, Default: true}},
		Modes:  []NativeCollaborationModeOption{{Name: "Pair", Mode: &mode}},
	}
	if err := validateNativeSettingsPatch(catalog, NativeThreadSettings{Model: model}, NativeThreadSettingsPatch{Mode: &mode}); err == nil {
		t.Fatal("unsupported collaboration mode was accepted")
	}
}

func TestValidateNativeSettingsPatchUsesModeModelMask(t *testing.T) {
	modePlan, maskedModel := "plan", "gpt-plan"
	catalog := NativeRuntimeCatalog{
		Models: []NativeModelOption{
			{
				ID: "gpt-default", Model: "gpt-default", Default: true,
				ReasoningEfforts: []ReasoningEffortOption{{Effort: "low"}},
				ServiceTiers:     []ServiceTierOption{{ID: "priority"}},
			},
			{
				ID: "gpt-plan", Model: "gpt-plan",
				ReasoningEfforts: []ReasoningEffortOption{{Effort: "xhigh"}},
				ServiceTiers:     []ServiceTierOption{{ID: "flex"}},
			},
		},
		Modes: []NativeCollaborationModeOption{{Name: "Plan", Mode: &modePlan, Model: &maskedModel}},
	}
	effort, tier := "xhigh", "flex"
	patch := NativeThreadSettingsPatch{Mode: &modePlan, PlanEffort: &effort, ServiceTier: &tier}
	if err := validateNativeSettingsPatch(catalog, NativeThreadSettings{Model: "gpt-default"}, patch); err != nil {
		t.Fatalf("mode-masked model settings were rejected: %v", err)
	}

	invalidEffort := "low"
	patch.PlanEffort = &invalidEffort
	if err := validateNativeSettingsPatch(catalog, NativeThreadSettings{Model: "gpt-default"}, patch); err == nil {
		t.Fatal("effort from the pre-mode model was accepted")
	}
	patch.PlanEffort = &effort
	invalidTier := "priority"
	patch.ServiceTier = &invalidTier
	if err := validateNativeSettingsPatch(catalog, NativeThreadSettings{Model: "gpt-default"}, patch); err == nil {
		t.Fatal("tier from the pre-mode model was accepted")
	}
}

func TestWorkspaceChatDraftSettingsManagementRoute(t *testing.T) {
	fixture := newWorkspaceChatTestFixture(t)
	draft, err := fixture.service.CreateDraft(context.Background(), workspaceWebClientID, fixture.workspaceA.Ref)
	if err != nil {
		t.Fatal(err)
	}
	management := NewManagementServer(0, "", nil)
	management.SetWorkspaceChat(fixture.service)
	body := []byte(`{"mode":"plan","plan_effort":"high"}`)
	path := "/api/v1/chat/workspaces/" + fixture.workspaceA.Ref + "/drafts/" + draft.ID + "/settings"
	request := httptest.NewRequest(http.MethodPatch, path, bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	management.handleWorkspaceChatWorkspaceRoutes(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PATCH draft settings status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var envelope workspaceChatManagementEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	var updated WorkspaceChatDraft
	workspaceChatDecodeData(t, envelope, &updated)
	if updated.ID != draft.ID || updated.SettingsPatch.Mode == nil || *updated.SettingsPatch.Mode != "plan" {
		t.Fatalf("PATCH draft settings response = %#v", updated)
	}
}
