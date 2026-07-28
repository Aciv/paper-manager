package types

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestPaperMetadataExtractPayloadCommand(t *testing.T) {
	documentID := uuid.New()
	payload := PaperMetadataExtractPayload{
		RequestID:      " request-1 ",
		DocumentID:     documentID.String(),
		MarkdownSHA256: strings.ToUpper(validTestSHA256()),
		Attempt:        2,
	}

	command, err := payload.Command()
	if err != nil {
		t.Fatalf("convert valid extraction payload: %v", err)
	}
	if command.DocumentID != documentID {
		t.Fatalf("DocumentID = %s, want %s", command.DocumentID, documentID)
	}
	if command.RequestID != "request-1" {
		t.Fatalf("RequestID = %q, want trimmed value", command.RequestID)
	}
	if command.MarkdownSHA256 != validTestSHA256() {
		t.Fatalf("MarkdownSHA256 = %q, want normalized lowercase", command.MarkdownSHA256)
	}
	if err := command.Validate(); err != nil {
		t.Fatalf("converted extraction command is invalid: %v", err)
	}
}

func TestPaperMetadataExtractPayloadRejectsInvalidFields(t *testing.T) {
	tests := []PaperMetadataExtractPayload{
		{DocumentID: "not-a-uuid", MarkdownSHA256: validTestSHA256()},
		{DocumentID: uuid.NewString(), MarkdownSHA256: "short"},
		{DocumentID: uuid.NewString(), MarkdownSHA256: validTestSHA256(), Attempt: -1},
	}
	for index, payload := range tests {
		if err := payload.Validate(); !errors.Is(err, ErrInvalidPaperTaskPayload) {
			t.Errorf("case %d: expected ErrInvalidPaperTaskPayload, got %v", index, err)
		}
	}
}

func TestPaperCatalogResolvePayloadCommand(t *testing.T) {
	documentID := uuid.New()
	extractionID := uuid.New()
	payload := PaperCatalogResolvePayload{
		DocumentID:     documentID.String(),
		MarkdownSHA256: validTestSHA256(),
		ExtractionID:   extractionID.String(),
		Attempt:        1,
	}

	command, err := payload.Command()
	if err != nil {
		t.Fatalf("convert valid catalog payload: %v", err)
	}
	if command.DocumentID != documentID || command.ExtractionID != extractionID {
		t.Fatalf("unexpected command identifiers: %#v", command)
	}
	if err := command.Validate(); err != nil {
		t.Fatalf("converted catalog command is invalid: %v", err)
	}
}

func TestPaperCatalogResolvePayloadRequiresExtractionID(t *testing.T) {
	payload := PaperCatalogResolvePayload{
		DocumentID:     uuid.NewString(),
		MarkdownSHA256: validTestSHA256(),
	}
	if err := payload.Validate(); !errors.Is(err, ErrInvalidPaperTaskPayload) {
		t.Fatalf("expected ErrInvalidPaperTaskPayload, got %v", err)
	}
}
