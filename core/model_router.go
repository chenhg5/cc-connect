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
	"strings"
	"time"
)

// ModelRouterConfig 模型路由规则。模型名 / base_url / token 一律不在此配置，
// 统一走 claude-models preset 注入的 env（见 routerModels）。
type ModelRouterConfig struct {
	Enabled         bool
	UseLLM          bool   // 规则未命中时是否用 LLM 兜底分类
	ModelDefault    string // "simple" | "complex"，规则未命中且 LLM 分类失败时的兜底档位
	ComplexKeywords []string
	SimpleKeywords  []string
	ComplexMinLen   int // 消息字符数（rune）超过即判 complex
}

// ModelRouteOverride 模型路由的 per-spawn 覆盖：目标模型名。
// base_url / token 由 claude-models preset 注入的 env 提供，无需覆盖。
type ModelRouteOverride struct {
	Model string
}

// ModelRouteResult 分类结果。
type ModelRouteResult struct {
	Tier    string // "simple" | "complex" | "default" | "disabled"
	Model   string
	UsedLLM bool
	Elapsed time.Duration
}

// routerModels 返回 (simple/flash 模型名, complex/pro 模型名)，均取自
// claude-models preset 注入的环境变量。pro = ANTHROPIC_MODEL（主模型），
// flash = ANTHROPIC_DEFAULT_HAIKU_MODEL（无则回退 CLAUDE_CODE_SUBAGENT_MODEL）。
func routerModels() (simple, complex string) {
	complex = os.Getenv("ANTHROPIC_MODEL")
	simple = os.Getenv("ANTHROPIC_DEFAULT_HAIKU_MODEL")
	if simple == "" {
		simple = os.Getenv("CLAUDE_CODE_SUBAGENT_MODEL")
	}
	return simple, complex
}

// defaultComplexKeywords 内置复杂关键词（config 未配时兜底）。
var defaultComplexKeywords = []string{
	"告警", "排查", "RCA", "traceId", "trace", "链路", "分析", "代码", "报错", "错误",
	"异常", "根因", "源码", "变价", "不命中", "不可售卖", "为什么", "怎么实现", "CR",
	"review", "架构", "panic", "error", "exception", "bug", "崩溃", "超时", "定位",
}

// defaultSimpleKeywords 内置简单关键词（config 未配时兜底）。
var defaultSimpleKeywords = []string{
	"查券", "查会员", "查城市", "查排期", "值班", "你好", "在吗", "同步skill", "最近修改",
	"查飞书", "查订单", "查redis", "查缓存", "谢谢", "好的",
}

// defaultComplexMinLen 消息长度阈值（rune 数）。
const defaultComplexMinLen = 800

// classifyTimeout LLM 兜底分类的调用超时。
const classifyTimeout = 5 * time.Second

// ClassifyMessage 按复杂度把消息路由到 simple/complex 模型。
// 判定顺序：复杂关键词 → 长度阈值 → 简单关键词 → LLM 兜底 → 默认档位。
func ClassifyMessage(ctx context.Context, text string, cfg ModelRouterConfig) ModelRouteResult {
	start := time.Now()
	res := ModelRouteResult{Elapsed: time.Since(start)}

	simpleModel, complexModel := routerModels()

	if !cfg.Enabled {
		res.Tier = "disabled"
		res.Model = complexModel
		if res.Model == "" {
			res.Model = simpleModel
		}
		res.Elapsed = time.Since(start)
		return res
	}

	complexKws := cfg.ComplexKeywords
	if len(complexKws) == 0 {
		complexKws = defaultComplexKeywords
	}
	simpleKws := cfg.SimpleKeywords
	if len(simpleKws) == 0 {
		simpleKws = defaultSimpleKeywords
	}
	minLen := cfg.ComplexMinLen
	if minLen <= 0 {
		minLen = defaultComplexMinLen
	}

	tier := ""
	// 1. 复杂关键词
	if containsAny(text, complexKws) {
		tier = "complex"
	} else if minLen > 0 && len([]rune(text)) >= minLen {
		// 2. 长度阈值
		tier = "complex"
	} else if containsAny(text, simpleKws) {
		// 3. 简单关键词
		tier = "simple"
	} else if cfg.UseLLM && simpleModel != "" {
		// 4. LLM 兜底（用 flash 模型做分类，最省）
		if t, ok := classifyViaLLM(ctx, text, simpleModel); ok {
			tier = t
			res.UsedLLM = true
		}
	}

	// 5. 默认档位
	if tier != "complex" && tier != "simple" {
		if strings.EqualFold(cfg.ModelDefault, "complex") {
			tier = "complex"
		} else {
			tier = "simple"
		}
	}

	res.Tier = tier
	if tier == "complex" {
		res.Model = complexModel
	} else {
		res.Model = simpleModel
	}
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

// classifyViaLLM 用 provider 的 anthropic 兼容端点做一次轻量分类。
// 返回 "simple" | "complex"。base_url / token 取自 claude-models preset 注入的 env。
func classifyViaLLM(ctx context.Context, text, model string) (string, bool) {
	baseURL := os.Getenv("ANTHROPIC_BASE_URL")
	token := os.Getenv("ANTHROPIC_AUTH_TOKEN")
	if token == "" {
		token = os.Getenv("ANTHROPIC_API_KEY")
	}
	if baseURL == "" || token == "" || model == "" {
		return "", false
	}

	url := strings.TrimRight(baseURL, "/") + "/v1/messages"
	prompt := "判断下面这条用户消息的复杂度，只回复一个词 simple 或 complex，不要解释：\n\n" + text

	payload := map[string]any{
		"model":      model,
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
	req.Header.Set("Authorization", "Bearer "+token)
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
	return fmt.Sprintf("分类耗时 %s · 选中 %s", formatElapsed(res.Elapsed), res.Model)
}
