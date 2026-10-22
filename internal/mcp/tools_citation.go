package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"paperMaster/internal/types"
)

// Citation tools form an independent collection for bibliography rows,
// resolution, and reference-PDF acquisition.

type citationDocumentInput struct {
	DocumentID string `json:"document_id"`
}

func (s *ToolServer) citationList(ctx context.Context, raw json.RawMessage) (any, error) {
	var input citationDocumentInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	items, err := s.papers.ListCitations(ctx, input.DocumentID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"items": items}, nil
}

func (s *ToolServer) citationListCitedBy(ctx context.Context, raw json.RawMessage) (any, error) {
	var input citationDocumentInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	items, err := s.papers.ListCitedBy(ctx, input.DocumentID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"items": items}, nil
}

type citationInputItem struct {
	RawReference    string  `json:"raw_reference"`
	CitedDocumentID *string `json:"cited_document_id"`
}

func citationInput(item citationInputItem) (types.PaperCitationInput, error) {
	cited, err := optionalUUID(item.CitedDocumentID)
	if err != nil {
		return types.PaperCitationInput{}, err
	}
	return types.PaperCitationInput{
		RawReference: item.RawReference, CitedDocumentID: cited,
	}, nil
}

type citationAddInput struct {
	DocumentID      string  `json:"document_id"`
	RawReference    string  `json:"raw_reference"`
	CitedDocumentID *string `json:"cited_document_id"`
}

func (s *ToolServer) citationAdd(ctx context.Context, raw json.RawMessage) (any, error) {
	var input citationAddInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	// Reuse the validated API shape and require a non-empty raw reference.
	if strings.TrimSpace(input.RawReference) == "" {
		return nil, fmt.Errorf("%w: raw_reference is required", errInvalidToolInput)
	}
	cited, err := optionalUUID(input.CitedDocumentID)
	if err != nil {
		return nil, err
	}
	citation, err := s.papers.AddCitation(ctx, input.DocumentID, types.PaperCitationInput{
		RawReference: input.RawReference, CitedDocumentID: cited,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"citation": citation}, nil
}

type citationReplaceInput struct {
	DocumentID string              `json:"document_id"`
	Citations  []citationInputItem `json:"citations"`
	Confirm    bool                `json:"confirm"`
}

func (s *ToolServer) citationReplace(ctx context.Context, raw json.RawMessage) (any, error) {
	var input citationReplaceInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	if !input.Confirm {
		return nil, fmt.Errorf("%w: confirm=true is required", errInvalidToolInput)
	}
	if len(input.Citations) > 500 {
		return nil, fmt.Errorf("%w: citations cannot contain more than 500 entries", errInvalidToolInput)
	}
	inputs := make([]types.PaperCitationInput, 0, len(input.Citations))
	for _, item := range input.Citations {
		converted, err := citationInput(item)
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, converted)
	}
	items, err := s.papers.ReplaceCitations(ctx, input.DocumentID, inputs)
	if err != nil {
		return nil, err
	}
	return map[string]any{"items": items}, nil
}

type citationReferenceInput struct {
	ReferenceID string `json:"reference_id"`
}

func (s *ToolServer) citationRemove(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		ReferenceID string `json:"reference_id"`
		Confirm     bool   `json:"confirm"`
	}
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	if !input.Confirm {
		return nil, fmt.Errorf("%w: confirm=true is required", errInvalidToolInput)
	}
	if err := s.papers.DeleteReference(ctx, input.ReferenceID); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": true, "referenceId": input.ReferenceID}, nil
}

type citationUpdateReferenceInput struct {
	ReferenceID string                 `json:"reference_id"`
	DOI         *string                `json:"doi"`
	Title       *string                `json:"title"`
	Authors     *[]metadataAuthorInput `json:"authors"`
	Venue       *string                `json:"venue"`
	PublishedAt *string                `json:"published_at"`
	Volume      *string                `json:"volume"`
	Issue       *string                `json:"issue"`
	Pages       *string                `json:"pages"`
	SourceURL   *string                `json:"source_url"`
}

func (s *ToolServer) citationUpdateReference(ctx context.Context, raw json.RawMessage) (any, error) {
	var input citationUpdateReferenceInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	update := types.PaperReferenceSourceUpdate{
		DOI: input.DOI, Title: input.Title, Venue: input.Venue,
		PublishedAt: input.PublishedAt, Volume: input.Volume,
		Issue: input.Issue, Pages: input.Pages, SourceURL: input.SourceURL,
	}
	if input.Authors != nil {
		authors := make([]types.PaperAuthor, 0, len(*input.Authors))
		for _, author := range *input.Authors {
			authors = append(authors, types.PaperAuthor{
				Name: author.Name, Affiliation: author.Affiliation,
			})
		}
		update.Authors = &authors
	}
	if !update.HasChanges() {
		return nil, fmt.Errorf("%w: at least one reference field is required", errInvalidToolInput)
	}
	reference, err := s.papers.UpdateReferenceSource(ctx, input.ReferenceID, update)
	if err != nil {
		return nil, err
	}
	return map[string]any{"reference": reference}, nil
}

type citationResolveInput struct {
	ReferenceID        string `json:"reference_id"`
	ResolvedDocumentID string `json:"resolved_document_id"`
}

func (s *ToolServer) citationResolve(ctx context.Context, raw json.RawMessage) (any, error) {
	var input citationResolveInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(strings.TrimSpace(input.ResolvedDocumentID)); err != nil {
		return nil, fmt.Errorf("%w: resolved_document_id must be a UUID", errInvalidToolInput)
	}
	reference, err := s.papers.ResolveReference(ctx, input.ReferenceID, input.ResolvedDocumentID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"reference": reference}, nil
}

func (s *ToolServer) citationRematch(ctx context.Context, raw json.RawMessage) (any, error) {
	var input citationReferenceInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	reference, matched, err := s.papers.RematchReference(ctx, input.ReferenceID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"reference": reference, "matched": matched}, nil
}

func (s *ToolServer) citationDownloadPDF(ctx context.Context, raw json.RawMessage) (any, error) {
	var input citationReferenceInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	if err := s.documents.RequestReferencePDFDownload(ctx, input.ReferenceID); err != nil {
		return nil, err
	}
	return map[string]any{"accepted": true, "referenceId": input.ReferenceID}, nil
}
