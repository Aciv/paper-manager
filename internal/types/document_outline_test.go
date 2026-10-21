package types

import (
	"strings"
	"testing"
)

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

func TestValidateRecognitionOutlineRejectsFutureParentAndOutOfBoundsRange(t *testing.T) {
	outline := DocumentOutline{SchemaVersion: 1, Source: DocumentOutlinePDF,
		Sections: []DocumentOutlineSection{{Ordinal: 0, Title: "Bad", Level: 2, ParentOrdinal: 0,
			StartPage: 1, EndPage: 1, StartBlockOrdinal: 0, EndBlockOrdinal: 2,
			MarkdownCodepointStart: 0, MarkdownCodepointEnd: 5}}}
	if err := ValidateRecognitionOutline(outline, "text", []RecognitionBlock{recognitionBlockForOutline(0, 1, 0, 4)}, 1); err == nil {
		t.Fatal("ValidateRecognitionOutline() accepted invalid hierarchy and ranges")
	}
}

func TestValidateRecognitionOutlineAcceptsSectionWithoutLocation(t *testing.T) {
	outline := DocumentOutline{SchemaVersion: 1, Source: DocumentOutlinePDF,
		Sections: []DocumentOutlineSection{{Ordinal: 0, Title: "Destination unavailable", Level: 1, ParentOrdinal: -1,
			StartBlockOrdinal: -1, EndBlockOrdinal: -1, MarkdownCodepointStart: -1, MarkdownCodepointEnd: -1}}}
	if err := ValidateRecognitionOutline(outline, "body", nil, 1); err != nil {
		t.Fatalf("ValidateRecognitionOutline() error = %v", err)
	}
}

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
