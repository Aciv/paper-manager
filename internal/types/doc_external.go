package types

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// ErrUnsupportedRecognitionContentType marks a deterministic recognizer
// capability mismatch (for example PDF sent to the local text recognizer).
// The document task treats it as terminal rather than wasting retry attempts.
var ErrUnsupportedRecognitionContentType = errors.New("document recognizer does not support content type")

// RecognitionInputMode describes how a worker supplies a source document to
// an external recognition service. Bytes are passed directly in-process;
// file-path and URL modes let the service fetch the source by reference.
type RecognitionInputMode string

const (
	RecognitionInputBytes    RecognitionInputMode = "bytes"
	RecognitionInputFilePath RecognitionInputMode = "file_path"
	RecognitionInputURL      RecognitionInputMode = "url"
)

// ErrInvalidRecognitionRequest indicates that a recognition request has no
// valid source, or includes source fields that contradict its selected mode.
var ErrInvalidRecognitionRequest = errors.New("invalid document recognition request")

// RecognitionRequest is the runtime input passed from a document-processing
// worker to an external recognition service. Content deliberately has no JSON
// representation: it must never be embedded in an Asynq payload. The worker
// loads the stored document after its task starts, then supplies bytes or a
// short-lived reference through this structure.
type RecognitionRequest struct {
	DocumentID       uuid.UUID            `json:"document_id"`
	OriginalFilename string               `json:"original_filename"`
	ContentType      string               `json:"content_type"`
	SizeBytes        int64                `json:"size_bytes"`
	InputMode        RecognitionInputMode `json:"input_mode"`

	// Content is populated only for RecognitionInputBytes. It is excluded from
	// JSON so it cannot accidentally be persisted in a task, log, or API body.
	Content []byte `json:"-"`
	// FilePath is populated only for RecognitionInputFilePath. It is an
	// already-authorized path supplied by storage code, never a client path.
	FilePath string `json:"file_path,omitempty"`
	// URL is populated only for RecognitionInputURL. Callers should create a
	// fresh, expiring URL for every processing attempt.
	URL string `json:"url,omitempty"`
}

// Validate checks that exactly one transport source matches InputMode. It
// validates the contract only; storage authorization and URL allow-listing are
// the responsibility of the worker and its storage implementation.
func (r RecognitionRequest) Validate() error {
	if r.DocumentID == uuid.Nil {
		return fmt.Errorf("%w: document_id is required", ErrInvalidRecognitionRequest)
	}
	if r.OriginalFilename == "" {
		return fmt.Errorf("%w: original_filename is required", ErrInvalidRecognitionRequest)
	}
	if r.SizeBytes < 0 {
		return fmt.Errorf("%w: size_bytes cannot be negative", ErrInvalidRecognitionRequest)
	}

	switch r.InputMode {
	case RecognitionInputBytes:
		if r.FilePath != "" || r.URL != "" {
			return fmt.Errorf("%w: bytes mode cannot include a file path or URL", ErrInvalidRecognitionRequest)
		}
		if int64(len(r.Content)) != r.SizeBytes {
			return fmt.Errorf("%w: bytes length does not match size_bytes", ErrInvalidRecognitionRequest)
		}
	case RecognitionInputFilePath:
		if r.Content != nil || r.URL != "" || strings.TrimSpace(r.FilePath) == "" {
			return fmt.Errorf("%w: file_path mode requires only file_path", ErrInvalidRecognitionRequest)
		}
	case RecognitionInputURL:
		if r.Content != nil || r.FilePath != "" || strings.TrimSpace(r.URL) == "" {
			return fmt.Errorf("%w: url mode requires only URL", ErrInvalidRecognitionRequest)
		}
	default:
		return fmt.Errorf("%w: unsupported input_mode %q", ErrInvalidRecognitionRequest, r.InputMode)
	}

	return nil
}

// RecognitionOutputMode describes how an external recognizer returns its
// generated Markdown artefacts. Directory mode is suited to a shared volume;
// markdown-bytes mode is for transports that return the Markdown directly.
type RecognitionOutputMode string

const (
	RecognitionOutputDirectory     RecognitionOutputMode = "directory"
	RecognitionOutputMarkdownBytes RecognitionOutputMode = "markdown_bytes"
)

// RecognitionMetadata describes the generated Markdown document independently
// of how its bytes were delivered. SHA256 is optional but lets callers verify
// a copied directory result or detect whether reprocessing changed Markdown.
type RecognitionMetadata struct {
	MarkdownFilename string `json:"markdown_filename"`
	ContentType      string `json:"content_type"`
	SizeBytes        int64  `json:"size_bytes"`
	SHA256           string `json:"sha256,omitempty"`
	Language         string `json:"language,omitempty"`
	PageCount        int    `json:"page_count,omitempty"`
	ParserName       string `json:"parser_name,omitempty"`
	ParserVersion    string `json:"parser_version,omitempty"`
}

// RecognitionBlock is a parser-owned source-map entry. It intentionally does
// not contain a database ID or DocumentID: the application assigns those when
// it durably writes the recognition result. Image and table blocks are not
// emitted until the recognizer can also provide a durable asset URL.
type RecognitionBlock struct {
	Ordinal  int              `json:"ordinal"`
	Type     string           `json:"type"`
	Text     string           `json:"text"`
	Location PDFBlockLocation `json:"location"`
}

// RecognitionResult is the raw output package from an external recognizer.
//
// In directory mode, PackagePath points to a worker-authorized directory that
// contains the Markdown file named by Metadata.MarkdownFilename. In
// markdown-bytes mode, Markdown contains the Markdown file itself and is
// deliberately excluded from JSON, logs, and Asynq payloads. The application
// service is responsible for durable storage and for converting Markdown into
// Blocks.
type RecognitionResult struct {
	OutputMode  RecognitionOutputMode `json:"output_mode"`
	PackagePath string                `json:"package_path,omitempty"`
	// Markdown is populated only in RecognitionOutputMarkdownBytes mode.
	Markdown []byte              `json:"-"`
	Metadata RecognitionMetadata `json:"metadata"`
	// Blocks is the parser-produced PDF-to-Markdown source map. It is never
	// sent in task payloads and is converted to persisted Blocks by the
	// document service.
	Blocks []RecognitionBlock `json:"blocks,omitempty"`
	// PaperMetadata is optional bibliographic data extracted alongside the
	// Markdown. It remains separate from the raw parser output package.
	PaperMetadata *PaperMetadata `json:"paper_metadata,omitempty"`
}
