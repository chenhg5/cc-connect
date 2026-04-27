package codex

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestAppServerSession_ApplyThreadRuntimeState(t *testing.T) {
	s := &appServerSession{}
	effort := "xhigh"

	s.applyThreadRuntimeState("/tmp/project", "gpt-5.4", &effort)

	if got := s.GetWorkDir(); got != "/tmp/project" {
		t.Fatalf("GetWorkDir() = %q, want /tmp/project", got)
	}
	if got := s.GetModel(); got != "gpt-5.4" {
		t.Fatalf("GetModel() = %q, want gpt-5.4", got)
	}
	if got := s.GetReasoningEffort(); got != "xhigh" {
		t.Fatalf("GetReasoningEffort() = %q, want xhigh", got)
	}
}

func TestAppServerCommandArgs_IncludesNativePermissionOverrides(t *testing.T) {
	s := &appServerSession{
		url: "stdio://",
		permissions: codexPermissionOverrides{
			ApprovalPolicy:    "on-request",
			ApprovalsReviewer: "auto_review",
			SandboxMode:       "workspace-write",
		},
	}

	args := s.appServerCommandArgs()

	for _, want := range [][]string{
		{"-c", `approval_policy="on-request"`},
		{"-c", `approvals_reviewer="auto_review"`},
		{"-c", `sandbox_mode="workspace-write"`},
	} {
		if !containsSequence(args, want) {
			t.Fatalf("args missing %v: %v", want, args)
		}
	}
}

func TestAppServerSession_ThreadRequestParamsNativePermissionOverrides(t *testing.T) {
	s := &appServerSession{
		mode: "full-auto",
		permissions: codexPermissionOverrides{
			ApprovalPolicy:    "on-request",
			ApprovalsReviewer: "auto_review",
			SandboxMode:       "read-only",
		},
	}

	params := s.threadRequestParams()

	if got := params["approvalPolicy"]; got != "on-request" {
		t.Fatalf("approvalPolicy = %#v, want on-request", got)
	}
	if got := params["approvalsReviewer"]; got != "auto_review" {
		t.Fatalf("approvalsReviewer = %#v, want auto_review", got)
	}
	if got := params["sandbox"]; got != "read-only" {
		t.Fatalf("sandbox = %#v, want read-only", got)
	}
}

func TestAppServerSession_ThreadRequestParamsPartialNativePermissionOverrides(t *testing.T) {
	tests := []struct {
		name        string
		mode        string
		permissions codexPermissionOverrides
		want        map[string]any
		absent      []string
	}{
		{
			name:        "sandbox only preserves full-auto approval",
			mode:        "full-auto",
			permissions: codexPermissionOverrides{SandboxMode: "read-only"},
			want: map[string]any{
				"approvalPolicy": "never",
				"sandbox":        "read-only",
			},
			absent: []string{"approvalsReviewer"},
		},
		{
			name:        "approval only preserves yolo sandbox",
			mode:        "yolo",
			permissions: codexPermissionOverrides{ApprovalPolicy: "on-request"},
			want: map[string]any{
				"approvalPolicy": "on-request",
				"sandbox":        "danger-full-access",
			},
			absent: []string{"approvalsReviewer"},
		},
		{
			name:        "reviewer only preserves full-auto mode settings",
			mode:        "full-auto",
			permissions: codexPermissionOverrides{ApprovalsReviewer: "guardian_subagent"},
			want: map[string]any{
				"approvalPolicy":    "never",
				"sandbox":           "workspace-write",
				"approvalsReviewer": "guardian_subagent",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &appServerSession{
				mode:        tt.mode,
				permissions: tt.permissions,
			}

			params := s.threadRequestParams()

			for key, want := range tt.want {
				if got := params[key]; got != want {
					t.Fatalf("%s = %#v, want %#v; params=%#v", key, got, want, params)
				}
			}
			for _, key := range tt.absent {
				if _, ok := params[key]; ok {
					t.Fatalf("%s present, want absent; params=%#v", key, params)
				}
			}
		})
	}
}

func TestAppServerSession_TurnRequestParamsNativePermissionOverrides(t *testing.T) {
	s := &appServerSession{
		mode:   "full-auto",
		effort: "high",
		permissions: codexPermissionOverrides{
			ApprovalPolicy:    "on-request",
			ApprovalsReviewer: "guardian_subagent",
			SandboxMode:       "workspace-write",
		},
	}

	params, err := s.turnRequestParams("thread-1", []map[string]any{{"type": "text", "text": "hello"}})
	if err != nil {
		t.Fatalf("turnRequestParams: %v", err)
	}

	if got := params["approvalPolicy"]; got != "on-request" {
		t.Fatalf("approvalPolicy = %#v, want on-request", got)
	}
	if got := params["approvalsReviewer"]; got != "guardian_subagent" {
		t.Fatalf("approvalsReviewer = %#v, want guardian_subagent", got)
	}
	sandboxPolicy, ok := params["sandboxPolicy"].(map[string]any)
	if !ok {
		t.Fatalf("sandboxPolicy = %#v, want map", params["sandboxPolicy"])
	}
	if got := sandboxPolicy["type"]; got != "workspaceWrite" {
		t.Fatalf("sandboxPolicy.type = %#v, want workspaceWrite", got)
	}
}

