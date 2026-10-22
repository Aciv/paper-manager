package types

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	MaxDocumentReadingNoteBytes = 60 << 20
	MaxReadingNoteHeadings      = 256
	MaxReadingNoteOperations    = 128
	MaxReadingNoteEdits         = 128
	// Inline semantic edits are for small diffs. Whole-section and whole-note
	// content use the raw Markdown/file endpoints instead of large JSON strings.
	MaxReadingNoteInlineEditFieldBytes = 256 << 10
	MaxReadingNoteInlineEditTotalBytes = 1 << 20
)

// Reading-note operations are applied in order to one revision. Text offsets
// and lengths are Unicode code-point positions within the target section,
// relative to the content produced by earlier operations in the same sequence.
// Replace is not a primitive: clients send deleteText followed by insertText.
const (
	ReadingNoteOpInsertText    = "insertText"
	ReadingNoteOpDeleteText    = "deleteText"
	ReadingNoteOpAddSection    = "addSection"
	ReadingNoteOpDeleteSection = "deleteSection"
)

// Reading-note edits are the semantic layer for agents and automation. Every
// text edit in one request is resolved against the section snapshot at
// ExpectedRevision; text edits within one section must not overlap.
const (
	ReadingNoteEditOpReplaceText    = "replaceText"
	ReadingNoteEditOpReplaceRange   = "replaceRange"
	ReadingNoteEditOpInsertAt       = "insertAt"
	ReadingNoteEditOpAppendText     = "appendText"
	ReadingNoteEditOpReplaceSection = "replaceSection"
	ReadingNoteEditOpAddSection     = "addSection"
	ReadingNoteEditOpDeleteSection  = "deleteSection"
)

var (
	ErrInvalidReadingNote     = errors.New("invalid document reading note")
	ErrReadingNoteConflict    = errors.New("document reading note revision conflict")
	ErrReadingNoteUnavailable = errors.New("document reading note is temporarily unavailable")
)

