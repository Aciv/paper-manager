package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type processingRetryInput struct {
	DocumentID string `json:"document_id"`
	Target     string `json:"target"`
}

// processingRetry exposes the same independent recovery actions as the web
// UI. "auto" keeps the document service's existing retry selection, while the
// other targets map to exactly one stage.
func (s *ToolServer) processingRetry(ctx context.Context, raw json.RawMessage) (any, error) {
	var input processingRetryInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	target := strings.ToLower(strings.TrimSpace(input.Target))
	if target == "" {
		target = "auto"
	}
	if s.documents == nil {
		return nil, fmt.Errorf("document service is unavailable")
	}
	var document any
	switch target {
	case "auto":
		updated, err := s.documents.RetryDocumentProcess(ctx, input.DocumentID)
		if err != nil {
			return nil, err
		}
		document = updated
	case "markdown":
		updated, err := s.documents.RetryDocumentParse(ctx, input.DocumentID)
		if err != nil {
			return nil, err
		}
		document = updated
	case "extraction", "catalog", "reference":
		if s.paperInfo == nil {
			return nil, fmt.Errorf("paper information service is unavailable")
		}
		var err error
		switch target {
		case "extraction":
			err = s.paperInfo.RetryExtraction(ctx, input.DocumentID)
		case "catalog":
			err = s.paperInfo.RetryCatalogResolution(ctx, input.DocumentID)
		case "reference":
			err = s.paperInfo.RetryReferenceResolution(ctx, input.DocumentID)
		}
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("%w: target must be auto, markdown, extraction, catalog, or reference", errInvalidToolInput)
	}
	result := map[string]any{
		"accepted": true, "documentId": input.DocumentID, "target": target,
	}
	if document != nil {
		result["document"] = document
	}
	return result, nil
}

type processingCancelInput struct {
	DocumentID string `json:"document_id"`
	Target     string `json:"target"`
}

// processingCancel fences only the selected lane. Cancelling a document parse
// does not stop paper extraction, and vice versa.
func (s *ToolServer) processingCancel(ctx context.Context, raw json.RawMessage) (any, error) {
	var input processingCancelInput
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	target := strings.ToLower(strings.TrimSpace(input.Target))
	if target == "" {
		target = "markdown"
	}
	switch target {
	case "markdown":
		if s.documents == nil {
			return nil, fmt.Errorf("document service is unavailable")
		}
		if _, err := s.documents.CancelDocumentParse(ctx, input.DocumentID); err != nil {
			return nil, err
		}
	case "paper_info":
		if s.paperInfo == nil {
			return nil, fmt.Errorf("paper information service is unavailable")
		}
		if err := s.paperInfo.Cancel(ctx, input.DocumentID); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("%w: target must be markdown or paper_info", errInvalidToolInput)
	}
	return map[string]any{
		"accepted": true, "documentId": input.DocumentID, "target": target,
	}, nil
}
