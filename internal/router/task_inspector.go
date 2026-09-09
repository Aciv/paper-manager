package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/hibiken/asynq"
)

// NewAsynqInspector constructs an *asynq.Inspector pointed at the same
// Redis used by the asynq client. Only registered in asynq mode.
func NewAsynqInspector() *asynq.Inspector {
	return asynq.NewInspector(getAsynqRedisClientOpt())
}

// asynqTaskInspector implements interfaces.TaskInspector backed by an
// *asynq.Inspector. Scans the queues we actually use ("default",
// "critical", "low") and matches tasks whose payload carries the given
// document_id. Best-effort: any scan/delete error is logged and
// swallowed so the cancel API still returns success even when Redis is
// flaky.
type asynqTaskInspector struct {
	inspector *asynq.Inspector
}

// NewAsynqTaskInspector returns a TaskInspector wrapping the given
// *asynq.Inspector. nil-safe: a nil inspector degrades to a no-op so
// the cancel path remains usable when the inspector failed to init.
func NewAsynqTaskInspector(inspector *asynq.Inspector) interfaces.TaskInspector {
	if inspector == nil {
		return noopTaskInspector{}
	}
	return &asynqTaskInspector{inspector: inspector}
}

// NewTaskInspector selects the Redis-backed inspector when asynchronous work
// is configured. Lite deployments have no Redis queue and use a no-op
// implementation, so document housekeeping falls back to stale-row checks.
func NewTaskInspector() interfaces.TaskInspector {
	if strings.TrimSpace(os.Getenv("REDIS_ADDR")) == "" {
		return noopTaskInspector{}
	}
	return NewAsynqTaskInspector(NewAsynqInspector())
}

// DocumentIDProbe is the minimal payload shape we need to filter
// tasks. All pipeline payload types embed a json:"document_id" field,
// so a single struct covers Document / ImageMultimodal / PostProcess /
// Question / Summary / Extract / Manual.
type DocumentIDProbe struct {
	DocumentID string              `json:"document_id,omitempty"`
	Stage      types.DocumentStage `json:"stage,omitempty"`
	Generation int64               `json:"generation,omitempty"`
}

// queuesScanned is the fixed set of queue names this codebase enqueues
// into. Kept tight on purpose — we never scan user-defined queues.
// MUST include every queue any cancelable task type can land in; the
// multimodal queue is required here so cancelling a Document also purges
// its (potentially hundreds of) pending image:multimodal tasks.
var queuesScanned = []string{
	types.QueueDefault,
	types.QueueCritical,
	types.QueueLow,
	types.QueueDownload,
}

// taskTypesForDocumentCancel lists every asynq task type that carries
// a document_id in its payload and should be cancelable. The set is
// deliberately narrow: we don't touch FAQ import / KB-level tasks
// because the cancel API is per-Document.
var taskTypesForDocumentCancel = map[string]struct{}{
	types.TypeDocumentRecognize:      {},
	types.TypePaperMetadataExtract:   {},
	types.TypePaperReferenceDispatch: {},
	types.TypePaperCatalogResolve:    {},
	types.TypeDocumentRemoteImport:   {},
}

// listPageSize caps each Redis LIST call. Asynq pages tasks, so we
// loop until a short page comes back. 100 matches asynq's default.
const listPageSize = 100

// CancelTasksForDocument removes queued tasks whose payload references
// the given document_id and signals active workers running such tasks
// to stop.
func (a *asynqTaskInspector) CancelTasksForDocument(
	ctx context.Context, documentID string,
) (int, int, error) {
	if a == nil || a.inspector == nil || documentID == "" {
		return 0, 0, nil
	}
	deleted := 0
	cancelled := 0

	for _, queue := range queuesScanned {
		// Pending / Scheduled / Retry can all be deleted by task ID.
		// Archived tasks are NOT touched: dead-letter rows are
		// already final and should remain visible to operators.
		deleted += a.deletePendingMatches(ctx, queue, documentID)
		deleted += a.deleteScheduledMatches(ctx, queue, documentID)
		deleted += a.deleteRetryMatches(ctx, queue, documentID)
		cancelled += a.cancelActiveMatches(ctx, queue, documentID)
	}

	logger.Info(ctx,
		"document task cancellation completed",
		"document_id", documentID, "deleted_from_queue", deleted,
		"active_cancel_signaled", cancelled,
	)
	return deleted, cancelled, nil
}

