package router

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"paperMaster/internal/application/service"
	"paperMaster/internal/common"
	"paperMaster/internal/config"
	"paperMaster/internal/logger"
	"paperMaster/internal/middleware/asynqdl"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/hibiken/asynq"
	"go.uber.org/dig"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type AsynqTaskParams struct {
	// dig 简化参数引用,感觉功能上可以看成python的**wargs
	dig.In

	Server           *asynq.Server
	Config           config.Config
	DocumentService  interfaces.DocumentService          `optional:"true"`
	PaperInfoService interfaces.PaperInfoService         `optional:"true"`
	TaskEnqueuer     interfaces.TaskEnqueuer             `optional:"true"`
	DeadLetterRepo   interfaces.TaskDeadLetterRepository `optional:"true"`
	SpanTracker      service.SpanTracker                 `optional:"true"`
}

const defaultRedisOpTimeoutMs = 500

// timeout from Env
func readRedisOpTimeoutMs() int {
	if v := strings.TrimSpace(os.Getenv("REDIS_OP_TIMEOUT_MS")); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			return parsed
		}
	}
	return defaultRedisOpTimeoutMs
}

func getAsynqRedisClientOpt() *asynq.RedisClientOpt {
	db := 0
	if dbStr := os.Getenv("REDIS_DB"); dbStr != "" {
		// to number
		if parsed, err := strconv.Atoi(dbStr); err == nil {
			db = parsed
		}
	}

	timeoutMs := readRedisOpTimeoutMs()
	// set with os env
	opt := &asynq.RedisClientOpt{
		Addr:        os.Getenv("REDIS_ADDR"),
		Username:    os.Getenv("REDIS_USERNAME"),
		Password:    os.Getenv("REDIS_PASSWORD"),
		ReadTimeout: time.Duration(timeoutMs) * time.Millisecond,
		// Writes are typically more sensitive to congestion than reads
		// (RESP pipelining, BRPOPLPUSH on Asynq dequeue), so we keep
		// WriteTimeout slightly larger to absorb head-of-line stalls.
		WriteTimeout: time.Duration(timeoutMs*2) * time.Millisecond,
		DB:           db,
		TLSConfig:    common.RedisTLSConfig(),
	}
	return opt
}

func NewAsyncqClient() (*asynq.Client, error) {
	opt := getAsynqRedisClientOpt()
	client := asynq.NewClient(opt)
	err := client.Ping()
	if err != nil {
		return nil, err
	}
	return client, nil
}

var _ interfaces.TaskEnqueuer = (*asynq.Client)(nil)

// NewTaskEnqueuer exposes the concrete Asynq client through the generic queue
// boundary while retaining the concrete client for lifecycle shutdown.
func NewTaskEnqueuer(client *asynq.Client) interfaces.TaskEnqueuer {
	return client
}

// wikiIngestRetryDelay is a fixed, short backoff for wiki ingest lock
// conflicts. Must be slightly longer than the active-lock TTL's worst-case
// "just got set" window so the retry is highly likely to succeed without
// burning through retries; but short enough that users don't feel the stall.
// const wikiIngestRetryDelay = 15 * time.Second

// asynqRetryDelayFunc customizes per-task retry backoff.
//
// Default asynq backoff is exponential (≈10s, 40s, 90s, 2.5m, ...), which
// is appropriate for transient errors like remote HTTP failures.

// But for wiki ingest lock conflicts (ErrWikiIngestConcurrent), exponential
// backoff is harmful: a freshly orphaned lock expires in ≤60s, so a 15s
// fixed retry virtually guarantees the next attempt succeeds. Without
// this override, a crash-restart cycle can leave a KB unable to make
// progress for 7–10 minutes while the orphan lock expires AND the retry
// schedule catches up.
func asynqRetryDelayFunc(n int, e error, t *asynq.Task) time.Duration {
	if delay := paperTaskRetryAfter(e); delay > 0 {
		return delay
	}
	// if errors.Is(e, service.ErrWikiIngestConcurrent) {
	// 	return wikiIngestRetryDelay
	// }
	return asynq.DefaultRetryDelayFunc(n, e, t)
}

// defaultAsynqConcurrency is the worker pool size used when
// ASYNQ_CONCURRENCY is unset. The asynq library defaults to
// runtime.NumCPU(), which under-provisions during batch document uploads:
// a single 4-core container can only process 4 documents in parallel even
// when 100 are queued, so the queue wait time eats into each task's
// DocumentProcessTimeout budget. 32 is a safer default for the I/O-bound
// nature of doc parsing (most time is spent in DocReader / embedding RPCs,
// not on local CPU).
const defaultAsynqConcurrency = 32

