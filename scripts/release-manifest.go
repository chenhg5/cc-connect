package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/controlstore"
	"github.com/chenhg5/cc-connect/releasecontract"
	"github.com/chenhg5/cc-connect/runtimeprotocol"
	"github.com/chenhg5/cc-connect/storage/workspacechat"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	directory := requiredEnvironment("RELEASE_DIST")
	tag := requiredEnvironment("RELEASE_TAG")
	commit := requiredEnvironment("RELEASE_COMMIT")
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	var artifacts []releasecontract.Artifact
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".tar.gz") {
			continue
		}
		artifact, ok := artifactFromName(entry.Name())
		if !ok {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		digest := sha256.Sum256(raw)
		artifact.SHA256 = hex.EncodeToString(digest[:])
		artifact.Size = info.Size()
		artifacts = append(artifacts, artifact)
	}
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Name < artifacts[j].Name })
	manifest := releasecontract.Manifest{
		Version: 1, Repository: releasecontract.Repository, Workflow: releasecontract.Workflow,
		Tag: tag, CommitSHA: commit, RuntimeContractHash: runtimeprotocol.ContractHash,
		ControlSchema: controlstore.SchemaVersion, WorkspaceChatSchema: workspacechat.SchemaVersion,
		GeneratedAt: time.Now().UTC(), Artifacts: artifacts,
	}
	if err := manifest.Validate(); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(directory, "manifest.json"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func artifactFromName(name string) (releasecontract.Artifact, bool) {
	base := strings.TrimSuffix(name, ".tar.gz")
	parts := strings.Split(base, "-")
	if len(parts) != 5 || parts[0] != "cc" || parts[1] != "connect" {
		return releasecontract.Artifact{}, false
	}
	component, osName, arch := parts[2], parts[3], parts[4]
	return releasecontract.Artifact{Name: name, Component: component, OS: osName, Arch: arch}, true
}

func requiredEnvironment(name string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		fmt.Fprintf(os.Stderr, "%s is required\n", name)
		os.Exit(2)
	}
	return value
}
