package releasecontract

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	Repository = "shusfun/cc-connect"
	Workflow   = ".github/workflows/release.yml"
)

type Artifact struct {
	Name      string `json:"name"`
	Component string `json:"component"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
}

type Manifest struct {
	Version             int        `json:"version"`
	Repository          string     `json:"repository"`
	Workflow            string     `json:"workflow"`
	Tag                 string     `json:"tag"`
	CommitSHA           string     `json:"commit_sha"`
	RuntimeContractHash string     `json:"runtime_contract_hash"`
	ControlSchema       int        `json:"control_schema"`
	WorkspaceChatSchema int        `json:"workspace_chat_schema"`
	GeneratedAt         time.Time  `json:"generated_at"`
	Artifacts           []Artifact `json:"artifacts"`
}

func Decode(raw []byte) (Manifest, error) {
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("release manifest: decode: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Manifest{}, errors.New("release manifest: trailing JSON is not allowed")
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (m Manifest) Validate() error {
	if m.Version != 1 || m.Repository != Repository || m.Workflow != Workflow {
		return errors.New("release manifest: unsupported version, repository, or workflow")
	}
	if !strings.HasPrefix(m.Tag, "v") || strings.TrimSpace(m.Tag) != m.Tag || len(m.Tag) < 2 {
		return errors.New("release manifest: valid v-prefixed tag is required")
	}
	if len(m.CommitSHA) != 40 {
		return errors.New("release manifest: full commit SHA is required")
	}
	if _, err := hex.DecodeString(m.CommitSHA); err != nil {
		return errors.New("release manifest: commit SHA is invalid")
	}
	if strings.TrimSpace(m.RuntimeContractHash) == "" || m.ControlSchema < 1 || m.WorkspaceChatSchema < 1 || m.GeneratedAt.IsZero() {
		return errors.New("release manifest: compatibility metadata is incomplete")
	}
	required := map[string]struct{}{
		"control/linux/amd64": {}, "control/linux/arm64": {},
		"server/linux/amd64": {}, "server/linux/arm64": {},
		"deployhost/linux/amd64": {}, "deployhost/linux/arm64": {},
		"runtime/darwin/amd64": {}, "runtime/darwin/arm64": {},
	}
	seen := make(map[string]struct{}, len(m.Artifacts))
	for _, artifact := range m.Artifacts {
		key := artifact.Component + "/" + artifact.OS + "/" + artifact.Arch
		if _, expected := required[key]; !expected {
			return fmt.Errorf("release manifest: unexpected artifact target %q", key)
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("release manifest: duplicate artifact target %q", key)
		}
		seen[key] = struct{}{}
		if strings.TrimSpace(artifact.Name) == "" || strings.ContainsAny(artifact.Name, `/\\`) || artifact.Size < 1 || len(artifact.SHA256) != 64 {
			return fmt.Errorf("release manifest: invalid artifact metadata for %q", key)
		}
		if _, err := hex.DecodeString(artifact.SHA256); err != nil || strings.ToLower(artifact.SHA256) != artifact.SHA256 {
			return fmt.Errorf("release manifest: invalid SHA-256 for %q", key)
		}
	}
	if len(seen) != len(required) {
		return errors.New("release manifest: required platform artifacts are incomplete")
	}
	return nil
}

func (m Manifest) Artifact(component, osName, arch string) (Artifact, bool) {
	for _, artifact := range m.Artifacts {
		if artifact.Component == component && artifact.OS == osName && artifact.Arch == arch {
			return artifact, true
		}
	}
	return Artifact{}, false
}
