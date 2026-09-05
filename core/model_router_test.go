package core

import (
	"context"
	"strings"
	"testing"
)

// setRouterEnv 模拟 claude-models preset 注入的 env：pro = ANTHROPIC_MODEL，
// flash = ANTHROPIC_DEFAULT_HAIKU_MODEL。
func setRouterEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ANTHROPIC_MODEL", "deepseek-v4-pro")
	t.Setenv("ANTHROPIC_DEFAULT_HAIKU_MODEL", "deepseek-v4-flash")
}

func TestClassifyMessage_Rules(t *testing.T) {
	setRouterEnv(t)
	cfg := ModelRouterConfig{Enabled: true, UseLLM: false, ComplexMinLen: 800}

	cases := []struct {
		name  string
		text  string
		want  string // "simple" | "complex"
		model string
	}{
		{"complex keyword", "帮我排查一下这个告警的根因", "complex", "deepseek-v4-pro"},
		{"complex keyword trace", "这个 traceId 的链路分析", "complex", "deepseek-v4-pro"},
		{"simple keyword", "查券", "simple", "deepseek-v4-flash"},
		{"simple keyword member", "查会员 13800000000", "simple", "deepseek-v4-flash"},
		{"long text", strings.Repeat("这是一个很长的消息", 100), "complex", "deepseek-v4-pro"},
	}
	for _, c := range cases {
		res := ClassifyMessage(context.Background(), c.text, cfg)
		if res.Tier != c.want {
			t.Errorf("%s: tier = %q, want %q (model=%q)", c.name, res.Tier, c.want, res.Model)
		}
		if res.Model != c.model {
			t.Errorf("%s: model = %q, want %q", c.name, res.Model, c.model)
		}
		if res.Elapsed <= 0 {
			t.Errorf("%s: elapsed not recorded", c.name)
		}
	}
}

func TestClassifyMessage_DefaultTier(t *testing.T) {
	setRouterEnv(t)
	// No keywords hit, LLM disabled → falls back to default tier.
	cfg := ModelRouterConfig{Enabled: true, UseLLM: false, ModelDefault: "simple", ComplexMinLen: 800}
	res := ClassifyMessage(context.Background(), "一个没有关键词的短消息", cfg)
	if res.Tier != "simple" || res.Model != "deepseek-v4-flash" {
		t.Fatalf("default tier = %q model = %q, want simple/deepseek-v4-flash", res.Tier, res.Model)
	}

	cfg.ModelDefault = "complex"
	res = ClassifyMessage(context.Background(), "另一个普通消息", cfg)
	if res.Tier != "complex" || res.Model != "deepseek-v4-pro" {
		t.Fatalf("default tier = %q model = %q, want complex/deepseek-v4-pro", res.Tier, res.Model)
	}
}

func TestClassifyMessage_UseLLMOff(t *testing.T) {
	setRouterEnv(t)
	// UseLLM=false：规则未命中直接走默认档位，不触发 LLM 分类。
	cfg := ModelRouterConfig{Enabled: true, UseLLM: false, ModelDefault: "simple", ComplexMinLen: 800}
	res := ClassifyMessage(context.Background(), "没有命中任何关键词的普通消息", cfg)
	if res.UsedLLM {
		t.Fatalf("UsedLLM = true, want false when use_llm is disabled")
	}
	if res.Tier != "simple" {
		t.Fatalf("tier = %q, want simple", res.Tier)
	}
}

func TestClassifyMessage_Disabled(t *testing.T) {
	setRouterEnv(t)
	cfg := ModelRouterConfig{Enabled: false}
	res := ClassifyMessage(context.Background(), "任意消息", cfg)
	if res.Tier != "disabled" {
		t.Fatalf("tier = %q, want disabled", res.Tier)
	}
}

func TestRouterModels_SubagentFallback(t *testing.T) {
	t.Setenv("ANTHROPIC_MODEL", "deepseek-v4-pro")
	t.Setenv("ANTHROPIC_DEFAULT_HAIKU_MODEL", "")
	t.Setenv("CLAUDE_CODE_SUBAGENT_MODEL", "deepseek-v4-flash")
	simple, complex := routerModels()
	if simple != "deepseek-v4-flash" || complex != "deepseek-v4-pro" {
		t.Fatalf("routerModels() = (%q, %q), want (deepseek-v4-flash, deepseek-v4-pro)", simple, complex)
	}
}

func TestFormatModelRouteResult(t *testing.T) {
	res := ModelRouteResult{Tier: "complex", Model: "deepseek-v4-pro"}
	out := FormatModelRouteResult(res)
	if !strings.Contains(out, "deepseek-v4-pro") {
		t.Fatalf("unexpected format: %q", out)
	}
}
