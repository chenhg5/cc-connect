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

func TestMgmt_CCSwitchImport_RejectsAmbiguousNamesBeforeWriting(t *testing.T) {
	claude := CCSwitchProviderInfo{Name: "shared", AppType: "claude", Model: "synthetic-claude"}
	codex := CCSwitchProviderInfo{Name: "shared", AppType: "codex", Model: "synthetic-codex"}
	unique := CCSwitchProviderInfo{Name: "unique", AppType: "claude"}
	for _, tt := range []struct {
		name      string
		providers []CCSwitchProviderInfo
		names     []string
	}{
		{"claude then codex", []CCSwitchProviderInfo{claude, codex}, []string{"shared"}},
		{"codex then claude", []CCSwitchProviderInfo{codex, claude}, []string{"shared"}},
		{"unique requested first", []CCSwitchProviderInfo{unique, claude, codex}, []string{"unique", "shared"}},
		{"unique requested last", []CCSwitchProviderInfo{codex, claude, unique}, []string{"shared", "unique"}},
		{"unknown requested first", []CCSwitchProviderInfo{claude, codex}, []string{"missing", "shared"}},
		{"same app type", []CCSwitchProviderInfo{claude, claude}, []string{"shared"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mgmt := NewManagementServer(0, "synthetic-token", nil)
			mgmt.SetListCCSwitchProviders(func() ([]CCSwitchProviderInfo, error) {
				return tt.providers, nil
			})
			var writes []GlobalProviderInfo
			mgmt.SetAddGlobalProvider(func(p GlobalProviderInfo) error {
				writes = append(writes, p)
				return nil
			})

			response, status := postCCSwitchImport(t, mgmt, tt.names)
			if status != http.StatusBadRequest || response.OK {
				t.Errorf("status = %d, ok = %v; want 400 and false", status, response.OK)
			}
			if !strings.Contains(response.Error, "ambiguous") || !strings.Contains(response.Error, "shared") {
				t.Errorf("error = %q; want ambiguous provider name", response.Error)
			}
			if len(writes) != 0 {
				t.Errorf("addGlobalProvider called %d times; want no writes", len(writes))
			}
		})
	}
}

func TestMgmt_CCSwitchImport_PreservesUniqueAndUnknownNames(t *testing.T) {
	mgmt := NewManagementServer(0, "synthetic-token", nil)
	mgmt.SetListCCSwitchProviders(func() ([]CCSwitchProviderInfo, error) {
		return []CCSwitchProviderInfo{
			{Name: "shared", AppType: "claude"},
			{Name: "shared", AppType: "codex"},
			{Name: "unique", AppType: "claude", APIKey: "synthetic-key", BaseURL: "https://example.invalid", Model: "synthetic-model"},
			{Name: "other", AppType: "codex"},
		}, nil
	})
	var writes []GlobalProviderInfo
	mgmt.SetAddGlobalProvider(func(p GlobalProviderInfo) error {
		writes = append(writes, p)
		return nil
	})

	response, status := postCCSwitchImport(t, mgmt, []string{"unique", "missing", "other"})
	if status != http.StatusOK || !response.OK {
		t.Fatalf("status = %d, response = %+v; want success", status, response)
	}
	var result struct {
		Imported []string `json:"imported"`
		Skipped  []string `json:"skipped"`
	}
	if err := json.Unmarshal(response.Data, &result); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Imported, []string{"unique", "other"}) || !reflect.DeepEqual(result.Skipped, []string{"missing"}) {
		t.Errorf("result = %+v; want unique and other imported, missing skipped", result)
	}
	want := []GlobalProviderInfo{
		{Name: "unique", AgentTypes: []string{"claudecode"}, APIKey: "synthetic-key", BaseURL: "https://example.invalid", Model: "synthetic-model"},
		{Name: "other", AgentTypes: []string{"codex"}},
	}
	if !reflect.DeepEqual(writes, want) {
		t.Errorf("imported providers = %+v; want %+v", writes, want)
	}
}

