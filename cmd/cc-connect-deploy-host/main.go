package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/chenhg5/cc-connect/containerhost"
	"github.com/chenhg5/cc-connect/releaseinstall"
)

var (
	version   = "dev"
	commit    = "none"
	buildTime = "unknown"
)

func main() {
	if err := run(); err != nil {
		slog.Error("cc-connect-deploy-host failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	socket := flag.String("socket", containerhost.DefaultSocket, "control 容器访问的 Unix Socket")
	state := flag.String("state", "/var/lib/cc-connect-docker/deployer/state.json", "宿主部署状态文件")
	environment := flag.String("environment", "/var/lib/cc-connect-docker/deployment.env", "固定 Compose 环境文件")
	controlDatabase := flag.String("control-database", "/var/lib/cc-connect-docker/control/control.db", "control 数据库")
	composeFile := flag.String("compose-file", "/opt/cc-connect-docker/compose.yaml", "固定 Compose 文件")
	projectDirectory := flag.String("project-directory", "/opt/cc-connect-docker", "固定 Compose 项目目录")
	initialTag := flag.String("initial-tag", "", "首次启动时必须激活的已签名 Release tag")
	clientUID := flag.Int("client-uid", 10001, "允许连接 Unix Socket 的 control 容器 UID")
	clientGID := flag.Int("client-gid", 10001, "Unix Socket 所属 GID")
	dockerBinary := flag.String("docker", "docker", "Docker CLI")
	cosignBinary := flag.String("cosign", "cosign", "cosign CLI")
	releaseBase := flag.String("release-base", "", "可选签名 Release 下载地址")
	releaseAPI := flag.String("release-api", "", "可选 latest Release API")
	activationTimeout := flag.Duration("activation-timeout", 2*time.Minute, "候选 control 健康确认期限")
	showVersion := flag.Bool("version", false, "输出版本")
	flag.Parse()
	if *showVersion {
		fmt.Printf("cc-connect-deploy-host %s\ncommit: %s\nbuilt: %s\n", version, commit, buildTime)
		return nil
	}
	releaseClient, err := releaseinstall.New(releaseinstall.Config{ReleaseBase: *releaseBase, ReleaseAPI: *releaseAPI, Cosign: *cosignBinary})
	if err != nil {
		return err
	}
	runner, err := containerhost.NewCommandRunner(containerhost.CommandRunnerConfig{
		DockerBinary: *dockerBinary, CosignBinary: *cosignBinary, ComposeFile: *composeFile,
		Environment: *environment, ProjectDir: *projectDirectory, ProjectName: "cc-connect", ServiceName: "cc-connect",
	})
	if err != nil {
		return err
	}
	server, err := containerhost.NewServer(containerhost.ServerConfig{
		SocketPath: *socket, StatePath: *state, EnvironmentPath: *environment,
		ControlDatabase: *controlDatabase, InitialTag: *initialTag, ClientUID: *clientUID, ClientGID: *clientGID,
		ActivationTimeout: *activationTimeout, ReleaseClient: releaseClient, Runner: runner,
	})
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := server.Start(ctx); err != nil {
		return err
	}
	slog.Info("cc-connect deploy host started", "socket", *socket, "version", version)
	<-ctx.Done()
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer closeCancel()
	return server.Close(closeCtx)
}
