package types

import (
	"errors"
	"github.com/google/uuid"
	"math"
	"testing"
)

// ---------------------------------------------------------------------------
// Consolidated from paper_catalog_author_test.go.
// ---------------------------------------------------------------------------

// The catalog lookup is keyed by DOI/title/source URL. Authors are optional
// matching hints, so a dirty/empty hint must not reject the whole query.
func TestPaperLookupQueryAllowsEmptyAuthorHints(t *testing.T) {
	query := PaperLookupQuery{
		Title:   "UICopilot: Automating UI Synthesis",
		Authors: []string{"", "  ", "Zhen Li"},
	}
	if err := query.Validate(); err != nil {
		t.Fatalf("query with empty author hints rejected: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Consolidated from paper_catalog_test.go.
// ---------------------------------------------------------------------------

// TestPaperLookupQueryValidateAndEffectiveLimit verifies that a valid lookup query
// is accepted and that its limit defaults only when the caller leaves it unset.
func TestPaperLookupQueryValidateAndEffectiveLimit(t *testing.T) {
	query := PaperLookupQuery{
		Title:   "A normalized paper title",
		Authors: []string{"Ada Lovelace"},
	}
	if err := query.Validate(); err != nil {
		t.Fatalf("valid catalog query rejected: %v", err)
	}
	if got := query.EffectiveLimit(); got != DefaultPaperCatalogLimit {
		t.Fatalf("EffectiveLimit() = %d, want %d", got, DefaultPaperCatalogLimit)
	}

	query.Limit = 5
	if got := query.EffectiveLimit(); got != 5 {
		t.Fatalf("EffectiveLimit() = %d, want 5", got)
	}
}

// TestPaperLookupQueryRequiresLookupIdentifier verifies that a query with no DOI,
// title, or source URL is rejected, while a source URL alone is sufficient.
func TestPaperLookupQueryRequiresLookupIdentifier(t *testing.T) {
	err := (PaperLookupQuery{}).Validate()
	if !errors.Is(err, ErrInvalidPaperCatalogData) {
		t.Fatalf("expected ErrInvalidPaperCatalogData, got %v", err)
	}

	query := PaperLookupQuery{SourceURL: "https://arxiv.org/abs/2401.01234"}
	if err := query.Validate(); err != nil {
		t.Fatalf("source URL query rejected: %v", err)
	}
}

// TestPaperCatalogCandidateValidate verifies that a complete candidate is accepted
// while malformed raw metadata or a NaN provider score is rejected.
func TestPaperCatalogCandidateValidate(t *testing.T) {
	candidate := PaperCatalogCandidate{
		Provider:      "openalex",
		ProviderID:    "W123",
		Title:         "A Paper",
		Authors:       []PaperAuthor{{Name: "Ada Lovelace"}},
		Tags:          []string{"computer science"},
		ProviderScore: 0.8,
		MatchScore:    0.9,
		RawMetadata:   JSON(`{"id":"W123"}`),
	}
	if err := candidate.Validate(); err != nil {
		t.Fatalf("valid catalog candidate rejected: %v", err)
	}

	candidate.RawMetadata = JSON(`{`)
	if err := candidate.Validate(); !errors.Is(err, ErrInvalidPaperCatalogData) {
		t.Fatalf("expected invalid raw metadata error, got %v", err)
	}

	candidate.RawMetadata = nil
	candidate.ProviderScore = math.NaN()
	if err := candidate.Validate(); !errors.Is(err, ErrInvalidPaperCatalogData) {
		t.Fatalf("expected invalid NaN score error, got %v", err)
	}
}

// TestPaperSourceQueryEnqueueCommandValidate verifies that a fenced source lookup
// command is accepted and that a nil document ID is rejected.
func TestPaperSourceQueryEnqueueCommandValidate(t *testing.T) {
	command := PaperSourceQueryEnqueueCommand{
		DocumentID: uuid.New(), Stage: DocumentStageSourceLookup, Generation: 1,
	}
	if err := command.Validate(); err != nil {
		t.Fatalf("valid catalog command rejected: %v", err)
	}

	command.DocumentID = uuid.Nil
	if err := command.Validate(); !errors.Is(err, ErrInvalidPaperCatalogData) {
		t.Fatalf("expected document id validation error, got %v", err)
	}
}

// TestPaperSourceQueryEnqueueCommandAllowsDirectInternalCallWithoutFence verifies
// that a direct source lookup needs only a document ID, leaving stage and generation optional.
func TestPaperSourceQueryEnqueueCommandAllowsDirectInternalCallWithoutFence(t *testing.T) {
	command := PaperSourceQueryEnqueueCommand{DocumentID: uuid.New()}
	if err := command.Validate(); err != nil {
		t.Fatalf("direct command rejected: %v", err)
	}
}

// TestPaperCatalogStatusIsValid verifies that success and failed catalog statuses
// are valid while an unknown status is rejected.
func TestPaperCatalogStatusIsValid(t *testing.T) {
	if !PaperCatalogStatusSuccess.IsValid() {
		t.Fatal("success catalog status should be valid")
	}
	if !PaperCatalogStatusFailed.IsValid() {
		t.Fatal("failed catalog status should be valid")
	}
	if PaperCatalogStatus("unknown").IsValid() {
		t.Fatal("unknown catalog status should be invalid")
	}
}

// ---------------------------------------------------------------------------
// Consolidated from paper_citation_test.go.
// ---------------------------------------------------------------------------

func testCitation(citingID, referenceID uuid.UUID, raw string) PaperCitation {
	return PaperCitation{
		PaperReference: PaperReference{
			ReferenceID:      referenceID,
			CitingDocumentID: citingID,
			RawReference:     raw,
			Authors:          []PaperAuthor{},
			ResolutionStatus: DocumentStageNotStarted,
			MetadataSource:   PaperMetadataSourceUnknown,
		},
	}
}

// TestPaperCitationValidate verifies that resolved and unresolved citations pass
// validation while missing identifiers, self-links, empty raw text, and negative
// bibliography order are rejected.
func TestPaperCitationValidate(t *testing.T) {
	citingID := uuid.New()
	citedID := uuid.New()
	referenceID := uuid.New()
	valid := testCitation(citingID, referenceID, "Example Author. Example Paper.")

	tests := []struct {
		name     string
		citation PaperCitation
		wantErr  error
	}{
		{name: "valid directional relation", citation: func() PaperCitation {
			citation := valid
			citation.CitedDocumentID = &citedID
			citation.ResolutionStatus = DocumentStageSucceeded
			return citation
		}()},
		{name: "unresolved reference is valid", citation: valid},
		{name: "missing reference", citation: func() PaperCitation {
			citation := valid
			citation.ReferenceID = uuid.Nil
			return citation
		}(), wantErr: ErrInvalidPaperCitation},
		{name: "self citation", citation: func() PaperCitation {
			citation := valid
			citation.CitedDocumentID = &citingID
			return citation
		}(), wantErr: ErrSelfPaperCitation},
		{name: "missing raw reference", citation: func() PaperCitation {
			citation := valid
			citation.RawReference = ""
			return citation
		}(), wantErr: ErrInvalidPaperReference},
		{name: "negative bibliography order", citation: func() PaperCitation {
			citation := valid
			order := -1
			citation.BibliographyOrder = &order
			return citation
		}(), wantErr: ErrInvalidPaperReference},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.citation.Validate()
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Validate() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

// TestPaperCitationTableName verifies that the citation model maps to the shared
// paper_citations table.
func TestPaperCitationTableName(t *testing.T) {
	if got := (PaperCitation{}).TableName(); got != "paper_citations" {
		t.Fatalf("TableName() = %q, want paper_citations", got)
	}
}

// TestPaperCitationValidateEndpointsDoesNotRequireRawReference verifies that
// endpoint validation accepts a linked citation without raw reference text.
func TestPaperCitationValidateEndpointsDoesNotRequireRawReference(t *testing.T) {
	citedID := uuid.New()
	citation := testCitation(uuid.New(), uuid.New(), "")
	citation.CitedDocumentID = &citedID
	if err := citation.ValidateEndpoints(); err != nil {
		t.Fatalf("ValidateEndpoints() error = %v", err)
	}
}

// TestPaperReferenceIdentityIsScopedToCitingPaper verifies that raw references
// normalized for whitespace share an identity within one citing paper but stay
// distinct when owned by different papers.
func TestPaperReferenceIdentityIsScopedToCitingPaper(t *testing.T) {
	citingID := uuid.New()
	otherCitingID := uuid.New()
	raw := "  Example   Author. Example Paper.  "
	first := PaperReferenceIDForRaw(citingID, raw)
	second := PaperReferenceIDForRaw(citingID, "Example Author. Example Paper.")
	if first != second {
		t.Fatalf("normalized raw reference identity changed: %s != %s", first, second)
	}
	if PaperReferenceIDForRaw(otherCitingID, raw) == first {
		t.Fatal("the same raw reference in another paper must have a distinct row identity")
	}
}

// ---------------------------------------------------------------------------
// Consolidated from paper_extraction_test.go.
// ---------------------------------------------------------------------------

// TestPaperExtractionRequestValidate verifies that a request with markdown and
// uniquely ordered blocks is accepted.
func TestPaperExtractionRequestValidate(t *testing.T) {
	markdown := []byte("# Paper title\n\nAbstract text.\n")
	request := PaperExtractionRequest{
		DocumentID: uuid.New(),
		Markdown:   markdown,
		Blocks: []PaperSourceBlock{
			{Ordinal: 0, Type: "text", Text: "Paper title"},
			{Ordinal: 1, Type: "text", Text: "Abstract text."},
		},
	}

	if err := request.Validate(); err != nil {
		t.Fatalf("valid extraction request rejected: %v", err)
	}
}

// TestPaperExtractionRequestRejectsEmptyMarkdown verifies that a request without
// Markdown is rejected with ErrInvalidPaperExtraction.
func TestPaperExtractionRequestRejectsEmptyMarkdown(t *testing.T) {
	request := PaperExtractionRequest{
		DocumentID: uuid.New(),
	}

	err := request.Validate()
	if !errors.Is(err, ErrInvalidPaperExtraction) {
		t.Fatalf("expected ErrInvalidPaperExtraction, got %v", err)
	}
}

// TestPaperExtractionRequestRejectsDuplicateBlockOrdinals verifies that repeated
// block ordinals are rejected so block evidence remains unambiguous.
func TestPaperExtractionRequestRejectsDuplicateBlockOrdinals(t *testing.T) {
	markdown := []byte("# Paper")
	request := PaperExtractionRequest{
		DocumentID: uuid.New(),
		Markdown:   markdown,
		Blocks: []PaperSourceBlock{
			{Ordinal: 1, Type: "text"},
			{Ordinal: 1, Type: "text"},
		},
	}

	err := request.Validate()
	if !errors.Is(err, ErrInvalidPaperExtraction) {
		t.Fatalf("expected duplicate ordinal validation error, got %v", err)
	}
}

// TestPaperExtractionResultValidate verifies that a well-formed result is accepted
// while confidence values outside [0,1] or NaN are rejected.
func TestPaperExtractionResultValidate(t *testing.T) {
	result := PaperExtractionResult{
		Title: &PaperTextCandidate{
			Value:      "A Paper",
			Confidence: 0.95,
			Evidence: []PaperSourceEvidence{{
				BlockOrdinal:           0,
				PageNumber:             1,
				MarkdownCodepointStart: 0,
				MarkdownCodepointEnd:   7,
			}},
		},
		Authors:            []PaperTextCandidate{{Value: "Ada Lovelace", Confidence: 0.8}},
		ReferencesDetected: true,
		References: []string{
			"- A. Author. A cited work. 2025.",
		},
		ExtractorName: "markdown-rules",
	}

	if err := result.Validate(); err != nil {
		t.Fatalf("valid extraction result rejected: %v", err)
	}

	result.Title.Confidence = 1.1
	if err := result.Validate(); !errors.Is(err, ErrInvalidPaperExtraction) {
		t.Fatalf("expected confidence validation error, got %v", err)
	}

	result.Title.Confidence = math.NaN()
	if err := result.Validate(); !errors.Is(err, ErrInvalidPaperExtraction) {
		t.Fatalf("expected non-finite confidence validation error, got %v", err)
	}
}

// TestPaperExtractionResultRejectsTooManyReferences verifies that exceeding the
// MaxPaperExtractionReferences cap fails validation.
func TestPaperExtractionResultRejectsTooManyReferences(t *testing.T) {
	result := PaperExtractionResult{
		ExtractorName:      "markdown-rules",
		ReferencesDetected: true,
		References:         make([]string, MaxPaperExtractionReferences+1),
	}
	for index := range result.References {
		result.References[index] = "reference"
	}
	if err := result.Validate(); !errors.Is(err, ErrInvalidPaperExtraction) {
		t.Fatalf("expected reference count validation error, got %v", err)
	}
}

// TestPaperExtractionResultRequiresDetectedBibliographyForReferences verifies that
// non-empty references require ReferencesDetected, while an empty bibliography is
// valid either way.
func TestPaperExtractionResultRequiresDetectedBibliographyForReferences(t *testing.T) {
	result := PaperExtractionResult{
		ExtractorName: "markdown-rules",
		References:    []string{"[1] A reference."},
	}
	if err := result.Validate(); !errors.Is(err, ErrInvalidPaperExtraction) {
		t.Fatalf("undetected bibliography error = %v, want invalid extraction", err)
	}

	result.References = nil
	if err := result.Validate(); err != nil {
		t.Fatalf("undetected empty bibliography should remain valid: %v", err)
	}
	result.ReferencesDetected = true
	if err := result.Validate(); err != nil {
		t.Fatalf("detected empty bibliography should remain valid: %v", err)
	}
}

// TestPaperExtractionStatusIsValid verifies that success and failed extraction
// statuses are valid while an unknown status is rejected.
func TestPaperExtractionStatusIsValid(t *testing.T) {
	if !PaperExtractionStatusSuccess.IsValid() {
		t.Fatal("success extraction status should be valid")
	}
	if !PaperExtractionStatusFailed.IsValid() {
		t.Fatal("failed extraction status should be valid")
	}
	if PaperExtractionStatus("unknown").IsValid() {
		t.Fatal("unknown extraction status should be invalid")
	}
}
