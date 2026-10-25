package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
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

// runApplication owns all in-process services; operating-system signals belong
// to cmd/server, while desktop owns the enclosing process and its dependencies.
func runApplication(ctx context.Context, onReady func()) error {
	return runApplicationMode(ctx, onReady, false)
}

func runApplicationMode(ctx context.Context, onReady func(), maintenance bool) error {
	dependencies := container.Build()
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
		renderer interfaces.PDFPageRenderer,
		toolServer *mcpadapter.ToolServer,
		taskParams router.AsynqTaskParams,
	) error {
		// Defer order is intentional (LIFO). The HTTP layer drains first
		// (below), then housekeeping stops, workers finish, the in-process
		// dispatcher drains while the task client is still open, and only then
		// are the queue, parser connections, and database closed. Drained jobs
		// are persisted as failed before the database goes away.
		defer repository.CloseDatabase(db)
		defer closeParserClients(recognizer, extractor, renderer)
		defer func() {
			if err := sharedLimiter.Close(); err != nil {
				logger.Warn(ctx, "close shared limiter failed", "error", err)
			}
		}()
		defer func() {
			taskClient.Close()
			logger.Info(ctx, "task enqueuer closed")
		}()
		if !maintenance {
			defer func() {
				catalogDispatcher.Stop()
				logger.Info(ctx, "catalog dispatcher stopped")
			}()
			defer func() {
				worker.Shutdown()
				logger.Info(ctx, "asynq worker stopped")
			}()
			if err := documentHousekeeping.FailInterruptedWork(ctx); err != nil {
				return fmt.Errorf("fail interrupted work: %w", err)
			}
			// The dispatcher remains available while Asynq drains.
			catalogDispatcher.Start(context.WithoutCancel(ctx))
			if _, err := router.RunAsynqServer(taskParams); err != nil {
				return fmt.Errorf("start Asynq worker: %w", err)
			}
			if err := documentHousekeeping.Start(ctx); err != nil {
				logger.Warn(ctx, "start document housekeeping failed", "error", err)
			}
			defer func() {
				documentHousekeeping.Stop()
				logger.Info(ctx, "document housekeeping stopped")
			}()
		}

		frontend, err := router.NewStaticFrontend(engine, cfg.WebDistDirectory)
		if err != nil {
			return err
		}
		defer frontend.Close()
		var handler http.Handler = frontend
		if maintenance {
			handler = maintenanceAPIHandler(frontend)
		}
		server := &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       cfg.HTTPReadTimeout,
			WriteTimeout:      cfg.HTTPWriteTimeout,
			IdleTimeout:       cfg.HTTPIdleTimeout,
		}
		listener, err := listenWithRetry(ctx, cfg.HTTPAddress, 10, 300*time.Millisecond)
		if err != nil {
			return fmt.Errorf("start listener: %w", err)
		}
		defer listener.Close()

		var mcpHTTP *http.Server
		var mcpServeErr chan error
		if cfg.MCPEnabled {
			mcpHandler, err := toolServer.Handler()
			if err != nil {
				return fmt.Errorf("prepare MCP adapter: %w", err)
			}
			if maintenance {
				mcpHandler = maintenanceMCPHandler(mcpHandler)
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
					logger.Warn(ctx, "MCP shutdown failed", "error", err)
				}
			}()
			logger.Info(ctx, "MCP server is running", "address", cfg.MCPAddress)
		}

		serveErr := make(chan error, 1)
		go func() { serveErr <- server.Serve(listener) }()
		logger.Info(ctx, "server is running", "address", cfg.HTTPAddress)
		onReady()

		select {
		case err := <-serveErr:
			if err != nil && err != http.ErrServerClosed {
				return fmt.Errorf("serve API: %w", err)
			}
		case err := <-mcpServeErr:
			if err != nil && err != http.ErrServerClosed {
				return fmt.Errorf("serve MCP: %w", err)
			}
		case <-ctx.Done():
			logger.Info(ctx, "application shutdown requested")
		}
		if !maintenance {
			worker.Stop()
		}
		// Shutdown closes the listener immediately but keeps waiting for
		// handlers. Waiting here (before main's defers) is what guarantees
		// HTTP requests finish before the worker/pool are closed.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTPShutdownTimeout)
		shutdownErr := server.Shutdown(shutdownCtx)
		cancel()
		if shutdownErr != nil {
			logger.Error(ctx, "graceful HTTP shutdown failed", "error", shutdownErr)
			_ = server.Close()
		} else {
			logger.Info(ctx, "HTTP handlers drained")
		}
		return nil
	})
	if err != nil {
		logger.Error(ctx, "run application", "error", err)
	}
	return err
}

// closeParserClients releases gRPC connections when the concrete adapters own
// one. The default recognizer has no connection and does not implement Close.
func closeParserClients(
	recognizer interfaces.DocumentRecognizer,
	extractor interfaces.PaperMetadataExtractor,
	renderer interfaces.PDFPageRenderer,
) {
	for _, candidate := range []struct {
		name  string
		value any
	}{
		{"document recognizer", recognizer},
		{"paper extractor", extractor},
		{"PDF page renderer", renderer},
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
