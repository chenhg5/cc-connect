package core

import (
	"sync"
	"testing"
	"time"
)

func testRoles() []RoleInput {
	return []RoleInput{
		{
			Name:             "admin",
			UserIDs:          []string{"admin1", "admin2"},
			DisabledCommands: []string{},
			RateLimit:        &RateLimitCfg{MaxMessages: 50, Window: time.Minute},
		},
		{
			Name:             "member",
			UserIDs:          []string{"*"},
			DisabledCommands: []string{"*"},
			RateLimit:        &RateLimitCfg{MaxMessages: 3, Window: time.Minute},
		},
	}
}

func TestUserRoleManager_ResolveRole_ExactMatch(t *testing.T) {
	m := NewUserRoleManager()
	m.Configure("member", testRoles())

	role := m.ResolveRole("admin1")
	if role == nil || role.Name != "admin" {
		t.Errorf("expected admin role, got %+v", role)
	}
	role = m.ResolveRole("admin2")
	if role == nil || role.Name != "admin" {
		t.Errorf("expected admin role for admin2, got %+v", role)
	}
}

func TestUserRoleManager_ResolveRole_CaseInsensitive(t *testing.T) {
	m := NewUserRoleManager()
	m.Configure("member", testRoles())

	role := m.ResolveRole("ADMIN1")
	if role == nil || role.Name != "admin" {
		t.Errorf("expected case-insensitive match to admin, got %+v", role)
	}
}

func TestUserRoleManager_ResolveRole_WildcardFallback(t *testing.T) {
	m := NewUserRoleManager()
	m.Configure("member", testRoles())

	role := m.ResolveRole("unknown_user")
	if role == nil || role.Name != "member" {
		t.Errorf("expected member (wildcard) role, got %+v", role)
	}
}

func TestUserRoleManager_ResolveRole_DefaultRole(t *testing.T) {
	// No wildcard role; use defaultRole
	m := NewUserRoleManager()
	m.Configure("viewer", []RoleInput{
		{Name: "admin", UserIDs: []string{"admin1"}, DisabledCommands: []string{}},
		{Name: "viewer", UserIDs: []string{"viewer1"}, DisabledCommands: []string{"shell"}},
	})

	role := m.ResolveRole("unknown_user")
	if role == nil || role.Name != "viewer" {
		t.Errorf("expected default role 'viewer', got %+v", role)
	}
}

func TestUserRoleManager_ResolveRole_NoMatch(t *testing.T) {
	m := NewUserRoleManager()
	m.Configure("", []RoleInput{
		{Name: "admin", UserIDs: []string{"admin1"}, DisabledCommands: []string{}},
	})

	role := m.ResolveRole("unknown_user")
	if role != nil {
		t.Errorf("expected nil for unmatched user, got %+v", role)
	}
}

func TestUserRoleManager_DisabledCmdsWildcard(t *testing.T) {
	m := NewUserRoleManager()
	m.Configure("member", testRoles())

	role := m.ResolveRole("regular_user")
	if role == nil {
		t.Fatal("expected member role")
	}
	// Member has disabled_commands = ["*"], should disable all builtins
	for _, bc := range builtinCommands {
		if !role.DisabledCmds[bc.id] {
			t.Errorf("member role should disable %q", bc.id)
		}
	}
}

func TestUserRoleManager_DisabledCmdsEmpty(t *testing.T) {
	m := NewUserRoleManager()
	m.Configure("member", testRoles())

	role := m.ResolveRole("admin1")
	if role == nil {
		t.Fatal("expected admin role")
	}
	if len(role.DisabledCmds) != 0 {
		t.Errorf("admin role should have no disabled commands, got %d", len(role.DisabledCmds))
	}
}

func TestUserRoleManager_AllowRate_RoleSpecific(t *testing.T) {
	m := NewUserRoleManager()
	m.Configure("member", testRoles())

	// Member has max_messages=3
	for i := 0; i < 3; i++ {
		allowed, handled := m.AllowRate("user1")
		if !allowed || !handled {
			t.Errorf("request %d should be allowed, got allowed=%v handled=%v", i+1, allowed, handled)
		}
	}
	// 4th should be blocked
	allowed, handled := m.AllowRate("user1")
	if allowed || !handled {
		t.Errorf("4th request should be blocked, got allowed=%v handled=%v", allowed, handled)
	}

	// Admin has max_messages=50, should still be allowed
	allowed, handled = m.AllowRate("admin1")
	if !allowed || !handled {
		t.Errorf("admin should be allowed, got allowed=%v handled=%v", allowed, handled)
	}
}

