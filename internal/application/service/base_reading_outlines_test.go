package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
)

type baseOutlineRepositoryStub struct {
	interfaces.DocumentBaseRepository
	rows  []types.BaseReadingOutlineRow
	total int64
	calls int
	err   error
}

func (s *baseOutlineRepositoryStub) ListReadingOutlines(
	context.Context, uuid.UUID, int, int,
) ([]types.BaseReadingOutlineRow, int64, error) {
	s.calls++
	return s.rows, s.total, s.err
}

func baseOutlineRow(t *testing.T, sections int) types.BaseReadingOutlineRow {
	t.Helper()
	id := uuid.New()
	generation, revision := int64(1), int64(3)
	row := types.BaseReadingOutlineRow{DocumentID: id, SourceState: types.DocumentSourceAvailable,
		MarkdownStatus: types.DocumentStageSucceeded, MarkdownGeneration: 1,
		SourceGeneration: &generation, NoteRevision: &revision}
	outline := types.DocumentOutline{SchemaVersion: 1, Source: types.DocumentOutlineNone,
		Sections: []types.DocumentOutlineSection{}}
	note := types.DocumentReadingNote{DocumentID: id}
	directory := types.ReadingNoteDirectory{SchemaVersion: 1, Sections: []types.ReadingNoteSectionEntry{}}
	for i := 0; i < sections; i++ {
		sectionID := uuid.New()
		outline.Source = types.DocumentOutlinePDF
		outline.Sections = append(outline.Sections, types.DocumentOutlineSection{
			Ordinal: i, Title: "Source", Level: 1, ParentOrdinal: -1})
		directory.Sections = append(directory.Sections, types.ReadingNoteSectionEntry{
			ID: sectionID, Title: "Notes", Order: float64(i),
			Level: 1, StorageKey: "notes/" + id.String() + "/" + sectionID.String() + ".md", ContentRevision: 1, SizeBytes: 8})
		note.SizeBytes += 8
	}
	if err := note.SetDirectory(directory); err != nil {
		t.Fatal(err)
	}
	data, err := outline.JSON()
	if err != nil {
		t.Fatal(err)
	}
	row.SourceOutlineJSON = string(data)
	row.NoteStructureJSON = string(note.Structure)
	row.NoteSizeBytes = note.SizeBytes
	return row
}

func TestBaseReadingOutlineBatchStatusesAndPrivacy(t *testing.T) {
	full := baseOutlineRow(t, 2)
	partial := baseOutlineRow(t, 2)
	partial.NoteRevision = nil
	missing := baseOutlineRow(t, 0)
	missing.MarkdownStatus = types.DocumentStageQueued
	missing.NoteRevision = nil
	for _, test := range []struct {
		name, status string
		rows         []types.BaseReadingOutlineRow
		want         types.BaseOutlineSummary
	}{
		{"all_succeeded", "succeeded", []types.BaseReadingOutlineRow{full}, types.BaseOutlineSummary{Total: 1, Succeeded: 1}},
		{"partial", "partial", []types.BaseReadingOutlineRow{full, partial, missing},
			types.BaseOutlineSummary{Total: 3, Succeeded: 1, Partial: 1, Failed: 1}},
		{"only_partial", "partial", []types.BaseReadingOutlineRow{partial}, types.BaseOutlineSummary{Total: 1, Partial: 1}},
		{"all_failed", "failed", []types.BaseReadingOutlineRow{missing}, types.BaseOutlineSummary{Total: 1, Failed: 1}},
		{"empty", "succeeded", nil, types.BaseOutlineSummary{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := &baseOutlineRepositoryStub{rows: test.rows, total: int64(len(test.rows))}
			service := NewDocumentBaseService(repo, nil)
			got, err := service.ListBaseReadingOutlines(context.Background(), uuid.NewString(), types.BaseReadingOutlinesQuery{})
			if err != nil || got.Status != test.status || got.Summary != test.want || repo.calls != 1 {
				t.Fatalf("status=%s summary=%+v calls=%d err=%v", got.Status, got.Summary, repo.calls, err)
			}
			encoded, _ := json.Marshal(got)
			if strings.Contains(string(encoded), "storageKey") || strings.Contains(string(encoded), "notes/") {
				t.Fatal("batch exposed private storage keys")
			}
			if got.Scope != "page" || got.HasMore {
				t.Fatal("invalid page scope")
			}
		})
	}
}

func TestBaseReadingOutlineSelectionTruncationAndFailures(t *testing.T) {
	row := baseOutlineRow(t, 2)
	repo := &baseOutlineRepositoryStub{rows: []types.BaseReadingOutlineRow{row}, total: 51}
	service := NewDocumentBaseService(repo, nil)
	id := uuid.NewString()
	got, err := service.ListBaseReadingOutlines(context.Background(), id, types.BaseReadingOutlinesQuery{SectionLimit: 1})
	if err != nil || got.Status != "partial" || !got.HasMore || got.Items[0].SourceOutline.Complete ||
		got.Items[0].NotesOutline.Complete || got.Items[0].NotesOutline.TotalSections != 2 {
		t.Fatalf("truncation: %+v %v", got, err)
	}
	for _, kind := range []string{"source", "notes"} {
		query := types.BaseReadingOutlinesQuery{OutlineType: kind}
		got, err = service.ListBaseReadingOutlines(context.Background(), id, query)
		if err != nil || got.Status != "succeeded" || (got.Items[0].SourceOutline == nil) != (kind == "notes") ||
			(got.Items[0].NotesOutline == nil) != (kind == "source") {
			t.Fatalf("selection %s: %+v %v", kind, got, err)
		}
	}
	empty := baseOutlineRow(t, 0)
	repo.rows = []types.BaseReadingOutlineRow{empty}
	got, err = service.ListBaseReadingOutlines(context.Background(), uuid.NewString(), types.BaseReadingOutlinesQuery{})
	if err != nil || got.Status != "succeeded" {
		t.Fatalf("valid empty outline failed: %+v %v", got, err)
	}
	row.NoteStructureJSON = `{"invalid":true}`
	row.SourceOutlineJSON = `"invalid"`
	repo.rows = []types.BaseReadingOutlineRow{row}
	got, err = service.ListBaseReadingOutlines(context.Background(), uuid.NewString(), types.BaseReadingOutlinesQuery{})
	if err != nil || got.Status != "failed" || got.Items[0].NotesOutline.State != "error" ||
		got.Items[0].SourceOutline.State != "error" {
		t.Fatalf("bad data: %+v %v", got, err)
	}
	repo.err = sql.ErrNoRows
	_, err = service.ListBaseReadingOutlines(context.Background(), id, types.BaseReadingOutlinesQuery{})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing base not propagated: %v", err)
	}
	repo.err = nil
	before := repo.calls
	_, err = service.ListBaseReadingOutlines(context.Background(), id, types.BaseReadingOutlinesQuery{PageSize: 51})
	if !errors.Is(err, ErrInvalidBaseOutlineQuery) || repo.calls != before {
		t.Fatalf("bad bound queried DB: %v", err)
	}
}
