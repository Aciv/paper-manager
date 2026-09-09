package service

import (
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	fileservice "paperMaster/internal/application/service/file"
	"paperMaster/internal/infrastructure/paperurl"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"
	"paperMaster/internal/utils"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

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

func (s *documentBatchRepoStub) MergeIntoCanonical(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
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
	if created == nil || created.ID == uuid.Nil || created.Status != types.DocumentStatusFailed {
		t.Fatalf("created = %#v, want durable failed document", created)
	}
	if created.ErrorMessage == nil || *created.ErrorMessage != "document processing was not queued; retry this document" {
		t.Fatalf("safe queue failure message = %#v", created.ErrorMessage)
	}
	opened, openErr := storage.Open(context.Background(), created.StorageKey)
	if openErr != nil {
		t.Fatalf("stored remote PDF must remain available for retry: %v", openErr)
	}
	_ = opened.Close()
}

func TestImportRemoteDocumentCleansDuplicateObjectAndRedirectsCitation(t *testing.T) {
	server := remotePDFTestServer(t)
	sourceID, placeholderID, canonicalID := uuid.New(), uuid.New(), uuid.New()
	repo := newPaperDocumentRepoStub(
		types.Document{ID: sourceID, Status: types.DocumentStatusReady},
		types.Document{ID: placeholderID, Status: types.DocumentStatusUnknown,
			ContentType: types.UnknownDocumentContentType, StorageKey: types.UnknownDocumentStorageScheme + placeholderID.String()},
		types.Document{ID: canonicalID, Status: types.DocumentStatusReady, FileSHA256: "canonical-hash", StorageKey: "pdf/canonical.pdf"},
	)
	repo.search = func(search types.DocumentSearch) (types.DocumentPage, error) {
		if search.FileSHA256 != "" {
			return types.DocumentPage{Items: []types.Document{{ID: canonicalID, Status: types.DocumentStatusReady, SourceState: types.DocumentSourceAvailable}}, Total: 1}, nil
		}
		return types.DocumentPage{}, nil
	}
	storageDir := t.TempDir()
	storage, err := fileservice.NewLocalStorage(storageDir, 1024)
	if err != nil {
		t.Fatal(err)
	}
	papers := newPaperInfoPaperServiceStub()
	papers.metadata[placeholderID.String()] = types.PaperMetadata{DocumentID: placeholderID,
		SourceURL: server.URL + "/paper.pdf", Authors: []types.PaperAuthor{}, Tags: []string{}, Metadata: types.JSON(`{}`)}
	papers.citations[sourceID.String()] = []types.PaperCitation{{CitingDocumentID: sourceID,
		CitedDocumentID: placeholderID, RawReference: "duplicate paper"}}
	service := &documentService{repo: repo, storage: storage, papers: papers, urlResolver: paperurl.NewResolver()}
	task, _, _ := NewDocumentRemoteImportTask(types.DocumentRemoteImportPayload{DocumentID: placeholderID.String(), Generation: 3}, 0, 0)
	if err := service.ImportRemoteDocument(context.Background(), task); err != nil {
		t.Fatalf("ImportRemoteDocument() error = %v", err)
	}
	if merged, _ := repo.Get(context.Background(), placeholderID); merged.SourceState != types.DocumentSourceMerged || merged.CanonicalDocumentID == nil || *merged.CanonicalDocumentID != canonicalID {
		t.Fatalf("merged document = %#v", merged)
	}
	entries, err := os.ReadDir(filepath.Join(storageDir, string(interfaces.StorageCategoryPDF)))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("orphaned PDF files = %#v", entries)
	}
}

func TestRemoteImportFailureDoesNotBlockAnotherDocument(t *testing.T) {
	server := remotePDFTestServer(t)
	badID, goodID := uuid.New(), uuid.New()
	repo := newPaperDocumentRepoStub(
		types.Document{ID: badID, Status: types.DocumentStatusUnknown, StorageKey: types.UnknownDocumentStorageScheme + badID.String()},
		types.Document{ID: goodID, Status: types.DocumentStatusUnknown, StorageKey: types.UnknownDocumentStorageScheme + goodID.String()},
	)
	storage, _ := fileservice.NewLocalStorage(t.TempDir(), 1024)
	papers := newPaperInfoPaperServiceStub()
	papers.metadata[badID.String()] = types.PaperMetadata{DocumentID: badID, SourceURL: server.URL + "/landing"}
	papers.metadata[goodID.String()] = types.PaperMetadata{DocumentID: goodID, SourceURL: server.URL + "/paper.pdf"}
	service := &documentService{repo: repo, storage: storage, papers: papers,
		taskEnqueuer: &documentTaskEnqueuerStub{}, urlResolver: paperurl.NewResolver()}
	badTask, _, _ := NewDocumentRemoteImportTask(types.DocumentRemoteImportPayload{DocumentID: badID.String(), Generation: 1}, 0, 0)
	if err := service.ImportRemoteDocument(context.Background(), badTask); !errors.Is(err, asynq.SkipRetry) {
		t.Fatalf("bad import error = %v, want SkipRetry", err)
	}
	goodTask, _, _ := NewDocumentRemoteImportTask(types.DocumentRemoteImportPayload{DocumentID: goodID.String(), Generation: 1}, 0, 0)
	if err := service.ImportRemoteDocument(context.Background(), goodTask); err != nil {
		t.Fatalf("good import error = %v", err)
	}
	if document, _ := repo.Get(context.Background(), goodID); document.SourceState != types.DocumentSourceAvailable || document.MarkdownStatus != types.DocumentStageQueued {
		t.Fatalf("good document = %#v", document)
	}
}

func TestImportRemoteDocumentPromotesUnknownAndQueuesExistingPipeline(t *testing.T) {
	server := remotePDFTestServer(t)
	documentID := uuid.New()
	repo := &documentStatusRepoStub{document: types.Document{
		ID: documentID, Status: types.DocumentStatusUnknown,
		StorageKey:  types.UnknownDocumentStorageScheme + documentID.String(),
		ContentType: types.UnknownDocumentContentType,
	}}
	storage, err := fileservice.NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	papers := newPaperInfoPaperServiceStub()
	papers.metadata[documentID.String()] = types.PaperMetadata{
		DocumentID: documentID, SourceURL: server.URL + "/paper.pdf",
		Authors: []types.PaperAuthor{}, Tags: []string{}, Metadata: types.JSON(`{}`),
	}
	tasks := &documentTaskEnqueuerStub{}
	service := &documentService{
		repo: repo, storage: storage, taskEnqueuer: tasks,
		papers: papers, urlResolver: paperurl.NewResolver(),
	}
	task, _, err := NewDocumentRemoteImportTask(types.DocumentRemoteImportPayload{
		DocumentID: documentID.String(), Generation: 2,
	}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ImportRemoteDocument(context.Background(), task); err != nil {
		t.Fatalf("ImportRemoteDocument() error = %v", err)
	}
	if repo.document.SourceState != types.DocumentSourceAvailable || repo.document.MarkdownStatus != types.DocumentStageQueued || repo.document.FileSHA256 == "" || repo.document.StorageKey == "" {
		t.Fatalf("promoted document = %#v", repo.document)
	}
	if len(tasks.documents) != 1 {
		t.Fatalf("queued tasks = %#v", tasks.documents)
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
