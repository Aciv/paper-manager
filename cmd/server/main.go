// Package main starts the shared Papers Manager server runtime.
//
// @title           paper_manager API
// @version         1.0.0
// @description     Paper upload and reading service API.
// @BasePath        /
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"paperMaster/internal/config"
	"paperMaster/internal/logger"
	serverruntime "paperMaster/internal/runtime/server"
)

func main() {
	if err := run(); err != nil {
		logger.Error(context.Background(), "run application failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	if os.Getenv("PAPERS_SKIP_DOTENV") != "1" {
		if err := config.LoadDotEnv(".env"); err != nil {
			return fmt.Errorf("load .env: %w", err)
		}
	}
	logger.InitFromEnv()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	parentClosed := parentShutdown(os.Getenv("PAPERS_PARENT_STDIN") == "1", os.Stdin)
	go func() {
		select {
		case <-signals:
		case <-parentClosed:
		case <-ctx.Done():
			return
		}
		cancel()
		select {
		case <-signals:
			logger.Warn(context.Background(), "second shutdown signal; forcing exit")
			os.Exit(1)
		case <-time.After(2 * time.Minute):
		}
	}()
	app := serverruntime.New()
	if os.Getenv("PAPERS_MAINTENANCE_PROBE") == "1" {
		app = serverruntime.NewMaintenanceProbe()
	}
	if err := app.Start(ctx); err != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer stopCancel()
		_ = app.Shutdown(stopCtx)
		return err
	}
	return app.Wait(context.Background())
}

// EOF is portable across Windows/Unix and also detects Launcher death. It is
// opt-in so normal developer/Compose stdin behavior remains unchanged.
func parentShutdown(enabled bool, input io.Reader) <-chan struct{} {
	if !enabled {
		return nil
	}
	closed := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, input)
		close(closed)
	}()
	return closed
}
