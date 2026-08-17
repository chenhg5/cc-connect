package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	githubRepo   = "timmyagentic/cc-connect-next"
	githubAPI    = "https://api.github.com/repos/" + githubRepo + "/releases/latest"
	downloadBase = "https://github.com/" + githubRepo + "/releases/download"
)

// cachedLatestVersion 缓存最新版本信息，避免频繁请求API
var cachedLatestVersion struct {
	version   string
	timestamp time.Time
	mu        sync.RWMutex
}

// versionCheckTTL 缓存有效期（1小时）
const versionCheckTTL = time.Hour

type githubRelease struct {
	TagName    string `json:"tag_name"`
	HTMLURL    string `json:"html_url"`
	Prerelease bool   `json:"prerelease"`
}

type updateInstallKind string

const (
	updateInstallStandalone updateInstallKind = "standalone"
	updateInstallNPM        updateInstallKind = "npm"
)

type updateInstallation struct {
	Kind           updateInstallKind
	ExecutablePath string
	PackageDir     string
	NPMPrefix      string
}

type commandRunner func(name string, args ...string) error
type installedVersionVerifier func(path, tag string) error

var stableReleaseTagPattern = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)

// fetchLatestStableReleaseAsync asynchronously fetches the latest stable
// release from the cc-connect-next GitHub repository.
func fetchLatestStableReleaseAsync() {
	go func() {
		release, err := fetchLatestStableRelease()
		if err != nil || release == nil {
			return
		}
		// 缓存结果
		cachedLatestVersion.mu.Lock()
		cachedLatestVersion.version = release.TagName
		cachedLatestVersion.timestamp = time.Now()
		cachedLatestVersion.mu.Unlock()
	}()
}

// checkUpdateAsync 启动异步版本检查（不阻塞）
func checkUpdateAsync() {
	// dev版本不检查
	if version == "dev" || version == "" {
		return
	}
	fetchLatestStableReleaseAsync()
}

// getUpdateHintIfAvailable returns an update hint only from cache (never blocks on network).
// Call checkUpdateAsync() early to populate the cache in the background.
func getUpdateHintIfAvailable() string {
	if version == "dev" || version == "" {
		return ""
	}

	cachedLatestVersion.mu.RLock()
	cachedVer := cachedLatestVersion.version
	cachedTime := cachedLatestVersion.timestamp
	cachedLatestVersion.mu.RUnlock()

	if cachedVer == "" || time.Since(cachedTime) > versionCheckTTL {
		// Cache miss or expired — trigger async refresh, don't block
		fetchLatestStableReleaseAsync()
		return ""
	}

	if isNewer(cachedVer, version) {
		return fmt.Sprintf("\n📦 Update available: %s → %s  (run: cc-connect-next update)\n", version, cachedVer)
	}
	return ""
}

func runUpdate() {
	if len(os.Args) == 3 && (os.Args[2] == "--help" || os.Args[2] == "-h") {
		fmt.Println("Usage: cc-connect-next update\n\nUpdates to the latest stable release. The installation method is detected automatically.")
		return
	}
	if err := runStableUpdate(os.Args[2:]); err != nil {
		fmt.Fprintf(os.Stderr, "Update failed: %v\n", err)
		os.Exit(1)
	}
}

func runStableUpdate(args []string) error {
	if err := validateStableUpdateArgs(args); err != nil {
		return err
	}

	fmt.Printf("cc-connect-next %s\n", version)
	fmt.Println("Checking for stable updates...")

	release, err := fetchLatestStableRelease()
	if err != nil {
		return fmt.Errorf("check stable release: %w", err)
	}
	if err := validateStableRelease(release); err != nil {
		return err
	}

	latest := release.TagName
	if !isNewer(latest, version) {
		fmt.Printf("Already up to date (%s >= %s).\n", version, latest)
		return nil
	}

	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate current binary: %w", err)
	}
	installation, err := detectUpdateInstallation(execPath)
	if err != nil {
		return err
	}

	fmt.Printf("New stable version available: %s → %s\n", version, latest)
	switch installation.Kind {
	case updateInstallNPM:
		fmt.Printf("Detected npm installation at %s.\n", installation.PackageDir)
		if err := updateNPMInstallation(installation, latest, runExternalCommand, verifyInstalledVersion); err != nil {
			return err
		}
	case updateInstallStandalone:
		fmt.Printf("Detected standalone binary at %s.\n", installation.ExecutablePath)
		if err := updateStandaloneInstallation(installation, latest); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported installation method %q", installation.Kind)
	}

	fmt.Printf("Updated to %s\n", latest)
	fmt.Println("Restart cc-connect-next to use the new version.")
	return nil
}

