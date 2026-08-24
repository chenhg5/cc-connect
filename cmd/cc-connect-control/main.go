package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/chenhg5/cc-connect/controlplane"
	"github.com/chenhg5/cc-connect/controlstore"
	"github.com/chenhg5/cc-connect/core"
	"github.com/chenhg5/cc-connect/releaseinstall"
	_ "github.com/chenhg5/cc-connect/web"
)

var (
	version   = "dev"
	commit    = "none"
	buildTime = "unknown"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "recover-activation" {
		if err := recoverActivation(os.Args[2:]); err != nil {
			slog.Error("cc-connect activation recovery failed", "error", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("cc-connect-control failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	listen := flag.String("listen", "127.0.0.1:9820", "control loopback listen address")
	publicURL := flag.String("public-url", "", "public HTTPS URL")
	controlDir := flag.String("control-dir", "/var/lib/cc-connect/control", "control state directory")
	appDir := flag.String("app-dir", "/var/lib/cc-connect/app", "business state directory")
	runDir := flag.String("run-dir", "/run/cc-connect", "private Unix socket directory")
	serverBinary := flag.String("server-binary", "/opt/cc-connect/current/cc-connect-server", "managed server binary")
	setupTokenFile := flag.String("setup-token-file", "/var/lib/cc-connect/control/setup-token", "首次启动一次性设置 Token 文件")
	releasesDir := flag.String("releases-dir", "/opt/cc-connect/releases", "signed release slots directory")
	currentLink := flag.String("current-link", "/opt/cc-connect/current", "active release symlink")
	cosignBinary := flag.String("cosign", "cosign", "cosign binary used for release verification")
	releaseBase := flag.String("release-base", "", "optional signed Release download base")
	releaseAPI := flag.String("release-api", "", "optional latest Release API")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("cc-connect-control %s\ncommit: %s\nbuilt: %s\n", version, commit, buildTime)
		return nil
	}
	setupToken := ""
	if raw, readErr := os.ReadFile(*setupTokenFile); readErr == nil {
		setupToken = strings.TrimSpace(string(raw))
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return fmt.Errorf("read one-time setup token: %w", readErr)
	}
	controlDatabase := filepath.Join(*controlDir, "control.db")
	activationPath := filepath.Join(*controlDir, "activation.json")
	pendingActivation, err := controlplane.ReadActivation(activationPath)
	if err != nil {
		return err
	}
	store, err := controlstore.Open(controlDatabase, setupToken)
	if err != nil {
		return err
	}
	if setupToken != "" {
		if err := os.Remove(*setupTokenFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = store.Close()
			return fmt.Errorf("consume one-time setup token file: %w", err)
		}
	}
	defer func() { _ = store.Close() }()
	preserveRunID := ""
	if pendingActivation != nil {
		preserveRunID = pendingActivation.RunID
	}
	if err := store.RecoverInterruptedRuns(context.Background(), preserveRunID); err != nil {
		return err
	}
	broker, err := controlplane.NewBroker(store)
	if err != nil {
		return err
	}
	serverSocket := filepath.Join(*runDir, "server.sock")
	runtimeSocket := filepath.Join(*runDir, "runtime.sock")
	controlServer, err := controlplane.New(controlplane.Config{
		ListenAddress: *listen, PublicURL: *publicURL, ServerSocket: serverSocket,
		RuntimeSocket: runtimeSocket, AppDirectory: *appDir, Assets: core.GetWebAssets(),
	}, store, broker)
	if err != nil {
		return err
	}
	configPath := filepath.Join(*appDir, "config.toml")
	supervisor, err := controlplane.NewSupervisor(controlplane.SupervisorConfig{
		Binary: *serverBinary, ConfigPath: configPath, ServerSocket: serverSocket,
		RuntimeSocket: runtimeSocket, LogDirectory: filepath.Join(*controlDir, "logs"),
	})
	if err != nil {
		return err
	}
	controlServer.SetSupervisor(supervisor)
	releaseClient, err := releaseinstall.New(releaseinstall.Config{ReleaseBase: *releaseBase, ReleaseAPI: *releaseAPI, Cosign: *cosignBinary})
	if err != nil {
		return err
	}
	restartControl := make(chan struct{}, 1)
	deployment, err := controlplane.NewDeploymentManager(controlplane.DeploymentConfig{
		ReleasesDirectory: *releasesDir, CurrentLink: *currentLink, ControlDatabase: controlDatabase,
		ActivationPath: activationPath, ReleaseClient: releaseClient,
		RestartControl: func() {
			select {
			case restartControl <- struct{}{}:
			default:
			}
		},
	}, store, broker, supervisor)
	if err != nil {
		return err
	}
	controlServer.SetDeploymentManager(deployment)
	if _, err := os.Stat(configPath); err == nil {
		if err := supervisor.Start(context.Background()); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect server configuration: %w", err)
	}
	if pendingActivation == nil {
		if err := deployment.RegisterCurrent(context.Background()); err != nil {
			return fmt.Errorf("register current signed release: %w", err)
		}
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = supervisor.Close(ctx)
	}()
	if err := controlServer.Start(); err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = controlServer.Close(ctx)
	}()
	if pendingActivation != nil {
		confirmCtx, confirmCancel := context.WithTimeout(context.Background(), 30*time.Second)
		err = deployment.ConfirmPending(confirmCtx)
		confirmCancel()
		if err != nil {
			return err
		}
	}
	slog.Info("cc-connect control started", "listen", *listen, "version", version)
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	select {
	case <-ctx.Done():
	case <-restartControl:
		slog.Info("signed release activation requested; handing control back to systemd")
	}
	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer shutdownCancel()
	return controlServer.Close(shutdownContext)
}

func recoverActivation(args []string) error {
	flags := flag.NewFlagSet("recover-activation", flag.ContinueOnError)
	record := flags.String("record", "/var/lib/cc-connect/control/activation.json", "activation record")
	releases := flags.String("releases-dir", "/opt/cc-connect/releases", "release slots")
	current := flags.String("current-link", "/opt/cc-connect/current", "active release link")
	database := flags.String("control-database", "/var/lib/cc-connect/control/control.db", "control database")
	if err := flags.Parse(args); err != nil {
		return err
	}
	return controlplane.RestoreActivation(*record, *releases, *current, *database)
}
