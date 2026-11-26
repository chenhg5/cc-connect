package core

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestMgmt_CCSwitchImportIDsKeepSameNamedSourcesDistinct(t *testing.T) {
	m := NewManagementServer(0, "synthetic-token", nil)
	m.SetListCCSwitchProviders(func() ([]CCSwitchProviderInfo, error) {
		return []CCSwitchProviderInfo{
			{ID: "source-claude", Name: "shared", AppType: "claude", Model: "claude-model"},
			{ID: "source-codex", Name: "shared", AppType: "codex", Model: "codex-model"},
		}, nil
	})
	var imported []GlobalProviderInfo
	m.SetAddGlobalProvider(func(p GlobalProviderInfo) error {
		for _, existing := range imported {
			if existing.Name == p.Name {
				return errors.New("duplicate destination name")
			}
		}
		imported = append(imported, p)
		return nil
	})

	body := `{"ids":["source-claude","source-codex"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/providers/cc-switch", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer synthetic-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	m.buildHandler(http.NewServeMux()).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("import status = %d, body = %s", w.Code, w.Body.String())
	}
	var result struct {
		Data struct {
			Imported []string `json:"imported"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Data.Imported, []string{"shared (claude)", "shared (codex)"}) || len(imported) != 2 || imported[0].Model != "claude-model" || imported[1].Model != "codex-model" {
		t.Fatalf("same-named sources should import distinctly: result=%+v, providers=%+v", result, imported)
	}
}

func TestMgmt_CCSwitchListExposesDistinctIDsAndImportNames(t *testing.T) {
	m := NewManagementServer(0, "synthetic-token", nil)
	m.SetListCCSwitchProviders(func() ([]CCSwitchProviderInfo, error) {
		return []CCSwitchProviderInfo{
			{ID: "source-claude", Name: "shared", AppType: "claude"},
			{ID: "source-codex", Name: "shared", AppType: "codex"},
			{ID: "source-unique", Name: "unique", AppType: "claude"},
		}, nil
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/providers/cc-switch", nil)
	req.Header.Set("Authorization", "Bearer synthetic-token")
	w := httptest.NewRecorder()
	m.buildHandler(http.NewServeMux()).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", w.Code, w.Body.String())
	}
	var result struct {
		Data struct {
			Providers []CCSwitchProviderInfo `json:"providers"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	got := result.Data.Providers
	if len(got) != 3 || got[0].ID != "source-claude" || got[1].ID != "source-codex" || got[0].ImportName != "shared (claude)" || got[1].ImportName != "shared (codex)" || got[2].ImportName != "unique" {
		t.Fatalf("distinct source choices were lost: %+v", got)
	}
}