// DocumentReadingNote is the sole durable, editable analysis artifact for one
// source document. Source structure and anchors deliberately do not live here.
type DocumentReadingNote struct {
	DocumentID uuid.UUID            `gorm:"column:document_id;type:uuid;primaryKey" json:"documentId"`
	Structure  JSON                 `gorm:"column:structure;type:jsonb;not null" json:"-"`
	Directory  ReadingNoteDirectory `gorm:"-" json:"directory"`
	SizeBytes  int64                `gorm:"column:size_bytes;not null" json:"sizeBytes"`
	Revision   int64                `gorm:"column:revision;not null" json:"revision"`
	CreatedAt  time.Time            `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt  time.Time            `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (DocumentReadingNote) TableName() string { return "document_reading_notes" }

type ReadingNoteDirectory struct {
	SchemaVersion int                       `json:"schemaVersion"`
	Sections      []ReadingNoteSectionEntry `json:"sections"`
}

type ReadingNoteSectionEntry struct {
	ID       uuid.UUID  `json:"id"`
	ParentID *uuid.UUID `json:"parentId,omitempty"`
	// Order is a sparse, monotonically increasing sort key. Structural edits
	// insert between neighbors and deletion leaves gaps; values are never
	// renumbered.
	Order           float64 `json:"order"`
	Level           int     `json:"level"`
	Title           string  `json:"title"`
	StorageKey      string  `json:"-"`
	StoredKey       string  `json:"storageKey,omitempty"`
	ContentRevision int64   `json:"contentRevision"`
	SizeBytes       int64   `json:"sizeBytes"`
}

func (n *DocumentReadingNote) SetDirectory(directory ReadingNoteDirectory) error {
	if err := ValidateReadingNoteDirectory(directory, n.SizeBytes); err != nil {
		return err
	}
	if err := validateReadingNoteStorageKeys(n.DocumentID, directory); err != nil {
		return err
	}
	directory.Sections = cloneReadingNoteSections(directory.Sections)
	persisted := directory
	persisted.Sections = cloneReadingNoteSections(directory.Sections)
	for index := range persisted.Sections {
		persisted.Sections[index].StoredKey = persisted.Sections[index].StorageKey
	}
	encoded, err := json.Marshal(persisted)
	if err != nil {
		return err
	}
	n.Structure = JSON(encoded)
	n.Directory = directory
	return nil
}

func (n *DocumentReadingNote) ParseDirectory() error {
	if err := json.Unmarshal(n.Structure, &n.Directory); err != nil {
		return fmt.Errorf("%w: invalid persisted directory", ErrInvalidReadingNote)
	}
	n.Directory.Sections = cloneReadingNoteSections(n.Directory.Sections)
	for index := range n.Directory.Sections {
		n.Directory.Sections[index].StorageKey = n.Directory.Sections[index].StoredKey
		n.Directory.Sections[index].StoredKey = ""
	}
	if err := ValidateReadingNoteDirectory(n.Directory, n.SizeBytes); err != nil {
		return err
	}
	return validateReadingNoteStorageKeys(n.DocumentID, n.Directory)
}

// cloneReadingNoteSections always returns a non-nil slice. An empty directory
// must serialize as [] rather than null so clients can iterate it directly.
func cloneReadingNoteSections(sections []ReadingNoteSectionEntry) []ReadingNoteSectionEntry {
	cloned := make([]ReadingNoteSectionEntry, len(sections))
	copy(cloned, sections)
	return cloned
}

func validateReadingNoteStorageKeys(documentID uuid.UUID, directory ReadingNoteDirectory) error {
	if documentID == uuid.Nil {
		return fmt.Errorf("%w: missing document id", ErrInvalidReadingNote)
	}
	for _, section := range directory.Sections {
		expected := "notes/" + documentID.String() + "/" + section.ID.String() + ".md"
		if section.StorageKey != expected {
			return fmt.Errorf("%w: section storage path does not match document", ErrInvalidReadingNote)
		}
	}
	return nil
}

func ValidateReadingNoteDirectory(directory ReadingNoteDirectory, totalSize int64) error {
	if directory.SchemaVersion != 1 || len(directory.Sections) > MaxReadingNoteHeadings || totalSize < 0 || totalSize > MaxDocumentReadingNoteBytes {
		return fmt.Errorf("%w: invalid section directory", ErrInvalidReadingNote)
	}
	// A deliberately empty note is a valid editing starting point. It has no
	// section files and therefore no bytes.
	if len(directory.Sections) == 0 {
		if totalSize != 0 {
			return fmt.Errorf("%w: empty note has non-zero size", ErrInvalidReadingNote)
		}
		return nil
	}
	seen := make(map[uuid.UUID]bool, len(directory.Sections))
	var size int64
	previousOrder := -1.0
	for index, section := range directory.Sections {
		if section.ID == uuid.Nil || seen[section.ID] || math.IsNaN(section.Order) || math.IsInf(section.Order, 0) ||
			section.Order < 0 || section.Order <= previousOrder || section.Level < 0 || section.Level > 3 ||
			section.Title == "" || section.StorageKey == "" || section.ContentRevision < 1 || section.SizeBytes <= 0 {
			return fmt.Errorf("%w: invalid section entry", ErrInvalidReadingNote)
		}
		previousOrder = section.Order
		if section.ParentID != nil && !seen[*section.ParentID] {
			return fmt.Errorf("%w: invalid section parent", ErrInvalidReadingNote)
		}
		if section.ParentID != nil {
			for _, parent := range directory.Sections[:index] {
				if parent.ID == *section.ParentID && (parent.Level == 0 || parent.Level >= section.Level) {
					return fmt.Errorf("%w: invalid section hierarchy", ErrInvalidReadingNote)
				}
			}
		}
		seen[section.ID] = true
		size += section.SizeBytes
	}
	if size != totalSize {
		return fmt.Errorf("%w: section size mismatch", ErrInvalidReadingNote)
	}
	return nil
}

func ValidateReadingNoteMarkdown(markdown string) error {
	if !utf8.ValidString(markdown) {
		return fmt.Errorf("%w: Markdown must be UTF-8", ErrInvalidReadingNote)
	}
	if len(markdown) > MaxDocumentReadingNoteBytes {
		return fmt.Errorf("%w: Markdown exceeds %d bytes", ErrInvalidReadingNote, MaxDocumentReadingNoteBytes)
	}
	// Empty is valid for a whole-note import/blank note. Section content still
	// flows through splitReadingNoteMarkdown and must contain a heading.
	return nil
}

// ReadingNoteSection is one section content read. Markdown is loaded from
// FileStorage only for the requested section.
type ReadingNoteSection struct {
	DocumentID uuid.UUID               `json:"documentId"`
	Revision   int64                   `json:"revision"`
	Section    ReadingNoteSectionEntry `json:"section"`
	Markdown   string                  `json:"markdown"`
	LineCount  int                     `json:"lineCount"`
}

// ReadingNoteExport is the explicit full-note view. It is the only place where
// all section files are concatenated into one Markdown document.
type ReadingNoteExport struct {
	DocumentID uuid.UUID `json:"documentId"`
	Revision   int64     `json:"revision"`
	Markdown   string    `json:"markdown"`
}

// ReadingNoteOperation is one edit in a request-scoped operation sequence.
// insertText and deleteText address one section with Unicode code-point offsets
// relative to the content produced by earlier operations in the same sequence.
// Replace is composed as deleteText followed by insertText.
type ReadingNoteOperation struct {
	Op        string     `json:"op"`
	SectionID *uuid.UUID `json:"sectionId,omitempty"`
	ParentID  *uuid.UUID `json:"parentId,omitempty"`
	AfterID   *uuid.UUID `json:"afterId,omitempty"`
	Offset    int        `json:"offset,omitempty"`
	Length    int        `json:"length,omitempty"`
	Text      string     `json:"text,omitempty"`
	Markdown  string     `json:"markdown,omitempty"`
}

type ReadingNoteOperations struct {
	ExpectedRevision int64                  `json:"expectedRevision"`
	Operations       []ReadingNoteOperation `json:"operations"`
}

// ReadingNotePosition is a section-local text boundary. Line and column are
// 1-based; column 1 is the boundary before the first code point and columns
// count Unicode code points. Ranges are end-exclusive. For range starts an
// omitted column means the start of the line; for range ends it means the end
// of the line (before the line terminator). Line == lineCount+1 addresses the
// end of the section.
type ReadingNotePosition struct {
	Line   int `json:"line"`
	Column int `json:"column,omitempty"`
}

// ReadingNoteEdit is one semantic edit for the agent-facing layer. Either a
// text range or a section structure edit is expressed per entry; the service
// resolves it against the revision snapshot before producing primitive
// insertText/deleteText operations. Content is op-defined: replacement or
// insertion text for range edits, and a complete section Markdown fragment for
// replaceSection/addSection.
type ReadingNoteEdit struct {
	Op        string               `json:"op"`
	SectionID *uuid.UUID           `json:"sectionId,omitempty"`
	ParentID  *uuid.UUID           `json:"parentId,omitempty"`
	AfterID   *uuid.UUID           `json:"afterId,omitempty"`
	OldText   string               `json:"oldText,omitempty"`
	NewText   string               `json:"newText,omitempty"`
	Content   string               `json:"content,omitempty"`
	Start     *ReadingNotePosition `json:"start,omitempty"`
	End       *ReadingNotePosition `json:"end,omitempty"`
	Position  *ReadingNotePosition `json:"position,omitempty"`
	LineStart int                  `json:"lineStart,omitempty"`
	LineEnd   int                  `json:"lineEnd,omitempty"`
}

type ReadingNoteEdits struct {
	ExpectedRevision int64             `json:"expectedRevision"`
	Edits            []ReadingNoteEdit `json:"edits"`
}

type ReadingNoteEditResult struct {
	DocumentID        uuid.UUID                 `json:"documentId"`
	Revision          int64                     `json:"revision"`
	Directory         ReadingNoteDirectory      `json:"directory"`
	ChangedSections   []ReadingNoteSectionEntry `json:"changedSections"`
	DeletedSectionIDs []uuid.UUID               `json:"deletedSectionIds,omitempty"`
}
