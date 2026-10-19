package service

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"paperMaster/internal/application/repository"
	"paperMaster/internal/config"
	"paperMaster/internal/types"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// TestFailInterruptedWorkPostgres verifies the restart policy for durable
// stage rows: running work from the dead process is failed, queued work is
// left for Asynq redelivery, and active spans are closed.
func TestFailInterruptedWorkPostgres(t *testing.T) {
	db, cleanup := openServicePostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	staleAt := time.Now().UTC().Add(-time.Hour)

	runningID := uuid.New()
	queuedID := uuid.New()
	deletingID := uuid.New()
	targetID := uuid.New()
	for _, document := range []struct {
		id    uuid.UUID
		state types.DocumentSourceState
	}{
		{runningID, types.DocumentSourceAvailable},
		{queuedID, types.DocumentSourceAvailable},
		{deletingID, types.DocumentSourceDeleting},
		{targetID, types.DocumentSourceAvailable},
	} {
		if err := db.Exec(`INSERT INTO documents
			(id, original_filename, content_type, size_bytes, storage_key, source_state, updated_at)
			VALUES (?, 'startup.pdf', 'application/pdf', 1, ?, ?, ?)`,
			document.id, "startup/"+document.id.String(), document.state, staleAt).Error; err != nil {
			t.Fatalf("insert document: %v", err)
		}
	}

	// The interrupted document has every stage running, each at its own
	// generation. The queued document distinguishes queued (keep) from
	// catalog queued (fail).
	if err := db.Exec(`UPDATE documents SET
		markdown_status = 'running', markdown_generation = 2, markdown_updated_at = ?,
		metadata_extraction_status = 'running', metadata_extraction_generation = 3, metadata_extraction_updated_at = ?,
		source_lookup_status = 'running', source_lookup_generation = 4, source_lookup_updated_at = ?,
		reference_lookup_status = 'running', reference_lookup_generation = 5, reference_lookup_updated_at = ?
		WHERE id = ?`, staleAt, staleAt, staleAt, staleAt, runningID).Error; err != nil {
		t.Fatalf("mark interrupted document running: %v", err)
	}
	if err := db.Exec(`UPDATE documents SET
		markdown_status = 'queued', markdown_generation = 1, markdown_updated_at = ?,
		metadata_extraction_status = 'succeeded', metadata_extraction_generation = 1, metadata_extraction_updated_at = ?,
		source_lookup_status = 'queued', source_lookup_generation = 1, source_lookup_updated_at = ?,
		reference_lookup_status = 'queued', reference_lookup_generation = 1, reference_lookup_updated_at = ?
		WHERE id = ?`, staleAt, staleAt, staleAt, staleAt, queuedID).Error; err != nil {
		t.Fatalf("mark queued document: %v", err)
	}
	if err := db.Exec(`UPDATE documents SET
		markdown_status = 'running', markdown_generation = 1, markdown_updated_at = ?
		WHERE id = ?`, staleAt, deletingID).Error; err != nil {
		t.Fatalf("mark deleting document running: %v", err)
	}

	// One unresolved running reference and one already-resolved reference.
	// Only the unresolved row may transition to failed.
	unresolvedReferenceID := uuid.New()
	resolvedReferenceID := uuid.New()
	for _, reference := range []struct {
		id       uuid.UUID
		key      string
		resolved *uuid.UUID
	}{
		{unresolvedReferenceID, "ref-unresolved", nil},
		{resolvedReferenceID, "ref-resolved", &targetID},
	} {
		if err := db.Exec(`INSERT INTO paper_citations
			(id, citing_document_id, reference_key, raw_reference, resolution_status,
			 resolution_generation, resolution_updated_at, cited_document_id, updated_at)
			VALUES (?, ?, ?, 'reference', 'running', 2, ?, ?, ?)`,
			reference.id, runningID, reference.key, staleAt, reference.resolved, staleAt).Error; err != nil {
			t.Fatalf("insert reference: %v", err)
		}
	}

	// Active spans from the dead process. A running non-stage span (for
	// example a remote download) is owned by a task Asynq will redeliver and
	// must be left untouched.
	spanIDs := map[string]uuid.UUID{}
	for _, span := range []struct {
		key    string
		kind   string
		status string
	}{
		{"recognition", string(types.ProcessingSpanRecognition), "running"},
		{"metadata", string(types.ProcessingSpanMetadataExtract), "running"},
		{"catalog_source", string(types.ProcessingSpanCatalogSource), "running"},
		{"catalog_reference", string(types.ProcessingSpanCatalogReference), "queued"},
		{"catalog_provider", string(types.ProcessingSpanCatalogProvider), "running"},
		{"download", string(types.ProcessingSpanDownloadPDF), "running"},
		{"finished", string(types.ProcessingSpanRecognition), "succeeded"},
	} {
		id := uuid.New()
		spanIDs[span.key] = id
		if err := db.Exec(`INSERT INTO document_processing_spans
			(id, document_id, attempt, kind, scope, scope_id, status, started_at, finished_at, updated_at)
			VALUES (?, ?, 1, ?, 'document', ?, ?, ?, ?, ?)`,
			id, runningID, span.kind, span.key, span.status, staleAt,
			nullableFinished(span.status, staleAt), staleAt).Error; err != nil {
			t.Fatalf("insert span %s: %v", span.key, err)
		}
	}

	housekeeping := NewDocumentHousekeepingService(db, config.Config{}, nil, nil)
	if err := housekeeping.FailInterruptedWork(ctx); err != nil {
		t.Fatalf("FailInterruptedWork: %v", err)
	}

	var stages struct {
		MarkdownStatus      string
		MarkdownGeneration  int64
		MetadataStatus      string
		MetadataGeneration  int64
		SourceStatus        string
		SourceGeneration    int64
		ReferenceStatus     string
		ReferenceGeneration int64
	}
	if err := db.Raw(`SELECT
		markdown_status AS markdown_status, markdown_generation AS markdown_generation,
		metadata_extraction_status AS metadata_status, metadata_extraction_generation AS metadata_generation,
		source_lookup_status AS source_status, source_lookup_generation AS source_generation,
		reference_lookup_status AS reference_status, reference_lookup_generation AS reference_generation
		FROM documents WHERE id = ?`, runningID).Scan(&stages).Error; err != nil {
		t.Fatalf("load interrupted stages: %v", err)
	}
	if stages.MarkdownStatus != string(types.DocumentStageFailed) || stages.MarkdownGeneration != 2 ||
		stages.MetadataStatus != string(types.DocumentStageFailed) || stages.MetadataGeneration != 3 ||
		stages.SourceStatus != string(types.DocumentStageFailed) || stages.SourceGeneration != 4 ||
		stages.ReferenceStatus != string(types.DocumentStageFailed) || stages.ReferenceGeneration != 5 {
		t.Fatalf("interrupted stages = %#v", stages)
	}

	var queued struct {
		MarkdownStatus  string
		MetadataStatus  string
		SourceStatus    string
		ReferenceStatus string
	}
	if err := db.Raw(`SELECT markdown_status, metadata_extraction_status AS metadata_status,
		source_lookup_status AS source_status, reference_lookup_status AS reference_status
		FROM documents WHERE id = ?`, queuedID).Scan(&queued).Error; err != nil {
		t.Fatalf("load queued stages: %v", err)
	}
	if queued.MarkdownStatus != string(types.DocumentStageQueued) ||
		queued.MetadataStatus != string(types.DocumentStageSucceeded) ||
		queued.SourceStatus != string(types.DocumentStageFailed) ||
		queued.ReferenceStatus != string(types.DocumentStageFailed) {
		t.Fatalf("queued stages = %#v", queued)
	}

	var deletingMarkdown string
	if err := db.Raw(`SELECT markdown_status FROM documents WHERE id = ?`, deletingID).Scan(&deletingMarkdown).Error; err != nil {
		t.Fatalf("load deleting stage: %v", err)
	}
	if deletingMarkdown != string(types.DocumentStageRunning) {
		t.Fatalf("deleting document markdown status = %q", deletingMarkdown)
	}

	var citations []struct {
		ID     uuid.UUID
		Status string
	}
	if err := db.Raw(`SELECT id, resolution_status AS status FROM paper_citations
		WHERE citing_document_id = ? ORDER BY reference_key`, runningID).Scan(&citations).Error; err != nil {
		t.Fatalf("load citations: %v", err)
	}
	if len(citations) != 2 ||
		citations[0].ID != resolvedReferenceID || citations[0].Status != string(types.DocumentStageRunning) ||
		citations[1].ID != unresolvedReferenceID || citations[1].Status != string(types.DocumentStageFailed) {
		t.Fatalf("citations = %#v", citations)
	}

	for key, want := range map[string]string{
		"recognition":       string(types.ProcessingSpanCancelled),
		"metadata":          string(types.ProcessingSpanCancelled),
		"catalog_source":    string(types.ProcessingSpanCancelled),
		"catalog_reference": string(types.ProcessingSpanCancelled),
		"catalog_provider":  string(types.ProcessingSpanCancelled),
		"download":          string(types.ProcessingSpanRunning),
		"finished":          string(types.ProcessingSpanSucceeded),
	} {
		var span struct {
			Status     string
			FinishedAt *time.Time
		}
		if err := db.Raw(`SELECT status, finished_at FROM document_processing_spans WHERE id = ?`, spanIDs[key]).Scan(&span).Error; err != nil {
			t.Fatalf("load span %s: %v", key, err)
		}
		if span.Status != want {
			t.Fatalf("span %s status = %q, want %q", key, span.Status, want)
		}
		if want == string(types.ProcessingSpanCancelled) && span.FinishedAt == nil {
			t.Fatalf("cancelled span %s has no finished_at", key)
		}
	}
}

