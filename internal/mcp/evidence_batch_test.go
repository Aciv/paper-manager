package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"paperMaster/internal/config"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
)

type lightweightReading struct {
	interfaces.DocumentReadingService
	calls map[string]int
	count int
	query types.DocumentBlockQuery
}

func (r *lightweightReading) GetDocumentPageCount(_ context.Context, id string) (int, error) {
	r.calls[id]++
	if id == "00000000-0000-4000-8000-000000000000" {
		return 0, sql.ErrNoRows
	}
	return r.count, nil
}

func (r *lightweightReading) GetDocumentPageCounts(ctx context.Context, ids []string) (map[string]int, error) {
	counts := make(map[string]int)
	for _, id := range ids {
		count, err := r.GetDocumentPageCount(ctx, id)
		if err == nil {
			counts[id] = count
		}
	}
	return counts, nil
}

func (r *lightweightReading) QueryDocumentBlocks(
	_ context.Context, id string, q types.DocumentBlockQuery,
) (types.DocumentBlockPage, error) {
	r.query = q
	return types.DocumentBlockPage{
		DocumentID: id, RecognitionGeneration: 9, Status: "succeeded", Mode: q.Mode,
	}, nil
}

func TestEvidenceLightweightValidation(t *testing.T) {
	reader := &lightweightReading{calls: map[string]int{}, count: 5}
	server := &ToolServer{reading: reader}
	id := uuid.NewString()
	for _, test := range []struct {
		suffix         string
		valid, checked bool
	}{
		{"&block=" + uuid.NewString(), true, true},
		{"&block=wrong", false, false},
		{"&rect=0.7,0,0.2,1", false, false},
		{"&rect=NaN,0,1,1", false, false},
		{"&page=3", false, false},
		{"&section=%XX", false, false},
		{"#fragment", false, false},
	} {
		got, err := server.validateEvidence(
			context.Background(), id, "papers-manager://self/evidence?v=1&page=2"+test.suffix,
		)
		if err != nil || got.Valid != test.valid || got.RangeChecked != test.checked {
			t.Fatalf("%s: %+v %v", test.suffix, got, err)
		}
	}
	got, err := server.validateEvidence(context.Background(), id, "papers-manager://self/evidence?v=1&page=6")
	if err != nil || got.Valid || !got.RangeChecked {
		t.Fatalf("bounds: %+v %v", got, err)
	}
	reader.count = 0
	got, err = server.validateEvidence(context.Background(), id, "papers-manager://self/evidence?v=1&page=2")
	if err != nil || !got.Valid || got.RangeChecked || len(got.Diagnostics) == 0 {
		t.Fatalf("unknown: %+v %v", got, err)
	}
	// Embedded methods panic if full Markdown or block reads are attempted.
}

func TestEvidenceBatchSDKAndSectionQueries(t *testing.T) {
	reader := &lightweightReading{calls: map[string]int{}, count: 5}
	server, err := NewToolServer(
		config.Config{MCPAddress: "127.0.0.1:8090"}, nil, reader, nil, nil, nil, nil, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	client := connectTestClient(t, server)
	id := uuid.NewString()
	for _, test := range []struct {
		pages []int
		want  string
	}{
		{[]int{1, 2}, "succeeded"},
		{[]int{1, 6}, "partial"},
		{[]int{6, 7}, "failed"},
	} {
		items := []evidenceValidateInput{}
		for _, page := range test.pages {
			items = append(items, evidenceValidateInput{
				DocumentID: id, Href: "papers-manager://self/evidence?v=1&page=" + strconv.Itoa(page),
			})
		}
		before := reader.calls[id]
		result := callTool(t, client, "papers_manager_evidence_validate_many", map[string]any{"items": items})
		var envelope struct {
			OK   bool                `json:"ok"`
			Data evidenceBatchResult `json:"data"`
		}
		if err := json.Unmarshal([]byte(resultText(t, result)), &envelope); err != nil {
			t.Fatal(err)
		}
		if result.IsError || !envelope.OK || envelope.Data.Status != test.want {
			t.Fatalf("batch: %s", resultText(t, result))
		}
		if reader.calls[id]-before != 1 {
			t.Fatalf("metadata reads = %d", reader.calls[id]-before)
		}
	}
	result := callTool(t, client, "papers_manager_document_read_section", map[string]any{
		"document_id": id, "ordinal": 0, "mode": "range", "include_ids": true,
		"page_size": 10, "expected_generation": 9,
	})
	if result.IsError || reader.query.SectionOrdinal == nil || reader.query.Mode != "range" || !reader.query.IncludeIDs {
		t.Fatalf("section: %s %+v", resultText(t, result), reader.query)
	}
	result = callTool(t, client, "papers_manager_document_read_blocks", map[string]any{
		"document_id": id, "block_ids": []string{uuid.NewString()}, "expected_generation": 9,
	})
	if result.IsError || len(reader.query.BlockIDs) != 1 {
		t.Fatalf("blocks: %s", resultText(t, result))
	}
	result = callTool(t, client, "papers_manager_evidence_validate_many", map[string]any{
		"items": []evidenceValidateInput{
			{DocumentID: id, Href: "papers-manager://self/evidence?v=1&page=1"},
			{DocumentID: id, Href: "https://example.com"},
		},
	})
	if result.IsError || !strings.Contains(resultText(t, result), `"status":"partial"`) {
		t.Fatalf("invalid link must not discard valid items: %s", resultText(t, result))
	}
	items := []evidenceValidateInput{
		{DocumentID: id, Href: "https://example.com"},
		{DocumentID: "00000000-0000-4000-8000-000000000000", Href: "papers-manager://self/evidence?v=1&page=1"},
	}
	raw, _ := json.Marshal(map[string]any{"items": items})
	value, err := server.evidenceValidateMany(context.Background(), raw)
	if err != nil || value.(evidenceBatchResult).Status != "failed" {
		t.Fatalf("errors: %+v %v", value, err)
	}
	if value.(evidenceBatchResult).Items[1].Error.Code != "NOT_FOUND" {
		t.Fatalf("unsafe lookup error: %+v", value)
	}
	reader.count = 0
	items = []evidenceValidateInput{{DocumentID: id, Href: "papers-manager://self/evidence?v=1&page=1"}}
	raw, _ = json.Marshal(map[string]any{"items": items})
	value, err = server.evidenceValidateMany(context.Background(), raw)
	if err != nil || value.(evidenceBatchResult).Status != "partial" {
		t.Fatalf("unknown page range: %+v %v", value, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := server.evidenceValidateMany(ctx, raw); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	result = callTool(t, client, "papers_manager_evidence_validate_many", map[string]any{"items": []any{}})
	if !result.IsError || !strings.Contains(resultText(t, result), "INVALID_ARGUMENT") {
		t.Fatalf("empty batch: %s", resultText(t, result))
	}
}
