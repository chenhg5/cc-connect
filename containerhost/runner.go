package containerhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

type Runner interface {
	PrepareImage(context.Context, string) (string, error)
	ComposeUp(context.Context) error
	ComposeStop(context.Context) error
}

type CommandRunnerConfig struct {
	DockerBinary string
	CosignBinary string
	ComposeFile  string
	Environment  string
	ProjectDir   string
	ProjectName  string
	ServiceName  string
}

type CommandRunner struct {
	config CommandRunnerConfig
}

func NewCommandRunner(config CommandRunnerConfig) (*CommandRunner, error) {
	if config.DockerBinary == "" {
		config.DockerBinary = "docker"
	}
	if config.CosignBinary == "" {
		config.CosignBinary = "cosign"
	}
	if config.ProjectName == "" {
		config.ProjectName = "cc-connect"
	}
	if config.ServiceName == "" {
		config.ServiceName = "cc-connect"
	}
	for _, value := range []string{config.ComposeFile, config.Environment, config.ProjectDir} {
		if !strings.HasPrefix(value, "/") {
			return nil, errors.New("container host runner: absolute compose, environment and project paths are required")
		}
	}
	return &CommandRunner{config: config}, nil
}

func (r *CommandRunner) PrepareImage(ctx context.Context, tag string) (string, error) {
	if !validTag(tag) {
		return "", errors.New("container host runner: valid release tag is required")
	}
	image := ImageRepository + ":" + tag
	if err := r.run(ctx, r.config.DockerBinary, "pull", image); err != nil {
		return "", err
	}
	command := exec.CommandContext(ctx, r.config.DockerBinary, "image", "inspect", "--format", "{{json .RepoDigests}}", image)
	raw, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("container host runner: inspect image digest: %w: %s", err, strings.TrimSpace(string(raw)))
	}
	var repoDigests []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &repoDigests); err != nil {
		return "", fmt.Errorf("container host runner: decode image digests: %w", err)
	}
	digest := ""
	for _, candidate := range repoDigests {
		if validDigestImage(candidate) {
			if digest != "" && digest != candidate {
				return "", errors.New("container host runner: image has multiple digests for the fixed repository")
			}
			digest = candidate
		}
	}
	if digest == "" {
		return "", errors.New("container host runner: Docker returned no digest for the fixed repository")
	}
	identity := "https://github.com/shusfun/cc-connect/.github/workflows/release.yml@refs/tags/" + tag
	if err := r.run(ctx, r.config.CosignBinary, "verify",
		"--certificate-oidc-issuer", "https://token.actions.githubusercontent.com",
		"--certificate-identity", identity, digest); err != nil {
		return "", err
	}
	return digest, nil
}

func (r *CommandRunner) ComposeUp(ctx context.Context) error {
	return r.compose(ctx, "up", "-d", "--no-build", r.config.ServiceName)
}

func (r *CommandRunner) ComposeStop(ctx context.Context) error {
	return r.compose(ctx, "stop", "--timeout", "45", r.config.ServiceName)
}

func (r *CommandRunner) compose(ctx context.Context, arguments ...string) error {
	prefix := []string{"compose", "--project-name", r.config.ProjectName, "--project-directory", r.config.ProjectDir,
		"--env-file", r.config.Environment, "--file", r.config.ComposeFile}
	return r.run(ctx, r.config.DockerBinary, append(prefix, arguments...)...)
}

func (r *CommandRunner) run(ctx context.Context, binary string, arguments ...string) error {
	command := exec.CommandContext(ctx, binary, arguments...)
	raw, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("container host runner: %s failed: %w: %s", filepathBase(binary), err, strings.TrimSpace(string(raw)))
	}
	return nil
}

func filepathBase(path string) string {
	if index := strings.LastIndexAny(path, `/\\`); index >= 0 {
		return path[index+1:]
	}
	return path
}

func validDigestImage(value string) bool {
	prefix := ImageRepository + "@sha256:"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+64 {
		return false
	}
	for _, character := range value[len(prefix):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
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
