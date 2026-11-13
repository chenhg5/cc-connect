package feishu

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

const (
	// rootCacheTTL bounds how long a message -> parent fact is trusted.
	rootCacheTTL = 7 * 24 * time.Hour
	// rootCacheMax caps memory; an entry is roughly 100 bytes.
	rootCacheMax = 8192
	// rootWalkMaxDepth stops pathological reply chains and cycles.
	rootWalkMaxDepth = 10
	// rootLookupTimeout is the whole budget for one lookup, retries included,
	// so a slow Feishu cannot stall the event loop.
	rootLookupTimeout = 2 * time.Second
	// After this many consecutive failures the breaker opens and lookups fail
	// fast for the cooldown, then one probe is allowed through.
	rootBreakerThreshold = 3
	rootBreakerCooldown  = 30 * time.Second
)

var errRootLookupBreaker = errors.New("root lookup circuit open")

// rootEntry records the parent of one message. An empty rootID means the
// message has no parent, i.e. it is the top of its reply chain.
type rootEntry struct {
	rootID string
	at     time.Time
}

// Note on recording: cache facts only when a lookup is actually needed, or
// when a topic's first message becomes the root later replies will reference.
// New messages are not cached merely because they could become chain heads.
// Deeper replies always resolve to the topic head, never to themselves, so they
// are not recorded.

// rememberRoot records where a message came from. Callers pass fields the
// Feishu event already carries, so this never costs an API call.
func (p *Platform) rememberRoot(messageID, rootID string) {
	if messageID == "" || messageID == rootID {
		return
	}
	p.rootMu.Lock()
	defer p.rootMu.Unlock()
	p.storeRootLocked(messageID, rootID, time.Now())
}

func (p *Platform) storeRootLocked(messageID, rootID string, now time.Time) {
	if p.rootCache == nil {
		p.rootCache = make(map[string]rootEntry)
	}
	p.rootCache[messageID] = rootEntry{rootID: rootID, at: now}
	if len(p.rootCache) <= rootCacheMax {
		return
	}
	before := len(p.rootCache)
	for id, e := range p.rootCache {
		if now.Sub(e.at) > rootCacheTTL {
			delete(p.rootCache, id)
		}
	}
	for id := range p.rootCache {
		if len(p.rootCache) <= rootCacheMax*3/4 {
			break
		}
		delete(p.rootCache, id)
	}
	slog.Debug(p.tag()+": root cache pruned",
		"before", before, "after", len(p.rootCache), "max", rootCacheMax)
}

func (p *Platform) cachedRoot(messageID string) (rootEntry, bool) {
	p.rootMu.Lock()
	defer p.rootMu.Unlock()
	e, ok := p.rootCache[messageID]
	if !ok {
		return rootEntry{}, false
	}
	if time.Since(e.at) > rootCacheTTL {
		delete(p.rootCache, messageID)
		return rootEntry{}, false
	}
	return e, true
}

// canonicalRoot walks parent links up to the message that started the chain,
// so every message in one topic resolves to the same session even when Feishu
// reports a different immediate root for the first message and the rest.
func (p *Platform) canonicalRoot(ctx context.Context, messageID string) string {
	if messageID == "" {
		return ""
	}
	start := time.Now()
	cur := messageID
	visited := make(map[string]struct{}, 4)
	for depth := 0; depth < rootWalkMaxDepth; depth++ {
		if _, seen := visited[cur]; seen {
			slog.Warn(p.tag()+": root chain cycle detected, using current id",
				"message_id", messageID, "node", cur, "hops", depth,
				"elapsed_ms", time.Since(start).Milliseconds())
			return cur
		}
		visited[cur] = struct{}{}

		e, ok := p.cachedRoot(cur)
		if !ok {
			rootID, err := p.lookupRootID(ctx, cur)
			if err != nil {
				slog.Warn(p.tag()+": root lookup failed, using current id",
					"message_id", cur, "root_of", messageID, "hops", depth,
					"elapsed_ms", time.Since(start).Milliseconds(), "error", err)
				return cur
			}
			e = rootEntry{rootID: rootID}
		}
		if e.rootID == "" {
			return cur
		}
		cur = e.rootID
	}
	slog.Warn(p.tag()+": root chain too deep, using current id",
		"message_id", messageID, "hops", rootWalkMaxDepth,
		"elapsed_ms", time.Since(start).Milliseconds())
	return cur
}

