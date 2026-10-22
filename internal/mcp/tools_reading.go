package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"paperMaster/internal/types"
)

type documentReadSectionInput struct {
	DocumentID string `json:"document_id"`
	Ordinal    int    `json:"ordinal"`
}

type sourceSectionResult struct {
	Section     types.DocumentOutlineSection `json:"section"`
	Markdown    string                       `json:"markdown,omitempty"`
	Diagnostics []string                     `json:"diagnostics,omitempty"`
}

func (s *ToolServer) documentStructure(ctx context.Context, raw json.RawMessage) (any, error) {
	var input documentIDInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	return s.reading.GetDocumentReadingStructure(ctx, input.DocumentID)
}

func (s *ToolServer) documentReadSection(ctx context.Context, raw json.RawMessage) (any, error) {
	var input documentReadSectionInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	structure, err := s.reading.GetDocumentReadingStructure(ctx, input.DocumentID)
	if err != nil {
		return nil, err
	}
	var section *types.DocumentOutlineSection
	for index := range structure.Outline.Sections {
		if structure.Outline.Sections[index].Ordinal == input.Ordinal {
			section = &structure.Outline.Sections[index]
			break
		}
	}
	if section == nil {
		return nil, fmt.Errorf("%w: source section %d", errInvalidToolInput, input.Ordinal)
	}
	result := sourceSectionResult{Section: *section}
	if section.MarkdownCodepointStart < 0 || section.MarkdownCodepointEnd < section.MarkdownCodepointStart {
		result.Diagnostics = append(result.Diagnostics, "source section has no Markdown range; use page or Block hints")
		return result, nil
	}
	markdown, err := s.reading.GetDocumentMarkdown(ctx, input.DocumentID)
	if err != nil {
		return nil, err
	}
	if markdown.RecognitionGeneration != structure.RecognitionGeneration {
		return nil, fmt.Errorf("%w: recognition generation changed while reading the section", errSnapshotConflict)
	}
	runes := []rune(string(markdown.Markdown))
	if section.MarkdownCodepointEnd > len(runes) {
		return nil, fmt.Errorf("%w: outline Markdown range exceeds the stored Markdown", errSnapshotConflict)
	}
	content := string(runes[section.MarkdownCodepointStart:section.MarkdownCodepointEnd])
	if len(content) > 4<<20 {
		return nil, fmt.Errorf("%w: source section exceeds the MCP response limit", errPayloadTooLarge)
	}
	result.Markdown = content
	return result, nil
}

func (s *ToolServer) documentReadMarkdown(ctx context.Context, raw json.RawMessage) (any, error) {
	var input documentIDInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	markdown, err := s.reading.GetDocumentMarkdown(ctx, input.DocumentID)
	if err != nil {
		return nil, err
	}
	if len(markdown.Markdown) > 16<<20 {
		return nil, fmt.Errorf("%w: document Markdown exceeds the MCP response limit", errPayloadTooLarge)
	}
	return map[string]any{
		"document_id": markdown.DocumentID.String(), "filename": markdown.Filename,
		"content_type": markdown.ContentType, "size_bytes": markdown.SizeBytes,
		"page_count": markdown.PageCount, "recognition_generation": markdown.RecognitionGeneration,
		"markdown": string(markdown.Markdown),
	}, nil
}

type readBlocksInput struct {
	DocumentID string `json:"document_id"`
	PageNumber int    `json:"page_number"`
}

func (s *ToolServer) documentReadBlocks(ctx context.Context, raw json.RawMessage) (any, error) {
	var input readBlocksInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	blocks, err := s.reading.GetDocumentBlocks(ctx, input.DocumentID, input.PageNumber)
	if err != nil {
		return nil, err
	}
	data := map[string]any{"items": blocks}
	encoded, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	if len(encoded) > 4<<20 {
		return nil, fmt.Errorf("%w: Block response exceeds the MCP response limit", errPayloadTooLarge)
	}
	return data, nil
}

type noteOutlineSection struct {
	ID              uuid.UUID  `json:"id"`
	ParentID        *uuid.UUID `json:"parent_id,omitempty"`
	Order           float64    `json:"order"`
	Level           int        `json:"level"`
	Title           string     `json:"title"`
	Description     string     `json:"description,omitempty"`
	ContentRevision int64      `json:"content_revision"`
	SizeBytes       int64      `json:"size_bytes"`
}

type noteOutlineInput struct {
	DocumentID          string `json:"document_id"`
	IncludeDescriptions *bool  `json:"include_descriptions"`
}

