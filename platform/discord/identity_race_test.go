package discord

import (
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/bwmarrin/discordgo"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Regression: the Ready callback wrote p.botID/p.appID without holding p.mu
// while MessageCreate, GuildCreate, RegisterCommands, and cacheBotRoleIDForGuild
// read those fields from separate goroutines. discordgo dispatches each event
// in its own goroutine, so -race flagged the field accesses.
//
// The writer goroutine mirrors the fixed Ready handler (write under p.mu.Lock).
// The reader goroutine exercises cacheBotRoleIDForGuild, which must take
// p.mu.RLock before reading p.botID; -race fails loudly if that read is ever
// reverted to an unlocked access.
//
// Run with: go test -race ./platform/discord/ -run TestPlatform_IdentityFields_Race
func TestPlatform_IdentityFields_Race(t *testing.T) {
	// Session whose HTTP client fails instantly so cacheBotRoleIDForGuild
	// never blocks on a network round-trip even when it sees a non-empty botID.
	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	session.Client = &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("no network in race test")
	})}

	p := &Platform{
		token:   "test-token",
		session: session,
		readyCh: make(chan struct{}),
	}
	p.mu.Lock()
	p.botID = "bot-seed"
	p.appID = "app-seed"
	p.mu.Unlock()

	const goroutines = 6
	const iters = 50
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				// Mirror the fixed Ready handler: identity writes under Lock.
				p.mu.Lock()
				p.botID = "bot-123"
				p.appID = "app-456"
				p.mu.Unlock()
			}
		}()
	}

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				// Production code path; must take p.mu.RLock internally.
				// If the RLock around p.botID is ever removed, -race fails.
				p.cacheBotRoleIDForGuild(session, "guild-1", nil)
			}
		}()
	}

	wg.Wait()
}
