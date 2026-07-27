package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	fileservice "paperMaster/internal/application/service/file"
	"paperMaster/internal/types"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

type documentTaskInspectorStub struct {
	calledWith string
}

func (s *documentTaskInspectorStub) CancelTasksForDocument(_ context.Context, documentID string) (int, int, error) {
	s.calledWith = documentID
	return 1, 0, nil
}

func (*documentTaskInspectorStub) HasQueuedTasksForDocument(context.Context, string) (bool, error) {
	return false, nil
}

type documentStatusRepoStub struct {
	document types.Document
	markdown types.DocumentMarkdown
	updated  bool
	deleted  bool
	blocks   []types.Block
}

func (s *documentStatusRepoStub) Create(_ context.Context, document types.Document) (types.Document, error) {
	s.document = document
	return document, nil
}

func (s *documentStatusRepoStub) Get(_ context.Context, id uuid.UUID) (types.Document, error) {
	if s.document.ID != id {
		return types.Document{}, nil
	}
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

func (s *documentStatusRepoStub) ReplaceParsedOutput(_ context.Context, markdown types.DocumentMarkdown, blocks []types.Block) error {
	if s.document.ID != markdown.DocumentID || s.document.Status != types.DocumentStatusProcessing {
		return types.ErrDocumentNotProcessing
	}
	s.markdown = markdown
	s.blocks = append([]types.Block(nil), blocks...)
	s.document.Status = types.DocumentStatusReady
	s.document.ErrorMessage = nil
	return nil
}

func (*documentStatusRepoStub) ListBlocks(context.Context, uuid.UUID, int) ([]types.Block, error) {
	return []types.Block{}, nil
}

func (s *documentStatusRepoStub) Delete(_ context.Context, id uuid.UUID) error {
	if s.document.ID != id {
		return sql.ErrNoRows
	}
	s.deleted = true
	return nil
}

func (s *documentStatusRepoStub) UpdateStatus(_ context.Context, id uuid.UUID, status types.DocumentStatus, message *string) error {
	if s.document.ID == id {
		s.document.Status, s.document.ErrorMessage, s.updated = status, message, true
	}
	return nil
}

func (s *documentStatusRepoStub) UpdateStatusIfCurrent(_ context.Context, id uuid.UUID, status types.DocumentStatus, message *string, expected []types.DocumentStatus) (bool, error) {
	if s.document.ID != id {
		return false, nil
	}
	for _, candidate := range expected {
		if s.document.Status == candidate {
			s.document.Status, s.document.ErrorMessage, s.updated = status, message, true
			return true, nil
		}
	}
	return false, nil
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

func TestFailDocumentIfProcessingDoesNotOverwriteReadyStatus(t *testing.T) {
	repo := &documentStatusRepoStub{document: types.Document{ID: uuid.New(), Status: types.DocumentStatusReady}}
	svc := NewDocumentService(repo, nil)
	updated, err := svc.FailDocumentIfProcessing(context.Background(), repo.document.ID.String(), "worker failed")
	if err != nil {
		t.Fatalf("FailDocumentIfProcessing() error = %v", err)
	}
	if updated || repo.document.Status != types.DocumentStatusReady {
		t.Fatalf("ready document was overwritten: updated=%v status=%s", updated, repo.document.Status)
	}
}

func TestSetDocumentStatusRejectsUnknownStatus(t *testing.T) {
	repo := &documentStatusRepoStub{document: types.Document{ID: uuid.New(), Status: types.DocumentStatusUploaded}}
	svc := NewDocumentService(repo, nil)
	if err := svc.SetDocumentStatus(context.Background(), repo.document.ID.String(), "corrupt", nil); err == nil {
		t.Fatal("SetDocumentStatus() accepted an invalid status")
	}
	if repo.updated {
		t.Fatal("repository was updated for an invalid status")
	}
}

func TestCancelDocumentParseMarksCancellableDocumentAndDequeuesTasks(t *testing.T) {
	repo := &documentStatusRepoStub{document: types.Document{ID: uuid.New(), Status: types.DocumentStatusProcessing}}
	inspector := &documentTaskInspectorStub{}
	svc := NewDocumentServiceWithTaskInspector(repo, nil, inspector)

	document, err := svc.CancelDocumentParse(context.Background(), repo.document.ID.String())
	if err != nil {
		t.Fatalf("CancelDocumentParse() error = %v", err)
	}
	if document.Status != types.DocumentStatusCancelled || repo.document.Status != types.DocumentStatusCancelled {
		t.Fatalf("document status = %q, want cancelled", document.Status)
	}
	if inspector.calledWith != repo.document.ID.String() {
		t.Fatalf("inspector called with %q, want %q", inspector.calledWith, repo.document.ID)
	}
}

func TestCancelDocumentParseRejectsFinishedDocument(t *testing.T) {
	repo := &documentStatusRepoStub{document: types.Document{ID: uuid.New(), Status: types.DocumentStatusReady}}
	svc := NewDocumentService(repo, nil)

	_, err := svc.CancelDocumentParse(context.Background(), repo.document.ID.String())
	if !errors.Is(err, ErrDocumentParseFinished) {
		t.Fatalf("CancelDocumentParse() error = %v, want ErrDocumentParseFinished", err)
	}
	if repo.updated {
		t.Fatal("finished document was unexpectedly updated")
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
		document: types.Document{ID: documentID, StorageKey: source.Key, Status: types.DocumentStatusReady},
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

type recognitionStub struct {
	result *types.RecognitionResult
	err    error
	onRead func()
}

func (s *recognitionStub) Read(context.Context, *types.RecognitionRequest) (*types.RecognitionResult, error) {
	if s.onRead != nil {
		s.onRead()
	}
	return s.result, s.err
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
		ContentType: "application/pdf", SizeBytes: source.Size, Status: types.DocumentStatusQueued,
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
	svc := &documentService{repo: repo, storage: storage, recognizer: recognizer}
	payload, err := json.Marshal(types.DocumentProcessPayload{DocumentID: documentID.String()})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := svc.ProcessDocument(context.Background(), asynq.NewTask(types.TypeDocumentProcess, payload)); err != nil {
		t.Fatalf("ProcessDocument() error = %v", err)
	}
	if repo.document.Status != types.DocumentStatusReady {
		t.Fatalf("document status = %q, want ready", repo.document.Status)
	}
	if repo.markdown.StorageKey == "" || len(repo.blocks) != 1 {
		t.Fatalf("persisted output = markdown=%+v blocks=%+v", repo.markdown, repo.blocks)
	}
	block := repo.blocks[0]
	if block.DocumentID != documentID || block.Location == nil || block.Location.PageNumber == nil || *block.Location.PageNumber != 1 {
		t.Fatalf("persisted block = %+v", block)
	}
	if _, err := storage.Open(context.Background(), repo.markdown.StorageKey); err != nil {
		t.Fatalf("stored markdown is missing: %v", err)
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
		ContentType: "application/pdf", SizeBytes: source.Size, Status: types.DocumentStatusQueued,
	}}
	svc := &documentService{
		repo: repo, storage: storage, recognitionOutputDir: outputRoot, maxRecognitionOutputSize: 1024,
		recognizer: &recognitionStub{result: &types.RecognitionResult{
			OutputMode: types.RecognitionOutputDirectory, PackagePath: "job-1",
			Metadata: types.RecognitionMetadata{MarkdownFilename: "paper.md", ContentType: "text/markdown", SizeBytes: int64(len(markdown)), ParserName: "external"},
		}},
	}
	payload, err := json.Marshal(types.DocumentProcessPayload{DocumentID: documentID.String()})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := svc.ProcessDocument(context.Background(), asynq.NewTask(types.TypeDocumentProcess, payload)); err != nil {
		t.Fatalf("ProcessDocument() error = %v", err)
	}
	if repo.document.Status != types.DocumentStatusReady {
		t.Fatalf("document status = %q, want ready", repo.document.Status)
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
		ContentType: "application/pdf", SizeBytes: source.Size, Status: types.DocumentStatusQueued,
	}}
	svc := &documentService{
		repo: repo, storage: storage,
		recognizer: &recognitionStub{
			result: &types.RecognitionResult{
				OutputMode: types.RecognitionOutputMarkdownBytes,
				Markdown:   []byte("# Late result\n"),
				Metadata:   types.RecognitionMetadata{MarkdownFilename: "late.md", ContentType: "text/markdown"},
			},
			onRead: func() { repo.document.Status = types.DocumentStatusCancelled },
		},
	}
	payload, err := json.Marshal(types.DocumentProcessPayload{DocumentID: documentID.String()})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := svc.ProcessDocument(context.Background(), asynq.NewTask(types.TypeDocumentProcess, payload)); err != nil {
		t.Fatalf("ProcessDocument() error = %v", err)
	}
	if repo.document.Status != types.DocumentStatusCancelled {
		t.Fatalf("document status = %q, want cancelled", repo.document.Status)
	}
	if repo.markdown.StorageKey != "" {
		t.Fatalf("Markdown was stored after cancellation: %+v", repo.markdown)
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
		ContentType: "application/pdf", SizeBytes: source.Size, Status: types.DocumentStatusQueued,
	}}
	svc := &documentService{
		repo: repo, storage: storage,
		recognizer: &recognitionStub{err: types.ErrUnsupportedRecognitionContentType},
	}
	payload, err := json.Marshal(types.DocumentProcessPayload{DocumentID: documentID.String()})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	err = svc.ProcessDocument(context.Background(), asynq.NewTask(types.TypeDocumentProcess, payload))
	if !errors.Is(err, asynq.SkipRetry) {
		t.Fatalf("ProcessDocument() error = %v, want SkipRetry", err)
	}
	if repo.document.Status != types.DocumentStatusFailed {
		t.Fatalf("document status = %q, want failed", repo.document.Status)
	}
}
