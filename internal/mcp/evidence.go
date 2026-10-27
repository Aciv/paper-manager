package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

type evidenceRect struct {
	X0 float64 `json:"x0"`
	Y0 float64 `json:"y0"`
	X1 float64 `json:"x1"`
	Y1 float64 `json:"y1"`
}

type evidenceTarget struct {
	DocumentID string         `json:"documentId,omitempty"`
	Page       int            `json:"page"`
	Rects      []evidenceRect `json:"rects,omitempty"`
	BlockID    string         `json:"blockId,omitempty"`
	Section    string         `json:"section,omitempty"`
}

type evidenceValidation struct {
	Target       *evidenceTarget `json:"target,omitempty"`
	Valid        bool            `json:"valid"`
	PageCount    int             `json:"pageCount,omitempty"`
	RangeChecked bool            `json:"range_checked"`
	Diagnostics  []string        `json:"diagnostics,omitempty"`
}

func parseEvidenceHref(href string) (evidenceTarget, []string) {
	diagnostics := []string{}
	parsed, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return evidenceTarget{}, []string{"evidence link is not a valid URL"}
	}
	if parsed.Scheme != "papers-manager" {
		return evidenceTarget{}, []string{"evidence link scheme must be papers-manager"}
	}
	target := evidenceTarget{}
	if parsed.User != nil || parsed.Fragment != "" {
		diagnostics = append(diagnostics, "evidence link must not contain user information or a fragment")
	}
	query, queryErr := url.ParseQuery(parsed.RawQuery)
	if queryErr != nil {
		diagnostics = append(diagnostics, "evidence query is malformed")
	}
	for _, name := range []string{"v", "page", "block", "section"} {
		if len(query[name]) > 1 {
			diagnostics = append(diagnostics, "evidence query repeats "+name)
		}
	}
	switch parsed.Host {
	case "self":
		if parsed.Path != "/evidence" {
			diagnostics = append(diagnostics, "self evidence path must be /evidence")
		}
	case "document":
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if len(parts) != 2 || parts[1] != "evidence" {
			diagnostics = append(diagnostics, "document evidence path must be /{uuid}/evidence")
		} else if _, err := uuid.Parse(parts[0]); err != nil {
			diagnostics = append(diagnostics, "document evidence target is not a UUID")
		} else {
			target.DocumentID = parts[0]
		}
	default:
		diagnostics = append(diagnostics, "unknown evidence link host")
	}
	if query.Get("v") != "1" {
		diagnostics = append(diagnostics, "unsupported evidence link version")
	}
	pageText := query.Get("page")
	page, err := strconv.Atoi(pageText)
	if err != nil || page < 1 {
		diagnostics = append(diagnostics, "evidence link has no valid page")
	} else {
		target.Page = page
	}
	for index, raw := range query["rect"] {
		parts := strings.Split(raw, ",")
		if len(parts) != 4 {
			diagnostics = append(diagnostics, fmt.Sprintf("evidence rect %d is malformed", index+1))
			continue
		}
		values := make([]float64, 4)
		valid := true
		for i, part := range parts {
			value, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
				valid = false
				break
			}
			values[i] = value
		}
		rect := evidenceRect{X0: values[0], Y0: values[1], X1: values[2], Y1: values[3]}
		if !valid || rect.X0 < 0 || rect.Y0 < 0 || rect.X1 > 1 || rect.Y1 > 1 ||
			rect.X0 >= rect.X1 || rect.Y0 >= rect.Y1 {
			diagnostics = append(diagnostics, fmt.Sprintf("evidence rect %d is outside the normalized page", index+1))
			continue
		}
		target.Rects = append(target.Rects, rect)
	}
	target.BlockID = strings.TrimSpace(query.Get("block"))
	target.Section = strings.TrimSpace(query.Get("section"))
	return target, diagnostics
}

// validateEvidence checks parameters and known page bounds, not target existence
// or semantic support. A well-formed block UUID is never looked up.
func (s *ToolServer) validateEvidence(ctx context.Context, documentID, href string) (evidenceValidation, error) {
	if s.reading == nil {
		return evidenceValidation{}, fmt.Errorf("document reading service is unavailable")
	}
	return s.validateEvidenceWithPageCount(ctx, documentID, href, s.reading.GetDocumentPageCount)
}

