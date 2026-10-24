package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	fileservice "paperMaster/internal/application/service/file"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"
)

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
