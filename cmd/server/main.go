// Package main starts the Papers Manager HTTP API.
//
// @title           Papers Manager API
// @version         1.0
// @description     Paper upload and reading service API.
// @BasePath        /
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"paperMaster/internal/application/repository"
	"paperMaster/internal/application/service"
	"paperMaster/internal/config"
	"paperMaster/internal/container"
	"paperMaster/internal/logger"
	"paperMaster/internal/router"

	"github.com/gin-gonic/gin"
	"github.com/hibiken/asynq"
	"gorm.io/gorm"
)

func main() {
	if err := config.LoadDotEnv(".env"); err != nil {
		fmt.Fprintf(os.Stderr, "load .env: %v\n", err)
		os.Exit(1)
	}
	logger.InitFromEnv()

	dependencies := container.Build()
	var catalogDispatcher *service.CatalogDispatcher
	var housekeeping *service.DocumentHousekeepingService
	if err := dependencies.Invoke(func(dispatcher *service.CatalogDispatcher, h *service.DocumentHousekeepingService) {
		catalogDispatcher = dispatcher
		housekeeping = h
	}); err != nil {
		logger.Error(context.Background(), "resolve catalog dispatcher", "error", err)
		return
	}
	// The in-process queue is lost on restart; fail durable rows before any
	// worker can observe them as queued/running.
	if err := housekeeping.FailInterruptedCatalogWork(context.Background()); err != nil {
		logger.Error(context.Background(), "fail interrupted catalog work", "error", err)
		return
	}
	// The dispatcher must accept submissions before the Asynq workers start
	// consuming stage tasks.
	catalogDispatcher.Start(context.Background())
	defer catalogDispatcher.Stop()
	if err := dependencies.Invoke(router.RunAsynqServer); err != nil {
		logger.Error(context.Background(), "start asynq server", "error", err)
		return
	}

	err := dependencies.Invoke(func(
		cfg config.Config,
		router *gin.Engine,
		db *gorm.DB,
		documentHousekeeping *service.DocumentHousekeepingService,
		catalogDispatcher *service.CatalogDispatcher,
		worker *asynq.Server,
		taskClient *asynq.Client,
	) error {
		// Defer order is intentional (LIFO): stop consuming, drain the
		// dispatcher while the task client is still open, then close the queue
		// and database. Drained jobs are persisted as failed before DB close.
		defer repository.CloseDatabase(db)
		defer taskClient.Close()
		defer catalogDispatcher.Stop()
		defer worker.Shutdown()
		if err := documentHousekeeping.Start(context.Background()); err != nil {
			logger.Warn(context.Background(), "start document housekeeping failed", "error", err)
		}
		defer documentHousekeeping.Stop()

		server := &http.Server{Handler: router, ReadHeaderTimeout: 5 * time.Second}
		listener, err := listenWithRetry(cfg.HTTPAddress, 10, 300*time.Millisecond)
		if err != nil {
			return fmt.Errorf("start listener: %w", err)
		}

		signals := make(chan os.Signal, 2)
		signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(signals)
		go shutdownOnSignal(server, signals)

		logger.Info(context.Background(), "server is running", "address", cfg.HTTPAddress)
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("serve API: %w", err)
		}
		return nil
	})
	if err != nil {
		logger.Error(context.Background(), "run application", "error", err)
	}
}

func shutdownOnSignal(server *http.Server, signals <-chan os.Signal) {
	signalReceived := <-signals
	logger.Info(context.Background(), "received shutdown signal", "signal", signalReceived)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error(context.Background(), "graceful shutdown failed", "error", err)
		_ = server.Close()
	}
}
