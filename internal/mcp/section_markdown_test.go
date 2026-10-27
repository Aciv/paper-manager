package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"paperMaster/internal/config"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
)

type sectionMarkdownReader struct {
	interfaces.DocumentReadingService
	structure types.DocumentReadingStructure
	markdown  types.DocumentMarkdownContent
	reads     int
}

func (r *sectionMarkdownReader) GetDocumentReadingStructure(
	context.Context, string,
) (types.DocumentReadingStructure, error) {
	return r.structure, nil
}

func (r *sectionMarkdownReader) GetDocumentMarkdown(
	context.Context, string,
) (types.DocumentMarkdownContent, error) {
	r.reads++
	return r.markdown, nil
}

func markdownSectionFixture() *sectionMarkdownReader {
	return &sectionMarkdownReader{
		structure: types.DocumentReadingStructure{
			RecognitionGeneration: 7,
			Outline: types.DocumentOutline{Sections: []types.DocumentOutlineSection{{
				Ordinal: 2, Title: "方法", StartBlockOrdinal: -1, EndBlockOrdinal: -1,
				MarkdownCodepointStart: 1, MarkdownCodepointEnd: 4,
			}}},
		},
		markdown: types.DocumentMarkdownContent{
			RecognitionGeneration: 7, Markdown: []byte("前方法🙂后"),
		},
	}
}

func TestSectionMarkdownLegacyAndExplicitSDK(t *testing.T) {
	reader := markdownSectionFixture()
	server, err := NewToolServer(
		config.Config{MCPAddress: "127.0.0.1:8090"}, nil, reader, nil, nil, nil, nil, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	client := connectTestClient(t, server)
	for _, mode := range []string{"", "markdown"} {
		args := map[string]any{"document_id": uuid.NewString(), "ordinal": 2}
		if mode != "" {
			args["mode"] = mode
		}
		result := callTool(t, client, "papers_manager_document_read_section", args)
		var envelope struct {
			OK   bool                `json:"ok"`
			Data sourceSectionResult `json:"data"`
		}
		if err := json.Unmarshal([]byte(resultText(t, result)), &envelope); err != nil {
			t.Fatal(err)
		}
		if result.IsError || !envelope.OK || envelope.Data.Markdown != "方法🙂" || envelope.Data.Section.Ordinal != 2 {
			t.Fatalf("mode=%q: %s", mode, resultText(t, result))
		}
	}
	if reader.reads != 2 {
		t.Fatalf("Markdown reads: %d", reader.reads)
	}
}

func TestSectionMarkdownConflictsAndLimits(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name   string
		change func(*sectionMarkdownReader, *documentReadSectionInput)
		want   error
	}{
		{"expected version", func(_ *sectionMarkdownReader, input *documentReadSectionInput) {
			generation := int64(6)
			input.ExpectedGeneration = &generation
		}, errSnapshotConflict},
		{"replacement during read", func(r *sectionMarkdownReader, _ *documentReadSectionInput) {
			r.markdown.RecognitionGeneration = 8
		}, errSnapshotConflict},
		{"out of bounds", func(r *sectionMarkdownReader, _ *documentReadSectionInput) {
			r.structure.Outline.Sections[0].MarkdownCodepointEnd = 999
		}, errSnapshotConflict},
		{"unknown ordinal", func(_ *sectionMarkdownReader, input *documentReadSectionInput) {
			input.Ordinal = 999
		}, errInvalidToolInput},
		{"pagination needs blocks", func(_ *sectionMarkdownReader, input *documentReadSectionInput) {
			input.Page = 2
		}, errInvalidToolInput},
		{"oversize", func(r *sectionMarkdownReader, _ *documentReadSectionInput) {
			r.markdown.Markdown = []byte(strings.Repeat("x", (4<<20)+1))
			r.structure.Outline.Sections[0].MarkdownCodepointStart = 0
			r.structure.Outline.Sections[0].MarkdownCodepointEnd = len(r.markdown.Markdown)
		}, errPayloadTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := markdownSectionFixture()
			input := documentReadSectionInput{DocumentID: uuid.NewString(), Ordinal: 2}
			test.change(reader, &input)
			server := &ToolServer{reading: reader}
			_, err := server.documentReadSectionMarkdown(ctx, input)
			if !errors.Is(err, test.want) {
				t.Fatalf("got %v, want %v", err, test.want)
			}
		})
	}
	reader := markdownSectionFixture()
	reader.structure.Outline.Sections[0].MarkdownCodepointStart = -1
	server := &ToolServer{reading: reader}
	result, err := server.documentReadSectionMarkdown(ctx, documentReadSectionInput{Ordinal: 2})
	if err != nil || result.Markdown != "" || len(result.Diagnostics) == 0 || reader.reads != 0 {
		t.Fatalf("unmapped Markdown range: %+v %v, reads=%d", result, err, reader.reads)
	}
}