func (s *ToolServer) validateEvidenceWithPageCount(
	ctx context.Context, documentID, href string,
	pageCount func(context.Context, string) (int, error),
) (evidenceValidation, error) {
	target, diagnostics := parseEvidenceHref(href)
	if target.BlockID != "" {
		if _, err := uuid.Parse(target.BlockID); err != nil {
			diagnostics = append(diagnostics, "evidence block id is not a UUID")
		}
	}
	result := evidenceValidation{Target: &target, Diagnostics: diagnostics}
	if len(diagnostics) > 0 {
		return result, nil
	}
	validationID := documentID
	if target.DocumentID != "" {
		validationID = target.DocumentID
	}
	count, err := pageCount(ctx, validationID)
	if err != nil {
		return result, err
	}
	result.PageCount = count
	result.RangeChecked = count > 0
	result.Valid = true
	if count > 0 && target.Page > count {
		result.Valid = false
		result.Diagnostics = append(result.Diagnostics, "evidence page is outside the document")
	} else if count <= 0 {
		result.Diagnostics = append(result.Diagnostics, "page count unknown; page range was not checked")
	}
	return result, nil
}

type evidenceBatchItem struct {
	Index      int                 `json:"index"`
	Status     string              `json:"status"`
	Validation *evidenceValidation `json:"validation,omitempty"`
	Error      *toolFailureDetails `json:"error,omitempty"`
}

type evidenceBatchResult struct {
	Status  string              `json:"status"`
	Items   []evidenceBatchItem `json:"items"`
	Summary map[string]int      `json:"summary"`
}

func (s *ToolServer) evidenceValidateMany(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		Items []evidenceValidateInput `json:"items"`
	}
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	if len(input.Items) == 0 || len(input.Items) > 100 {
		return nil, errInvalidToolInput
	}
	// Fetch every distinct document's bounds in one metadata projection query.
	ids := make([]string, 0, len(input.Items))
	seen := make(map[string]bool)
	for _, item := range input.Items {
		target, diagnostics := parseEvidenceHref(item.Href)
		if len(diagnostics) > 0 {
			continue
		}
		if target.BlockID != "" {
			if _, err := uuid.Parse(target.BlockID); err != nil {
				continue
			}
		}
		id := item.DocumentID
		if target.DocumentID != "" {
			id = target.DocumentID
		}
		parsed, err := uuid.Parse(id)
		if err != nil {
			return nil, errInvalidToolInput
		}
		id = parsed.String()
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	counts := map[string]int{}
	var lookupErr error
	if len(ids) > 0 {
		if s.reading == nil {
			return nil, fmt.Errorf("document reading service is unavailable")
		}
		counts, lookupErr = s.reading.GetDocumentPageCounts(ctx, ids)
	}
	lookup := func(_ context.Context, id string) (int, error) {
		if lookupErr != nil {
			return 0, lookupErr
		}
		parsed, err := uuid.Parse(id)
		if err != nil {
			return 0, errInvalidToolInput
		}
		count, ok := counts[parsed.String()]
		if !ok {
			return 0, sql.ErrNoRows
		}
		return count, nil
	}
	result := evidenceBatchResult{
		Status: "succeeded", Items: make([]evidenceBatchItem, 0, len(input.Items)),
		Summary: map[string]int{"total": len(input.Items), "succeeded": 0, "partial": 0, "failed": 0},
	}
	for index, item := range input.Items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		validation, err := s.validateEvidenceWithPageCount(ctx, item.DocumentID, item.Href, lookup)
		entry := evidenceBatchItem{Index: index, Status: "succeeded", Validation: &validation}
		if err != nil {
			failure := classifyToolError(err)
			entry.Error, entry.Validation, entry.Status = &failure, nil, "failed"
		} else if !validation.Valid {
			entry.Status = "failed"
		} else if !validation.RangeChecked {
			entry.Status = "partial"
		}
		result.Summary[entry.Status]++
		result.Items = append(result.Items, entry)
	}
	if result.Summary["failed"] == len(input.Items) {
		result.Status = "failed"
	} else if result.Summary["succeeded"] != len(input.Items) {
		result.Status = "partial"
	}
	return result, nil
}
