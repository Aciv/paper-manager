package types

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

const (
	DocumentOutlineSchemaVersion  = 1
	MaxDocumentOutlineItems       = 512
	MaxDocumentOutlineDepth       = 8
	MaxDocumentOutlineTitleRunes  = 512
	MaxDocumentOutlineDiagnostics = 32
)

type DocumentOutlineSource string

const (
	DocumentOutlinePDF         DocumentOutlineSource = "pdf_outline"
	DocumentOutlineMarkdown    DocumentOutlineSource = "markdown_headings"
	DocumentOutlineNone        DocumentOutlineSource = "none"
	DocumentOutlineUnavailable DocumentOutlineSource = "unavailable"
)

type DocumentOutline struct {
	SchemaVersion int                      `json:"schemaVersion"`
	Source        DocumentOutlineSource    `json:"source"`
	Sections      []DocumentOutlineSection `json:"sections"`
	Diagnostics   []string                 `json:"diagnostics,omitempty"`
}

type DocumentOutlineSection struct {
	Ordinal                int     `json:"ordinal"`
	Title                  string  `json:"title"`
	Level                  int     `json:"level"`
	ParentOrdinal          int     `json:"parentOrdinal"`
	StartPage              int     `json:"startPage"`
	StartX                 float64 `json:"startX"`
	StartY                 float64 `json:"startY"`
	EndPage                int     `json:"endPage"`
	EndX                   float64 `json:"endX"`
	EndY                   float64 `json:"endY"`
	StartBlockOrdinal      int     `json:"startBlockOrdinal"`
	EndBlockOrdinal        int     `json:"endBlockOrdinal"`
	MarkdownCodepointStart int     `json:"markdownCodepointStart"`
	MarkdownCodepointEnd   int     `json:"markdownCodepointEnd"`
}

func LegacyDocumentOutline() DocumentOutline {
	return DocumentOutline{SchemaVersion: 0, Source: DocumentOutlineUnavailable,
		Sections: []DocumentOutlineSection{}, Diagnostics: []string{"legacy_missing_outline"}}
}

func (o DocumentOutline) JSON() (JSON, error) {
	data, err := json.Marshal(o)
	if err != nil {
		return nil, err
	}
	return JSON(data), nil
}

func ParseDocumentOutline(data JSON) (DocumentOutline, error) {
	if len(data) == 0 || string(data) == "null" || string(data) == "{}" {
		return LegacyDocumentOutline(), nil
	}
	var outline DocumentOutline
	if err := json.Unmarshal(data, &outline); err != nil {
		return DocumentOutline{}, fmt.Errorf("decode document outline: %w", err)
	}
	return outline, nil
}

