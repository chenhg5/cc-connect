package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/daemon"
)

func runDaemon(args []string) {
	if len(args) == 0 {
		printDaemonUsage()
		os.Exit(1)
	}

	switch args[0] {
	case "install":
		daemonInstall(args[1:])
	case "uninstall":
		daemonUninstall()
	case "start":
		daemonStart()
	case "stop":
		daemonStop()
	case "restart":
		daemonRestart()
	case "status":
		daemonStatus()
	case "logs":
		daemonLogs(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown daemon command: %s\n\n", args[0])
		printDaemonUsage()
		os.Exit(1)
	}
}

// ── install ─────────────────────────────────────────────────

func daemonInstall(args []string) {
	cfg, force, err := parseDaemonInstallArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if err := daemon.Resolve(&cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	configPath := cfg.WorkDir + "/config.toml"
	if _, err := os.Stat(configPath); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: config.toml not found in %s\n", cfg.WorkDir)
		fmt.Fprintf(os.Stderr, "  Use --work-dir to specify the config directory or --config to point to the config file\n")
		os.Exit(1)
	}

	mgr, err := daemon.NewManager()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	st, _ := mgr.Status()
	if st != nil && st.Installed && !force {
		fmt.Fprintf(os.Stderr, "Service already installed. Use --force to reinstall.\n")
		os.Exit(1)
	}

	if err := mgr.Install(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Install failed: %v\n", err)
		os.Exit(1)
	}

	if err := daemon.SaveMeta(&daemon.Meta{
		LogFile:     cfg.LogFile,
		LogMaxSize:  cfg.LogMaxSize,
		WorkDir:     cfg.WorkDir,
		BinaryPath:  cfg.BinaryPath,
		InstalledAt: daemon.NowISO(),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to save metadata: %v\n", err)
	}

	fmt.Println("cc-connect daemon installed and started.")
	fmt.Println()
	fmt.Printf("  Platform:  %s\n", mgr.Platform())
	fmt.Printf("  Binary:    %s\n", cfg.BinaryPath)
	fmt.Printf("  WorkDir:   %s\n", cfg.WorkDir)
	fmt.Printf("  Log:       %s\n", cfg.LogFile)
	fmt.Printf("  LogMax:    %d MB\n", cfg.LogMaxSize/1024/1024)
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  cc-connect daemon status    - Check status")
	fmt.Println("  cc-connect daemon logs -f   - Follow logs")
	fmt.Println("  cc-connect daemon restart   - Restart")
	fmt.Println("  cc-connect daemon stop      - Stop")
	fmt.Println("  cc-connect daemon uninstall - Remove")
}

func parseDaemonInstallArgs(args []string) (daemon.Config, bool, error) {
	var cfg daemon.Config
	var force bool

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--force":
			force = true
		case arg == "--log-file":
			value, next, err := daemonInstallFlagValue(args, i, "--log-file")
			if err != nil {
				return daemon.Config{}, false, err
			}
			cfg.LogFile = value
			i = next
		case strings.HasPrefix(arg, "--log-file="):
			cfg.LogFile = strings.TrimPrefix(arg, "--log-file=")
		case arg == "--log-max-size":
			value, next, err := daemonInstallFlagValue(args, i, "--log-max-size")
			if err != nil {
				return daemon.Config{}, false, err
			}
			mb, err := strconv.Atoi(value)
			if err != nil {
				return daemon.Config{}, false, fmt.Errorf("invalid value for --log-max-size: %s", value)
			}
			cfg.LogMaxSize = int64(mb) * 1024 * 1024
			i = next
		case strings.HasPrefix(arg, "--log-max-size="):
			value := strings.TrimPrefix(arg, "--log-max-size=")
			mb, err := strconv.Atoi(value)
			if err != nil {
				return daemon.Config{}, false, fmt.Errorf("invalid value for --log-max-size: %s", value)
			}
			cfg.LogMaxSize = int64(mb) * 1024 * 1024
		case arg == "--work-dir":
			value, next, err := daemonInstallFlagValue(args, i, "--work-dir")
			if err != nil {
				return daemon.Config{}, false, err
			}
			cfg.WorkDir = value
			i = next
		case strings.HasPrefix(arg, "--work-dir="):
			cfg.WorkDir = strings.TrimPrefix(arg, "--work-dir=")
		case arg == "--config" || arg == "-config":
			value, next, err := daemonInstallFlagValue(args, i, arg)
			if err != nil {
				return daemon.Config{}, false, err
			}
			cfg.WorkDir = filepath.Dir(value)
			i = next
		case strings.HasPrefix(arg, "--config="):
			cfg.WorkDir = filepath.Dir(strings.TrimPrefix(arg, "--config="))
		case strings.HasPrefix(arg, "-config="):
			cfg.WorkDir = filepath.Dir(strings.TrimPrefix(arg, "-config="))
		default:
			return daemon.Config{}, false, fmt.Errorf("unknown flag: %s", arg)
		}
	}

	return cfg, force, nil
}

func daemonInstallFlagValue(args []string, index int, flagName string) (string, int, error) {
	next := index + 1
	if next >= len(args) {
		return "", index, fmt.Errorf("missing value for %s", flagName)
	}
	return args[next], next, nil
}

// ── uninstall ───────────────────────────────────────────────

