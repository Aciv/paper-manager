package types

import (
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
	// MaxPaperExtractionReferences bounds citation-row creation and follow-up
	// catalog calls from one malformed or adversarial extraction response.
	MaxPaperExtractionReferences = 2_000
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
	RequestID  string
	DocumentID uuid.UUID
	Stage      DocumentStage
	Generation int64
}

// Validate verifies identifiers and attempt information on the command.
func (command PaperMetadataExtractCommand) Validate() error {
	if command.DocumentID == uuid.Nil {
		return fmt.Errorf("%w: document_id is required", ErrInvalidPaperExtraction)
	}
	if command.Stage != "" || command.Generation != 0 {
		if err := ValidateDocumentStageEnvelope(command.Stage, command.Generation, DocumentStageMetadataExtraction); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidPaperExtraction, err)
		}
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

// PaperReferenceCandidate is an optional structured interpretation of one raw
// reference. RawReference remains the citation edge evidence and must also be
// present in PaperExtractionResult.References. Empty parsed fields mean the
// parser could not identify that field; they never erase stored metadata.
type PaperReferenceCandidate struct {
	RawReference  string   `json:"raw_reference"`
	Authors       []string `json:"authors,omitempty"`
	Title         string   `json:"title,omitempty"`
	Venue         string   `json:"venue,omitempty"`
	PublishedYear int      `json:"published_year,omitempty"`
	DOI           string   `json:"doi,omitempty"`
	ArXivID       string   `json:"arxiv_id,omitempty"`
	Volume        string   `json:"volume,omitempty"`
	Pages         string   `json:"pages,omitempty"`
	Confidence    float64  `json:"confidence"`
}

// Validate ensures the structured candidate is safe to apply to one citation
// row. Bibliographic fields are intentionally optional.
func (candidate PaperReferenceCandidate) Validate() error {
	if strings.TrimSpace(candidate.RawReference) == "" {
		return fmt.Errorf("%w: reference candidate raw_reference is required", ErrInvalidPaperExtraction)
	}
	if candidate.PublishedYear < 0 || candidate.PublishedYear > 9999 {
		return fmt.Errorf("%w: reference candidate published_year is invalid", ErrInvalidPaperExtraction)
	}
	if math.IsNaN(candidate.Confidence) || math.IsInf(candidate.Confidence, 0) ||
		candidate.Confidence < 0 || candidate.Confidence > 1 {
		return fmt.Errorf("%w: reference candidate confidence must be between 0 and 1", ErrInvalidPaperExtraction)
	}
	for index, author := range candidate.Authors {
		if strings.TrimSpace(author) == "" {
			return fmt.Errorf("%w: reference candidate author %d is empty", ErrInvalidPaperExtraction, index)
		}
	}
	return nil
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
	DocumentID uuid.UUID          `json:"document_id"`
	Markdown   []byte             `json:"-"`
	Blocks     []PaperSourceBlock `json:"blocks,omitempty"`
	// SourceStorageKey is an opaque, Go-created relative storage key. Python
	// resolves it under its shared upload root only in CRF/hybrid mode; Go does
	// not interpret PDF line layout.
	SourceStorageKey string `json:"source_storage_key,omitempty"`
}

// Validate checks request bounds and UTF-8.
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
	Title    *PaperTextCandidate  `json:"title,omitempty"`
	Authors  []PaperTextCandidate `json:"authors"`
	Abstract *PaperTextCandidate  `json:"abstract,omitempty"`
	// ReferencesDetected distinguishes a positively identified bibliography
	// section (including an empty one) from an unavailable/undetected section.
	// Only the former may reconcile automatic citation edges.
	ReferencesDetected  bool                      `json:"references_detected"`
	References          []string                  `json:"references"`
	ReferenceCandidates []PaperReferenceCandidate `json:"reference_candidates,omitempty"`
	ExtractorName       string                    `json:"extractor_name"`
	ExtractorVersion    string                    `json:"extractor_version,omitempty"`
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
	if len(result.References) > MaxPaperExtractionReferences {
		return fmt.Errorf(
			"%w: reference count exceeds %d",
			ErrInvalidPaperExtraction,
			MaxPaperExtractionReferences,
		)
	}
	if !result.ReferencesDetected && (len(result.References) > 0 || len(result.ReferenceCandidates) > 0) {
		return fmt.Errorf("%w: references require references_detected", ErrInvalidPaperExtraction)
	}
	for index, reference := range result.References {
		if strings.TrimSpace(reference) == "" {
			return fmt.Errorf("%w: reference %d is empty", ErrInvalidPaperExtraction, index)
		}
	}
	references := make(map[string]struct{}, len(result.References))
	for _, reference := range result.References {
		references[compactPaperExtractionText(reference)] = struct{}{}
	}
	for index, candidate := range result.ReferenceCandidates {
		if err := candidate.Validate(); err != nil {
			return fmt.Errorf("%w: reference candidate %d: %v", ErrInvalidPaperExtraction, index, err)
		}
		if _, exists := references[compactPaperExtractionText(candidate.RawReference)]; !exists {
			return fmt.Errorf("%w: reference candidate %d has no matching raw reference", ErrInvalidPaperExtraction, index)
		}
	}
	return nil
}

func compactPaperExtractionText(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}
