package types

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	MaxDocumentReadingNoteBytes = 10 << 20
	MaxReadingNoteHeadings      = 256
	MaxReadingNoteOperations    = 128
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
	ID              uuid.UUID  `json:"id"`
	ParentID        *uuid.UUID `json:"parentId,omitempty"`
	Order           int        `json:"order"`
	Level           int        `json:"level"`
	Title           string     `json:"title"`
	StorageKey      string     `json:"-"`
	StoredKey       string     `json:"storageKey,omitempty"`
	ContentRevision int64      `json:"contentRevision"`
	SizeBytes       int64      `json:"sizeBytes"`
}

func (n *DocumentReadingNote) SetDirectory(directory ReadingNoteDirectory) error {
	if err := ValidateReadingNoteDirectory(directory, n.SizeBytes); err != nil {
		return err
	}
	if err := validateReadingNoteStorageKeys(n.DocumentID, directory); err != nil {
		return err
	}
	persisted := directory
	persisted.Sections = append([]ReadingNoteSectionEntry(nil), directory.Sections...)
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
	for index := range n.Directory.Sections {
		n.Directory.Sections[index].StorageKey = n.Directory.Sections[index].StoredKey
		n.Directory.Sections[index].StoredKey = ""
	}
	if err := ValidateReadingNoteDirectory(n.Directory, n.SizeBytes); err != nil {
		return err
	}
	return validateReadingNoteStorageKeys(n.DocumentID, n.Directory)
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
	if directory.SchemaVersion != 1 || len(directory.Sections) == 0 || len(directory.Sections) > MaxReadingNoteHeadings || totalSize <= 0 || totalSize > MaxDocumentReadingNoteBytes {
		return fmt.Errorf("%w: invalid section directory", ErrInvalidReadingNote)
	}
	seen := make(map[uuid.UUID]bool, len(directory.Sections))
	var size int64
	for index, section := range directory.Sections {
		if section.ID == uuid.Nil || seen[section.ID] || section.Order != index || section.Level < 0 || section.Level > 3 ||
			section.Title == "" || section.StorageKey == "" || section.ContentRevision < 1 || section.SizeBytes <= 0 {
			return fmt.Errorf("%w: invalid section entry", ErrInvalidReadingNote)
		}
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
	if len(markdown) == 0 {
		return fmt.Errorf("%w: Markdown cannot be empty; delete the note instead", ErrInvalidReadingNote)
	}
	return nil
}

// ReadingNoteSection is one section content read. Markdown is loaded from
// FileStorage only for the requested section.
type ReadingNoteSection struct {
	DocumentID uuid.UUID               `json:"documentId"`
	Revision   int64                   `json:"revision"`
	Section    ReadingNoteSectionEntry `json:"section"`
	Markdown   string                  `json:"markdown"`
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

type ReadingNoteEditResult struct {
	DocumentID        uuid.UUID                 `json:"documentId"`
	Revision          int64                     `json:"revision"`
	Directory         ReadingNoteDirectory      `json:"directory"`
	ChangedSections   []ReadingNoteSectionEntry `json:"changedSections"`
	DeletedSectionIDs []uuid.UUID               `json:"deletedSectionIds,omitempty"`
}
