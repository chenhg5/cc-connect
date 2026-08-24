package containerhost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandRunnerUsesOnlyFixedRepositoryIdentityAndComposeTarget(t *testing.T) {
	directory := t.TempDir()
	logPath := filepath.Join(directory, "commands.log")
	digest := ImageRepository + "@sha256:" + strings.Repeat("a", 64)
	docker := filepath.Join(directory, "docker")
	cosign := filepath.Join(directory, "cosign")
	writeRunnerScript(t, docker, `#!/bin/sh
printf 'docker' >> "$COMMAND_LOG"
for argument in "$@"; do printf '\t%s' "$argument" >> "$COMMAND_LOG"; done
printf '\n' >> "$COMMAND_LOG"
if [ "${1:-}" = image ] && [ "${2:-}" = inspect ]; then
  printf '["example.invalid/other@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","%s"]\n' "$EXPECTED_DIGEST"
fi
`)
	writeRunnerScript(t, cosign, `#!/bin/sh
printf 'cosign' >> "$COMMAND_LOG"
for argument in "$@"; do printf '\t%s' "$argument" >> "$COMMAND_LOG"; done
printf '\n' >> "$COMMAND_LOG"
`)
	t.Setenv("COMMAND_LOG", logPath)
	t.Setenv("EXPECTED_DIGEST", digest)
	runner, err := NewCommandRunner(CommandRunnerConfig{DockerBinary: docker, CosignBinary: cosign,
		ComposeFile: "/opt/cc-connect-docker/compose.yaml", Environment: "/var/lib/cc-connect-docker/deployment.env",
		ProjectDir: "/opt/cc-connect-docker", ProjectName: "cc-connect", ServiceName: "cc-connect"})
	if err != nil {
		t.Fatal(err)
	}
	actual, err := runner.PrepareImage(context.Background(), "v0.2.0")
	if err != nil || actual != digest {
		t.Fatalf("PrepareImage() = %q, %v", actual, err)
	}
	if err := runner.ComposeUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := runner.ComposeStop(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	commands := string(raw)
	for _, required := range []string{
		"docker\tpull\tghcr.io/shusfun/cc-connect:v0.2.0",
		"docker\timage\tinspect\t--format\t{{json .RepoDigests}}\tghcr.io/shusfun/cc-connect:v0.2.0",
		"cosign\tverify\t--certificate-oidc-issuer\thttps://token.actions.githubusercontent.com\t--certificate-identity\thttps://github.com/shusfun/cc-connect/.github/workflows/release.yml@refs/tags/v0.2.0\t" + digest,
		"docker\tcompose\t--project-name\tcc-connect\t--project-directory\t/opt/cc-connect-docker\t--env-file\t/var/lib/cc-connect-docker/deployment.env\t--file\t/opt/cc-connect-docker/compose.yaml\tup\t-d\t--no-build\tcc-connect",
		"docker\tcompose\t--project-name\tcc-connect\t--project-directory\t/opt/cc-connect-docker\t--env-file\t/var/lib/cc-connect-docker/deployment.env\t--file\t/opt/cc-connect-docker/compose.yaml\tstop\t--timeout\t45\tcc-connect",
	} {
		if !strings.Contains(commands, required) {
			t.Fatalf("command log missing %q:\n%s", required, commands)
		}
	}
}

func writeRunnerScript(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
}
