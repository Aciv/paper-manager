package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	fileservice "paperMaster/internal/application/service/file"
	"paperMaster/internal/infrastructure/paperurl"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"
	"paperMaster/internal/utils"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

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
	if created.DOI != "10.1000/remote" || created.FileSHA256 == "" || created.Status != types.DocumentStatusQueued {
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
			return types.DocumentPage{Items: []types.Document{{ID: canonicalID, Status: types.DocumentStatusReady}}, Total: 1}, nil
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
	task, _, _ := NewDocumentRemoteImportTask(types.DocumentRemoteImportPayload{DocumentID: placeholderID.String(), Attempt: 3}, 0, 0)
	if err := service.ImportRemoteDocument(context.Background(), task); err != nil {
		t.Fatalf("ImportRemoteDocument() error = %v", err)
	}
	if len(papers.resolutions) != 1 || papers.resolutions[0].CitedDocumentID != canonicalID {
		t.Fatalf("resolutions = %#v", papers.resolutions)
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
	badTask, _, _ := NewDocumentRemoteImportTask(types.DocumentRemoteImportPayload{DocumentID: badID.String(), Attempt: 1}, 0, 0)
	if err := service.ImportRemoteDocument(context.Background(), badTask); !errors.Is(err, asynq.SkipRetry) {
		t.Fatalf("bad import error = %v, want SkipRetry", err)
	}
	goodTask, _, _ := NewDocumentRemoteImportTask(types.DocumentRemoteImportPayload{DocumentID: goodID.String(), Attempt: 1}, 0, 0)
	if err := service.ImportRemoteDocument(context.Background(), goodTask); err != nil {
		t.Fatalf("good import error = %v", err)
	}
	if document, _ := repo.Get(context.Background(), goodID); document.Status != types.DocumentStatusQueued {
		t.Fatalf("good document = %#v", document)
	}
}

type cancelledRemoteSpanStub struct{ paperInfoSpanStub }

func (*cancelledRemoteSpanStub) ScopeCancelled(context.Context, uuid.UUID, types.ProcessingSpanKind, types.ProcessingSpanScope, types.ProcessingSpanScopeID) (bool, error) {
	return true, nil
}

func TestImportRemoteDocumentStopsBeforeIOWhenRunCancelled(t *testing.T) {
	documentID := uuid.New()
	service := &documentService{spans: &cancelledRemoteSpanStub{}}
	task, _, _ := NewDocumentRemoteImportTask(types.DocumentRemoteImportPayload{DocumentID: documentID.String(), Attempt: 9}, 0, 0)
	if err := service.ImportRemoteDocument(context.Background(), task); !errors.Is(err, asynq.RevokeTask) {
		t.Fatalf("ImportRemoteDocument() error = %v, want RevokeTask", err)
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
		DocumentID: documentID.String(), Attempt: 2,
	}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ImportRemoteDocument(context.Background(), task); err != nil {
		t.Fatalf("ImportRemoteDocument() error = %v", err)
	}
	if repo.document.Status != types.DocumentStatusQueued || repo.document.FileSHA256 == "" || repo.document.StorageKey == "" {
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
