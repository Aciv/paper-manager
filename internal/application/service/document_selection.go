package service

import (
	"context"
	"unicode/utf8"

	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"
)

type documentSelectionProcessor struct {
	documents interfaces.DocumentRepository
}

// NewDocumentSelectionProcessor creates the synchronous, non-persistent Go
// boundary for transient PDF selections.
func NewDocumentSelectionProcessor(documents interfaces.DocumentRepository) interfaces.DocumentSelectionProcessor {
	return &documentSelectionProcessor{documents: documents}
}

func (s *documentSelectionProcessor) ProcessSelection(
	ctx context.Context, input types.DocumentSelectionInput,
) (types.DocumentSelectionResult, error) {
	if err := types.ValidateDocumentSelection(ctx, input); err != nil {
		return types.DocumentSelectionResult{}, err
	}
	if _, err := s.documents.Get(ctx, input.DocumentID); err != nil {
		return types.DocumentSelectionResult{}, err
	}
	result := types.DocumentSelectionResult{
		Kind: input.Kind, PageNumber: input.PageNumber,
		Message: "selection accepted for transient processing",
	}
	switch input.Kind {
	case types.DocumentSelectionText:
		result.TextRunes = utf8.RuneCountInString(input.Text)
	case types.DocumentSelectionBlock:
		block, err := s.documents.GetBlock(ctx, input.DocumentID, input.BlockID)
		if err != nil {
			return types.DocumentSelectionResult{}, err
		}
		result.BlockID = block.ID.String()
		if block.Location != nil && block.Location.PageNumber != nil {
			result.PageNumber = *block.Location.PageNumber
		}
	case types.DocumentSelectionArea:
		result.ImageWidth = input.ImageWidth
		result.ImageHeight = input.ImageHeight
	}
	if err := ctx.Err(); err != nil {
		return types.DocumentSelectionResult{}, err
	}
	return result, nil
}
