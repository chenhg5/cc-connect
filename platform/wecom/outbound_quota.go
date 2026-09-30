package wecom

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

type quotaLimits struct{ minute, hour int }

type outboundQuotaConfig struct {
	enabled           bool
	messages, uploads quotaLimits
}

func parseOutboundQuota(opts map[string]any) (outboundQuotaConfig, error) {
	cfg := outboundQuotaConfig{messages: quotaLimits{20, 900}, uploads: quotaLimits{20, 900}}
	raw, exists := opts["outbound_quota"]
	if !exists {
		return cfg, nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return cfg, fmt.Errorf("wecom: outbound_quota must be a table")
	}
	for key, value := range values {
		if key == "enabled" {
			cfg.enabled, ok = value.(bool)
			if !ok {
				return cfg, fmt.Errorf("wecom: outbound_quota.enabled must be a boolean")
			}
			continue
		}
		fields := map[string]*int{
			"messages_per_minute": &cfg.messages.minute, "messages_per_hour": &cfg.messages.hour,
			"uploads_per_minute": &cfg.uploads.minute, "uploads_per_hour": &cfg.uploads.hour,
		}
		field, known := fields[key]
		if !known {
			return cfg, fmt.Errorf("wecom: unknown outbound_quota option %q", key)
		}
		n, err := quotaPositiveInt(value)
		if err != nil {
			return cfg, fmt.Errorf("wecom: outbound_quota.%s: %w", key, err)
		}
		*field = n
	}
	return cfg, nil
}

func (cfg outboundQuotaConfig) conflictDetails(existing outboundQuotaConfig) string {
	var differences []string
	if cfg.enabled != existing.enabled {
		differences = append(differences, fmt.Sprintf("enabled=%t (this project) vs %t (existing account)", cfg.enabled, existing.enabled))
	}
	for _, field := range []struct {
		name              string
		current, existing int
	}{
		{"messages_per_minute", cfg.messages.minute, existing.messages.minute},
		{"messages_per_hour", cfg.messages.hour, existing.messages.hour},
		{"uploads_per_minute", cfg.uploads.minute, existing.uploads.minute},
		{"uploads_per_hour", cfg.uploads.hour, existing.uploads.hour},
	} {
		if field.current != field.existing {
			differences = append(differences, fmt.Sprintf("%s=%d (this project) vs %d (existing account)", field.name, field.current, field.existing))
		}
	}
	return strings.Join(differences, "; ")
}

func quotaPositiveInt(value any) (int, error) {
	var n int64
	switch v := value.(type) {
	case int:
		n = int64(v)
	case int64:
		n = v
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 1 || v >= float64(math.MaxInt) || v != math.Trunc(v) {
			return 0, fmt.Errorf("must be a positive integer")
		}
		n = int64(v)
	default:
		return 0, fmt.Errorf("must be a positive integer")
	}
	if n <= 0 || uint64(n) > uint64(math.MaxInt) {
		return 0, fmt.Errorf("must be a positive integer")
	}
	return int(n), nil
}

// IDs and endpoint distinguish accounts; secrets and project names do not.
type quotaAccountKey struct{ mode, endpoint, corpID, appID string }

// Equivalent endpoint spellings must share an account's quota across projects;
// otherwise each spelling would create an independent allowance.
func quotaEndpoint(endpoint string) string {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return strings.TrimSpace(endpoint)
	}
	u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
	u.User, u.Fragment = nil, ""
	if ((u.Scheme == "https" || u.Scheme == "wss") && u.Port() == "443") ||
		((u.Scheme == "http" || u.Scheme == "ws") && u.Port() == "80") {
		u.Host = u.Hostname()
		if strings.Contains(u.Host, ":") {
			u.Host = "[" + u.Host + "]"
		}
	}
	// A custom WS path's trailing slash can select a different endpoint.
	if u.Path == "/" {
		u.Path = ""
	}
	u.RawQuery = u.Query().Encode()
	return u.String()
}

type quotaTarget struct {
	upload    bool
	recipient string
}

type quotaBucket struct {
	history  []time.Time // completion times, ordered under the account mutex
	inflight int         // reservations never expire while a request is in flight
	queue    []*quotaWaiter
	changed  chan struct{} // closed and replaced only while holding account.mu
}

