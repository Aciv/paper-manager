package interfaces

import (
	"context"
	"errors"
	"io"
)

// FileUpload is a stream supplied by an HTTP handler or a background job.
// Filename is metadata only; storage implementations must not use it as a path.
type FileUpload struct {
	Filename    string
	ContentType string
	Content     io.Reader
	// Category selects a provider-neutral top-level storage directory. Empty
	// keeps backward-compatible unclassified storage for callers that do not
	// own a durable document artefact.
	Category StorageCategory
}

// StorageCategory separates source PDFs, parser-generated Markdown, editable
// reading notes, and rendered block images without exposing filesystem paths.
type StorageCategory string

const (
	StorageCategoryPDF      StorageCategory = "pdf"
	StorageCategoryMarkdown StorageCategory = "markdown"
	StorageCategoryNotes    StorageCategory = "notes"
	StorageCategoryImage    StorageCategory = "image"
)

// StoredFile is the durable, provider-neutral representation of an upload.
type StoredFile struct {
	Key         string
	Filename    string
	ContentType string
	Size        int64
	// SHA256 is the lowercase digest of the bytes consumed by Save. It is
	// optional for legacy/custom backends; upload orchestration falls back to
	// hashing the stored object only when this digest is absent.
	SHA256 string
}

// SaveFromURLRequest describes one already-resolved remote object. URL must be
// an absolute HTTP(S) URL; storage revalidates it and every redirect before
// opening a connection. Filename is metadata and must not be used as a path.
type SaveFromURLRequest struct {
	URL      string
	Filename string
	Category StorageCategory
}

// SaveFromURLResult includes the durable object and the final response URL so
// callers can persist canonical redirect information without trusting input.
type SaveFromURLResult struct {
	File     StoredFile
	FinalURL string
}

// FileStorage is implemented by local disk, object storage, or another durable
// backend.
type FileStorage interface {
	CheckConnectivity(ctx context.Context) error
	Save(ctx context.Context, upload FileUpload) (StoredFile, error)
	SaveBytes(ctx context.Context, filename string, content []byte) (StoredFile, error)
	SaveFromURL(ctx context.Context, request SaveFromURLRequest) (SaveFromURLResult, error)
	Copy(ctx context.Context, source StoredFile) (StoredFile, error)
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}

type TextEditKind string

const (
	TextEditInsert TextEditKind = "insert"
	TextEditDelete TextEditKind = "delete"
)

// TextEdit is applied relative to the content produced by earlier edits in the
// same sequence. Offset and Length are Unicode code-point positions.
type TextEdit struct {
	Kind   TextEditKind
	Offset int
	Length int
	Text   string
}

var ErrInvalidTextEdit = errors.New("invalid text edit")

// TextFileEditor is the file-layer primitive used by text-backed artifacts such
// as reading-note sections. Local and future OSS/mirror implementations apply
// the same edit semantics; a complete final content replacement must not expose
// partial writes to concurrent readers.
type TextFileEditor interface {
	ApplyTextEdits(content string, edits []TextEdit) (string, error)
	ReadText(ctx context.Context, key string, maxBytes int64) (string, error)
	ReplaceText(ctx context.Context, key, content string, maxBytes int64) (StoredFile, error)
	Delete(ctx context.Context, key string) error
}

// FileRangeStorage is an optional optimization for consumers that already
// have validated byte ranges. Implementations should avoid reading bytes before
// offset; callers fall back to FileStorage.Open when it is unavailable.
type FileRangeStorage interface {
	OpenRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error)
}
