package feishu

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// askqClaimTTL bounds how long an ask-question card stays claimable after it
// was issued. A pending AskUserQuestion rarely outlives a few minutes; 30
// minutes is generous while still guaranteeing the registry cannot accumulate
// unbounded stale tokens.
const askqClaimTTL = 30 * time.Minute

// askqMaxAnswerRunes is the server-side limit on a free-form answer. The
// card's max_length enforces the same number client-side, but Feishu treats
// it as advisory: a form_submit carrying 501 CJK characters is delivered and
// would otherwise be forwarded to the agent verbatim.
const askqMaxAnswerRunes = 500

// askqClaim is the server-side identity of one ask-question card: which exact
// session (including its thread, under thread_isolation) the question was
// asked in, which question batch it is, and whether its answer callback has
// already been consumed.
type askqClaim struct {
	sessionKey    string // the exact originating session, e.g. feishu:oc_xxx:root:om_yyy
	chatID        string // the chat the card was rendered in
	qIdx          int    // question batch, to detect cards superseded by a newer ask
	question      string // for the confirmation card
	issuedBy      string // operator open_id, recorded for auditing
	issuedAt      time.Time
	consumed      bool
	expiredNotice string // localized rejection text, captured at send time
	tooLongNotice string // localized rejection text, captured at send time
}

// askqRegistry mints one-time tokens for ask-question cards and validates
// them on callback. Tokens are opaque (128-bit random hex) and never encode
// meaning, so a forged or reused value can only fail the lookup.
//
// A session has at most one pending question: minting for a sessionKey
// supersedes every earlier live token for that session, whatever the question
// batch — so a card left unanswered (because the user replied by typing
// instead) can never answer the next question either.
type askqRegistry struct {
	mu    sync.Mutex
	live  map[string]*askqClaim // token -> claim (only latest per session)
	index map[string]string     // sessionKey -> live token
}

func newAskqRegistry() *askqRegistry {
	return &askqRegistry{
		live:  make(map[string]*askqClaim),
		index: make(map[string]string),
	}
}

// mint registers a fresh claim for the session and returns its token. Any
// previously live token for the session is dropped, superseding every older
// card. The localized rejection notices are captured here because the card
// callback arrives on a path with no i18n context. Expired entries are swept
// opportunistically so the registry stays bounded without a background
// goroutine.
func (r *askqRegistry) mint(sessionKey, chatID string, qIdx int, question, issuedBy, expiredNotice, tooLongNotice string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is effectively fatal; degrade to a
		// time-derived token rather than disabling the card flow.
		now := time.Now().UnixNano()
		for i := 0; i < 16; i++ {
			b[i] = byte(now >> (uint(i%8) * 8))
		}
	}
	token := hex.EncodeToString(b[:])

	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.index[sessionKey]; ok {
		// Keep the superseded claim (marked consumed) rather than deleting it:
		// a late callback on the old card still finds its localized rejection
		// notice. The TTL sweep reclaims it.
		if c, ok := r.live[old]; ok {
			c.consumed = true
		}
	}
	r.sweepLocked()
	r.index[sessionKey] = token
	r.live[token] = &askqClaim{
		sessionKey:    sessionKey,
		chatID:        chatID,
		qIdx:          qIdx,
		question:      question,
		issuedBy:      issuedBy,
		issuedAt:      time.Now(),
		expiredNotice: expiredNotice,
		tooLongNotice: tooLongNotice,
	}
	return token
}

// claim atomically consumes the token. The second return is false when the
// token is unknown, expired, already consumed, or superseded by a newer card
// for the same session and question — the caller must reject the callback
// explicitly and must not dispatch anything.
func (r *askqRegistry) claim(token string) (*askqClaim, bool) {
	if token == "" {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.live[token]
	if !ok || c.consumed || time.Since(c.issuedAt) > askqClaimTTL {
		// Leave the entry in place: a rejected callback still peeks the claim
		// to render the localized notice, and the TTL sweep reclaims it.
		return nil, false
	}
	c.consumed = true
	return c, true
}

// peek returns the claim for a token without consuming it, for choosing the
// localized rejection notice on a callback that failed to claim. The claim may
// be absent (unknown token) — the caller falls back to a default notice.
func (r *askqRegistry) peek(token string) (*askqClaim, bool) {
	if token == "" {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.live[token]
	return c, ok
}

// sweepLocked drops expired entries; caller holds mu.
func (r *askqRegistry) sweepLocked() {
	now := time.Now()
	for token, c := range r.live {
		if now.Sub(c.issuedAt) > askqClaimTTL {
			delete(r.live, token)
			if t, ok := r.index[c.sessionKey]; ok && t == token {
				delete(r.index, c.sessionKey)
			}
		}
	}
}
