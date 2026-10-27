package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"paperMaster/internal/config"
	"paperMaster/internal/types"

	"github.com/google/uuid"
)

func TestURLImportOptionsProgressRetryAndIdempotency(t *testing.T) {
	queue := &urlImportQueue{records: map[string]types.TaskRecord{}}
	commands := []types.DocumentURLImportCommand{
		{URL: "https://example.org/failed.pdf", Metadata: &types.PaperMetadataImport{
			PaperMetadataUpdate: types.PaperMetadataUpdate{Title: importString("Keep on retry")}},
			Processing: types.DocumentImportProcessing{SourceLookup: types.SourceLookupSkip}},
		{URL: "https://example.org/metadata-failed.pdf"},
	}
	calls := 0
	documents := &urlImportDocuments{create: func(
		_ context.Context, command types.DocumentURLImportCommand,
	) (*types.Document, error) {
		calls++
		if command.URL == commands[0].URL {
			if *command.Metadata.Title != "Keep on retry" || command.Processing.SourceLookup != types.SourceLookupSkip {
				t.Fatal("worker lost import options")
			}
			return nil, errors.New("download unavailable")
		}
		return &types.Document{ID: uuid.New(), ImportMetadata: &types.DocumentImportMetadataResult{
			Status: "failed", Error: "METADATA_APPLY_FAILED",
		}}, nil
	}}
	service := NewURLImportService(documents, nil, queue, queue, config.Config{}).(*urlImportService)
	requestID := uuid.NewString()
	job, err := service.EnqueueURLs(context.Background(), commands, "", requestID)
	if err != nil || calls != 0 {
		t.Fatalf("enqueue = %#v, %v, calls=%d", job, err, calls)
	}
	changed := append([]types.DocumentURLImportCommand(nil), commands...)
	changed[0].Processing.SourceLookup = types.SourceLookupAuto
	if _, err := service.EnqueueURLs(context.Background(), changed, "", requestID); !errors.Is(err, ErrURLImportRequestConflict) {
		t.Fatalf("changed processing reused request ID: %v", err)
	}
	var payload types.URLImportPayload
	_ = json.Unmarshal(queue.records[job.ID].Payload, &payload)
	if err := service.processURLImport(context.Background(), payload, progressWriter{write: func(data []byte) error {
		record := queue.records[job.ID]
		record.State, record.Result = "completed", append([]byte{}, data...)
		queue.records[job.ID] = record
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	result, err := service.GetURLImportTask(context.Background(), job.ID)
	if err != nil || result.State != "partial" || result.Summary.MetadataFailed != 1 ||
		len(result.RetryURLs) != 1 || *result.RetryURLs[0].Metadata.Title != "Keep on retry" ||
		result.RetryURLs[0].Processing.SourceLookup != types.SourceLookupSkip {
		t.Fatalf("progress/retry = %#v, %v", result, err)
	}
}

func TestURLImportOptionsPayloadLimit(t *testing.T) {
	authors := make([]types.PaperAuthor, 12)
	for index := range authors {
		authors[index] = types.PaperAuthor{Name: "A", Affiliation: strings.Repeat("a", 2000)}
	}
	commands := make([]types.DocumentURLImportCommand, 20)
	for index := range commands {
		commands[index] = types.DocumentURLImportCommand{
			URL: "https://example.org/paper.pdf", Metadata: &types.PaperMetadataImport{
				PaperMetadataUpdate: types.PaperMetadataUpdate{
					Title:    importString(strings.Repeat("t", 4096)),
					Abstract: importString(strings.Repeat("a", 32768)), Authors: &authors,
				},
			},
		}
	}
	queue := &urlImportQueue{records: map[string]types.TaskRecord{}}
	service := NewURLImportService(nil, nil, queue, queue, config.Config{})
	if _, err := service.EnqueueURLs(context.Background(), commands, "", uuid.NewString()); !errors.Is(err, ErrInvalidURLImport) || len(queue.records) != 0 {
		t.Fatalf("oversized payload reached Redis: %v, records=%d", err, len(queue.records))
	}
}
