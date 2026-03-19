//go:build performance

// Package performance contains benchmark tests for cc-connect.
// These tests measure latency, throughput, and resource usage.
//
// Run with: go test -bench=. -benchmem -tags=performance ./tests/performance/...
package performance

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/chenhg5/cc-connect/tests/mocks/fake"
)

// ---------------------------------------------------------------------------
// T-400: Single Message Latency
// ---------------------------------------------------------------------------

func Benchmark_SingleMessageLatency(b *testing.B) {
	ctx := context.Background()
	agent := fake.NewFakeAgent("bench-agent")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sess, _ := agent.StartSession(ctx, "bench-session")
		sess.Send("Hello", nil, nil)
		for e := range sess.Events() {
			if e.Done {
				break
			}
		}
	}
}

// ---------------------------------------------------------------------------
// T-401: Concurrent Throughput
// ---------------------------------------------------------------------------

func Benchmark_ConcurrentThroughput(b *testing.B) {
	ctx := context.Background()
	numAgents := 10

	// Create multiple agents with sessions
	agents := make([]struct {
		agent *fake.FakeAgent
		sess  core.AgentSession
	}, numAgents)
	for i := 0; i < numAgents; i++ {
		agent := fake.NewFakeAgent("bench-agent")
		sess, _ := agent.StartSession(ctx, "bench-session")
		agents[i] = struct {
			agent *fake.FakeAgent
			sess  core.AgentSession
		}{agent: agent, sess: sess}
	}

	var totalMessages int64

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		idx := 0
		for pb.Next() {
			a := &agents[idx%numAgents]
			a.sess.Send("Concurrent message", nil, nil)
			atomic.AddInt64(&totalMessages, 1)
			idx++
		}
	})

	b.StopTimer()
	msgsPerSec := float64(totalMessages) / b.Elapsed().Seconds()
	b.ReportMetric(msgsPerSec, "msgs/sec")
}

// ---------------------------------------------------------------------------
// T-402: Session Switch Latency
// ---------------------------------------------------------------------------

func Benchmark_SessionSwitch(b *testing.B) {
	ctx := context.Background()
	agent := fake.NewFakeAgent("bench-agent")

	// Pre-create multiple sessions
	const numSessions = 10
	sessions := make([]core.AgentSession, numSessions)
	for i := 0; i < numSessions; i++ {
		sess, _ := agent.StartSession(ctx, "session-switch")
		sessions[i] = sess
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sess := sessions[i%numSessions]
		sess.Send("Switch test", nil, nil)
	}
}

// ---------------------------------------------------------------------------
// T-403: Memory Usage During Message Processing
// ---------------------------------------------------------------------------

func Benchmark_MemoryUsage(b *testing.B) {
	ctx := context.Background()
	agent := fake.NewFakeAgent("bench-agent")
	sess, _ := agent.StartSession(ctx, "bench-session")

	b.ResetTimer()
	b.ReportMetric(float64(b.N)*32, "bytes/op") // baseline estimate

	for i := 0; i < b.N; i++ {
		sess.Send("Memory test message", nil, nil)
		// Consume events
		for e := range sess.Events() {
			if e.Done {
				break
			}
		}
	}
}

// ---------------------------------------------------------------------------
// T-404: Rate Limiter Performance
// ---------------------------------------------------------------------------

func Benchmark_RateLimiter(b *testing.B) {
	rl := core.NewRateLimiter(1000, time.Second)
	defer rl.Stop()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rl.Allow("bench-user")
	}
}

// ---------------------------------------------------------------------------
// T-405: Message Deduplication Performance
// ---------------------------------------------------------------------------

func Benchmark_MessageDedup(b *testing.B) {
	dedup := &core.MessageDedup{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		msgID := "bench-msg"
		dedup.IsDuplicate(msgID)
	}
}

// ---------------------------------------------------------------------------
// T-406: Command Registry Lookup
// ---------------------------------------------------------------------------

func Benchmark_CommandRegistryLookup(b *testing.B) {
	registry := core.NewCommandRegistry()

	// Add commands
	for i := 0; i < 50; i++ {
		registry.Add("cmd", "Command", "{{1}}", "", "", "bench")
	}
	registry.Add("target", "Target command", "{{1}}", "", "", "bench")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		registry.Resolve("target")
	}
}

