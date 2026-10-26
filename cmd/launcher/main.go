// Command launcher owns the application's private runtime and an independent
// loopback management page. It does not install global services.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"paperMaster/internal/config"
	"paperMaster/internal/logger"
	"paperMaster/internal/runtime/desktop"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() (runErr error) {
	// Install before opening the browser or starting services: a console close
	// during startup must follow the same shutdown path as a later interruption.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	bundle, err := desktop.BundleDirectory(executable)
	if err != nil {
		return err
	}
	defaultRoot, err := desktop.DefaultBundleDataDir(bundle)
	if err != nil {
		return err
	}
	root := flag.String("data-dir", defaultRoot, "private writable application directory")
	install := flag.String("install-dir", bundle, "read-only runtime bundle directory")
	initialize := flag.Bool("initialize", false, "perform explicit first-time setup and exit")
	configure := flag.Bool("configure", false, "create/validate runtime.json and exit")
	browser := flag.Bool("open-browser", true, "open the independent management page")
	envFile := flag.String("env-file", filepath.Join(desktop.DeliveryDirectory(bundle), ".launcher_env"),
		"optional Launcher-only environment file")
	maintenance := flag.String("maintenance", "", "offline install, retry, restore, cleanup, status, delete-checkpoint or uninstall")
	program := flag.String("program-dir", "", "managed program directory, separate from user data")
	source := flag.String("source-bundle", bundle, "sealed directory to install or update")
	checkpoint := flag.String("checkpoint-id", "", "managed upgrade checkpoint to restore")
	confirmRestore := flag.Bool("confirm-restore", false, "confirm replacement of managed user data")
	shortcuts := flag.String("shortcut-dir", "", "optional per-user desktop/application shortcut directory")
	confirmDelete := flag.Bool("confirm-delete", false, "confirm explicit deletion of a managed checkpoint")
	uninstallDir := flag.String("uninstall-dir", "", "remove a stopped unpacked program; retain user data")
	flag.Parse()
	if *uninstallDir != "" {
		return desktop.RemoveUnpacked(*uninstallDir)
	}
	if *maintenance != "" {
		if *configure || *initialize {
			return fmt.Errorf("maintenance cannot be combined with configure or initialize")
		}
		return desktop.RunMaintenance(ctx, desktop.MaintenanceOptions{
			Operation: *maintenance, DataDir: *root, ProgramDir: *program,
			SourceBundle: *source, Checkpoint: *checkpoint, ConfirmRestore: *confirmRestore,
			ShortcutDir:   *shortcuts,
			ConfirmDelete: *confirmDelete,
		})
	}
	if *configure && *initialize {
		return fmt.Errorf("--configure and --initialize are separate operations")
	}
	if err := config.LoadDotEnv(*envFile); err != nil {
		return err
	}
	explicit := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	for name, field := range map[string]*string{"data-dir": root, "install-dir": install} {
		key := map[string]string{
			"data-dir": "LAUNCHER_DATA_DIR", "install-dir": "LAUNCHER_INSTALL_DIR",
		}[name]
		if value := os.Getenv(key); !explicit[name] && value != "" {
			if !filepath.IsAbs(value) {
				return fmt.Errorf("%s must be an absolute path", key)
			}
			*field = value
		}
	}
	if value := os.Getenv("LAUNCHER_OPEN_BROWSER"); !explicit["open-browser"] && value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("LAUNCHER_OPEN_BROWSER must be true or false")
		}
		*browser = parsed
	}
	if _, err := os.Stat(filepath.Join(*install, "bundle.json")); err == nil {
		bundleLock, err := desktop.AcquireBundle(*install)
		if err != nil {
			return err
		}
		defer bundleLock.Close()
	}
	lock, err := desktop.Acquire(*root)
	if err != nil {
		return fmt.Errorf("lock runtime: %w; see %s", err, filepath.Join(*root, "runtime.json"))
	}
	defer lock.Close()
	if err := desktop.CheckMaintenanceReady(*root); err != nil {
		return err
	}
	openConfig := desktop.OpenBundleConfig
	for _, key := range []string{
		"LAUNCHER_SERVER", "LAUNCHER_PYTHON", "LAUNCHER_PARSER_DIR",
		"LAUNCHER_POSTGRES_BIN", "LAUNCHER_REDIS", "LAUNCHER_WEB_DIR",
	} {
		if os.Getenv(key) != "" {
			// Explicit component overrides remain available to source developers.
			openConfig = desktop.OpenConfig
			break
		}
	}
	var cfg desktop.Config
	err = desktop.RunWithProgress(ctx, "校验程序文件、检查数据兼容性并适配已有配置", func() error {
		var configErr error
		cfg, configErr = openConfig(*root, *install)
		return configErr
	})
	if err != nil {
		return err
	}
	cfg, err = desktop.ApplyEnvironment(cfg)
	if err != nil {
		return err
	}
	if err := desktop.SaveConfig(cfg); err != nil {
		return err
	}
	if *configure {
		fmt.Println(filepath.Join(cfg.DataDir, "runtime.json"))
		return nil
	}
	closeLogging, err := desktop.ConfigureLogging(cfg)
	if err != nil {
		return err
	}
	defer func() {
		if runErr != nil {
			// Runtime errors can contain paths, but private credentials never
			// belong in either console output or the persisted Launcher log.
			logger.Error(ctx, "Launcher 运行失败", "error", runErr)
		}
		closeLogging()
	}()
	logger.Info(ctx, "Launcher 已启动", "data_dir", cfg.DataDir,
		"config_file", filepath.Join(cfg.DataDir, "runtime.json"),
		"log_dir", filepath.Join(cfg.DataDir, "logs"))
	logger.Info(ctx, "配置已就绪；已有论文和笔记将继续使用，数据库结构检查或升级在服务启动时执行")
	app := desktop.New(cfg)
	app.EnableConfigReload()
	if *initialize {
		app.Initialize()
		if err := app.Wait(ctx); err != nil {
			app.Stop()
			drain, stop := context.WithTimeout(context.Background(), cfg.StopTimeout)
			defer stop()
			_ = app.Wait(drain)
			return err
		}
		state := app.Snapshot()
		if !state.Initialized || state.State == "failed" {
			return fmt.Errorf("initialization failed: %s", state.Error)
		}
		fmt.Println("Initialization completed; services have not been started.")
		return nil
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.Ports.Manager))
	if err != nil {
		return fmt.Errorf("management port occupied; see runtime.json: %w", err)
	}
	server := &http.Server{
		Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second,
		IdleTimeout: 30 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	fmt.Printf("Management: %s\nConfig: %s\n",
		app.ManagementURL(), filepath.Join(cfg.DataDir, "runtime.json"))
	logger.Info(ctx, "管理页已就绪", "url", app.ManagementURL())
	logger.Info(ctx, "关闭浏览器不会停止服务；结束使用请在管理页退出 Launcher，或在此按 Ctrl+C")
	if *browser {
		go func() {
			if err := openBrowser(app.ManagementURL()); err != nil {
				logger.Warn(ctx, "自动打开浏览器失败，请手动打开管理地址", "url", app.ManagementURL(), "error", err)
			}
		}()
	}
	app.Start()
	select {
	case <-ctx.Done():
		logger.Info(context.Background(), "收到系统退出信号，正在关闭所有服务")
	case <-app.QuitRequested():
		logger.Info(ctx, "收到管理页退出请求，正在关闭所有服务")
	case err := <-serveErr:
		logger.Error(ctx, "管理页服务已退出", "error", err)
	}
	app.Quit()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 4*cfg.StopTimeout+time.Second)
	defer stopCancel()
	if err := app.Wait(stopCtx); err != nil {
		return err
	}
	if err := server.Shutdown(stopCtx); err != nil {
		return err
	}
	logger.Info(context.Background(), "Launcher 已退出，数据和日志已保留", "data_dir", cfg.DataDir)
	return nil
}

func openBrowser(url string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		command = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		command = exec.CommandContext(ctx, "open", url)
	default:
		command = exec.CommandContext(ctx, "xdg-open", url)
	}
	return command.Run()
}
