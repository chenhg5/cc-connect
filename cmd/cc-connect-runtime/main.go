package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/chenhg5/cc-connect/agent/codex"
	"github.com/chenhg5/cc-connect/core"
	"github.com/chenhg5/cc-connect/releaseinstall"
	"github.com/chenhg5/cc-connect/runtimeclient"
	"github.com/chenhg5/cc-connect/runtimeidentity"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("cc-connect-runtime failed", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 && args[0] == "version" {
		fmt.Println(version)
		return nil
	}
	pairing := len(args) > 0 && args[0] == "pair"
	if pairing {
		args = args[1:]
	}
	flags := flag.NewFlagSet("cc-connect-runtime", flag.ContinueOnError)
	stateDir := flags.String("state-dir", defaultStateDirectory(), "Runtime 状态目录（私钥始终保存在 macOS Keychain）")
	serverURL := flags.String("server", "", "control 的公开 HTTPS URL")
	pairingCode := flags.String("code", "", "Web 生成的一次性配对码")
	deviceName := flags.String("name", hostname(), "设备显示名称")
	codexHome := flags.String("codex-home", "", "可选 CODEX_HOME")
	if err := flags.Parse(args); err != nil {
		return err
	}
	store, err := runtimeidentity.New(*stateDir)
	if err != nil {
		return err
	}
	if pairing {
		if strings.TrimSpace(*serverURL) == "" || strings.TrimSpace(*pairingCode) == "" {
			return errors.New("pair requires --server and --code")
		}
		privateKey, err := store.LoadOrCreateKey()
		if err != nil {
			return err
		}
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		deviceID, err := runtimeclient.Pair(ctx, *serverURL, *pairingCode, *deviceName, privateKey.Public().(ed25519.PublicKey), false)
		if err != nil {
			return err
		}
		if err := store.SaveMetadata(*serverURL, deviceID); err != nil {
			return err
		}
		fmt.Printf("Runtime 已配对：%s\n", deviceID)
		return nil
	}
	identity, err := store.Load()
	if err != nil {
		return fmt.Errorf("请先在 Web 生成配对码并运行 cc-connect-runtime pair: %w", err)
	}
	releaseClient, err := releaseinstall.New(releaseinstall.Config{})
	if err != nil {
		return err
	}
	updater, err := runtimeclient.NewUpdateManager(runtimeclient.UpdateManagerConfig{StateDirectory: *stateDir, ReleaseClient: releaseClient})
	if err != nil {
		return err
	}
	defer func() {
		if rollbackErr := updater.RollbackStartupFailure(); rollbackErr != nil {
			slog.Error("Runtime 未确认更新回滚失败", "error", rollbackErr)
		}
	}()
	agentValue, err := codex.New(map[string]any{"backend": "app_server", "codex_home": strings.TrimSpace(*codexHome)})
	if err != nil {
		return err
	}
	defer func() { _ = agentValue.Stop() }()
	catalog, okCatalog := agentValue.(core.WorkspaceCatalogProvider)
	validator, okValidator := agentValue.(core.WorkspaceAccessValidator)
	backend, okBackend := agentValue.(core.NativeConversationBackend)
	settings, okSettings := agentValue.(core.NativeConversationSettingsController)
	turns, okTurns := agentValue.(core.NativeConversationTurnController)
	realtime, _ := agentValue.(core.NativeConversationRealtimeController)
	if !okCatalog || !okValidator || !okBackend || !okSettings || !okTurns {
		return errors.New("本机 Codex 后端缺少原生会话能力，请更新 cc-connect-runtime")
	}
	if err := validateCodexRuntime(context.Background(), catalog, backend); err != nil {
		return err
	}
	handler, err := runtimeclient.NewHandler(runtimeclient.Dependencies{Catalog: catalog, Validator: validator, Backend: backend, Settings: settings, Turns: turns, Realtime: realtime, Updater: updater})
	if err != nil {
		return err
	}
	client, err := runtimeclient.NewClient(runtimeclient.ClientConfig{ServerURL: identity.ServerURL, DeviceID: identity.DeviceID, PrivateKey: identity.PrivateKey, Handler: handler, Checkpoint: store})
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	clientResult := make(chan error, 1)
	go func() { clientResult <- client.Run(ctx) }()
	select {
	case err := <-clientResult:
		return err
	case <-updater.RestartRequested():
		return client.Close()
	}
}

func validateCodexRuntime(ctx context.Context, catalog core.WorkspaceCatalogProvider, backend core.NativeConversationBackend) error {
	workspaces, err := catalog.ListWorkspaces(ctx)
	if err != nil {
		return fmt.Errorf("无法读取 Codex App 项目状态: %w", err)
	}
	var probeErrors []error
	for _, workspace := range workspaces {
		if !workspace.Available {
			continue
		}
		if _, err := backend.NativeRuntimeCatalog(ctx, workspace); err != nil {
			probeErrors = append(probeErrors, fmt.Errorf("项目 %s: %w", workspace.Ref, err))
			continue
		}
		return nil
	}
	if len(probeErrors) > 0 {
		return fmt.Errorf("codex CLI、认证或 App Server 校验失败: %w", errors.Join(probeErrors...))
	}
	return errors.New("codex App 状态中没有有效项目")
}

func defaultStateDirectory() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".cc-connect-runtime"
	}
	return filepath.Join(home, "Library", "Application Support", "cc-connect-runtime")
}

func hostname() string {
	name, err := os.Hostname()
	if err != nil || strings.TrimSpace(name) == "" {
		return "Mac"
	}
	return name
}
