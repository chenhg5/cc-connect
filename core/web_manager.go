package core

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	npmPackageName    = "cc-connect-web"
	npmRegistryURL    = "https://registry.npmjs.org"
	defaultMgmtPort   = 9820
	defaultBridgePort  = 9810
)

// WebInstallDir returns the directory where cc-connect-web is installed.
// Typically ~/.cc-connect/web/
func WebInstallDir(dataDir string) string {
	return filepath.Join(dataDir, "web")
}

// WebDistDir returns the path to the built static files.
func WebDistDir(dataDir string) string {
	return filepath.Join(WebInstallDir(dataDir), "node_modules", npmPackageName, "dist")
}

// WebInstalledVersion reads the installed version from node_modules.
func WebInstalledVersion(dataDir string) string {
	pkgJSON := filepath.Join(WebInstallDir(dataDir), "node_modules", npmPackageName, "package.json")
	data, err := os.ReadFile(pkgJSON)
	if err != nil {
		return ""
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return ""
	}
	return pkg.Version
}

// WebLatestVersion fetches the latest version from npm registry.
func WebLatestVersion() (string, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(npmRegistryURL + "/" + npmPackageName + "/latest")
	if err != nil {
		return "", fmt.Errorf("fetch npm registry: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("npm registry returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read npm response: %w", err)
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &pkg); err != nil {
		return "", fmt.Errorf("parse npm response: %w", err)
	}
	return pkg.Version, nil
}

// WebInstall installs cc-connect-web via npm into the data directory.
// Returns the installed version and any error.
func WebInstall(dataDir string) (string, error) {
	dir := WebInstallDir(dataDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create web dir: %w", err)
	}

	pkgJSON := filepath.Join(dir, "package.json")
	if _, err := os.Stat(pkgJSON); os.IsNotExist(err) {
		if err := os.WriteFile(pkgJSON, []byte(`{"private":true}`), 0644); err != nil {
			return "", fmt.Errorf("create package.json: %w", err)
		}
	}

	npmBin := findNpmBin()
	if npmBin == "" {
		return "", fmt.Errorf("npm not found in PATH — please install Node.js (https://nodejs.org)")
	}

	cmd := exec.Command(npmBin, "install", npmPackageName+"@latest", "--save")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("npm install failed: %w\n%s", err, strings.TrimSpace(string(output)))
	}

	version := WebInstalledVersion(dataDir)
	if version == "" {
		return "", fmt.Errorf("install succeeded but version not found")
	}
	slog.Info("web admin installed", "version", version, "dir", dir)
	return version, nil
}

// WebUpgrade upgrades cc-connect-web to the latest version.
// Returns (oldVersion, newVersion, error).
func WebUpgrade(dataDir string) (string, string, error) {
	oldVersion := WebInstalledVersion(dataDir)
	if oldVersion == "" {
		v, err := WebInstall(dataDir)
		return "", v, err
	}

	dir := WebInstallDir(dataDir)
	npmBin := findNpmBin()
	if npmBin == "" {
		return oldVersion, "", fmt.Errorf("npm not found in PATH")
	}

	cmd := exec.Command(npmBin, "install", npmPackageName+"@latest", "--save")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return oldVersion, "", fmt.Errorf("npm update failed: %w\n%s", err, strings.TrimSpace(string(output)))
	}

	newVersion := WebInstalledVersion(dataDir)
	slog.Info("web admin upgraded", "from", oldVersion, "to", newVersion)
	return oldVersion, newVersion, nil
}

// WebIsInstalled checks if cc-connect-web is installed.
func WebIsInstalled(dataDir string) bool {
	return WebInstalledVersion(dataDir) != ""
}

// GenerateToken creates a random hex token.
func GenerateToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("cc-connect-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func findNpmBin() string {
	if path, err := exec.LookPath("npm"); err == nil {
		return path
	}
	for _, candidate := range []string{
		"/usr/local/bin/npm",
		"/usr/bin/npm",
		filepath.Join(os.Getenv("HOME"), ".nvm/current/bin/npm"),
	} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}