func TestAppServerSession_TurnRequestParamsPartialNativePermissionOverrides(t *testing.T) {
	tests := []struct {
		name              string
		mode              string
		permissions       codexPermissionOverrides
		wantApproval      string
		wantReviewer      string
		wantSandboxPolicy map[string]any
	}{
		{
			name:              "sandbox only preserves full-auto approval",
			mode:              "full-auto",
			permissions:       codexPermissionOverrides{SandboxMode: "read-only"},
			wantApproval:      "never",
			wantSandboxPolicy: map[string]any{"type": "readOnly"},
		},
		{
			name:         "approval only leaves yolo sandbox unchanged",
			mode:         "yolo",
			permissions:  codexPermissionOverrides{ApprovalPolicy: "on-request"},
			wantApproval: "on-request",
		},
		{
			name:         "reviewer only preserves full-auto approval",
			mode:         "full-auto",
			permissions:  codexPermissionOverrides{ApprovalsReviewer: "guardian_subagent"},
			wantApproval: "never",
			wantReviewer: "guardian_subagent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &appServerSession{
				mode:        tt.mode,
				permissions: tt.permissions,
			}

			params, err := s.turnRequestParams("thread-1", []map[string]any{{"type": "text", "text": "hello"}})
			if err != nil {
				t.Fatalf("turnRequestParams: %v", err)
			}

			if got := params["approvalPolicy"]; got != tt.wantApproval {
				t.Fatalf("approvalPolicy = %#v, want %#v; params=%#v", got, tt.wantApproval, params)
			}
			if tt.wantReviewer != "" {
				if got := params["approvalsReviewer"]; got != tt.wantReviewer {
					t.Fatalf("approvalsReviewer = %#v, want %#v; params=%#v", got, tt.wantReviewer, params)
				}
			} else if _, ok := params["approvalsReviewer"]; ok {
				t.Fatalf("approvalsReviewer present, want absent; params=%#v", params)
			}
			if tt.wantSandboxPolicy != nil {
				if got := params["sandboxPolicy"]; !mapsEqual(got, tt.wantSandboxPolicy) {
					t.Fatalf("sandboxPolicy = %#v, want %#v; params=%#v", got, tt.wantSandboxPolicy, params)
				}
			} else if _, ok := params["sandboxPolicy"]; ok {
				t.Fatalf("sandboxPolicy present, want omitted to keep thread sandbox; params=%#v", params)
			}
		})
	}
}

func TestAppServerSession_TurnRequestParamsRejectsUnknownSandboxMode(t *testing.T) {
	s := &appServerSession{
		permissions: codexPermissionOverrides{SandboxMode: "container"},
	}

	_, err := s.turnRequestParams("thread-1", []map[string]any{{"type": "text", "text": "hello"}})
	if err == nil {
		t.Fatal("turnRequestParams() error = nil, want unsupported sandbox_mode error")
	}
	if !strings.Contains(err.Error(), "unsupported codex sandbox_mode") {
		t.Fatalf("turnRequestParams() error = %v, want unsupported sandbox_mode", err)
	}
}

func mapsEqual(got any, want map[string]any) bool {
	gotMap, ok := got.(map[string]any)
	if !ok || len(gotMap) != len(want) {
		return false
	}
	for key, wantValue := range want {
		if gotMap[key] != wantValue {
			return false
		}
	}
	return true
}

