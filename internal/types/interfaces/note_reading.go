package interfaces

import (
	"context"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

type NoteReadingRepository interface {
	Get(ctx context.Context, documentID uuid.UUID) (types.DocumentReadingNote, error)
	Put(ctx context.Context, note types.DocumentReadingNote, expectedRevision int64) (types.DocumentReadingNote, error)
	Delete(ctx context.Context, documentID uuid.UUID, expectedRevision int64) (types.DocumentReadingNote, error)
}

// NoteReadingService owns the durable note aggregate: its revisioned
// section directory, section files, and primitive insert/delete operations.
// Evidence links are ordinary Markdown content; the backend neither indexes nor
// resolves them.
type NoteReadingService interface {
	GetManifest(ctx context.Context, documentID string) (types.DocumentReadingNote, error)
	ReadSection(ctx context.Context, documentID, sectionID string) (types.ReadingNoteSection, error)
	Export(ctx context.Context, documentID string) (types.ReadingNoteExport, error)
	ApplyOperations(ctx context.Context, documentID string, request types.ReadingNoteOperations) (types.ReadingNoteEditResult, error)
	ApplyEdits(ctx context.Context, documentID string, request types.ReadingNoteEdits) (types.ReadingNoteEditResult, error)
	ReplaceSection(ctx context.Context, documentID, sectionID string, expectedRevision int64, markdown string) (types.ReadingNoteEditResult, error)
	Put(ctx context.Context, documentID string, expectedRevision int64, markdown string) (types.DocumentReadingNote, error)
	Delete(ctx context.Context, documentID string, expectedRevision int64) error
}
