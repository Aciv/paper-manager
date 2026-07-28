package types

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	// MaxPaperExtractionMarkdownBytes bounds one in-memory extraction request.
	MaxPaperExtractionMarkdownBytes = 32 << 20
	// MaxPaperExtractionBlocks prevents unbounded protobuf and model input.
	MaxPaperExtractionBlocks = 50_000
)

// ErrInvalidPaperExtraction indicates invalid or contradictory extractor data.
var ErrInvalidPaperExtraction = errors.New("invalid paper metadata extraction")

// PaperExtractionStatus records only the terminal result of Python candidate
// extraction. Queue lifecycle belongs to Asynq and is not persisted here.
type PaperExtractionStatus string

const (
	PaperExtractionStatusSuccess PaperExtractionStatus = "success"
	PaperExtractionStatusFailed  PaperExtractionStatus = "failed"
)

// IsValid reports whether status is a supported terminal result.
func (status PaperExtractionStatus) IsValid() bool {
	switch status {
	case PaperExtractionStatusSuccess, PaperExtractionStatusFailed:
		return true
	default:
		return false
	}
}

// PaperMetadataExtractCommand is the typed application command decoded from a
// queue payload. It carries identifiers only; the service reloads source data.
type PaperMetadataExtractCommand struct {
	RequestID      string
	DocumentID     uuid.UUID
	MarkdownSHA256 string
	Attempt        int
}

// Validate verifies identifiers and revision information on the command.
func (command PaperMetadataExtractCommand) Validate() error {
	if command.DocumentID == uuid.Nil {
		return fmt.Errorf("%w: document_id is required", ErrInvalidPaperExtraction)
	}
	if err := validateSHA256("markdown_sha256", command.MarkdownSHA256); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPaperExtraction, err)
	}
	if command.Attempt < 0 {
		return fmt.Errorf("%w: attempt cannot be negative", ErrInvalidPaperExtraction)
	}
	return nil
}

// PaperSourceBlock is the transport-safe subset of a persisted Block needed by
// deterministic rules and future layout-aware models. It excludes database
// IDs, private storage keys, URLs, and image bytes.
type PaperSourceBlock struct {
	Ordinal  int               `json:"ordinal"`
	Type     string            `json:"type"`
	Text     string            `json:"text"`
	Location *PDFBlockLocation `json:"location,omitempty"`
}

// PaperSourceEvidence links one extracted value back to its source region.
// Offsets are half-open Unicode code-point positions: [start, end).
type PaperSourceEvidence struct {
	BlockOrdinal           int `json:"block_ordinal"`
	PageNumber             int `json:"page_number"`
	MarkdownCodepointStart int `json:"markdown_codepoint_start"`
	MarkdownCodepointEnd   int `json:"markdown_codepoint_end"`
}

// Validate checks that evidence points to a possible persisted source region.
func (e PaperSourceEvidence) Validate() error {
	if e.BlockOrdinal < 0 {
		return fmt.Errorf("%w: evidence block ordinal cannot be negative", ErrInvalidPaperExtraction)
	}
	if e.PageNumber < 1 {
		return fmt.Errorf("%w: evidence page number must be positive", ErrInvalidPaperExtraction)
	}
	if e.MarkdownCodepointStart < 0 || e.MarkdownCodepointEnd < e.MarkdownCodepointStart {
		return fmt.Errorf("%w: invalid evidence Markdown range", ErrInvalidPaperExtraction)
	}
	return nil
}

// PaperTextCandidate is one Python-extracted textual value and its evidence.
// Confidence is normalized to [0,1].
type PaperTextCandidate struct {
	Value      string                `json:"value"`
	Confidence float64               `json:"confidence"`
	Evidence   []PaperSourceEvidence `json:"evidence,omitempty"`
}

// Validate checks a non-empty value, normalized confidence, and all evidence.
func (candidate PaperTextCandidate) Validate() error {
	if strings.TrimSpace(candidate.Value) == "" {
		return fmt.Errorf("%w: candidate value is required", ErrInvalidPaperExtraction)
	}
	if math.IsNaN(candidate.Confidence) ||
		math.IsInf(candidate.Confidence, 0) ||
		candidate.Confidence < 0 ||
		candidate.Confidence > 1 {
		return fmt.Errorf("%w: candidate confidence must be between 0 and 1", ErrInvalidPaperExtraction)
	}
	for index, evidence := range candidate.Evidence {
		if err := evidence.Validate(); err != nil {
			return fmt.Errorf("%w: evidence %d: %v", ErrInvalidPaperExtraction, index, err)
		}
	}
	return nil
}

// PaperExtractionRequest is the runtime-only request sent to Python. Markdown
// and Blocks are loaded by the worker and must never be serialized into Asynq.
type PaperExtractionRequest struct {
	DocumentID     uuid.UUID          `json:"document_id"`
	MarkdownSHA256 string             `json:"markdown_sha256"`
	Markdown       []byte             `json:"-"`
	Blocks         []PaperSourceBlock `json:"blocks,omitempty"`
}

