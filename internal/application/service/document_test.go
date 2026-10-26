package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	papersv1 "paperMaster/gen/papers/v1"
	"paperMaster/internal/application/repository"
	fileservice "paperMaster/internal/application/service/file"
	"paperMaster/internal/config"
	"paperMaster/internal/infrastructure/docrecognition"
	"paperMaster/internal/infrastructure/paperurl"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"
	"paperMaster/internal/utils"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Consolidated from document_test.go.
// ---------------------------------------------------------------------------

type documentTaskInspectorStub struct {
	calledWith string
	stage      types.DocumentStage
	generation int64
	err        error
}

func (s *documentTaskInspectorStub) CancelTasksForStage(_ context.Context, documentID string, stage types.DocumentStage, generation int64) (int, int, error) {
	s.calledWith, s.stage, s.generation = documentID, stage, generation
	return 1, 0, s.err
}

func (s *documentTaskInspectorStub) CancelTasksForDocument(_ context.Context, documentID string) (int, int, error) {
	s.calledWith = documentID
	return 1, 0, nil
}

func (*documentTaskInspectorStub) HasQueuedTasksForDocument(context.Context, string) (bool, error) {
	return false, nil
}

func (*documentTaskInspectorStub) HasQueuedTaskTypeForDocument(
	context.Context,
	string,
	string,
) (bool, error) {
	return false, nil
}

func (*documentTaskInspectorStub) HasQueuedTaskForStage(context.Context, string, types.DocumentStage, int64) (bool, error) {
	return false, nil
}

func (*documentTaskInspectorStub) HasQueuedQueryApplyForReference(context.Context, string, int64) (bool, error) {
	return false, nil
}

func (*documentTaskInspectorStub) SnapshotQueuedWork(context.Context) (types.QueuedWorkSnapshot, error) {
	return types.NewQueuedWorkSnapshot(), nil
}

type documentStatusRepoStub struct {
	document types.Document
	markdown types.DocumentMarkdown
	updated  bool
	deleted  bool
	blocks   []types.Block
}

func (*documentStatusRepoStub) CitationTargetProgress(context.Context, uuid.UUID) (types.PaperReferenceProgress, error) {
	return types.PaperReferenceProgress{}, nil
}

func (*documentStatusRepoStub) FindAvailableByDOIs(
	context.Context, []string, uuid.UUID,
) (map[string]uuid.UUID, error) {
	return map[string]uuid.UUID{}, nil
}

func (s *documentStatusRepoStub) GetMany(_ context.Context, ids []uuid.UUID) ([]types.Document, error) {
	documents := make([]types.Document, 0, len(ids))
	for _, id := range ids {
		if id == s.document.ID {
			documents = append(documents, s.document)
		}
	}
	return documents, nil
}

func (s *documentStatusRepoStub) CitationTargetProgressBatch(
	_ context.Context, ids []uuid.UUID,
) (map[uuid.UUID]types.PaperReferenceProgress, error) {
	return make(map[uuid.UUID]types.PaperReferenceProgress, len(ids)), nil
}

func (s *documentStatusRepoStub) GetStage(_ context.Context, _ uuid.UUID, stage types.DocumentStage) (types.DocumentStageState, error) {
	state, err := s.document.StageState(stage)
	if err == nil && state.Status == "" {
		state.Status = types.DocumentStageNotStarted
	}
	return state, err
}

func (s *documentStatusRepoStub) QueueStageNewGeneration(_ context.Context, _ uuid.UUID, stage types.DocumentStage) (types.DocumentStageState, bool, error) {
	return queueStubDocumentStage(&s.document, stage)
}

func (s *documentStatusRepoStub) TryStartStage(_ context.Context, _ uuid.UUID, stage types.DocumentStage, generation int64) (types.DocumentStageState, bool, error) {
	state, _ := s.document.StageState(stage)
	if state.Status == "" {
		setStubDocumentStage(&s.document, types.DocumentStageState{Stage: stage, Status: types.DocumentStageQueued, Generation: generation})
	}
	return transitionStubDocumentStage(&s.document, stage, generation, []types.DocumentStageStatus{types.DocumentStageQueued}, types.DocumentStageRunning)
}

func (s *documentStatusRepoStub) MarkStageRetryQueued(_ context.Context, _ uuid.UUID, stage types.DocumentStage, generation int64) (types.DocumentStageState, bool, error) {
	return transitionStubDocumentStage(&s.document, stage, generation, []types.DocumentStageStatus{types.DocumentStageRunning}, types.DocumentStageQueued)
}

func (s *documentStatusRepoStub) FinishStage(_ context.Context, _ uuid.UUID, stage types.DocumentStage, generation int64, status types.DocumentStageStatus, errorCode, errorMessage string) (types.DocumentStageState, bool, error) {
	state, applied, err := transitionStubDocumentStage(&s.document, stage, generation, []types.DocumentStageStatus{types.DocumentStageQueued, types.DocumentStageRunning}, status)
	if applied && stage == types.DocumentStageMarkdown {
		if errorCode != "" {
			s.document.MarkdownErrorCode = &errorCode
		}
		if errorMessage != "" {
			s.document.MarkdownErrorMessage = &errorMessage
		}
	}
	return state, applied, err
}

func (s *documentStatusRepoStub) CancelStage(_ context.Context, _ uuid.UUID, stage types.DocumentStage, generation int64) (types.DocumentStageState, bool, error) {
	return transitionStubDocumentStage(&s.document, stage, generation, []types.DocumentStageStatus{types.DocumentStageQueued, types.DocumentStageRunning}, types.DocumentStageCancelled)
}

func (s *documentStatusRepoStub) AllowsStageWork(_ context.Context, _ uuid.UUID, stage types.DocumentStage, generation int64) (bool, error) {
	state, err := s.document.StageState(stage)
	return err == nil && state.Generation == generation && state.Status == types.DocumentStageRunning, err
}

// TestDocumentSelectionProcessorReloadsOwnedBlock verifies that a block
// selection is reloaded from the owning document and returns that stored block ID.
func TestDocumentSelectionProcessorReloadsOwnedBlock(t *testing.T) {
	documentID, blockID := uuid.New(), uuid.New()
	repository := &documentStatusRepoStub{
		document: types.Document{ID: documentID},
		blocks:   []types.Block{{ID: blockID, DocumentID: documentID}},
	}
	processor := NewDocumentSelectionProcessor(repository)
	result, err := processor.ProcessSelection(context.Background(), types.DocumentSelectionInput{
		DocumentID: documentID,
		Kind:       types.DocumentSelectionBlock,
		BlockID:    blockID,
	})
	if err != nil {
		t.Fatalf("process owned block: %v", err)
	}
	if result.BlockID != blockID.String() {
		t.Fatalf("block id = %q, want %q", result.BlockID, blockID)
	}
}

// TestDocumentSelectionProcessorHonorsCancelledContext verifies that a canceled
// context aborts selection processing before any work and is surfaced as context.Canceled.
func TestDocumentSelectionProcessorHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	processor := NewDocumentSelectionProcessor(&documentStatusRepoStub{})
	_, err := processor.ProcessSelection(ctx, types.DocumentSelectionInput{
		DocumentID: uuid.New(),
		Kind:       types.DocumentSelectionText,
		Text:       "selected text",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func (s *documentStatusRepoStub) Create(_ context.Context, document types.Document) (types.Document, error) {
	document = normalizeTestDocumentSource(document)
	s.document = document
	return document, nil
}

func (s *documentStatusRepoStub) Get(_ context.Context, id uuid.UUID) (types.Document, error) {
	if s.document.ID != id {
		return types.Document{}, nil
	}
	s.document = normalizeTestDocumentSource(s.document)
	return s.document, nil
}

func (s *documentStatusRepoStub) Search(context.Context, types.DocumentSearch) (types.DocumentPage, error) {
	return types.DocumentPage{}, nil
}

func (s *documentStatusRepoStub) GetMarkdown(_ context.Context, id uuid.UUID) (types.DocumentMarkdown, error) {
	if s.document.ID != id || s.markdown.StorageKey == "" {
		return types.DocumentMarkdown{}, sql.ErrNoRows
	}
	return s.markdown, nil
}

func (s *documentStatusRepoStub) ReplaceParsedOutput(_ context.Context, markdown types.DocumentMarkdown, blocks []types.Block, _ int64) error {
	s.document = normalizeTestDocumentSource(s.document)
	if s.document.ID != markdown.DocumentID || s.document.SourceState != types.DocumentSourceAvailable || s.document.MarkdownStatus != types.DocumentStageRunning {
		return types.ErrDocumentNotProcessing
	}
	s.markdown = markdown
	s.blocks = append([]types.Block(nil), blocks...)
	s.document.SourceState = types.DocumentSourceAvailable
	setStubDocumentStage(&s.document, types.DocumentStageState{Stage: types.DocumentStageMarkdown, Status: types.DocumentStageSucceeded, Generation: s.document.MarkdownGeneration})
	return nil
}

func (s *documentStatusRepoStub) ListBlocks(_ context.Context, id uuid.UUID, pageNumber int) ([]types.Block, error) {
	if s.document.ID != id {
		return nil, sql.ErrNoRows
	}
	blocks := make([]types.Block, 0, len(s.blocks))
	for _, block := range s.blocks {
		if pageNumber == 0 || (block.Location != nil && block.Location.PageNumber != nil && *block.Location.PageNumber == pageNumber) {
			blocks = append(blocks, block)
		}
	}
	return blocks, nil
}

func (s *documentStatusRepoStub) GetBlock(_ context.Context, documentID, blockID uuid.UUID) (types.Block, error) {
	if s.document.ID != documentID {
		return types.Block{}, sql.ErrNoRows
	}
	for _, block := range s.blocks {
		if block.ID == blockID {
			return block, nil
		}
	}
	return types.Block{}, sql.ErrNoRows
}

func (s *documentStatusRepoStub) Delete(_ context.Context, id uuid.UUID) error {
	if s.document.ID != id {
		return sql.ErrNoRows
	}
	s.deleted = true
	return nil
}

func (s *documentStatusRepoStub) MarkDeleting(_ context.Context, id uuid.UUID) (types.DocumentSourceState, bool, error) {
	if s.document.ID != id {
		return "", false, sql.ErrNoRows
	}
	previous := s.document.SourceState
	if previous == types.DocumentSourceDeleting || previous == types.DocumentSourceMerged {
		return previous, false, nil
	}
	s.document.SourceState = types.DocumentSourceDeleting
	return previous, true, nil
}

func (s *documentStatusRepoStub) RestoreSourceState(_ context.Context, id uuid.UUID, state types.DocumentSourceState) error {
	if s.document.ID == id && s.document.SourceState == types.DocumentSourceDeleting {
		s.document.SourceState = state
	}
	return nil
}

func (s *documentStatusRepoStub) UpdateDocumentInfo(_ context.Context, id uuid.UUID, originalFilename, doi *string) (types.Document, error) {
	if s.document.ID != id {
		return types.Document{}, nil
	}
	if originalFilename != nil {
		s.document.OriginalFilename = *originalFilename
	}
	if doi != nil {
		s.document.DOI = *doi
	}
	return s.document, nil
}

func (s *documentStatusRepoStub) SetDOIIfEmpty(_ context.Context, id uuid.UUID, doi string) (bool, error) {
	if s.document.ID != id || s.document.DOI != "" {
		return false, nil
	}
	s.document.DOI = doi
	return true, nil
}

func (s *documentStatusRepoStub) ReplaceOlderDocument(
	_ context.Context, olderID, _ uuid.UUID,
) (types.DocumentFileKeys, error) {
	if s.document.ID == olderID {
		s.document = types.Document{}
	}
	return types.DocumentFileKeys{}, nil
}

// TestCancelDocumentParseFencesMarkdownGenerationAndDequeuesExactTask verifies
// that cancelling a queued parse marks the markdown stage cancelled and asks the
// inspector to cancel only that document, stage, and generation.
func TestCancelDocumentParseFencesMarkdownGenerationAndDequeuesExactTask(t *testing.T) {
	repo := &documentStatusRepoStub{document: types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable, MarkdownStatus: types.DocumentStageQueued, MarkdownGeneration: 7}}
	inspector := &documentTaskInspectorStub{}
	svc := NewDocumentServiceWithTaskInspector(repo, nil, inspector)

	document, err := svc.CancelDocumentParse(context.Background(), repo.document.ID.String())
	if err != nil {
		t.Fatalf("CancelDocumentParse() error = %v", err)
	}
	if document.MarkdownStatus != types.DocumentStageCancelled || repo.document.MarkdownStatus != types.DocumentStageCancelled {
		t.Fatalf("markdown status = %q, want cancelled", document.MarkdownStatus)
	}
	if inspector.calledWith != repo.document.ID.String() {
		t.Fatalf("inspector called with %q, want %q", inspector.calledWith, repo.document.ID)
	}
	if inspector.stage != types.DocumentStageMarkdown || inspector.generation != 7 {
		t.Fatalf("inspector envelope = %s/%d", inspector.stage, inspector.generation)
	}
}

// TestCancelDocumentParseRejectsFinishedDocument verifies that a completed
// markdown stage cannot be cancelled and that no repository mutation occurs.
func TestCancelDocumentParseRejectsFinishedDocument(t *testing.T) {
	repo := &documentStatusRepoStub{document: types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable, MarkdownStatus: types.DocumentStageSucceeded, MarkdownGeneration: 1}}
	svc := NewDocumentService(repo, nil)

	_, err := svc.CancelDocumentParse(context.Background(), repo.document.ID.String())
	if !errors.Is(err, ErrDocumentParseFinished) {
		t.Fatalf("CancelDocumentParse() error = %v, want ErrDocumentParseFinished", err)
	}
	if repo.updated {
		t.Fatal("finished document was unexpectedly updated")
	}
}

// TestCancelDocumentParseKeepsFenceWhenInspectorFails verifies that a queue
// inspection error does not roll back the cancellation fence, so a late worker
// for the old generation is still rejected.
func TestCancelDocumentParseKeepsFenceWhenInspectorFails(t *testing.T) {
	repo := &documentStatusRepoStub{document: types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable, MarkdownStatus: types.DocumentStageRunning, MarkdownGeneration: 3}}
	svc := NewDocumentServiceWithTaskInspector(repo, nil, &documentTaskInspectorStub{err: errors.New("redis unavailable")})
	document, err := svc.CancelDocumentParse(context.Background(), repo.document.ID.String())
	if err != nil {
		t.Fatalf("CancelDocumentParse() error = %v", err)
	}
	if document.MarkdownStatus != types.DocumentStageCancelled || repo.document.MarkdownStatus != types.DocumentStageCancelled {
		t.Fatalf("cancel fence was not retained: %#v", repo.document)
	}
	allowed, err := repo.AllowsStageWork(context.Background(), repo.document.ID, types.DocumentStageMarkdown, 3)
	if err != nil || allowed {
		t.Fatalf("late worker allowed=%t error=%v, want rejected", allowed, err)
	}
}

// TestRetryDocumentParseQueuesNewAttemptAfterFailure verifies that retrying a
// failed parse increments the markdown generation and enqueues one recognition
// task carrying the new generation.
func TestRetryDocumentParseQueuesNewAttemptAfterFailure(t *testing.T) {
	repo := &documentStatusRepoStub{document: types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable, MarkdownStatus: types.DocumentStageFailed, MarkdownGeneration: 1}}
	tasks := &documentTaskEnqueuerStub{}
	svc := &documentService{repo: repo, taskEnqueuer: tasks}

	document, err := svc.RetryDocumentParse(context.Background(), repo.document.ID.String())
	if err != nil {
		t.Fatalf("RetryDocumentParse() error = %v", err)
	}
	if document.MarkdownStatus != types.DocumentStageQueued || repo.document.MarkdownStatus != types.DocumentStageQueued {
		t.Fatalf("retry Markdown status = %q, want queued", document.MarkdownStatus)
	}
	if len(tasks.documents) != 1 || tasks.documents[0].Generation != 2 {
		t.Fatalf("recognition retry task = %#v, want generation 2", tasks.documents)
	}
}

