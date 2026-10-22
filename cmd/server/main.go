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
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"paperMaster/internal/application/repository"
	"paperMaster/internal/application/service"
	"paperMaster/internal/config"
	"paperMaster/internal/container"
	"paperMaster/internal/limit"
	"paperMaster/internal/logger"
	mcpadapter "paperMaster/internal/mcp"
	"paperMaster/internal/router"
	"paperMaster/internal/types/interfaces"

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
	// Running workers died with the previous process, and the in-process
	// catalog queue is lost on restart. Fail those durable rows before any
	// worker can observe them as running.
	if err := housekeeping.FailInterruptedWork(context.Background()); err != nil {
		logger.Error(context.Background(), "fail interrupted processing work", "error", err)
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
		engine *gin.Engine,
		db *gorm.DB,
		documentHousekeeping *service.DocumentHousekeepingService,
		catalogDispatcher *service.CatalogDispatcher,
		worker *asynq.Server,
		taskClient *asynq.Client,
		sharedLimiter *limit.Engine,
		recognizer interfaces.DocumentRecognizer,
		extractor interfaces.PaperMetadataExtractor,
		toolServer *mcpadapter.ToolServer,
	) error {
		// Defer order is intentional (LIFO). The HTTP layer drains first
		// (below), then housekeeping stops, workers finish, the in-process
		// dispatcher drains while the task client is still open, and only then
		// are the queue, parser connections, and database closed. Drained jobs
		// are persisted as failed before the database goes away.
		defer repository.CloseDatabase(db)
		defer closeParserClients(recognizer, extractor)
		defer func() {
			if err := sharedLimiter.Close(); err != nil {
				logger.Warn(context.Background(), "close shared limiter failed", "error", err)
			}
		}()
		defer func() {
			taskClient.Close()
			logger.Info(context.Background(), "task enqueuer closed")
		}()
		defer func() {
			catalogDispatcher.Stop()
			logger.Info(context.Background(), "catalog dispatcher stopped")
		}()
		defer func() {
			worker.Shutdown()
			logger.Info(context.Background(), "asynq worker stopped")
		}()
		if err := documentHousekeeping.Start(context.Background()); err != nil {
			logger.Warn(context.Background(), "start document housekeeping failed", "error", err)
		}
		defer func() {
			documentHousekeeping.Stop()
			logger.Info(context.Background(), "document housekeeping stopped")
		}()

		server := &http.Server{
			Handler:           engine,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       cfg.HTTPReadTimeout,
			WriteTimeout:      cfg.HTTPWriteTimeout,
			IdleTimeout:       cfg.HTTPIdleTimeout,
		}
		listener, err := listenWithRetry(cfg.HTTPAddress, 10, 300*time.Millisecond)
		if err != nil {
			return fmt.Errorf("start listener: %w", err)
		}

		var mcpHTTP *http.Server
		var mcpServeErr chan error
		if cfg.MCPEnabled {
			mcpHandler, err := toolServer.Handler()
			if err != nil {
				return fmt.Errorf("prepare MCP adapter: %w", err)
			}
			mcpListener, err := net.Listen("tcp", cfg.MCPAddress)
			if err != nil {
				return fmt.Errorf("start MCP listener: %w", err)
			}
			mcpHTTP = &http.Server{
				Handler:           mcpHandler,
				ReadHeaderTimeout: 5 * time.Second,
				ReadTimeout:       cfg.HTTPReadTimeout,
				WriteTimeout:      cfg.HTTPWriteTimeout,
				IdleTimeout:       cfg.HTTPIdleTimeout,
			}
			mcpServeErr = make(chan error, 1)
			go func() { mcpServeErr <- mcpHTTP.Serve(mcpListener) }()
			defer func() {
				shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTPShutdownTimeout)
				defer cancel()
				if err := mcpHTTP.Shutdown(shutdownCtx); err != nil {
					logger.Warn(context.Background(), "MCP shutdown failed", "error", err)
				}
			}()
			logger.Info(context.Background(), "MCP server is running", "address", cfg.MCPAddress)
		}

		signals := make(chan os.Signal, 2)
		signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(signals)

		serveErr := make(chan error, 1)
		go func() { serveErr <- server.Serve(listener) }()
		logger.Info(context.Background(), "server is running", "address", cfg.HTTPAddress)

		select {
		case err := <-serveErr:
			if err != nil && err != http.ErrServerClosed {
				return fmt.Errorf("serve API: %w", err)
			}
		case err := <-mcpServeErr:
			if err != nil && err != http.ErrServerClosed {
				return fmt.Errorf("serve MCP: %w", err)
			}
		case received := <-signals:
			logger.Info(context.Background(), "received shutdown signal", "signal", received)
			// Shutdown closes the listener immediately but keeps waiting for
			// handlers. Waiting here (before main's defers) is what guarantees
			// HTTP requests finish before the worker/pool are closed.
			shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTPShutdownTimeout)
			shutdownErr := server.Shutdown(shutdownCtx)
			cancel()
			if shutdownErr != nil {
				logger.Error(context.Background(), "graceful HTTP shutdown failed", "error", shutdownErr)
				_ = server.Close()
			} else {
				logger.Info(context.Background(), "HTTP handlers drained")
			}
			if err := <-serveErr; err != nil && err != http.ErrServerClosed {
				logger.Warn(context.Background(), "HTTP serve stopped during shutdown", "error", err)
			}
			// A second signal skips the remaining drain. This is an explicit
			// operator escape hatch, not part of the normal shutdown path.
			go forceExitOnSecondSignal(signals)
		}
		return nil
	})
	if err != nil {
		logger.Error(context.Background(), "run application", "error", err)
	}
}

func forceExitOnSecondSignal(signals <-chan os.Signal) {
	signalReceived := <-signals
	logger.Error(context.Background(), "received second shutdown signal; forcing exit",
		"signal", signalReceived)
	os.Exit(1)
}

// closeParserClients releases gRPC connections when the concrete adapters own
// one. The default recognizer has no connection and does not implement Close.
func closeParserClients(recognizer interfaces.DocumentRecognizer, extractor interfaces.PaperMetadataExtractor) {
	for _, candidate := range []struct {
		name  string
		value any
	}{
		{"document recognizer", recognizer},
		{"paper extractor", extractor},
	} {
		closer, ok := candidate.value.(interface{ Close() error })
		if !ok {
			continue
		}
		if err := closer.Close(); err != nil {
			logger.Warn(context.Background(), "close parser client failed",
				"client", candidate.name, "error", err)
		}
	}
}
