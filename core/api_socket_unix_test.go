//go:build !windows

package core

import (
	"net"
	"os"
	"os/user"
	"strconv"
	"syscall"
	"testing"
)

func TestPlanSocketAccess(t *testing.T) {
	// Fake lookup: users a/b share narrow group "cc-agents" (gid 3000);
	// user c is in the broad "staff" group (gid 20); user d is in its own
	// private group (gid 4000); "ghost" does not exist.
	idents := map[string]userIdent{
		"a": {uid: 1001, gid: 3000, group: "cc-agents"},
		"b": {uid: 1002, gid: 3000, group: "cc-agents"},
		"c": {uid: 1003, gid: 20, group: "staff"},
		"d": {uid: 1004, gid: 4000, group: "d"},
	}
	lookup := func(name string) (userIdent, error) {
		id, ok := idents[name]
		if !ok {
			return userIdent{}, os.ErrNotExist
		}
		return id, nil
	}

	tests := []struct {
		name    string
		users   []string
		want    socketAccess
		wantErr bool
	}{
		{
			name:  "no users leaves socket untouched",
			users: nil,
			want:  socketAccess{uid: -1, gid: -1, mode: 0},
		},
		{
			name:  "single user chowns owner, keeps mode",
			users: []string{"a"},
			want:  socketAccess{uid: 1001, gid: -1, mode: 0},
		},
		{
			name:  "shared narrow group chgrps and relaxes to 0660",
			users: []string{"a", "b"},
			want:  socketAccess{uid: -1, gid: 3000, mode: 0o660},
		},
		{
			name:    "shared broad group refuses (no silent widening)",
			users:   []string{"c", "c"},
			wantErr: true,
		},
		{
			name:    "distinct groups refuse",
			users:   []string{"a", "d"},
			wantErr: true,
		},
		{
			name:    "unknown user propagates error",
			users:   []string{"ghost"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := planSocketAccess(tt.users, lookup)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("planSocketAccess(%v) = %+v, want error", tt.users, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("planSocketAccess(%v) unexpected error: %v", tt.users, err)
			}
			if got != tt.want {
				t.Errorf("planSocketAccess(%v) = %+v, want %+v", tt.users, got, tt.want)
			}
		})
	}
}

// newUnixSocket creates a listening unix socket at 0600, mirroring
// NewAPIServer's setup, and returns its path. It chdirs into the temp dir and
// binds a short relative name to stay under the platform's sun_path length
// limit (~104 bytes on macOS), which the long t.TempDir() path would exceed.
func newUnixSocket(t *testing.T) string {
	t.Helper()
	t.Chdir(t.TempDir())
	sock := "api.sock"
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	if err := os.Chmod(sock, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	return sock
}

func statMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	return fi.Mode().Perm()
}

func statUID(t *testing.T, path string) int {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("stat sys not *syscall.Stat_t")
	}
	return int(st.Uid)
}

func TestGrantSocketAccess_NoUsersLeaves0600(t *testing.T) {
	sock := newUnixSocket(t)
	if err := grantSocketAccess(sock, nil); err != nil {
		t.Fatalf("grantSocketAccess: %v", err)
	}
	if mode := statMode(t, sock); mode != 0o600 {
		t.Errorf("mode = %o, want 0600", mode)
	}
}

func TestGrantSocketAccess_SingleUserChownsOwner(t *testing.T) {
	// chown to self is permitted without privileges; chown to a different
	// uid needs root, so we use the current user as the single target.
	cur, err := user.Current()
	if err != nil {
		t.Skipf("cannot resolve current user: %v", err)
	}
	wantUID, _ := strconv.Atoi(cur.Uid)

	sock := newUnixSocket(t)
	if err := grantSocketAccess(sock, []string{cur.Username}); err != nil {
		t.Fatalf("grantSocketAccess: %v", err)
	}
	if uid := statUID(t, sock); uid != wantUID {
		t.Errorf("owner uid = %d, want %d", uid, wantUID)
	}
	if mode := statMode(t, sock); mode != 0o600 {
		t.Errorf("mode = %o, want 0600 (single-user keeps owner-only)", mode)
	}
}

func TestGrantSocketAccess_UnknownUserErrors(t *testing.T) {
	sock := newUnixSocket(t)
	err := grantSocketAccess(sock, []string{"definitely-no-such-user-xyz"})
	if err == nil {
		t.Fatal("grantSocketAccess with unknown user: want error, got nil")
	}
	if mode := statMode(t, sock); mode != 0o600 {
		t.Errorf("mode = %o, want 0600 (unchanged on failure)", mode)
	}
}