// TestRetryDocumentProcessMarksMarkdownFailedWhenQueueFails verifies that a
// retry enqueue failure leaves the markdown stage failed with a safe error
// instead of reporting it as queued.
func TestRetryDocumentProcessMarksMarkdownFailedWhenQueueFails(t *testing.T) {
	repo := &documentStatusRepoStub{document: types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable, MarkdownStatus: types.DocumentStageFailed, MarkdownGeneration: 1}}
	svc := &documentService{
		repo:         repo,
		taskEnqueuer: &documentTaskEnqueuerStub{enqueueErr: errors.New("redis unavailable")},
	}

	document, err := svc.RetryDocumentProcess(context.Background(), repo.document.ID.String())
	if !errors.Is(err, ErrDocumentProcessQueue) {
		t.Fatalf("RetryDocumentProcess() error = %v, want ErrDocumentProcessQueue", err)
	}
	if document == nil || document.MarkdownStatus != types.DocumentStageFailed || repo.document.MarkdownStatus != types.DocumentStageFailed {
		t.Fatalf("retry must restore failed markdown stage: document=%#v repo=%#v", document, repo.document)
	}
	if document.MarkdownErrorMessage == nil || *document.MarkdownErrorMessage != "processing task could not be queued" {
		t.Fatalf("retry queue failure message = %#v", document.MarkdownErrorMessage)
	}
}

// TestCreateDocumentFromFileReturnsCreatedDocumentWhenInitialQueueFails verifies
// that a queue failure after upload still returns the persisted document marked
// failed, with its source retained for a later retry.
func TestCreateDocumentFromFileReturnsCreatedDocumentWhenInitialQueueFails(t *testing.T) {
	repo := &documentStatusRepoStub{}
	storage, err := fileservice.NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	svc := &documentService{
		repo:         repo,
		storage:      storage,
		taskEnqueuer: &documentTaskEnqueuerStub{enqueueErr: errors.New("redis unavailable")},
	}

	created, err := svc.CreateDocumentFromFile(context.Background(), documentUploadFileHeader(t, "queued.txt", []byte("queued content")), "")
	if !errors.Is(err, ErrDocumentProcessQueue) {
		t.Fatalf("CreateDocumentFromFile() error = %v, want ErrDocumentProcessQueue", err)
	}
	if created == nil || created.ID == uuid.Nil {
		t.Fatalf("created document = %#v, want durable ID", created)
	}
	if created.MarkdownStatus != types.DocumentStageFailed || repo.document.MarkdownStatus != types.DocumentStageFailed {
		t.Fatalf("markdown status = created:%q repo:%q, want failed", created.MarkdownStatus, repo.document.MarkdownStatus)
	}
	if created.MarkdownErrorMessage == nil || *created.MarkdownErrorMessage != "processing task could not be queued" {
		t.Fatalf("safe queue failure message = %#v", created.MarkdownErrorMessage)
	}
	reader, openErr := storage.Open(context.Background(), created.StorageKey)
	if openErr != nil {
		t.Fatalf("stored document must remain retryable: %v", openErr)
	}
	_ = reader.Close()
}

// TestCreateDocumentFromLocalFileStoresAndQueues verifies that importing a
// local regular file persists its bytes, records the supplied DOI, and queues
// markdown recognition.
func TestCreateDocumentFromLocalFileStoresAndQueues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local-paper.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.7\nlocal"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := &documentStatusRepoStub{}
	storage, err := fileservice.NewLocalStorage(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	svc := &documentService{
		repo: repo, storage: storage, taskEnqueuer: &documentTaskEnqueuerStub{},
	}
	created, err := svc.CreateDocumentFromLocalFile(context.Background(), path, "10.1000/local")
	if err != nil {
		t.Fatalf("CreateDocumentFromLocalFile() error = %v", err)
	}
	if created == nil || created.ID == uuid.Nil || created.OriginalFilename != "local-paper.pdf" {
		t.Fatalf("created document = %#v", created)
	}
	if created.DOI != "10.1000/local" {
		t.Fatalf("created DOI = %q", created.DOI)
	}
	if created.MarkdownStatus != types.DocumentStageQueued {
		t.Fatalf("markdown status = %q, want queued", created.MarkdownStatus)
	}
	reader, openErr := storage.Open(context.Background(), created.StorageKey)
	if openErr != nil {
		t.Fatalf("stored local file is missing: %v", openErr)
	}
	_ = reader.Close()
}

// TestCreateDocumentFromLocalFileRejectsNonRegularFile verifies that local
// import rejects a directory path rather than attempting to store it.
func TestCreateDocumentFromLocalFileRejectsNonRegularFile(t *testing.T) {
	svc := &documentService{}
	if _, err := svc.CreateDocumentFromLocalFile(context.Background(), t.TempDir(), ""); err == nil {
		t.Fatal("directory import was accepted")
	}
}

// TestCreateDocumentsFromLocalFilesProcessesEachItem verifies that a batch local
// import processes every file independently and keeps each optional DOI aligned
// with its corresponding item.
func TestCreateDocumentsFromLocalFilesProcessesEachItem(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.pdf")
	second := filepath.Join(dir, "second.pdf")
	if err := os.WriteFile(first, []byte("%PDF-1.7\nfirst"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("%PDF-1.7\nsecond"), 0o600); err != nil {
		t.Fatal(err)
	}
	storage, err := fileservice.NewLocalStorage(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	svc := &documentService{
		repo: &documentStatusRepoStub{}, storage: storage,
		taskEnqueuer: &documentTaskEnqueuerStub{},
	}
	result, err := svc.CreateDocumentsFromLocalFiles(
		context.Background(), []string{first, second}, []string{"10.1000/first", ""},
	)
	if err != nil {
		t.Fatalf("CreateDocumentsFromLocalFiles() error = %v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("items = %#v", result.Items)
	}
	for index, item := range result.Items {
		if item.Status != types.DocumentImportItemAccepted || item.Document == nil {
			t.Fatalf("item %d = %#v", index, item)
		}
	}
	if result.Items[0].Document.DOI != "10.1000/first" ||
		result.Items[1].Document.DOI != "" {
		t.Fatalf("DOIs = %q, %q", result.Items[0].Document.DOI, result.Items[1].Document.DOI)
	}
}

// TestCreateDocumentsFromLocalFilesEnforcesBatchLimits verifies that batch local
// import rejects oversized batches and DOI lists that do not match the file count.
func TestCreateDocumentsFromLocalFilesEnforcesBatchLimits(t *testing.T) {
	svc := &documentService{}
	paths := make([]string, types.MaxDocumentBatchItems+1)
	for index := range paths {
		paths[index] = filepath.Join(t.TempDir(), "paper.pdf")
	}
	if _, err := svc.CreateDocumentsFromLocalFiles(context.Background(), paths, nil); err == nil {
		t.Fatal("oversized batch was accepted")
	}
	if _, err := svc.CreateDocumentsFromLocalFiles(
		context.Background(), []string{"one.pdf", "two.pdf"}, []string{"10.1000/one"},
	); err == nil {
		t.Fatal("misaligned DOI batch was accepted")
	}
}

func documentUploadFileHeader(t *testing.T, filename string, content []byte) *multipart.FileHeader {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/upload", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if err := request.ParseMultipartForm(int64(body.Len()) + 1024); err != nil {
		t.Fatal(err)
	}
	return request.MultipartForm.File["file"][0]
}

// TestRetryDocumentParseRejectsActiveDocument verifies that a document whose
// markdown stage never ran is not retryable and returns ErrDocumentParseFinished.
func TestRetryDocumentParseRejectsActiveDocument(t *testing.T) {
	repo := &documentStatusRepoStub{document: types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}}
	svc := &documentService{repo: repo, taskEnqueuer: &documentTaskEnqueuerStub{}}

	_, err := svc.RetryDocumentParse(context.Background(), repo.document.ID.String())
	if !errors.Is(err, ErrDocumentParseFinished) {
		t.Fatalf("RetryDocumentParse() error = %v, want ErrDocumentParseFinished", err)
	}
}

// TestDeleteDocumentDeletesSourceAndMarkdownFiles verifies that deleting an
// available document removes both its stored source and parsed Markdown objects.
func TestDeleteDocumentDeletesSourceAndMarkdownFiles(t *testing.T) {
	storage, err := fileservice.NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatalf("NewLocalStorage() error = %v", err)
	}
	source, err := storage.SaveBytes(context.Background(), "source.txt", []byte("source"))
	if err != nil {
		t.Fatalf("save source: %v", err)
	}
	markdown, err := storage.SaveBytes(context.Background(), "source.md", []byte("# parsed"))
	if err != nil {
		t.Fatalf("save markdown: %v", err)
	}

	documentID := uuid.New()
	repo := &documentStatusRepoStub{
		document: types.Document{ID: documentID, StorageKey: source.Key, SourceState: types.DocumentSourceAvailable},
		markdown: types.DocumentMarkdown{DocumentID: documentID, StorageKey: markdown.Key},
	}
	svc := NewDocumentService(repo, storage)
	if err := svc.DeleteDocument(context.Background(), documentID.String()); err != nil {
		t.Fatalf("DeleteDocument() error = %v", err)
	}
	if !repo.deleted {
		t.Fatal("document repository Delete was not called")
	}
	for _, key := range []string{source.Key, markdown.Key} {
		if _, err := storage.Open(context.Background(), key); err == nil {
			t.Fatalf("stored object %q still exists after deletion", key)
		}
	}
}

// TestCreateDocumentFromFileRejectsBeforeHashingOrLookup verifies that unsupported
// file types and oversized uploads are rejected before hashing or duplicate
// lookup runs.
func TestCreateDocumentFromFileRejectsBeforeHashingOrLookup(t *testing.T) {
	repo := &documentBatchRepoStub{}
	service := &documentService{repo: repo, maxUploadSize: 8}

	_, err := service.CreateDocumentFromFile(context.Background(),
		documentUploadFileHeader(t, "malware.exe", []byte("payload")), "")
	if !errors.Is(err, fileservice.ErrUnsupportedFileType) {
		t.Fatalf("invalid extension error = %v, want ErrUnsupportedFileType", err)
	}
	if repo.searchCalls != 0 {
		t.Fatalf("duplicate lookup ran before extension rejection: %d", repo.searchCalls)
	}

	oversize := &multipart.FileHeader{Filename: "paper.pdf", Size: 9}
	_, err = service.CreateDocumentFromFile(context.Background(), oversize, "")
	if !errors.Is(err, fileservice.ErrFileTooLarge) {
		t.Fatalf("oversize error = %v, want ErrFileTooLarge", err)
	}
	if repo.searchCalls != 0 {
		t.Fatalf("duplicate lookup ran before size rejection: %d", repo.searchCalls)
	}
}

