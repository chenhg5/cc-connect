package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

func TestClassifyMessage_ReasonDisclosesKeywordAndLength(t *testing.T) {
	cfg := routerCfg(t)
	cases := []struct {
		name   string
		text   string
		substr string // Reason 应包含的子串
	}{
		{"complex keyword 披露关键词", "帮我排查一下这个告警的根因", "「根因」"},
		{"simple keyword 披露关键词", "查券", "「查券」"},
	}
	for _, c := range cases {
		res := ClassifyMessage(context.Background(), c.text, false, cfg)
		if !strings.Contains(res.Reason, c.substr) {
			t.Errorf("%s: Reason=%q, want contains %q", c.name, res.Reason, c.substr)
		}
	}

	// 长度阈值命中时披露实际字数
	longText := strings.Repeat("这是一个很长的消息", 100)
	res := ClassifyMessage(context.Background(), longText, false, cfg)
	wantLen := fmt.Sprintf("（%d字）", len([]rune(longText)))
	if !strings.Contains(res.Reason, wantLen) {
		t.Errorf("length: Reason=%q, want contains %q", res.Reason, wantLen)
	}
}

func TestClassifyMessage_Fallback(t *testing.T) {
	cfg := routerCfg(t)
	// 无关键词命中，LLM 关闭 → fallback_model
	res := ClassifyMessage(context.Background(), "没有关键词的普通消息", false, cfg)
	if res.Tier != "fallback" || res.Model != "deepseek-v4-flash" {
		t.Fatalf("tier=%q model=%q, want fallback/deepseek-v4-flash", res.Tier, res.Model)
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

func TestClassifyMessage_ConfigError(t *testing.T) {
	cfg := routerCfg(t)
	// 选中的模型 key 不在 claude-models.json 里 → 返回配置错误
	cfg.ComplexModel = "nonexistent-model"
	res := ClassifyMessage(context.Background(), "帮我排查这个告警的根因", false, cfg)
	if res.ConfigErr == "" {
		t.Fatalf("ConfigErr empty, want config error for missing model key")
	}
	if res.Override.Model != "" {
		t.Fatalf("Override.Model=%q, want empty when key missing", res.Override.Model)
	}
	if !strings.Contains(FormatModelRouteResult(res), "配置错误") {
		t.Fatalf("format should contain 配置错误, got %q", FormatModelRouteResult(res))
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
	// 完整 env 原样收集（配啥注入啥）
	glmEnv := m["glm-5.3-flash"].Env
	if len(glmEnv) != 3 {
		t.Fatalf("glm Env len=%d, want 3（BASE_URL/AUTH_TOKEN/MODEL）: %v", len(glmEnv), glmEnv)
	}
}

func TestLoadClaudeModels_EnvAllFields(t *testing.T) {
	// env 里配的所有字段（含额外项）都应进 Env，不挑拣
	content := `{
  "models": {
    "glm-5.3-flash": {
      "env": {
        "ANTHROPIC_BASE_URL": "https://open.bigmodel.cn/api/anthropic",
        "ANTHROPIC_AUTH_TOKEN": "key-glm",
        "ANTHROPIC_MODEL": "glm-5.3-flash",
        "ANTHROPIC_DEFAULT_HAIKU_MODEL": "glm-5.3-flash",
        "CLAUDE_CODE_SUBAGENT_MODEL": "glm-5.3-flash",
        "CUSTOM_FUTURE_KEY": "some-value"
      }
    }
  }
}`
	p := filepath.Join(t.TempDir(), "claude-models.json")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	m := loadClaudeModels(p)
	env := m["glm-5.3-flash"].Env
	joined := strings.Join(env, "\n")
	for _, want := range []string{"ANTHROPIC_BASE_URL=", "ANTHROPIC_AUTH_TOKEN=", "ANTHROPIC_MODEL=", "ANTHROPIC_DEFAULT_HAIKU_MODEL=", "CLAUDE_CODE_SUBAGENT_MODEL=", "CUSTOM_FUTURE_KEY="} {
		if !strings.Contains(joined, want) {
			t.Fatalf("Env 缺少 %q：%v", want, env)
		}
	}
	if len(env) != 6 {
		t.Fatalf("Env len=%d, want 6（配啥注入啥）", len(env))
	}
}

func TestFormatModelRouteResult(t *testing.T) {
	res := ModelRouteResult{Tier: "complex", Model: "deepseek-v4-pro"}
	out := FormatModelRouteResult(res)
	if !strings.Contains(out, "deepseek-v4-pro") {
		t.Fatalf("unexpected format: %q", out)
	}
}

func TestClassifyModelName_StripsContextWindowSuffix(t *testing.T) {
	cases := map[string]string{
		"deepseek-flash[1m]":    "deepseek-flash",
		"deepseek-v4-pro[1m]":   "deepseek-v4-pro",
		"glm-5.3-flash[1m]":     "glm-5.3-flash",
		"glm-5.3-flash[1M]":     "glm-5.3-flash",
		"glm-5.3-flash":         "glm-5.3-flash",
		"deepseek-flash [1m]":   "deepseek-flash",
		"claude-sonnet-4-5[1m]": "claude-sonnet-4-5",
	}
	for in, want := range cases {
		if got := classifyModelName(in); got != want {
			t.Fatalf("classifyModelName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestClassifyViaLLM_ThinkingFlag 验证 classify_thinking 开关直接决定请求体的 thinking.type，
// 且直连端点时模型名已剥掉 [1m] 后缀。
func TestClassifyViaLLM_ThinkingFlag(t *testing.T) {
	var got struct {
		Model    string `json:"model"`
		Thinking struct {
			Type string `json:"type"`
		} `json:"thinking"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"content":[{"type":"thinking","thinking":"..."},{"type":"text","text":"simple"}]}`)
	}))
	defer srv.Close()

	cred := ModelRouteOverride{BaseURL: srv.URL, APIKey: "k", Model: "deepseek-flash[1m]"}
	for _, want := range []string{"disabled", "enabled"} {
		tier, ok, reason := classifyViaLLM(context.Background(), "查下这个订单", cred, "", nil, nil, want == "enabled")
		if !ok || tier != "simple" {
			t.Fatalf("thinking=%s: tier=%q ok=%v reason=%q", want, tier, ok, reason)
		}
		if got.Thinking.Type != want {
			t.Fatalf("thinking.type = %q, want %q", got.Thinking.Type, want)
		}
		if got.Model != "deepseek-flash" {
			t.Fatalf("model = %q, want [1m] 后缀被剥离", got.Model)
		}
	}
}

// TestClassifyMessage_LLMFailReasonOnCard 验证 LLM 分类失败时，失败原因（HTTP 状态码 + 端点错误摘要）
// 会出现在路由卡片的「调度依据」里，不用翻日志。
func TestClassifyMessage_LLMFailReasonOnCard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"type":"error","error":{"type":"invalid_request_error","code":"1210","message":"该模型始终思考，不支持关闭思考"}}`)
	}))
	defer srv.Close()

	cfg := routerCfg(t)
	cfg.ModelsConfig = writeTempModelsConfig(t, "glm-5.3-flash", srv.URL, "glm-5.3-flash[1m]")
	cfg.ClassifyModel = "glm-5.3-flash"
	cfg.FallbackModel = "glm-5.3-flash" // 临时配置只有这一个 key，兜底也指它
	cfg.UseLLMClassify = true
	// 关键词都不命中的消息 → 走 LLM 兜底
	res := ClassifyMessage(context.Background(), "在吗", false, cfg)
	if res.Tier != "fallback" {
		t.Fatalf("tier=%q, want fallback", res.Tier)
	}
	card := FormatModelRouteResult(res)
	if !strings.Contains(card, "HTTP 400") || !strings.Contains(card, "该模型始终思考") {
		t.Fatalf("卡片未带上失败原因: %q", card)
	}
}

func writeTempModelsConfig(t *testing.T, key, baseURL, model string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "claude-models.json")
	content := fmt.Sprintf(`{"models":{%q:{"env":{"ANTHROPIC_BASE_URL":%q,"ANTHROPIC_AUTH_TOKEN":"k","ANTHROPIC_MODEL":%q}}}}`, key, baseURL, model)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}
