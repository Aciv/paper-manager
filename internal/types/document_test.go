package types

import (
	"encoding/json"
	"gorm.io/gorm/schema"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Consolidated from document_block_test.go.
// ---------------------------------------------------------------------------

// TestBlockEmbedsPDFLocationInDatabaseAndNestsItInJSON verifies that Block
// persists the embedded PDF location as flattened database columns while
// serializing it as a nested "location" object in JSON.
func TestBlockEmbedsPDFLocationInDatabaseAndNestsItInJSON(t *testing.T) {
	page, start, end := 2, 10, 24
	width, height := 595.0, 842.0
	block := Block{
		Location: &PDFBlockLocation{
			PageNumber:             &page,
			PageWidth:              &width,
			PageHeight:             &height,
			MarkdownCodepointStart: &start,
			MarkdownCodepointEnd:   &end,
		},
	}

	data, err := json.Marshal(block)
	if err != nil {
		t.Fatalf("marshal block: %v", err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal block JSON: %v", err)
	}
	if _, ok := payload["location"]; !ok {
		t.Fatalf("Block JSON does not contain nested location: %s", data)
	}
	if _, ok := payload["pageNumber"]; ok {
		t.Fatalf("Block JSON unexpectedly flattens location: %s", data)
	}

	parsed, err := schema.Parse(&Block{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("parse Block schema: %v", err)
	}
	for _, column := range []string{
		"page_number", "page_width", "page_height", "bbox_x0", "bbox_y0",
		"bbox_x1", "bbox_y1", "markdown_codepoint_start", "markdown_codepoint_end",
	} {
		if parsed.FieldsByDBName[column] == nil {
			t.Fatalf("embedded PDFBlockLocation does not map column %q", column)
		}
	}
}

// ---------------------------------------------------------------------------
// Consolidated from document_outline_test.go.
// ---------------------------------------------------------------------------

// TestValidateRecognitionOutlineAcceptsHierarchicalHalfOpenRanges verifies that a
// parent-before-child outline whose sections use half-open page, block, and
// codepoint ranges is accepted.
func TestValidateRecognitionOutlineAcceptsHierarchicalHalfOpenRanges(t *testing.T) {
	markdown := "# Intro\nbody\n## Method\nmore"
	blocks := []RecognitionBlock{
		recognitionBlockForOutline(0, 1, 0, 13),
		recognitionBlockForOutline(1, 2, 13, len([]rune(markdown))),
	}
	outline := DocumentOutline{SchemaVersion: 1, Source: DocumentOutlineMarkdown,
		Sections: []DocumentOutlineSection{
			{Ordinal: 0, Title: "Intro", Level: 1, ParentOrdinal: -1, StartPage: 1, EndPage: 2,
				StartBlockOrdinal: 0, EndBlockOrdinal: 2, MarkdownCodepointStart: 0,
				MarkdownCodepointEnd: len([]rune(markdown))},
			{Ordinal: 1, Title: "Method", Level: 2, ParentOrdinal: 0, StartPage: 2, EndPage: 2,
				StartBlockOrdinal: 1, EndBlockOrdinal: 2, MarkdownCodepointStart: 13,
				MarkdownCodepointEnd: len([]rune(markdown))},
		}}
	if err := ValidateRecognitionOutline(outline, markdown, blocks, 2); err != nil {
		t.Fatalf("ValidateRecognitionOutline() error = %v", err)
	}
}

// TestValidateRecognitionOutlineRejectsFutureParentAndOutOfBoundsRange verifies
// that a section cannot reference itself as its parent or extend past the
// markdown and block ranges it describes.
func TestValidateRecognitionOutlineRejectsFutureParentAndOutOfBoundsRange(t *testing.T) {
	outline := DocumentOutline{SchemaVersion: 1, Source: DocumentOutlinePDF,
		Sections: []DocumentOutlineSection{{Ordinal: 0, Title: "Bad", Level: 2, ParentOrdinal: 0,
			StartPage: 1, EndPage: 1, StartBlockOrdinal: 0, EndBlockOrdinal: 2,
			MarkdownCodepointStart: 0, MarkdownCodepointEnd: 5}}}
	if err := ValidateRecognitionOutline(outline, "text", []RecognitionBlock{recognitionBlockForOutline(0, 1, 0, 4)}, 1); err == nil {
		t.Fatal("ValidateRecognitionOutline() accepted invalid hierarchy and ranges")
	}
}

// TestValidateRecognitionOutlineAcceptsSectionWithoutLocation verifies that a
// section whose PDF destination has no usable location may use -1 sentinels and
// omit all page or block bounds.
func TestValidateRecognitionOutlineAcceptsSectionWithoutLocation(t *testing.T) {
	outline := DocumentOutline{SchemaVersion: 1, Source: DocumentOutlinePDF,
		Sections: []DocumentOutlineSection{{Ordinal: 0, Title: "Destination unavailable", Level: 1, ParentOrdinal: -1,
			StartBlockOrdinal: -1, EndBlockOrdinal: -1, MarkdownCodepointStart: -1, MarkdownCodepointEnd: -1}}}
	if err := ValidateRecognitionOutline(outline, "body", nil, 1); err != nil {
		t.Fatalf("ValidateRecognitionOutline() error = %v", err)
	}
}

// TestDocumentOutlineIgnoresAndDropsLegacyMappingQuality verifies that an outline
// containing the retired mappingQuality field still parses and re-serializes
// without that field.
func TestDocumentOutlineIgnoresAndDropsLegacyMappingQuality(t *testing.T) {
	legacy := JSON(`{"schemaVersion":1,"source":"pdf_outline","sections":[{"ordinal":0,"title":"Intro","level":1,"parentOrdinal":-1,"startPage":1,"startX":0,"startY":0,"endPage":1,"endX":0,"endY":0,"startBlockOrdinal":-1,"endBlockOrdinal":-1,"markdownCodepointStart":-1,"markdownCodepointEnd":-1,"mappingQuality":"page_approximate"}]}`)
	outline, err := ParseDocumentOutline(legacy)
	if err != nil {
		t.Fatalf("ParseDocumentOutline() error = %v", err)
	}
	encoded, err := outline.JSON()
	if err != nil {
		t.Fatalf("DocumentOutline.JSON() error = %v", err)
	}
	if strings.Contains(string(encoded), "mappingQuality") {
		t.Fatalf("DocumentOutline.JSON() retained removed mapping quality: %s", encoded)
	}
}

func recognitionBlockForOutline(ordinal, page, start, end int) RecognitionBlock {
	width, height, x0, y0, x1, y1 := 600.0, 800.0, 10.0, 20.0, 100.0, 40.0
	return RecognitionBlock{Ordinal: ordinal, Type: "text", Location: PDFBlockLocation{
		PageNumber: &page, PageWidth: &width, PageHeight: &height,
		BBoxX0: &x0, BBoxY0: &y0, BBoxX1: &x1, BBoxY1: &y1,
		MarkdownCodepointStart: &start, MarkdownCodepointEnd: &end,
	}}
}

// ---------------------------------------------------------------------------
// Consolidated from document_stage_test.go.
// ---------------------------------------------------------------------------

// TestDocumentStageStatusClassification verifies that known stage statuses expose
// the correct active and terminal classifications while unknown statuses are invalid.
func TestDocumentStageStatusClassification(t *testing.T) {
	for _, status := range []DocumentStageStatus{
		DocumentStageNotStarted, DocumentStageQueued, DocumentStageRunning,
		DocumentStageSucceeded, DocumentStageFailed, DocumentStageCancelled, DocumentStageSkipped,
	} {
		if !status.Valid() {
			t.Fatalf("status %q should be valid", status)
		}
	}
	if DocumentStageStatus("unknown").Valid() {
		t.Fatal("unknown stage status should be invalid")
	}
	if !DocumentStageQueued.Active() || !DocumentStageRunning.Active() || DocumentStageFailed.Active() {
		t.Fatal("active classification is incorrect")
	}
	if !DocumentStageSucceeded.Terminal() || !DocumentStageFailed.Terminal() ||
		!DocumentStageCancelled.Terminal() || !DocumentStageSkipped.Terminal() || DocumentStageRunning.Terminal() {
		t.Fatal("terminal classification is incorrect")
	}
}

// TestDocumentStageStateReadsIndependentFields verifies that StageState returns
// each stage's own status, generation, and timestamp and rejects an unknown stage.
func TestDocumentStageStateReadsIndependentFields(t *testing.T) {
	markdownTime := time.Now().UTC().Add(-time.Minute)
	sourceTime := markdownTime.Add(time.Second)
	document := Document{
		MarkdownStatus:         DocumentStageSucceeded,
		MarkdownGeneration:     2,
		MarkdownUpdatedAt:      markdownTime,
		SourceLookupStatus:     DocumentStageRunning,
		SourceLookupGeneration: 7,
		SourceLookupUpdatedAt:  sourceTime,
	}
	markdown, err := document.StageState(DocumentStageMarkdown)
	if err != nil || markdown.Status != DocumentStageSucceeded || markdown.Generation != 2 || !markdown.UpdatedAt.Equal(markdownTime) {
		t.Fatalf("markdown state = %#v, error %v", markdown, err)
	}
	source, err := document.StageState(DocumentStageSourceLookup)
	if err != nil || source.Status != DocumentStageRunning || source.Generation != 7 || !source.UpdatedAt.Equal(sourceTime) {
		t.Fatalf("source state = %#v, error %v", source, err)
	}
	if _, err := document.StageState(DocumentStage("invalid")); err == nil {
		t.Fatal("invalid stage should fail")
	}
}