// TestCreateDocumentsFromURLsStopsAtRequestDeadline verifies that a canceled
// request stops a URL batch import and reports every item as failed without
// returning a separate error.
func TestCreateDocumentsFromURLsStopsAtRequestDeadline(t *testing.T) {
	service := &documentService{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := service.CreateDocumentsFromURLs(ctx, []types.DocumentURLImportCommand{
		{URL: "https://example.test/one.pdf"},
		{URL: "https://example.test/two.pdf"},
	})
	if err != nil {
		t.Fatalf("CreateDocumentsFromURLs() error = %v", err)
	}
	if !result.Stopped || len(result.Items) != 2 {
		t.Fatalf("result = %#v, want two stopped items", result)
	}
	for _, item := range result.Items {
		if item.Status != types.DocumentImportItemFailed {
			t.Fatalf("item = %#v, want failed", item)
		}
	}
}

// TestGetDocumentProjectionsBatchesMetadataAndProcessing verifies that a projection
// read deduplicates requested IDs and batch-loads metadata and processing state
// into a single item.
func TestGetDocumentProjectionsBatchesMetadataAndProcessing(t *testing.T) {
	documentID := uuid.New()
	repo := &documentStatusRepoStub{document: types.Document{
		ID: documentID, OriginalFilename: "paper.pdf",
		SourceState: types.DocumentSourceAvailable, MarkdownStatus: types.DocumentStageSucceeded,
	}}
	metadataRepo := newPaperMetadataRepoStub()
	metadataRepo.items[documentID] = types.PaperMetadata{
		DocumentID: documentID, Title: "Projected Paper",
		Authors: []types.PaperAuthor{}, Tags: []string{},
	}
	spanRepo := &spanRepositoryStub{rows: []types.DocumentProcessingSpan{{
		ID: uuid.New(), DocumentID: documentID, Kind: types.ProcessingSpanRecognition,
		Scope: types.ProcessingSpanScopeDocument, Status: types.ProcessingSpanSucceeded,
	}}}
	service := &documentService{
		repo:       repo,
		papers:     NewPaperService(repo, metadataRepo, newPaperCitationRepoStub()),
		processing: NewSpanTracker(spanRepo),
	}
	items, err := service.GetDocumentProjections(context.Background(), types.DocumentProjectionQuery{
		DocumentIDs:     []string{documentID.String(), documentID.String()},
		IncludeMetadata: true, IncludeProcessing: true,
	})
	if err != nil {
		t.Fatalf("GetDocumentProjections() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %#v, want one deduplicated projection", items)
	}
	if items[0].Metadata == nil || items[0].Metadata.Title != "Projected Paper" {
		t.Fatalf("metadata = %#v", items[0].Metadata)
	}
	if items[0].Processing == nil || len(items[0].Processing.Stages) != 1 ||
		items[0].Processing.ReferenceProgress == nil {
		t.Fatalf("processing = %#v", items[0].Processing)
	}
}

// TestNonAvailableDocumentHasNoSourceFile verifies that a document fenced for
// deletion cannot serve its source file even when a storage key is recorded.
func TestNonAvailableDocumentHasNoSourceFile(t *testing.T) {
	documentID := uuid.New()
	repo := &documentStatusRepoStub{
		document: types.Document{
			ID:          documentID,
			StorageKey:  "source/" + documentID.String(),
			SourceState: types.DocumentSourceDeleting,
		},
	}
	svc := NewDocumentReadingService(repo, nil, nil)

	_, reader, _, err := svc.OpenDocumentFile(context.Background(), documentID.String())
	if reader != nil {
		reader.Close()
		t.Fatal("OpenDocumentFile() returned a reader for a non-available document")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("OpenDocumentFile() error = %v, want sql.ErrNoRows", err)
	}
}

type recognitionStub struct {
	result *types.RecognitionResult
	err    error
	onRead func()
}

type documentTaskEnqueuerStub struct {
	documents   []types.DocumentProcessPayload
	extractions []types.PaperMetadataExtractPayload
	catalogs    []types.PaperSourceQueryEnqueuePayload
	tasks       []*asynq.Task
	extractErr  error
	catalogErr  error
	enqueueErr  error
	onEnqueue   func(*asynq.Task)
}

type documentPaperInfoStub struct {
	extract    []types.PaperMetadataExtractCommand
	source     []types.PaperSourceQueryEnqueueCommand
	references []types.PaperReferenceQueryEnqueueCommand
	parents    []uuid.UUID
}

func (stub *documentPaperInfoStub) ExtractMetadata(ctx context.Context, command types.PaperMetadataExtractCommand) (bool, error) {
	stub.extract = append(stub.extract, command)
	stub.parents = append(stub.parents, uuid.Nil)
	return true, nil
}
func (stub *documentPaperInfoStub) PaperSourceQueryEnqueue(ctx context.Context, command types.PaperSourceQueryEnqueueCommand) error {
	stub.source = append(stub.source, command)
	stub.parents = append(stub.parents, uuid.Nil)
	return nil
}
func (stub *documentPaperInfoStub) ReferenceQueryEnqueue(context.Context, types.PaperReferenceQueryEnqueueCommand) error {
	return nil
}
func (*documentPaperInfoStub) RetryExtraction(context.Context, string) error          { return nil }
func (*documentPaperInfoStub) RetryCatalogResolution(context.Context, string) error   { return nil }
func (*documentPaperInfoStub) RetryReferenceResolution(context.Context, string) error { return nil }
func (*documentPaperInfoStub) Cancel(context.Context, string) error                   { return nil }

func (stub *documentTaskEnqueuerStub) EnqueueContext(
	_ context.Context,
	task *asynq.Task,
	_ ...asynq.Option,
) (*asynq.TaskInfo, error) {
	stub.tasks = append(stub.tasks, task)
	if stub.onEnqueue != nil {
		stub.onEnqueue(task)
	}
	if stub.enqueueErr != nil {
		return nil, stub.enqueueErr
	}
	switch task.Type() {
	case types.TypeDocumentRecognize:
		var payload types.DocumentProcessPayload
		_ = json.Unmarshal(task.Payload(), &payload)
		stub.documents = append(stub.documents, payload)
	case types.TypePaperMetadataExtract:
		var payload types.PaperMetadataExtractPayload
		_ = json.Unmarshal(task.Payload(), &payload)
		stub.extractions = append(stub.extractions, payload)
		return nil, stub.extractErr
	case types.TypePaperSourceQueryEnqueue:
		var payload types.PaperSourceQueryEnqueuePayload
		_ = json.Unmarshal(task.Payload(), &payload)
		stub.catalogs = append(stub.catalogs, payload)
		return nil, stub.catalogErr
	}
	return nil, nil
}

func (s *recognitionStub) Read(context.Context, *types.RecognitionRequest) (*types.RecognitionResult, error) {
	if s.onRead != nil {
		s.onRead()
	}
	return s.result, s.err
}

// TestRecognizeDocumentRejectsCancelledStageBeforeExternalIO verifies that a
// cancelled markdown stage is revoked before the recognizer performs any
// external I/O.
func TestRecognizeDocumentRejectsCancelledStageBeforeExternalIO(t *testing.T) {
	documentID := uuid.New()
	repo := &documentStatusRepoStub{document: types.Document{
		ID: documentID, SourceState: types.DocumentSourceAvailable,
		MarkdownStatus: types.DocumentStageCancelled, MarkdownGeneration: 1,
	}}
	called := false
	svc := &documentService{repo: repo, recognizer: &recognitionStub{onRead: func() { called = true }}}
	payload, err := json.Marshal(types.DocumentProcessPayload{
		DocumentID: documentID.String(), Stage: types.DocumentStageMarkdown, Generation: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = svc.RecognizeDocument(context.Background(), asynq.NewTask(types.TypeDocumentRecognize, payload))
	if !errors.Is(err, asynq.RevokeTask) {
		t.Fatalf("RecognizeDocument() error = %v, want RevokeTask", err)
	}
	if called {
		t.Fatal("cancelled stage reached the recognizer")
	}
}

// TestRecognizeDocumentRetryKeepsGeneration verifies that a transient recognizer
// failure leaves the markdown stage queued at the same generation so Asynq can
// retry it.
func TestRecognizeDocumentRetryKeepsGeneration(t *testing.T) {
	storage, err := fileservice.NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	source, err := storage.Save(context.Background(), interfaces.FileUpload{
		Filename: "paper.pdf", Content: strings.NewReader("%PDF-1.7\nsource"), Category: interfaces.StorageCategoryPDF,
	})
	if err != nil {
		t.Fatal(err)
	}
	documentID := uuid.New()
	repo := &documentStatusRepoStub{document: types.Document{
		ID: documentID, StorageKey: source.Key, OriginalFilename: "paper.pdf",
		ContentType: "application/pdf", SizeBytes: source.Size, SourceState: types.DocumentSourceAvailable,
		MarkdownStatus: types.DocumentStageQueued, MarkdownGeneration: 4,
	}}
	svc := &documentService{
		repo: repo, storage: storage,
		recognizer: &recognitionStub{err: errors.New("temporary parser failure")},
	}
	payload, err := json.Marshal(types.DocumentProcessPayload{
		DocumentID: documentID.String(), Stage: types.DocumentStageMarkdown, Generation: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = svc.RecognizeDocument(context.Background(), asynq.NewTask(types.TypeDocumentRecognize, payload))
	if err == nil {
		t.Fatal("RecognizeDocument() error = nil")
	}
	if repo.document.MarkdownStatus != types.DocumentStageQueued || repo.document.MarkdownGeneration != 4 {
		t.Fatalf("retry stage = %s generation=%d, want queued generation=4",
			repo.document.MarkdownStatus, repo.document.MarkdownGeneration)
	}
}

// TestProcessDocumentPersistsMarkdownAndPDFBlocks verifies that successful PDF
// recognition stores Markdown and page blocks, records the generation, and
// hands off only to metadata extraction.
func TestProcessDocumentPersistsMarkdownAndPDFBlocks(t *testing.T) {
	storage, err := fileservice.NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatalf("NewLocalStorage() error = %v", err)
	}
	source, err := storage.SaveBytes(context.Background(), "paper.pdf", []byte("%PDF-1.7\nsource"))
	if err != nil {
		t.Fatalf("save source: %v", err)
	}
	documentID := uuid.New()
	page, start, end := 1, 0, 15
	width, height, x0, y0, x1, y1 := 612.0, 792.0, 72.0, 72.0, 280.0, 108.0
	repo := &documentStatusRepoStub{document: types.Document{
		ID: documentID, StorageKey: source.Key, OriginalFilename: "paper.pdf",
		ContentType: "application/pdf", SizeBytes: source.Size, DOI: "10.1000/test",
		SourceState: types.DocumentSourceAvailable,
	}}
	recognizer := &recognitionStub{result: &types.RecognitionResult{
		OutputMode: types.RecognitionOutputMarkdownBytes,
		Markdown:   []byte("# Parsed paper\n"),
		Metadata: types.RecognitionMetadata{
			MarkdownFilename: "paper.md", ContentType: "text/markdown", SizeBytes: 15,
			PageCount: 1, ParserName: "test",
		},
		Blocks: []types.RecognitionBlock{{
			Ordinal: 0, Type: "title", Text: "Parsed paper",
			Location: types.PDFBlockLocation{PageNumber: &page, PageWidth: &width, PageHeight: &height,
				BBoxX0: &x0, BBoxY0: &y0, BBoxX1: &x1, BBoxY1: &y1,
				MarkdownCodepointStart: &start, MarkdownCodepointEnd: &end},
		}},
	}}
	documentTasks := &documentTaskEnqueuerStub{}
	svc := &documentService{
		repo: repo, storage: storage, recognizer: recognizer,
		taskEnqueuer: documentTasks,
	}
	payload, err := json.Marshal(types.DocumentProcessPayload{DocumentID: documentID.String(), Stage: types.DocumentStageMarkdown, Generation: 1})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := svc.RecognizeDocument(context.Background(), asynq.NewTask(types.TypeDocumentRecognize, payload)); err != nil {
		t.Fatalf("ProcessDocument() error = %v", err)
	}
	if repo.document.SourceState != types.DocumentSourceAvailable {
		t.Fatalf("document source state = %q, want available", repo.document.SourceState)
	}
	if repo.markdown.StorageKey == "" || len(repo.blocks) != 1 {
		t.Fatalf("persisted output = markdown=%+v blocks=%+v", repo.markdown, repo.blocks)
	}
	outline, err := types.ParseDocumentOutline(repo.markdown.DocumentOutline)
	if err != nil || outline.Source != types.DocumentOutlineNone || repo.markdown.RecognitionGeneration != 1 {
		t.Fatalf("persisted outline=%#v generation=%d error=%v", outline, repo.markdown.RecognitionGeneration, err)
	}
	block := repo.blocks[0]
	if block.DocumentID != documentID || block.Location == nil || block.Location.PageNumber == nil || *block.Location.PageNumber != 1 {
		t.Fatalf("persisted block = %+v", block)
	}
	reader, err := storage.Open(context.Background(), repo.markdown.StorageKey)
	if err != nil {
		t.Fatalf("stored markdown is missing: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close stored file: %v", err)
	}
	if !strings.HasPrefix(repo.markdown.StorageKey, "markdown/") {
		t.Fatalf("markdown storage key = %q, want markdown/ prefix", repo.markdown.StorageKey)
	}
	if len(documentTasks.extractions) != 1 || len(documentTasks.catalogs) != 0 {
		t.Fatalf("recognition handoff = extractions:%#v catalogs:%#v", documentTasks.extractions, documentTasks.catalogs)
	}
}

// TestProcessDocumentStoresRenderedImageBlock verifies that recognition image
// crops are persisted as block assets with the document-scoped image URL.
func TestProcessDocumentStoresRenderedImageBlock(t *testing.T) {
	storage, err := fileservice.NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatalf("NewLocalStorage() error = %v", err)
	}
	source, err := storage.Save(context.Background(), interfaces.FileUpload{
		Filename: "paper.pdf", Content: strings.NewReader("%PDF-1.7\nsource"), Category: interfaces.StorageCategoryPDF,
	})
	if err != nil {
		t.Fatalf("save source: %v", err)
	}
	documentID := uuid.New()
	page, start, end := 1, 0, 0
	width, height, x0, y0, x1, y1 := 612.0, 792.0, 72.0, 72.0, 280.0, 180.0
	repo := &documentStatusRepoStub{document: types.Document{
		ID: documentID, StorageKey: source.Key, OriginalFilename: "paper.pdf", ContentType: "application/pdf",
		SizeBytes: source.Size, SourceState: types.DocumentSourceAvailable,
	}}
	svc := &documentService{repo: repo, storage: storage, recognizer: &recognitionStub{result: &types.RecognitionResult{
		OutputMode: types.RecognitionOutputMarkdownBytes, Markdown: []byte("# Parsed\n"),
		Metadata: types.RecognitionMetadata{MarkdownFilename: "paper.md", ContentType: "text/markdown"},
		Blocks: []types.RecognitionBlock{{
			Ordinal: 0, Type: "image", Text: "diagram", ImageData: []byte("PNG crop"), ImageContentType: "image/png",
			Location: types.PDFBlockLocation{PageNumber: &page, PageWidth: &width, PageHeight: &height,
				BBoxX0: &x0, BBoxY0: &y0, BBoxX1: &x1, BBoxY1: &y1,
				MarkdownCodepointStart: &start, MarkdownCodepointEnd: &end},
		}},
	}}}
	payload, err := json.Marshal(types.DocumentProcessPayload{DocumentID: documentID.String(), Stage: types.DocumentStageMarkdown, Generation: 1})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := svc.RecognizeDocument(context.Background(), asynq.NewTask(types.TypeDocumentRecognize, payload)); err != nil {
		t.Fatalf("ProcessDocument() error = %v", err)
	}
	if len(repo.blocks) != 1 || repo.blocks[0].AssetKey == "" || !strings.HasPrefix(repo.blocks[0].AssetKey, "image/") {
		t.Fatalf("image block was not persisted: %+v", repo.blocks)
	}
	if want := "/api/document/" + documentID.String() + "/blocks/" + repo.blocks[0].ID.String() + "/image"; repo.blocks[0].URL != want {
		t.Fatalf("block URL = %q, want %q", repo.blocks[0].URL, want)
	}
	reader, err := storage.Open(context.Background(), repo.blocks[0].AssetKey)
	if err != nil {
		t.Fatalf("stored image is missing: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close stored file: %v", err)
	}
}

// TestDeleteDocumentRemovesRenderedBlockImages verifies that document deletion
// cancels queued work and removes rendered block image assets.
func TestDeleteDocumentRemovesRenderedBlockImages(t *testing.T) {
	storage, err := fileservice.NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatalf("NewLocalStorage() error = %v", err)
	}
	source, err := storage.Save(context.Background(), interfaces.FileUpload{
		Filename: "paper.pdf", Content: strings.NewReader("%PDF-1.7\nsource"), Category: interfaces.StorageCategoryPDF,
	})
	if err != nil {
		t.Fatalf("save source: %v", err)
	}
	image, err := storage.Save(context.Background(), interfaces.FileUpload{
		Filename: "block.png", Content: strings.NewReader("PNG crop"), Category: interfaces.StorageCategoryImage,
	})
	if err != nil {
		t.Fatalf("save block image: %v", err)
	}
	documentID := uuid.New()
	repo := &documentStatusRepoStub{
		document: types.Document{ID: documentID, StorageKey: source.Key, SourceState: types.DocumentSourceAvailable},
		blocks:   []types.Block{{ID: uuid.New(), DocumentID: documentID, Type: "image", AssetKey: image.Key}},
	}
	inspector := &documentTaskInspectorStub{}
	svc := NewDocumentServiceWithTaskInspector(repo, storage, inspector)
	if err := svc.DeleteDocument(context.Background(), documentID.String()); err != nil {
		t.Fatalf("DeleteDocument() error = %v", err)
	}
	if inspector.calledWith != documentID.String() {
		t.Fatalf("queue cancellation document = %q, want %q", inspector.calledWith, documentID)
	}
	if _, err := storage.Open(context.Background(), image.Key); err == nil {
		t.Fatalf("rendered block image %q remains after document deletion", image.Key)
	}
}

// TestDeleteDocumentRemovesReadingNoteFile verifies that document deletion also
// removes its stored reading-note section files.
func TestDeleteDocumentRemovesReadingNoteFile(t *testing.T) {
	storage, err := fileservice.NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	source, err := storage.SaveBytes(context.Background(), "paper.pdf", []byte("%PDF-1.7\nsource"))
	if err != nil {
		t.Fatal(err)
	}
	documentID, sectionID := uuid.New(), uuid.New()
	noteFile, err := storage.ReplaceText(context.Background(), "notes/"+documentID.String()+"/"+sectionID.String()+".md", "# Note", 1024)
	if err != nil {
		t.Fatal(err)
	}
	repo := &documentStatusRepoStub{document: types.Document{ID: documentID, StorageKey: source.Key, SourceState: types.DocumentSourceAvailable}}
	note := types.DocumentReadingNote{DocumentID: documentID, SizeBytes: noteFile.Size, Revision: 1}
	if err := note.SetDirectory(types.ReadingNoteDirectory{SchemaVersion: 1, Sections: []types.ReadingNoteSectionEntry{{
		ID: sectionID, Order: 0, Level: 1, Title: "Note", StorageKey: noteFile.Key, ContentRevision: 1, SizeBytes: noteFile.Size,
	}}}); err != nil {
		t.Fatal(err)
	}
	notes := &memoryReadingNoteRepository{notes: map[uuid.UUID]types.DocumentReadingNote{documentID: note}}
	svc := NewDocumentServiceWithTaskInspector(repo, storage, nil).(*documentService)
	svc.readingNotes = notes
	if err := svc.DeleteDocument(context.Background(), documentID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.Open(context.Background(), noteFile.Key); err == nil {
		t.Fatalf("reading note file %q remains after document deletion", noteFile.Key)
	}
}

// TestProcessDocumentCopiesDirectoryRecognizerMarkdown verifies that directory-mode
// recognizer output is copied into Markdown storage while the original recognizer
// package remains untouched.
func TestProcessDocumentCopiesDirectoryRecognizerMarkdown(t *testing.T) {
	storage, err := fileservice.NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatalf("NewLocalStorage() error = %v", err)
	}
	source, err := storage.SaveBytes(context.Background(), "paper.pdf", []byte("%PDF-1.7\nsource"))
	if err != nil {
		t.Fatalf("save source: %v", err)
	}
	outputRoot := t.TempDir()
	packageDir := filepath.Join(outputRoot, "job-1")
	if err := os.Mkdir(packageDir, 0o750); err != nil {
		t.Fatalf("create recognizer package: %v", err)
	}
	markdown := []byte("# Saved by recognizer\n")
	if err := os.WriteFile(filepath.Join(packageDir, "paper.md"), markdown, 0o640); err != nil {
		t.Fatalf("write recognizer Markdown: %v", err)
	}

	documentID := uuid.New()
	repo := &documentStatusRepoStub{document: types.Document{
		ID: documentID, StorageKey: source.Key, OriginalFilename: "paper.pdf",
		ContentType: "application/pdf", SizeBytes: source.Size, SourceState: types.DocumentSourceAvailable,
	}}
	svc := &documentService{
		repo: repo, storage: storage, recognitionOutputDir: outputRoot, maxRecognitionOutputSize: 1024,
		recognizer: &recognitionStub{result: &types.RecognitionResult{
			OutputMode: types.RecognitionOutputDirectory, PackagePath: "job-1",
			Metadata: types.RecognitionMetadata{MarkdownFilename: "paper.md", ContentType: "text/markdown", SizeBytes: int64(len(markdown)), ParserName: "external"},
		}},
	}
	payload, err := json.Marshal(types.DocumentProcessPayload{DocumentID: documentID.String(), Stage: types.DocumentStageMarkdown, Generation: 1})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := svc.RecognizeDocument(context.Background(), asynq.NewTask(types.TypeDocumentRecognize, payload)); err != nil {
		t.Fatalf("ProcessDocument() error = %v", err)
	}
	if repo.document.SourceState != types.DocumentSourceAvailable {
		t.Fatalf("document source state = %q, want available", repo.document.SourceState)
	}
	stored, err := storage.Open(context.Background(), repo.markdown.StorageKey)
	if err != nil {
		t.Fatalf("open copied Markdown: %v", err)
	}
	defer stored.Close()
	got, err := os.ReadFile(filepath.Join(outputRoot, "job-1", "paper.md"))
	if err != nil {
		t.Fatalf("read original recognizer Markdown: %v", err)
	}
	if string(got) != string(markdown) {
		t.Fatalf("original Markdown = %q, want %q", got, markdown)
	}
	storedBytes, err := io.ReadAll(stored)
	if err != nil {
		t.Fatalf("read copied Markdown: %v", err)
	}
	if string(storedBytes) != string(markdown) {
		t.Fatalf("copied Markdown = %q, want %q", storedBytes, markdown)
	}
}

// TestProcessDocumentDiscardsOutputCancelledDuringRecognition verifies that output
// from recognition is discarded when the markdown stage was cancelled while the
// recognizer was running.
func TestProcessDocumentDiscardsOutputCancelledDuringRecognition(t *testing.T) {
	storage, err := fileservice.NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatalf("NewLocalStorage() error = %v", err)
	}
	source, err := storage.SaveBytes(context.Background(), "paper.pdf", []byte("%PDF-1.7\nsource"))
	if err != nil {
		t.Fatalf("save source: %v", err)
	}
	documentID := uuid.New()
	repo := &documentStatusRepoStub{document: types.Document{
		ID: documentID, StorageKey: source.Key, OriginalFilename: "paper.pdf",
		ContentType: "application/pdf", SizeBytes: source.Size, SourceState: types.DocumentSourceAvailable,
	}}
	svc := &documentService{
		repo: repo, storage: storage,
		recognizer: &recognitionStub{
			result: &types.RecognitionResult{
				OutputMode: types.RecognitionOutputMarkdownBytes,
				Markdown:   []byte("# Late result\n"),
				Metadata:   types.RecognitionMetadata{MarkdownFilename: "late.md", ContentType: "text/markdown"},
			},
			onRead: func() { _, _, _ = repo.CancelStage(context.Background(), documentID, types.DocumentStageMarkdown, 1) },
		},
	}
	payload, err := json.Marshal(types.DocumentProcessPayload{DocumentID: documentID.String(), Stage: types.DocumentStageMarkdown, Generation: 1})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := svc.RecognizeDocument(context.Background(), asynq.NewTask(types.TypeDocumentRecognize, payload)); !errors.Is(err, asynq.RevokeTask) {
		t.Fatalf("RecognizeDocument() error = %v, want RevokeTask", err)
	}
	if repo.document.MarkdownStatus != types.DocumentStageCancelled {
		t.Fatalf("Markdown status = %q, want cancelled", repo.document.MarkdownStatus)
	}
	if repo.markdown.StorageKey != "" {
		t.Fatalf("Markdown was stored after cancellation: %+v", repo.markdown)
	}
}

// TestRecognizeDocumentDiscardsOutputWhenStageCancelledDuringRecognition verifies
// that a stage cancelled mid-recognition yields a revoke and does not persist the
// late Markdown result.
func TestRecognizeDocumentDiscardsOutputWhenStageCancelledDuringRecognition(t *testing.T) {
	storage, err := fileservice.NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatalf("NewLocalStorage() error = %v", err)
	}
	source, err := storage.SaveBytes(context.Background(), "paper.pdf", []byte("%PDF-1.7\nsource"))
	if err != nil {
		t.Fatalf("save source: %v", err)
	}
	documentID := uuid.New()
	repo := &documentStatusRepoStub{document: types.Document{
		ID: documentID, StorageKey: source.Key, OriginalFilename: "paper.pdf",
		ContentType: "application/pdf", SizeBytes: source.Size, SourceState: types.DocumentSourceAvailable,
	}}
	svc := &documentService{
		repo: repo, storage: storage,
		recognizer: &recognitionStub{
			result: &types.RecognitionResult{
				OutputMode: types.RecognitionOutputMarkdownBytes,
				Markdown:   []byte("# Late result\n"),
				Metadata:   types.RecognitionMetadata{MarkdownFilename: "late.md", ContentType: "text/markdown"},
			},
			onRead: func() {
				_, _, _ = repo.CancelStage(context.Background(), documentID, types.DocumentStageMarkdown, 1)
			},
		},
	}
	payload, err := json.Marshal(types.DocumentProcessPayload{
		DocumentID: documentID.String(), Stage: types.DocumentStageMarkdown, Generation: 1,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := svc.RecognizeDocument(context.Background(), asynq.NewTask(types.TypeDocumentRecognize, payload)); !errors.Is(err, asynq.RevokeTask) {
		t.Fatalf("RecognizeDocument() error = %v, want RevokeTask", err)
	}
	if repo.markdown.StorageKey != "" {
		t.Fatalf("Markdown was stored after run cancellation: %+v", repo.markdown)
	}
}

// TestProcessDocumentMarksUnsupportedRecognitionFailedWithoutRetry verifies that
// unsupported source content permanently fails the markdown stage and is
// classified for Asynq as skip-retry.
func TestProcessDocumentMarksUnsupportedRecognitionFailedWithoutRetry(t *testing.T) {
	storage, err := fileservice.NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatalf("NewLocalStorage() error = %v", err)
	}
	source, err := storage.SaveBytes(context.Background(), "paper.pdf", []byte("%PDF-1.7\nsource"))
	if err != nil {
		t.Fatalf("save source: %v", err)
	}
	documentID := uuid.New()
	repo := &documentStatusRepoStub{document: types.Document{
		ID: documentID, StorageKey: source.Key, OriginalFilename: "paper.pdf",
		ContentType: "application/pdf", SizeBytes: source.Size, SourceState: types.DocumentSourceAvailable,
	}}
	svc := &documentService{
		repo: repo, storage: storage,
		recognizer: &recognitionStub{err: types.ErrUnsupportedRecognitionContentType},
	}
	payload, err := json.Marshal(types.DocumentProcessPayload{DocumentID: documentID.String(), Stage: types.DocumentStageMarkdown, Generation: 1})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	err = svc.RecognizeDocument(context.Background(), asynq.NewTask(types.TypeDocumentRecognize, payload))
	if !errors.Is(err, asynq.SkipRetry) {
		t.Fatalf("ProcessDocument() error = %v, want SkipRetry", err)
	}
	if repo.document.MarkdownStatus != types.DocumentStageFailed {
		t.Fatalf("Markdown status = %q, want failed", repo.document.MarkdownStatus)
	}
}

// TestProcessDocumentMarksUnavailableRecognizerFailedWithoutRetry verifies that a
// required recognizer outage fails the markdown stage with a bounded safe message
// and asks Asynq to skip retries.
func TestProcessDocumentMarksUnavailableRecognizerFailedWithoutRetry(t *testing.T) {
	storage, err := fileservice.NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatalf("NewLocalStorage() error = %v", err)
	}
	source, err := storage.SaveBytes(context.Background(), "paper.pdf", []byte("%PDF-1.7\nsource"))
	if err != nil {
		t.Fatalf("save source: %v", err)
	}
	documentID := uuid.New()
	repo := &documentStatusRepoStub{document: types.Document{
		ID: documentID, StorageKey: source.Key, OriginalFilename: "paper.pdf",
		ContentType: "application/pdf", SizeBytes: source.Size, SourceState: types.DocumentSourceAvailable,
	}}
	svc := &documentService{
		repo: repo, storage: storage,
		recognizer: &recognitionStub{err: types.ErrRecognitionServiceUnavailable},
	}
	payload, err := json.Marshal(types.DocumentProcessPayload{DocumentID: documentID.String(), Stage: types.DocumentStageMarkdown, Generation: 1})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	err = svc.RecognizeDocument(context.Background(), asynq.NewTask(types.TypeDocumentRecognize, payload))
	if !errors.Is(err, asynq.SkipRetry) {
		t.Fatalf("ProcessDocument() error = %v, want SkipRetry", err)
	}
	if repo.document.MarkdownStatus != types.DocumentStageFailed {
		t.Fatalf("Markdown status = %q, want failed", repo.document.MarkdownStatus)
	}
	if repo.document.MarkdownErrorMessage == nil || *repo.document.MarkdownErrorMessage != "a required processing service is unavailable" {
		t.Fatalf("Markdown error message = %v", repo.document.MarkdownErrorMessage)
	}
}