func (s *ToolServer) readingNoteOutline(ctx context.Context, raw json.RawMessage) (any, error) {
	var input noteOutlineInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	includeDescriptions := input.IncludeDescriptions == nil || *input.IncludeDescriptions
	manifest, err := s.notes.GetManifest(ctx, input.DocumentID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %v", errNoteNotFound, err)
	}
	if err != nil {
		return nil, err
	}
	sections := make([]noteOutlineSection, len(manifest.Directory.Sections))
	diagnostics := []string{}
	descriptionsComplete := true
	var descriptionBytes int64
	for index, section := range manifest.Directory.Sections {
		sections[index] = noteOutlineSection{
			ID: section.ID, ParentID: section.ParentID, Order: section.Order,
			Level: section.Level, Title: section.Title,
			ContentRevision: section.ContentRevision, SizeBytes: section.SizeBytes,
		}
		if !includeDescriptions {
			continue
		}
		if descriptionBytes >= 8<<20 || section.SizeBytes > 4<<20 {
			descriptionsComplete = false
			diagnostics = append(diagnostics, "section description budget exhausted")
			continue
		}
		body, err := s.notes.ReadSection(ctx, input.DocumentID, section.ID.String())
		if err != nil {
			return nil, err
		}
		descriptionBytes += int64(len(body.Markdown))
		description, metaDiagnostics := parseSectionMeta(body.Markdown)
		sections[index].Description = description
		diagnostics = append(diagnostics, metaDiagnostics...)
	}
	after, err := s.notes.GetManifest(ctx, input.DocumentID)
	if err != nil {
		return nil, err
	}
	if after.Revision != manifest.Revision {
		return nil, fmt.Errorf("%w: reading note revision changed while building the outline", types.ErrReadingNoteConflict)
	}
	return map[string]any{
		"document_id": input.DocumentID, "revision": manifest.Revision,
		"sections": sections, "descriptions_complete": descriptionsComplete,
		"diagnostics": diagnostics,
	}, nil
}

type noteReadSectionInput struct {
	DocumentID string `json:"document_id"`
	SectionID  string `json:"section_id"`
}

func (s *ToolServer) readingNoteReadSection(ctx context.Context, raw json.RawMessage) (any, error) {
	var input noteReadSectionInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	section, err := s.notes.ReadSection(ctx, input.DocumentID, input.SectionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %v", errNoteNotFound, err)
	}
	if err != nil {
		return nil, err
	}
	if len(section.Markdown) > 4<<20 {
		return nil, fmt.Errorf("%w: note section exceeds the MCP response limit", errPayloadTooLarge)
	}
	description, diagnostics := parseSectionMeta(section.Markdown)
	return map[string]any{
		"document_id": section.DocumentID.String(), "revision": section.Revision,
		"section": section.Section, "markdown": section.Markdown,
		"line_count": section.LineCount, "description": description,
		"diagnostics": diagnostics,
	}, nil
}

type noteCreateInput struct {
	DocumentID       string `json:"document_id"`
	Markdown         string `json:"markdown"`
	ExpectedRevision int64  `json:"expected_revision"`
}

func (s *ToolServer) readingNoteCreate(ctx context.Context, raw json.RawMessage) (any, error) {
	var input noteCreateInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	if input.ExpectedRevision != 0 {
		return nil, fmt.Errorf("%w: reading note creation requires expected_revision=0", errInvalidToolInput)
	}
	note, err := s.notes.Put(ctx, input.DocumentID, 0, input.Markdown)
	if err != nil {
		return nil, err
	}
	return note, nil
}

type noteReplaceInput struct {
	DocumentID       string `json:"document_id"`
	Markdown         string `json:"markdown"`
	ExpectedRevision int64  `json:"expected_revision"`
}

func (s *ToolServer) readingNoteReplace(ctx context.Context, raw json.RawMessage) (any, error) {
	var input noteReplaceInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	note, err := s.notes.Put(ctx, input.DocumentID, input.ExpectedRevision, input.Markdown)
	if err != nil {
		return nil, err
	}
	return note, nil
}

