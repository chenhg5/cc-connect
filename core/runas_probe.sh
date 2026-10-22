#!/bin/sh
#
# runas_probe.sh — leak-audit probe for the run_as_user sandbox.
#
# Invoked by core.RunIsolationProbe via `sudo -n -iu <target> -- /bin/sh -s`
# with this script piped on stdin. Writes a stable, parseable report to
# stdout. Every line is prefixed with a section tag so the Go side can
# read it deterministically without shell quoting hazards.
#
# This script must NOT read or echo any secret material. It reports
# existence and access, never contents.
#
# It is also intentionally written in POSIX /bin/sh, not bash, so it runs
# on macOS and BusyBox-based systems without relying on bashisms.
#
# Environment inputs (set by the Go caller via the shell invocation):
#   CC_PROBE_WORKDIR        — project work_dir (required)
#   CC_PROBE_OTHER_USERS    — space-separated list of other run_as_user
#                             values configured in the same cc-connect
#                             instance, for cross-user denial tests
#   CC_PROBE_SUPERVISOR     — the supervisor Unix username (for denial test)

set -u

emit() {
    # emit TAG VALUE ...
    printf '%s\n' "$*"
}

emit "BEGIN probe-version=1"

# ---------------------------------------------------------------- identity
emit "ID $(id 2>/dev/null || echo unknown)"
emit "WHOAMI $(whoami 2>/dev/null || echo unknown)"
emit "GROUPS $(id -Gn 2>/dev/null || echo unknown)"
emit "UMASK $(umask 2>/dev/null || echo unknown)"
emit "PWD $(pwd 2>/dev/null || echo unknown)"
emit "HOME ${HOME:-unknown}"
emit "SHELL ${SHELL:-unknown}"

# ---------------------------------------------------------------- work_dir
if [ -n "${CC_PROBE_WORKDIR:-}" ]; then
    emit "WORKDIR_PATH ${CC_PROBE_WORKDIR}"
    if [ -d "${CC_PROBE_WORKDIR}" ]; then
        emit "WORKDIR_EXISTS yes"
    else
        emit "WORKDIR_EXISTS no"
    fi
    if [ -r "${CC_PROBE_WORKDIR}" ]; then
        emit "WORKDIR_READABLE yes"
    else
        emit "WORKDIR_READABLE no"
    fi
    if [ -w "${CC_PROBE_WORKDIR}" ]; then
        emit "WORKDIR_WRITABLE yes"
    else
        emit "WORKDIR_WRITABLE no"
    fi
fi

# ----------------------------------------------------- target user's config
# Things the target user is supposed to have in THEIR home. We just check
# existence — not contents.
for f in \
    "${HOME}/.claude/settings.json" \
    "${HOME}/.claude.json" \
    "${HOME}/.claude/plugins" \
    "${HOME}/.pgpass" \
    "${HOME}/keys" \
    "${HOME}/.ssh" \
    "${HOME}/.config/gh"
do
    if [ -e "$f" ]; then
        emit "TARGET_HAS $f"
    else
        emit "TARGET_MISSING $f"
    fi
done

# ------------------------------------------- cross-user denial tests
# For each OTHER configured run_as_user, try to READ a file inside their
# home. Expected outcome: denied. We report DENIED or LEAKED per path.
if [ -n "${CC_PROBE_OTHER_USERS:-}" ]; then
    for other in ${CC_PROBE_OTHER_USERS}; do
        if [ "$other" = "$(whoami)" ]; then
            continue
        fi
        other_home=$(getent passwd "$other" 2>/dev/null | cut -d: -f6)
        if [ -z "$other_home" ]; then
            emit "CROSS_UNKNOWN $other"
            continue
        fi
        for f in \
            "$other_home/.claude/settings.json" \
            "$other_home/.claude.json" \
            "$other_home/.ssh/id_rsa" \
            "$other_home/.ssh/id_ed25519" \
            "$other_home/.pgpass" \
            "$other_home/keys"
        do
            if [ ! -e "$f" ]; then
                emit "CROSS_MISSING ${other} ${f}"
                continue
            fi
            if [ -r "$f" ]; then
                emit "CROSS_LEAKED ${other} ${f}"
            else
                emit "CROSS_DENIED ${other} ${f}"
            fi
        done
    done
fi

# ---------------------------------------------------- supervisor denial
if [ -n "${CC_PROBE_SUPERVISOR:-}" ]; then
    sup=${CC_PROBE_SUPERVISOR}
    if [ "$sup" != "$(whoami)" ]; then
        sup_home=$(getent passwd "$sup" 2>/dev/null | cut -d: -f6)
        if [ -n "$sup_home" ]; then
            for f in \
                "$sup_home/.claude/settings.json" \
                "$sup_home/.claude.json" \
                "$sup_home/.ssh/id_rsa" \
                "$sup_home/.ssh/id_ed25519" \
                "$sup_home/.pgpass"
            do
                if [ ! -e "$f" ]; then
                    emit "SUPERVISOR_MISSING ${f}"
                    continue
                fi
                if [ -r "$f" ]; then
                    emit "SUPERVISOR_LEAKED ${f}"
                else
                    emit "SUPERVISOR_DENIED ${f}"
                fi
            done
        fi
    fi
fi

emit "END probe-version=1"