// lookupRootID queries Feishu for one message. Concurrent callers asking for
// the same message wait for the first lookup instead of hitting the API twice.
func (p *Platform) lookupRootID(ctx context.Context, messageID string) (string, error) {
	p.rootMu.Lock()
	if p.rootInflight == nil {
		p.rootInflight = make(map[string]chan struct{})
	}
	if ch, ok := p.rootInflight[messageID]; ok {
		p.rootMu.Unlock()
		slog.Debug(p.tag()+": waiting for in-flight root lookup", "message_id", messageID)
		select {
		case <-ch:
			if e, ok := p.cachedRoot(messageID); ok {
				return e.rootID, nil
			}
			return "", fmt.Errorf("%s: lookup for %s failed", p.tag(), messageID)
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	ch := make(chan struct{})
	p.rootInflight[messageID] = ch
	p.rootMu.Unlock()

	rootID, err := p.fetchRootID(ctx, messageID)

	p.rootMu.Lock()
	if err == nil {
		// Store before closing the channel so waiters can read the result.
		p.storeRootLocked(messageID, rootID, time.Now())
	}
	delete(p.rootInflight, messageID)
	close(ch)
	p.rootMu.Unlock()
	return rootID, err
}

func (p *Platform) fetchRootID(ctx context.Context, messageID string) (string, error) {
	if until, open := p.rootBreakerOpen(); open {
		return "", fmt.Errorf("%s: %w until %s", p.tag(), errRootLookupBreaker, until.Format(time.RFC3339))
	}

	// One budget for the whole lookup: retries and token refresh cannot
	// stretch past it, so an unhealthy Feishu never blocks message handling.
	cctx, cancel := context.WithTimeout(ctx, rootLookupTimeout)
	defer cancel()

	startedAt := time.Now()
	req := larkim.NewGetMessageReqBuilder().MessageId(messageID).Build()
	var rootID string
	err := p.withTransientRetry(cctx, "root_lookup", func() error {
		return p.fetchRootOnce(cctx, req, messageID, &rootID)
	})
	if err != nil {
		if ctx.Err() == nil {
			p.noteRootLookupFailure(messageID, err)
		}
		return "", err
	}
	p.noteRootLookupSuccess()
	slog.Info(p.tag()+": root lookup resolved",
		"message_id", messageID, "root_id", rootID,
		"elapsed_ms", time.Since(startedAt).Milliseconds())
	return rootID, nil
}

func (p *Platform) fetchRootOnce(ctx context.Context, req *larkim.GetMessageReq, messageID string, out *string) error {
	resp, err := p.client.Im.Message.Get(ctx, req)
	if err != nil || (resp != nil && !resp.Success()) {
		if isTenantAccessTokenInvalid(err) || (resp != nil && resp.Code == 99991663) {
			slog.Warn(p.tag()+": root lookup hit invalid tenant token, retrying with a fresh one",
				"message_id", messageID)
			if token, tokenErr := p.fetchFreshTenantAccessToken(ctx); tokenErr == nil {
				resp, err = p.client.Im.Message.Get(ctx, req, larkcore.WithTenantAccessToken(token))
			} else {
				slog.Warn(p.tag()+": root lookup token refresh failed",
					"message_id", messageID, "error", tokenErr)
			}
		}
	}
	if err != nil {
		return fmt.Errorf("%s: get message %s: %w", p.tag(), messageID, err)
	}
	if resp == nil {
		return fmt.Errorf("%s: get message %s: nil response", p.tag(), messageID)
	}
	if !resp.Success() {
		return fmt.Errorf("%s: get message %s failed code=%d msg=%s", p.tag(), messageID, resp.Code, resp.Msg)
	}
	if resp.Data == nil || len(resp.Data.Items) == 0 || resp.Data.Items[0] == nil {
		return fmt.Errorf("%s: get message %s: empty data", p.tag(), messageID)
	}
	rootID := stringValue(resp.Data.Items[0].RootId)
	if rootID == messageID {
		// Defensive: a self-referencing root_id would look like a cycle.
		rootID = ""
	}
	*out = rootID
	return nil
}

func (p *Platform) rootBreakerOpen() (time.Time, bool) {
	p.rootMu.Lock()
	defer p.rootMu.Unlock()
	if p.rootBrokenUntil.IsZero() || time.Now().After(p.rootBrokenUntil) {
		return time.Time{}, false
	}
	return p.rootBrokenUntil, true
}

func (p *Platform) noteRootLookupFailure(messageID string, err error) {
	p.rootMu.Lock()
	p.rootFailStreak++
	open := false
	if p.rootFailStreak >= rootBreakerThreshold || !p.rootBrokenUntil.IsZero() {
		p.rootBrokenUntil = time.Now().Add(rootBreakerCooldown)
		p.rootFailStreak = 0
		open = true
	}
	p.rootMu.Unlock()
	if open {
		slog.Warn(p.tag()+": root lookup circuit opened, failing fast",
			"cooldown", rootBreakerCooldown.String(),
			"message_id", messageID, "error", err)
	}
}

func (p *Platform) noteRootLookupSuccess() {
	p.rootMu.Lock()
	wasBroken := !p.rootBrokenUntil.IsZero()
	p.rootFailStreak = 0
	p.rootBrokenUntil = time.Time{}
	p.rootMu.Unlock()
	if wasBroken {
		slog.Info(p.tag() + ": root lookup circuit closed")
	}
}
