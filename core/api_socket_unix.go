//go:build !windows

// api_socket_unix.go — grant the API control socket to the run_as_user
// agent(s).
//
// The daemon creates the socket 0600 owned by the process user (root when a
// project uses run_as_user, since spawning via `sudo -iu` requires a root
// supervisor). The agent then runs as a different, unprivileged user and gets
// EACCES on connect(), so `send`/`cron`/`timer`/`relay` all fail (issue #1527).
//
// Unlike the non-secret system-prompt file (#1429, fixed by chmod 0644), this
// socket accepts control requests, so it must NOT be world-accessible. We open
// it to exactly the configured agent user(s):
//
//   - one target  -> chown owner to that user, keep 0600 (only that user + root)
//   - N targets    -> if they share a group we can confirm is narrow (see
//                      isNarrowSharedGroup), chgrp to it + 0660; otherwise
//                      refuse and leave the socket 0600 (never silently widen)
//   - no targets   -> leave untouched (default deployment is unchanged)
//
// Caveat on the N-target path: 0660 grants access to every member of the shared
// group, and we cannot enumerate group membership portably. "Narrow" therefore
// means "not a known system/shared group" (gid range + name denylist), not
// "contains only the agent users" — an operator who adds another account to
// that group grants it control-socket access. The single-target path (the
// common case, and the one issue #1527 hits) has no such caveat: 0600 owned by
// the agent restricts to that user plus root.
package core

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
	"strings"
)

// userIdent is the resolved Unix identity of a run_as_user target.
type userIdent struct {
	uid   int
	gid   int    // primary group id
	group string // primary group name
}

// userLookupFunc resolves a username to its Unix identity. Abstracted so the
// permission-planning logic is testable without real OS users.
type userLookupFunc func(username string) (userIdent, error)

// socketAccess is the ownership/mode change to apply to the socket. A uid or
// gid of -1 means "leave unchanged"; a mode of 0 means "leave mode unchanged".
type socketAccess struct {
	uid  int
	gid  int
	mode os.FileMode
}

// minSharedGID is the lowest group id we treat as a plausibly-narrow,
// application-created group. Groups below it are system/shared groups
// (root=0, wheel, staff=20, users=100, ...) that must never gate a 0660
// control socket. 1000 is the conventional Linux GID_MIN; on macOS an
// operator wanting the multi-user path must likewise use a >=1000 group.
const minSharedGID = 1000

// broadGroups are well-known shared groups refused by name as a second line
// of defence behind the gid-range check in isNarrowSharedGroup. Names are a
// belt-and-suspenders signal, not the primary guard — see that function.
var broadGroups = map[string]bool{
	"root": true, "wheel": true, "sudo": true, "admin": true, "adm": true,
	"staff": true, "users": true, "daemon": true, "bin": true, "sys": true,
	"nogroup": true, "nobody": true, "docker": true, "www-data": true,
}

// isNarrowSharedGroup reports whether a group shared by multiple run_as_users
// is safe to expose the socket to at 0660. It fails closed: we can only
// positively confirm narrowness from the gid range and a name denylist, not
// from membership (Go's os/user cannot enumerate group members portably), so
// anything we cannot confirm is treated as broad and refused. This does NOT
// prove the group contains only the agent users — an operator who adds a third
// account to a narrow group grants it socket access (documented at file head).
func isNarrowSharedGroup(gid int, group string) bool {
	if group == "" {
		return false // unresolved group name — cannot verify, refuse
	}
	if gid < minSharedGID || gid >= 65534 {
		return false // system/shared groups and the nobody/nogroup sentinels
	}
	return !broadGroups[strings.ToLower(group)]
}

func lookupUnixUser(username string) (userIdent, error) {
	u, err := user.Lookup(username)
	if err != nil {
		return userIdent{}, err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return userIdent{}, fmt.Errorf("parse uid %q: %w", u.Uid, err)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return userIdent{}, fmt.Errorf("parse gid %q: %w", u.Gid, err)
	}
	group := ""
	if g, err := user.LookupGroupId(u.Gid); err == nil {
		group = g.Name
	}
	return userIdent{uid: uid, gid: gid, group: group}, nil
}

// planSocketAccess decides how to open the socket to the given run_as_user
// targets. It never returns a plan that widens access beyond the target
// user(s); ambiguous multi-user cases return an error so the caller leaves the
// socket restricted.
func planSocketAccess(runAsUsers []string, lookup userLookupFunc) (socketAccess, error) {
	none := socketAccess{uid: -1, gid: -1, mode: 0}
	switch len(runAsUsers) {
	case 0:
		return none, nil
	case 1:
		id, err := lookup(runAsUsers[0])
		if err != nil {
			return none, fmt.Errorf("resolve run_as_user %q: %w", runAsUsers[0], err)
		}
		// Owner becomes the agent; 0600 already restricts to owner + root.
		return socketAccess{uid: id.uid, gid: -1, mode: 0}, nil
	default:
		gid := -1
		group := ""
		for _, name := range runAsUsers {
			id, err := lookup(name)
			if err != nil {
				return none, fmt.Errorf("resolve run_as_user %q: %w", name, err)
			}
			if gid == -1 {
				gid, group = id.gid, id.group
				continue
			}
			if id.gid != gid {
				return none, fmt.Errorf("run_as_users do not share a primary group (%q vs gid %d); set a shared group and socket perms explicitly", name, gid)
			}
		}
		if !isNarrowSharedGroup(gid, group) {
			return none, fmt.Errorf("run_as_users share group %q (gid %d) that is not a confirmed narrow group; refusing to expose control socket to it — set socket perms explicitly", group, gid)
		}
		return socketAccess{uid: -1, gid: gid, mode: 0o660}, nil
	}
}

// grantSocketAccess opens the socket at sockPath to the configured run_as_user
// agent(s) per planSocketAccess. Callers treat a returned error as non-fatal
// (the socket still works for root); it just means the agent can't connect.
func grantSocketAccess(sockPath string, runAsUsers []string) error {
	acc, err := planSocketAccess(runAsUsers, lookupUnixUser)
	if err != nil {
		return err
	}
	if acc.uid != -1 || acc.gid != -1 {
		if err := os.Chown(sockPath, acc.uid, acc.gid); err != nil {
			return fmt.Errorf("chown socket: %w", err)
		}
	}
	if acc.mode != 0 {
		if err := os.Chmod(sockPath, acc.mode); err != nil {
			return fmt.Errorf("chmod socket: %w", err)
		}
	}
	return nil
}
