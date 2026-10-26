package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	"paperMaster/internal/config"
	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

const urlImportTaskPrefix = "url-import-"

var (
	ErrInvalidURLImport         = errors.New("invalid URL import")
	ErrURLImportRequestConflict = errors.New("URL import request_id conflicts with existing task")
)

// URL imports reuse document validation/storage/deduplication and normal
// processing. Only remote acceptance moves off the MCP request lifetime.
type urlImportService struct {
	documents interfaces.DocumentService
	bases     interfaces.DocumentBaseService
	tasks     interfaces.TaskEnqueuer
	inspector interfaces.TaskResultInspector
	timeout   time.Duration
}

func NewURLImportService(
	documents interfaces.DocumentService, bases interfaces.DocumentBaseService,
	tasks interfaces.TaskEnqueuer, inspector interfaces.TaskResultInspector, cfg config.Config,
) interfaces.URLImportService {
	timeout := cfg.BatchImportTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	return &urlImportService{documents: documents, bases: bases, tasks: tasks, inspector: inspector, timeout: timeout}
}

func validateURLImportPayload(payload types.URLImportPayload) error {
	if _, err := uuid.Parse(payload.RequestID); err != nil {
		return fmt.Errorf("%w: request_id must be a UUID", ErrInvalidURLImport)
	}
	if err := validateBatchLength(len(payload.Commands)); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidURLImport, err)
	}
	if payload.BaseID != "" {
		if _, err := uuid.Parse(payload.BaseID); err != nil {
			return fmt.Errorf("%w: invalid document base id", ErrInvalidURLImport)
		}
	}
	return nil
}

func (s *urlImportService) EnqueueURLs(
	ctx context.Context, commands []types.DocumentURLImportCommand, baseID, requestID string,
) (types.URLImportTask, error) {
	payload := types.URLImportPayload{Commands: commands, BaseID: strings.TrimSpace(baseID), RequestID: requestID}
	if payload.RequestID == "" {
		payload.RequestID = uuid.NewString()
	}
	if err := validateURLImportPayload(payload); err != nil {
		return types.URLImportTask{}, err
	}
	if payload.BaseID != "" {
		if s.bases == nil {
			return types.URLImportTask{}, errors.New("base service is unavailable")
		}
		if _, err := s.bases.GetBase(ctx, payload.BaseID); err != nil {
			return types.URLImportTask{}, err
		}
	}
	if s.tasks == nil {
		return types.URLImportTask{}, types.ErrTaskInspectorUnavailable
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return types.URLImportTask{}, err
	}
	id := urlImportTaskPrefix + payload.RequestID
	_, err = s.tasks.EnqueueContext(ctx, asynq.NewTask(types.TypeLibraryImportURLs, encoded),
		asynq.Queue(types.QueueDownload), asynq.TaskID(id), asynq.Timeout(s.timeout+30*time.Second),
		asynq.MaxRetry(2), asynq.Retention(24*time.Hour))
	if errors.Is(err, asynq.ErrTaskIDConflict) && s.inspector != nil {
		record, lookupErr := s.inspector.GetTask(ctx, types.QueueDownload, id)
		if lookupErr != nil {
			return types.URLImportTask{}, lookupErr
		}
		var existing types.URLImportPayload
		if json.Unmarshal(record.Payload, &existing) != nil || record.Type != types.TypeLibraryImportURLs ||
			!reflect.DeepEqual(existing, payload) {
			return types.URLImportTask{}, ErrURLImportRequestConflict
		}
		return s.GetURLImportTask(ctx, id)
	}
	if err != nil {
		logger.Error(ctx, "enqueue URL import failed", "task_id", id, "error", err)
		return types.URLImportTask{}, fmt.Errorf("%w: %v", types.ErrTaskInspectorUnavailable, err)
	}
	logger.Info(ctx, "URL import queued", "task_id", id, "item_count", len(commands), "base_id", payload.BaseID)
	return types.URLImportTask{ID: id, State: "queued", BaseID: payload.BaseID, Total: len(commands)}, nil
}

func (s *urlImportService) GetURLImportTask(ctx context.Context, id string) (types.URLImportTask, error) {
	if !strings.HasPrefix(id, urlImportTaskPrefix) {
		return types.URLImportTask{}, types.ErrTaskNotFound
	}
	if _, err := uuid.Parse(strings.TrimPrefix(id, urlImportTaskPrefix)); err != nil {
		return types.URLImportTask{}, types.ErrTaskNotFound
	}
	if s.inspector == nil {
		return types.URLImportTask{}, types.ErrTaskInspectorUnavailable
	}
	record, err := s.inspector.GetTask(ctx, types.QueueDownload, id)
	if err != nil {
		if errors.Is(err, types.ErrTaskNotFound) || ctx.Err() != nil {
			return types.URLImportTask{}, err
		}
		return types.URLImportTask{}, fmt.Errorf("%w: %v", types.ErrTaskInspectorUnavailable, err)
	}
	var payload types.URLImportPayload
	if record.Type != types.TypeLibraryImportURLs || json.Unmarshal(record.Payload, &payload) != nil ||
		validateURLImportPayload(payload) != nil || id != urlImportTaskPrefix+payload.RequestID {
		return types.URLImportTask{}, types.ErrTaskNotFound
	}
	result := types.URLImportTask{ID: id, BaseID: payload.BaseID, Total: len(payload.Commands), State: "queued"}
	if len(record.Result) > 0 {
		var progress types.DocumentBatchResult
		if err := json.Unmarshal(record.Result, &progress); err != nil {
			return types.URLImportTask{}, fmt.Errorf("decode URL import progress: %w", err)
		}
		result.Result, result.Completed = &progress, len(progress.Items)
	}
	switch record.State {
	case "active":
		result.State = "running"
	case "completed":
		if result.Result == nil {
			return types.URLImportTask{}, errors.New("completed URL import has no result")
		}
		result.State = "succeeded"
	case "archived":
		result.State, result.Error = "failed", "URL import task failed; inspect completed items before retrying"
	}
	return result, nil
}