// ValidateRecognitionOutline rejects malformed parser projections before any
// part of their generation is made durable.
func ValidateRecognitionOutline(outline DocumentOutline, markdown string, blocks []RecognitionBlock, pageCount int) error {
	if outline.SchemaVersion != DocumentOutlineSchemaVersion {
		return fmt.Errorf("unsupported outline schema version %d", outline.SchemaVersion)
	}
	if outline.Source != DocumentOutlinePDF && outline.Source != DocumentOutlineMarkdown && outline.Source != DocumentOutlineNone {
		return fmt.Errorf("unknown outline source %q", outline.Source)
	}
	if len(outline.Sections) > MaxDocumentOutlineItems || len(outline.Diagnostics) > MaxDocumentOutlineDiagnostics {
		return errors.New("outline exceeds item or diagnostic limit")
	}
	if outline.Source == DocumentOutlineNone && len(outline.Sections) != 0 {
		return errors.New("outline source none cannot contain sections")
	}
	if outline.Source != DocumentOutlineNone && len(outline.Sections) == 0 {
		return errors.New("selected outline source contains no sections")
	}
	markdownRunes := utf8.RuneCountInString(markdown)
	pageDimensions := make(map[int][2]float64)
	if len(outline.Sections) == 0 {
		return nil
	}
	for index, block := range blocks {
		if block.Ordinal != index || block.Location.PageNumber == nil || block.Location.PageWidth == nil || block.Location.PageHeight == nil ||
			block.Location.MarkdownCodepointStart == nil || block.Location.MarkdownCodepointEnd == nil {
			return fmt.Errorf("recognition block %d cannot support outline ranges", index)
		}
		if *block.Location.PageNumber < 1 || *block.Location.PageNumber > pageCount || *block.Location.PageWidth <= 0 || *block.Location.PageHeight <= 0 ||
			*block.Location.MarkdownCodepointStart < 0 || *block.Location.MarkdownCodepointEnd < *block.Location.MarkdownCodepointStart ||
			*block.Location.MarkdownCodepointEnd > markdownRunes {
			return fmt.Errorf("recognition block %d has invalid source bounds", index)
		}
		pageDimensions[*block.Location.PageNumber] = [2]float64{*block.Location.PageWidth, *block.Location.PageHeight}
	}
	lastBlock, lastMarkdown := -1, -1
	for index, section := range outline.Sections {
		if section.Ordinal != index || strings.TrimSpace(section.Title) == "" || utf8.RuneCountInString(section.Title) > MaxDocumentOutlineTitleRunes {
			return fmt.Errorf("outline section %d has invalid identity", index)
		}
		if section.Level < 1 || section.Level > MaxDocumentOutlineDepth {
			return fmt.Errorf("outline section %d has invalid level", index)
		}
		if section.ParentOrdinal >= index || section.ParentOrdinal < -1 {
			return fmt.Errorf("outline section %d has invalid parent", index)
		}
		if section.ParentOrdinal == -1 && section.Level != 1 {
			return fmt.Errorf("outline section %d root must have level one", index)
		}
		if section.ParentOrdinal >= 0 && outline.Sections[section.ParentOrdinal].Level >= section.Level {
			return fmt.Errorf("outline section %d parent level is invalid", index)
		}
		if !finiteNonNegative(section.StartX, section.StartY, section.EndX, section.EndY) {
			return fmt.Errorf("outline section %d has invalid mapping", index)
		}
		if section.StartPage < 0 || section.EndPage < 0 || section.StartPage > pageCount || section.EndPage > pageCount ||
			(section.StartPage > 0 && section.EndPage > 0 && section.EndPage < section.StartPage) {
			return fmt.Errorf("outline section %d has invalid page range", index)
		}
		if dimensions, ok := pageDimensions[section.StartPage]; ok && (section.StartX > dimensions[0] || section.StartY > dimensions[1]) {
			return fmt.Errorf("outline section %d start position exceeds page bounds", index)
		}
		if dimensions, ok := pageDimensions[section.EndPage]; ok && (section.EndX > dimensions[0] || section.EndY > dimensions[1]) {
			return fmt.Errorf("outline section %d end position exceeds page bounds", index)
		}
		mappedBlocks := section.StartBlockOrdinal >= 0 || section.EndBlockOrdinal >= 0
		if mappedBlocks {
			if section.StartBlockOrdinal < 0 || section.EndBlockOrdinal < section.StartBlockOrdinal || section.EndBlockOrdinal > len(blocks) || section.StartBlockOrdinal < lastBlock {
				return fmt.Errorf("outline section %d has invalid block range", index)
			}
			lastBlock = section.StartBlockOrdinal
		} else if section.StartBlockOrdinal != -1 || section.EndBlockOrdinal != -1 {
			return fmt.Errorf("outline section %d has partial block range", index)
		}
		mappedMarkdown := section.MarkdownCodepointStart >= 0 || section.MarkdownCodepointEnd >= 0
		if mappedMarkdown {
			if section.MarkdownCodepointStart < 0 || section.MarkdownCodepointEnd < section.MarkdownCodepointStart || section.MarkdownCodepointEnd > markdownRunes || section.MarkdownCodepointStart < lastMarkdown {
				return fmt.Errorf("outline section %d has invalid Markdown range", index)
			}
			lastMarkdown = section.MarkdownCodepointStart
		} else if section.MarkdownCodepointStart != -1 || section.MarkdownCodepointEnd != -1 {
			return fmt.Errorf("outline section %d has partial Markdown range", index)
		}
	}
	return nil
}

func finiteNonNegative(values ...float64) bool {
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return false
		}
	}
	return true
}

// DocumentReadingStructure is the disposable read model for the persisted
// source outline. Reading-note sections and evidence links are separate: notes
// are read from their manifest/section files, and evidence links are interpreted
// by their consumers.
type DocumentReadingStructure struct {
	SchemaVersion         int             `json:"schemaVersion"`
	DocumentID            string          `json:"documentId"`
	RecognitionGeneration int64           `json:"recognitionGeneration"`
	Fingerprint           string          `json:"fingerprint"`
	Outline               DocumentOutline `json:"outline"`
	Diagnostics           []string        `json:"diagnostics,omitempty"`
}
