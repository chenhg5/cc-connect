package releasecontract

import (
	"strings"
	"testing"
	"time"
)

func validManifest() Manifest {
	manifest := Manifest{
		Version: 1, Repository: Repository, Workflow: Workflow, Tag: "v0.1.0",
		CommitSHA: strings.Repeat("a", 40), RuntimeContractHash: "contract", ControlSchema: 3,
		WorkspaceChatSchema: 3, GeneratedAt: time.Now(),
	}
	for _, target := range [][3]string{
		{"control", "linux", "amd64"}, {"control", "linux", "arm64"},
		{"server", "linux", "amd64"}, {"server", "linux", "arm64"},
		{"runtime", "darwin", "amd64"}, {"runtime", "darwin", "arm64"},
	} {
		manifest.Artifacts = append(manifest.Artifacts, Artifact{
			Name:      "cc-connect-" + target[0] + "-" + target[1] + "-" + target[2] + ".tar.gz",
			Component: target[0], OS: target[1], Arch: target[2], SHA256: strings.Repeat("b", 64), Size: 1,
		})
	}
	return manifest
}

func TestManifestValidateRequiresExactRepositoryWorkflowAndArtifacts(t *testing.T) {
	manifest := validManifest()
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
	manifest.Repository = "chenhg5/cc-connect"
	if err := manifest.Validate(); err == nil {
		t.Fatal("upstream repository manifest was accepted")
	}
	manifest = validManifest()
	manifest.Artifacts = manifest.Artifacts[:5]
	if err := manifest.Validate(); err == nil {
		t.Fatal("incomplete artifact set was accepted")
	}
}

func TestDecodeRejectsUnknownAndTrailingJSON(t *testing.T) {
	if _, err := Decode([]byte(`{"version":1,"unknown":true}`)); err == nil {
		t.Fatal("unknown manifest field was accepted")
	}
	if _, err := Decode([]byte(`{} {}`)); err == nil {
		t.Fatal("trailing manifest JSON was accepted")
	}
}
