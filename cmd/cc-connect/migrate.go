package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

type migrationReport struct {
	CopiedFiles     int
	SkippedRuntime  int
	SkippedSymlinks int
	DryRun          bool
}

var legacyRuntimeEntries = map[string]struct{}{
	"run":                   {},
	"logs":                  {},
	"daemon.json":           {},
	"cc-connect-daemon.ps1": {},
	"restart_notify":        {},
	"instance.lock":         {},
	"config.toml.lock":      {},
}

func runMigrate(args []string) int {
	return runMigrateCommand(args, os.Stdout, os.Stderr)
}

func runMigrateCommand(args []string, stdout, stderr io.Writer) int {
	home, err := os.UserHomeDir()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "migrate: resolve home directory: %v\n", err)
		return 1
	}
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	source := flags.String("source", filepath.Join(home, ".cc-connect"), "official CC Connect data directory")
	target := flags.String("target", filepath.Join(home, ".cc-connect-next"), "cc-connect-next data directory")
	force := flags.Bool("force", false, "merge into an existing target and overwrite matching files")
	dryRun := flags.Bool("dry-run", false, "validate and report without writing files")
	flags.Usage = func() {
		_, _ = fmt.Fprintln(flags.Output(), "Usage: cc-connect-next migrate [--source DIR] [--target DIR] [--dry-run] [--force]")
		_, _ = fmt.Fprintln(flags.Output(), "Copies configuration and persistent state while excluding daemon, logs, locks, and sockets.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	report, err := migrateLegacyData(expandMigrationPath(*source, home), expandMigrationPath(*target, home), *force, *dryRun)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "migrate: %v\n", err)
		return 1
	}
	writeOutput := func(format string, args ...any) bool {
		if _, err := fmt.Fprintf(stdout, format, args...); err != nil {
			_, _ = fmt.Fprintf(stderr, "migrate: write output: %v\n", err)
			return false
		}
		return true
	}
	verb := "Migrated"
	if report.DryRun {
		verb = "Would migrate"
	}
	if !writeOutput("%s %d persistent files from %s to %s.\n", verb, report.CopiedFiles, *source, *target) {
		return 1
	}
	if report.SkippedRuntime > 0 || report.SkippedSymlinks > 0 {
		if !writeOutput("Skipped %d runtime entries and %d symlinks.\n", report.SkippedRuntime, report.SkippedSymlinks) {
			return 1
		}
	}
	if !report.DryRun {
		if !writeOutput("The official CC Connect installation was not modified or stopped.\n") {
			return 1
		}
		if !writeOutput("Next: cc-connect-next --config %s\n", filepath.Join(expandMigrationPath(*target, home), "config.toml")) {
			return 1
		}
	}
	return 0
}

func migrateLegacyData(source, target string, force, dryRun bool) (migrationReport, error) {
	report := migrationReport{DryRun: dryRun}
	source, err := filepath.Abs(filepath.Clean(source))
	if err != nil {
		return report, fmt.Errorf("resolve source: %w", err)
	}
	target, err = filepath.Abs(filepath.Clean(target))
	if err != nil {
		return report, fmt.Errorf("resolve target: %w", err)
	}
	if pathsOverlap(source, target) {
		return report, fmt.Errorf("source and target must be separate directories")
	}
	info, err := os.Stat(source)
	if err != nil {
		return report, fmt.Errorf("read source directory: %w", err)
	}
	if !info.IsDir() {
		return report, fmt.Errorf("source is not a directory: %s", source)
	}
	if !force {
		nonEmpty, err := directoryHasEntries(target)
		if err != nil {
			return report, err
		}
		if nonEmpty {
			return report, fmt.Errorf("target is not empty: %s (use --force to merge deliberately)", target)
		}
	}

	configPath := filepath.Join(source, "config.toml")
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		return report, fmt.Errorf("read source config: %w", err)
	}
	var parsed map[string]any
	if _, err := toml.Decode(string(configBytes), &parsed); err != nil {
		return report, fmt.Errorf("source config is invalid TOML: %w", err)
	}
	migratedConfig := rewriteMigratedDataDir(configBytes, target)
	parsed = nil
	if _, err := toml.Decode(string(migratedConfig), &parsed); err != nil {
		return report, fmt.Errorf("rewritten config is invalid TOML: %w", err)
	}
	report.CopiedFiles++

	if !dryRun {
		if err := os.MkdirAll(target, 0o700); err != nil {
			return report, fmt.Errorf("create target directory: %w", err)
		}
		if err := os.Chmod(target, 0o700); err != nil {
			return report, fmt.Errorf("secure target directory: %w", err)
		}
		if err := writeMigrationFile(filepath.Join(target, "config.toml"), migratedConfig); err != nil {
			return report, fmt.Errorf("write migrated config: %w", err)
		}
	}

	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if _, skip := legacyRuntimeEntries[parts[0]]; skip || strings.HasSuffix(parts[0], ".lock") {
			if len(parts) == 1 {
				report.SkippedRuntime++
			}
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if rel == "config.toml" {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			report.SkippedSymlinks++
			return nil
		}
		if entry.IsDir() {
			if !dryRun {
				dst := filepath.Join(target, rel)
				if err := os.MkdirAll(dst, 0o700); err != nil {
					return err
				}
				if err := os.Chmod(dst, 0o700); err != nil {
					return err
				}
			}
			return nil
		}
		entryInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if !entryInfo.Mode().IsRegular() {
			report.SkippedRuntime++
			return nil
		}
		report.CopiedFiles++
		if dryRun {
			return nil
		}
		dst := filepath.Join(target, rel)
		if err := copyMigrationFile(path, dst); err != nil {
			return fmt.Errorf("copy %s: %w", rel, err)
		}
		return nil
	})
	if err != nil {
		return report, fmt.Errorf("copy persistent state: %w", err)
	}
	return report, nil
}

func directoryHasEntries(path string) (bool, error) {
	dir, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect target: %w", err)
	}
	defer func() { _ = dir.Close() }()
	if info, err := dir.Stat(); err != nil {
		return false, err
	} else if !info.IsDir() {
		return false, fmt.Errorf("target is not a directory: %s", path)
	}
	_, err = dir.Readdirnames(1)
	if errors.Is(err, io.EOF) {
		return false, nil
	}
	return err == nil, err
}

func pathsOverlap(a, b string) bool {
	contains := func(parent, child string) bool {
		rel, err := filepath.Rel(parent, child)
		if err != nil {
			return false
		}
		return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
	}
	return contains(a, b) || contains(b, a)
}

var topLevelDataDirLine = regexp.MustCompile(`^(\s*data_dir\s*=\s*)(?:"(?:\\.|[^"])*"|'[^']*')(\s*(?:#.*)?)$`)

func rewriteMigratedDataDir(configBytes []byte, target string) []byte {
	lines := strings.Split(string(configBytes), "\n")
	replacement := strconv.Quote(filepath.ToSlash(target))
	replaced := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			break
		}
		if match := topLevelDataDirLine.FindStringSubmatch(line); match != nil {
			lines[i] = match[1] + replacement + match[2]
			replaced = true
			break
		}
	}
	if !replaced {
		lines = append([]string{"data_dir = " + replacement, ""}, lines...)
	}
	return []byte(strings.Join(lines, "\n"))
}

func copyMigrationFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Chmod(target, 0o600)
}

func writeMigrationFile(target string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".config.toml.migrate-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, target)
}

func expandMigrationPath(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		return filepath.Join(home, path[2:])
	}
	return path
}