// ---------------------------------------------------------------------------
// Consolidated from document_housekeeping_test.go.
// ---------------------------------------------------------------------------

type documentQueueProbeStub struct {
	queued map[string]bool
	errs   map[string]error
}

func (s *documentQueueProbeStub) HasQueuedTaskForStage(_ context.Context, documentID string, _ types.DocumentStage, _ int64) (bool, error) {
	return s.queued[documentID], s.errs[documentID]
}

func (*documentQueueProbeStub) HasQueuedQueryApplyForReference(context.Context, string, int64) (bool, error) {
	return false, nil
}

func (s *documentQueueProbeStub) SnapshotQueuedWork(context.Context) (types.QueuedWorkSnapshot, error) {
	snapshot := types.NewQueuedWorkSnapshot()
	for _, err := range s.errs {
		if err != nil {
			return snapshot, err
		}
	}
	for documentID, queued := range s.queued {
		if queued {
			snapshot.StageTasks[types.QueuedStageKey(documentID, types.DocumentStageMarkdown, 1)] = struct{}{}
		}
	}
	return snapshot, nil
}

func (*documentQueueProbeStub) CancelTasksForDocument(context.Context, string) (int, int, error) {
	return 0, 0, nil
}
func (*documentQueueProbeStub) CancelTasksForStage(context.Context, string, types.DocumentStage, int64) (int, int, error) {
	return 0, 0, nil
}
func (s *documentQueueProbeStub) HasQueuedTasksForDocument(_ context.Context, documentID string) (bool, error) {
	return s.queued[documentID], s.errs[documentID]
}

func (s *documentQueueProbeStub) HasQueuedTaskTypeForDocument(
	_ context.Context,
	documentID string,
	_ string,
) (bool, error) {
	return s.queued[documentID], s.errs[documentID]
}

// TestDocumentHousekeepingStageProbeStub verifies that the housekeeping queue probe
// distinguishes a live queued task, an orphaned stage, and an inspection error.
func TestDocumentHousekeepingStageProbeStub(t *testing.T) {
	queuedDocument := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	orphanedDocument := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	probeFailureDocument := types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}
	service := &DocumentHousekeepingService{inspector: &documentQueueProbeStub{
		queued: map[string]bool{queuedDocument.ID.String(): true},
		errs:   map[string]error{probeFailureDocument.ID.String(): errors.New("redis unavailable")},
	}}

	queued, err := service.inspector.HasQueuedTaskForStage(context.Background(), queuedDocument.ID.String(), types.DocumentStageMarkdown, 1)
	if err != nil || !queued {
		t.Fatalf("queued stage = %t, error=%v", queued, err)
	}
	queued, err = service.inspector.HasQueuedTaskForStage(context.Background(), orphanedDocument.ID.String(), types.DocumentStageMarkdown, 1)
	if err != nil || queued {
		t.Fatalf("orphaned stage = %t, error=%v", queued, err)
	}
	if _, err = service.inspector.HasQueuedTaskForStage(context.Background(), probeFailureDocument.ID.String(), types.DocumentStageMarkdown, 1); err == nil {
		t.Fatal("queue probe error was lost")
	}
}

// TestDocumentStageColumnMapping verifies that each document stage maps to its
// dedicated status column and that an unknown stage maps to no column.
func TestDocumentStageColumnMapping(t *testing.T) {
	cases := map[types.DocumentStage]string{
		types.DocumentStageMarkdown:           "markdown_status",
		types.DocumentStageMetadataExtraction: "metadata_extraction_status",
		types.DocumentStageSourceLookup:       "source_lookup_status",
		types.DocumentStageReferenceLookup:    "reference_lookup_status",
	}
	for stage, want := range cases {
		if got := stageStateColumn(stage); got != want {
			t.Fatalf("stageStateColumn(%s) = %q, want %q", stage, got, want)
		}
	}
	if got := stageStateColumn("unknown"); got != "" {
		t.Fatalf("unknown stage column = %q", got)
	}
}

// ---------------------------------------------------------------------------
// Consolidated from document_housekeeping_postgres_integration_test.go.
// ---------------------------------------------------------------------------

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

	inspector.snapshotErr = errors.New("redis timeout")
	housekeeping.recoverStaleStages(ctx, time.Now().UTC())
	if got := markdownStatus(); got != types.DocumentStageRunning {
		t.Fatalf("markdown status after snapshot error = %q, want running", got)
	}

	inspector.snapshotErr = nil
	inspector.queuedStage = types.QueuedStageKey(documentID.String(), types.DocumentStageMarkdown, 1)
	housekeeping.recoverStaleStages(ctx, time.Now().UTC())
	if got := markdownStatus(); got != types.DocumentStageRunning {
		t.Fatalf("markdown status with queued task = %q, want running", got)
	}

	inspector.queuedStage = ""
	housekeeping.recoverStaleStages(ctx, time.Now().UTC())
	if got := markdownStatus(); got != types.DocumentStageFailed {
		t.Fatalf("markdown status without queued task = %q, want failed", got)
	}
}

// TestRecoverStaleStagesUsesOneSnapshotForManyCandidatesPostgres verifies that
// stale-stage recovery scans queued work once for all candidates and fails at
// most the bounded 200 candidates per pass.
func TestRecoverStaleStagesUsesOneSnapshotForManyCandidatesPostgres(t *testing.T) {
	db, cleanup := openServicePostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	staleAt := time.Now().UTC().Add(-2 * time.Hour)
	if err := db.Exec(`INSERT INTO documents
		(id, original_filename, content_type, size_bytes, storage_key, source_state, updated_at,
		 markdown_status, markdown_generation, markdown_updated_at)
		SELECT gen_random_uuid(), 'stale.pdf', 'application/pdf', 1,
		       'stale/' || gen_random_uuid(), 'available', ?,
		       'running', 1, ?
		FROM generate_series(1, 250)`, staleAt, staleAt).Error; err != nil {
		t.Fatalf("insert stale documents: %v", err)
	}
	inspector := &scriptedTaskInspector{}
	housekeeping := NewDocumentHousekeepingService(db, config.Config{}, inspector, nil)
	housekeeping.recoverStaleStages(ctx, time.Now().UTC())
	if inspector.snapshotCalls != 1 {
		t.Fatalf("snapshot calls = %d, want one bounded scan", inspector.snapshotCalls)
	}
	var failed int64
	if err := db.Raw(`SELECT COUNT(*) FROM documents WHERE markdown_status = 'failed'`).Scan(&failed).Error; err != nil {
		t.Fatalf("count failed documents: %v", err)
	}
	if failed != 200 {
		t.Fatalf("failed documents = %d, want the 200-candidate limit", failed)
	}
}

