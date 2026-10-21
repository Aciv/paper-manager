package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	fileservice "paperMaster/internal/application/service/file"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

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

func TestRetryDocumentParseRejectsActiveDocument(t *testing.T) {
	repo := &documentStatusRepoStub{document: types.Document{ID: uuid.New(), SourceState: types.DocumentSourceAvailable}}
	svc := &documentService{repo: repo, taskEnqueuer: &documentTaskEnqueuerStub{}}

	_, err := svc.RetryDocumentParse(context.Background(), repo.document.ID.String())
	if !errors.Is(err, ErrDocumentParseFinished) {
		t.Fatalf("RetryDocumentParse() error = %v, want ErrDocumentParseFinished", err)
	}
}

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

func TestNonAvailableDocumentHasNoSourceFile(t *testing.T) {
	documentID := uuid.New()
	repo := &documentStatusRepoStub{
		document: types.Document{
			ID:          documentID,
			StorageKey:  "source/" + documentID.String(),
			SourceState: types.DocumentSourceDeleting,
		},
	}
	svc := NewDocumentService(repo, nil)

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
	if _, err := storage.Open(context.Background(), repo.markdown.StorageKey); err != nil {
		t.Fatalf("stored markdown is missing: %v", err)
	}
	if !strings.HasPrefix(repo.markdown.StorageKey, "markdown/") {
		t.Fatalf("markdown storage key = %q, want markdown/ prefix", repo.markdown.StorageKey)
	}
	if len(documentTasks.extractions) != 1 || len(documentTasks.catalogs) != 0 {
		t.Fatalf("recognition handoff = extractions:%#v catalogs:%#v", documentTasks.extractions, documentTasks.catalogs)
	}
}

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
	if _, err := storage.Open(context.Background(), repo.blocks[0].AssetKey); err != nil {
		t.Fatalf("stored image is missing: %v", err)
	}
}

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
