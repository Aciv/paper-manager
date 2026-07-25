package types

import (
	"encoding/json"
	"sync"
	"testing"

	"gorm.io/gorm/schema"
)

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
