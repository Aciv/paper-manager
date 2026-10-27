package service

import (
	"context"
	"errors"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"
)

var ErrInvalidBaseOutlineQuery = errors.New("invalid base outline query")

func (s *documentBaseService) ListBaseReadingOutlines(
	ctx context.Context, rawID string, query types.BaseReadingOutlinesQuery,
) (types.BaseReadingOutlinesPage, error) {
	id, err := parseDocumentBaseID(rawID)
	if err != nil {
		return types.BaseReadingOutlinesPage{}, ErrInvalidBaseOutlineQuery
	}
	if query.Page == 0 {
		query.Page = 1
	}
	if query.PageSize == 0 {
		query.PageSize = 20
	}
	if query.SectionLimit == 0 {
		query.SectionLimit = 64
	}
	if query.OutlineType == "" {
		query.OutlineType = "both"
	}
	if query.Page < 1 || query.Page > 10000 || query.PageSize < 1 || query.PageSize > 50 ||
		query.SectionLimit < 1 || query.SectionLimit > 512 ||
		(query.OutlineType != "source" && query.OutlineType != "notes" && query.OutlineType != "both") {
		return types.BaseReadingOutlinesPage{}, ErrInvalidBaseOutlineQuery
	}
	rows, total, err := s.bases.ListReadingOutlines(ctx, id, query.Page, query.PageSize)
	if err != nil {
		return types.BaseReadingOutlinesPage{}, err
	}
	result := types.BaseReadingOutlinesPage{BaseID: id, Scope: "page", OutlineType: query.OutlineType,
		Page: query.Page, PageSize: query.PageSize, Total: total,
		HasMore: int64(query.Page*query.PageSize) < total, Items: []types.BaseReadingOutlineItem{}}
	for _, row := range rows {
		item := types.BaseReadingOutlineItem{DocumentID: row.DocumentID, Title: row.Title, MarkdownStatus: row.MarkdownStatus}
		var parts []types.OutlineAvailability
		if query.OutlineType != "notes" {
			item.SourceOutline = baseSourceOutline(ctx, row, query.SectionLimit)
			parts = append(parts, item.SourceOutline.OutlineAvailability)
		}
		if query.OutlineType != "source" {
			item.NotesOutline = baseNotesOutline(ctx, row, query.SectionLimit)
			parts = append(parts, item.NotesOutline.OutlineAvailability)
		}
		available, complete := 0, 0
		for _, part := range parts {
			if part.State == "available" {
				available++
				if part.Complete {
					complete++
				}
			}
		}
		item.Status = "failed"
		if complete == len(parts) {
			item.Status = "succeeded"
		} else if available > 0 {
			item.Status = "partial"
		}
		switch item.Status {
		case "succeeded":
			result.Summary.Succeeded++
		case "partial":
			result.Summary.Partial++
		case "failed":
			result.Summary.Failed++
		}
		result.Items = append(result.Items, item)
	}
	result.Summary.Total = len(result.Items)
	result.Status = "succeeded"
	if result.Summary.Total > 0 && result.Summary.Succeeded != result.Summary.Total {
		result.Status = "partial"
		if result.Summary.Failed == result.Summary.Total {
			result.Status = "failed"
		}
	}
	return result, nil
}

func baseSourceOutline(ctx context.Context, row types.BaseReadingOutlineRow, limit int) *types.BaseSourceOutline {
	result := &types.BaseSourceOutline{Sections: []types.DocumentOutlineSection{}}
	result.State = "not_ready"
	if row.SourceState != types.DocumentSourceAvailable {
		result.State = "unavailable"
		result.ErrorCode = "source_unavailable"
		return result
	}
	if row.MarkdownStatus != types.DocumentStageSucceeded {
		if row.MarkdownStatus == types.DocumentStageFailed || row.MarkdownStatus == types.DocumentStageCancelled ||
			row.MarkdownStatus == types.DocumentStageSkipped {
			result.State = "unavailable"
			result.ErrorCode = "markdown_" + string(row.MarkdownStatus)
		}
		return result
	}
	if row.SourceGeneration == nil {
		result.State = "missing"
		return result
	}
	result.Generation = *row.SourceGeneration
	if result.Generation != row.MarkdownGeneration {
		result.State = "unavailable"
		result.ErrorCode = "generation_mismatch"
		return result
	}
	outline, err := types.ParseDocumentOutline(types.JSON(row.SourceOutlineJSON))
	if err != nil {
		logger.Error(ctx, "decode base source outline failed", "document_id", row.DocumentID, "error", err)
		result.State = "error"
		result.ErrorCode = "invalid_source_outline"
		return result
	}
	if outline.SchemaVersion != types.DocumentOutlineSchemaVersion {
		result.State = "unavailable"
		result.ErrorCode = "outline_schema_unavailable"
		return result
	}
	if (outline.Source != types.DocumentOutlinePDF && outline.Source != types.DocumentOutlineMarkdown &&
		outline.Source != types.DocumentOutlineNone) || len(outline.Sections) > types.MaxDocumentOutlineItems ||
		(outline.Source == types.DocumentOutlineNone && len(outline.Sections) > 0) ||
		(outline.Source != types.DocumentOutlineNone && len(outline.Sections) == 0) {
		result.State = "error"
		result.ErrorCode = "invalid_source_outline"
		return result
	}
	result.State = "available"
	result.SchemaVersion = outline.SchemaVersion
	result.Source = outline.Source
	result.TotalSections = len(outline.Sections)
	result.Complete = len(outline.Sections) <= limit
	result.Sections = append(result.Sections, outline.Sections[:min(limit, len(outline.Sections))]...)
	return result
}

func baseNotesOutline(ctx context.Context, row types.BaseReadingOutlineRow, limit int) *types.BaseNotesOutline {
	result := &types.BaseNotesOutline{Sections: []types.ReadingNoteSectionEntry{}}
	result.State = "missing"
	if row.NoteRevision == nil {
		return result
	}
	result.Revision = *row.NoteRevision
	note := types.DocumentReadingNote{DocumentID: row.DocumentID,
		Structure: types.JSON(row.NoteStructureJSON), SizeBytes: row.NoteSizeBytes}
	if err := note.ParseDirectory(); err != nil {
		logger.Error(ctx, "decode base note outline failed", "document_id", row.DocumentID, "error", err)
		result.State = "error"
		result.ErrorCode = "invalid_notes_outline"
		return result
	}
	result.State = "available"
	result.TotalSections = len(note.Directory.Sections)
	result.Complete = result.TotalSections <= limit
	sections := publicDirectory(note.Directory).Sections
	result.Sections = append(result.Sections, sections[:min(limit, len(sections))]...)
	return result
}