// scriptedTaskInspector lets the housekeeping test drive snapshot outcomes.
type scriptedTaskInspector struct {
	queuedStage   string
	snapshotErr   error
	snapshotCalls int
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

func (*scriptedTaskInspector) HasQueuedTaskForStage(context.Context, string, types.DocumentStage, int64) (bool, error) {
	return false, nil
}

func (*scriptedTaskInspector) HasQueuedQueryApplyForReference(context.Context, string, int64) (bool, error) {
	return false, nil
}

func (i *scriptedTaskInspector) SnapshotQueuedWork(context.Context) (types.QueuedWorkSnapshot, error) {
	i.snapshotCalls++
	snapshot := types.NewQueuedWorkSnapshot()
	if i.snapshotErr != nil {
		return snapshot, i.snapshotErr
	}
	if i.queuedStage != "" {
		snapshot.StageTasks[i.queuedStage] = struct{}{}
	}
	return snapshot, nil
}

func nullableFinished(status string, at time.Time) interface{} {
	if status == "succeeded" {
		return at
	}
	return nil
}

// ensureServiceTrgmExtension matches the repository test helper: pg_trgm must
// live in public so dropping a per-test schema cannot remove the operator
// class used by another concurrently running package's indexes.
func ensureServiceTrgmExtension(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Connection(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_lock(?)", int64(2026101901)).Error; err != nil {
			return err
		}
		defer func() { _ = tx.Exec("SELECT pg_advisory_unlock(?)", int64(2026101901)).Error }()
		var schema string
		if err := tx.Raw(`SELECT n.nspname FROM pg_extension e
			JOIN pg_namespace n ON n.oid = e.extnamespace
			WHERE e.extname = 'pg_trgm'`).Scan(&schema).Error; err != nil {
			return err
		}
		if schema == "" {
			return tx.Exec("CREATE EXTENSION pg_trgm SCHEMA public").Error
		}
		if schema != "public" {
			return tx.Exec("ALTER EXTENSION pg_trgm SET SCHEMA public").Error
		}
		return nil
	}); err != nil {
		t.Fatalf("ensure pg_trgm extension: %v", err)
	}
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
	ensureServiceTrgmExtension(t, admin)
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

// ---------------------------------------------------------------------------
// Consolidated from document_projections_postgres_integration_test.go.
// ---------------------------------------------------------------------------

// TestReadModelScalePostgres seeds 1,000 documents with metadata, citations,
// and spans, then verifies one 200-document projection batch stays bounded.
// It is the P2-7 scale check for the fan-out fix.
func TestReadModelScalePostgres(t *testing.T) {
	db, cleanup := openServicePostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()

	if err := db.Exec(`INSERT INTO documents
		(id, original_filename, content_type, size_bytes, storage_key, source_state, updated_at,
		 markdown_status, metadata_extraction_status)
		SELECT gen_random_uuid(), 'scale-' || lpad(n::text, 4, '0') || '.pdf',
		       'application/pdf', 1, 'scale/' || n, 'available', now(),
		       'succeeded', 'succeeded'
		FROM generate_series(1, 1000) AS n`).Error; err != nil {
		t.Fatalf("seed documents: %v", err)
	}
	if err := db.Exec(`INSERT INTO paper_metadata (document_id, title, authors, tags)
		SELECT id, 'Scale Paper ' || original_filename, '[]'::jsonb, '[]'::jsonb
		FROM documents`).Error; err != nil {
		t.Fatalf("seed metadata: %v", err)
	}
	if err := db.Exec(`INSERT INTO paper_citations
		(id, citing_document_id, reference_key, raw_reference, resolution_status,
		 resolution_updated_at, origin, created_at, updated_at)
		SELECT gen_random_uuid(), d.id, 'reference-' || n, 'Reference ' || n,
		       'not_started', now(), 'extraction', now(), now()
		FROM documents d CROSS JOIN generate_series(1, 5) AS n`).Error; err != nil {
		t.Fatalf("seed citations: %v", err)
	}
	if err := db.Exec(`INSERT INTO document_processing_spans
		(id, document_id, attempt, kind, scope, scope_id, status, started_at)
		SELECT gen_random_uuid(), id, 1, 'recognition', 'document', '', 'succeeded', now()
		FROM documents`).Error; err != nil {
		t.Fatalf("seed spans: %v", err)
	}

	var ids []uuid.UUID
	if err := db.Raw(`SELECT id FROM documents ORDER BY original_filename LIMIT 200`).Scan(&ids).Error; err != nil {
		t.Fatalf("load ids: %v", err)
	}
	documentIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		documentIDs = append(documentIDs, id.String())
	}

	documents := repository.NewDocumentRepository(db)
	metadata := repository.NewPaperMetadataRepository(db)
	citations := repository.NewPaperCitationRepository(db)
	spans := repository.NewDocumentProcessingSpanRepository(db)
	service := &documentService{
		repo:       documents,
		papers:     NewPaperService(documents, metadata, citations),
		processing: NewSpanTracker(spans),
	}

	started := time.Now()
	items, err := service.GetDocumentProjections(ctx, types.DocumentProjectionQuery{
		DocumentIDs: documentIDs, IncludeMetadata: true, IncludeProcessing: true,
	})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("GetDocumentProjections() error = %v", err)
	}
	if len(items) != 200 {
		t.Fatalf("projection count = %d, want 200", len(items))
	}
	for index, item := range items {
		if item.Document.ID != ids[index] {
			t.Fatalf("item %d document = %s, want %s", index, item.Document.ID, ids[index])
		}
		if item.Metadata == nil || item.Metadata.Title == "" {
			t.Fatalf("item %d metadata = %#v", index, item.Metadata)
		}
		if item.Processing == nil || item.Processing.ReferenceProgress == nil {
			t.Fatalf("item %d processing = %#v", index, item.Processing)
		}
		if item.Processing.ReferenceProgress.Queued != 5 {
			t.Fatalf("item %d queued progress = %d, want 5",
				index, item.Processing.ReferenceProgress.Queued)
		}
		if len(item.Processing.Stages) != 1 {
			t.Fatalf("item %d span count = %d, want 1", index, len(item.Processing.Stages))
		}
	}
	t.Logf("200-document projection over 1,000 documents: %s", elapsed)
	if elapsed > 10*time.Second {
		t.Fatalf("projection batch took %s, want under 10s", elapsed)
	}
}

// ---------------------------------------------------------------------------
// Consolidated from document_reading_test.go.
// ---------------------------------------------------------------------------

type documentPageRendererStub struct {
	request types.PDFPageRenderRequest
	result  *types.RenderedPDFPage
	err     error
}

func (stub *documentPageRendererStub) RenderPage(
	_ context.Context, request types.PDFPageRenderRequest,
) (*types.RenderedPDFPage, error) {
	stub.request = request
	return stub.result, stub.err
}

// TestDocumentReadingServiceRendersPage verifies that page rendering resolves an
// available source and forwards the page, bbox, DPI, and recognition generation
// to the renderer.
func TestDocumentReadingServiceRendersPage(t *testing.T) {
	documentID := uuid.New()
	renderer := &documentPageRendererStub{result: &types.RenderedPDFPage{
		PNG:         []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'},
		ContentType: "image/png", Width: 10, Height: 20,
	}}
	repo := &documentStatusRepoStub{document: types.Document{
		ID: documentID, SourceState: types.DocumentSourceAvailable,
		StorageKey: "pdf/paper.pdf", MarkdownGeneration: 7,
	}}
	service := NewDocumentReadingServiceWithRenderer(repo, nil, nil, renderer)
	bbox := &types.NormalizedPDFRect{X0: 0.1, Y0: 0.2, X1: 0.8, Y1: 0.9}
	result, err := service.RenderPage(context.Background(), documentID.String(), 3, bbox, 144)
	if err != nil {
		t.Fatalf("RenderPage() error = %v", err)
	}
	if result.Width != 10 || result.Height != 20 {
		t.Fatalf("result = %#v", result)
	}
	if renderer.request.SourceStorageKey != "pdf/paper.pdf" ||
		renderer.request.PageNumber != 3 ||
		renderer.request.DPI != 144 ||
		renderer.request.FullPage ||
		renderer.request.RecognitionGeneration != 7 {
		t.Fatalf("renderer request = %#v", renderer.request)
	}
	if renderer.request.BBox == nil || renderer.request.BBox.X0 != 0.1 {
		t.Fatalf("renderer bbox = %#v", renderer.request.BBox)
	}
}

// TestDocumentReadingServiceRejectsOversizedRenderedPage verifies that a rendered
// page exceeding the pixel budget is rejected with ErrPDFRenderTooLarge.
func TestDocumentReadingServiceRejectsOversizedRenderedPage(t *testing.T) {
	documentID := uuid.New()
	renderer := &documentPageRendererStub{result: &types.RenderedPDFPage{
		PNG:         []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'},
		ContentType: "image/png", Width: types.MaxPDFRenderPixels + 1, Height: 1,
	}}
	repo := &documentStatusRepoStub{document: types.Document{
		ID: documentID, SourceState: types.DocumentSourceAvailable, StorageKey: "pdf/paper.pdf",
	}}
	service := NewDocumentReadingServiceWithRenderer(repo, nil, nil, renderer)
	if _, err := service.RenderPage(context.Background(), documentID.String(), 1, nil, 144); !errors.Is(err, types.ErrPDFRenderTooLarge) {
		t.Fatalf("oversized page error = %v", err)
	}
}

// TestDocumentReadingServiceRejectsUnavailableRendererAndInvalidBBox verifies that
// page rendering reports ErrPDFRenderUnavailable without a renderer and rejects
// an inverted bbox as invalid.
func TestDocumentReadingServiceRejectsUnavailableRendererAndInvalidBBox(t *testing.T) {
	documentID := uuid.New()
	repo := &documentStatusRepoStub{document: types.Document{
		ID: documentID, SourceState: types.DocumentSourceAvailable, StorageKey: "pdf/paper.pdf",
	}}
	service := NewDocumentReadingService(repo, nil, nil)
	if _, err := service.RenderPage(context.Background(), documentID.String(), 1, nil, 0); !errors.Is(err, types.ErrPDFRenderUnavailable) {
		t.Fatalf("unavailable renderer error = %v", err)
	}
	renderer := &documentPageRendererStub{}
	service = NewDocumentReadingServiceWithRenderer(repo, nil, nil, renderer)
	bbox := &types.NormalizedPDFRect{X0: 0.8, Y0: 0.2, X1: 0.1, Y1: 0.9}
	if _, err := service.RenderPage(context.Background(), documentID.String(), 1, bbox, 144); !errors.Is(err, types.ErrInvalidPDFRenderRequest) {
		t.Fatalf("invalid bbox error = %v", err)
	}
}

// TestDocumentReadingServiceReadsMarkdownAndBlocks verifies that reading returns
// stored Markdown metadata and page-filtered blocks, and rejects a negative page
// number.
func TestDocumentReadingServiceReadsMarkdownAndBlocks(t *testing.T) {
	storage, err := fileservice.NewLocalStorage(t.TempDir(), fileservice.DefaultMaxUploadSize)
	if err != nil {
		t.Fatal(err)
	}
	storedMarkdown, err := storage.Save(context.Background(), interfaces.FileUpload{
		Filename: "parsed.md", ContentType: "text/markdown", Category: interfaces.StorageCategoryMarkdown,
		Content: strings.NewReader("# Paper\nbody"),
	})
	if err != nil {
		t.Fatal(err)
	}
	page := 1
	documentID, blockID := uuid.New(), uuid.New()
	repo := &documentStatusRepoStub{
		document: types.Document{ID: documentID, SourceState: types.DocumentSourceAvailable, OriginalFilename: "paper.pdf"},
		markdown: types.DocumentMarkdown{
			DocumentID: documentID, StorageKey: storedMarkdown.Key, Filename: "parsed.md",
			ContentType: "text/markdown", SizeBytes: storedMarkdown.Size, PageCount: 2, RecognitionGeneration: 3,
		},
		blocks: []types.Block{{
			ID: blockID, DocumentID: documentID, Ordinal: 0, Type: "text", Text: "# Paper",
			Location: &types.PDFBlockLocation{PageNumber: &page},
		}},
	}
	service := NewDocumentReadingService(repo, storage, nil)

	content, err := service.GetDocumentMarkdown(context.Background(), documentID.String())
	if err != nil || string(content.Markdown) != "# Paper\nbody" ||
		content.PageCount != 2 || content.RecognitionGeneration != 3 || content.Filename != "parsed.md" {
		t.Fatalf("Markdown content = %#v, error=%v", content, err)
	}
	blocks, err := service.GetDocumentBlocks(context.Background(), documentID.String(), 1)
	if err != nil || len(blocks) != 1 || blocks[0].ID != blockID {
		t.Fatalf("blocks = %#v, error=%v", blocks, err)
	}
	if _, err := service.GetDocumentBlocks(context.Background(), documentID.String(), -1); err == nil {
		t.Fatal("negative page was accepted")
	}
}

// ---------------------------------------------------------------------------
// Consolidated from document_recognition_enqueue_test.go.
// ---------------------------------------------------------------------------

// TestEnqueueDocumentRecognitionUsesStageGeneration verifies that first recognition
// enqueue creates the queued markdown stage and sends its initial generation in
// the task payload.
func TestEnqueueDocumentRecognitionUsesStageGeneration(t *testing.T) {
	documentID := uuid.New()
	documents := &documentStatusRepoStub{document: types.Document{ID: documentID, SourceState: types.DocumentSourceAvailable}}
	enqueuer := &documentTaskEnqueuerStub{}
	enqueuer.onEnqueue = func(task *asynq.Task) {
		var payload types.DocumentProcessPayload
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			t.Fatal(err)
		}
		if task.Type() != types.TypeDocumentRecognize || payload.Stage != types.DocumentStageMarkdown || payload.Generation != 1 {
			t.Fatalf("new recognition payload = %#v type=%s", payload, task.Type())
		}
	}
	service := &documentService{repo: documents, taskEnqueuer: enqueuer}
	if err := service.enqueueDocumentRecognition(context.Background(), documentID); err != nil {
		t.Fatal(err)
	}
}

// TestEnqueueDocumentRecognitionMarksStageFailedWhenEnqueueFails verifies that a
// recognition enqueue failure marks the newly queued markdown stage failed after
// a single attempt.
func TestEnqueueDocumentRecognitionMarksStageFailedWhenEnqueueFails(t *testing.T) {
	documentID := uuid.New()
	documents := &documentStatusRepoStub{document: types.Document{ID: documentID, SourceState: types.DocumentSourceAvailable}}
	enqueuer := &documentTaskEnqueuerStub{enqueueErr: errors.New("queue unavailable")}
	service := &documentService{repo: documents, taskEnqueuer: enqueuer}
	if err := service.enqueueDocumentRecognition(context.Background(), documentID); err == nil {
		t.Fatal("enqueueDocumentRecognition() error = nil")
	}
	if len(enqueuer.tasks) != 1 {
		t.Fatalf("enqueued task attempts = %d, want 1", len(enqueuer.tasks))
	}
	if documents.document.MarkdownStatus != types.DocumentStageFailed {
		t.Fatalf("document after queue failure = %#v", documents.document)
	}
}

// ---------------------------------------------------------------------------
// Consolidated from document_recognition_parser_integration_test.go.
// ---------------------------------------------------------------------------

type recordingLiveRecognitionClient struct {
	papersv1.DocumentParserClient
	requests []*papersv1.RecognitionRequest
}

func (c *recordingLiveRecognitionClient) Recognize(ctx context.Context, request *papersv1.RecognitionRequest, options ...grpc.CallOption) (*papersv1.RecognitionResult, error) {
	c.requests = append(c.requests, request)
	return c.DocumentParserClient.Recognize(ctx, request, options...)
}

