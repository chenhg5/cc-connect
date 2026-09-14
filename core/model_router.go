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
	"regexp"
	"strings"
	"time"
)

// ModelRouterConfig 模型路由规则。模型凭证（base_url/token/model）不在此配置，
// 统一从 models_config 指向的 claude-models.json 读取（唯一凭证源）。
// complex_model/simple_model/fallback_model/classify_model/multimodal_model 必须与 claude-models.json 的 models 节点 key 一致。
type ModelRouterConfig struct {
	Enabled          bool
	ModelsConfig     string // claude-models.json 路径（唯一凭证源）
	ComplexModel     string // 复杂问题模型 key
	SimpleModel      string // 简单问题模型 key
	FallbackModel    string // 兜底模型 key（分类失败/LLM 失败时）
	ClassifyModel    string // LLM 分类用模型 key
	ClassifyPrompt   string // LLM 分类提示词（{text} 占位符替换为用户消息；空则用内置默认）
	MultimodalModel  string // 多模态消息（图片/文件等）时强制用的模型 key
	UseLLMClassify   bool   // 规则未命中时是否用 LLM 兜底分类
	ComplexKeywords  []string
	SimpleKeywords   []string
	ComplexMinLen    int  // 消息字符数（rune）超过即判 complex
	ClassifyThinking bool // LLM 分类请求是否开 thinking（true → "enabled"，false → "disabled"）
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
	llmFailReason := ""
	// 1. 复杂关键词
	if kw := matchKeyword(text, complexKws); kw != "" {
		tier = "complex"
		reason = fmt.Sprintf("命中复杂语义关键词「%s」", kw)
	} else if minLen > 0 && len([]rune(text)) >= minLen {
		// 2. 长度阈值
		tier = "complex"
		reason = fmt.Sprintf("消息长度超阈值（%d字）", len([]rune(text)))
	} else if kw := matchKeyword(text, simpleKws); kw != "" {
		// 3. 简单关键词
		tier = "simple"
		reason = fmt.Sprintf("命中简单语义关键词「%s」", kw)
	} else if cfg.UseLLMClassify && classifyCred.Model != "" {
		// 4. LLM 兜底
		if t, ok, failReason := classifyViaLLM(ctx, text, classifyCred, cfg.ClassifyPrompt, complexKws, simpleKws, cfg.ClassifyThinking); ok {
			tier = t
			res.UsedLLM = true
			reason = "LLM 判定为 " + t
		} else {
			llmFailed = true
			llmFailReason = failReason
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
				// 失败原因带上，卡片「调度依据」直接可见，省得翻日志
				if llmFailReason != "" {
					reason = "LLM 分类失败（" + llmFailReason + "），回退兜底"
				} else {
					reason = "LLM 分类失败，回退兜底"
				}
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

// matchKeyword 返回 text 命中的第一个关键词，未命中返回空串。
func matchKeyword(text string, keywords []string) string {
	for _, kw := range keywords {
		if kw == "" {
			continue
		}
		if strings.Contains(text, kw) {
			return kw
		}
	}
	return ""
}

// classifyModelName 剥掉模型名里的 [1m] 上下文窗口后缀。
// [1m] 是 Claude Code 客户端的窗口声明，走 CLI 时由客户端在发请求前剥掉、端点看不到；
// 但 classify 是 cc-connect 用 Go 直连 /v1/messages、不经过 CLI，必须自己剥：
// deepseek 端点容忍带后缀名（回显时自行去掉），智谱端点直接 400「模型不存在」。
var contextWindowSuffixRe = regexp.MustCompile(`(?i)\[1m\]`)

func classifyModelName(model string) string {
	return strings.TrimSpace(contextWindowSuffixRe.ReplaceAllString(model, ""))
}

// classifyViaLLM 用指定凭证的 anthropic 兼容端点做一次轻量分类。
// 返回 ("simple"|"complex", 是否成功, 失败原因)。
// 失败原因会拼进路由卡片「调度依据」，便于直接看到是 400 还是超时，不用翻日志。
func classifyViaLLM(ctx context.Context, text string, cred ModelRouteOverride, prompt string, complexKws, simpleKws []string, thinking bool) (string, bool, string) {
	if cred.BaseURL == "" || cred.APIKey == "" || cred.Model == "" {
		slog.Warn("model_router: llm classify skip, missing credential", "model", cred.Model, "has_base_url", cred.BaseURL != "", "has_api_key", cred.APIKey != "")
		return "", false, "凭证缺失"
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
	// 注入规则关键词，让 LLM 参考关键词判断、与规则层保持一致
	if len(complexKws) > 0 || len(simpleKws) > 0 {
		var ref strings.Builder
		ref.WriteString("\n\n参考规则关键词（消息命中这些词就按对应档位判）：\n")
		if len(complexKws) > 0 {
			ref.WriteString("复杂（判 complex）：" + strings.Join(complexKws, "、") + "\n")
		}
		if len(simpleKws) > 0 {
			ref.WriteString("简单（判 simple）：" + strings.Join(simpleKws, "、") + "\n")
		}
		prompt += ref.String()
	}

	url := strings.TrimRight(cred.BaseURL, "/") + "/v1/messages"

	// thinking 档位：默认 disabled（deepseek 默认就吐 thinking 块，会吃掉 max_tokens 且拖慢分类）；
	// 端点不接受 disabled 时（智谱 400 [1210]「该模型始终思考」）由 classify_thinking=true 打开。
	thinkingType := "disabled"
	if thinking {
		thinkingType = "enabled"
	}
	payload := map[string]any{
		"model":      classifyModelName(cred.Model), // 直连端点，剥掉客户端窗口声明后缀（见 classifyModelName）
		"max_tokens": 256,
		"thinking":   map[string]any{"type": thinkingType},
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("model_router: llm classify marshal failed", "model", cred.Model, "error", err)
		return "", false, "构造请求失败"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		slog.Warn("model_router: llm classify new request failed", "model", cred.Model, "url", url, "error", err)
		return "", false, "构造请求失败"
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cred.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: classifyTimeout}
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("model_router: llm classify request failed", "model", cred.Model, "url", url, "error", err)
		if ctx.Err() != nil {
			return "", false, "请求超时"
		}
		return "", false, "网络错误"
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, e := io.ReadAll(io.LimitReader(resp.Body, 512))
		if e == nil {
			slog.Warn("model_router: llm classify non-200", "model", cred.Model, "status", resp.StatusCode, "body", strings.TrimSpace(string(b)))
			return "", false, fmt.Sprintf("HTTP %d %s", resp.StatusCode, classifyErrSummary(b))
		}
		slog.Warn("model_router: llm classify non-200", "model", cred.Model, "status", resp.StatusCode)
		return "", false, fmt.Sprintf("HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		slog.Warn("model_router: llm classify read body failed", "model", cred.Model, "error", err)
		return "", false, "读取响应失败"
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		slog.Warn("model_router: llm classify unmarshal failed", "model", cred.Model, "error", err, "body", truncate(string(data), 200))
		return "", false, "响应解析失败"
	}

	// 遍历所有 content 块收集 text 字段（跳过 thinking 块：deepseek 等 reasoning 模型
	// 会先输出 thinking 块，真正的答复在后面的 text 块里）
	var sb strings.Builder
	for _, c := range out.Content {
		if c.Text != "" {
			sb.WriteString(c.Text)
		}
	}
	answer := strings.ToLower(strings.TrimSpace(sb.String()))
	if answer == "" {
		slog.Warn("model_router: llm classify empty answer", "model", cred.Model, "body", truncate(string(data), 200))
		return "", false, "空答案"
	}

	switch {
	case strings.Contains(answer, "complex"):
		return "complex", true, ""
	case strings.Contains(answer, "simple"):
		return "simple", true, ""
	default:
		slog.Warn("model_router: llm classify unexpected answer", "model", cred.Model, "answer", answer)
		return "", false, "非预期答案：" + truncate(answer, 20)
	}
}

// classifyErrSummary 从 anthropic 兼容端点的错误响应里取一句人话原因（取不到返回空串）。
// 例：{"error":{"message":"[1211] 模型不存在"}} → "[1211] 模型不存在"。
func classifyErrSummary(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &e); err != nil {
		return ""
	}
	msg := e.Error.Message
	if msg == "" {
		msg = e.Message
	}
	return truncate(strings.TrimSpace(msg), 60)
}

// truncate 截断字符串到 n 字节（用于日志里的响应片段）。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// FormatModelRouteResult 返回分类结果的可读描述（用于卡片/日志）。
func FormatModelRouteResult(res ModelRouteResult) string {
	if res.ConfigErr != "" {
		return fmt.Sprintf("⚠️ 配置错误：%s", res.ConfigErr)
	}
	if res.Reason != "" {
		return fmt.Sprintf("分配模型：**%s**\n调度依据：%s\n路由耗时：%s", res.Model, res.Reason, formatElapsed(res.Elapsed))
	}
	return fmt.Sprintf("分配模型：**%s**\n路由耗时：%s", res.Model, formatElapsed(res.Elapsed))
}
