package core

import (
	"reflect"
	"testing"
)

func TestMiniMaxProviderModelsPreserveExplicitConfiguration(t *testing.T) {
	ordinary := []ModelOption{{Name: "MiniMax-M3.1-Flash"}, {Name: "MiniMax-M3"}}
	for _, endpoint := range []string{"https://api.minimax.io", "https://api.minimax.io/v1", "https://api.minimax.io/anthropic/", "https://api.minimaxi.com/v1", "https://api.minimaxi.com/anthropic"} {
		t.Run(endpoint, func(t *testing.T) {
			providers := []ProviderConfig{{Name: "my-provider", BaseURL: endpoint, Model: "MiniMax-M2.7"}}
			if got := GetMiniMaxProviderModels(providers, 0); !reflect.DeepEqual(got, ordinary) {
				t.Fatalf("ordinary choices = %v", got)
			}
			if providers[0].Model != "MiniMax-M2.7" {
				t.Fatal("saved model was reset")
			}
			custom := []ModelOption{{Name: "saved-custom-model", Alias: "custom"}, {Name: "MiniMax-M2.7", Alias: "legacy"}}
			providers[0].Models = custom
			got := GetMiniMaxProviderModels(providers, 0)
			if !reflect.DeepEqual(got, custom) {
				t.Fatalf("explicit model list changed: %v", got)
			}
			got[0].Name = "changed-copy"
			if providers[0].Models[0].Name != "saved-custom-model" {
				t.Fatal("returned list aliases saved configuration")
			}
		})
	}
}

func TestMiniMaxProviderModelsLeaveUnverifiedRoutesAlone(t *testing.T) {
	for _, endpoint := range []string{"", "https://api.openai.com/v1", "https://openrouter.ai/api/v1", "https://api.minimaxi.chat/v1", "https://api.minimax.io.example/v1", "https://api.minimax.io:8443/v1", "http://api.minimax.io/v1", "https://api.minimax.io/custom", "https://user@api.minimax.io/v1", "https://api.minimax.io/v1?route=custom"} {
		if got := GetMiniMaxProviderModels([]ProviderConfig{{Name: "minimax", BaseURL: endpoint}}, 0); got != nil {
			t.Errorf("unexpected override for %q: %v", endpoint, got)
		}
	}
	for _, index := range []int{-1, 0, 1} {
		if got := GetMiniMaxProviderModels(nil, index); got != nil {
			t.Errorf("unexpected models for absent provider %d", index)
		}
	}
}
