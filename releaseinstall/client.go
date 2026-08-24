package releaseinstall

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/releasecontract"
)

const (
	defaultReleaseBase = "https://github.com/shusfun/cc-connect/releases"
	defaultReleaseAPI  = "https://api.github.com/repos/shusfun/cc-connect/releases/latest"
)

type VerifyFunc func(context.Context, string, []byte, []byte) error

type Config struct {
	HTTPClient  *http.Client
	ReleaseBase string
	ReleaseAPI  string
	Cosign      string
	Verify      VerifyFunc
}

type Client struct {
	http        *http.Client
	releaseBase string
	releaseAPI  string
	verify      VerifyFunc
}

type Release struct {
	Manifest    releasecontract.Manifest
	ManifestRaw []byte
	BundleRaw   []byte
}

func New(config Config) (*Client, error) {
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: 2 * time.Minute}
	}
	if strings.TrimSpace(config.ReleaseBase) == "" {
		config.ReleaseBase = defaultReleaseBase
	}
	if strings.TrimSpace(config.ReleaseAPI) == "" {
		config.ReleaseAPI = defaultReleaseAPI
	}
	if config.Verify == nil {
		cosign := strings.TrimSpace(config.Cosign)
		if cosign == "" {
			cosign = "cosign"
		}
		config.Verify = cosignVerifier(cosign)
	}
	return &Client{http: config.HTTPClient, releaseBase: strings.TrimSuffix(config.ReleaseBase, "/"), releaseAPI: config.ReleaseAPI, verify: config.Verify}, nil
}

func (c *Client) LatestTag(ctx context.Context) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.releaseAPI, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	response, err := c.http.Do(request)
	if err != nil {
		return "", fmt.Errorf("release install: query latest release: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("release install: latest release status %s", response.Status)
	}
	var value struct {
		Tag string `json:"tag_name"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err := decoder.Decode(&value); err != nil {
		return "", fmt.Errorf("release install: decode latest release: %w", err)
	}
	if !validTag(value.Tag) {
		return "", errors.New("release install: latest release returned an invalid tag")
	}
	return value.Tag, nil
}

func (c *Client) Fetch(ctx context.Context, tag string) (Release, error) {
	if !validTag(tag) {
		return Release{}, errors.New("release install: valid release tag is required")
	}
	base := c.releaseBase + "/download/" + tag + "/"
	manifestRaw, err := c.downloadBytes(ctx, base+"manifest.json", 2<<20)
	if err != nil {
		return Release{}, err
	}
	bundleRaw, err := c.downloadBytes(ctx, base+"manifest.bundle", 8<<20)
	if err != nil {
		return Release{}, err
	}
	if err := c.verify(ctx, tag, manifestRaw, bundleRaw); err != nil {
		return Release{}, fmt.Errorf("release install: verify signed manifest: %w", err)
	}
	manifest, err := DecodeLockedManifest(manifestRaw, tag)
	if err != nil {
		return Release{}, err
	}
	return Release{Manifest: manifest, ManifestRaw: manifestRaw, BundleRaw: bundleRaw}, nil
}

// DecodeLockedManifest 校验已由调用方可信边界验签的清单，并将其绑定到指定标签。
func DecodeLockedManifest(manifestRaw []byte, tag string) (releasecontract.Manifest, error) {
	manifest, err := releasecontract.Decode(manifestRaw)
	if err != nil {
		return releasecontract.Manifest{}, err
	}
	if manifest.Tag != tag {
		return releasecontract.Manifest{}, fmt.Errorf("release install: manifest tag %q does not match locked tag %q", manifest.Tag, tag)
	}
	return manifest, nil
}

func (c *Client) DownloadArtifact(ctx context.Context, release Release, artifact releasecontract.Artifact, destination string) error {
	if artifact.Name == "" || artifact.Size < 1 {
		return errors.New("release install: invalid artifact metadata")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.releaseBase+"/download/"+release.Manifest.Tag+"/"+artifact.Name, nil)
	if err != nil {
		return err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("release install: download %s: %w", artifact.Name, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("release install: download %s status %s", artifact.Name, response.Status)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(destination)
		}
	}()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, artifact.Size+1))
	if err != nil {
		return fmt.Errorf("release install: save %s: %w", artifact.Name, err)
	}
	if written != artifact.Size {
		return fmt.Errorf("release install: %s size mismatch", artifact.Name)
	}
	if actual := hex.EncodeToString(hash.Sum(nil)); actual != artifact.SHA256 {
		return fmt.Errorf("release install: %s SHA-256 mismatch", artifact.Name)
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	remove = false
	return nil
}

func ExtractBinary(archivePath, destination, binaryName string) error {
	archive, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = archive.Close() }()
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return fmt.Errorf("release install: open gzip archive: %w", err)
	}
	defer func() { _ = gz.Close() }()
	reader := tar.NewReader(gz)
	header, err := reader.Next()
	if err != nil {
		return fmt.Errorf("release install: read archive: %w", err)
	}
	if header.Typeflag != tar.TypeReg || header.Name != binaryName || filepath.Base(header.Name) != header.Name || header.Size < 1 {
		return errors.New("release install: archive must contain exactly the expected regular binary")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	temporary, err := os.OpenFile(destination+".tmp", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	remove := true
	defer func() {
		_ = temporary.Close()
		if remove {
			_ = os.Remove(temporary.Name())
		}
	}()
	written, err := io.Copy(temporary, io.LimitReader(reader, header.Size+1))
	if err != nil {
		return fmt.Errorf("release install: extract binary: %w", err)
	}
	if written != header.Size {
		return errors.New("release install: extract binary size mismatch")
	}
	if _, err := reader.Next(); !errors.Is(err, io.EOF) {
		return errors.New("release install: archive contains unexpected additional entries")
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary.Name(), destination); err != nil {
		return err
	}
	remove = false
	return nil
}

func (c *Client) downloadBytes(ctx context.Context, address string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("release install: download %s: %w", address, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release install: download %s status %s", address, response.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("release install: response exceeds size limit")
	}
	return raw, nil
}

func cosignVerifier(binary string) VerifyFunc {
	return func(ctx context.Context, tag string, manifest, bundle []byte) error {
		directory, err := os.MkdirTemp("", "cc-connect-cosign-*")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(directory) }()
		manifestPath := filepath.Join(directory, "manifest.json")
		bundlePath := filepath.Join(directory, "manifest.bundle")
		if err := os.WriteFile(manifestPath, manifest, 0o600); err != nil {
			return err
		}
		if err := os.WriteFile(bundlePath, bundle, 0o600); err != nil {
			return err
		}
		identity := "https://github.com/shusfun/cc-connect/.github/workflows/release.yml@refs/tags/" + tag
		command := exec.CommandContext(ctx, binary, "verify-blob", "--bundle", bundlePath,
			"--certificate-oidc-issuer", "https://token.actions.githubusercontent.com",
			"--certificate-identity", identity, manifestPath)
		if output, err := command.CombinedOutput(); err != nil {
			return fmt.Errorf("cosign verify-blob: %w: %s", err, strings.TrimSpace(string(output)))
		}
		return nil
	}
}

func validTag(tag string) bool {
	if len(tag) < 2 || tag[0] != 'v' || strings.TrimSpace(tag) != tag || strings.ContainsAny(tag, `/\\`) {
		return false
	}
	for _, character := range tag[1:] {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '.' || character == '-' {
			continue
		}
		return false
	}
	return true
}
