package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"paperMaster/internal/types"
)

// Base tools form an independent collection. They manage collections and
// memberships; document creation/deletion stays in the library collection.

type baseListInput struct {
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
}

func (s *ToolServer) baseList(ctx context.Context, raw json.RawMessage) (any, error) {
	var input baseListInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	page, size := normalizedPage(input.Page, input.PageSize)
	bases, err := s.bases.ListBases(ctx)
	if err != nil {
		return nil, err
	}
	start := (page - 1) * size
	if start > len(bases) {
		start = len(bases)
	}
	end := start + size
	if end > len(bases) {
		end = len(bases)
	}
	return map[string]any{"items": bases[start:end], "page": page, "pageSize": size, "total": len(bases)}, nil
}

type baseGetInput struct {
	BaseID           string `json:"base_id"`
	IncludeDocuments bool   `json:"include_documents"`
	Page             int    `json:"page"`
}

func (s *ToolServer) baseGet(ctx context.Context, raw json.RawMessage) (any, error) {
	var input baseGetInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	base, err := s.bases.GetBase(ctx, input.BaseID)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"documentBase": base}
	if input.IncludeDocuments {
		page := input.Page
		if page < 1 {
			page = 1
		}
		documents, err := s.bases.ListBaseDocuments(ctx, input.BaseID, page, false)
		if err != nil {
			return nil, err
		}
		result["documents"] = documents
	}
	return result, nil
}

type baseListDocumentsInput struct {
	BaseID         string `json:"base_id"`
	Page           int    `json:"page"`
	ExcludeUnknown bool   `json:"exclude_unknown"`
}

func (s *ToolServer) baseListDocuments(ctx context.Context, raw json.RawMessage) (any, error) {
	var input baseListDocumentsInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	page := input.Page
	if page < 1 {
		page = 1
	}
	documents, err := s.bases.ListBaseDocuments(ctx, input.BaseID, page, input.ExcludeUnknown)
	if err != nil {
		return nil, err
	}
	return documents, nil
}

type baseCreateInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (s *ToolServer) baseCreate(ctx context.Context, raw json.RawMessage) (any, error) {
	var input baseCreateInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	base, err := s.bases.CreateBase(ctx, input.Name, input.Description)
	if err != nil {
		return nil, err
	}
	return map[string]any{"documentBase": base}, nil
}

type baseUpdateInput struct {
	BaseID      string  `json:"base_id"`
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

func (s *ToolServer) baseUpdate(ctx context.Context, raw json.RawMessage) (any, error) {
	var input baseUpdateInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	if input.Name == nil && input.Description == nil {
		return nil, fmt.Errorf("%w: name or description is required", errInvalidToolInput)
	}
	base, err := s.bases.UpdateBase(ctx, input.BaseID, input.Name, input.Description)
	if err != nil {
		return nil, err
	}
	return map[string]any{"documentBase": base}, nil
}

type baseDeleteInput struct {
	BaseID  string `json:"base_id"`
	Confirm bool   `json:"confirm"`
}

func (s *ToolServer) baseDelete(ctx context.Context, raw json.RawMessage) (any, error) {
	var input baseDeleteInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	if !input.Confirm {
		return nil, fmt.Errorf("%w: confirm=true is required", errInvalidToolInput)
	}
	if err := s.bases.DeleteBase(ctx, input.BaseID); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": true, "baseId": input.BaseID}, nil
}

type baseMembershipInput struct {
	BaseID     string `json:"base_id"`
	DocumentID string `json:"document_id"`
}

func (s *ToolServer) baseAddDocument(ctx context.Context, raw json.RawMessage) (any, error) {
	var input baseMembershipInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	if err := s.bases.AddDocumentToBase(ctx, input.BaseID, input.DocumentID); err != nil {
		return nil, err
	}
	return map[string]any{"added": true}, nil
}

func (s *ToolServer) baseRemoveDocument(ctx context.Context, raw json.RawMessage) (any, error) {
	var input baseMembershipInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	if err := s.bases.RemoveDocumentFromBase(ctx, input.BaseID, input.DocumentID); err != nil {
		return nil, err
	}
	return map[string]any{"removed": true}, nil
}

type baseBibliographyGenerateInput struct {
	BaseID string `json:"base_id"`
}

func (s *ToolServer) baseBibliographyGenerate(ctx context.Context, raw json.RawMessage) (any, error) {
	var input baseBibliographyGenerateInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	if s.bibliography == nil {
		return nil, fmt.Errorf("bibliography service is unavailable")
	}
	requestID, _ := ctx.Value(types.RequestIDContextKey).(string)
	if strings.TrimSpace(requestID) == "" {
		requestID = uuid.NewString()
	}
	task, err := s.bibliography.EnqueueBaseBibliography(ctx, input.BaseID, requestID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"task": task}, nil
}

type baseBibliographyStatusInput struct {
	BaseID string `json:"base_id"`
	TaskID string `json:"task_id"`
}

func (s *ToolServer) baseBibliographyStatus(ctx context.Context, raw json.RawMessage) (any, error) {
	var input baseBibliographyStatusInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	if s.bibliography == nil {
		return nil, fmt.Errorf("bibliography service is unavailable")
	}
	task, err := s.bibliography.GetBaseBibliographyTask(ctx, input.BaseID, input.TaskID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"task": task}, nil
}