func (a *asynqTaskInspector) CancelTasksForRun(
	ctx context.Context, ownerDocumentID, runID string, kind types.ProcessingWorkflowKind,
) (int, int, error) {
	if a == nil || a.inspector == nil || ownerDocumentID == "" || runID == "" {
		return 0, 0, nil
	}
	deleted, cancelled := 0, 0
	var cleanupErrors []error
	for _, queue := range queuesScanned {
		states := []struct {
			name string
			list func(string, ...asynq.ListOption) ([]*asynq.TaskInfo, error)
		}{
			{"pending", a.inspector.ListPendingTasks},
			{"scheduled", a.inspector.ListScheduledTasks},
			{"retry", a.inspector.ListRetryTasks},
		}
		for _, state := range states {
			matches, err := a.listRunTaskIDs(ctx, queue, state.name, ownerDocumentID, runID, kind, state.list)
			if err != nil {
				cleanupErrors = append(cleanupErrors, err)
				continue
			}
			for _, taskID := range matches {
				if err := a.inspector.DeleteTask(queue, taskID); err != nil {
					cleanupErrors = append(cleanupErrors, fmt.Errorf("delete %s task %s from %s: %w", state.name, taskID, queue, err))
					continue
				}
				deleted++
			}
		}
		matches, err := a.listRunTaskIDs(ctx, queue, "active", ownerDocumentID, runID, kind, a.inspector.ListActiveTasks)
		if err != nil {
			cleanupErrors = append(cleanupErrors, err)
			continue
		}
		for _, taskID := range matches {
			if err := a.inspector.CancelProcessing(taskID); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("signal active task %s in %s: %w", taskID, queue, err))
				continue
			}
			cancelled++
		}
	}
	logger.Info(ctx, "processing run queue cancellation completed",
		"document_id", ownerDocumentID, "run_id", runID, "workflow", kind,
		"deleted_from_queue", deleted, "active_cancel_signaled", cancelled)
	return deleted, cancelled, errors.Join(cleanupErrors...)
}

func (a *asynqTaskInspector) listRunTaskIDs(
	ctx context.Context,
	queue, state, ownerDocumentID, runID string,
	kind types.ProcessingWorkflowKind,
	list func(string, ...asynq.ListOption) ([]*asynq.TaskInfo, error),
) ([]string, error) {
	result := make([]string, 0)
	for page := 1; ; page++ {
		tasks, err := list(queue, asynq.PageSize(listPageSize), asynq.Page(page))
		if err != nil {
			if errors.Is(err, asynq.ErrQueueNotFound) {
				return result, nil
			}
			return result, fmt.Errorf("list %s tasks in %s: %w", state, queue, err)
		}
		for _, task := range tasks {
			if matchesProcessingRun(task.Type, task.Payload, ownerDocumentID, runID, kind) {
				result = append(result, task.ID)
			}
		}
		if len(tasks) < listPageSize {
			return result, nil
		}
	}
}

func matchesProcessingRun(string, []byte, string, string, types.ProcessingWorkflowKind) bool {
	// New tasks are fenced by document stage/generation and never carry run
	// ownership. Historical run rows remain queryable until Phase E, but cannot
	// match or cancel current work.
	return false
}

