package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type migrationReport struct {
	CopiedFiles        int
	ProjectDirectories int
	SkippedRuntime     int
	SkippedSymlinks    int
	SkippedProjects    []migrationSkippedProjectRecord
	SourceDataDir      string
	SourceWorkDir      string
	BackupDir          string
	Backups            []migrationBackupRecord
	ManifestPath       string
	DryRun             bool
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
	skipProjectData := flags.Bool("skip-project-data", false, "do not copy project-local .cc-connect images and attachments")
	runtimeWorkDir := flags.String("runtime-work-dir", "", "official runtime working directory for resolving relative config paths (auto-detected)")
	flags.Usage = func() {
		_, _ = fmt.Fprintln(flags.Output(), "Usage: cc-connect-next migrate [--source DIR] [--target DIR] [--runtime-work-dir DIR] [--dry-run] [--force] [--skip-project-data]")
		_, _ = fmt.Fprintln(flags.Output(), "Copies configuration, the effective data_dir, and project-local state while excluding daemon, logs, locks, and sockets.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	report, err := migrateLegacyDataWithOptions(migrationOptions{
		Source:             expandMigrationPath(*source, home),
		Target:             expandMigrationPath(*target, home),
		Home:               home,
		RuntimeWorkDir:     expandMigrationPath(*runtimeWorkDir, home),
		Force:              *force,
		DryRun:             *dryRun,
		IncludeProjectData: !*skipProjectData,
	})
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
	if len(report.SkippedProjects) > 0 {
		if !writeOutput("Skipped %d optional project-data discovery entries; grant access or repair malformed metadata and rerun before relying on project-local completeness.\n", len(report.SkippedProjects)) {
			return 1
		}
		for _, skipped := range report.SkippedProjects {
			if !writeOutput("  - %s (%s)\n", skipped.Source, skipped.Reason) {
				return 1
			}
		}
	}
	if !writeOutput("Official runtime work_dir: %s. Effective data_dir: %s. Project-local directories: %d.\n", report.SourceWorkDir, report.SourceDataDir, report.ProjectDirectories) {
		return 1
	}
	if !report.DryRun {
		for _, backup := range report.Backups {
			if !writeOutput("Previous target backup: %s -> %s\n", backup.Target, backup.Backup) {
				return 1
			}
		}
		if !writeOutput("Verification manifest: %s\n", report.ManifestPath) {
			return 1
		}
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
	home, err := os.UserHomeDir()
	if err != nil {
		return migrationReport{DryRun: dryRun}, fmt.Errorf("resolve home directory: %w", err)
	}
	return migrateLegacyDataWithOptions(migrationOptions{
		Source:             source,
		Target:             target,
		Home:               home,
		Force:              force,
		DryRun:             dryRun,
		IncludeProjectData: true,
	})
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