// fetchLatestStableRelease fetches the latest stable release (no pre-releases).
func fetchLatestStableRelease() (*githubRelease, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	req, _ := http.NewRequest("GET", githubAPI, nil)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := client.Do(req)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == 200 {
			var release githubRelease
			if err := json.NewDecoder(resp.Body).Decode(&release); err == nil {
				return &release, nil
			}
		}
	}

	// Fallback: follow redirect from /releases/latest to extract tag
	latestURL := "https://github.com/" + githubRepo + "/releases/latest"
	noRedirect := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp2, err := noRedirect.Get(latestURL)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp2.Body.Close()

	loc := resp2.Header.Get("Location")
	if loc == "" {
		return nil, fmt.Errorf("no release found")
	}
	parts := strings.Split(loc, "/tag/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("unexpected redirect: %s", loc)
	}
	return &githubRelease{TagName: parts[1], HTMLURL: loc}, nil
}

func validateStableUpdateArgs(args []string) error {
	if len(args) == 0 {
		return nil
	}
	for _, arg := range args {
		if arg == "--pre" || arg == "--beta" {
			return fmt.Errorf("cc-connect-next update installs stable releases only; use npm install -g cc-connect-next@beta to opt into prereleases")
		}
	}
	return fmt.Errorf("unknown update option %q; cc-connect-next update accepts no options and installs the latest stable release", args[0])
}

func validateStableRelease(release *githubRelease) error {
	if release == nil {
		return fmt.Errorf("stable release response was empty")
	}
	tag := strings.TrimSpace(release.TagName)
	if release.Prerelease || !stableReleaseTagPattern.MatchString(tag) {
		return fmt.Errorf("refusing non-stable release %q", release.TagName)
	}
	return nil
}

func detectUpdateInstallation(execPath string) (updateInstallation, error) {
	if strings.TrimSpace(execPath) == "" {
		return updateInstallation{}, fmt.Errorf("current executable path is empty")
	}

	resolvedPath := execPath
	if resolved, err := filepath.EvalSymlinks(execPath); err == nil {
		resolvedPath = resolved
	}
	installation := updateInstallation{
		Kind:           updateInstallStandalone,
		ExecutablePath: resolvedPath,
	}

	binDir := filepath.Dir(resolvedPath)
	if filepath.Base(binDir) != "bin" {
		return installation, nil
	}
	packageDir := filepath.Dir(binDir)
	packageJSONPath := filepath.Join(packageDir, "package.json")
	data, err := os.ReadFile(packageJSONPath)
	if os.IsNotExist(err) {
		return installation, nil
	}
	if err != nil {
		return updateInstallation{}, fmt.Errorf("inspect possible npm installation: %w", err)
	}

	var metadata struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		return updateInstallation{}, fmt.Errorf("parse %s: %w", packageJSONPath, err)
	}
	if metadata.Name != "cc-connect-next" {
		return installation, nil
	}

	nodeModulesDir := filepath.Dir(packageDir)
	if filepath.Base(nodeModulesDir) != "node_modules" {
		return updateInstallation{}, fmt.Errorf("npm package %s is not directly inside node_modules", packageDir)
	}
	prefixParent := filepath.Dir(nodeModulesDir)
	npmPrefix := prefixParent
	if filepath.Base(prefixParent) == "lib" {
		npmPrefix = filepath.Dir(prefixParent)
	}

	installation.Kind = updateInstallNPM
	installation.PackageDir = packageDir
	installation.NPMPrefix = npmPrefix
	return installation, nil
}

func npmExecutableName() string {
	if runtime.GOOS == "windows" {
		return "npm.cmd"
	}
	return "npm"
}

func runExternalCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func updateNPMInstallation(installation updateInstallation, tag string, runner commandRunner, verifyVersion installedVersionVerifier) error {
	if installation.Kind != updateInstallNPM || installation.PackageDir == "" || installation.NPMPrefix == "" {
		return fmt.Errorf("invalid npm installation metadata")
	}
	if !stableReleaseTagPattern.MatchString(tag) {
		return fmt.Errorf("refusing non-stable npm version %q", tag)
	}
	if runner == nil {
		return fmt.Errorf("npm command runner is unavailable")
	}
	if verifyVersion == nil {
		return fmt.Errorf("installed version verifier is unavailable")
	}

	stableVersion := strings.TrimPrefix(tag, "v")
	packageSpec := "cc-connect-next@" + stableVersion
	fmt.Printf("Updating npm package to %s...\n", packageSpec)
	if err := runner(npmExecutableName(), "install", "--global", "--prefix", installation.NPMPrefix, packageSpec); err != nil {
		return fmt.Errorf("npm install %s: %w", packageSpec, err)
	}

	packageJSONPath := filepath.Join(installation.PackageDir, "package.json")
	data, err := os.ReadFile(packageJSONPath)
	if err != nil {
		return fmt.Errorf("verify updated npm package: %w", err)
	}
	var metadata struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		return fmt.Errorf("verify updated npm package metadata: %w", err)
	}
	if metadata.Name != "cc-connect-next" || strings.TrimPrefix(metadata.Version, "v") != stableVersion {
		return fmt.Errorf("npm package metadata did not update to %s", stableVersion)
	}
	if err := verifyVersion(installation.ExecutablePath, tag); err != nil {
		return fmt.Errorf("verify updated npm binary: %w", err)
	}
	return nil
}

func updateStandaloneInstallation(installation updateInstallation, tag string) error {
	if installation.Kind != updateInstallStandalone || installation.ExecutablePath == "" {
		return fmt.Errorf("invalid standalone installation metadata")
	}
	if !stableReleaseTagPattern.MatchString(tag) {
		return fmt.Errorf("refusing non-stable standalone version %q", tag)
	}

	archiveAsset := archiveAssetName(tag)
	checksumsURL := fmt.Sprintf("%s/%s/checksums.txt", downloadBase, tag)
	manifest, err := downloadSmallFile(checksumsURL, 1024*1024)
	if err != nil {
		return fmt.Errorf("download checksums.txt: %w", err)
	}
	expectedChecksum, err := parseReleaseChecksum(string(manifest), archiveAsset)
	if err != nil {
		return err
	}

	archiveURL := fmt.Sprintf("%s/%s/%s", downloadBase, tag, archiveAsset)
	fmt.Printf("Downloading %s ...\n", archiveURL)
	archivePath, err := downloadToTemp(archiveURL)
	if err != nil {
		return fmt.Errorf("download release archive: %w", err)
	}
	defer os.Remove(archivePath)

	if err := verifyReleaseChecksum(archivePath, expectedChecksum); err != nil {
		return err
	}
	fmt.Printf("Verified SHA-256 for %s\n", archiveAsset)

	extractedPath, err := extractBinaryFromArchive(archivePath, archiveAsset)
	if err != nil {
		return fmt.Errorf("extract release archive: %w", err)
	}
	defer os.Remove(extractedPath)

	verifyVersion := func(path string) error {
		return verifyInstalledVersion(path, tag)
	}
	if err := replaceExecutable(installation.ExecutablePath, extractedPath, verifyVersion); err != nil {
		return fmt.Errorf("replace standalone binary: %w", err)
	}
	return nil
}

func verifyInstalledVersion(path, tag string) error {
	if path == "" {
		return fmt.Errorf("updated binary path is empty")
	}
	cmd := exec.Command(path, "--version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("run updated binary: %w", err)
	}
	if !versionOutputMatches(string(output), tag) {
		return fmt.Errorf("updated binary does not report %s", tag)
	}
	return nil
}

func versionOutputMatches(output, tag string) bool {
	expectedTag := strings.TrimSpace(tag)
	if !strings.HasPrefix(expectedTag, "v") {
		expectedTag = "v" + expectedTag
	}
	firstLine, _, _ := strings.Cut(strings.TrimSpace(output), "\n")
	return strings.TrimSpace(firstLine) == "cc-connect-next "+expectedTag
}