func (s *urlImportService) ProcessURLImport(ctx context.Context, task *asynq.Task) error {
	var payload types.URLImportPayload
	if task == nil || json.Unmarshal(task.Payload(), &payload) != nil || validateURLImportPayload(payload) != nil {
		return fmt.Errorf("%w: invalid URL import task", asynq.SkipRetry)
	}
	writer := task.ResultWriter()
	if writer == nil {
		return errors.New("URL import result writer is unavailable")
	}
	previous := types.DocumentBatchResult{}
	if s.inspector == nil {
		return types.ErrTaskInspectorUnavailable
	}
	record, err := s.inspector.GetTask(ctx, types.QueueDownload, urlImportTaskPrefix+payload.RequestID)
	if err != nil {
		return err
	}
	if len(record.Result) > 0 {
		if err := json.Unmarshal(record.Result, &previous); err != nil {
			return fmt.Errorf("decode saved URL import progress: %w", err)
		}
	}
	return s.processURLImport(ctx, payload, writer, previous)
}

func (s *urlImportService) processURLImport(
	ctx context.Context, payload types.URLImportPayload, writer io.Writer, saved ...types.DocumentBatchResult,
) error {
	if writer == nil {
		return errors.New("URL import result writer is unavailable")
	}
	ctx = logger.WithRequestID(ctx, payload.RequestID)
	ctx = context.WithValue(ctx, types.RequestIDContextKey, payload.RequestID)
	ctx = logger.WithAttrs(ctx, "task_id", urlImportTaskPrefix+payload.RequestID)
	batchCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	result := types.DocumentBatchResult{Items: make([]types.DocumentBatchItemResult, 0, len(payload.Commands))}
	if len(saved) > 0 {
		result = saved[0]
		if len(result.Items) > len(payload.Commands) {
			return errors.New("saved URL import progress exceeds the input batch")
		}
		for index, item := range result.Items {
			if item.Index != index || item.URL != payload.Commands[index].URL {
				return errors.New("saved URL import progress does not match the input batch")
			}
			switch item.Status {
			case types.DocumentImportItemAccepted, types.DocumentImportItemDuplicate:
				if item.Document == nil || item.Document.ID == uuid.Nil {
					return errors.New("saved URL import result is missing its document")
				}
			case types.DocumentImportItemRejected, types.DocumentImportItemFailed:
			default:
				return errors.New("saved URL import result has an invalid status")
			}
		}
	}
	if result.Items == nil {
		result.Items = []types.DocumentBatchItemResult{}
	}
	publish := func() error {
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		n, err := writer.Write(encoded)
		if err == nil && n != len(encoded) {
			return io.ErrShortWrite
		}
		return err
	}
	if err := publish(); err != nil {
		return err
	}
	if result.Stopped {
		return nil
	}
	for index := len(result.Items); index < len(payload.Commands); index++ {
		command := payload.Commands[index]
		if batchCtx.Err() != nil {
			result = stopBatchForContext(batchCtx, result, index, payload.Commands)
			return publish()
		}
		document, err := s.documents.CreateDocumentFromURL(batchCtx, command)
		if errors.Is(err, errDocumentDuplicateLookup) {
			result = stopBatchForDeduplicationFailure(ctx, result,
				types.DocumentBatchItemResult{Index: index, URL: command.URL}, err)
			for next := index + 1; next < len(payload.Commands); next++ {
				result.Items = append(result.Items, types.DocumentBatchItemResult{
					Index: next, URL: payload.Commands[next].URL,
					Status: types.DocumentImportItemFailed, Error: "batch stopped before this URL was imported",
				})
			}
			return publish()
		}
		item := batchResultForDocument(types.DocumentBatchItemResult{Index: index, URL: command.URL}, document, err)
		// Attach each durable item immediately, using the worker lifetime so a
		// batch deadline cannot strand an already-created document outside Base.
		if item.Document != nil && payload.BaseID != "" &&
			(item.Status == types.DocumentImportItemAccepted || item.Status == types.DocumentImportItemDuplicate) {
			if err := s.bases.AddDocumentToBase(ctx, payload.BaseID, item.Document.ID.String()); err != nil {
				item.Status, item.Error = types.DocumentImportItemFailed, "document was imported but could not be added to the Base"
			}
		}
		result.Items = append(result.Items, item)
		if err := publish(); err != nil {
			return err
		}
		logger.Info(ctx, "URL import item completed", "index", index, "status", item.Status)
		if batchCtx.Err() != nil {
			result = stopBatchForContext(batchCtx, result, index+1, payload.Commands)
			return publish()
		}
	}
	logger.Info(ctx, "URL import task completed", "item_count", len(result.Items))
	return nil
}