func TestUserRoleManager_AllowRate_NoRoleLimit(t *testing.T) {
	m := NewUserRoleManager()
	m.Configure("viewer", []RoleInput{
		{Name: "viewer", UserIDs: []string{"*"}, DisabledCommands: []string{}},
		// No RateLimit on viewer role
	})

	allowed, handled := m.AllowRate("anyone")
	if !allowed || handled {
		t.Errorf("expected allowed=true handled=false for role without rate_limit, got %v %v", allowed, handled)
	}
}

func TestUserRoleManager_AllowRate_Concurrent(t *testing.T) {
	m := NewUserRoleManager()
	m.Configure("member", testRoles())

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.AllowRate("concurrent_user")
		}()
	}
	wg.Wait()
}

func TestUserRoleManager_NilReceiver(t *testing.T) {
	var m *UserRoleManager

	if role := m.ResolveRole("anyone"); role != nil {
		t.Error("nil manager should return nil role")
	}
	allowed, handled := m.AllowRate("anyone")
	if !allowed || handled {
		t.Error("nil manager should return allowed=true handled=false")
	}
	m.Stop() // should not panic
}

func TestUserRoleManager_Stop(t *testing.T) {
	m := NewUserRoleManager()
	m.Configure("member", testRoles())

	m.Stop()
	m.Stop() // idempotent

	// AllowRate should still work (just no cleanup goroutine)
	allowed, handled := m.AllowRate("admin1")
	if !allowed || !handled {
		t.Errorf("should still work after Stop, got allowed=%v handled=%v", allowed, handled)
	}
}

func TestUserRoleManager_Snapshot(t *testing.T) {
	m := NewUserRoleManager()
	m.Configure("member", testRoles())

	snap := m.Snapshot()
	if snap["configured"] != true {
		t.Error("expected configured=true")
	}
	if snap["default_role"] != "member" {
		t.Errorf("expected default_role=member, got %v", snap["default_role"])
	}
	roles, ok := snap["roles"].(map[string]any)
	if !ok || len(roles) != 2 {
		t.Errorf("expected 2 roles, got %v", snap["roles"])
	}
}

func TestUserRoleManager_Snapshot_Nil(t *testing.T) {
	var m *UserRoleManager
	snap := m.Snapshot()
	if snap["configured"] != false {
		t.Error("nil manager snapshot should have configured=false")
	}
}

func TestUserRoleManager_ResolveRole_EmptyUserID(t *testing.T) {
	m := NewUserRoleManager()
	m.Configure("member", testRoles())

	// Empty userID should still resolve to default/wildcard role
	role := m.ResolveRole("")
	if role == nil || role.Name != "member" {
		t.Errorf("empty userID should resolve to default role, got %+v", role)
	}
}

func TestValidateRoleInputs_DuplicateUserIDs(t *testing.T) {
	err := ValidateRoleInputs("admin", []RoleInput{
		{Name: "admin", UserIDs: []string{"user1"}},
		{Name: "member", UserIDs: []string{"user1"}},
	})
	if err == nil {
		t.Error("expected error for duplicate user IDs")
	}
}

func TestValidateRoleInputs_MultipleWildcards(t *testing.T) {
	err := ValidateRoleInputs("admin", []RoleInput{
		{Name: "admin", UserIDs: []string{"*"}},
		{Name: "member", UserIDs: []string{"*"}},
	})
	if err == nil {
		t.Error("expected error for multiple wildcards")
	}
}

func TestValidateRoleInputs_InvalidDefaultRole(t *testing.T) {
	err := ValidateRoleInputs("nonexistent", []RoleInput{
		{Name: "admin", UserIDs: []string{"user1"}},
	})
	if err == nil {
		t.Error("expected error for invalid default_role")
	}
}

func TestValidateRoleInputs_EmptyUserIDs(t *testing.T) {
	err := ValidateRoleInputs("admin", []RoleInput{
		{Name: "admin", UserIDs: []string{}},
	})
	if err == nil {
		t.Error("expected error for empty user_ids")
	}
}

func TestValidateRoleInputs_Valid(t *testing.T) {
	err := ValidateRoleInputs("member", testRoles())
	if err != nil {
		t.Errorf("expected no error for valid config, got %v", err)
	}
}