// scripts/parser/check_recognition_storage_key.py owns the server and shared root.
// This exercises Go service -> gRPC proxy -> actual shared worker -> PDF.
func TestStoredPDFRecognitionParserIntegration(t *testing.T) {
	address, root := os.Getenv("PAPERS_MANAGER_TEST_PARSER_ADDR"), os.Getenv("PAPERS_MANAGER_TEST_PARSER_UPLOAD_DIR")
	if address == "" || root == "" {
		t.Skip("private parser integration environment is not configured")
	}
	content, err := os.ReadFile(os.Getenv("PAPERS_MANAGER_TEST_PARSER_PDF"))
	if err != nil {
		t.Fatal(err)
	}
	connection, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(16<<20)))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	for _, shared := range []bool{true, false} {
		name := "key"
		directory := root
		if !shared {
			name = "bytes-fallback"
			directory = t.TempDir()
		}
		t.Run(name, func(t *testing.T) {
			storage, err := fileservice.NewLocalStorage(directory, 16<<20)
			if err != nil {
				t.Fatal(err)
			}
			source, err := storage.SaveBytes(context.Background(), "source.pdf", content)
			if err != nil {
				t.Fatal(err)
			}
			counted := &recognitionSourceStorage{FileStorage: storage}
			client := &recordingLiveRecognitionClient{DocumentParserClient: papersv1.NewDocumentParserClient(connection)}
			documentID := uuid.New()
			repo := &documentStatusRepoStub{document: types.Document{ID: documentID, StorageKey: source.Key, OriginalFilename: "source.pdf", ContentType: "application/pdf", SizeBytes: source.Size, SourceState: types.DocumentSourceAvailable, MarkdownStatus: types.DocumentStageQueued, MarkdownGeneration: 1}}
			svc := &documentService{repo: repo, storage: counted, recognizer: docrecognition.NewBuiltinRecognizerWithClient(client), taskEnqueuer: &documentTaskEnqueuerStub{}}
			payload, _ := json.Marshal(types.DocumentProcessPayload{DocumentID: documentID.String(), Stage: types.DocumentStageMarkdown, Generation: 1})
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if err := svc.RecognizeDocument(ctx, asynq.NewTask(types.TypeDocumentRecognize, payload)); err != nil {
				t.Fatal(err)
			}
			wantCalls, wantOpens, wantMode := 1, 0, types.RecognitionInputStorageKey
			if !shared {
				wantCalls, wantOpens, wantMode = 2, 1, types.RecognitionInputBytes
			}
			if len(client.requests) != wantCalls || counted.opens != wantOpens || repo.markdown.InputMode != string(wantMode) || repo.markdown.PageCount != 2 || len(repo.blocks) == 0 {
				t.Fatalf("calls=%d opens=%d input=%s pages=%d blocks=%d", len(client.requests), counted.opens, repo.markdown.InputMode, repo.markdown.PageCount, len(repo.blocks))
			}
			if client.requests[0].GetSourceStorageKey() != source.Key || len(client.requests[0].GetContent()) != 0 {
				t.Fatal("initial request transmitted PDF bytes")
			}
			if !shared && len(client.requests[1].GetContent()) != len(content) {
				t.Fatal("fallback content missing")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Consolidated from document_recognition_source_test.go.
// ---------------------------------------------------------------------------

type recognitionSourceStorage struct {
	interfaces.FileStorage
	content       []byte
	opens, closes int
	err           error
}

func (s *recognitionSourceStorage) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	s.opens++
	if s.err != nil {
		return nil, s.err
	}
	if s.FileStorage != nil {
		return s.FileStorage.Open(ctx, key)
	}
	return &recognitionSourceReader{Reader: bytes.NewReader(s.content), close: func() { s.closes++ }}, nil
}

type recognitionSourceReader struct {
	*bytes.Reader
	close func()
}

func (r *recognitionSourceReader) Close() error { r.close(); return nil }

type sourceRequestRecorder struct {
	requests []types.RecognitionRequest
	read     func(context.Context, *types.RecognitionRequest) (*types.RecognitionResult, error)
}

func (r *sourceRequestRecorder) Read(ctx context.Context, request *types.RecognitionRequest) (*types.RecognitionResult, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	r.requests = append(r.requests, *request)
	if r.read != nil {
		return r.read(ctx, request)
	}
	return &types.RecognitionResult{OutputMode: types.RecognitionOutputMarkdownBytes, Markdown: []byte("# Read\n"), Metadata: types.RecognitionMetadata{MarkdownFilename: "paper.md", ContentType: "text/markdown", SizeBytes: 7}}, nil
}

type storageKeyRequestRecorder struct{ *sourceRequestRecorder }

func (*storageKeyRequestRecorder) SupportsStorageKey() bool { return true }

// TestRecognitionSourcePrefersKeyAndLoadsBytesOnlyOnFallback verifies that
// storage-key-capable recognizers receive the source key without reading bytes,
// falling back to bounded source bytes only when required or when the shared file
// is missing.
func TestRecognitionSourcePrefersKeyAndLoadsBytesOnlyOnFallback(t *testing.T) {
	for _, test := range []struct {
		name, contentType    string
		keyCapable           bool
		firstError           error
		wantMode             types.RecognitionInputMode
		wantCalls, wantOpens int
	}{
		{"saved pdf", "application/pdf; charset=binary", true, nil, types.RecognitionInputStorageKey, 1, 0},
		{"missing shared file", "application/pdf", true, types.ErrRecognitionStoredSourceUnavailable, types.RecognitionInputBytes, 2, 1},
		{"text input", "text/plain", true, nil, types.RecognitionInputBytes, 1, 1},
		{"bytes-only provider", "application/pdf", false, nil, types.RecognitionInputBytes, 1, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			storage := &recognitionSourceStorage{content: []byte("%PDF")}
			recorder := &sourceRequestRecorder{}
			recorder.read = func(_ context.Context, req *types.RecognitionRequest) (*types.RecognitionResult, error) {
				if req.InputMode == types.RecognitionInputStorageKey && test.firstError != nil {
					return nil, test.firstError
				}
				return &types.RecognitionResult{}, nil
			}
			var recognizer interfaces.DocumentRecognizer = recorder
			if test.keyCapable {
				recognizer = &storageKeyRequestRecorder{recorder}
			}
			svc := &documentService{storage: storage, recognizer: recognizer}
			document := types.Document{ID: uuid.New(), OriginalFilename: "paper.pdf", ContentType: test.contentType, SizeBytes: 4, StorageKey: "pdf/source.pdf"}
			_, mode, err := svc.readStoredRecognitionSource(context.Background(), document, types.DocumentProcessPayload{})
			if err != nil || mode != test.wantMode || len(recorder.requests) != test.wantCalls || storage.opens != test.wantOpens || storage.closes != test.wantOpens {
				t.Fatalf("mode=%s err=%v requests=%+v opens=%d closes=%d", mode, err, recorder.requests, storage.opens, storage.closes)
			}
			if recorder.requests[0].InputMode == types.RecognitionInputStorageKey && (recorder.requests[0].Content != nil || recorder.requests[0].SourceStorageKey != document.StorageKey) {
				t.Fatal("key request carried bytes or wrong key")
			}
			if mode == types.RecognitionInputBytes && string(recorder.requests[len(recorder.requests)-1].Content) != "%PDF" {
				t.Fatal("bytes fallback lost source content")
			}
		})
	}
}

// TestRecognitionSourceDoesNotFallbackOnParseTransportOrCancellationErrors verifies
// that parse, transport, and cancellation failures from a storage-key request are
// returned directly instead of triggering a bytes fallback.
func TestRecognitionSourceDoesNotFallbackOnParseTransportOrCancellationErrors(t *testing.T) {
	for _, failure := range []error{errors.New("parse failed"), types.ErrRecognitionServiceUnavailable, context.Canceled, context.DeadlineExceeded} {
		t.Run(failure.Error(), func(t *testing.T) {
			storage := &recognitionSourceStorage{err: errors.New("unexpected source read")}
			recorder := &sourceRequestRecorder{read: func(context.Context, *types.RecognitionRequest) (*types.RecognitionResult, error) {
				return nil, failure
			}}
			svc := &documentService{storage: storage, recognizer: &storageKeyRequestRecorder{recorder}}
			_, _, err := svc.readStoredRecognitionSource(context.Background(), types.Document{ID: uuid.New(), OriginalFilename: "paper.pdf", ContentType: "application/pdf", SizeBytes: 4, StorageKey: "pdf/source.pdf"}, types.DocumentProcessPayload{})
			if !errors.Is(err, failure) || storage.opens != 0 || len(recorder.requests) != 1 {
				t.Fatalf("err=%v opens=%d calls=%d", err, storage.opens, len(recorder.requests))
			}
		})
	}
}

// TestRecognitionSourceRechecksCancellationBeforeBytesFallback verifies that
// cancellation occurring during the key request is checked before any bytes
// fallback read.
func TestRecognitionSourceRechecksCancellationBeforeBytesFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	storage := &recognitionSourceStorage{}
	recorder := &sourceRequestRecorder{read: func(context.Context, *types.RecognitionRequest) (*types.RecognitionResult, error) {
		cancel()
		return nil, types.ErrRecognitionStoredSourceUnavailable
	}}
	svc := &documentService{storage: storage, recognizer: &storageKeyRequestRecorder{recorder}}
	_, _, err := svc.readStoredRecognitionSource(ctx, types.Document{ID: uuid.New(), OriginalFilename: "paper.pdf", ContentType: "application/pdf", StorageKey: "pdf/source.pdf"}, types.DocumentProcessPayload{})
	if !errors.Is(err, context.Canceled) || storage.opens != 0 {
		t.Fatalf("err=%v opens=%d", err, storage.opens)
	}
}

// TestRecognitionSourceFallbackStillEnforcesRecordedSize verifies that the bytes
// fallback enforces the recorded source size limit and closes the opened file
// when it is exceeded.
func TestRecognitionSourceFallbackStillEnforcesRecordedSize(t *testing.T) {
	storage := &recognitionSourceStorage{content: []byte("too large")}
	recorder := &sourceRequestRecorder{read: func(context.Context, *types.RecognitionRequest) (*types.RecognitionResult, error) {
		return nil, types.ErrRecognitionStoredSourceUnavailable
	}}
	svc := &documentService{storage: storage, recognizer: &storageKeyRequestRecorder{recorder}}
	_, _, err := svc.readStoredRecognitionSource(context.Background(), types.Document{ID: uuid.New(), OriginalFilename: "paper.pdf", ContentType: "application/pdf", SizeBytes: 4, StorageKey: "pdf/source.pdf"}, types.DocumentProcessPayload{})
	if err == nil || storage.closes != 1 || len(recorder.requests) != 1 {
		t.Fatalf("err=%v closes=%d calls=%d", err, storage.closes, len(recorder.requests))
	}
}

// TestRecognitionSourceRechecksStageGenerationBeforeFallback verifies that the
// stage generation fence is rechecked before bytes fallback, so cancelled work is
// revoked instead of reading the source.
func TestRecognitionSourceRechecksStageGenerationBeforeFallback(t *testing.T) {
	document := types.Document{ID: uuid.New(), OriginalFilename: "paper.pdf", ContentType: "application/pdf", StorageKey: "pdf/source.pdf", MarkdownStatus: types.DocumentStageRunning, MarkdownGeneration: 1}
	repo := &documentStatusRepoStub{document: document}
	storage := &recognitionSourceStorage{}
	recorder := &sourceRequestRecorder{read: func(context.Context, *types.RecognitionRequest) (*types.RecognitionResult, error) {
		repo.document.MarkdownStatus = types.DocumentStageCancelled
		return nil, types.ErrRecognitionStoredSourceUnavailable
	}}
	svc := &documentService{repo: repo, storage: storage, recognizer: &storageKeyRequestRecorder{recorder}}
	_, _, err := svc.readStoredRecognitionSource(context.Background(), document, types.DocumentProcessPayload{Stage: types.DocumentStageMarkdown, Generation: 1})
	if !errors.Is(err, asynq.RevokeTask) || storage.opens != 0 {
		t.Fatalf("error=%v opens=%d", err, storage.opens)
	}
}

// TestRecognizeDocumentPersistsActualStorageKeyInputMode verifies that successful
// storage-key recognition records the actual input mode on the Markdown row
// without opening the source file.
func TestRecognizeDocumentPersistsActualStorageKeyInputMode(t *testing.T) {
	local, err := fileservice.NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	storage := &recognitionSourceStorage{FileStorage: local}
	documentID := uuid.New()
	repo := &documentStatusRepoStub{document: types.Document{ID: documentID, OriginalFilename: "paper.pdf", ContentType: "application/pdf", StorageKey: "pdf/shared.pdf", SizeBytes: 4, SourceState: types.DocumentSourceAvailable, MarkdownStatus: types.DocumentStageQueued, MarkdownGeneration: 1}}
	recorder := &sourceRequestRecorder{}
	svc := &documentService{repo: repo, storage: storage, recognizer: &storageKeyRequestRecorder{recorder}, taskEnqueuer: &documentTaskEnqueuerStub{}}
	payload, _ := json.Marshal(types.DocumentProcessPayload{DocumentID: documentID.String(), Stage: types.DocumentStageMarkdown, Generation: 1})
	if err := svc.RecognizeDocument(context.Background(), asynq.NewTask(types.TypeDocumentRecognize, payload)); err != nil {
		t.Fatal(err)
	}
	if repo.markdown.InputMode != string(types.RecognitionInputStorageKey) || storage.opens != 0 {
		t.Fatalf("input=%s opens=%d", repo.markdown.InputMode, storage.opens)
	}
}

// ---------------------------------------------------------------------------
// Consolidated from document_remote_test.go.
// ---------------------------------------------------------------------------

type documentBatchRepoStub struct {
	documentStatusRepoStub
	documents    map[uuid.UUID]types.Document
	searchCalls  int
	failSearchAt int
	searchErr    error
}

func (*documentBatchRepoStub) CitationTargetProgress(context.Context, uuid.UUID) (types.PaperReferenceProgress, error) {
	return types.PaperReferenceProgress{}, nil
}

func (s *documentBatchRepoStub) ReplaceOlderDocument(context.Context, uuid.UUID, uuid.UUID) (types.DocumentFileKeys, error) {
	return types.DocumentFileKeys{}, nil
}

func (s *documentBatchRepoStub) Create(_ context.Context, document types.Document) (types.Document, error) {
	if s.documents == nil {
		s.documents = make(map[uuid.UUID]types.Document)
	}
	s.documents[document.ID] = document
	return document, nil
}

func (s *documentBatchRepoStub) Get(_ context.Context, id uuid.UUID) (types.Document, error) {
	document, ok := s.documents[id]
	if !ok {
		return types.Document{}, os.ErrNotExist
	}
	return document, nil
}

func (s *documentBatchRepoStub) Search(_ context.Context, search types.DocumentSearch) (types.DocumentPage, error) {
	s.searchCalls++
	if s.failSearchAt > 0 && s.searchCalls >= s.failSearchAt {
		return types.DocumentPage{}, s.searchErr
	}
	result := types.DocumentPage{Page: search.Page, PageSize: search.PageSize, Items: []types.Document{}}
	for _, document := range s.documents {
		if search.DOI != "" && strings.EqualFold(document.DOI, search.DOI) ||
			search.FileSHA256 != "" && strings.EqualFold(document.FileSHA256, search.FileSHA256) {
			result.Items = append(result.Items, document)
		}
	}
	result.Total = int64(len(result.Items))
	return result, nil
}

// TestCreateDocumentsFromFilesPreservesPriorResultsWhenDeduplicationFails verifies
// that a duplicate-lookup failure stops a file batch, preserves the previously
// accepted item, and reports the current item as failed.
func TestCreateDocumentsFromFilesPreservesPriorResultsWhenDeduplicationFails(t *testing.T) {
	repo := &documentBatchRepoStub{failSearchAt: 2, searchErr: errors.New("database unavailable")}
	storage, err := fileservice.NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	service := &documentService{repo: repo, storage: storage}

	result, err := service.CreateDocumentsFromFiles(context.Background(), []*multipart.FileHeader{
		documentUploadFileHeader(t, "first.txt", []byte("first")),
		documentUploadFileHeader(t, "second.txt", []byte("second")),
		documentUploadFileHeader(t, "not-processed.txt", []byte("third")),
	}, nil)
	if err != nil {
		t.Fatalf("CreateDocumentsFromFiles() error = %v", err)
	}
	if !result.Stopped || result.Error == "" || len(result.Items) != 2 {
		t.Fatalf("result = %#v", result)
	}
	if result.Items[0].Status != types.DocumentImportItemAccepted || result.Items[0].Document == nil {
		t.Fatalf("accepted item = %#v", result.Items[0])
	}
	if result.Items[1].Index != 1 || result.Items[1].Status != types.DocumentImportItemFailed || result.Items[1].Error == "" {
		t.Fatalf("failed item = %#v", result.Items[1])
	}
	if len(repo.documents) != 1 {
		t.Fatalf("created documents = %#v, want only the first file", repo.documents)
	}
	if repo.searchCalls != 2 {
		t.Fatalf("duplicate lookup count = %d, want one lookup per processed file", repo.searchCalls)
	}
}

// TestCreateDocumentsFromURLsStopsWhenDeduplicationFails verifies that a
// duplicate-lookup failure stops a URL batch after preserving the accepted first
// import and marking the second item failed.
func TestCreateDocumentsFromURLsStopsWhenDeduplicationFails(t *testing.T) {
	server := remotePDFTestServer(t)
	repo := &documentBatchRepoStub{failSearchAt: 2, searchErr: errors.New("database unavailable")}
	storage, err := fileservice.NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	service := &documentService{repo: repo, storage: storage, urlResolver: paperurl.NewResolver()}

	result, err := service.CreateDocumentsFromURLs(context.Background(), []types.DocumentURLImportCommand{
		{URL: server.URL + "/first.pdf"},
		{URL: server.URL + "/second.pdf"},
		{URL: server.URL + "/not-processed.pdf"},
	})
	if err != nil {
		t.Fatalf("CreateDocumentsFromURLs() error = %v", err)
	}
	if !result.Stopped || result.Error == "" || len(result.Items) != 2 {
		t.Fatalf("result = %#v", result)
	}
	if result.Items[0].Status != types.DocumentImportItemAccepted || result.Items[0].Document == nil {
		t.Fatalf("accepted item = %#v", result.Items[0])
	}
	if result.Items[1].Index != 1 || result.Items[1].Status != types.DocumentImportItemFailed || result.Items[1].Error == "" {
		t.Fatalf("failed item = %#v", result.Items[1])
	}
	if len(repo.documents) != 1 {
		t.Fatalf("created documents = %#v, want only the first URL", repo.documents)
	}
}

// TestCreateDocumentsFromURLsReportsPartialResultsAndContentDuplicates verifies
// that URL batch import reports an invalid URL, queues a valid import, and maps
// repeated content to the same accepted document as a duplicate.
func TestCreateDocumentsFromURLsReportsPartialResultsAndContentDuplicates(t *testing.T) {
	server := remotePDFTestServer(t)
	repo := &documentBatchRepoStub{}
	storage, err := fileservice.NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	service := &documentService{repo: repo, storage: storage, urlResolver: paperurl.NewResolver()}

	result, err := service.CreateDocumentsFromURLs(context.Background(), []types.DocumentURLImportCommand{
		{URL: "ftp://example.invalid/paper.pdf"},
		{URL: server.URL + "/paper.pdf"},
		{URL: server.URL + "/paper.pdf"},
	})
	if err != nil {
		t.Fatalf("CreateDocumentsFromURLs() error = %v", err)
	}
	if len(result.Items) != 3 {
		t.Fatalf("items = %#v", result.Items)
	}
	if result.Items[0].Status != types.DocumentImportItemRejected {
		t.Fatalf("invalid URL item = %#v", result.Items[0])
	}
	if result.Items[1].Status != types.DocumentImportItemAccepted || result.Items[1].Document == nil ||
		result.Items[1].ProcessingQueued == nil || !*result.Items[1].ProcessingQueued {
		t.Fatalf("accepted item = %#v", result.Items[1])
	}
	if result.Items[2].Status != types.DocumentImportItemDuplicate || result.Items[2].Document == nil ||
		result.Items[2].Document.ID != result.Items[1].Document.ID {
		t.Fatalf("duplicate item = %#v", result.Items[2])
	}
}

// TestCreateDocumentFromURLStoresPDFAndStartsPipeline verifies that importing a
// remote PDF normalizes the DOI, stores the file, records its hash, and queues
// markdown recognition.
func TestCreateDocumentFromURLStoresPDFAndStartsPipeline(t *testing.T) {
	server := remotePDFTestServer(t)
	repo := &documentStatusRepoStub{}
	storage, err := fileservice.NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	tasks := &documentTaskEnqueuerStub{}
	papers := newPaperInfoPaperServiceStub()
	service := &documentService{
		repo: repo, storage: storage, taskEnqueuer: tasks,
		papers: papers, urlResolver: paperurl.NewResolver(),
	}
	created, err := service.CreateDocumentFromURL(context.Background(), types.DocumentURLImportCommand{
		URL: server.URL + "/paper.pdf", DOI: "https://doi.org/10.1000/Remote",
	})
	if err != nil {
		t.Fatalf("CreateDocumentFromURL() error = %v", err)
	}
	if created.DOI != "10.1000/remote" || created.FileSHA256 == "" || created.SourceState != types.DocumentSourceAvailable || created.MarkdownStatus != types.DocumentStageQueued {
		t.Fatalf("created = %#v", created)
	}
	if len(tasks.documents) != 1 || tasks.documents[0].DocumentID != created.ID.String() {
		t.Fatalf("queued tasks = %#v", tasks.documents)
	}
	opened, err := storage.Open(context.Background(), created.StorageKey)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	_ = opened.Close()
}

// TestCreateDocumentFromURLReturnsCreatedDocumentWhenInitialQueueFails verifies
// that a queue failure after a remote download still returns a persisted,
// retryable document marked failed with a safe error.
func TestCreateDocumentFromURLReturnsCreatedDocumentWhenInitialQueueFails(t *testing.T) {
	server := remotePDFTestServer(t)
	repo := &documentStatusRepoStub{}
	storage, err := fileservice.NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	service := &documentService{
		repo: repo, storage: storage,
		taskEnqueuer: &documentTaskEnqueuerStub{enqueueErr: errors.New("redis unavailable")},
		urlResolver:  paperurl.NewResolver(),
	}

	created, err := service.CreateDocumentFromURL(context.Background(), types.DocumentURLImportCommand{
		URL: server.URL + "/paper.pdf",
	})
	if !errors.Is(err, ErrDocumentProcessQueue) {
		t.Fatalf("CreateDocumentFromURL() error = %v, want ErrDocumentProcessQueue", err)
	}
	if created == nil || created.ID == uuid.Nil || created.MarkdownStatus != types.DocumentStageFailed {
		t.Fatalf("created = %#v, want durable failed document", created)
	}
	if created.MarkdownErrorMessage == nil || *created.MarkdownErrorMessage != "processing task could not be queued" {
		t.Fatalf("safe queue failure message = %#v", created.MarkdownErrorMessage)
	}
	opened, openErr := storage.Open(context.Background(), created.StorageKey)
	if openErr != nil {
		t.Fatalf("stored remote PDF must remain available for retry: %v", openErr)
	}
	_ = opened.Close()
}

// TestRequestReferencePDFDownloadQueuesOnlyExplicitTarget verifies that requesting
// a reference PDF enqueues exactly one remote-import task for that reference and
// records the download attempt on the reference row.
func TestRequestReferencePDFDownloadQueuesOnlyExplicitTarget(t *testing.T) {
	referenceID := uuid.New()
	repo := newPaperDocumentRepoStub()
	papers := newPaperInfoPaperServiceStub()
	papers.references[referenceID] = types.PaperReference{
		ReferenceID: referenceID, CitingDocumentID: uuid.New(), RawReference: "Downloadable reference",
		SourceURL:        "https://example.test/paper.pdf",
		ResolutionStatus: types.DocumentStageNotStarted, ResolutionGeneration: 2,
	}
	tasks := &documentTaskEnqueuerStub{}
	service := &documentService{repo: repo, papers: papers, taskEnqueuer: tasks}
	if err := service.RequestReferencePDFDownload(context.Background(), referenceID.String()); err != nil {
		t.Fatal(err)
	}
	if len(tasks.tasks) != 1 || tasks.tasks[0].Type() != types.TypeDocumentRemoteImport {
		t.Fatalf("queued tasks = %#v", tasks.tasks)
	}
	var payload types.DocumentRemoteImportPayload
	if err := json.Unmarshal(tasks.tasks[0].Payload(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ReferenceID != referenceID.String() {
		t.Fatalf("payload reference = %q, want %s", payload.ReferenceID, referenceID)
	}
	if payload.DownloadAttempt == "" {
		t.Fatal("download attempt was not recorded")
	}
	stored := papers.references[referenceID]
	if stored.DownloadAttempt == nil || stored.DownloadAttempt.String() != payload.DownloadAttempt {
		t.Fatalf("reference download attempt = %v, payload = %q", stored.DownloadAttempt, payload.DownloadAttempt)
	}
	if stored.DownloadRequestedAt == nil {
		t.Fatal("reference download request time was not recorded")
	}
}

// TestRequestReferencePDFDownloadRejectsDuplicateInFlightRequest verifies that a
// second download request while one is in flight is rejected and does not enqueue
// another task.
func TestRequestReferencePDFDownloadRejectsDuplicateInFlightRequest(t *testing.T) {
	referenceID := uuid.New()
	repo := newPaperDocumentRepoStub()
	papers := newPaperInfoPaperServiceStub()
	papers.references[referenceID] = types.PaperReference{
		ReferenceID: referenceID, CitingDocumentID: uuid.New(), RawReference: "Downloadable reference",
		SourceURL:        "https://example.test/paper.pdf",
		ResolutionStatus: types.DocumentStageNotStarted, ResolutionGeneration: 1,
	}
	tasks := &documentTaskEnqueuerStub{}
	service := &documentService{repo: repo, papers: papers, taskEnqueuer: tasks}
	if err := service.RequestReferencePDFDownload(context.Background(), referenceID.String()); err != nil {
		t.Fatalf("first request: %v", err)
	}
	if err := service.RequestReferencePDFDownload(context.Background(), referenceID.String()); !errors.Is(err, types.ErrReferenceDownloadInProgress) {
		t.Fatalf("duplicate request error = %v, want ErrReferenceDownloadInProgress", err)
	}
	if len(tasks.tasks) != 1 {
		t.Fatalf("queued tasks = %d, want one", len(tasks.tasks))
	}
}

// TestImportRemoteDocumentRecordsTerminalDownloadFailure verifies that a remote
// import lacking a downloadable URL records a terminal, safe download error and
// skips retries.
func TestImportRemoteDocumentRecordsTerminalDownloadFailure(t *testing.T) {
	referenceID := uuid.New()
	attempt := uuid.New()
	papers := newPaperInfoPaperServiceStub()
	papers.references[referenceID] = types.PaperReference{
		ReferenceID: referenceID, CitingDocumentID: uuid.New(), RawReference: "No source URL",
		DownloadAttempt: &attempt,
	}
	service := &documentService{papers: papers, urlResolver: paperurl.NewResolver()}
	payload, err := json.Marshal(types.DocumentRemoteImportPayload{
		ReferenceID: referenceID.String(), Generation: 1, DownloadAttempt: attempt.String(),
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	err = service.ImportRemoteDocument(context.Background(), asynq.NewTask(types.TypeDocumentRemoteImport, payload))
	if !errors.Is(err, asynq.SkipRetry) {
		t.Fatalf("ImportRemoteDocument() error = %v, want SkipRetry", err)
	}
	stored := papers.references[referenceID]
	if stored.DownloadErrorCode == nil || *stored.DownloadErrorCode != types.ProcessingErrorServiceUnavailable {
		t.Fatalf("download error code = %v", stored.DownloadErrorCode)
	}
	if stored.DownloadErrorMessage == nil || *stored.DownloadErrorMessage != "reference has no downloadable PDF" {
		t.Fatalf("download error message = %v", stored.DownloadErrorMessage)
	}
}

// TestImportRemoteDocumentIgnoresStaleDownloadAttempt verifies that a remote-import
// payload for an older download attempt cannot overwrite the reference's current
// download state.
func TestImportRemoteDocumentIgnoresStaleDownloadAttempt(t *testing.T) {
	referenceID := uuid.New()
	currentAttempt := uuid.New()
	staleAttempt := uuid.New()
	papers := newPaperInfoPaperServiceStub()
	papers.references[referenceID] = types.PaperReference{
		ReferenceID: referenceID, CitingDocumentID: uuid.New(), RawReference: "No source URL",
		DownloadAttempt: &currentAttempt,
	}
	service := &documentService{papers: papers, urlResolver: paperurl.NewResolver()}
	payload, err := json.Marshal(types.DocumentRemoteImportPayload{
		ReferenceID: referenceID.String(), Generation: 1, DownloadAttempt: staleAttempt.String(),
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := service.ImportRemoteDocument(context.Background(), asynq.NewTask(types.TypeDocumentRemoteImport, payload)); err == nil {
		t.Fatal("ImportRemoteDocument() error = nil, want terminal failure")
	}
	if stored := papers.references[referenceID]; stored.DownloadErrorMessage != nil {
		t.Fatalf("stale attempt wrote download error: %v", stored.DownloadErrorMessage)
	}
}

func remotePDFTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	t.Setenv("SSRF_WHITELIST", "127.0.0.1")
	utils.ResetSSRFWhitelistForTest()
	t.Cleanup(utils.ResetSSRFWhitelistForTest)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/pdf")
		_, _ = writer.Write([]byte("%PDF-1.7\nremote paper"))
	}))
	t.Cleanup(server.Close)
	return server
}

// TestImportReferencePDFCreatesDocumentOnlyAfterDownload verifies that a successful
// reference-PDF download creates an available source document, links the reference,
// and queues recognition only after the file is stored.
func TestImportReferencePDFCreatesDocumentOnlyAfterDownload(t *testing.T) {
	server := remotePDFTestServer(t)
	repo := newPaperDocumentRepoStub()
	storage, err := fileservice.NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	tasks := &documentTaskEnqueuerStub{}
	papers := newPaperInfoPaperServiceStub()
	referenceID := uuid.New()
	papers.references[referenceID] = types.PaperReference{
		ReferenceID: referenceID, CitingDocumentID: uuid.New(), RawReference: "Downloadable reference",
		Title: "Downloadable reference", SourceURL: server.URL + "/paper.pdf",
		ResolutionStatus: types.DocumentStageNotStarted, ResolutionGeneration: 1,
	}
	service := &documentService{
		repo: repo, storage: storage, taskEnqueuer: tasks,
		papers: papers, urlResolver: paperurl.NewResolver(),
	}
	payload, err := json.Marshal(types.DocumentRemoteImportPayload{
		ReferenceID: referenceID.String(), Generation: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ImportRemoteDocument(context.Background(), asynq.NewTask(types.TypeDocumentRemoteImport, payload)); err != nil {
		t.Fatalf("ImportRemoteDocument() error = %v", err)
	}
	if len(repo.documents) != 1 {
		t.Fatalf("created documents = %d, want 1", len(repo.documents))
	}
	var created types.Document
	for _, document := range repo.documents {
		created = document
	}
	if created.SourceState != types.DocumentSourceAvailable || created.FileSHA256 == "" {
		t.Fatalf("created document = %#v", created)
	}
	linked := papers.references[referenceID]
	if linked.CitedDocumentID == nil || *linked.CitedDocumentID != created.ID {
		t.Fatalf("reference was not linked to the created document: %#v", linked)
	}
	if len(tasks.documents) != 1 || tasks.documents[0].DocumentID != created.ID.String() {
		t.Fatalf("recognition tasks = %#v", tasks.documents)
	}
}

// ---------------------------------------------------------------------------
// Consolidated from document_stage_stub_test.go.
// ---------------------------------------------------------------------------

func queueStubDocumentStage(document *types.Document, stage types.DocumentStage) (types.DocumentStageState, bool, error) {
	state, err := document.StageState(stage)
	if err != nil {
		return state, false, err
	}
	if state.Status == "" {
		state.Status = types.DocumentStageNotStarted
	}
	if state.Status.Active() {
		return state, false, nil
	}
	state.Status = types.DocumentStageQueued
	state.Generation++
	state.UpdatedAt = time.Now().UTC()
	state.ErrorCode, state.ErrorMessage = nil, nil
	setStubDocumentStage(document, state)
	return state, true, nil
}

func transitionStubDocumentStage(document *types.Document, stage types.DocumentStage, generation int64, from []types.DocumentStageStatus, to types.DocumentStageStatus) (types.DocumentStageState, bool, error) {
	state, err := document.StageState(stage)
	if err != nil {
		return state, false, err
	}
	allowed := false
	for _, status := range from {
		allowed = allowed || state.Status == status
	}
	if state.Generation != generation || !allowed {
		return state, false, nil
	}
	state.Status, state.UpdatedAt = to, time.Now().UTC()
	state.ErrorCode, state.ErrorMessage = nil, nil
	setStubDocumentStage(document, state)
	return state, true, nil
}

func setStubDocumentStage(document *types.Document, state types.DocumentStageState) {
	switch state.Stage {
	case types.DocumentStageMarkdown:
		document.MarkdownStatus, document.MarkdownGeneration, document.MarkdownUpdatedAt = state.Status, state.Generation, state.UpdatedAt
		document.MarkdownErrorCode, document.MarkdownErrorMessage = state.ErrorCode, state.ErrorMessage
	case types.DocumentStageMetadataExtraction:
		document.MetadataExtractionStatus, document.MetadataExtractionGeneration, document.MetadataExtractionUpdatedAt = state.Status, state.Generation, state.UpdatedAt
		document.MetadataExtractionErrorCode, document.MetadataExtractionErrorMessage = state.ErrorCode, state.ErrorMessage
	case types.DocumentStageSourceLookup:
		document.SourceLookupStatus, document.SourceLookupGeneration, document.SourceLookupUpdatedAt = state.Status, state.Generation, state.UpdatedAt
		document.SourceLookupErrorCode, document.SourceLookupErrorMessage = state.ErrorCode, state.ErrorMessage
	case types.DocumentStageReferenceLookup:
		document.ReferenceLookupStatus, document.ReferenceLookupGeneration, document.ReferenceLookupUpdatedAt = state.Status, state.Generation, state.UpdatedAt
		document.ReferenceLookupErrorCode, document.ReferenceLookupErrorMessage = state.ErrorCode, state.ErrorMessage
	}
}

// ---------------------------------------------------------------------------
// Consolidated from document_stage_test.go.
// ---------------------------------------------------------------------------

// TestClassifyTaskError verifies the task disposition contract: transient failures
// retry, known terminal inputs skip, and stale or cancelled work is revoked, with
// an explicit skip marker taking precedence.
func TestClassifyTaskError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want TaskDisposition
	}{
		{name: "nil retries", err: nil, want: TaskDispositionRetry},
		{name: "catalog unavailable skips", err: ErrPaperInfoCatalogUnavailable, want: TaskDispositionSkip},
		{name: "missing match skips", err: ErrPaperCatalogNoMatch, want: TaskDispositionSkip},
		{name: "invalid payload skips", err: types.ErrInvalidPaperInfoTaskPayload, want: TaskDispositionSkip},
		{name: "unsupported recognizer skips", err: types.ErrUnsupportedRecognitionContentType, want: TaskDispositionSkip},
		{name: "grpc invalid argument skips", err: status.Error(codes.InvalidArgument, "bad input"), want: TaskDispositionSkip},
		{name: "grpc unavailable retries", err: status.Error(codes.Unavailable, "offline"), want: TaskDispositionRetry},
		{name: "database error retries", err: errors.New("database unavailable"), want: TaskDispositionRetry},
		{name: "stale work revokes", err: ErrStalePaperInfo, want: TaskDispositionRevoke},
		{name: "cancelled work revokes", err: context.Canceled, want: TaskDispositionRevoke},
		{name: "missing row revokes", err: sql.ErrNoRows, want: TaskDispositionRevoke},
		{name: "explicit skip wins", err: errors.Join(errors.New("x"), asynq.SkipRetry), want: TaskDispositionSkip},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ClassifyTaskError(test.err); got != test.want {
				t.Fatalf("ClassifyTaskError(%v) = %v, want %v", test.err, got, test.want)
			}
		})
	}
}

// TestFinishDocumentStageTaskMatchesRouterClassification verifies that stage finish
// handling matches router classification: terminal errors fail, retryable errors
// requeue, and stale generations leave the running stage untouched.
func TestFinishDocumentStageTaskMatchesRouterClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want types.DocumentStageStatus
	}{
		{name: "skip retry fails stage", err: ErrPaperInfoCatalogUnavailable, want: types.DocumentStageFailed},
		{name: "terminal grpc fails stage", err: status.Error(codes.InvalidArgument, "bad input"), want: types.DocumentStageFailed},
		{name: "retryable grpc requeues stage", err: status.Error(codes.Unavailable, "offline"), want: types.DocumentStageQueued},
		{name: "database error requeues stage", err: errors.New("database unavailable"), want: types.DocumentStageQueued},
		{name: "stale generation leaves stage running", err: ErrStalePaperInfo, want: types.DocumentStageRunning},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			documentID := uuid.New()
			repo := &documentStatusRepoStub{document: types.Document{
				ID: documentID, SourceState: types.DocumentSourceAvailable,
				MarkdownStatus: types.DocumentStageRunning, MarkdownGeneration: 1,
			}}
			finishDocumentStageTask(
				context.Background(), repo, documentID,
				types.DocumentStageMarkdown, 1, test.err,
			)
			if repo.document.MarkdownStatus != test.want {
				t.Fatalf("markdown status = %q, want %q", repo.document.MarkdownStatus, test.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Consolidated from document_structure_test.go.
// ---------------------------------------------------------------------------

// TestDocumentStructureRebuildsMissingCorruptAndStaleCache verifies that the
// structure cache is rebuilt on a miss, corruption, schema mismatch, stale
// generation, or removed cache file while preserving stable fingerprints where
// applicable.
func TestDocumentStructureRebuildsMissingCorruptAndStaleCache(t *testing.T) {
	documentID := uuid.New()
	cacheDir := t.TempDir()
	outlineJSON, err := (types.DocumentOutline{SchemaVersion: 1, Source: types.DocumentOutlineNone,
		Sections: []types.DocumentOutlineSection{}, Diagnostics: []string{"empty"}}).JSON()
	if err != nil {
		t.Fatal(err)
	}
	repository := &documentStatusRepoStub{
		document: types.Document{ID: documentID, FileSHA256: "abc", SourceState: types.DocumentSourceAvailable},
		markdown: types.DocumentMarkdown{DocumentID: documentID, StorageKey: "markdown/current.md",
			DocumentOutline: outlineJSON, RecognitionGeneration: 3},
		blocks: []types.Block{{ID: uuid.New(), DocumentID: documentID, Ordinal: 0, Type: "text"}},
	}
	service := NewDocumentStructureService(config.Config{DocumentStructureCacheDir: cacheDir,
		DocumentStructureCacheEntries: 2, DocumentStructureCacheMaxBytes: 1 << 20}, repository)

	first, err := service.GetDocumentReadingStructure(context.Background(), documentID.String())
	if err != nil {
		t.Fatalf("first structure read: %v", err)
	}
	cachePath := filepath.Join(cacheDir, documentID.String()+".json")
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("cache was not written: %v", err)
	}
	cached, err := service.GetDocumentReadingStructure(context.Background(), documentID.String())
	if err != nil || cached.Fingerprint != first.Fingerprint || cached.SchemaVersion != 6 {
		t.Fatalf("cache hit = %#v, error=%v", cached, err)
	}
	if err := os.WriteFile(cachePath, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := service.GetDocumentReadingStructure(context.Background(), documentID.String())
	if err != nil || rebuilt.Fingerprint != first.Fingerprint || rebuilt.SchemaVersion != 6 {
		t.Fatalf("corrupt cache rebuild = %#v, error=%v", rebuilt, err)
	}
	cacheData, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	cacheData = bytes.Replace(cacheData, []byte(`"schemaVersion":6`), []byte(`"schemaVersion":5`), 1)
	if err := os.WriteFile(cachePath, cacheData, 0o600); err != nil {
		t.Fatal(err)
	}
	upgraded, err := service.GetDocumentReadingStructure(context.Background(), documentID.String())
	if err != nil || upgraded.SchemaVersion != 6 {
		t.Fatalf("schema cache rebuild = %#v, error=%v", upgraded, err)
	}
	repository.markdown.RecognitionGeneration = 4
	staleRebuilt, err := service.GetDocumentReadingStructure(context.Background(), documentID.String())
	if err != nil {
		t.Fatalf("stale cache rebuild: %v", err)
	}
	if staleRebuilt.Fingerprint == first.Fingerprint || staleRebuilt.RecognitionGeneration != 4 {
		t.Fatalf("stale cache was reused: %#v", staleRebuilt)
	}
	if err := os.Remove(cachePath); err != nil {
		t.Fatal(err)
	}
	cacheLossRebuilt, err := service.GetDocumentReadingStructure(context.Background(), documentID.String())
	if err != nil || cacheLossRebuilt.Fingerprint != staleRebuilt.Fingerprint {
		t.Fatalf("cache loss rebuild = %#v, error=%v", cacheLossRebuilt, err)
	}
}

type blockingStructureRepository struct {
	*documentStatusRepoStub
	started chan struct{}
	release chan struct{}
	once    sync.Once
	calls   atomic.Int32
}

func (r *blockingStructureRepository) GetMarkdown(ctx context.Context, documentID uuid.UUID) (types.DocumentMarkdown, error) {
	r.calls.Add(1)
	r.once.Do(func() { close(r.started) })
	select {
	case <-ctx.Done():
		return types.DocumentMarkdown{}, ctx.Err()
	case <-r.release:
		return r.documentStatusRepoStub.GetMarkdown(ctx, documentID)
	}
}

// TestDocumentStructureOmitsRecognitionHintAfterSuccessfulMarkdown verifies that
// the recognition hint is omitted once markdown succeeds but retained while the
// markdown stage is still running.
func TestDocumentStructureOmitsRecognitionHintAfterSuccessfulMarkdown(t *testing.T) {
	documentID := uuid.New()
	outlineJSON, err := types.LegacyDocumentOutline().JSON()
	if err != nil {
		t.Fatal(err)
	}
	repository := &documentStatusRepoStub{
		document: types.Document{ID: documentID, FileSHA256: "abc", SourceState: types.DocumentSourceAvailable,
			MarkdownStatus: types.DocumentStageSucceeded},
		markdown: types.DocumentMarkdown{DocumentID: documentID, StorageKey: "markdown/current.md",
			DocumentOutline: outlineJSON, RecognitionGeneration: 1},
	}
	service := NewDocumentStructureService(config.Config{DocumentStructureCacheDir: t.TempDir(),
		DocumentStructureCacheEntries: 2, DocumentStructureCacheMaxBytes: 1 << 20}, repository)

	structure, err := service.GetDocumentReadingStructure(context.Background(), documentID.String())
	if err != nil {
		t.Fatalf("structure read: %v", err)
	}
	for _, diagnostic := range structure.Diagnostics {
		if diagnostic == "recognize_document_to_create_outline" {
			t.Fatalf("successful markdown still produced a recognition hint: %#v", structure.Diagnostics)
		}
	}

	repository.document.MarkdownStatus = types.DocumentStageRunning
	repository.markdown.RecognitionGeneration = 2
	running, err := service.GetDocumentReadingStructure(context.Background(), documentID.String())
	if err != nil {
		t.Fatalf("running structure read: %v", err)
	}
	found := false
	for _, diagnostic := range running.Diagnostics {
		if diagnostic == "recognize_document_to_create_outline" {
			found = true
		}
	}
	if !found {
		t.Fatalf("unfinished markdown should keep the recognition hint: %#v", running.Diagnostics)
	}
}

// TestDocumentStructureSingleflightKeepsCompileAfterFirstCallerCancels verifies
// that singleflight compilation continues after the first caller cancels so the
// remaining caller receives the shared result from one compile.
func TestDocumentStructureSingleflightKeepsCompileAfterFirstCallerCancels(t *testing.T) {
	documentID := uuid.New()
	outline, _ := (types.DocumentOutline{SchemaVersion: 1, Source: types.DocumentOutlineNone,
		Sections: []types.DocumentOutlineSection{}}).JSON()
	repository := &blockingStructureRepository{
		documentStatusRepoStub: &documentStatusRepoStub{
			document: types.Document{ID: documentID, FileSHA256: "abc", SourceState: types.DocumentSourceAvailable},
			markdown: types.DocumentMarkdown{DocumentID: documentID, StorageKey: "markdown/current.md",
				DocumentOutline: outline, RecognitionGeneration: 1},
		},
		started: make(chan struct{}), release: make(chan struct{}),
	}
	service := NewDocumentStructureService(config.Config{DocumentStructureCacheDir: t.TempDir(),
		DocumentStructureCacheEntries: 2, DocumentStructureCacheMaxBytes: 1 << 20}, repository)

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstResult := make(chan error, 1)
	go func() {
		_, err := service.GetDocumentReadingStructure(firstCtx, documentID.String())
		firstResult <- err
	}()
	<-repository.started

	secondResult := make(chan error, 1)
	secondInvoked := make(chan struct{})
	go func() {
		close(secondInvoked)
		_, err := service.GetDocumentReadingStructure(context.Background(), documentID.String())
		secondResult <- err
	}()
	<-secondInvoked
	select {
	case err := <-secondResult:
		t.Fatalf("second caller returned before shared compile was released: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	cancelFirst()
	if err := <-firstResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("first caller error = %v, want context.Canceled", err)
	}
	close(repository.release)
	if err := <-secondResult; err != nil {
		t.Fatalf("second caller did not receive shared result: %v", err)
	}
	if calls := repository.calls.Load(); calls != 1 {
		t.Fatalf("shared compile calls = %d, want 1", calls)
	}
}

// TestDocumentStructureResponseIsBounded verifies that a structure response
// exceeding the configured cache byte limit is rejected as too large.
func TestDocumentStructureResponseIsBounded(t *testing.T) {
	documentID := uuid.New()
	outline, _ := (types.DocumentOutline{SchemaVersion: 1, Source: types.DocumentOutlineNone,
		Sections: []types.DocumentOutlineSection{}}).JSON()
	repository := &documentStatusRepoStub{
		document: types.Document{ID: documentID, SourceState: types.DocumentSourceAvailable},
		markdown: types.DocumentMarkdown{DocumentID: documentID, StorageKey: "markdown/current.md", DocumentOutline: outline},
	}
	service := NewDocumentStructureService(config.Config{DocumentStructureCacheDir: t.TempDir(),
		DocumentStructureCacheEntries: 1, DocumentStructureCacheMaxBytes: 32}, repository)
	_, err := service.GetDocumentReadingStructure(context.Background(), documentID.String())
	if !errors.Is(err, ErrDocumentStructureTooLarge) {
		t.Fatalf("error = %v, want ErrDocumentStructureTooLarge", err)
	}
}

// TestDocumentStructureCacheWriteFailureIsNonFatal verifies that an unwritable
// structure cache does not fail the read and that a legacy outline is returned as
// unavailable.
func TestDocumentStructureCacheWriteFailureIsNonFatal(t *testing.T) {
	documentID := uuid.New()
	parent := t.TempDir()
	cachePath := filepath.Join(parent, "not-a-directory")
	if err := os.WriteFile(cachePath, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy, _ := types.LegacyDocumentOutline().JSON()
	repository := &documentStatusRepoStub{
		document: types.Document{ID: documentID, FileSHA256: "abc", SourceState: types.DocumentSourceAvailable},
		markdown: types.DocumentMarkdown{DocumentID: documentID, StorageKey: "markdown/legacy.md", DocumentOutline: legacy},
	}
	service := NewDocumentStructureService(config.Config{DocumentStructureCacheDir: cachePath,
		DocumentStructureCacheEntries: 2, DocumentStructureCacheMaxBytes: 1 << 20}, repository)
	structure, err := service.GetDocumentReadingStructure(context.Background(), documentID.String())
	if err != nil {
		t.Fatalf("structure read must survive cache failure: %v", err)
	}
	if structure.Outline.Source != types.DocumentOutlineUnavailable {
		t.Fatalf("legacy outline source = %q", structure.Outline.Source)
	}
}

// TestDocumentStructureStartupEvictsOldestCacheFiles verifies that structure
// service startup trims the cache to the configured entry count by removing the
// oldest files.
func TestDocumentStructureStartupEvictsOldestCacheFiles(t *testing.T) {
	cacheDir := t.TempDir()
	for index, name := range []string{"a.json", "b.json", "c.json"} {
		path := filepath.Join(cacheDir, name)
		if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		stamp := time.Unix(int64(index+1), 0)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	_ = NewDocumentStructureService(config.Config{DocumentStructureCacheDir: cacheDir,
		DocumentStructureCacheEntries: 2, DocumentStructureCacheMaxBytes: 1024}, &documentStatusRepoStub{})
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name() != "b.json" || entries[1].Name() != "c.json" {
		t.Fatalf("remaining cache files = %#v", entries)
	}
}