func TestAppServerSession_HandleRateLimitsUpdatedCachesUsage(t *testing.T) {
	s := &appServerSession{}
	raw, err := json.Marshal(appServerRateLimitsResponse{
		RateLimits: appServerRateLimitSnapshot{
			LimitID:   "codex",
			PlanType:  "pro",
			Primary:   &appServerRateLimitWindow{UsedPercent: 25, WindowDurationMins: 15, ResetsAt: 1730947200},
			Secondary: &appServerRateLimitWindow{UsedPercent: 42, WindowDurationMins: 60, ResetsAt: 1730950800},
			Credits:   &appServerCreditsSnapshot{HasCredits: true, Unlimited: false},
		},
	})
	if err != nil {
		t.Fatalf("marshal notification: %v", err)
	}

	s.handleNotification("account/rateLimits/updated", raw)

	report, err := s.GetUsage(context.Background())
	if err != nil {
		t.Fatalf("GetUsage() returned error: %v", err)
	}
	if report.Provider != "codex" {
		t.Fatalf("provider = %q, want codex", report.Provider)
	}
	if report.Plan != "pro" {
		t.Fatalf("plan = %q, want pro", report.Plan)
	}
	if len(report.Buckets) != 1 {
		t.Fatalf("buckets = %d, want 1", len(report.Buckets))
	}
	if got := report.Buckets[0].Name; got != "codex" {
		t.Fatalf("bucket name = %q, want codex", got)
	}
	if got := report.Buckets[0].Windows[0].WindowSeconds; got != 15*60 {
		t.Fatalf("primary window seconds = %d, want %d", got, 15*60)
	}
	if got := report.Buckets[0].Windows[1].UsedPercent; got != 42 {
		t.Fatalf("secondary used percent = %d, want 42", got)
	}
	if report.Credits == nil || !report.Credits.HasCredits {
		t.Fatalf("credits = %#v, want has credits", report.Credits)
	}
}

func TestAppServerSession_HandleThreadTokenUsageUpdatedCachesContextUsage(t *testing.T) {
	s := &appServerSession{}
	raw, err := json.Marshal(appServerThreadTokenUsageNotification{
		ThreadID: "thread-1",
		TurnID:   "turn-1",
		TokenUsage: struct {
			Total              codexTokenUsage `json:"total"`
			Last               codexTokenUsage `json:"last"`
			ModelContextWindow int             `json:"modelContextWindow"`
		}{
			Total: codexTokenUsage{
				TotalTokens:           52011395,
				InputTokens:           51847383,
				CachedInputTokens:     48187904,
				OutputTokens:          164012,
				ReasoningOutputTokens: 78910,
			},
			Last: codexTokenUsage{
				TotalTokens:           41061,
				InputTokens:           40849,
				CachedInputTokens:     36864,
				OutputTokens:          212,
				ReasoningOutputTokens: 32,
			},
			ModelContextWindow: 258400,
		},
	})
	if err != nil {
		t.Fatalf("marshal notification: %v", err)
	}

	s.handleNotification("thread/tokenUsage/updated", raw)

	usage := s.GetContextUsage()
	if usage == nil {
		t.Fatal("GetContextUsage() = nil, want cached context usage")
	}
	if usage.UsedTokens != 41061 {
		t.Fatalf("used tokens = %d, want 41061", usage.UsedTokens)
	}
	if usage.BaselineTokens != codexContextBaselineTokens {
		t.Fatalf("baseline tokens = %d, want %d", usage.BaselineTokens, codexContextBaselineTokens)
	}
	if usage.TotalTokens != 41061 {
		t.Fatalf("total tokens = %d, want 41061", usage.TotalTokens)
	}
	if usage.ContextWindow != 258400 {
		t.Fatalf("context window = %d, want 258400", usage.ContextWindow)
	}
	if usage.CachedInputTokens != 36864 {
		t.Fatalf("cached input tokens = %d, want 36864", usage.CachedInputTokens)
	}
	if usage.InputTokens != 40849 {
		t.Fatalf("input tokens = %d, want 40849", usage.InputTokens)
	}
}

func TestMapAppServerRateLimits_PrefersMultiBucketView(t *testing.T) {
	report := mapAppServerRateLimits(appServerRateLimitsResponse{
		RateLimits: appServerRateLimitSnapshot{
			LimitID:  "legacy",
			PlanType: "team",
			Primary:  &appServerRateLimitWindow{UsedPercent: 99, WindowDurationMins: 15},
		},
		RateLimitsByLimitID: map[string]appServerRateLimitSnapshot{
			"codex": {
				LimitID:   "codex",
				LimitName: "Codex",
				PlanType:  "team",
				Primary:   &appServerRateLimitWindow{UsedPercent: 10, WindowDurationMins: 15},
			},
			"codex_other": {
				LimitID:  "codex_other",
				PlanType: "team",
				Primary:  &appServerRateLimitWindow{UsedPercent: 20, WindowDurationMins: 60},
			},
		},
	})

	if report.Plan != "team" {
		t.Fatalf("plan = %q, want team", report.Plan)
	}
	if len(report.Buckets) != 2 {
		t.Fatalf("buckets = %d, want 2", len(report.Buckets))
	}
	if report.Buckets[0].Name != "Codex" {
		t.Fatalf("first bucket = %q, want Codex", report.Buckets[0].Name)
	}
	if report.Buckets[1].Name != "codex_other" {
		t.Fatalf("second bucket = %q, want codex_other", report.Buckets[1].Name)
	}
}

var _ interface {
	GetUsage(context.Context) (*core.UsageReport, error)
} = (*appServerSession)(nil)

var _ interface {
	GetContextUsage() *core.ContextUsage
} = (*appServerSession)(nil)