// TestRecoverStaleStagesLeavesCandidateOnProbeErrorPostgres covers P1-3: a
// failed queue probe is not evidence that the task disappeared, so the row
// must stay running until a successful probe reports no matching task.
func TestRecoverStaleStagesLeavesCandidateOnProbeErrorPostgres(t *testing.T) {
	db, cleanup := openServicePostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	staleAt := time.Now().UTC().Add(-2 * time.Hour)
	documentID := uuid.New()
	if err := db.Exec(`INSERT INTO documents
		(id, original_filename, content_type, size_bytes, storage_key, source_state, updated_at)
		VALUES (?, 'probe.pdf', 'application/pdf', 1, ?, 'available', ?)`,
		documentID, "probe/"+documentID.String(), staleAt).Error; err != nil {
		t.Fatalf("insert document: %v", err)
	}
	if err := db.Exec(`UPDATE documents SET
		markdown_status = 'running', markdown_generation = 1, markdown_updated_at = ?
		WHERE id = ?`, staleAt, documentID).Error; err != nil {
		t.Fatalf("mark stage running: %v", err)
	}

	inspector := &scriptedTaskInspector{}
	housekeeping := NewDocumentHousekeepingService(db, config.Config{}, inspector, nil)
	markdownStatus := func() types.DocumentStageStatus {
		t.Helper()
		var status string
		if err := db.Raw(`SELECT markdown_status FROM documents WHERE id = ?`, documentID).Scan(&status).Error; err != nil {
			t.Fatalf("load markdown status: %v", err)
		}
		return types.DocumentStageStatus(status)
	}

	inspector.stageErr = errors.New("redis timeout")
	housekeeping.recoverStaleStages(ctx, time.Now().UTC())
	if got := markdownStatus(); got != types.DocumentStageRunning {
		t.Fatalf("markdown status after probe error = %q, want running", got)
	}

	inspector.stageErr = nil
	inspector.stageQueued = true
	housekeeping.recoverStaleStages(ctx, time.Now().UTC())
	if got := markdownStatus(); got != types.DocumentStageRunning {
		t.Fatalf("markdown status with queued task = %q, want running", got)
	}

	inspector.stageQueued = false
	housekeeping.recoverStaleStages(ctx, time.Now().UTC())
	if got := markdownStatus(); got != types.DocumentStageFailed {
		t.Fatalf("markdown status without queued task = %q, want failed", got)
	}
}