func NewAsynqServer() *asynq.Server {
	opt := getAsynqRedisClientOpt()
	concurrency := defaultAsynqConcurrency

	if v := strings.TrimSpace(os.Getenv("ASYNQ_CONCURRENCY")); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			concurrency = parsed
		}
	}

	// use system service to support multi priority setting, DB > Env > Defualt
	//if svc != nil {
	//	n := svc.GetInt(context.Background(), "asynq.concurrency", "ASYNQ_CONCURRENCY", defaultAsynqConcurrency)
	//	if n > 0 {
	//		concurrency = int(n)
	//	}
	//}
	logger.Info(context.Background(), "asynq server configured", "concurrency", concurrency, "redis_op_timeout_ms", readRedisOpTimeoutMs())

	srv := asynq.NewServer(
		opt,
		asynq.Config{
			Concurrency: concurrency,
			Queues: map[string]int{
				types.QueueCritical: 6, // Highest priority queue
				types.QueueDefault:  3, // Default priority queue
				types.QueueLow:      1, // Lowest priority queue
				//types.QueueMultimodal: 1, // Isolated lane for high-volume slow VLM image tasks
				//types.QueueGraph:      1, // Isolated lane for high-volume slow graph-extraction tasks
				//types.QueueQuestion:   1, // Isolated lane for high-volume slow question-generation tasks
			},
			RetryDelayFunc: asynqRetryDelayFunc,
		},
	)
	return srv
}

func RunAsynqServer(params AsynqTaskParams) *asynq.ServeMux {
	// Create a new mux and register all handlers
	mux := asynq.NewServeMux()

	// Install the dead-letter middleware FIRST so it sees the raw error
	// returned by the handler, before any other middleware that might
	// transform it. The middleware records one task_dead_letters row per
	// task that exhausts its retry budget — operators can then SQL-query
	// failures by task type, scope, or tenant without scraping logs.
	// Best-effort: a DB failure is logged and swallowed; the original task
	// error always propagates upstream to asynq for retry/archival.
	//
	// The callback flips Knowledge.parse_status to "failed" the moment a
	// document-related task exhausts its retry budget. Without this hook,
	// a permanently-failing task left its parent knowledge stranded in
	// "processing" until housekeeping cron caught it minutes later — the
	// UI signal users actually see.
	knowledgeFailer := newDeadLetterDocumentFailer(params.DocumentService, params.SpanTracker)
	mux.Use(asynqdl.MiddlewareWithCallback(params.DeadLetterRepo, knowledgeFailer))

	// Install Langfuse middleware BEFORE handler registration so every task
	// type is automatically wrapped. When Langfuse is disabled the middleware
	// is a pass-through; when enabled it resumes the upstream HTTP trace (if
	// the payload carries one) or opens a standalone trace, then wraps the
	// handler execution in a SPAN so all child generations (embedding / VLM /
	// chat / rerank / ASR) nest correctly in the Langfuse UI.
	// mux.Use(langfuse.AsynqMiddleware())

	// Register document processing handler
	if params.DocumentService != nil {
		mux.HandleFunc(types.TypeDocumentProcess, params.DocumentService.ProcessDocument)
	}
	if params.PaperInfoService != nil {
		mux.HandleFunc(types.TypePaperMetadataExtract, func(ctx context.Context, task *asynq.Task) error {
			return processPaperMetadataExtractTask(
				ctx,
				task,
				params.PaperInfoService,
				params.TaskEnqueuer,
				params.Config,
			)
		})
		mux.HandleFunc(types.TypePaperCatalogResolve, func(ctx context.Context, task *asynq.Task) error {
			return processPaperCatalogResolveTask(
				ctx,
				task,
				params.PaperInfoService,
			)
		})
	}

	go func() {
		// Start the server
		if err := params.Server.Run(mux); err != nil {
			logger.Error(context.Background(), "asynq server stopped", "error", err)
		}
	}()
	return mux
}

func processPaperMetadataExtractTask(
	ctx context.Context,
	task *asynq.Task,
	info interfaces.PaperInfoService,
	enqueuer interfaces.TaskEnqueuer,
	cfg config.Config,
) error {
	if task == nil {
		return skipPaperTask(errors.New("paper metadata extraction task is required"))
	}
	var payload types.PaperMetadataExtractPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return skipPaperTask(fmt.Errorf("decode paper metadata extraction payload: %w", err))
	}
	command, err := payload.Command()
	if err != nil {
		return skipPaperTask(err)
	}
	ctx = paperTaskContext(ctx, payload.RequestID, payload.DocumentID, task.Type())
	resolvable, err := info.ExtractMetadata(ctx, command)
	if err != nil {
		return classifyPaperTaskError(err)
	}
	if !resolvable {
		logger.Info(ctx, "paper catalog resolution not queued",
			"reason", "title and DOI are both unavailable")
		return nil
	}
	if enqueuer == nil {
		return errors.New("document task enqueuer is not configured")
	}
	// begin Catalog recognition
	catalogTask, options, err := service.NewPaperCatalogResolveTask(
		types.PaperCatalogResolvePayload{
			RequestID:      payload.RequestID,
			DocumentID:     payload.DocumentID,
			MarkdownSHA256: payload.MarkdownSHA256,
			Attempt:        payload.Attempt,
		},
		cfg.PaperCatalogTaskTimeout,
		cfg.PaperCatalogTaskMaxRetry,
	)
	if err != nil {
		return skipPaperTask(err)
	}
	if err := enqueueTask(
		ctx,
		enqueuer,
		catalogTask,
		options,
		payload.DocumentID,
	); err != nil {
		// Extraction writes are idempotent. Returning this error makes Asynq
		// retry the extraction task and therefore recover the lost handoff.
		return err
	}
	return nil
}

