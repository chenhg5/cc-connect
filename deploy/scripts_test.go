package deploy_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/controlstore"
	"github.com/chenhg5/cc-connect/releasecontract"
	"github.com/chenhg5/cc-connect/runtimeprotocol"
)

func TestBootstrapIsIdempotentAndCreatesOnlyControlService(t *testing.T) {
	fixture := newReleaseFixture(t, false)
	root := filepath.Join(t.TempDir(), "root")
	fakeBin := filepath.Join(t.TempDir(), "bin")
	writeExecutable(t, filepath.Join(fakeBin, "cosign"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(fakeBin, "systemctl"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$CC_TEST_SYSTEMCTL_LOG\"\n")
	logPath := filepath.Join(t.TempDir(), "systemctl.log")
	command := func() string {
		cmd := exec.Command("bash", "./bootstrap.sh", "--release-dir", fixture.directory)
		cmd.Env = append(os.Environ(),
			"PATH="+fakeBin+":"+os.Getenv("PATH"),
			"CC_CONNECT_BOOTSTRAP_ROOT="+root,
			"CC_CONNECT_BOOTSTRAP_TESTING=1",
			"CC_TEST_SYSTEMCTL_LOG="+logPath,
		)
		raw, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bootstrap: %v\n%s", err, raw)
		}
		return string(raw)
	}
	firstOutput := command()
	firstToken := strings.TrimSpace(readFile(t, filepath.Join(root, "var/lib/cc-connect/control/setup-token")))
	secondOutput := command()
	secondToken := strings.TrimSpace(readFile(t, filepath.Join(root, "var/lib/cc-connect/control/setup-token")))
	if firstToken == "" || firstToken != secondToken || !strings.Contains(firstOutput, firstToken) || !strings.Contains(secondOutput, firstToken) {
		t.Fatalf("bootstrap token was not stable across retries: first=%q second=%q", firstToken, secondToken)
	}
	assertMode(t, filepath.Join(root, "var/lib/cc-connect/control"), 0o700)
	assertMode(t, filepath.Join(root, "var/lib/cc-connect/app"), 0o750)
	assertMode(t, filepath.Join(root, "opt/cc-connect/releases", fixture.manifest.Tag), 0o755)
	current, err := filepath.EvalSymlinks(filepath.Join(root, "opt/cc-connect/current"))
	if err != nil || filepath.Base(current) != fixture.manifest.Tag {
		t.Fatalf("current release = %q, %v", current, err)
	}
	unit := readFile(t, filepath.Join(root, "etc/systemd/system/cc-connect-control.service"))
	if !strings.Contains(unit, "ExecStart=/opt/cc-connect/current/cc-connect-control") || strings.Contains(unit, "cc-connect-server.service") || !strings.Contains(unit, "ExecStopPost=") {
		t.Fatalf("unexpected systemd unit:\n%s", unit)
	}
	for _, name := range []string{"manifest.json", "manifest.bundle", "cc-connect-control", "cc-connect-server"} {
		if _, err := os.Stat(filepath.Join(root, "opt/cc-connect/releases", fixture.manifest.Tag, name)); err != nil {
			t.Fatalf("release slot missing %s: %v", name, err)
		}
	}
}

func TestDockerDeploymentKeepsControlAsSingleProcessOwner(t *testing.T) {
	dockerfile := readFile(t, filepath.Join("..", "Dockerfile"))
	compose := readFile(t, filepath.Join("..", "compose.yaml"))
	entrypoint := readFile(t, "docker-entrypoint.sh")

	for name, content := range map[string]string{
		"Dockerfile": dockerfile, "compose.yaml": compose, "docker-entrypoint.sh": entrypoint,
	} {
		if strings.Contains(content, "cc-connect-server.service") || strings.Contains(content, "docker.sock") {
			t.Fatalf("%s introduces a second lifecycle owner", name)
		}
	}
	for _, required := range []string{
		`USER cc-connect:cc-connect`, `--deployment-owner", "container`,
		`ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]`,
	} {
		if !strings.Contains(dockerfile, required) {
			t.Fatalf("Dockerfile missing %q", required)
		}
	}
	for _, required := range []string{
		`127.0.0.1:${CC_CONNECT_PORT:-9820}:9820`,
		`${CC_CONNECT_STATE_ROOT:-/var/lib/cc-connect-docker}/control:/var/lib/cc-connect/control`,
		`${CC_CONNECT_STATE_ROOT:-/var/lib/cc-connect-docker}/app:/var/lib/cc-connect/app`,
		`/run/cc-connect-deploy:/run/cc-connect-deploy:ro`,
		`no-new-privileges:true`,
	} {
		if !strings.Contains(compose, required) {
			t.Fatalf("compose.yaml missing %q", required)
		}
	}
	if !strings.Contains(entrypoint, `exec /usr/local/bin/cc-connect-control "$@"`) {
		t.Fatal("container entrypoint does not exec control as the only managed process")
	}
}

func TestDockerWebBuildUsesRepositoryLockfile(t *testing.T) {
	dockerfile := readFile(t, filepath.Join("..", "Dockerfile"))
	for _, required := range []string{
		"COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./",
		"pnpm install --frozen-lockfile",
		"WORKDIR /src/web",
	} {
		if !strings.Contains(dockerfile, required) {
			t.Fatalf("Dockerfile missing workspace lockfile install step %q", required)
		}
	}
	if strings.Contains(dockerfile, "pnpm --dir web install") {
		t.Fatal("Dockerfile uses --dir install instead of the declared root workspace")
	}
	workspace := readFile(t, filepath.Join("..", "pnpm-workspace.yaml"))
	if !strings.Contains(workspace, "- web") {
		t.Fatal("pnpm workspace does not declare the web importer")
	}
}

func TestDockerPortableBuildersRunOnBuildPlatform(t *testing.T) {
	dockerfile := readFile(t, filepath.Join("..", "Dockerfile"))
	for _, required := range []string{
		"FROM --platform=${BUILDPLATFORM} ${NODE_IMAGE} AS web-build",
		"FROM --platform=${BUILDPLATFORM} ${GO_IMAGE} AS go-build",
		"CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build",
	} {
		if !strings.Contains(dockerfile, required) {
			t.Fatalf("Dockerfile missing native cross-build contract %q", required)
		}
	}
	if strings.Contains(dockerfile, "FROM --platform=${BUILDPLATFORM} ${ALPINE_IMAGE} AS runtime") {
		t.Fatal("Dockerfile pins the target runtime image to the build platform")
	}
}

func TestDockerContextIncludesGoEmbedInputs(t *testing.T) {
	dockerIgnore := readFile(t, filepath.Join("..", ".dockerignore"))
	excludeIndex := strings.Index(dockerIgnore, "config.*.toml")
	includeIndex := strings.Index(dockerIgnore, "!config.example.toml")
	if excludeIndex < 0 || includeIndex <= excludeIndex {
		t.Fatal(".dockerignore must re-include config.example.toml after excluding deployment config variants")
	}
	for _, path := range []string{
		filepath.Join("..", "config.example.toml"),
		"install-runtime.sh",
		filepath.Join("..", "core", "runas_probe.sh"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("embedded Docker build input missing: %s: %v", path, err)
		}
	}
}

func TestContainerBootstrapIsIdempotentAndInstallsOnlyDeployHostService(t *testing.T) {
	fixture := newReleaseFixture(t, false)
	root := filepath.Join(t.TempDir(), "root")
	fakeBin := filepath.Join(t.TempDir(), "bin")
	writeExecutable(t, filepath.Join(fakeBin, "cosign"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(fakeBin, "docker"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(fakeBin, "systemctl"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$CC_TEST_SYSTEMCTL_LOG\"\n")
	logPath := filepath.Join(t.TempDir(), "systemctl.log")
	run := func() string {
		command := exec.Command("bash", "./bootstrap-container.sh", "--release-dir", fixture.directory)
		command.Env = append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"),
			"CC_CONNECT_BOOTSTRAP_ROOT="+root, "CC_CONNECT_BOOTSTRAP_TESTING=1", "CC_TEST_SYSTEMCTL_LOG="+logPath)
		raw, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("container bootstrap: %v\n%s", err, raw)
		}
		return string(raw)
	}
	first := run()
	token := strings.TrimSpace(readFile(t, filepath.Join(root, "var/lib/cc-connect-docker/control/setup-token")))
	second := run()
	if token == "" || !strings.Contains(first, token) || !strings.Contains(second, token) {
		t.Fatalf("container bootstrap token is not stable: %q", token)
	}
	for _, path := range []string{
		filepath.Join(root, "opt/cc-connect-docker/cc-connect-deploy-host"),
		filepath.Join(root, "opt/cc-connect-docker/compose.yaml"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("container bootstrap missing %s: %v", path, err)
		}
	}
	unit := readFile(t, filepath.Join(root, "etc/systemd/system/cc-connect-deploy-host.service"))
	for _, required := range []string{"User=root", "cc-connect-deploy-host", "--client-uid 10001", "--initial-tag v0.1.0", "RuntimeDirectory=cc-connect-deploy"} {
		if !strings.Contains(unit, required) {
			t.Fatalf("deploy host unit missing %q:\n%s", required, unit)
		}
	}
	if strings.Contains(unit, "cc-connect-control.service") || strings.Contains(unit, "docker.sock") {
		t.Fatalf("container bootstrap introduced a second service or Docker socket mount:\n%s", unit)
	}
	if strings.Contains(unit, "ExecStartPre=/bin/chown") {
		t.Fatalf("container bootstrap must leave socket directory ownership to deploy-host:\n%s", unit)
	}
	systemctlLog := readFile(t, logPath)
	if strings.Count(systemctlLog, "restart cc-connect-deploy-host.service") != 2 {
		t.Fatalf("container bootstrap did not activate the installed deploy-host on every run:\n%s", systemctlLog)
	}
	compose := readFile(t, filepath.Join(root, "opt/cc-connect-docker/compose.yaml"))
	if strings.Contains(compose, "docker.sock") || !strings.Contains(compose, "/run/cc-connect-deploy:/run/cc-connect-deploy:ro") {
		t.Fatalf("installed compose has an unsafe control boundary:\n%s", compose)
	}
}

func TestRuntimeInstallerIsIdempotentAndPreservesSignedSlot(t *testing.T) {
	fixture := newReleaseFixture(t, true)
	home := t.TempDir()
	fakeBin := filepath.Join(t.TempDir(), "bin")
	writeExecutable(t, filepath.Join(fakeBin, "cosign"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(fakeBin, "launchctl"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$CC_TEST_LAUNCHCTL_LOG\"\n[ \"${1:-}\" != print ]\n")
	writeExecutable(t, filepath.Join(fakeBin, "curl"), `#!/bin/sh
set -eu
destination=""
url=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = -o ]; then destination="$2"; shift 2; continue; fi
  url="$1"
  shift
done
cp "$CC_TEST_RELEASE_DIR/${url##*/}" "$destination"
`)
	writeExecutable(t, filepath.Join(fakeBin, "shasum"), `#!/bin/sh
set -eu
if [ "$1" = -a ] && [ "$2" = 256 ]; then shift 2; fi
sha256sum "$@"
`)
	launchLog := filepath.Join(t.TempDir(), "launchctl.log")
	run := func(withCode bool) {
		args := []string{"./install-runtime.sh", "--server", "https://cc.example.com", "--tag", fixture.manifest.Tag}
		if withCode {
			args = append(args, "--code", "pair-once")
		}
		cmd := exec.Command("bash", args...)
		cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+fakeBin+":"+os.Getenv("PATH"),
			"CC_TEST_RELEASE_DIR="+fixture.directory, "CC_TEST_LAUNCHCTL_LOG="+launchLog)
		if raw, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("install runtime: %v\n%s", err, raw)
		}
	}
	run(true)
	run(false)
	state := filepath.Join(home, "Library", "Application Support", "cc-connect-runtime")
	mismatch := exec.Command("bash", "./install-runtime.sh", "--server", "https://other.example.com", "--tag", fixture.manifest.Tag)
	mismatch.Env = append(os.Environ(), "HOME="+home, "PATH="+fakeBin+":"+os.Getenv("PATH"),
		"CC_TEST_RELEASE_DIR="+filepath.Join(t.TempDir(), "missing"), "CC_TEST_LAUNCHCTL_LOG="+launchLog)
	if raw, err := mismatch.CombinedOutput(); err == nil || !strings.Contains(string(raw), "已配对到其他服务器") {
		t.Fatalf("不同服务器身份未在下载前拒绝: err=%v output=%s", err, raw)
	}
	if strings.TrimSpace(readFile(t, filepath.Join(state, "pair-count"))) != "1" {
		t.Fatal("runtime installer paired more than once")
	}
	slot := filepath.Join(state, "releases", fixture.manifest.Tag)
	assertMode(t, filepath.Join(slot, "manifest.json"), 0o600)
	assertMode(t, filepath.Join(slot, "manifest.bundle"), 0o600)
	assertMode(t, filepath.Join(home, "Library/LaunchAgents/dev.cc-connect.runtime.plist"), 0o600)
	current, err := filepath.EvalSymlinks(filepath.Join(state, "current"))
	currentInfo, currentStatErr := os.Stat(current)
	slotInfo, slotStatErr := os.Stat(slot)
	if err != nil || currentStatErr != nil || slotStatErr != nil || !os.SameFile(currentInfo, slotInfo) {
		t.Fatalf("runtime current = %q, %v", current, err)
	}
}

type releaseFixture struct {
	directory string
	manifest  releasecontract.Manifest
}

func newReleaseFixture(t *testing.T, executableRuntime bool) releaseFixture {
	t.Helper()
	directory := t.TempDir()
	manifest := releasecontract.Manifest{Version: 1, Repository: releasecontract.Repository, Workflow: releasecontract.Workflow,
		Tag: "v0.1.0", CommitSHA: strings.Repeat("a", 40), RuntimeContractHash: runtimeprotocol.ContractHash,
		ControlSchema: controlstore.SchemaVersion, WorkspaceChatSchema: 3, GeneratedAt: time.Now().UTC()}
	for _, target := range [][3]string{{"control", "linux", "amd64"}, {"control", "linux", "arm64"}, {"server", "linux", "amd64"}, {"server", "linux", "arm64"}, {"deployhost", "linux", "amd64"}, {"deployhost", "linux", "arm64"}, {"runtime", "darwin", "amd64"}, {"runtime", "darwin", "arm64"}} {
		name := "cc-connect-" + strings.Join(target[:], "-") + ".tar.gz"
		binary := "cc-connect-" + target[0]
		contents := []byte(target[0])
		if target[0] == "runtime" && executableRuntime {
			contents = []byte("#!/bin/sh\nstate=\"$HOME/Library/Application Support/cc-connect-runtime\"\nmkdir -p \"$state\"\ncount=0\n[ ! -f \"$state/pair-count\" ] || count=$(cat \"$state/pair-count\")\nprintf '%s\\n' $((count + 1)) > \"$state/pair-count\"\nprintf '{\"server_url\":\"https://cc.example.com\",\"device_id\":\"device-test\"}\\n' > \"$state/identity.json\"\n")
		}
		var archive []byte
		if target[0] == "deployhost" {
			archive = makeArchiveFiles(t, map[string]archiveFile{
				"cc-connect-deploy-host": {mode: 0o755, contents: []byte("deployhost")},
				"compose.yaml":           {mode: 0o644, contents: []byte("services:\n  cc-connect:\n    image: ${CC_CONNECT_IMAGE}\n    volumes:\n      - /run/cc-connect-deploy:/run/cc-connect-deploy:ro\n")},
			})
		} else {
			archive = makeArchive(t, binary, contents)
		}
		if err := os.WriteFile(filepath.Join(directory, name), archive, 0o600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(archive)
		manifest.Artifacts = append(manifest.Artifacts, releasecontract.Artifact{Name: name, Component: target[0], OS: target[1], Arch: target[2], SHA256: hex.EncodeToString(digest[:]), Size: int64(len(archive))})
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "manifest.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "manifest.bundle"), []byte("signed"), 0o600); err != nil {
		t.Fatal(err)
	}
	return releaseFixture{directory: directory, manifest: manifest}
}

func makeArchive(t *testing.T, name string, contents []byte) []byte {
	return makeArchiveFiles(t, map[string]archiveFile{name: {mode: 0o755, contents: contents}})
}

type archiveFile struct {
	mode     int64
	contents []byte
}

func makeArchiveFiles(t *testing.T, files map[string]archiveFile) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, file := range files {
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: file.mode, Typeflag: tar.TypeReg, Size: int64(len(file.contents))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(file.contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func assertMode(t *testing.T, path string, expected os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if actual := info.Mode().Perm(); actual != expected {
		t.Fatalf("%s mode = %o, want %o", path, actual, expected)
	}
}
