package repository

import (
	"context"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"paperMaster/internal/types"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestProcessingRunRepositoryPostgres(t *testing.T) {
	db, cleanup := openProcessingRunTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	ownerID := uuid.New()
	if err := db.Exec(`INSERT INTO documents
		(id, original_filename, content_type, size_bytes, storage_key, status)
		VALUES (?, 'phase1.pdf', 'application/pdf', 1, ?, 'uploaded')`, ownerID, "phase1/"+ownerID.String()).Error; err != nil {
		t.Fatalf("insert owner document: %v", err)
	}

	repo := NewProcessingRunRepository(db)
	firstInput := types.ProcessingRun{
		ID: uuid.New(), OwnerDocumentID: ownerID,
		WorkflowKind:   types.ProcessingWorkflowCatalogReference,
		IdempotencyKey: "catalog:attempt:1", Attempt: 1,
	}
	first, created, err := repo.CreateQueued(ctx, firstInput)
	if err != nil || !created {
		t.Fatalf("CreateQueued(first) run=%#v created=%t err=%v", first, created, err)
	}
	duplicate, created, err := repo.CreateQueued(ctx, types.ProcessingRun{
		ID: uuid.New(), OwnerDocumentID: ownerID,
		WorkflowKind:   types.ProcessingWorkflowCatalogReference,
		IdempotencyKey: "catalog:attempt:1", Attempt: 1,
	})
	if err != nil || created || duplicate.ID != first.ID {
		t.Fatalf("CreateQueued(duplicate) run=%#v created=%t err=%v, want %s", duplicate, created, err, first.ID)
	}

	startGate := make(chan struct{})
	type transitionResult struct {
		run     types.ProcessingRun
		applied bool
		err     error
	}
	started, cancelled := make(chan transitionResult, 1), make(chan transitionResult, 1)
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-startGate
		run, applied, err := repo.TryStart(ctx, first.ID)
		started <- transitionResult{run: run, applied: applied, err: err}
	}()
	go func() {
		defer wait.Done()
		<-startGate
		run, applied, err := repo.RequestCancel(ctx, first.ID, "USER_CANCELLED", "cancelled by test")
		cancelled <- transitionResult{run: run, applied: applied, err: err}
	}()
	close(startGate)
	wait.Wait()
	startResult, cancelResult := <-started, <-cancelled
	if startResult.err != nil || cancelResult.err != nil {
		t.Fatalf("concurrent transitions start=%#v cancel=%#v", startResult, cancelResult)
	}
	if !cancelResult.applied {
		t.Fatalf("RequestCancel applied=false after concurrent start; result=%#v", cancelResult)
	}
	stored, err := repo.Get(ctx, first.ID)
	if err != nil {
		t.Fatalf("Get(after cancellation): %v", err)
	}
	if stored.State != types.ProcessingRunCancelRequested || stored.CancelRequestedAt == nil {
		t.Fatalf("state after concurrent start/cancel = %#v, want cancel_requested", stored)
	}
	if repeated, applied, err := repo.RequestCancel(ctx, first.ID, "USER_CANCELLED", "cancelled again"); err != nil || applied || repeated.State != types.ProcessingRunCancelRequested {
		t.Fatalf("RequestCancel(repeated) run=%#v applied=%t err=%v", repeated, applied, err)
	}
	if _, applied, err := repo.TryStart(ctx, first.ID); err != nil || applied {
		t.Fatalf("TryStart(cancel_requested) applied=%t err=%v, want false nil", applied, err)
	}
	if _, applied, err := repo.Finish(ctx, first.ID, types.ProcessingRunSucceeded, "", ""); err != nil || applied {
		t.Fatalf("Finish(success after cancel request) applied=%t err=%v, want false nil", applied, err)
	}
	terminal, applied, err := repo.Finish(ctx, first.ID, types.ProcessingRunCancelled, "USER_CANCELLED", "cancelled by test")
	if err != nil || !applied || terminal.State != types.ProcessingRunCancelled || terminal.TerminalAt == nil {
		t.Fatalf("Finish(cancelled) run=%#v applied=%t err=%v", terminal, applied, err)
	}
	if _, applied, err := repo.TryStart(ctx, first.ID); err != nil || applied {
		t.Fatalf("TryStart(terminal) applied=%t err=%v, want false nil", applied, err)
	}

	retry, created, err := repo.CreateQueued(ctx, types.ProcessingRun{
		OwnerDocumentID: ownerID, WorkflowKind: types.ProcessingWorkflowCatalogReference,
		IdempotencyKey: "catalog:attempt:2", Attempt: time.Now().UnixNano(),
	})
	if err != nil || !created || retry.ID == first.ID {
		t.Fatalf("CreateQueued(explicit retry) run=%#v created=%t err=%v", retry, created, err)
	}
	if _, applied, err := repo.TryStart(ctx, retry.ID); err != nil || !applied {
		t.Fatalf("TryStart(retry) applied=%t err=%v", applied, err)
	}
	if queued, applied, err := repo.MarkRetryQueued(ctx, retry.ID, "RETRY", "retry later"); err != nil || !applied || queued.State != types.ProcessingRunQueued {
		t.Fatalf("MarkRetryQueued() run=%#v applied=%t err=%v", queued, applied, err)
	}
	if active, err := repo.ListActive(ctx, ownerID, types.ProcessingWorkflowCatalogReference); err != nil || len(active) != 1 || active[0].ID != retry.ID {
		t.Fatalf("ListActive() runs=%#v err=%v", active, err)
	}
	if _, applied, err := repo.Finish(ctx, retry.ID, types.ProcessingRunSucceeded, "", ""); err != nil || applied {
		t.Fatalf("Finish(success from queued) applied=%t err=%v, want false nil", applied, err)
	}
	if _, applied, err := repo.TryStart(ctx, retry.ID); err != nil || !applied {
		t.Fatalf("TryStart(retry after requeue) applied=%t err=%v", applied, err)
	}
	if succeeded, applied, err := repo.Finish(ctx, retry.ID, types.ProcessingRunSucceeded, "IGNORED", "ignored"); err != nil || !applied || succeeded.State != types.ProcessingRunSucceeded || succeeded.ErrorCode != "" {
		t.Fatalf("Finish(succeeded) run=%#v applied=%t err=%v", succeeded, applied, err)
	}
	if latest, err := repo.LatestAttempt(ctx, ownerID, types.ProcessingWorkflowCatalogReference); err != nil || latest != retry.Attempt {
		t.Fatalf("LatestAttempt() = %d, error %v, want %d", latest, err, retry.Attempt)
	}

	fanout, created, err := repo.CreateQueued(ctx, types.ProcessingRun{
		OwnerDocumentID: ownerID, WorkflowKind: types.ProcessingWorkflowReferenceDownload,
		IdempotencyKey: "download:items", Attempt: 2,
	})
	if err != nil || !created {
		t.Fatalf("CreateQueued(fanout) run=%#v created=%t err=%v", fanout, created, err)
	}
	if _, applied, err := repo.TryStart(ctx, fanout.ID); err != nil || !applied {
		t.Fatalf("TryStart(fanout) applied=%t err=%v", applied, err)
	}
	for _, itemKey := range []string{"target-a", "target-b"} {
		if _, created, err := repo.RegisterItem(ctx, fanout.ID, itemKey); err != nil || !created {
			t.Fatalf("RegisterItem(%s) created=%t err=%v", itemKey, created, err)
		}
		if _, created, err := repo.RegisterItem(ctx, fanout.ID, itemKey); err != nil || created {
			t.Fatalf("RegisterItem(%s duplicate) created=%t err=%v", itemKey, created, err)
		}
		if _, applied, err := repo.TryStartItem(ctx, fanout.ID, itemKey); err != nil || !applied {
			t.Fatalf("TryStartItem(%s) applied=%t err=%v", itemKey, applied, err)
		}
	}
	if run, err := repo.FinishItem(ctx, fanout.ID, "target-a", types.ProcessingRunSucceeded, "", ""); err != nil || run.State != types.ProcessingRunRunning {
		t.Fatalf("FinishItem(before seal) run=%#v err=%v", run, err)
	}
	if run, err := repo.SealItems(ctx, fanout.ID); err != nil || run.State != types.ProcessingRunRunning || run.ItemsSealedAt == nil {
		t.Fatalf("SealItems(active child) run=%#v err=%v", run, err)
	}
	if run, err := repo.FinishItem(ctx, fanout.ID, "target-b", types.ProcessingRunFailed, "TASK_FAILED", "safe"); err != nil || run.State != types.ProcessingRunFailed {
		t.Fatalf("FinishItem(final failure) run=%#v err=%v", run, err)
	}

	legacyAttempt := time.Now().UnixNano()
	legacySpanID := uuid.New()
	if err := db.Exec(`INSERT INTO document_processing_spans
		(id, document_id, attempt, kind, scope, scope_id, status, started_at)
		VALUES (?, ?, ?, 'recognition', 'document', 'recognition:legacy', 'succeeded', CURRENT_TIMESTAMP)`,
		legacySpanID, ownerID, legacyAttempt).Error; err != nil {
		t.Fatalf("insert legacy span with BIGINT attempt: %v", err)
	}
	var legacy struct {
		Attempt int64
		RunID   *uuid.UUID
	}
	if err := db.Table("document_processing_spans").Select("attempt, run_id").Where("id = ?", legacySpanID).Scan(&legacy).Error; err != nil {
		t.Fatalf("load legacy span: %v", err)
	}
	if legacy.Attempt != legacyAttempt || legacy.RunID != nil {
		t.Fatalf("legacy span = %#v, want attempt=%d and nil run_id", legacy, legacyAttempt)
	}

	linkedRun, created, err := repo.CreateQueued(ctx, types.ProcessingRun{
		OwnerDocumentID: ownerID, WorkflowKind: types.ProcessingWorkflowReferenceDownload,
		IdempotencyKey: "download:fk", Attempt: 3,
	})
	if err != nil || !created {
		t.Fatalf("CreateQueued(linked run) run=%#v created=%t err=%v", linkedRun, created, err)
	}
	if _, created, err := repo.RegisterItem(ctx, linkedRun.ID, "linked-target"); err != nil || !created {
		t.Fatalf("RegisterItem(linked target) created=%t err=%v", created, err)
	}
	linkedSpanID := uuid.New()
	if err := db.Exec(`INSERT INTO document_processing_spans
		(id, document_id, run_id, attempt, kind, scope, scope_id, status, started_at)
		VALUES (?, ?, ?, 3, 'reference_download', 'remote_document', 'reference_download:linked', 'running', CURRENT_TIMESTAMP)`,
		linkedSpanID, ownerID, linkedRun.ID).Error; err != nil {
		t.Fatalf("insert run-linked span: %v", err)
	}
	targetID, targetSpanID := uuid.New(), uuid.New()
	if err := db.Exec(`INSERT INTO documents
		(id, original_filename, content_type, size_bytes, storage_key, status, source_state)
		VALUES (?, 'target.pdf', 'application/pdf', 1, ?, 'unknown', 'missing')`, targetID, "unknown://"+targetID.String()).Error; err != nil {
		t.Fatalf("insert target document: %v", err)
	}
	if err := db.Exec(`INSERT INTO document_processing_spans
		(id, document_id, run_id, parent_span_id, attempt, kind, scope, scope_id, status, started_at)
		VALUES (?, ?, ?, ?, 3, 'download_pdf', 'remote_document', 'download_pdf:linked', 'running', CURRENT_TIMESTAMP)`,
		targetSpanID, targetID, linkedRun.ID, linkedSpanID).Error; err != nil {
		t.Fatalf("insert cross-document run span: %v", err)
	}
	cancelledRuns, err := repo.RequestCancelActive(ctx, ownerID, types.ProcessingWorkflowReferenceDownload,
		"USER_CANCELLED", "cancelled by integration test")
	if err != nil || len(cancelledRuns) != 1 || cancelledRuns[0].ID != linkedRun.ID ||
		cancelledRuns[0].State != types.ProcessingRunCancelRequested {
		t.Fatalf("RequestCancelActive() runs=%#v error=%v", cancelledRuns, err)
	}
	var cancelledSpanCount int64
	if err := db.Table("document_processing_spans").Where("run_id = ? AND status = 'cancelled'", linkedRun.ID).Count(&cancelledSpanCount).Error; err != nil {
		t.Fatalf("count cancelled run spans: %v", err)
	}
	if cancelledSpanCount != 2 {
		t.Fatalf("cancelled cross-document spans = %d, want 2", cancelledSpanCount)
	}
	var cancelledItemCount int64
	if err := db.Model(&types.ProcessingRunItem{}).
		Where("run_id = ? AND state = ?", linkedRun.ID, types.ProcessingRunCancelled).
		Count(&cancelledItemCount).Error; err != nil || cancelledItemCount != 1 {
		t.Fatalf("cancelled run items = %d, error %v", cancelledItemCount, err)
	}
	if _, applied, err := repo.Finish(ctx, linkedRun.ID, types.ProcessingRunCancelled,
		"USER_CANCELLED", "cancelled by integration test"); err != nil || !applied {
		t.Fatalf("Finish(cancelled linked run) applied=%v error=%v", applied, err)
	}
	if err := db.Exec("DELETE FROM processing_runs WHERE id = ?", linkedRun.ID).Error; err != nil {
		t.Fatalf("delete linked run: %v", err)
	}
	var linkedSpanCount int64
	if err := db.Table("document_processing_spans").Where("id = ?", linkedSpanID).Count(&linkedSpanCount).Error; err != nil {
		t.Fatalf("count cascaded linked span: %v", err)
	}
	if err := db.Table("document_processing_spans").Where("id IN ?", []uuid.UUID{linkedSpanID, targetSpanID}).Count(&linkedSpanCount).Error; err != nil {
		t.Fatalf("count cascaded cross-document spans: %v", err)
	}
	if linkedSpanCount != 0 {
		t.Fatalf("run-linked spans remaining after run deletion = %d, want 0", linkedSpanCount)
	}
}