func enqueueTask(
	ctx context.Context,
	enqueuer interfaces.TaskEnqueuer,
	task *asynq.Task,
	options []asynq.Option,
	documentID string,
) error {
	if enqueuer == nil {
		return errors.New("task enqueuer is not configured")
	}
	if task == nil {
		return errors.New("task is required")
	}
	_, err := enqueuer.EnqueueContext(ctx, task, options...)
	if errors.Is(err, asynq.ErrDuplicateTask) {
		logger.Debug(ctx, "task already queued",
			"document_id", documentID,
			"task_type", task.Type())
		return nil
	}
	if err != nil {
		logger.Error(ctx, "enqueue task failed",
			"document_id", documentID,
			"task_type", task.Type(),
			"error", err)
		return fmt.Errorf("enqueue %s: %w", task.Type(), err)
	}
	logger.Info(ctx, "task enqueued",
		"document_id", documentID,
		"task_type", task.Type())
	return nil
}

func processPaperCatalogResolveTask(
	ctx context.Context,
	task *asynq.Task,
	info interfaces.PaperInfoService,
) error {
	if task == nil {
		return skipPaperTask(errors.New("paper catalog resolution task is required"))
	}
	var payload types.PaperCatalogResolvePayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return skipPaperTask(fmt.Errorf("decode paper catalog resolution payload: %w", err))
	}
	command, err := payload.Command()
	if err != nil {
		return skipPaperTask(err)
	}
	ctx = paperTaskContext(ctx, payload.RequestID, payload.DocumentID, task.Type())
	if err := info.ResolveMetadata(ctx, command); err != nil {
		return classifyPaperTaskError(err)
	}
	return nil
}

func paperTaskContext(
	ctx context.Context,
	requestID string,
	documentID string,
	taskType string,
) context.Context {
	if strings.TrimSpace(requestID) != "" {
		ctx = logger.WithRequestID(ctx, requestID)
	}
	return logger.WithAttrs(
		ctx,
		"document_id", documentID,
		"task_type", taskType,
	)
}

func classifyPaperTaskError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) ||
		errors.Is(err, sql.ErrNoRows) ||
		errors.Is(err, service.ErrStalePaperInfo) {
		return fmt.Errorf("%v: %w", err, asynq.RevokeTask)
	}
	if errors.Is(err, types.ErrInvalidPaperInfoTaskPayload) ||
		errors.Is(err, types.ErrInvalidPaperExtraction) ||
		errors.Is(err, types.ErrInvalidPaperCatalogData) ||
		errors.Is(err, service.ErrInvalidPaperMetadata) ||
		errors.Is(err, service.ErrPaperCatalogNoMatch) ||
		errors.Is(err, service.ErrPaperInfoCatalogUnavailable) {
		return skipPaperTask(err)
	}

	retryableExternal, classifiedExternal := externalTaskRetryability(err)
	if classifiedExternal && !retryableExternal {
		return skipPaperTask(err)
	}
	return err
}

func skipPaperTask(err error) error {
	return fmt.Errorf("%v: %w", err, asynq.SkipRetry)
}

func externalTaskRetryability(err error) (retryable bool, classified bool) {
	walkTaskErrors(err, func(item error) {
		if retryClass, ok := item.(interface{ TaskRetryable() bool }); ok {
			classified = true
			retryable = retryable || retryClass.TaskRetryable()
		}
		switch status.Code(item) {
		case codes.Canceled:
			classified = true
		case codes.InvalidArgument,
			codes.NotFound,
			codes.AlreadyExists,
			codes.PermissionDenied,
			codes.Unauthenticated,
			codes.FailedPrecondition,
			codes.OutOfRange,
			codes.Unimplemented:
			classified = true
		case codes.DeadlineExceeded,
			codes.ResourceExhausted,
			codes.Aborted,
			codes.Internal,
			codes.Unavailable,
			codes.DataLoss:
			classified = true
			retryable = true
		}
	})
	return retryable, classified
}

