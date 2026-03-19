package core

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	githubReleasesAPI = "https://api.github.com/repos/chenhg5/cc-connect/releases"
	giteeReleasesAPI  = "https://gitee.com/api/v5/repos/cg33/cc-connect/releases"
	githubDownload    = "https://github.com/chenhg5/cc-connect/releases/download"
	giteeDownload     = "https://gitee.com/cg33/cc-connect/releases/download"
)

type ReleaseInfo struct {
	TagName    string `json:"tag_name"`
	Name       string `json:"name"`
	Body       string `json:"body"`
	Prerelease bool   `json:"prerelease"`
	CreatedAt  string `json:"created_at"`
}

// CheckForUpdate queries GitHub/Gitee for newer releases.
// If preferGitee is true, tries Gitee first (faster in China); otherwise GitHub first.
func CheckForUpdate(currentVersion string, preferGitee bool) (*ReleaseInfo, error) {
	releases, err := fetchReleases(preferGitee)
	if err != nil {
		return nil, err
	}
	if len(releases) == 0 {
		return nil, nil
	}

	// Find the newest release by semver comparison
	var best *ReleaseInfo
	for i := range releases {
		r := &releases[i]
		if r.TagName == "" {
			continue
		}
		if best == nil || semverCompare(r.TagName, best.TagName) > 0 {
			best = r
		}
	}

	if best == nil {
		return nil, nil
	}

	cur := normalizeVersion(currentVersion)
	latest := normalizeVersion(best.TagName)
	if cur == latest || semverCompare(best.TagName, currentVersion) <= 0 {
		return nil, nil
	}

	return best, nil
}

func fetchReleases(preferGitee bool) ([]ReleaseInfo, error) {
	type source struct {
		name string
		url  string
	}
	sources := []source{
		{"github", githubReleasesAPI + "?per_page=20"},
		{"gitee", giteeReleasesAPI + "?per_page=20&direction=desc&sort=created"},
	}
	if preferGitee {
		sources[0], sources[1] = sources[1], sources[0]
	}

	releases, err := fetchReleasesFrom(sources[0].url)
	if err == nil && len(releases) > 0 {
		return releases, nil
	}
	slog.Debug("updater: primary source failed, trying fallback", "primary", sources[0].name, "error", err)

	releases, err = fetchReleasesFrom(sources[1].url)
	if err != nil {
		return nil, fmt.Errorf("check updates failed (both sources): %w", err)
	}
	return releases, nil
}

func fetchReleasesFrom(apiURL string) ([]ReleaseInfo, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "cc-connect-updater")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned %d", resp.StatusCode)
	}

	var releases []ReleaseInfo
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, err
	}
	return releases, nil
}

// SelfUpdate downloads and installs the given release version.
// If preferGitee is true, tries Gitee download first.
func SelfUpdate(tag string, preferGitee bool) error {
	goos := runtime.GOOS
	goarch := runtime.GOARCH

	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	filename := fmt.Sprintf("cc-connect-%s-%s-%s%s", tag, goos, goarch, ext)

	giteeURL := fmt.Sprintf("%s/%s/%s", giteeDownload, tag, filename)
	githubURL := fmt.Sprintf("%s/%s/%s", githubDownload, tag, filename)
	urls := []string{githubURL, giteeURL}
	if preferGitee {
		urls = []string{giteeURL, githubURL}
	}

	var data []byte
	var lastErr error
	for _, u := range urls {
		slog.Info("updater: downloading", "url", u)
		data, lastErr = downloadFile(u)
		if lastErr == nil {
			break
		}
		slog.Debug("updater: download failed, trying next", "error", lastErr)
	}
	if lastErr != nil && data == nil {
		return fmt.Errorf("download failed from all sources: %w", lastErr)
	}

	var binary []byte
	var err error
	if goos == "windows" {
		binary, err = extractBinaryFromZip(data)
	} else {
		binary, err = extractBinaryFromTarGz(data)
	}
	if err != nil {
		return fmt.Errorf("extract binary: %w", err)
	}

	return replaceBinary(binary)
}

func downloadFile(url string) ([]byte, error) {
	client := &http.Client{
		Timeout: 5 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "cc-connect-updater")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}

	return io.ReadAll(resp.Body)
}

func extractBinaryFromTarGz(data []byte) ([]byte, error) {
	r := bytes.NewReader(data)
	gr, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		name := filepath.Base(hdr.Name)
		if strings.HasPrefix(name, "cc-connect") && hdr.Typeflag == tar.TypeReg {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("cc-connect binary not found in archive")
}

func extractBinaryFromZip(data []byte) ([]byte, error) {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	for _, f := range r.File {
		name := filepath.Base(f.Name)
		if strings.HasPrefix(name, "cc-connect") && !f.FileInfo().IsDir() {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(rc)
		}
	}
	return nil, fmt.Errorf("cc-connect binary not found in zip archive")
}

func replaceBinary(newBinary []byte) error {
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return fmt.Errorf("resolve symlinks: %w", err)
	}

	dir := filepath.Dir(execPath)
	tmpFile, err := os.CreateTemp(dir, "cc-connect-update-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	if _, err := tmpFile.Write(newBinary); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write new binary: %w", err)
	}
	tmpFile.Close()

	if err := os.Chmod(tmpPath, 0o755); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod: %w", err)
	}

	oldPath := execPath + ".old"
	os.Remove(oldPath)

	if err := os.Rename(execPath, oldPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("backup old binary: %w", err)
	}

	if err := os.Rename(tmpPath, execPath); err != nil {
		// Try to restore
		_ = os.Rename(oldPath, execPath)
		return fmt.Errorf("install new binary: %w", err)
	}

	// Don't remove .old file on Linux - the running process may still need it
	// for os.Executable() to work correctly after restart.
	// The .old file will be overwritten on next update.

	slog.Info("updater: binary replaced successfully", "path", execPath)
	return nil
}

// --- semver comparison ---

var semverRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-(.+))?$`)

type semver struct {
	major, minor, patch int
	pre                 string
	preNum              int
}

func parseSemver(v string) semver {
	m := semverRe.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return semver{}
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	patch, _ := strconv.Atoi(m[3])
	pre := m[4]
	preNum := 0
	if idx := strings.LastIndex(pre, "."); idx >= 0 {
		preNum, _ = strconv.Atoi(pre[idx+1:])
	}
	return semver{major: major, minor: minor, patch: patch, pre: pre, preNum: preNum}
}

func normalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return v
}

// semverCompare returns >0 if a > b, <0 if a < b, 0 if equal.
func semverCompare(a, b string) int {
	sa := parseSemver(a)
	sb := parseSemver(b)

	if d := sa.major - sb.major; d != 0 {
		return d
	}
	if d := sa.minor - sb.minor; d != 0 {
		return d
	}
	if d := sa.patch - sb.patch; d != 0 {
		return d
	}
	// No pre-release > has pre-release (1.0.0 > 1.0.0-beta.1)
	if sa.pre == "" && sb.pre != "" {
		return 1
	}
	if sa.pre != "" && sb.pre == "" {
		return -1
	}
	// Both have pre-release: compare lexicographically, then by number
	if sa.pre != sb.pre {
		if d := strings.Compare(sa.pre, sb.pre); d != 0 {
			// "beta" prefix comparison; if same prefix, compare numbers
			aPre := strings.TrimRight(sa.pre, "0123456789.")
			bPre := strings.TrimRight(sb.pre, "0123456789.")
			if aPre == bPre {
				return sa.preNum - sb.preNum
			}
			return d
		}
	}
	return 0
}