type quotaWaiter struct{ _ byte }

func (b *quotaBucket) notify() { close(b.changed); b.changed = make(chan struct{}) }

func (b *quotaBucket) prune(now time.Time) {
	i := 0
	for i < len(b.history) && !b.history[i].After(now.Add(-time.Hour)) {
		i++
	}
	b.history = slices.Delete(b.history, 0, i)
}

// delay requires BOTH windows to have room. An in-flight reservation is counted
// in both windows until completion; waiting callers consume neither window.
func (b *quotaBucket) delay(now time.Time, limits quotaLimits) (bool, time.Duration) {
	if b.inflight >= limits.minute || b.inflight >= limits.hour {
		return false, 0
	}
	var delay time.Duration
	for _, window := range []struct {
		duration time.Duration
		limit    int
	}{{time.Minute, limits.minute}, {time.Hour, limits.hour}} {
		i := 0
		for i < len(b.history) && !b.history[i].After(now.Add(-window.duration)) {
			i++
		}
		count := len(b.history) - i + b.inflight
		if count >= window.limit {
			// i is the first unexpired record; count also includes in-flight requests.
			// Admit one more request after count-limit+1 completed records expire.
			delay = max(delay, b.history[i+count-window.limit].Add(window.duration).Sub(now))
		}
	}
	return delay == 0, delay
}

type quotaAccount struct {
	mu        sync.Mutex
	cfg       outboundQuotaConfig
	refs      int // protected by registry.mu
	buckets   map[quotaTarget]*quotaBucket
	lastSweep time.Time
}

func (a *quotaAccount) sweep(now time.Time) {
	for key, b := range a.buckets {
		b.prune(now)
		if len(b.history) == 0 && b.inflight == 0 && len(b.queue) == 0 {
			delete(a.buckets, key)
		}
	}
	a.lastSweep = now
}

type quotaRegistry struct {
	mu        sync.Mutex
	accounts  map[quotaAccountKey]*quotaAccount
	lastSweep time.Time
}

var sharedQuotas = &quotaRegistry{}

type outboundQuota struct {
	registry *quotaRegistry
	account  *quotaAccount
	ctx      context.Context
	cancel   context.CancelFunc
	once     sync.Once
}