// CancelReferenceTasksForDocuments removes only the new single-document
// scheduler-owned reference tasks. Run ownership is looked up by the caller
// before this best-effort queue sweep, so no run ID needs to leak into JSON.
func (a *asynqTaskInspector) CancelReferenceTasksForDocuments(ctx context.Context, documentIDs []string) (int, int, error) {
	if a == nil || a.inspector == nil || len(documentIDs) == 0 {
		return 0, 0, nil
	}
	targets := make(map[string]struct{}, len(documentIDs))
	for _, id := range documentIDs {
		if id != "" {
			targets[id] = struct{}{}
		}
	}
	deleted, cancelled := 0, 0
	var cleanupErrors []error
	for _, queue := range queuesScanned {
		for _, state := range []struct {
			list func(string, ...asynq.ListOption) ([]*asynq.TaskInfo, error)
			name string
		}{
			{a.inspector.ListPendingTasks, "pending"}, {a.inspector.ListScheduledTasks, "scheduled"}, {a.inspector.ListRetryTasks, "retry"},
		} {
			matches, err := a.listReferenceTargetTaskIDs(queue, state.name, targets, state.list)
			if err != nil {
				cleanupErrors = append(cleanupErrors, err)
				continue
			}
			for _, taskID := range matches {
				if err := a.inspector.DeleteTask(queue, taskID); err != nil {
					cleanupErrors = append(cleanupErrors, fmt.Errorf("delete scheduler target task %s from %s: %w", taskID, queue, err))
					continue
				}
				deleted++
			}
		}
		matches, err := a.listReferenceTargetTaskIDs(queue, "active", targets, a.inspector.ListActiveTasks)
		if err != nil {
			cleanupErrors = append(cleanupErrors, err)
			continue
		}
		for _, taskID := range matches {
			if err := a.inspector.CancelProcessing(taskID); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("signal scheduler target task %s in %s: %w", taskID, queue, err))
				continue
			}
			cancelled++
		}
	}
	return deleted, cancelled, errors.Join(cleanupErrors...)
}

func (a *asynqTaskInspector) listReferenceTargetTaskIDs(
	queue, state string,
	targets map[string]struct{},
	list func(string, ...asynq.ListOption) ([]*asynq.TaskInfo, error),
) ([]string, error) {
	result := make([]string, 0)
	for page := 1; ; page++ {
		tasks, err := list(queue, asynq.PageSize(listPageSize), asynq.Page(page))
		if err != nil {
			if errors.Is(err, asynq.ErrQueueNotFound) {
				return result, nil
			}
			return result, fmt.Errorf("list %s scheduler target tasks in %s: %w", state, queue, err)
		}
		for _, task := range tasks {
			if matchesSchedulerReferenceTask(task.Type, task.Payload, targets) {
				result = append(result, task.ID)
			}
		}
		if len(tasks) < listPageSize {
			return result, nil
		}
	}
}

func matchesSchedulerReferenceTask(taskType string, payload []byte, targets map[string]struct{}) bool {
	if taskType != types.TypePaperCatalogResolve {
		return false
	}
	var probe DocumentIDProbe
	if json.Unmarshal(payload, &probe) != nil || probe.Stage != types.DocumentStageSourceLookup || probe.Generation <= 0 {
		return false
	}
	_, ok := targets[probe.DocumentID]
	return ok
}

// HasQueuedTasksForDocument reports whether any pending / scheduled /
// retry / active task referencing DocumentID still lives in the queue.
// Read-only counterpart of CancelTasksForDocument — the housekeeping
// sweep uses it to avoid flagging a backlogged-but-not-orphaned row as
// failed. Short-circuits on the first match and never deletes anything.
func (a *asynqTaskInspector) HasQueuedTasksForDocument(
	ctx context.Context, documentID string,
) (bool, error) {
	if a == nil || a.inspector == nil || documentID == "" {
		return false, nil
	}
	listers := []struct {
		state string
		list  func(string, ...asynq.ListOption) ([]*asynq.TaskInfo, error)
	}{
		{"pending", a.inspector.ListPendingTasks},
		{"scheduled", a.inspector.ListScheduledTasks},
		{"retry", a.inspector.ListRetryTasks},
		{"active", a.inspector.ListActiveTasks},
	}
	for _, queue := range queuesScanned {
		for _, l := range listers {
			if a.queueStateHasMatch(ctx, queue, documentID, "", l.state, l.list) {
				return true, nil
			}
		}
	}
	return false, nil
}

