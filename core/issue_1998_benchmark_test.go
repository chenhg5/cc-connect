package core

import (
	"strconv"
	"testing"
)

func BenchmarkIssue1998VisibleAgentSessionIDs(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			sm := NewSessionManager("")
			for i := 0; i < n; i++ {
				suffix := strconv.Itoa(i)
				sm.NewSideSession("plain:chat:user"+suffix, "").SetAgentSessionID("agent-"+suffix, "stub")
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				sm.VisibleAgentSessionIDs("plain:chat:user0", false)
			}
		})
	}
}

func BenchmarkIssue1998ActiveIDVisible(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			sm := NewSessionManager("")
			for i := 0; i < n; i++ {
				suffix := strconv.Itoa(i)
				sm.NewSideSession("plain:chat:user"+suffix, "").SetAgentSessionID("agent-"+suffix, "stub")
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				sm.AgentSessionIDVisible("plain:chat:user0", "agent-0", false)
			}
		})
	}
}