func paperTaskRetryAfter(err error) time.Duration {
	var retryAfter time.Duration
	walkTaskErrors(err, func(item error) {
		if delay, ok := item.(interface{ TaskRetryAfter() time.Duration }); ok {
			if candidate := delay.TaskRetryAfter(); candidate > retryAfter {
				retryAfter = candidate
			}
		}
	})
	return retryAfter
}

func walkTaskErrors(err error, visit func(error)) {
	if err == nil {
		return
	}
	visit(err)
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		for _, nested := range wrapped.Unwrap() {
			walkTaskErrors(nested, visit)
		}
	case interface{ Unwrap() error }:
		walkTaskErrors(wrapped.Unwrap(), visit)
	}
}

// deadLetterKnowledgePayload extracts only the field we need from any
// document-related asynq payload. Kept narrow so we don't accidentally
// depend on the full payload schema and survive future field churn.
type deadLetterDocumentPayload struct {
	DocumentID string `json:"document_id,omitempty"`
	// Attempt threads through DocumentProcess / ManualProcess /
	// KnowledgePostProcess payloads (added when span tracking shipped)
	// — extracted here so the dead-letter callback can also close the
	// matching root span as failed. Older in-flight payloads without
	// this field decode as 0 and the tracker call no-ops.
	Attempt int `json:"attempt,omitempty"`
}

// taskTypesAffectingKnowledgeStatus enumerates the asynq task types whose
// dead-letter event should flip the parent Knowledge to "failed". Only
// terminal task types are listed here:
//
//   - TypeDocumentProcess: the entry point of the parsing pipeline.
//   - TypeImageMultimodal: a single image hitting dead-letter would have
//     been counted by isFinalAsynqAttempt (see image_multimodal.go), so
//     the parent might still complete via remaining images. We DO NOT mark
//     the parent failed for this case — finalize-on-last-attempt already
//     ensures progress.
//   - TypeKnowledgePostProcess: terminal stage; failure here strands the
//     knowledge in "processing".
//   - TypeManualProcess: same shape as DocumentProcess for re-indexing.
//
// Question/Summary generation are NOT included: they run after parse_status
// has already become "completed" and have their own status fields.
var taskTypesAffectingKnowledgeStatus = map[string]struct{}{
	types.TypeDocumentProcess: {},
	// types.TypeKnowledgePostProcess: {},
	// types.TypeManualProcess:        {},
}

type deadLetterDocumentListDeletePayload struct {
	DocumentIDs []string `json:"knowledge_ids,omitempty"`
}

// newDeadLetterKnowledgeFailer returns the callback wired into the asynq
// dead-letter middleware. When a document-related task exhausts its retry
// budget, this callback marks the corresponding Knowledge row as failed so
// the UI surfaces the error instead of a perpetual spinner.
//
// All work is best-effort: missing payload, missing knowledge_id, or DB
// errors are logged and swallowed. The dead-letter record is the source of
// truth — this is purely a UX shortcut so users don't wait for the
// housekeeping cron's next sweep.
func newDeadLetterDocumentFailer(documents interfaces.DocumentService, tracker service.SpanTracker) asynqdl.OnDeadLetter {
	if documents == nil {
		return nil
	}
	return func(ctx context.Context, t *asynq.Task, taskErr error) {
		if t == nil {
			return
		}
		if _, ok := taskTypesAffectingKnowledgeStatus[t.Type()]; !ok {
			return
		}
		var probe deadLetterDocumentPayload
		if err := json.Unmarshal(t.Payload(), &probe); err != nil || probe.DocumentID == "" {
			return
		}
		errMsg := "task " + t.Type() + " exhausted retries"
		if taskErr != nil {
			errMsg += ": " + taskErr.Error()
		}
		if len(errMsg) > 8192 {
			errMsg = errMsg[:8192]
		}
		if documents != nil {
			updated, err := documents.FailDocumentIfProcessing(ctx, probe.DocumentID, errMsg)
			if err != nil {
				logger.Warn(ctx, "dead-letter callback failed to mark document", "document_id", probe.DocumentID, "error", err)
				return
			}
			if !updated {
				logger.Info(ctx, "dead-letter callback left document status unchanged", "document_id", probe.DocumentID, "task_type", t.Type())
				return
			}
		}
		if tracker != nil && probe.Attempt > 0 {
			tracker.FinalizeAttempt(ctx, probe.DocumentID, probe.Attempt, "failed", nil, "TASK_DEAD_LETTER", errMsg)
		}
		logger.Info(ctx, "dead-letter callback marked document failed", "document_id", probe.DocumentID, "task_type", t.Type())
	}
}
