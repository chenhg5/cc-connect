package droid

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDroidCustomModelsFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	content := `{
  "customModels": [
    {
      "id": "custom:model-1",
      "displayName": "Custom Model 1",
      "model": "base-1"
    },
    {
      "id": "custom:model-2",
      "displayName": "",
      "model": "base-2"
    }
  ]
}`

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	models := loadDroidCustomModelsFromFile(path)
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2", len(models))
	}
	if models[0].Name != "custom:model-1" || models[0].Desc != "Custom Model 1" {
		t.Fatalf("model[0] = %+v, want id=custom:model-1 desc=Custom Model 1", models[0])
	}
	if models[1].Name != "custom:model-2" || models[1].Desc != "base-2" {
		t.Fatalf("model[1] = %+v, want id=custom:model-2 desc=base-2", models[1])
	}
}