// Validate checks request bounds, UTF-8, and that MarkdownSHA256 describes the
// exact supplied Markdown bytes.
func (request PaperExtractionRequest) Validate() error {
	if request.DocumentID == uuid.Nil {
		return fmt.Errorf("%w: document_id is required", ErrInvalidPaperExtraction)
	}
	if len(request.Markdown) == 0 {
		return fmt.Errorf("%w: Markdown is required", ErrInvalidPaperExtraction)
	}
	if len(request.Markdown) > MaxPaperExtractionMarkdownBytes {
		return fmt.Errorf("%w: Markdown exceeds %d bytes", ErrInvalidPaperExtraction, MaxPaperExtractionMarkdownBytes)
	}
	if !utf8.Valid(request.Markdown) {
		return fmt.Errorf("%w: Markdown must be valid UTF-8", ErrInvalidPaperExtraction)
	}
	if err := validateSHA256("markdown_sha256", request.MarkdownSHA256); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPaperExtraction, err)
	}
	sum := sha256.Sum256(request.Markdown)
	if !strings.EqualFold(strings.TrimSpace(request.MarkdownSHA256), hex.EncodeToString(sum[:])) {
		return fmt.Errorf("%w: Markdown SHA-256 does not match content", ErrInvalidPaperExtraction)
	}
	if len(request.Blocks) > MaxPaperExtractionBlocks {
		return fmt.Errorf("%w: block count exceeds %d", ErrInvalidPaperExtraction, MaxPaperExtractionBlocks)
	}
	markdownCodepoints := utf8.RuneCount(request.Markdown)
	ordinals := make(map[int]struct{}, len(request.Blocks))
	for index, block := range request.Blocks {
		if block.Ordinal < 0 {
			return fmt.Errorf("%w: block %d ordinal cannot be negative", ErrInvalidPaperExtraction, index)
		}
		if _, exists := ordinals[block.Ordinal]; exists {
			return fmt.Errorf("%w: duplicate block ordinal %d", ErrInvalidPaperExtraction, block.Ordinal)
		}
		ordinals[block.Ordinal] = struct{}{}
		if strings.TrimSpace(block.Type) == "" {
			return fmt.Errorf("%w: block %d type is required", ErrInvalidPaperExtraction, index)
		}
		if err := validatePaperSourceLocation(block.Location, markdownCodepoints); err != nil {
			return fmt.Errorf("%w: block %d: %v", ErrInvalidPaperExtraction, index, err)
		}
	}
	return nil
}

func validatePaperSourceLocation(location *PDFBlockLocation, markdownCodepoints int) error {
	if location == nil {
		return nil
	}
	if location.PageNumber == nil ||
		location.PageWidth == nil ||
		location.PageHeight == nil ||
		location.BBoxX0 == nil ||
		location.BBoxY0 == nil ||
		location.BBoxX1 == nil ||
		location.BBoxY1 == nil ||
		location.MarkdownCodepointStart == nil ||
		location.MarkdownCodepointEnd == nil {
		return errors.New("source location must be complete when present")
	}
	if *location.PageNumber < 1 {
		return errors.New("source location page number must be positive")
	}
	if !finitePaperExtractionFloat(*location.PageWidth) ||
		!finitePaperExtractionFloat(*location.PageHeight) ||
		*location.PageWidth <= 0 ||
		*location.PageHeight <= 0 {
		return errors.New("source location page dimensions must be positive")
	}
	if !finitePaperExtractionFloat(*location.BBoxX0) ||
		!finitePaperExtractionFloat(*location.BBoxY0) ||
		!finitePaperExtractionFloat(*location.BBoxX1) ||
		!finitePaperExtractionFloat(*location.BBoxY1) ||
		*location.BBoxX0 < 0 ||
		*location.BBoxY0 < 0 ||
		*location.BBoxX1 <= *location.BBoxX0 ||
		*location.BBoxY1 <= *location.BBoxY0 ||
		*location.BBoxX1 > *location.PageWidth ||
		*location.BBoxY1 > *location.PageHeight {
		return errors.New("source location bounding box is outside its page")
	}
	if *location.MarkdownCodepointStart < 0 ||
		*location.MarkdownCodepointEnd < *location.MarkdownCodepointStart ||
		*location.MarkdownCodepointEnd > markdownCodepoints {
		return errors.New("source location Markdown range is invalid")
	}
	return nil
}

func finitePaperExtractionFloat(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

// PaperExtractionResult contains parser candidates, not canonical database
// metadata. References deliberately remain raw strings for later processing.
type PaperExtractionResult struct {
	Title            *PaperTextCandidate  `json:"title,omitempty"`
	Authors          []PaperTextCandidate `json:"authors"`
	Abstract         *PaperTextCandidate  `json:"abstract,omitempty"`
	References       []string             `json:"references"`
	ExtractorName    string               `json:"extractor_name"`
	ExtractorVersion string               `json:"extractor_version,omitempty"`
}

// Validate verifies extractor identity and every returned candidate.
func (result PaperExtractionResult) Validate() error {
	if strings.TrimSpace(result.ExtractorName) == "" {
		return fmt.Errorf("%w: extractor_name is required", ErrInvalidPaperExtraction)
	}
	if result.Title != nil {
		if err := result.Title.Validate(); err != nil {
			return fmt.Errorf("%w: title: %v", ErrInvalidPaperExtraction, err)
		}
	}
	for index, author := range result.Authors {
		if err := author.Validate(); err != nil {
			return fmt.Errorf("%w: author %d: %v", ErrInvalidPaperExtraction, index, err)
		}
	}
	if result.Abstract != nil {
		if err := result.Abstract.Validate(); err != nil {
			return fmt.Errorf("%w: abstract: %v", ErrInvalidPaperExtraction, err)
		}
	}
	for index, reference := range result.References {
		if strings.TrimSpace(reference) == "" {
			return fmt.Errorf("%w: reference %d is empty", ErrInvalidPaperExtraction, index)
		}
	}
	return nil
}

func validateSHA256(fieldName, value string) error {
	clean := strings.TrimSpace(value)
	if len(clean) != sha256.Size*2 {
		return fmt.Errorf("%s must contain 64 hexadecimal characters", fieldName)
	}
	if _, err := hex.DecodeString(clean); err != nil {
		return fmt.Errorf("%s must be hexadecimal", fieldName)
	}
	return nil
}
