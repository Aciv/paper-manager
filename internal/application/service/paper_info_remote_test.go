package service

import (
	"context"
	"encoding/json"
	"testing"

	"paperMaster/internal/config"
	"paperMaster/internal/types"

	"github.com/google/uuid"
)

type referenceDownloadCancelSpanStub struct {
	paperInfoSpanStub
	documentID uuid.UUID
	calls      int
}

func (stub *referenceDownloadCancelSpanStub) CancelReferenceDownloadRuns(
	_ context.Context, documentID uuid.UUID, _, _ string,
) ([]uuid.UUID, error) {
	stub.documentID = documentID
	stub.calls++
	return []uuid.UUID{uuid.New()}, nil
}

func TestDispatchReferenceDownloadsDeduplicatesAndSkipsStoredTargets(t *testing.T) {
	sourceID, unknownID, storedID := uuid.New(), uuid.New(), uuid.New()
	documents := newPaperDocumentRepoStub(
		types.Document{ID: sourceID, Status: types.DocumentStatusReady},
		types.Document{ID: unknownID, Status: types.DocumentStatusUnknown,
			ContentType: types.UnknownDocumentContentType, StorageKey: types.UnknownDocumentStorageScheme + unknownID.String()},
		types.Document{ID: storedID, Status: types.DocumentStatusReady, StorageKey: "pdf/stored.pdf"},
	)
	papers := newPaperInfoPaperServiceStub()
	papers.metadata[unknownID.String()] = types.PaperMetadata{
		DocumentID: unknownID, SourceURL: "https://arxiv.org/abs/2401.01234",
		Authors: []types.PaperAuthor{}, Tags: []string{}, Metadata: types.JSON(`{}`),
	}
	papers.citations[sourceID.String()] = []types.PaperCitation{
		{CitingDocumentID: sourceID, CitedDocumentID: unknownID, RawReference: "first"},
		{CitingDocumentID: sourceID, CitedDocumentID: unknownID, RawReference: "duplicate"},
		{CitingDocumentID: sourceID, CitedDocumentID: storedID, RawReference: "stored"},
	}
	tasks := &documentTaskEnqueuerStub{}
	service := &paperInfoService{
		cfg:       config.Config{RemotePDFDispatchLimit: 10, RemotePDFTaskMaxRetry: 2},
		documents: documents, papers: papers, tasks: tasks,
	}
	payload := types.PaperReferenceDownloadDispatchPayload{
		SourceDocumentID: sourceID.String(), ReferenceRunID: uuid.NewString(), Attempt: 1,
	}
	if err := service.DispatchReferenceDownloads(context.Background(), payload); err != nil {
		t.Fatalf("DispatchReferenceDownloads() error = %v", err)
	}
	if len(tasks.tasks) != 1 || tasks.tasks[0].Type() != types.TypeDocumentRemoteImport {
		t.Fatalf("tasks = %#v", tasks.tasks)
	}
	var remote types.DocumentRemoteImportPayload
	if err := json.Unmarshal(tasks.tasks[0].Payload(), &remote); err != nil {
		t.Fatal(err)
	}
	if remote.DocumentID != unknownID.String() {
		t.Fatalf("remote payload = %#v", remote)
	}
}

func TestDispatchReferenceDownloadsTraversesMoreThanOnePage(t *testing.T) {
	sourceID := uuid.New()
	documents := newPaperDocumentRepoStub(types.Document{ID: sourceID, Status: types.DocumentStatusReady})
	papers := newPaperInfoPaperServiceStub()
	const citationCount = 53
	for index := 0; index < citationCount; index++ {
		targetID := uuid.New()
		documents.documents[targetID] = types.Document{
			ID: targetID, Status: types.DocumentStatusUnknown,
			ContentType: types.UnknownDocumentContentType,
			StorageKey:  types.UnknownDocumentStorageScheme + targetID.String(),
		}
		papers.metadata[targetID.String()] = types.PaperMetadata{
			DocumentID: targetID,
			SourceURL:  "https://arxiv.org/abs/2401.01234",
		}
		papers.citations[sourceID.String()] = append(
			papers.citations[sourceID.String()],
			types.PaperCitation{CitingDocumentID: sourceID, CitedDocumentID: targetID},
		)
	}
	tasks := &documentTaskEnqueuerStub{}
	service := &paperInfoService{
		cfg: config.Config{
			RemotePDFDispatchLimit: citationCount,
			RemotePDFTaskMaxRetry:  2,
		},
		documents: documents,
		papers:    papers,
		tasks:     tasks,
	}
	payload := types.PaperReferenceDownloadDispatchPayload{
		SourceDocumentID: sourceID.String(),
		ReferenceRunID:   uuid.NewString(),
	}
	if err := service.DispatchReferenceDownloads(context.Background(), payload); err != nil {
		t.Fatalf("DispatchReferenceDownloads() error = %v", err)
	}
	if len(tasks.tasks) != citationCount {
		t.Fatalf("queued tasks = %d, want %d across multiple pages", len(tasks.tasks), citationCount)
	}
}

func TestDispatchReferenceDownloadsHonorsConfiguredLimit(t *testing.T) {
	sourceID := uuid.New()
	documents := newPaperDocumentRepoStub(types.Document{ID: sourceID, Status: types.DocumentStatusReady})
	papers := newPaperInfoPaperServiceStub()
	for index := 0; index < 5; index++ {
		targetID := uuid.New()
		documents.documents[targetID] = types.Document{
			ID: targetID, Status: types.DocumentStatusUnknown,
			ContentType: types.UnknownDocumentContentType,
			StorageKey:  types.UnknownDocumentStorageScheme + targetID.String(),
		}
		papers.metadata[targetID.String()] = types.PaperMetadata{
			DocumentID: targetID,
			SourceURL:  "https://arxiv.org/abs/2401.01234",
		}
		papers.citations[sourceID.String()] = append(papers.citations[sourceID.String()],
			types.PaperCitation{CitingDocumentID: sourceID, CitedDocumentID: targetID})
	}
	tasks := &documentTaskEnqueuerStub{}
	service := &paperInfoService{
		cfg: config.Config{
			RemotePDFDispatchLimit: 2,
			RemotePDFTaskMaxRetry:  2,
		},
		documents: documents,
		papers:    papers,
		tasks:     tasks,
	}
	if err := service.DispatchReferenceDownloads(context.Background(), types.PaperReferenceDownloadDispatchPayload{
		SourceDocumentID: sourceID.String(), ReferenceRunID: uuid.NewString(),
	}); err != nil {
		t.Fatalf("DispatchReferenceDownloads() error = %v", err)
	}
	if len(tasks.tasks) != 2 {
		t.Fatalf("queued tasks = %d, want configured limit 2", len(tasks.tasks))
	}
}

func TestCancelReferenceDownloadsCancelsOnlyAcquisitionRuns(t *testing.T) {
	documentID := uuid.New()
	spans := &referenceDownloadCancelSpanStub{}
	service := &paperInfoService{spans: spans}
	if err := service.CancelReferenceDownloads(context.Background(), documentID.String()); err != nil {
		t.Fatalf("CancelReferenceDownloads() error = %v", err)
	}
	if spans.calls != 1 || spans.documentID != documentID {
		t.Fatalf("cancel calls = %d document = %s", spans.calls, spans.documentID)
	}
}
