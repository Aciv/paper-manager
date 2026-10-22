package mcp

import (
	"context"
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
	Target      *evidenceTarget `json:"target,omitempty"`
	Valid       bool            `json:"valid"`
	PageCount   int             `json:"pageCount,omitempty"`
	BlockFound  bool            `json:"blockFound,omitempty"`
	Diagnostics []string        `json:"diagnostics,omitempty"`
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
	if parsed.Query().Get("v") != "1" {
		diagnostics = append(diagnostics, "unsupported evidence link version")
	}
	pageText := parsed.Query().Get("page")
	page, err := strconv.Atoi(pageText)
	if err != nil || page < 1 {
		diagnostics = append(diagnostics, "evidence link has no valid page")
	} else {
		target.Page = page
	}
	for index, raw := range parsed.Query()["rect"] {
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
	target.BlockID = strings.TrimSpace(parsed.Query().Get("block"))
	target.Section = strings.TrimSpace(parsed.Query().Get("section"))
	return target, diagnostics
}

func (s *ToolServer) validateEvidence(ctx context.Context, documentID, href string) (evidenceValidation, error) {
	if s.reading == nil {
		return evidenceValidation{}, fmt.Errorf("document reading service is unavailable")
	}
	target, diagnostics := parseEvidenceHref(href)
	if target.Page < 1 {
		return evidenceValidation{Target: &target, Diagnostics: diagnostics}, nil
	}
	validationDocumentID := documentID
	if target.DocumentID != "" {
		validationDocumentID = target.DocumentID
	}
	markdown, err := s.reading.GetDocumentMarkdown(ctx, validationDocumentID)
	if err != nil {
		return evidenceValidation{}, err
	}
	result := evidenceValidation{
		Target: &target, PageCount: markdown.PageCount,
		Diagnostics: diagnostics,
	}
	if markdown.PageCount > 0 && target.Page > markdown.PageCount {
		result.Diagnostics = append(result.Diagnostics, "evidence page is outside the document")
	}
	if target.BlockID != "" {
		if _, err := uuid.Parse(target.BlockID); err != nil {
			result.Diagnostics = append(result.Diagnostics, "evidence block id is not a UUID")
		} else {
			blocks, err := s.reading.GetDocumentBlocks(ctx, validationDocumentID, target.Page)
			if err != nil {
				return evidenceValidation{}, err
			}
			for _, block := range blocks {
				if block.ID.String() == target.BlockID {
					result.BlockFound = true
					break
				}
			}
			if !result.BlockFound {
				result.Diagnostics = append(result.Diagnostics, "evidence block was not found on that page")
			}
		}
	}
	result.Valid = len(result.Diagnostics) == 0
	return result, nil
}
