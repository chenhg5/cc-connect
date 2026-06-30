package core

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// NormalizeWorkdirPath returns a stable absolute path for comparing agent
// working directories.
func NormalizeWorkdirPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = filepath.Clean(path)
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return path
}

// WorkdirContains reports whether child is root or under root.
func WorkdirContains(root, child string) bool {
	root = NormalizeWorkdirPath(root)
	child = NormalizeWorkdirPath(child)
	if root == "" || child == "" {
		return false
	}
	if root == child {
		return true
	}
	rel, err := filepath.Rel(root, child)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

// WorkdirRootForCwd returns the most specific project root that contains cwd.
func WorkdirRootForCwd(cwd string, projectRoots []string) string {
	cwd = NormalizeWorkdirPath(cwd)
	var best string
	for _, root := range projectRoots {
		root = NormalizeWorkdirPath(root)
		if WorkdirContains(root, cwd) && len(root) > len(best) {
			best = root
		}
	}
	return best
}

// MatchSessionWorkdir reports whether a session cwd belongs to the requested
// filter. When filterIsProjectRoot is true, nested session directories match.
func MatchSessionWorkdir(sessionCwd, filterCwd string, filterIsProjectRoot bool) bool {
	filterCwd = NormalizeWorkdirPath(filterCwd)
	if filterCwd == "" {
		return true
	}
	cwd := NormalizeWorkdirPath(sessionCwd)
	if cwd == "" {
		return false
	}
	if filterIsProjectRoot {
		return WorkdirContains(filterCwd, cwd)
	}
	return cwd == filterCwd
}

// GroupAgentWorkdirs summarizes sessions by cwd. If projectRoots is provided,
// sessions under a root are grouped by the root and roots are emitted in the
// provider's order before ungrouped workdirs sorted by latest activity.
func GroupAgentWorkdirs(sessions []AgentSessionInfo, projectRoots []string) []AgentWorkdirInfo {
	roots := normalizeUniqueWorkdirRoots(projectRoots)
	byCwd := make(map[string]*AgentWorkdirInfo)
	for _, info := range sessions {
		cwd := NormalizeWorkdirPath(info.Cwd)
		if cwd == "" {
			continue
		}
		groupCwd := WorkdirRootForCwd(cwd, roots)
		if groupCwd == "" {
			groupCwd = cwd
		}
		group := byCwd[groupCwd]
		if group == nil {
			group = &AgentWorkdirInfo{Cwd: groupCwd}
			byCwd[groupCwd] = group
		}
		group.SessionCount++
		group.MessageCount += info.MessageCount
		if info.ModifiedAt.After(group.ModifiedAt) {
			group.ModifiedAt = info.ModifiedAt
			group.LatestSummary = info.Summary
		}
	}

	workdirs := make([]AgentWorkdirInfo, 0, len(byCwd))
	seen := make(map[string]struct{}, len(byCwd))
	for _, root := range roots {
		if info := byCwd[root]; info != nil {
			workdirs = append(workdirs, *info)
			seen[root] = struct{}{}
		}
	}
	var ungrouped []AgentWorkdirInfo
	for cwd, info := range byCwd {
		if _, ok := seen[cwd]; ok {
			continue
		}
		ungrouped = append(ungrouped, *info)
	}
	sort.Slice(ungrouped, func(i, j int) bool {
		return ungrouped[i].ModifiedAt.After(ungrouped[j].ModifiedAt)
	})
	workdirs = append(workdirs, ungrouped...)
	return workdirs
}

func normalizeUniqueWorkdirRoots(projectRoots []string) []string {
	roots := make([]string, 0, len(projectRoots))
	seen := make(map[string]struct{}, len(projectRoots))
	for _, raw := range projectRoots {
		root := NormalizeWorkdirPath(raw)
		if root == "" {
			continue
		}
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		roots = append(roots, root)
	}
	return roots
}
