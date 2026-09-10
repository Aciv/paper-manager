package service

import (
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	fileservice "paperMaster/internal/application/service/file"
	"paperMaster/internal/infrastructure/paperurl"
	"paperMaster/internal/types"
	"paperMaster/internal/utils"

	"github.com/google/uuid"
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

func TestRequestReferencePDFDownloadQueuesOnlyExplicitTarget(t *testing.T) {
	documentID := uuid.New()
	repo := newPaperDocumentRepoStub(types.Document{
		ID: documentID, SourceState: types.DocumentSourceMissing, SourceLookupGeneration: 2,
	})
	papers := newPaperInfoPaperServiceStub()
	papers.metadata[documentID.String()] = types.PaperMetadata{DocumentID: documentID, SourceURL: "https://example.test/paper.pdf"}
	tasks := &documentTaskEnqueuerStub{}
	service := &documentService{repo: repo, papers: papers, taskEnqueuer: tasks}
	if err := service.RequestReferencePDFDownload(context.Background(), documentID.String()); err != nil {
		t.Fatal(err)
	}
	if len(tasks.tasks) != 1 || tasks.tasks[0].Type() != types.TypeDocumentRemoteImport {
		t.Fatalf("queued tasks = %#v", tasks.tasks)
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