// ---------------------------------------------------------------------------
// T-407: Card Rendering Performance
// ---------------------------------------------------------------------------

func Benchmark_CardRendering(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		card := core.NewCard().
			Title("Benchmark Card", "blue").
			Markdown("## Section\nSome content here.").
			Markdown("- Item 1\n- Item 2\n- Item 3").
			Divider().
			Buttons(
				core.PrimaryBtn("Confirm", "act:/confirm"),
				core.DefaultBtn("Cancel", "act:/cancel"),
				core.DangerBtn("Delete", "act:/delete"),
			).
			Note("Footnote text").
			Build()

		_ = card.RenderText()
		_ = card.CollectButtons()
	}
}

// ---------------------------------------------------------------------------
// T-408: Cron Store Operations
// ---------------------------------------------------------------------------

func Benchmark_CronStoreOperations(b *testing.B) {
	tmpDir := b.TempDir()
	store, _ := core.NewCronStore(tmpDir)

	// Pre-populate
	for i := 0; i < 20; i++ {
		store.Add(&core.CronJob{
			ID:          "bench-cron",
			Description: "Bench",
			Prompt:      "Run",
			CronExpr:    "0 9 * * *",
			Enabled:     true,
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		jobs := store.List()
		_ = len(jobs)
		store.SetEnabled("bench-cron", true)
	}
}

// ---------------------------------------------------------------------------
// T-409: Session Creation Overhead
// ---------------------------------------------------------------------------

func Benchmark_SessionCreation(b *testing.B) {
	ctx := context.Background()
	agent := fake.NewFakeAgent("bench-agent")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = agent.StartSession(ctx, "bench-session")
	}
}

// ---------------------------------------------------------------------------
// T-410: Session Send/Receive Overhead
// ---------------------------------------------------------------------------

func Benchmark_SessionSendReceive(b *testing.B) {
	ctx := context.Background()
	agent := fake.NewFakeAgent("bench-agent")
	sess, _ := agent.StartSession(ctx, "bench-session")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sess.Send("Bench message", nil, nil)
		for e := range sess.Events() {
			if e.Done {
				break
			}
		}
	}
}

// ---------------------------------------------------------------------------
// T-411: Role Manager Resolution
// ---------------------------------------------------------------------------

func Benchmark_RoleManagerResolution(b *testing.B) {
	manager := core.NewUserRoleManager()
	manager.Configure("viewer", []core.RoleInput{
		{Name: "admin", UserIDs: []string{"admin1", "admin2"}},
		{Name: "developer", UserIDs: []string{"dev1", "dev2"}},
		{Name: "viewer", UserIDs: []string{"*"}},
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		manager.ResolveRole("admin1")
	}
}

// ---------------------------------------------------------------------------
// T-412: Concurrent Rate Limiter Access
// ---------------------------------------------------------------------------

func Benchmark_ConcurrentRateLimiter(b *testing.B) {
	rl := core.NewRateLimiter(10000, time.Second)
	defer rl.Stop()

	var counter int64
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if rl.Allow("bench-user") {
				atomic.AddInt64(&counter, 1)
			}
		}
	})
	b.ReportMetric(float64(counter), "allowed")
}

// ---------------------------------------------------------------------------
// T-413: Multi-Agent Coordination
// ---------------------------------------------------------------------------

func Benchmark_MultiAgentCoordination(b *testing.B) {
	ctx := context.Background()
	numAgents := 5

	agents := make([]*fake.FakeAgent, numAgents)
	for i := 0; i < numAgents; i++ {
		agents[i] = fake.NewFakeAgent("bench-agent")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var wg sync.WaitGroup
		for j := 0; j < numAgents; j++ {
			wg.Add(1)
			go func(a *fake.FakeAgent) {
				defer wg.Done()
				sess, _ := a.StartSession(ctx, "coord-session")
				sess.Send("Coordinated message", nil, nil)
			}(agents[j])
		}
		wg.Wait()
	}
}
