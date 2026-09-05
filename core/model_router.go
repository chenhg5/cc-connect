package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ModelRouterConfig 模型路由规则。模型凭证（base_url/token/model）不在此配置，
// 统一从 models_config 指向的 claude-models.json 读取（唯一凭证源）。
// complex_model/simple_model/fallback_model/classify_model/multimodal_model 必须与 claude-models.json 的 models 节点 key 一致。
type ModelRouterConfig struct {
	Enabled         bool
	ModelsConfig    string // claude-models.json 路径（唯一凭证源）
	ComplexModel    string // 复杂问题模型 key
	SimpleModel     string // 简单问题模型 key
	FallbackModel   string // 兜底模型 key（分类失败/LLM 失败时）
	ClassifyModel   string // LLM 分类用模型 key
	ClassifyPrompt  string // LLM 分类提示词（{text} 占位符替换为用户消息；空则用内置默认）
	MultimodalModel string // 多模态消息（图片/文件等）时强制用的模型 key
	UseLLMClassify  bool   // 规则未命中时是否用 LLM 兜底分类
	ComplexKeywords []string
	SimpleKeywords  []string
	ComplexMinLen   int // 消息字符数（rune）超过即判 complex
}

// ModelRouteOverride 模型路由的 per-spawn 覆盖：完整凭证（来自 claude-models.json）。
type ModelRouteOverride struct {
	BaseURL string   // 用于 LLM 分类请求
	APIKey  string   // 用于 LLM 分类请求
	Model   string   // 模型 ID（LLM 分类请求 + footer 显示）
	Env     []string // 完整 env（claude-models.json 该模型 env 的所有字段），配啥注入啥
}

// ModelRouteResult 分类结果。
type ModelRouteResult struct {
	Tier      string // "complex" | "simple" | "fallback" | "disabled" | "multimodal"
	Model     string // 选中的模型 key
	Reason    string // 选择原因（展示给用户）
	ConfigErr string // 配置错误（选中的模型 key 不在 claude-models.json 的 models 中时非空）
	Override  ModelRouteOverride
	UsedLLM   bool
	Elapsed   time.Duration
}

// claudeModelsFile 是 claude-models.json 的顶层结构。
type claudeModelsFile struct {
	Models map[string]struct {
		Env map[string]string `json:"env"`
	} `json:"models"`
}

// loadClaudeModels 读取 claude-models.json，返回 key -> 完整凭证 的映射。
func loadClaudeModels(path string) map[string]ModelRouteOverride {
	path = expandHome(path)
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Warn("model_router: cannot read models config", "path", path, "error", err)
		return nil
	}
	var f claudeModelsFile
	if err := json.Unmarshal(data, &f); err != nil {
		slog.Warn("model_router: invalid models config", "path", path, "error", err)
		return nil
	}
	out := make(map[string]ModelRouteOverride, len(f.Models))
	for key, m := range f.Models {
		env := m.Env
		if env == nil {
			env = map[string]string{}
		}
		cred := ModelRouteOverride{
			BaseURL: env["ANTHROPIC_BASE_URL"],
			APIKey:  env["ANTHROPIC_AUTH_TOKEN"],
			Model:   env["ANTHROPIC_MODEL"],
		}
		if cred.APIKey == "" {
			cred.APIKey = env["ANTHROPIC_API_KEY"]
		}
		// 完整 env 原样收集，配啥注入啥（以后加任何新字段都自动注入，不挑拣）
		var envList []string
		for k, v := range env {
			if v != "" {
				envList = append(envList, k+"="+v)
			}
		}
		cred.Env = envList
		out[key] = cred
	}
	return out
}

// resolveOverride 从 models 查 key 的凭证。key 为空返回空凭证；
// key 不在 models 中返回配置错误信息（用于路由后卡片告知用户）。
func resolveOverride(models map[string]ModelRouteOverride, key string) (ModelRouteOverride, string) {
	if key == "" {
		return ModelRouteOverride{}, ""
	}
	cred, ok := models[key]
	if !ok {
		return ModelRouteOverride{}, fmt.Sprintf("模型 %q 不在 claude-models.json 的 models 节点中", key)
	}
	return cred, ""
}

// expandHome 展开路径开头的 ~ 为当前用户主目录。
func expandHome(p string) string {
	if p == "" {
		return p
	}
	if p == "~" {
		if h, err := os.UserHomeDir(); err == nil {
			return h
		}
		return p
	}
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}

// defaultComplexMinLen 消息长度阈值（rune 数）。
const defaultComplexMinLen = 800

// classifyTimeout LLM 兜底分类的调用超时。
const classifyTimeout = 5 * time.Second

// defaultClassifyPrompt 内置分类提示词（config 未配 classify_prompt 时兜底）。
// {text} 占位符替换为用户消息。
const defaultClassifyPrompt = `你是消息复杂度分类器。判断下面这条用户消息该用「复杂模型」还是「简单模型」处理，只回复一个词 simple 或 complex，不要任何解释、标点或换行。

判 complex（复杂，需要强模型）：
- 根因分析（RCA）、源码定位
- 代码审查（CR）
- 价格变价、渠道不可售卖、营销不命中等需要证据链的分析
- 相关性分析、关联分析
- 订单问题排查、深度排查
- 涉及多系统、多步推理的深度分析
- 新增/修改脚本
- 整理文档
- 整理/修改 skill 和知识库

判 simple（简单，轻量模型即可）：
- 简单查询：查券、查会员、查城市、查排期、查订单、查 redis/缓存
- 查告警、查链路、查 traceId 等日常查询
- 问候、寒暄、简单确认
- 一句话回答或查单个值的问题

用户消息：
{text}`

