package types

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestPaperMetadataExtractPayloadCommand(t *testing.T) {
	documentID := uuid.New()
	payload := PaperMetadataExtractPayload{
		RequestID:  " request-1 ",
		DocumentID: documentID.String(),
		Attempt:    2,
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
	if err := command.Validate(); err != nil {
		t.Fatalf("converted extraction command is invalid: %v", err)
	}
}

func TestPaperMetadataExtractPayloadRejectsInvalidFields(t *testing.T) {
	tests := []PaperMetadataExtractPayload{
		{DocumentID: "not-a-uuid"},
		{DocumentID: uuid.NewString(), Attempt: -1},
	}
	for index, payload := range tests {
		if err := payload.Validate(); !errors.Is(err, ErrInvalidPaperInfoTaskPayload) {
			t.Errorf("case %d: expected ErrInvalidPaperInfoTaskPayload, got %v", index, err)
		}
	}
}

func TestPaperReferenceDispatchPayloadCommand(t *testing.T) {
	documentID, runID := uuid.New(), uuid.New()
	payload := PaperReferenceDispatchPayload{
		RequestID:      " request-1 ",
		DocumentID:     documentID.String(),
		Attempt:        2,
		ReferenceRunID: runID.String(),
	}
	command, err := payload.Command()
	if err != nil {
		t.Fatalf("convert valid reference dispatcher payload: %v", err)
	}
	if command.DocumentID != documentID || command.ReferenceRunID != runID || command.RequestID != "request-1" {
		t.Fatalf("dispatcher command = %#v", command)
	}
	if err := command.Validate(); err != nil {
		t.Fatalf("converted dispatcher command is invalid: %v", err)
	}
	if err := (PaperReferenceDispatchPayload{DocumentID: documentID.String()}).Validate(); !errors.Is(err, ErrInvalidPaperInfoTaskPayload) {
		t.Fatalf("dispatcher payload without run id error = %v", err)
	}
}

func TestPaperCatalogResolvePayloadCommand(t *testing.T) {
	documentID := uuid.New()
	payload := PaperCatalogResolvePayload{DocumentID: documentID.String()}

	command, err := payload.Command()
	if err != nil {
		t.Fatalf("convert valid catalog payload: %v", err)
	}
	if command.DocumentID != documentID {
		t.Fatalf("unexpected command identifiers: %#v", command)
	}
	if err := command.Validate(); err != nil {
		t.Fatalf("converted catalog command is invalid: %v", err)
	}
}

func TestPaperCatalogResolvePayloadRejectsInvalidDocumentID(t *testing.T) {
	payload := PaperCatalogResolvePayload{DocumentID: "invalid"}
	if err := payload.Validate(); !errors.Is(err, ErrInvalidPaperInfoTaskPayload) {
		t.Fatalf("expected ErrInvalidPaperInfoTaskPayload, got %v", err)
	}
}

func TestPaperCatalogResolvePayloadAllowsUnknownWithoutRevision(t *testing.T) {
	payload := PaperCatalogResolvePayload{DocumentID: uuid.NewString()}
	command, err := payload.Command()
	if err != nil {
		t.Fatalf("source-free catalog payload rejected: %v", err)
	}
	if command.IsReference {
		t.Fatal("source payload unexpectedly marked reference")
	}
}

func TestPaperCatalogResolvePayloadReferenceTarget(t *testing.T) {
	documentID := uuid.New()
	payload := PaperCatalogResolvePayload{DocumentID: documentID.String(), IsReference: true}
	command, err := payload.Command()
	if err != nil {
		t.Fatal(err)
	}
	if !command.IsReference || command.DocumentID != documentID {
		t.Fatalf("reference command = %#v", command)
	}
}
