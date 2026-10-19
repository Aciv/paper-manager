package service

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"paperMaster/internal/types"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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