func openProcessingRunTestDatabase(t *testing.T) (*gorm.DB, func()) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("PAPERS_MANAGER_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("PAPERS_MANAGER_TEST_POSTGRES_DSN is not configured")
	}
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open PostgreSQL test database: %v", err)
	}
	adminSQL, err := admin.DB()
	if err != nil {
		t.Fatalf("get PostgreSQL admin sql.DB: %v", err)
	}
	schema := "processing_run_phase1_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		adminSQL.Close()
		t.Fatalf("create test schema: %v", err)
	}
	scopedDSN := processingRunSearchPathDSN(t, dsn, schema)
	db, err := gorm.Open(postgres.Open(scopedDSN), &gorm.Config{})
	if err != nil {
		_ = admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error
		adminSQL.Close()
		t.Fatalf("open scoped PostgreSQL database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get scoped PostgreSQL sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(8)
	sqlDB.SetMaxIdleConns(8)
	if err := Migrate(context.Background(), db); err != nil {
		sqlDB.Close()
		_ = admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error
		adminSQL.Close()
		t.Fatalf("migrate scoped PostgreSQL database: %v", err)
	}
	cleanup := func() {
		sqlDB.Close()
		if err := admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Errorf("drop test schema: %v", err)
		}
		adminSQL.Close()
	}
	return db, cleanup
}

func processingRunSearchPathDSN(t *testing.T, dsn, schema string) string {
	t.Helper()
	if strings.Contains(dsn, "://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatalf("parse PostgreSQL DSN: %v", err)
		}
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return dsn + " search_path=" + schema
}