func TestMgmt_CCSwitchImport_PreservesSkippedResults(t *testing.T) {
	for _, tt := range []struct {
		name         string
		names        []string
		wantImported []string
		wantSkipped  []string
		wantWrites   int
	}{
		{"unknown only", []string{"missing"}, nil, []string{"missing"}, 0},
		{"repeated unique name", []string{"unique", "unique"}, []string{"unique"}, []string{"unique"}, 2},
		{"add failure", []string{"unwritable", "unique"}, []string{"unique"}, []string{"unwritable"}, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mgmt := NewManagementServer(0, "synthetic-token", nil)
			mgmt.SetListCCSwitchProviders(func() ([]CCSwitchProviderInfo, error) {
				return []CCSwitchProviderInfo{{Name: "unique"}, {Name: "unwritable"}}, nil
			})
			writes := 0
			added := make(map[string]bool)
			mgmt.SetAddGlobalProvider(func(p GlobalProviderInfo) error {
				writes++
				if p.Name == "unwritable" || added[p.Name] {
					return errors.New("synthetic add failure")
				}
				added[p.Name] = true
				return nil
			})

			response, status := postCCSwitchImport(t, mgmt, tt.names)
			if status != http.StatusOK || !response.OK {
				t.Fatalf("status = %d, response = %+v; want success", status, response)
			}
			var result struct {
				Imported []string `json:"imported"`
				Skipped  []string `json:"skipped"`
			}
			if err := json.Unmarshal(response.Data, &result); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(result.Imported, tt.wantImported) || !reflect.DeepEqual(result.Skipped, tt.wantSkipped) {
				t.Errorf("result = %+v; want imported %v and skipped %v", result, tt.wantImported, tt.wantSkipped)
			}
			if writes != tt.wantWrites {
				t.Errorf("addGlobalProvider called %d times; want %d", writes, tt.wantWrites)
			}
		})
	}
}

// The dashboard language is independent of project settings. Failed imports
// must be localized without changing the selection or writing any providers.
func TestMgmt_CCSwitchImport_LocalizedErrorAndRetry(t *testing.T) {
	for _, tt := range []struct {
		language string
		want     string
	}{
		{"en", `ambiguous cc-switch provider name "shared": multiple source providers match`},
		{"zh", `cc-switch 中的服务商名称 "shared" 不明确：存在多个同名来源服务商`},
		{"zh-TW", `cc-switch 中的服務商名稱 "shared" 不明確：存在多個同名來源服務商`},
		{"ja", `cc-switch のプロバイダー名 "shared" が曖昧です：同名のプロバイダーが複数存在します`},
		{"es", `El nombre del proveedor de cc-switch "shared" es ambiguo: coinciden varios proveedores de origen`},
		{"", `ambiguous cc-switch provider name "shared": multiple source providers match`},
		{"unknown", `ambiguous cc-switch provider name "shared": multiple source providers match`},
	} {
		t.Run(tt.language, func(t *testing.T) {
			mgmt := NewManagementServer(0, "synthetic-token", nil)
			mgmt.SetListCCSwitchProviders(func() ([]CCSwitchProviderInfo, error) {
				return []CCSwitchProviderInfo{{Name: "unique"}, {Name: "shared", AppType: "claude"}, {Name: "shared", AppType: "codex"}}, nil
			})
			var writes []string
			mgmt.SetAddGlobalProvider(func(p GlobalProviderInfo) error {
				writes = append(writes, p.Name)
				return nil
			})
			response, status := postCCSwitchImportLanguage(t, mgmt, []string{"unique", "shared"}, tt.language)
			if status != http.StatusBadRequest || response.OK || response.Error != tt.want {
				t.Fatalf("status = %d, response = %+v; want localized rejection %q", status, response, tt.want)
			}
			if len(writes) != 0 {
				t.Fatalf("rejected import wrote providers: %v", writes)
			}
			response, status = postCCSwitchImportLanguage(t, mgmt, []string{"unique"}, tt.language)
			if status != http.StatusOK || !response.OK || !reflect.DeepEqual(writes, []string{"unique"}) {
				t.Fatalf("retry: status = %d, response = %+v, writes = %v", status, response, writes)
			}
		})
	}
}

// Exercise production routing and authentication without opening a listener.
func postCCSwitchImport(t *testing.T, mgmt *ManagementServer, names []string) (mgmtResponse, int) {
	t.Helper()
	return postCCSwitchImportLanguage(t, mgmt, names, "")
}

func postCCSwitchImportLanguage(t *testing.T, mgmt *ManagementServer, names []string, language string) (mgmtResponse, int) {
	t.Helper()
	bodyFields := map[string]any{"names": names}
	if language != "" {
		bodyFields["language"] = language
	}
	body, err := json.Marshal(bodyFields)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/providers/cc-switch", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer synthetic-token")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	mgmt.buildHandler(http.NewServeMux()).ServeHTTP(recorder, req)
	var response mgmtResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return response, recorder.Code
}