// ClassifyMessage 按复杂度把消息路由到 complex/simple 模型。
// 多模态消息（multimodal=true）优先用 multimodal_model，跳过复杂度分类。
// 判定顺序：复杂关键词 → 长度阈值 → 简单关键词 → LLM 兜底 → fallback_model。
// 选中的模型 key 从 claude-models.json 取完整凭证（base_url/token/model）。
func ClassifyMessage(ctx context.Context, text string, multimodal bool, cfg ModelRouterConfig) ModelRouteResult {
	start := time.Now()
	res := ModelRouteResult{}

	models := loadClaudeModels(cfg.ModelsConfig)

	if !cfg.Enabled {
		res.Tier = "disabled"
		res.Model = cfg.FallbackModel
		res.Reason = "路由未启用"
		res.Override, res.ConfigErr = resolveOverride(models, res.Model)
		res.Elapsed = time.Since(start)
		return res
	}

	// 多模态消息（图片/文件等）强制走 multimodal_model
	if multimodal && cfg.MultimodalModel != "" {
		res.Tier = "multimodal"
		res.Model = cfg.MultimodalModel
		res.Reason = "多模态消息"
		res.Override, res.ConfigErr = resolveOverride(models, res.Model)
		res.Elapsed = time.Since(start)
		return res
	}

	complexKws := cfg.ComplexKeywords
	simpleKws := cfg.SimpleKeywords
	minLen := cfg.ComplexMinLen
	if minLen <= 0 {
		minLen = defaultComplexMinLen
	}

	classifyCred := models[cfg.ClassifyModel]

	tier := ""
	reason := ""
	llmFailed := false
	// 1. 复杂关键词
	if containsAny(text, complexKws) {
		tier = "complex"
		reason = "命中复杂关键词"
	} else if minLen > 0 && len([]rune(text)) >= minLen {
		// 2. 长度阈值
		tier = "complex"
		reason = "消息长度超阈值"
	} else if containsAny(text, simpleKws) {
		// 3. 简单关键词
		tier = "simple"
		reason = "命中简单关键词"
	} else if cfg.UseLLMClassify && classifyCred.Model != "" {
		// 4. LLM 兜底
		if t, ok := classifyViaLLM(ctx, text, classifyCred, cfg.ClassifyPrompt); ok {
			tier = t
			res.UsedLLM = true
			reason = "LLM 判定为 " + t
		} else {
			llmFailed = true
		}
	}

	// 5. 选模型 key
	var key string
	switch tier {
	case "complex":
		key = cfg.ComplexModel
	case "simple":
		key = cfg.SimpleModel
	}
	if key == "" {
		key = cfg.FallbackModel
		tier = "fallback"
		if reason == "" {
			if llmFailed {
				reason = "LLM 分类失败，回退兜底"
			} else {
				reason = "规则未命中，回退兜底"
			}
		}
	}

	res.Tier = tier
	res.Model = key
	res.Reason = reason
	res.Override, res.ConfigErr = resolveOverride(models, key)
	res.Elapsed = time.Since(start)
	return res
}

func containsAny(text string, keywords []string) bool {
	for _, kw := range keywords {
		if kw == "" {
			continue
		}
		if strings.Contains(text, kw) {
			return true
		}
	}
	return false
}

// classifyViaLLM 用指定凭证的 anthropic 兼容端点做一次轻量分类。
// 返回 "simple" | "complex"。
func classifyViaLLM(ctx context.Context, text string, cred ModelRouteOverride, prompt string) (string, bool) {
	if cred.BaseURL == "" || cred.APIKey == "" || cred.Model == "" {
		return "", false
	}
	if prompt == "" {
		prompt = defaultClassifyPrompt
	}
	// {text} 占位符替换为用户消息；无占位符则直接拼到末尾。
	if strings.Contains(prompt, "{text}") {
		prompt = strings.ReplaceAll(prompt, "{text}", text)
	} else {
		prompt = prompt + "\n\n" + text
	}

	url := strings.TrimRight(cred.BaseURL, "/") + "/v1/messages"

	payload := map[string]any{
		"model":      cred.Model,
		"max_tokens": 4,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", false
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cred.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: classifyTimeout}
	resp, err := client.Do(req)
	if err != nil {
		slog.Debug("model_router: llm classify failed", "error", err)
		return "", false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Debug("model_router: llm classify non-200", "status", resp.StatusCode)
		return "", false
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", false
	}
	var out struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", false
	}
	if len(out.Content) == 0 {
		return "", false
	}

	answer := strings.ToLower(strings.TrimSpace(out.Content[0].Text))
	switch {
	case strings.Contains(answer, "complex"):
		return "complex", true
	case strings.Contains(answer, "simple"):
		return "simple", true
	default:
		return "", false
	}
}

// FormatModelRouteResult 返回分类结果的可读描述（用于卡片/日志）。
func FormatModelRouteResult(res ModelRouteResult) string {
	if res.ConfigErr != "" {
		return fmt.Sprintf("⚠️ 配置错误：%s", res.ConfigErr)
	}
	if res.Reason != "" {
		return fmt.Sprintf("分类耗时 %s · 选中 %s · 原因 %s", formatElapsed(res.Elapsed), res.Model, res.Reason)
	}
	return fmt.Sprintf("分类耗时 %s · 选中 %s", formatElapsed(res.Elapsed), res.Model)
}
