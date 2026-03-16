package core

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// UserRole holds the resolved policy for a single role.
type UserRole struct {
	Name         string
	DisabledCmds map[string]bool // resolved command IDs (including "*" wildcard)
	RateLimitCfg *RateLimitCfg   // nil = no role-specific limit; use global fallback
}

// RoleInput is the configuration data used to build a UserRoleManager.
type RoleInput struct {
	Name             string
	UserIDs          []string
	DisabledCommands []string
	RateLimit        *RateLimitCfg
}

// UserRoleManager resolves user IDs to roles and manages per-role rate limiters.
type UserRoleManager struct {
	mu          sync.RWMutex
	roles       []roleEntry              // ordered list for iteration
	defaultRole string                   // fallback role name
	roleMap     map[string]*UserRole     // role name → resolved policy
	limiters    map[string]*RateLimiter  // role name → shared per-role rate limiter
}

type roleEntry struct {
	roleName string
	userIDs  map[string]bool // normalized user IDs; nil when wildcard
	wildcard bool            // true if user_ids contains "*"
}

// NewUserRoleManager creates an empty manager. Call Configure() to populate.
func NewUserRoleManager() *UserRoleManager {
	return &UserRoleManager{
		roleMap:  make(map[string]*UserRole),
		limiters: make(map[string]*RateLimiter),
	}
}

// Configure replaces the role configuration. Should be called on a fresh manager
// before passing to Engine.SetUserRoles().
func (m *UserRoleManager) Configure(defaultRole string, roles []RoleInput) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Stop any existing limiters
	for _, rl := range m.limiters {
		rl.Stop()
	}

	m.defaultRole = defaultRole
	m.roleMap = make(map[string]*UserRole, len(roles))
	m.limiters = make(map[string]*RateLimiter, len(roles))
	m.roles = make([]roleEntry, 0, len(roles))

	// Sort roles by name for deterministic iteration order
	sorted := make([]RoleInput, len(roles))
	copy(sorted, roles)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	for _, ri := range sorted {
		role := &UserRole{
			Name:         ri.Name,
			DisabledCmds: resolveDisabledCmds(ri.DisabledCommands),
			RateLimitCfg: ri.RateLimit,
		}
		m.roleMap[ri.Name] = role

		entry := roleEntry{roleName: ri.Name}
		for _, uid := range ri.UserIDs {
			if uid == "*" {
				entry.wildcard = true
			} else {
				if entry.userIDs == nil {
					entry.userIDs = make(map[string]bool)
				}
				entry.userIDs[strings.ToLower(uid)] = true
			}
		}
		m.roles = append(m.roles, entry)

		// Create per-role rate limiter if configured
		if ri.RateLimit != nil && ri.RateLimit.MaxMessages > 0 {
			m.limiters[ri.Name] = NewRateLimiter(ri.RateLimit.MaxMessages, ri.RateLimit.Window)
		}
	}
}

// ResolveRole returns the role for a given user ID.
// Resolution order: explicit match → default role → wildcard → nil.
// Nil-receiver safe.
func (m *UserRoleManager) ResolveRole(userID string) *UserRole {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	uid := strings.ToLower(userID)

	// 1. Explicit match in non-wildcard roles
	for _, entry := range m.roles {
		if !entry.wildcard && entry.userIDs[uid] {
			return m.roleMap[entry.roleName]
		}
	}

	// 2. Default role
	if m.defaultRole != "" {
		if role, ok := m.roleMap[m.defaultRole]; ok {
			return role
		}
	}

	// 3. Wildcard role
	for _, entry := range m.roles {
		if entry.wildcard {
			return m.roleMap[entry.roleName]
		}
	}

	return nil
}

// AllowRate checks the per-user rate limit based on the user's role.
// Returns (allowed, handled). handled=false means no role-specific limit
// was found; the caller should fall back to the global limiter.
// Nil-receiver safe.
func (m *UserRoleManager) AllowRate(userID string) (allowed, handled bool) {
	if m == nil {
		return true, false
	}
	role := m.ResolveRole(userID)
	if role == nil || role.RateLimitCfg == nil {
		return true, false
	}
	m.mu.RLock()
	rl := m.limiters[role.Name]
	m.mu.RUnlock()
	if rl == nil {
		return true, false
	}
	return rl.Allow(userID), true
}

// Snapshot returns a serializable representation of the current role configuration.
func (m *UserRoleManager) Snapshot() map[string]any {
	if m == nil {
		return map[string]any{"configured": false}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	roles := make(map[string]any, len(m.roles))
	for _, entry := range m.roles {
		role := m.roleMap[entry.roleName]

		userIDs := make([]string, 0)
		if entry.wildcard {
			userIDs = append(userIDs, "*")
		}
		for id := range entry.userIDs {
			userIDs = append(userIDs, id)
		}
		sort.Strings(userIDs)

		disabledCmds := make([]string, 0, len(role.DisabledCmds))
		for cmd := range role.DisabledCmds {
			disabledCmds = append(disabledCmds, cmd)
		}
		sort.Strings(disabledCmds)

		roleData := map[string]any{
			"user_ids":          userIDs,
			"disabled_commands": disabledCmds,
		}
		if role.RateLimitCfg != nil {
			roleData["rate_limit"] = map[string]any{
				"max_messages": role.RateLimitCfg.MaxMessages,
				"window_secs":  int(role.RateLimitCfg.Window / time.Second),
			}
		}
		roles[entry.roleName] = roleData
	}

	return map[string]any{
		"configured":   true,
		"default_role": m.defaultRole,
		"roles":        roles,
	}
}

// ValidateRoleInputs checks role inputs for consistency: duplicate user IDs,
// multiple wildcards, empty user_ids, and default_role existence.
func ValidateRoleInputs(defaultRole string, roles []RoleInput) error {
	if len(roles) == 0 {
		return fmt.Errorf("no roles defined")
	}
	wildcardCount := 0
	seenUserIDs := make(map[string]string) // userID → role name
	roleNames := make(map[string]bool, len(roles))
	for _, ri := range roles {
		roleNames[ri.Name] = true
		if len(ri.UserIDs) == 0 {
			return fmt.Errorf("role %q has empty user_ids", ri.Name)
		}
		for _, uid := range ri.UserIDs {
			if uid == "*" {
				wildcardCount++
				continue
			}
			if prev, dup := seenUserIDs[uid]; dup {
				return fmt.Errorf("user %q appears in both role %q and %q", uid, prev, ri.Name)
			}
			seenUserIDs[uid] = ri.Name
		}
	}
	if wildcardCount > 1 {
		return fmt.Errorf("wildcard user_ids=[\"*\"] appears in multiple roles")
	}
	if defaultRole != "" {
		if !roleNames[defaultRole] {
			return fmt.Errorf("default_role %q does not match any defined role", defaultRole)
		}
	}
	return nil
}

// Stop terminates all per-role rate limiter goroutines. Nil-receiver safe.
func (m *UserRoleManager) Stop() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, rl := range m.limiters {
		rl.Stop()
	}
}
