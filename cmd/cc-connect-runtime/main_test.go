package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

type runtimeValidationCatalog struct {
	core.WorkspaceCatalogProvider
	workspaces []core.Workspace
	err        error
}

func (c runtimeValidationCatalog) ListWorkspaces(context.Context) ([]core.Workspace, error) {
	return c.workspaces, c.err
}

type runtimeValidationBackend struct {
	core.NativeConversationBackend
	workspaceRef string
	err          error
}

func (b *runtimeValidationBackend) NativeRuntimeCatalog(_ context.Context, workspace core.Workspace) (core.NativeRuntimeCatalog, error) {
	b.workspaceRef = workspace.Ref
	return core.NativeRuntimeCatalog{}, b.err
}

func TestValidateCodexRuntimeRequiresAvailableProjectAndNativeCatalog(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		backend := &runtimeValidationBackend{}
		err := validateCodexRuntime(context.Background(), runtimeValidationCatalog{workspaces: []core.Workspace{
			{Ref: "invalid", Available: false}, {Ref: "valid", Available: true},
		}}, backend)
		if err != nil || backend.workspaceRef != "valid" {
			t.Fatalf("validateCodexRuntime() = %v, workspace=%q", err, backend.workspaceRef)
		}
	})

	t.Run("no available project", func(t *testing.T) {
		err := validateCodexRuntime(context.Background(), runtimeValidationCatalog{workspaces: []core.Workspace{{Ref: "invalid"}}}, &runtimeValidationBackend{})
		if err == nil || !strings.Contains(err.Error(), "没有有效项目") {
			t.Fatalf("validateCodexRuntime() error = %v", err)
		}
	})

	t.Run("native authentication", func(t *testing.T) {
		err := validateCodexRuntime(context.Background(), runtimeValidationCatalog{workspaces: []core.Workspace{{Ref: "valid", Available: true}}}, &runtimeValidationBackend{err: errors.New("not authenticated")})
		if err == nil || !strings.Contains(err.Error(), "认证") {
			t.Fatalf("validateCodexRuntime() error = %v", err)
		}
	})
}
