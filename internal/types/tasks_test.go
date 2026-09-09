package types

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestStagePayloadCommandsRequireGenerationFence(t *testing.T) {
	documentID := uuid.NewString()
	tests := []struct {
		name     string
		validate func() error
	}{
		{"metadata", func() error { return PaperMetadataExtractPayload{DocumentID: documentID}.Validate() }},
		{"source", func() error { return PaperCatalogResolvePayload{DocumentID: documentID}.Validate() }},
		{"reference", func() error { return PaperReferenceDispatchPayload{DocumentID: documentID}.Validate() }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.validate(); !errors.Is(err, ErrInvalidDocumentStageEnvelope) {
				t.Fatalf("Validate() error=%v", err)
			}
		})
	}
}

func TestStagePayloadCommandsDecodeDocumentStageAndGeneration(t *testing.T) {
	documentID := uuid.New()
	metadata, err := (PaperMetadataExtractPayload{DocumentID: documentID.String(), Stage: DocumentStageMetadataExtraction, Generation: 3}).Command()
	if err != nil || metadata.DocumentID != documentID || metadata.Generation != 3 {
		t.Fatalf("metadata Command()=%#v err=%v", metadata, err)
	}
	source, err := (PaperCatalogResolvePayload{DocumentID: documentID.String(), Stage: DocumentStageSourceLookup, Generation: 4}).Command()
	if err != nil || source.DocumentID != documentID || source.Generation != 4 {
		t.Fatalf("source Command()=%#v err=%v", source, err)
	}
	reference, err := (PaperReferenceDispatchPayload{DocumentID: documentID.String(), Stage: DocumentStageReferenceLookup, Generation: 5}).Command()
	if err != nil || reference.DocumentID != documentID || reference.Generation != 5 {
		t.Fatalf("reference Command()=%#v err=%v", reference, err)
	}
}
