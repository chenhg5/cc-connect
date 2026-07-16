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
//   - N targets    -> if they share a narrow (non-broad) primary group, chgrp to
//                      it + 0660; otherwise refuse and leave the socket
//                      restricted (never silently widen access)
//   - no targets   -> leave untouched (default deployment is unchanged)
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

// broadGroups are well-known shared groups that would over-expose a control
// socket if used for 0660 group access. If configured run_as_users share one
// of these as their primary group, we refuse rather than widen access.
var broadGroups = map[string]bool{
	"root": true, "wheel": true, "sudo": true, "admin": true, "adm": true,
	"staff": true, "users": true, "daemon": true, "bin": true, "sys": true,
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
		if broadGroups[strings.ToLower(group)] || gid == 0 {
			return none, fmt.Errorf("run_as_users share broad primary group %q (gid %d); refusing to expose control socket to it", group, gid)
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
