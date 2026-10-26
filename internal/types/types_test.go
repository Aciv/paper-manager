package types

import (
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"math"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Consolidated from json_test.go.
// ---------------------------------------------------------------------------

// TestJSONMarshalsAsRawJSON verifies that a JSON value is emitted verbatim instead
// of being encoded as a quoted string or base64 bytes.
func TestJSONMarshalsAsRawJSON(t *testing.T) {
	encoded, err := json.Marshal(JSON(`{"read":true}`))
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if got, want := string(encoded), `{"read":true}`; got != want {
		t.Fatalf("Marshal() = %s, want %s", got, want)
	}
}

// ---------------------------------------------------------------------------
// Consolidated from note_reading_test.go.
// ---------------------------------------------------------------------------

// TestValidateReadingNoteMarkdownBoundsUTF8AndBytes verifies that valid UTF-8,
// including empty content, is accepted while invalid UTF-8 and oversize Markdown
// are rejected.
func TestValidateReadingNoteMarkdownBoundsUTF8AndBytes(t *testing.T) {
	if err := ValidateReadingNoteMarkdown("# 注释"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateReadingNoteMarkdown(""); err != nil {
		t.Fatalf("empty Markdown should be allowed for a blank note: %v", err)
	}
	if err := ValidateReadingNoteMarkdown(string([]byte{0xff})); !errors.Is(err, ErrInvalidReadingNote) {
		t.Fatalf("invalid UTF-8 error = %v", err)
	}
	tooLarge := make([]byte, MaxDocumentReadingNoteBytes+1)
	if err := ValidateReadingNoteMarkdown(string(tooLarge)); !errors.Is(err, ErrInvalidReadingNote) {
		t.Fatalf("oversize error = %v", err)
	}
}

// TestEmptyReadingNoteDirectorySerializesSectionsAsArray verifies that an empty
// note directory persists and parses back with a non-nil sections array rather than null.
func TestEmptyReadingNoteDirectorySerializesSectionsAsArray(t *testing.T) {
	note := DocumentReadingNote{DocumentID: uuid.New()}
	if err := note.SetDirectory(ReadingNoteDirectory{SchemaVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if note.Directory.Sections == nil {
		t.Fatal("in-memory directory has nil sections")
	}
	if !strings.Contains(string(note.Structure), `"sections":[]`) {
		t.Fatalf("persisted directory = %s", note.Structure)
	}
	persisted := DocumentReadingNote{DocumentID: note.DocumentID, Structure: note.Structure}
	if err := persisted.ParseDirectory(); err != nil {
		t.Fatal(err)
	}
	if persisted.Directory.Sections == nil {
		t.Fatal("parsed directory has nil sections")
	}
}

// TestReadingNoteDirectoryRequiresDocumentScopedSectionPaths verifies that section
// storage keys must stay under their owning document's directory and rejects keys
// scoped to another document.
func TestReadingNoteDirectoryRequiresDocumentScopedSectionPaths(t *testing.T) {
	documentID, sectionID := uuid.New(), uuid.New()
	note := DocumentReadingNote{DocumentID: documentID, SizeBytes: 8}
	directory := ReadingNoteDirectory{SchemaVersion: 1, Sections: []ReadingNoteSectionEntry{{
		ID: sectionID, Order: 0, Level: 1, Title: "Summary", StorageKey: "notes/" + documentID.String() + "/" + sectionID.String() + ".md",
		ContentRevision: 1, SizeBytes: 8,
	}}}
	if err := note.SetDirectory(directory); err != nil {
		t.Fatal(err)
	}
	directory.Sections[0].StorageKey = "notes/" + uuid.NewString() + "/" + sectionID.String() + ".md"
	if err := note.SetDirectory(directory); !errors.Is(err, ErrInvalidReadingNote) {
		t.Fatalf("cross-document key error=%v", err)
	}
}

// ---------------------------------------------------------------------------
// Consolidated from pdf_region_test.go.
// ---------------------------------------------------------------------------

// TestPDFRegionRenderRequestValidate verifies that a bounded region request is
// accepted while missing source or page data, inverted or NaN bounds, and zero DPI are rejected.
func TestPDFRegionRenderRequestValidate(t *testing.T) {
	valid := PDFRegionRenderRequest{
		SourceStorageKey: "pdf/source.pdf", PageNumber: 2,
		BBoxX0: 10, BBoxY0: 20, BBoxX1: 210, BBoxY1: 120, DPI: 216,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}

	tests := []PDFRegionRenderRequest{
		{},
		{SourceStorageKey: "pdf/source.pdf", PageNumber: 0, BBoxX1: 1, BBoxY1: 1, DPI: 72},
		{SourceStorageKey: "pdf/source.pdf", PageNumber: 1, BBoxX0: 2, BBoxX1: 1, BBoxY1: 1, DPI: 72},
		{SourceStorageKey: "pdf/source.pdf", PageNumber: 1, BBoxX1: math.NaN(), BBoxY1: 1, DPI: 72},
		{SourceStorageKey: "pdf/source.pdf", PageNumber: 1, BBoxX1: 1, BBoxY1: 1, DPI: 0},
	}
	for index, request := range tests {
		if err := request.Validate(); !errors.Is(err, ErrInvalidPDFRegionRender) {
			t.Fatalf("case %d error = %v, want ErrInvalidPDFRegionRender", index, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Consolidated from recognition_source_test.go.
// ---------------------------------------------------------------------------

// TestRecognitionStorageKeyInputValidation verifies that storage-key mode accepts
// normalized relative keys, including Unicode names, but rejects mixed input modes
// and absolute, traversal, or Windows-style paths.
func TestRecognitionStorageKeyInputValidation(t *testing.T) {
	for _, key := range []string{"pdf/source.pdf", "pdf/论文 source.pdf"} {
		r := RecognitionRequest{DocumentID: uuid.New(), OriginalFilename: "source.pdf", InputMode: RecognitionInputStorageKey, SourceStorageKey: key, SizeBytes: 4}
		if err := r.Validate(); err != nil {
			t.Fatal(err)
		}
		r.Content = []byte("%PDF")
		if err := r.Validate(); err == nil {
			t.Fatal("mixed bytes and key accepted")
		}
		r.Content = nil
		r.InputMode = RecognitionInputBytes
		r.SizeBytes = 0
		if err := r.Validate(); err == nil {
			t.Fatal("bytes mode accepted a key")
		}
	}
	for _, key := range []string{"", "/source.pdf", "../source.pdf", "pdf/../source.pdf", "pdf//source.pdf", "pdf/./source.pdf", `C:\source.pdf`, `pdf\source.pdf`, "pdf/\x00.pdf"} {
		r := RecognitionRequest{DocumentID: uuid.New(), OriginalFilename: "source.pdf", InputMode: RecognitionInputStorageKey, SourceStorageKey: key}
		if err := r.Validate(); err == nil {
			t.Fatalf("invalid key %q accepted", key)
		}
	}
}

// ---------------------------------------------------------------------------
// Consolidated from span_tracker_test.go.
// ---------------------------------------------------------------------------

// TestComposeProcessingSpanScopeID verifies that scope IDs join normalized provider
// names and split back into their original three components.
func TestComposeProcessingSpanScopeID(t *testing.T) {
	runID := uuid.New()
	scopeID := ComposeProcessingSpanScopeID(
		ProcessingSpanCatalogReference,
		runID.String(),
		NormalizePaperCatalogProvider(" OpenAlex ").String(),
	)
	if got, want := scopeID.String(), "catalog_reference:"+runID.String()+":openalex"; got != want {
		t.Fatalf("ComposeProcessingSpanScopeID() = %q, want %q", got, want)
	}
	parts := SplitProcessingSpanScopeID(scopeID)
	if len(parts) != 3 || parts[0] != string(ProcessingSpanCatalogReference) || parts[1] != runID.String() || parts[2] != PaperCatalogProviderOpenAlex.String() {
		t.Fatalf("SplitProcessingSpanScopeID() = %#v", parts)
	}
}

// TestProcessingSpanScopeIDHelpersKeepTargetsSeparate verifies that provider scopes
// for source and reference targets remain distinct and that a two-part reference
// scope is preserved.
func TestProcessingSpanScopeIDHelpersKeepTargetsSeparate(t *testing.T) {
	sourceID, citedID, runID := uuid.New(), uuid.New(), uuid.New()
	source := ComposeProcessingSpanScopeID(
		ProcessingSpanCatalogProvider,
		ComposeProcessingSpanScopeID(ProcessingSpanCatalogSource, sourceID.String()).String(),
		PaperCatalogProviderArXiv.String(),
	)
	reference := ComposeProcessingSpanScopeID(
		ProcessingSpanCatalogProvider,
		ComposeProcessingSpanScopeID(ProcessingSpanCatalogReference, runID.String(), citedID.String()).String(),
		PaperCatalogProviderArXiv.String(),
	)
	if source == reference {
		t.Fatalf("source and reference provider scope IDs must differ: %q", source)
	}
	if got, want := ComposeProcessingSpanScopeID(ProcessingSpanCatalogReference, runID.String()).String(), "catalog_reference:"+runID.String(); got != want {
		t.Fatalf("run scope ID = %q, want %q", got, want)
	}
}

// TestSplitProcessingSpanScopeIDEmpty verifies that splitting an empty scope ID
// returns nil rather than an empty slice.
func TestSplitProcessingSpanScopeIDEmpty(t *testing.T) {
	if parts := SplitProcessingSpanScopeID(""); parts != nil {
		t.Fatalf("SplitProcessingSpanScopeID(empty) = %#v, want nil", parts)
	}
}

// ---------------------------------------------------------------------------
// Consolidated from tasks_test.go.
// ---------------------------------------------------------------------------

// TestStagePayloadCommandsRequireGenerationFence verifies that metadata, source,
// and reference task payloads are rejected when their stage or generation fence is absent.
func TestStagePayloadCommandsRequireGenerationFence(t *testing.T) {
	documentID := uuid.NewString()
	tests := []struct {
		name     string
		validate func() error
	}{
		{"metadata", func() error { return PaperMetadataExtractPayload{DocumentID: documentID}.Validate() }},
		{"source", func() error { return PaperSourceQueryEnqueuePayload{DocumentID: documentID}.Validate() }},
		{"reference", func() error { return PaperReferenceQueryEnqueuePayload{DocumentID: documentID}.Validate() }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.validate(); !errors.Is(err, ErrInvalidDocumentStageEnvelope) {
				t.Fatalf("Validate() error=%v", err)
			}
		})
	}
}

// TestStagePayloadCommandsDecodeDocumentStageAndGeneration verifies that each stage
// payload converts to a command carrying its document, stage, generation, and metadata restart flag.
func TestStagePayloadCommandsDecodeDocumentStageAndGeneration(t *testing.T) {
	documentID := uuid.New()
	metadata, err := (PaperMetadataExtractPayload{
		DocumentID: documentID.String(), Stage: DocumentStageMetadataExtraction,
		Generation: 3, RestartSourceLookup: true,
	}).Command()
	if err != nil || metadata.DocumentID != documentID || metadata.Generation != 3 || !metadata.RestartSourceLookup {
		t.Fatalf("metadata Command()=%#v err=%v", metadata, err)
	}
	source, err := (PaperSourceQueryEnqueuePayload{DocumentID: documentID.String(), Stage: DocumentStageSourceLookup, Generation: 4}).Command()
	if err != nil || source.DocumentID != documentID || source.Generation != 4 {
		t.Fatalf("source Command()=%#v err=%v", source, err)
	}
	reference, err := (PaperReferenceQueryEnqueuePayload{DocumentID: documentID.String(), Stage: DocumentStageReferenceLookup, Generation: 5}).Command()
	if err != nil || reference.DocumentID != documentID || reference.Generation != 5 {
		t.Fatalf("reference Command()=%#v err=%v", reference, err)
	}
}
