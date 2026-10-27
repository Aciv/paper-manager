package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"
)

func documentImportOptions(options []types.DocumentImportOptions) (types.DocumentImportOptions, error) {
	if len(options) > 1 {
		return types.DocumentImportOptions{}, invalidImport("provide one import options object")
	}
	if len(options) == 1 {
		return options[0], nil
	}
	return types.DocumentImportOptions{}, nil
}

func validateImportOptionsBatch(count int, options [][]types.DocumentImportOptions) error {
	if len(options) == 0 {
		return nil
	}
	if len(options) != 1 || len(options[0]) != count {
		return invalidImport("provide exactly one import options object per item")
	}
	encoded, err := json.Marshal(options[0])
	if err != nil || len(encoded) > types.MaxDocumentImportPayloadBytes {
		return invalidImport("batch import options exceed 1 MiB")
	}
	for _, item := range options[0] {
		if _, err := item.Processing.SourceLookup.Normalized(); err != nil {
			return err
		}
		if _, err := item.DuplicateMetadata.Normalized(); err != nil {
			return err
		}
	}
	return nil
}

func batchImportOptions(options [][]types.DocumentImportOptions, index int) []types.DocumentImportOptions {
	if len(options) == 0 {
		return nil
	}
	return []types.DocumentImportOptions{options[0][index]}
}

func (s *documentService) finishImportedDocument(
	ctx context.Context, document *types.Document, options types.DocumentImportOptions, importErr error,
) (*types.Document, error) {
	duplicate := errors.Is(importErr, types.ErrDuplicateDOI) || errors.Is(importErr, types.ErrDuplicateFileHash)
	if importErr != nil && !duplicate {
		return document, importErr
	}
	if options.Metadata != nil {
		result := &types.DocumentImportMetadataResult{Status: "applied"}
		policy, _ := options.DuplicateMetadata.Normalized()
		applyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		var err error
		if duplicate && policy == types.DuplicateMetadataNone {
			result.Status = "skipped"
		} else {
			update := options.Metadata.PaperMetadataUpdate
			update.FillMissingOnly = duplicate && policy == types.DuplicateMetadataFillMissing
			if duplicate && options.Metadata.DOI != "" && document.DOI == "" {
				var applied bool
				applied, err = s.repo.SetDOIIfEmpty(applyCtx, document.ID, options.Metadata.DOI)
				if err == nil && applied {
					document.DOI = options.Metadata.DOI
				}
			}
			if err == nil && update.HasDataChanges() {
				if s.papers == nil {
					err = errors.New("paper metadata service is unavailable")
				} else {
					_, err = s.papers.UpdateMetadata(applyCtx, document.ID.String(), update)
				}
			}
			if err != nil {
				result.Status, result.Error = "failed", "METADATA_APPLY_FAILED"
				logger.Warn(ctx, "apply imported metadata failed", "document_id", document.ID, "error", err)
			}
		}
		document.ImportMetadata = result
	}
	if duplicate {
		return document, importErr
	}
	processed, err := s.startCreatedDocumentProcessing(ctx, *document)
	if processed != nil {
		processed.ImportMetadata = document.ImportMetadata
	}
	return processed, err
}

