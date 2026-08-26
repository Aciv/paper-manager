package interfaces

import (
	"context"
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

// StorageCategory separates source PDFs, generated Markdown, and rendered
// block images without exposing filesystem paths to callers.
type StorageCategory string

const (
	StorageCategoryPDF      StorageCategory = "pdf"
	StorageCategoryMarkdown StorageCategory = "markdown"
	StorageCategoryImage    StorageCategory = "image"
)

// StoredFile is the durable, provider-neutral representation of an upload.
type StoredFile struct {
	Key         string
	Filename    string
	ContentType string
	Size        int64
	// SHA256 is the lowercase digest of the bytes consumed by Save. It is
	// optional for legacy/custom backends; upload orchestration computes its own
	// digest before persistence when duplicate detection requires it.
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
// backend. Save must consume the input stream without loading the whole file in memory.
type FileStorage interface {
	CheckConnectivity(ctx context.Context) error
	Save(ctx context.Context, upload FileUpload) (StoredFile, error)
	SaveBytes(ctx context.Context, filename string, content []byte) (StoredFile, error)
	SaveFromURL(ctx context.Context, request SaveFromURLRequest) (SaveFromURLResult, error)
	Copy(ctx context.Context, source StoredFile) (StoredFile, error)
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}