func archiveAssetName(tag string) string {
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	base := fmt.Sprintf("cc-connect-next-%s-%s-%s", tag, goos, goarch)
	if goos == "windows" {
		return base + ".zip"
	}
	return base + ".tar.gz"
}

// extractBinaryFromArchive extracts the cc-connect-next binary from a .tar.gz or .zip archive.
func extractBinaryFromArchive(archivePath, archiveName string) (string, error) {
	if strings.HasSuffix(archiveName, ".zip") {
		return extractFromZip(archivePath)
	}
	return extractFromTarGz(archivePath)
}

func extractFromTarGz(archivePath string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if strings.HasPrefix(hdr.Name, "cc-connect-next") {
			tmp, err := os.CreateTemp("", "cc-connect-next-update-*")
			if err != nil {
				return "", err
			}
			if _, err := io.Copy(tmp, tr); err != nil {
				tmp.Close()
				os.Remove(tmp.Name())
				return "", fmt.Errorf("extract: %w", err)
			}
			tmp.Close()
			return tmp.Name(), nil
		}
	}
	return "", fmt.Errorf("binary not found in archive")
}

func extractFromZip(archivePath string) (string, error) {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", fmt.Errorf("zip: %w", err)
	}
	defer r.Close()

	for _, f := range r.File {
		if !strings.HasPrefix(f.Name, "cc-connect-next") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		tmp, err := os.CreateTemp("", "cc-connect-next-update-*")
		if err != nil {
			rc.Close()
			return "", err
		}
		if _, err := io.Copy(tmp, rc); err != nil {
			tmp.Close()
			rc.Close()
			os.Remove(tmp.Name())
			return "", fmt.Errorf("extract: %w", err)
		}
		rc.Close()
		tmp.Close()
		return tmp.Name(), nil
	}
	return "", fmt.Errorf("binary not found in archive")
}

func downloadSmallFile(url string, maxBytes int64) ([]byte, error) {
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "cc-connect-next-updater")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("download exceeded %d bytes", maxBytes)
	}
	return data, nil
}

func parseReleaseChecksum(manifest, asset string) (string, error) {
	var checksum string
	for _, line := range strings.Split(manifest, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != asset {
			continue
		}
		if checksum != "" {
			return "", fmt.Errorf("checksums.txt contains more than one entry for %s", asset)
		}
		candidate := strings.ToLower(fields[0])
		decoded, err := hex.DecodeString(candidate)
		if err != nil || len(decoded) != sha256.Size {
			return "", fmt.Errorf("checksums.txt contains an invalid SHA-256 for %s", asset)
		}
		checksum = candidate
	}
	if checksum == "" {
		return "", fmt.Errorf("checksums.txt does not contain %s", asset)
	}
	return checksum, nil
}

func verifyReleaseChecksum(path, expected string) error {
	normalizedExpected := strings.ToLower(strings.TrimSpace(expected))
	decoded, err := hex.DecodeString(normalizedExpected)
	if err != nil || len(decoded) != sha256.Size {
		return fmt.Errorf("invalid expected SHA-256")
	}

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open downloaded archive: %w", err)
	}
	defer f.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return fmt.Errorf("hash downloaded archive: %w", err)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != normalizedExpected {
		return fmt.Errorf("checksum mismatch for downloaded release archive; refusing to install")
	}
	return nil
}

func downloadToTemp(url string) (string, error) {
	client := &http.Client{
		Timeout: 5 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "cc-connect-next-updater")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}

	tmp, err := os.CreateTemp("", "cc-connect-next-update-*")
	if err != nil {
		return "", err
	}

	size, err := io.Copy(tmp, resp.Body)
	if err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", fmt.Errorf("write: %w", err)
	}
	tmp.Close()

	fmt.Printf("Downloaded %.1f MB\n", float64(size)/1024/1024)
	return tmp.Name(), nil
}

