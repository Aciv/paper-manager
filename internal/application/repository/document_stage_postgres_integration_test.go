package repository

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

func TestDocumentStageRepositoryPostgres(t *testing.T) {
	db, cleanup := openProcessingRunTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	documentID := uuid.New()
	if err := db.Exec(`INSERT INTO documents
		(id, original_filename, content_type, size_bytes, storage_key, status, updated_at)
		VALUES (?, 'stages.pdf', 'application/pdf', 1, ?, 'uploaded', ?)`,
		documentID, "stages/"+documentID.String(), time.Now().UTC().Add(-time.Hour)).Error; err != nil {
		t.Fatalf("insert document: %v", err)
	}

	repo := NewDocumentRepository(db)
	created, err := repo.Create(ctx, types.Document{
		ID: uuid.New(), OriginalFilename: "created.pdf", ContentType: "application/pdf",
		SizeBytes: 1, StorageKey: "stages/created", Status: types.DocumentStatusUploaded,
	})
	if err != nil {
		t.Fatalf("Create(with stage defaults): %v", err)
	}
	if created.MarkdownStatus != types.DocumentStageNotStarted || created.MarkdownGeneration != 0 ||
		created.MetadataExtractionStatus != types.DocumentStageNotStarted || created.SourceLookupStatus != types.DocumentStageNotStarted ||
		created.ReferenceLookupStatus != types.DocumentStageNotStarted || created.MarkdownUpdatedAt.IsZero() ||
		created.MetadataExtractionUpdatedAt.IsZero() || created.SourceLookupUpdatedAt.IsZero() || created.ReferenceLookupUpdatedAt.IsZero() {
		t.Fatalf("created document stage defaults = %#v", created)
	}
	initialDocument, err := repo.Get(ctx, documentID)
	if err != nil {
		t.Fatalf("Get(initial): %v", err)
	}
	for _, stage := range []types.DocumentStage{
		types.DocumentStageMarkdown,
		types.DocumentStageMetadataExtraction,
		types.DocumentStageSourceLookup,
		types.DocumentStageReferenceLookup,
	} {
		state, err := repo.GetStage(ctx, documentID, stage)
		if err != nil || state.Status != types.DocumentStageNotStarted || state.Generation != 0 {
			t.Fatalf("initial %s state = %#v, error %v", stage, state, err)
		}
	}

	type queueResult struct {
		state   types.DocumentStageState
		applied bool
		err     error
	}
	gate := make(chan struct{})
	results := make(chan queueResult, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-gate
			state, applied, err := repo.QueueStageNewGeneration(ctx, documentID, types.DocumentStageMarkdown)
			results <- queueResult{state, applied, err}
		}()
	}
	close(gate)
	wait.Wait()
	close(results)
	appliedCount := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent QueueStageNewGeneration: %v", result.err)
		}
		if result.applied {
			appliedCount++
		}
		if result.state.Status != types.DocumentStageQueued || result.state.Generation != 1 {
			t.Fatalf("queued state = %#v", result.state)
		}
	}
	if appliedCount != 1 {
		t.Fatalf("concurrent queue applied count = %d, want 1", appliedCount)
	}

	started, applied, err := repo.TryStartStage(ctx, documentID, types.DocumentStageMarkdown, 1)
	if err != nil || !applied || started.Status != types.DocumentStageRunning {
		t.Fatalf("TryStartStage = %#v, applied %t, error %v", started, applied, err)
	}
	if allowed, err := repo.AllowsStageWork(ctx, documentID, types.DocumentStageMarkdown, 1); err != nil || !allowed {
		t.Fatalf("AllowsStageWork(running) = %t, error %v", allowed, err)
	}
	cancelled, applied, err := repo.CancelStage(ctx, documentID, types.DocumentStageMarkdown, 1)
	if err != nil || !applied || cancelled.Status != types.DocumentStageCancelled {
		t.Fatalf("CancelStage = %#v, applied %t, error %v", cancelled, applied, err)
	}
	if _, applied, err := repo.TryStartStage(ctx, documentID, types.DocumentStageMarkdown, 1); err != nil || applied {
		t.Fatalf("TryStartStage(after cancel) applied=%t error=%v", applied, err)
	}
	if _, applied, err := repo.FinishStage(ctx, documentID, types.DocumentStageMarkdown, 1, types.DocumentStageSucceeded, "", ""); err != nil || applied {
		t.Fatalf("FinishStage(after cancel) applied=%t error=%v", applied, err)
	}
	if allowed, err := repo.AllowsStageWork(ctx, documentID, types.DocumentStageMarkdown, 1); err != nil || allowed {
		t.Fatalf("AllowsStageWork(cancelled) = %t, error %v", allowed, err)
	}

	retry, applied, err := repo.QueueStageNewGeneration(ctx, documentID, types.DocumentStageMarkdown)
	if err != nil || !applied || retry.Generation != 2 || retry.Status != types.DocumentStageQueued {
		t.Fatalf("QueueStageNewGeneration(retry) = %#v, applied %t, error %v", retry, applied, err)
	}
	if _, applied, err := repo.TryStartStage(ctx, documentID, types.DocumentStageMarkdown, 1); err != nil || applied {
		t.Fatalf("old generation start applied=%t error=%v", applied, err)
	}
	if _, applied, err := repo.TryStartStage(ctx, documentID, types.DocumentStageMarkdown, 2); err != nil || !applied {
		t.Fatalf("new generation start applied=%t error=%v", applied, err)
	}
	if _, applied, err := repo.MarkStageRetryQueued(ctx, documentID, types.DocumentStageMarkdown, 2); err != nil || !applied {
		t.Fatalf("MarkStageRetryQueued applied=%t error=%v", applied, err)
	}
	if _, applied, err := repo.TryStartStage(ctx, documentID, types.DocumentStageMarkdown, 2); err != nil || !applied {
		t.Fatalf("start retry applied=%t error=%v", applied, err)
	}
	finished, applied, err := repo.FinishStage(ctx, documentID, types.DocumentStageMarkdown, 2, types.DocumentStageSucceeded, "", "")
	if err != nil || !applied || finished.Status != types.DocumentStageSucceeded {
		t.Fatalf("FinishStage = %#v, applied %t, error %v", finished, applied, err)
	}

	metadata, applied, err := repo.QueueStageNewGeneration(ctx, documentID, types.DocumentStageMetadataExtraction)
	if err != nil || !applied || metadata.Generation != 1 {
		t.Fatalf("queue metadata = %#v, applied %t, error %v", metadata, applied, err)
	}
	markdown, err := repo.GetStage(ctx, documentID, types.DocumentStageMarkdown)
	if err != nil || markdown.Status != types.DocumentStageSucceeded || markdown.Generation != 2 {
		t.Fatalf("metadata transition changed markdown: %#v, error %v", markdown, err)
	}
	storedDocument, err := repo.Get(ctx, documentID)
	if err != nil {
		t.Fatalf("Get(after stages): %v", err)
	}
	if !storedDocument.UpdatedAt.Equal(initialDocument.UpdatedAt) {
		t.Fatalf("stage transitions changed lifecycle updated_at from %s to %s", initialDocument.UpdatedAt, storedDocument.UpdatedAt)
	}
	if !storedDocument.MetadataExtractionUpdatedAt.After(initialDocument.MetadataExtractionUpdatedAt) ||
		!storedDocument.MarkdownUpdatedAt.After(initialDocument.MarkdownUpdatedAt) {
		t.Fatal("stage transitions did not update independent timestamps")
	}
	if _, _, err := repo.FinishStage(ctx, documentID, types.DocumentStageMarkdown, 2, types.DocumentStageCancelled, "", ""); err == nil {
		t.Fatal("unsupported finish status should fail")
	}
	if _, err := repo.GetStage(ctx, documentID, types.DocumentStage("invalid")); err == nil {
		t.Fatal("invalid stage should fail")
	}

	reference, applied, err := repo.QueueStageNewGeneration(ctx, documentID, types.DocumentStageReferenceLookup)
	if err != nil || !applied {
		t.Fatalf("queue reference = %#v, applied %t, error %v", reference, applied, err)
	}
	type transitionResult struct {
		applied bool
		err     error
	}
	transitionGate := make(chan struct{})
	startResult, cancelResult := make(chan transitionResult, 1), make(chan transitionResult, 1)
	go func() {
		<-transitionGate
		_, applied, err := repo.TryStartStage(ctx, documentID, types.DocumentStageReferenceLookup, reference.Generation)
		startResult <- transitionResult{applied, err}
	}()
	go func() {
		<-transitionGate
		_, applied, err := repo.CancelStage(ctx, documentID, types.DocumentStageReferenceLookup, reference.Generation)
		cancelResult <- transitionResult{applied, err}
	}()
	close(transitionGate)
	startTransition, cancelTransition := <-startResult, <-cancelResult
	if startTransition.err != nil || cancelTransition.err != nil || !cancelTransition.applied {
		t.Fatalf("concurrent start=%#v cancel=%#v", startTransition, cancelTransition)
	}
	storedReference, err := repo.GetStage(ctx, documentID, types.DocumentStageReferenceLookup)
	if err != nil || storedReference.Status != types.DocumentStageCancelled {
		t.Fatalf("reference after start/cancel = %#v, error %v", storedReference, err)
	}
	if _, applied, err := repo.FinishStage(ctx, documentID, types.DocumentStageReferenceLookup,
		reference.Generation, types.DocumentStageSucceeded, "", ""); err != nil || applied {
		t.Fatalf("finish reference after cancellation applied=%t error=%v", applied, err)
	}

	markdownAttempt, applied, err := repo.QueueStageNewGeneration(ctx, documentID, types.DocumentStageMarkdown)
	if err != nil || !applied {
		t.Fatalf("queue Markdown commit race = %#v, applied %t, error %v", markdownAttempt, applied, err)
	}
	if _, applied, err := repo.TryStartStage(ctx, documentID, types.DocumentStageMarkdown, markdownAttempt.Generation); err != nil || !applied {
		t.Fatalf("start Markdown commit race applied=%t error=%v", applied, err)
	}
	if _, applied, err := repo.CancelStage(ctx, documentID, types.DocumentStageMarkdown, markdownAttempt.Generation); err != nil || !applied {
		t.Fatalf("cancel Markdown commit race applied=%t error=%v", applied, err)
	}
	err = repo.ReplaceParsedOutput(ctx, types.DocumentMarkdown{
		DocumentID: documentID, StorageKey: "markdown/rejected.md", Filename: "rejected.md",
		ContentType: "text/markdown", Metadata: types.JSON(`{}`),
	}, nil, markdownAttempt.Generation)
	if !errors.Is(err, types.ErrDocumentNotProcessing) {
		t.Fatalf("ReplaceParsedOutput(after cancellation) error = %v, want ErrDocumentNotProcessing", err)
	}
	var rejectedCount int64
	if err := db.Model(&types.DocumentMarkdown{}).Where("document_id = ? AND storage_key = ?", documentID, "markdown/rejected.md").Count(&rejectedCount).Error; err != nil {
		t.Fatal(err)
	}
	if rejectedCount != 0 {
		t.Fatalf("cancelled Markdown output rows = %d, want 0", rejectedCount)
	}

	sourceAttempt, applied, err := repo.QueueStageNewGeneration(ctx, documentID, types.DocumentStageSourceLookup)
	if err != nil || !applied {
		t.Fatalf("queue source error test = %#v, applied %t, error %v", sourceAttempt, applied, err)
	}
	if _, applied, err := repo.TryStartStage(ctx, documentID, types.DocumentStageSourceLookup, sourceAttempt.Generation); err != nil || !applied {
		t.Fatalf("start source error test applied=%t error=%v", applied, err)
	}
	failed, applied, err := repo.FinishStage(ctx, documentID, types.DocumentStageSourceLookup,
		sourceAttempt.Generation, types.DocumentStageFailed, "CATALOG_UNAVAILABLE", "catalog lookup is temporarily unavailable")
	if err != nil || !applied || failed.ErrorCode == nil || *failed.ErrorCode != "CATALOG_UNAVAILABLE" ||
		failed.ErrorMessage == nil || *failed.ErrorMessage != "catalog lookup is temporarily unavailable" {
		t.Fatalf("failed stage state = %#v, applied %t, error %v", failed, applied, err)
	}
	queuedAgain, applied, err := repo.QueueStageNewGeneration(ctx, documentID, types.DocumentStageSourceLookup)
	if err != nil || !applied || queuedAgain.ErrorCode != nil || queuedAgain.ErrorMessage != nil {
		t.Fatalf("new generation did not clear stage error: %#v, applied %t, error %v", queuedAgain, applied, err)
	}
}