// scriptedTaskInspector lets the housekeeping test drive probe outcomes.
type scriptedTaskInspector struct {
	stageQueued bool
	stageErr    error
}

func (*scriptedTaskInspector) CancelTasksForStage(context.Context, string, types.DocumentStage, int64) (int, int, error) {
	return 0, 0, nil
}

func (*scriptedTaskInspector) CancelTasksForDocument(context.Context, string) (int, int, error) {
	return 0, 0, nil
}

func (*scriptedTaskInspector) HasQueuedTasksForDocument(context.Context, string) (bool, error) {
	return false, nil
}

func (*scriptedTaskInspector) HasQueuedTaskTypeForDocument(context.Context, string, string) (bool, error) {
	return false, nil
}

func (i *scriptedTaskInspector) HasQueuedTaskForStage(context.Context, string, types.DocumentStage, int64) (bool, error) {
	return i.stageQueued, i.stageErr
}

func (*scriptedTaskInspector) HasQueuedQueryApplyForReference(context.Context, string, int64) (bool, error) {
	return false, nil
}

func nullableFinished(status string, at time.Time) interface{} {
	if status == "succeeded" {
		return at
	}
	return nil
}

func openServicePostgresTestDatabase(t *testing.T) (*gorm.DB, func()) {
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
	schema := "papers_manager_service_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		adminSQL.Close()
		t.Fatalf("create test schema: %v", err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		_ = admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error
		adminSQL.Close()
		t.Fatalf("parse PostgreSQL DSN: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := gorm.Open(postgres.Open(parsed.String()), &gorm.Config{})
	if err != nil {
		_ = admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error
		adminSQL.Close()
		t.Fatalf("open scoped PostgreSQL database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get scoped PostgreSQL sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(4)
	sqlDB.SetMaxIdleConns(4)
	if err := repository.Migrate(context.Background(), db); err != nil {
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
