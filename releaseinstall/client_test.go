package releaseinstall

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/releasecontract"
)

func TestClientLocksLatestTagVerifiesManifestAndArtifactDigest(t *testing.T) {
	archive := testArchive(t, "cc-connect-control", []byte("control"), false)
	digest := sha256.Sum256(archive)
	manifest := releasecontract.Manifest{
		Version: 1, Repository: releasecontract.Repository, Workflow: releasecontract.Workflow, Tag: "v0.1.0",
		CommitSHA: strings.Repeat("a", 40), RuntimeContractHash: "contract", ControlSchema: 4,
		WorkspaceChatSchema: 3, GeneratedAt: time.Now().UTC(),
	}
	for _, target := range [][3]string{{"control", "linux", "amd64"}, {"control", "linux", "arm64"}, {"server", "linux", "amd64"}, {"server", "linux", "arm64"}, {"runtime", "darwin", "amd64"}, {"runtime", "darwin", "arm64"}} {
		manifest.Artifacts = append(manifest.Artifacts, releasecontract.Artifact{Name: "cc-connect-" + strings.Join(target[:], "-") + ".tar.gz", Component: target[0], OS: target[1], Arch: target[2], SHA256: hex.EncodeToString(digest[:]), Size: int64(len(archive))})
	}
	manifestRaw, _ := json.Marshal(manifest)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/latest":
			_, _ = w.Write([]byte(`{"tag_name":"v0.1.0","ignored":true}`))
		case strings.HasSuffix(r.URL.Path, "/manifest.json"):
			_, _ = w.Write(manifestRaw)
		case strings.HasSuffix(r.URL.Path, "/manifest.bundle"):
			_, _ = w.Write([]byte("bundle"))
		default:
			_, _ = w.Write(archive)
		}
	}))
	defer server.Close()
	verified := false
	client, err := New(Config{HTTPClient: server.Client(), ReleaseBase: server.URL, ReleaseAPI: server.URL + "/latest", Verify: func(_ context.Context, tag string, gotManifest, bundle []byte) error {
		verified = tag == "v0.1.0" && bytes.Equal(gotManifest, manifestRaw) && string(bundle) == "bundle"
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	tag, err := client.LatestTag(context.Background())
	if err != nil || tag != "v0.1.0" {
		t.Fatalf("LatestTag() = %q, %v", tag, err)
	}
	release, err := client.Fetch(context.Background(), tag)
	if err != nil || !verified {
		t.Fatalf("Fetch() error = %v, verified=%v", err, verified)
	}
	destination := t.TempDir() + "/artifact.tar.gz"
	if err := client.DownloadArtifact(context.Background(), release, manifest.Artifacts[0], destination); err != nil {
		t.Fatal(err)
	}
}

func TestExtractBinaryRejectsAdditionalArchiveEntries(t *testing.T) {
	archive := testArchive(t, "cc-connect-control", []byte("control"), true)
	path := t.TempDir() + "/archive.tar.gz"
	if err := os.WriteFile(path, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ExtractBinary(path, t.TempDir()+"/binary", "cc-connect-control"); err == nil {
		t.Fatal("archive with additional entry was accepted")
	}
}

func testArchive(t *testing.T, name string, raw []byte, extra bool) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gz)
	if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(raw)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	_, _ = tarWriter.Write(raw)
	if extra {
		_ = tarWriter.WriteHeader(&tar.Header{Name: "extra", Mode: 0o600, Size: 1, Typeflag: tar.TypeReg})
		_, _ = tarWriter.Write([]byte("x"))
	}
	_ = tarWriter.Close()
	_ = gz.Close()
	return buffer.Bytes()
}
