package service

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	papersv1 "paperMaster/gen/papers/v1"
	fileservice "paperMaster/internal/application/service/file"
	"paperMaster/internal/infrastructure/docrecognition"
	"paperMaster/internal/types"
)

type recordingLiveRecognitionClient struct {
	papersv1.DocumentParserClient
	requests []*papersv1.RecognitionRequest
}

func (c *recordingLiveRecognitionClient) Recognize(ctx context.Context, request *papersv1.RecognitionRequest, options ...grpc.CallOption) (*papersv1.RecognitionResult, error) {
	c.requests = append(c.requests, request)
	return c.DocumentParserClient.Recognize(ctx, request, options...)
}

// scripts/check_recognition_storage_key.py owns the server and shared root.
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
