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
	"paperMaster/internal/runtime/desktop"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	defaultRoot, err := desktop.DefaultDataDir()
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	bundle, err := desktop.BundleDirectory(executable)
	if err != nil {
		return err
	}
	root := flag.String("data-dir", defaultRoot, "private writable application directory")
	install := flag.String("install-dir", bundle, "read-only runtime bundle directory")
	initialize := flag.Bool("initialize", false, "perform explicit first-time setup and exit")
	configure := flag.Bool("configure", false, "create/validate runtime.json and exit")
	browser := flag.Bool("open-browser", true, "open the independent management page")
	envFile := flag.String("env-file", filepath.Join(bundle, ".launcher_env"),
		"optional Launcher-only environment file")
	maintenance := flag.String("maintenance", "", "offline install, retry, restore, cleanup, status, delete-checkpoint or uninstall")
	program := flag.String("program-dir", "", "managed program directory, separate from user data")
	source := flag.String("source-bundle", bundle, "sealed directory to install or update")
	checkpoint := flag.String("checkpoint-id", "", "managed upgrade checkpoint to restore")
	confirmRestore := flag.Bool("confirm-restore", false, "confirm replacement of managed user data")
	shortcuts := flag.String("shortcut-dir", "", "optional per-user desktop/application shortcut directory")
	confirmDelete := flag.Bool("confirm-delete", false, "confirm explicit deletion of a managed checkpoint")
	flag.Parse()
	if *maintenance != "" {
		if *configure || *initialize {
			return fmt.Errorf("maintenance cannot be combined with configure or initialize")
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
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
	cfg, err := openConfig(*root, *install)
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
	app := desktop.New(cfg)
	if *initialize {
		app.Initialize()
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
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
	if *browser {
		if err := openBrowser(app.ManagementURL()); err != nil {
			fmt.Fprintln(os.Stderr, "Open the management address in your browser:", err)
		}
	}
	app.Start()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	select {
	case <-ctx.Done():
	case <-app.QuitRequested():
	case err := <-serveErr:
		fmt.Fprintln(os.Stderr, "management server stopped:", err)
	}
	app.Quit()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 4*cfg.StopTimeout+time.Second)
	defer stopCancel()
	if err := app.Wait(stopCtx); err != nil {
		return err
	}
	return server.Shutdown(stopCtx)
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
