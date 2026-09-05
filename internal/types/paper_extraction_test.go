package types

import (
	"errors"
	"math"
	"testing"

	"github.com/google/uuid"
)

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

func TestPaperExtractionRequestRejectsEmptyMarkdown(t *testing.T) {
	request := PaperExtractionRequest{
		DocumentID: uuid.New(),
	}

	err := request.Validate()
	if !errors.Is(err, ErrInvalidPaperExtraction) {
		t.Fatalf("expected ErrInvalidPaperExtraction, got %v", err)
	}
}

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