// normalizeDocumentImport runs before storage or download. The returned options
// own their pointers and slices: normalizing one item cannot mutate a caller's
// metadata or another batch item that shares the same input object.
func normalizeDocumentImport(
	doi string, options types.DocumentImportOptions,
) (string, types.DocumentImportOptions, error) {
	mode, err := options.Processing.SourceLookup.Normalized()
	if err != nil {
		return "", options, err
	}
	policy, err := options.DuplicateMetadata.Normalized()
	if err != nil {
		return "", options, err
	}
	normalized := options
	normalized.Processing.SourceLookup = mode
	normalized.DuplicateMetadata = policy
	if normalized.Metadata != nil {
		if err := validateImportMetadata(normalized.Metadata); err != nil {
			return "", options, err
		}
		encoded, err := json.Marshal(normalized.Metadata)
		if err != nil || len(encoded) > types.MaxDocumentImportMetadataBytes {
			return "", options, invalidImport("metadata exceeds 64 KiB or cannot be encoded")
		}
		var metadata types.PaperMetadataImport
		if err := json.Unmarshal(encoded, &metadata); err != nil {
			return "", options, invalidImport("metadata cannot be decoded")
		}
		update, err := normalizePaperMetadataUpdate(&metadata.PaperMetadataUpdate)
		if err != nil {
			return "", options, invalidImport("metadata contains invalid values")
		}
		metadata.PaperMetadataUpdate = *update
		source := types.PaperMetadataSourceManual
		metadata.MetadataSource = &source
		normalized.Metadata = &metadata
	}
	canonicalDOI, err := normalizeImportDOI(doi)
	if err != nil {
		return "", options, err
	}
	if normalized.Metadata != nil {
		metadataDOI, err := normalizeImportDOI(normalized.Metadata.DOI)
		if err != nil {
			return "", options, err
		}
		if canonicalDOI != "" && metadataDOI != "" && canonicalDOI != metadataDOI {
			return "", options, invalidImport("top-level and metadata DOI disagree")
		}
		normalized.Metadata.DOI = metadataDOI
		if canonicalDOI == "" {
			canonicalDOI = metadataDOI
		}
	}
	return canonicalDOI, normalized, nil
}

func normalizeImportDOI(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	if !validImportText(value, 2048) {
		return "", invalidImport("DOI contains invalid characters or exceeds its limit")
	}
	normalized := types.NormalizeDOI(value)
	if normalized == "" || len(normalized) > maxDocumentDOILength {
		return "", invalidImport("DOI must be a valid DOI up to 255 bytes")
	}
	return normalized, nil
}

func validateImportMetadata(metadata *types.PaperMetadataImport) error {
	if !validImportText(metadata.DOI, 2048) {
		return invalidImport("metadata DOI contains invalid characters or exceeds its limit")
	}
	for _, field := range []struct {
		value *string
		limit int
	}{
		{metadata.Title, 4096}, {metadata.Abstract, 32 << 10},
		{metadata.SourceName, 1024}, {metadata.SourceURL, 2048},
		{metadata.Venue, 1024}, {metadata.Volume, 128},
		{metadata.Issue, 128}, {metadata.Pages, 128}, {metadata.Language, 16},
	} {
		if field.value != nil && !validImportText(*field.value, field.limit) {
			return invalidImport("metadata text contains invalid characters or exceeds its limit")
		}
	}
	if metadata.Authors != nil {
		if len(*metadata.Authors) > 100 {
			return invalidImport("metadata may contain at most 100 authors")
		}
		for _, author := range *metadata.Authors {
			if strings.TrimSpace(author.Name) == "" || !validImportText(author.Name, 512) ||
				!validImportText(author.Affiliation, 2048) {
				return invalidImport("author name or affiliation is invalid")
			}
		}
	}
	if metadata.Tags != nil {
		if _, err := normalizePaperTags(*metadata.Tags); err != nil {
			return invalidImport("metadata tags are invalid")
		}
		for _, tag := range *metadata.Tags {
			if !validImportText(tag, 256) {
				return invalidImport("metadata tag contains invalid characters")
			}
		}
	}
	if metadata.SourceURL != nil && strings.TrimSpace(*metadata.SourceURL) != "" {
		parsed, err := url.Parse(strings.TrimSpace(*metadata.SourceURL))
		if err != nil || parsed.Hostname() == "" || parsed.User != nil ||
			(parsed.Scheme != "http" && parsed.Scheme != "https") {
			return invalidImport("sourceUrl must be an absolute HTTP or HTTPS URL without credentials")
		}
	}
	if metadata.PublishedAt != nil &&
		(metadata.PublishedAt.Year() < 1 || metadata.PublishedAt.Year() > 9999) {
		return invalidImport("publishedAt must be a valid calendar date")
	}
	return nil
}

func validImportText(value string, maxBytes int) bool {
	if len(value) > maxBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < ' ' || character == '\x7f' {
			return false
		}
	}
	return true
}

func invalidImport(message string) error {
	return fmt.Errorf("%w: %s", types.ErrInvalidDocumentImport, message)
}