func (a *asynqTaskInspector) HasQueuedTaskTypeForDocument(
	ctx context.Context,
	documentID string,
	taskType string,
) (bool, error) {
	if a == nil || a.inspector == nil || documentID == "" {
		return false, nil
	}
	if _, supported := taskTypesForDocumentCancel[taskType]; !supported {
		return false, nil
	}
	listers := []struct {
		state string
		list  func(string, ...asynq.ListOption) ([]*asynq.TaskInfo, error)
	}{
		{"pending", a.inspector.ListPendingTasks},
		{"scheduled", a.inspector.ListScheduledTasks},
		{"retry", a.inspector.ListRetryTasks},
		{"active", a.inspector.ListActiveTasks},
	}
	for _, queue := range queuesScanned {
		for _, lister := range listers {
			if a.queueStateHasMatch(
				ctx,
				queue,
				documentID,
				taskType,
				lister.state,
				lister.list,
			) {
				return true, nil
			}
		}
	}
	return false, nil
}

// queueStateHasMatch pages through one (queue, state) list looking for a
// task that references DocumentID. Mirrors the delete* scanners but is
// strictly read-only and returns early on the first hit. A backend error
// is logged and treated as "no match" (false); the caller's fail-safe
// then errs toward recovering the row rather than preserving it forever.
func (a *asynqTaskInspector) queueStateHasMatch(
	ctx context.Context, queue, documentID, taskType, state string,
	list func(string, ...asynq.ListOption) ([]*asynq.TaskInfo, error),
) bool {
	page := 1
	for {
		tasks, err := list(queue, asynq.PageSize(listPageSize), asynq.Page(page))
		if err != nil {
			if !errors.Is(err, asynq.ErrQueueNotFound) {
				logger.Warn(ctx, "inspect document task queue failed",
					"state", state, "queue", queue, "page", page, "error", err)
			}
			return false
		}
		if len(tasks) == 0 {
			return false
		}
		for _, t := range tasks {
			if matchesDocument(t.Type, t.Payload, documentID, taskType) {
				return true
			}
		}
		if len(tasks) < listPageSize {
			return false
		}
		page++
	}
}

// matchesDocument returns true when the task type is one we cancel
// AND its payload references the target Document ID.
func matchesDocument(
	taskType string,
	payload []byte,
	documentID string,
	requiredTaskType string,
) bool {
	if _, ok := taskTypesForDocumentCancel[taskType]; !ok {
		return false
	}
	if requiredTaskType != "" && taskType != requiredTaskType {
		return false
	}
	var probe DocumentIDProbe
	if err := json.Unmarshal(payload, &probe); err != nil {
		return false
	}
	return probe.DocumentID == documentID
}

func (a *asynqTaskInspector) deletePendingMatches(ctx context.Context, queue, DocumentID string) int {
	deleted := 0
	page := 1
	for {
		tasks, err := a.inspector.ListPendingTasks(queue, asynq.PageSize(listPageSize), asynq.Page(page))
		if err != nil {
			if !errors.Is(err, asynq.ErrQueueNotFound) {
				logger.Warn(ctx, "list pending document tasks failed", "queue", queue, "page", page, "error", err)
			}
			return deleted
		}
		if len(tasks) == 0 {
			return deleted
		}
		for _, t := range tasks {
			if !matchesDocument(t.Type, t.Payload, DocumentID, "") {
				continue
			}
			if err := a.inspector.DeleteTask(queue, t.ID); err != nil {
				logger.Warn(ctx, "delete pending document task failed", "queue", queue, "task_type", t.Type, "task_id", t.ID, "error", err)
				continue
			}
			deleted++
		}
		if len(tasks) < listPageSize {
			return deleted
		}
		page++
	}
}

func (a *asynqTaskInspector) deleteScheduledMatches(ctx context.Context, queue, DocumentID string) int {
	deleted := 0
	page := 1
	for {
		tasks, err := a.inspector.ListScheduledTasks(queue, asynq.PageSize(listPageSize), asynq.Page(page))
		if err != nil {
			if !errors.Is(err, asynq.ErrQueueNotFound) {
				logger.Warn(ctx, "list scheduled document tasks failed", "queue", queue, "page", page, "error", err)
			}
			return deleted
		}
		if len(tasks) == 0 {
			return deleted
		}
		for _, t := range tasks {
			if !matchesDocument(t.Type, t.Payload, DocumentID, "") {
				continue
			}
			if err := a.inspector.DeleteTask(queue, t.ID); err != nil {
				logger.Warn(ctx, "delete scheduled document task failed", "queue", queue, "task_type", t.Type, "task_id", t.ID, "error", err)
				continue
			}
			deleted++
		}
		if len(tasks) < listPageSize {
			return deleted
		}
		page++
	}
}

