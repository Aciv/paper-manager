package service

import (
	"context"
	"encoding/json"
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
