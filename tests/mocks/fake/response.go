package fake

import (
	"context"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// TestUsageReport creates a test usage report.
func TestUsageReport(provider, accountID, email string) *core.UsageReport {
	return &core.UsageReport{
		Provider:  provider,
		AccountID: accountID,
		UserID:    "test-user",
		Email:     email,
		Plan:      "pro",
		Buckets: []core.UsageBucket{
			{
				Name:          "Standard Requests",
				Allowed:       true,
				LimitReached:  false,
				Windows: []core.UsageWindow{
					{UsedPercent: 45, WindowSeconds: 3600},
				},
			},
		},
		Credits: &core.UsageCredits{
			HasCredits: true,
			Unlimited:  false,
			Balance:    "$12.50",
		},
	}
}

// TestPermissionModeInfo creates a test permission mode info.
func TestPermissionModeInfo(key, name, nameZh, desc, descZh string) core.PermissionModeInfo {
	return core.PermissionModeInfo{
		Key:    key,
		Name:   name,
		NameZh: nameZh,
		Desc:   desc,
		DescZh: descZh,
	}
}

// TestCard creates a test card using the CardBuilder.
func TestCard() *core.Card {
	return core.NewCard().
		Title("Test Card", "blue").
		Markdown("**Test content**").
		Build()
}

// TestCardWithTitle creates a card with a specific title.
func TestCardWithTitle(title string) *core.Card {
	return core.NewCard().
		Title(title, "blue").
		Markdown("**Content**").
		Build()
}

// TestCardWithButtons creates a card with buttons.
func TestCardWithButtons(buttons ...core.CardButton) *core.Card {
	return core.NewCard().
		Title("Test Card", "blue").
		Markdown("Select an option:").
		Buttons(buttons...).
		Build()
}

// TestMessageHandler is a simple message handler for testing.
type TestMessageHandler struct {
	Messages []*core.Message
}

func NewTestMessageHandler() *TestMessageHandler {
	return &TestMessageHandler{
		Messages: make([]*core.Message, 0),
	}
}

func (h *TestMessageHandler) Handle(p core.Platform, msg *core.Message) {
	h.Messages = append(h.Messages, msg)
}

func (h *TestMessageHandler) GetMessages() []*core.Message {
	return h.Messages
}

func (h *TestMessageHandler) Clear() {
	h.Messages = h.Messages[:0]
}

// TestDedupeItem creates a test deduplication item.
type TestDedupeItem struct {
	key        string
	expiration time.Time
}

func NewTestDedupeItem(key string, ttl time.Duration) *TestDedupeItem {
	return &TestDedupeItem{
		key:        key,
		expiration: time.Now().Add(ttl),
	}
}

// TestRateLimiterToken creates a test rate limiter token bucket state.
type TestRateLimiterToken struct {
	tokens    float64
	lastCheck time.Time
}

// TestCronJob creates a test cron job.
func TestCronJob(id, desc, prompt string, cronExpr string) *core.CronJob {
	enabled := true
	return &core.CronJob{
		ID:          id,
		Description: desc,
		Prompt:      prompt,
		CronExpr:    cronExpr,
		Enabled:     enabled,
	}
}

// TestAgentSessionInfoList creates a list of test agent session info.
func TestAgentSessionInfoList(count int) []core.AgentSessionInfo {
	sessions := make([]core.AgentSessionInfo, count)
	for i := 0; i < count; i++ {
		sessions[i] = core.AgentSessionInfo{
			ID:           "session-" + string(rune('0'+i)),
			Summary:      "Test session " + string(rune('0'+i)),
			MessageCount: (i + 1) * 10,
			ModifiedAt:   time.Now().Add(-time.Duration(i) * time.Hour),
		}
	}
	return sessions
}

// TestContext returns a context with timeout for testing.
func TestContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), timeout)
}
