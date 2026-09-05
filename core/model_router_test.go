package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeModelsConfig 写一个临时 claude-models.json，返回路径。
func writeModelsConfig(t *testing.T) string {
	t.Helper()
	content := `{
  "models": {
    "glm-5.3-flash": {
      "env": {
        "ANTHROPIC_BASE_URL": "https://open.bigmodel.cn/api/anthropic",
        "ANTHROPIC_AUTH_TOKEN": "key-glm",
        "ANTHROPIC_MODEL": "glm-5.3-flash"
      }
    },
    "deepseek-v4-pro": {
      "env": {
        "ANTHROPIC_BASE_URL": "https://api.deepseek.com/anthropic",
        "ANTHROPIC_AUTH_TOKEN": "key-ds",
        "ANTHROPIC_MODEL": "deepseek-v4-pro"
      }
    },
    "deepseek-v4-flash": {
      "env": {
        "ANTHROPIC_BASE_URL": "https://api.deepseek.com/anthropic",
        "ANTHROPIC_AUTH_TOKEN": "key-ds",
        "ANTHROPIC_MODEL": "deepseek-v4-flash"
      }
    }
  }
}`
	p := filepath.Join(t.TempDir(), "claude-models.json")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func routerCfg(t *testing.T) ModelRouterConfig {
	return ModelRouterConfig{
		Enabled:         true,
		ModelsConfig:    writeModelsConfig(t),
		ComplexModel:    "deepseek-v4-pro",
		SimpleModel:     "glm-5.3-flash",
		FallbackModel:   "deepseek-v4-flash",
		ClassifyModel:   "glm-5.3-flash",
		MultimodalModel: "glm-5.3-flash",
		UseLLMClassify:  false,
		ComplexKeywords: []string{"根因", "RCA", "源码", "相关性", "深度排查"},
		SimpleKeywords:  []string{"查券", "查会员", "告警", "链路", "traceId"},
		ComplexMinLen:   800,
	}
}

func TestClassifyMessage_Rules(t *testing.T) {
	cfg := routerCfg(t)
	cases := []struct {
		name  string
		text  string
		want  string // tier
		model string // 选中的 key
	}{
		{"complex keyword 根因", "帮我排查一下这个告警的根因", "complex", "deepseek-v4-pro"},
		{"complex keyword 相关性", "这个报错的相关性分析", "complex", "deepseek-v4-pro"},
		{"simple keyword", "查券", "simple", "glm-5.3-flash"},
		{"simple keyword 告警", "查一下这个告警", "simple", "glm-5.3-flash"},
		{"simple keyword 链路", "查这个 traceId 的链路", "simple", "glm-5.3-flash"},
		{"long text", strings.Repeat("这是一个很长的消息", 100), "complex", "deepseek-v4-pro"},
	}
	for _, c := range cases {
		res := ClassifyMessage(context.Background(), c.text, false, cfg)
		if res.Tier != c.want || res.Model != c.model {
			t.Errorf("%s: tier=%q model=%q, want %q/%q", c.name, res.Tier, res.Model, c.want, c.model)
		}
		if res.Override.Model == "" || res.Override.BaseURL == "" || res.Override.APIKey == "" {
			t.Errorf("%s: override 不完整: %+v", c.name, res.Override)
		}
		if res.Elapsed <= 0 {
			t.Errorf("%s: elapsed not recorded", c.name)
		}
	}
}

func TestClassifyMessage_Fallback(t *testing.T) {
	cfg := routerCfg(t)
	// 无关键词命中，LLM 关闭 → fallback_model
	res := ClassifyMessage(context.Background(), "没有关键词的普通消息", false, cfg)
	if res.Tier != "default" || res.Model != "deepseek-v4-flash" {
		t.Fatalf("tier=%q model=%q, want default/deepseek-v4-flash", res.Tier, res.Model)
	}
}

func TestClassifyMessage_UseLLMOff(t *testing.T) {
	cfg := routerCfg(t)
	cfg.UseLLMClassify = false
	res := ClassifyMessage(context.Background(), "没有命中任何关键词的普通消息", false, cfg)
	if res.UsedLLM {
		t.Fatal("UsedLLM=true, want false when use_llm_classify disabled")
	}
}

func TestClassifyMessage_Disabled(t *testing.T) {
	cfg := routerCfg(t)
	cfg.Enabled = false
	res := ClassifyMessage(context.Background(), "任意消息", false, cfg)
	if res.Tier != "disabled" {
		t.Fatalf("tier=%q, want disabled", res.Tier)
	}
}

func TestClassifyMessage_Multimodal(t *testing.T) {
	cfg := routerCfg(t)
	// 多模态消息即使含复杂关键词，也强制走 multimodal_model
	res := ClassifyMessage(context.Background(), "帮我排查这个告警的根因", true, cfg)
	if res.Tier != "multimodal" || res.Model != "glm-5.3-flash" {
		t.Fatalf("tier=%q model=%q, want multimodal/glm-5.3-flash", res.Tier, res.Model)
	}
}

func TestLoadClaudeModels(t *testing.T) {
	p := writeModelsConfig(t)
	m := loadClaudeModels(p)
	if len(m) != 3 {
		t.Fatalf("len=%d, want 3", len(m))
	}
	if m["glm-5.3-flash"].Model != "glm-5.3-flash" {
		t.Fatalf("glm model=%q", m["glm-5.3-flash"].Model)
	}
	if m["deepseek-v4-pro"].BaseURL != "https://api.deepseek.com/anthropic" {
		t.Fatalf("ds base_url=%q", m["deepseek-v4-pro"].BaseURL)
	}
}

func TestFormatModelRouteResult(t *testing.T) {
	res := ModelRouteResult{Tier: "complex", Model: "deepseek-v4-pro"}
	out := FormatModelRouteResult(res)
	if !strings.Contains(out, "deepseek-v4-pro") {
		t.Fatalf("unexpected format: %q", out)
	}
}
