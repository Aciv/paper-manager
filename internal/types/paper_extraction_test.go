package types

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"testing"

	"github.com/google/uuid"
)

func TestPaperExtractionRequestValidate(t *testing.T) {
	markdown := []byte("# Paper title\n\nAbstract text.\n")
	sum := sha256.Sum256(markdown)
	request := PaperExtractionRequest{
		DocumentID:     uuid.New(),
		MarkdownSHA256: hex.EncodeToString(sum[:]),
		Markdown:       markdown,
		Blocks: []PaperSourceBlock{
			{Ordinal: 0, Type: "text", Text: "Paper title"},
			{Ordinal: 1, Type: "text", Text: "Abstract text."},
		},
	}

	if err := request.Validate(); err != nil {
		t.Fatalf("valid extraction request rejected: %v", err)
	}
}

func TestPaperExtractionRequestRejectsStaleMarkdown(t *testing.T) {
	request := PaperExtractionRequest{
		DocumentID:     uuid.New(),
		MarkdownSHA256: validTestSHA256(),
		Markdown:       []byte("# different content"),
	}

	err := request.Validate()
	if !errors.Is(err, ErrInvalidPaperExtraction) {
		t.Fatalf("expected ErrInvalidPaperExtraction, got %v", err)
	}
}

func TestPaperExtractionRequestRejectsDuplicateBlockOrdinals(t *testing.T) {
	markdown := []byte("# Paper")
	sum := sha256.Sum256(markdown)
	request := PaperExtractionRequest{
		DocumentID:     uuid.New(),
		MarkdownSHA256: hex.EncodeToString(sum[:]),
		Markdown:       markdown,
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
		Authors: []PaperTextCandidate{{Value: "Ada Lovelace", Confidence: 0.8}},
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

func validTestSHA256() string {
	return "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
}