type noteDeleteInput struct {
	DocumentID       string `json:"document_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	Confirm          bool   `json:"confirm"`
}

func (s *ToolServer) readingNoteDelete(ctx context.Context, raw json.RawMessage) (any, error) {
	var input noteDeleteInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	if !input.Confirm {
		return nil, fmt.Errorf("%w: confirm=true is required", errInvalidToolInput)
	}
	if err := s.notes.Delete(ctx, input.DocumentID, input.ExpectedRevision); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": true, "documentId": input.DocumentID}, nil
}

type noteExportInput struct {
	DocumentID string `json:"document_id"`
}

func (s *ToolServer) readingNoteExport(ctx context.Context, raw json.RawMessage) (any, error) {
	var input noteExportInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	note, err := s.notes.Export(ctx, input.DocumentID)
	if err != nil {
		return nil, err
	}
	if len(note.Markdown) > 16<<20 {
		return nil, fmt.Errorf("%w: reading note export exceeds the MCP response limit", errPayloadTooLarge)
	}
	return map[string]any{
		"documentId": note.DocumentID, "revision": note.Revision, "markdown": note.Markdown,
	}, nil
}

type noteReplaceSectionInput struct {
	DocumentID       string `json:"document_id"`
	SectionID        string `json:"section_id"`
	Markdown         string `json:"markdown"`
	ExpectedRevision int64  `json:"expected_revision"`
}

func (s *ToolServer) readingNoteReplaceSection(ctx context.Context, raw json.RawMessage) (any, error) {
	var input noteReplaceSectionInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	result, err := s.notes.ReplaceSection(
		ctx, input.DocumentID, input.SectionID, input.ExpectedRevision, input.Markdown,
	)
	if err != nil {
		return nil, err
	}
	return result, nil
}

type blockImageInput struct {
	DocumentID string `json:"document_id"`
	BlockID    string `json:"block_id"`
}

// documentReadBlockImage returns the parser-stored PNG crop for image/table
// Blocks. It never returns the private storage key.
func (s *ToolServer) documentReadBlockImage(
	ctx context.Context, raw json.RawMessage,
) (*sdk.CallToolResult, error) {
	var input blockImageInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	if s.reading == nil {
		return nil, fmt.Errorf("document reading service is unavailable")
	}
	reader, filename, err := s.reading.GetDocumentBlockImage(ctx, input.DocumentID, input.BlockID)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	maxImageBytes := s.cfg.MCPMaxImageBytes
	if maxImageBytes <= 0 {
		maxImageBytes = 12 << 20
	}
	content, err := io.ReadAll(io.LimitReader(reader, maxImageBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > maxImageBytes {
		return nil, fmt.Errorf("%w: block image exceeds the MCP image limit", errPayloadTooLarge)
	}
	contentType := http.DetectContentType(content)
	if !strings.HasPrefix(contentType, "image/") {
		return nil, fmt.Errorf("block asset is not an image")
	}
	metadata := map[string]any{
		"documentId": input.DocumentID, "blockId": input.BlockID,
		"filename": filename, "contentType": contentType, "sizeBytes": len(content),
	}
	return successImageResult(metadata, content, contentType), nil
}

type notePositionInput struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

type noteEditInput struct {
	Op        string             `json:"op"`
	SectionID *string            `json:"section_id"`
	ParentID  *string            `json:"parent_id"`
	AfterID   *string            `json:"after_id"`
	OldText   string             `json:"old_text"`
	NewText   string             `json:"new_text"`
	Content   string             `json:"content"`
	Start     *notePositionInput `json:"start"`
	End       *notePositionInput `json:"end"`
	Position  *notePositionInput `json:"position"`
	LineStart int                `json:"line_start"`
	LineEnd   int                `json:"line_end"`
}

type noteEditsInput struct {
	DocumentID       string          `json:"document_id"`
	ExpectedRevision int64           `json:"expected_revision"`
	Edits            []noteEditInput `json:"edits"`
}

func optionalUUID(raw *string) (*uuid.UUID, error) {
	if raw == nil {
		return nil, nil
	}
	id, err := uuid.Parse(strings.TrimSpace(*raw))
	if err != nil {
		return nil, fmt.Errorf("%w: invalid UUID", errInvalidToolInput)
	}
	return &id, nil
}

func notePosition(raw *notePositionInput) *types.ReadingNotePosition {
	if raw == nil {
		return nil
	}
	return &types.ReadingNotePosition{Line: raw.Line, Column: raw.Column}
}

func (s *ToolServer) readingNoteApplyEdits(ctx context.Context, raw json.RawMessage) (any, error) {
	var input noteEditsInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	edits := make([]types.ReadingNoteEdit, 0, len(input.Edits))
	for _, edit := range input.Edits {
		sectionID, err := optionalUUID(edit.SectionID)
		if err != nil {
			return nil, err
		}
		parentID, err := optionalUUID(edit.ParentID)
		if err != nil {
			return nil, err
		}
		afterID, err := optionalUUID(edit.AfterID)
		if err != nil {
			return nil, err
		}
		edits = append(edits, types.ReadingNoteEdit{
			Op: edit.Op, SectionID: sectionID, ParentID: parentID, AfterID: afterID,
			OldText: edit.OldText, NewText: edit.NewText, Content: edit.Content,
			Start: notePosition(edit.Start), End: notePosition(edit.End), Position: notePosition(edit.Position),
			LineStart: edit.LineStart, LineEnd: edit.LineEnd,
		})
	}
	result, err := s.notes.ApplyEdits(ctx, input.DocumentID, types.ReadingNoteEdits{
		ExpectedRevision: input.ExpectedRevision, Edits: edits,
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

type evidenceValidateInput struct {
	DocumentID string `json:"document_id"`
	Href       string `json:"href"`
}

func (s *ToolServer) evidenceValidate(ctx context.Context, raw json.RawMessage) (any, error) {
	var input evidenceValidateInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	validation, err := s.validateEvidence(ctx, input.DocumentID, input.Href)
	if err != nil {
		return nil, err
	}
	if validation.Target == nil {
		return nil, fmt.Errorf("%w: evidence link could not be parsed", errInvalidEvidence)
	}
	return validation, nil
}