func (r *quotaRegistry) register(key quotaAccountKey, cfg outboundQuotaConfig) (*outboundQuota, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.accounts == nil {
		r.accounts = make(map[quotaAccountKey]*quotaAccount)
	}
	now := time.Now()
	if now.Sub(r.lastSweep) >= time.Minute {
		for k, a := range r.accounts {
			a.mu.Lock()
			a.sweep(now)
			if a.refs == 0 && len(a.buckets) == 0 {
				delete(r.accounts, k)
			}
			a.mu.Unlock()
		}
		r.lastSweep = now
	}
	a := r.accounts[key]
	if a == nil {
		a = &quotaAccount{cfg: cfg, buckets: make(map[quotaTarget]*quotaBucket)}
		r.accounts[key] = a
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	// Disabled limits have no effect. HTTP does not use upload limits.
	if a.refs > 0 && a.cfg != cfg {
		return nil, fmt.Errorf("wecom: conflicting outbound_quota settings for the same account: %s; configure all projects identically", cfg.conflictDetails(a.cfg))
	}
	a.cfg = cfg
	a.refs++
	ctx, cancel := context.WithCancel(context.Background())
	return &outboundQuota{registry: r, account: a, ctx: ctx, cancel: cancel}, nil
}

func newOutboundQuota(opts map[string]any, key quotaAccountKey) (*outboundQuota, error) {
	cfg, err := parseOutboundQuota(opts)
	if err != nil {
		return nil, err
	}
	if !cfg.enabled {
		cfg.messages, cfg.uploads = quotaLimits{}, quotaLimits{}
	}
	if key.mode == "http" {
		cfg.uploads = quotaLimits{}
	}
	q, err := sharedQuotas.register(key, cfg)
	if err == nil && cfg.enabled {
		slog.Info("wecom: outbound quota enabled", "mode", key.mode, "messages_per_minute", cfg.messages.minute,
			"messages_per_hour", cfg.messages.hour, "uploads_per_minute", cfg.uploads.minute, "uploads_per_hour", cfg.uploads.hour)
	}
	return q, err
}

func (q *outboundQuota) close() {
	if q == nil {
		return
	}
	q.once.Do(func() {
		q.cancel()
		q.registry.mu.Lock()
		q.account.refs--
		q.registry.mu.Unlock()
	})
}

// context ties an entire public send (including uploads and ACKs) to Stop.
// The caller's cancellation and deadline still apply, including while queued;
// callers that need background delivery must provide a suitable parent context.
// Nil receivers support existing zero-value adapters used in tests.
func (q *outboundQuota) context(parent context.Context) (context.Context, context.CancelFunc) {
	if q == nil {
		return context.WithCancel(parent)
	}
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(q.ctx, cancel)
	if q.ctx.Err() != nil {
		cancel()
	}
	return ctx, func() { stop(); cancel() }
}

type quotaPermit struct {
	account *quotaAccount
	bucket  *quotaBucket
	once    sync.Once
}

func (p *quotaPermit) finish(attempted bool) {
	if p == nil {
		return
	}
	p.once.Do(func() {
		p.account.mu.Lock()
		defer p.account.mu.Unlock()
		p.bucket.inflight--
		if attempted {
			p.bucket.history = append(p.bucket.history, time.Now())
		}
		p.bucket.notify()
	})
}

func (q *outboundQuota) acquire(ctx context.Context, target quotaTarget) (*quotaPermit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if q == nil {
		return nil, nil
	}
	if err := q.ctx.Err(); err != nil {
		return nil, err
	}
	a := q.account
	a.mu.Lock()
	if !a.cfg.enabled {
		a.mu.Unlock()
		return nil, nil
	}
	limits := a.cfg.messages
	if target.upload {
		limits = a.cfg.uploads
	}
	if limits.minute <= 0 || limits.hour <= 0 {
		a.mu.Unlock()
		return nil, fmt.Errorf("wecom: quota unavailable for operation")
	}
	if !target.upload && target.recipient == "" {
		a.mu.Unlock()
		return nil, fmt.Errorf("wecom: quota requires a message recipient")
	}
	if now := time.Now(); now.Sub(a.lastSweep) >= time.Minute {
		a.sweep(now)
	}
	b := a.buckets[target]
	if b == nil {
		b = &quotaBucket{changed: make(chan struct{})}
		a.buckets[target] = b
	}
	w := &quotaWaiter{}
	b.queue = append(b.queue, w)
	a.mu.Unlock()
	return q.wait(ctx, a, b, w, limits)
}

func (q *outboundQuota) wait(ctx context.Context, a *quotaAccount, b *quotaBucket, w *quotaWaiter, limits quotaLimits) (*quotaPermit, error) {
	start := time.Now()
	waited := false
	defer func() {
		if waited {
			err := ctx.Err()
			if err == nil {
				err = q.ctx.Err()
			}
			slog.Debug("wecom: quota wait ended", "elapsed", time.Since(start), "error", err)
		}
	}()
	for {
		a.mu.Lock()
		err := ctx.Err()
		if err == nil {
			err = q.ctx.Err()
		}
		if err != nil {
			b.queue = slices.DeleteFunc(b.queue, func(v *quotaWaiter) bool { return v == w })
			b.notify()
			a.mu.Unlock()
			return nil, err
		}
		now := time.Now()
		b.prune(now)
		ready, delay := b.delay(now, limits)
		if b.queue[0] == w && ready {
			b.queue = slices.Delete(b.queue, 0, 1)
			b.inflight++
			b.notify()
			a.mu.Unlock()
			return &quotaPermit{account: a, bucket: b}, nil
		}
		if b.queue[0] != w {
			delay = 0
		}
		changed := b.changed
		a.mu.Unlock()
		if !waited {
			waited = true
			slog.Debug("wecom: waiting for outbound quota")
		}
		var timer *time.Timer
		var tick <-chan time.Time
		if delay > 0 {
			timer = time.NewTimer(delay)
			tick = timer.C
		}
		select {
		case <-ctx.Done():
		case <-q.ctx.Done():
		case <-changed:
		case <-tick:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}
