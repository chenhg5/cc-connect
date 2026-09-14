package skillroots

import (
	"os"
	"path/filepath"
)

// Find returns nested directories named "skills" under root.
// It treats each returned directory as a skill root; callers still rely on the
// core depth-1 SkillRegistry to register only <root>/<skill>/SKILL.md.
func Find(root string) []string {
	root = filepath.Clean(root)
	if filepath.Base(root) == "skills" && isDir(root) {
		return []string{root}
	}
	var result []string
	seenDirs := map[string]struct{}{}
	seenRoots := map[string]struct{}{}
	walk(root, seenDirs, seenRoots, &result)
	return result
}

func walk(dir string, seenDirs, seenRoots map[string]struct{}, result *[]string) {
	real := realDir(dir)
	if real == "" {
		return
	}
	if _, ok := seenDirs[real]; ok {
		return
	}
	seenDirs[real] = struct{}{}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		child := filepath.Join(dir, entry.Name())
		if !isDir(child) {
			continue
		}
		if entry.Name() == "skills" {
			clean := filepath.Clean(child)
			if _, ok := seenRoots[clean]; !ok {
				seenRoots[clean] = struct{}{}
				*result = append(*result, clean)
			}
			continue
		}
		walk(child, seenDirs, seenRoots, result)
	}
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func realDir(path string) string {
	if !isDir(path) {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(resolved)
}