func replaceExecutable(target, src string, verify func(path string) error) error {
	if err := os.Chmod(src, 0o755); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}

	// On Windows, rename over a running exe is not possible directly.
	// Move old binary aside, then move new one in.
	backup := target + ".old"
	if err := os.Remove(backup); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove previous backup: %w", err)
	}

	if err := os.Rename(target, backup); err != nil {
		return fmt.Errorf("backup old binary: %w", err)
	}

	if err := copyFile(src, target); err != nil {
		if restoreErr := restoreExecutable(target, backup); restoreErr != nil {
			slog.Warn("update: failed to restore old binary after copy error", "error", restoreErr)
		}
		return fmt.Errorf("install new binary: %w", err)
	}

	if err := os.Chmod(target, 0o755); err != nil {
		if restoreErr := restoreExecutable(target, backup); restoreErr != nil {
			slog.Warn("update: failed to restore old binary after chmod error", "error", restoreErr)
		}
		return fmt.Errorf("chmod new binary: %w", err)
	}
	if verify != nil {
		if err := verify(target); err != nil {
			if restoreErr := restoreExecutable(target, backup); restoreErr != nil {
				slog.Warn("update: failed to restore old binary after verification error", "error", restoreErr)
			}
			return fmt.Errorf("verify new binary: %w", err)
		}
	}

	if err := os.Remove(backup); err != nil && !os.IsNotExist(err) {
		slog.Warn("update: failed to remove old binary backup", "path", backup, "error", err)
	}
	return nil
}

func restoreExecutable(target, backup string) error {
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove failed update: %w", err)
	}
	if err := os.Rename(backup, target); err != nil {
		return fmt.Errorf("restore backup: %w", err)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func checkUpdate() {
	release, err := fetchLatestStableRelease()
	if err != nil {
		return
	}
	if err := validateStableRelease(release); err != nil {
		return
	}
	if isNewer(release.TagName, version) {
		fmt.Fprintf(os.Stderr, "Stable update available: %s → %s (run: cc-connect-next update)\n", version, release.TagName)
	}
}

// isNewer returns true if latest represents a newer release than current.
// Handles semver tags (v1.2.3), pre-release tags (v1.2.3-beta.1, v1.2.3-rc.1),
// and dev builds (v1.2.3-10-gHASH).
func isNewer(latest, current string) bool {
	if latest == "" || current == "" {
		return false
	}
	if strings.HasPrefix(current, "dev") {
		return true
	}

	l := strings.TrimPrefix(latest, "v")
	c := strings.TrimPrefix(current, "v")

	lBase, lPre, _ := strings.Cut(l, "-")
	cBase, cPre, _ := strings.Cut(c, "-")

	lParts := strings.Split(lBase, ".")
	cParts := strings.Split(cBase, ".")

	for i := 0; i < len(lParts) || i < len(cParts); i++ {
		var lv, cv int
		if i < len(lParts) {
			_, _ = fmt.Sscanf(lParts[i], "%d", &lv)
		}
		if i < len(cParts) {
			_, _ = fmt.Sscanf(cParts[i], "%d", &cv)
		}
		if lv > cv {
			return true
		}
		if lv < cv {
			return false
		}
	}

	// Same base version — compare pre-release suffix
	// No pre-release beats a pre-release (1.2.0 > 1.2.0-beta.1)
	if cPre != "" && lPre == "" {
		return true
	}
	if cPre == "" && lPre != "" {
		return false
	}
	// Both have pre-release: split on "." and compare each segment
	// numerically where possible so beta.10 > beta.2.
	if lPre != "" && cPre != "" {
		return comparePreRelease(lPre, cPre) > 0
	}

	return false
}

// comparePreRelease compares two pre-release strings segment by segment.
// Numeric segments are compared as integers; non-numeric segments are
// compared lexicographically. Returns >0 if a is greater, <0 if b is
// greater, 0 if equal.
func comparePreRelease(a, b string) int {
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")

	max := len(aParts)
	if len(bParts) > max {
		max = len(bParts)
	}
	for i := 0; i < max; i++ {
		var ap, bp string
		if i < len(aParts) {
			ap = aParts[i]
		}
		if i < len(bParts) {
			bp = bParts[i]
		}

		var an, bn int
		aN, _ := fmt.Sscanf(ap, "%d", &an)
		bN, _ := fmt.Sscanf(bp, "%d", &bn)
		aIsNum := aN == 1 && fmt.Sprintf("%d", an) == ap
		bIsNum := bN == 1 && fmt.Sprintf("%d", bn) == bp

		if aIsNum && bIsNum {
			if an != bn {
				return an - bn
			}
			continue
		}
		// Non-numeric: lexicographic
		if ap < bp {
			return -1
		}
		if ap > bp {
			return 1
		}
	}
	return 0
}