func (a *asynqTaskInspector) deleteRetryMatches(ctx context.Context, queue, DocumentID string) int {
	deleted := 0
	page := 1
	for {
		tasks, err := a.inspector.ListRetryTasks(queue, asynq.PageSize(listPageSize), asynq.Page(page))
		if err != nil {
			if !errors.Is(err, asynq.ErrQueueNotFound) {
				logger.Warn(ctx, "list retry document tasks failed", "queue", queue, "page", page, "error", err)
			}
			return deleted
		}
		if len(tasks) == 0 {
			return deleted
		}
		for _, t := range tasks {
			if !matchesDocument(t.Type, t.Payload, DocumentID, "") {
				continue
			}
			if err := a.inspector.DeleteTask(queue, t.ID); err != nil {
				logger.Warn(ctx, "delete retry document task failed", "queue", queue, "task_type", t.Type, "task_id", t.ID, "error", err)
				continue
			}
			deleted++
		}
		if len(tasks) < listPageSize {
			return deleted
		}
		page++
	}
}

// cancelActiveMatches signals active workers to abort via
// Inspector.CancelProcessing. The worker's ctx becomes Done() so the
// next blocking call (or our checkpoint reads) bails. The DB-level
// abort flag (parse_status=cancelled) remains the durable signal —
// this is a latency optimization, not the correctness mechanism.
func (a *asynqTaskInspector) cancelActiveMatches(ctx context.Context, queue, DocumentID string) int {
	cancelled := 0
	page := 1
	for {
		tasks, err := a.inspector.ListActiveTasks(queue, asynq.PageSize(listPageSize), asynq.Page(page))
		if err != nil {
			if !errors.Is(err, asynq.ErrQueueNotFound) {
				logger.Warn(ctx, "list active document tasks failed", "queue", queue, "page", page, "error", err)
			}
			return cancelled
		}
		if len(tasks) == 0 {
			return cancelled
		}
		for _, t := range tasks {
			if !matchesDocument(t.Type, t.Payload, DocumentID, "") {
				continue
			}
			if err := a.inspector.CancelProcessing(t.ID); err != nil {
				logger.Warn(ctx, "cancel active document task failed", "queue", queue, "task_type", t.Type, "task_id", t.ID, "error", err)
				continue
			}
			cancelled++
		}
		if len(tasks) < listPageSize {
			return cancelled
		}
		page++
	}
}

// noopTaskInspector is the Lite-mode (no Redis) inspector. Inline
// goroutines spawned by SyncTaskExecutor cannot be dequeued before
// they start; the checkpoint-based abort in worker code is the only
// stop signal in that mode.
type noopTaskInspector struct{}

// NewNoopTaskInspector returns a no-op TaskInspector for Lite mode.
func NewNoopTaskInspector() interfaces.TaskInspector { return noopTaskInspector{} }

func (noopTaskInspector) CancelTasksForDocument(
	ctx context.Context, documentID string,
) (int, int, error) {
	return 0, 0, nil
}

func (noopTaskInspector) CancelTasksForRun(context.Context, string, string, types.ProcessingWorkflowKind) (int, int, error) {
	return 0, 0, nil
}

func (noopTaskInspector) CancelReferenceTasksForDocuments(context.Context, []string) (int, int, error) {
	return 0, 0, nil
}

// HasQueuedTasksForDocument always reports false in Lite mode: inline
// executors never enqueue, so there is no backlog to protect against and
// the housekeeping sweep's span/updated_at checks stay authoritative.
func (noopTaskInspector) HasQueuedTasksForDocument(
	ctx context.Context, documentID string,
) (bool, error) {
	return false, nil
}

func (noopTaskInspector) HasQueuedTaskTypeForDocument(
	ctx context.Context,
	documentID string,
	taskType string,
) (bool, error) {
	return false, nil
}