func daemonUninstall() {
	mgr, err := daemon.NewManager()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	st, _ := mgr.Status()
	if st != nil && !st.Installed {
		fmt.Println("Service is not installed.")
		return
	}

	if err := mgr.Uninstall(); err != nil {
		fmt.Fprintf(os.Stderr, "Uninstall failed: %v\n", err)
		os.Exit(1)
	}

	daemon.RemoveMeta()
	fmt.Println("cc-connect daemon uninstalled.")
}

// ── start / stop / restart ──────────────────────────────────

func daemonStart() {
	mgr := mustManager()
	requireInstalled(mgr)
	if err := mgr.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Start failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("cc-connect daemon started.")
}

func daemonStop() {
	mgr := mustManager()
	requireInstalled(mgr)
	if err := mgr.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "Stop failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("cc-connect daemon stopped.")
}

func daemonRestart() {
	mgr := mustManager()
	requireInstalled(mgr)
	if err := mgr.Restart(); err != nil {
		fmt.Fprintf(os.Stderr, "Restart failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("cc-connect daemon restarted.")
}

func requireInstalled(mgr daemon.Manager) {
	st, _ := mgr.Status()
	if st == nil || !st.Installed {
		fmt.Fprintln(os.Stderr, "Service is not installed. Run first:")
		fmt.Fprintln(os.Stderr, "  cc-connect daemon install --work-dir /path/to/config-dir")
		os.Exit(1)
	}
}

// ── status ──────────────────────────────────────────────────

func daemonStatus() {
	mgr := mustManager()
	st, err := mgr.Status()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("cc-connect daemon status")
	fmt.Println()

	if !st.Installed {
		fmt.Println("  Status:    Not installed")
		fmt.Printf("  Platform:  %s\n", st.Platform)
		fmt.Println()
		fmt.Println("  Run: cc-connect daemon install")
		return
	}

	statusStr := "Stopped"
	if st.Running {
		statusStr = "Running"
	}
	fmt.Printf("  Status:    %s\n", statusStr)
	fmt.Printf("  Platform:  %s\n", st.Platform)
	if st.PID > 0 {
		fmt.Printf("  PID:       %d\n", st.PID)
	}

	if meta, err := daemon.LoadMeta(); err == nil {
		fmt.Printf("  Log:       %s\n", meta.LogFile)
		fmt.Printf("  WorkDir:   %s\n", meta.WorkDir)
		if t, err := time.Parse(time.RFC3339, meta.InstalledAt); err == nil {
			fmt.Printf("  Installed: %s\n", t.Format("2006-01-02 15:04:05"))
		}
	}
}

// ── logs ────────────────────────────────────────────────────

func daemonLogs(args []string) {
	follow := false
	lines := 100
	logFile := ""

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-f", "--follow":
			follow = true
		case "-n":
			i++
			if i < len(args) {
				if n, err := strconv.Atoi(args[i]); err == nil && n > 0 {
					lines = n
				}
			}
		case "--log-file":
			i++
			if i < len(args) {
				logFile = args[i]
			}
		}
	}

	if logFile == "" {
		if meta, err := daemon.LoadMeta(); err == nil {
			logFile = meta.LogFile
		} else {
			logFile = daemon.DefaultLogFile()
		}
	}

	if _, err := os.Stat(logFile); err != nil {
		fmt.Fprintf(os.Stderr, "Log file not found: %s\n", logFile)
		os.Exit(1)
	}

	if !follow {
		printLastLines(logFile, lines)
		return
	}

	printLastLines(logFile, lines)
	followFile(logFile)
}

func printLastLines(path string, n int) {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading log: %v\n", err)
		return
	}

	allLines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	start := 0
	if len(allLines) > n {
		start = len(allLines) - n
	}
	for _, line := range allLines[start:] {
		fmt.Println(line)
	}
}

func followFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	_, _ = f.Seek(0, io.SeekEnd)
	reader := bufio.NewReader(f)

	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			fmt.Print(line)
		}
		if err == io.EOF {
			time.Sleep(300 * time.Millisecond)
			reader.Reset(f)
			continue
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return
		}
	}
}

// ── helpers ─────────────────────────────────────────────────

func mustManager() daemon.Manager {
	mgr, err := daemon.NewManager()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	return mgr
}

func printDaemonUsage() {
	fmt.Println(`Usage: cc-connect daemon <command> [flags]

Commands:
  install     Install and start as system service
  uninstall   Remove system service
  start       Start the service
  stop        Stop the service
  restart     Restart the service
  status      Show service status
  logs        View log output

Install flags:
  --config PATH         Path to config.toml (uses its parent as work dir)
  --log-file PATH       Log file path (default: ~/.cc-connect/logs/cc-connect.log)
  --log-max-size N      Max log file size in MB (default: 10)
  --work-dir DIR        Directory containing config.toml (default: current dir)
  --force               Overwrite existing installation

Logs flags:
  -f, --follow          Follow log output (like tail -f)
  -n N                  Number of lines to show (default: 100)
  --log-file PATH       Custom log file path

Supported platforms:
  Linux (root)     - systemd system service (/etc/systemd/system/)
  Linux (non-root) - systemd user service (~/.config/systemd/user/)
  macOS            - launchd LaunchAgent`)
}
