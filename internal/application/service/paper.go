package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
)

const (
	maxPaperRawReferenceBytes = 64 << 10
	// localTitleSeedBatchSize bounds one batched title lookup so a very large
	// bibliography cannot build an unbounded LATERAL query.
	localTitleSeedBatchSize = 200
)

var (
	// ErrInvalidPaperMetadata indicates a malformed bibliographic patch or
	// search request.
	ErrInvalidPaperMetadata = errors.New("invalid paper metadata")
	// ErrPaperCannotCite prevents documents without an available source file
	// from acting as source papers in the citation graph.
	ErrPaperCannotCite = errors.New("paper without an available source cannot cite documents")
	// ErrCitationTargetNotResolved means a citation was given a document whose
	// source file is not available.
	ErrCitationTargetNotResolved = errors.New("citation target is not a resolved document")
)

type paperService struct {
	documents interfaces.DocumentRepository
	metadata  interfaces.PaperMetadataRepository
	citations interfaces.PaperCitationRepository
}

// NewPaperService creates the paper-domain boundary from its persistence
// abstractions. It deliberately does not depend on DocumentService.
func NewPaperService(
	documents interfaces.DocumentRepository,
	metadata interfaces.PaperMetadataRepository,
	citations interfaces.PaperCitationRepository,
) interfaces.PaperService {
	return &paperService{
		documents: documents,
		metadata:  metadata,
		citations: citations,
	}
}

func (s *paperService) GetMetadata(
	ctx context.Context, documentID string,
) (*types.PaperMetadata, error) {
	id, err := parsePaperDocumentID(documentID)
	if err != nil {
		return nil, err
	}
	if _, err := s.documents.Get(ctx, id); err != nil {
		return nil, err
	}
	metadata, err := s.metadata.GetByDocumentID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return &types.PaperMetadata{
			DocumentID:     id,
			Authors:        []types.PaperAuthor{},
			Tags:           []string{},
			Metadata:       types.JSON(`{}`),
			MetadataSource: types.PaperMetadataSourceUnknown,
		}, nil
	}
	if err != nil {
		return nil, err
	}
	return &metadata, nil
}

func (s *paperService) GetMetadataBatch(
	ctx context.Context, documentIDs []uuid.UUID,
) (map[uuid.UUID]types.PaperMetadata, error) {
	return s.metadata.GetByDocumentIDs(ctx, documentIDs)
}

func (s *paperService) UpdateMetadata(
	ctx context.Context,
	documentID string,
	update types.PaperMetadataUpdate,
) (*types.PaperMetadata, error) {
	if !update.HasDataChanges() {
		return nil, errors.New("at least one paper metadata field is required")
	}
	id, err := parsePaperDocumentID(documentID)
	if err != nil {
		return nil, err
	}
	if _, err := s.documents.Get(ctx, id); err != nil {
		return nil, err
	}
	normalized, err := normalizePaperMetadataUpdate(&update)
	if err != nil {
		return nil, err
	}
	if normalized.MetadataSource == nil {
		source := types.PaperMetadataSourceManual
		normalized.MetadataSource = &source
	}
	updated, err := s.metadata.Upsert(ctx, id, *normalized)
	if err != nil {
		return nil, err
	}
	logger.Info(ctx, "paper metadata update completed",
		"document_id", id,
		"metadata_source", updated.MetadataSource)
	return &updated, nil
}

func (s *paperService) SearchMetadata(
	ctx context.Context, search types.PaperSearch,
) (types.PaperMetadataPage, error) {
	search.Title = strings.TrimSpace(search.Title)
	search.Author = strings.TrimSpace(search.Author)
	if search.TagMode == "" {
		search.TagMode = types.PaperTagModeAll
	}
	if search.TagMode != types.PaperTagModeAll && search.TagMode != types.PaperTagModeAny {
		return types.PaperMetadataPage{}, fmt.Errorf("invalid tag mode %q", search.TagMode)
	}
	if paperSearchTermIsTooShort(search.Title) {
		return types.PaperMetadataPage{}, fmt.Errorf(
			"%w: title search requires at least two characters", ErrInvalidPaperMetadata)
	}
	if paperSearchTermIsTooShort(search.Author) {
		return types.PaperMetadataPage{}, fmt.Errorf(
			"%w: author search requires at least two characters", ErrInvalidPaperMetadata)
	}
	tags, err := normalizePaperTags(search.Tags)
	if err != nil {
		return types.PaperMetadataPage{}, err
	}
	search.Tags = tags
	return s.metadata.Search(ctx, search)
}

// paperSearchTermIsTooShort rejects a lone ASCII character such as "a". It
// matches most rows, cannot use the trigram index effectively, and turns the
// search into a sequential scan. A single CJK character is a meaningful
// surname/title query and is allowed; PostgreSQL decides whether its trigram
// is selective enough to index.
func paperSearchTermIsTooShort(term string) bool {
	runes := []rune(term)
	return len(runes) == 1 && runes[0] < utf8.RuneSelf
}

func (s *paperService) AddCitation(
	ctx context.Context,
	citingDocumentID string,
	input types.PaperCitationInput,
) (*types.PaperCitation, error) {
	citingID, _, err := s.requireCitingDocument(ctx, citingDocumentID)
	if err != nil {
		return nil, err
	}
	rawReference, err := validateRawReference(input.RawReference)
	if err != nil {
		return nil, err
	}
	citation := newPaperCitation(citingID, rawReference)
	citation.Origin = types.PaperCitationOriginManual
	if input.CitedDocumentID != nil {
		if err := s.applyCitationTarget(ctx, &citation, *input.CitedDocumentID); err != nil {
			return nil, err
		}
	}
	citation, err = s.applyReferenceCandidate(citation, input.Candidate, rawReference)
	if err != nil {
		return nil, err
	}
	saved, err := s.citations.AddCitation(ctx, citation)
	if err != nil {
		return nil, err
	}
	logger.Info(ctx, "paper citation add completed",
		"citing_document_id", citingID,
		"reference_id", saved.ReferenceID,
		"unresolved", saved.CitedDocumentID == nil)
	return &saved, nil
}

func (s *paperService) ReplaceCitations(
	ctx context.Context,
	citingDocumentID string,
	inputs []types.PaperCitationInput,
) ([]types.PaperCitation, error) {
	citingID, _, err := s.requireCitingDocument(ctx, citingDocumentID)
	if err != nil {
		return nil, err
	}
	previous, err := s.citations.ListByCitingDocumentID(ctx, citingID)
	if err != nil {
		return nil, err
	}
	previousByKey := citationsByReferenceKey(previous)

	replacements := make([]types.PaperCitation, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		rawReference, validateErr := validateRawReference(input.RawReference)
		if validateErr != nil {
			return nil, validateErr
		}
		key := types.PaperCitationReferenceKey(rawReference)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		citation := previousByKey[key]
		if citation.ReferenceID == uuid.Nil {
			citation = newPaperCitation(citingID, rawReference)
		}
		citation.Origin = types.PaperCitationOriginManual
		citation.RawReference = rawReference
		if input.CitedDocumentID != nil {
			if applyErr := s.applyCitationTarget(ctx, &citation, *input.CitedDocumentID); applyErr != nil {
				return nil, applyErr
			}
		}
		citation, validateErr = s.applyReferenceCandidate(citation, input.Candidate, rawReference)
		if validateErr != nil {
			return nil, validateErr
		}
		replacements = append(replacements, citation)
	}

	if err := s.citations.ReplaceCitations(ctx, citingID, replacements); err != nil {
		return nil, err
	}
	logger.Info(ctx, "paper citation replacement completed",
		"citing_document_id", citingID,
		"citation_count", len(replacements))
	return replacements, nil
}

// MergeExtractedCitations reconciles one positively detected bibliography.
// Each row belongs to the citing paper; manual rows are never removed by
// automation, and existing resolved targets are retained.
func (s *paperService) MergeExtractedCitations(
	ctx context.Context,
	citingDocumentID string,
	inputs []types.PaperCitationInput,
) ([]types.PaperCitation, error) {
	citingID, _, err := s.requireCitingDocument(ctx, citingDocumentID)
	if err != nil {
		return nil, err
	}
	previous, err := s.citations.ListByCitingDocumentID(ctx, citingID)
	if err != nil {
		return nil, err
	}
	previousByKey := citationsByReferenceKey(previous)

	automatic := make([]types.PaperCitation, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		rawReference, validateErr := validateRawReference(input.RawReference)
		if validateErr != nil {
			return nil, validateErr
		}
		key := types.PaperCitationReferenceKey(rawReference)
		if _, seen := seen[key]; seen {
			continue
		}
		seen[key] = struct{}{}

		existing, exists := previousByKey[key]
		if exists && existing.EffectiveOrigin() == types.PaperCitationOriginManual {
			continue
		}
		citation := existing
		if !exists {
			citation = newPaperCitation(citingID, rawReference)
		}
		citation.Origin = types.PaperCitationOriginExtraction
		citation.RawReference = rawReference
		if input.CitedDocumentID != nil {
			if applyErr := s.applyCitationTarget(ctx, &citation, *input.CitedDocumentID); applyErr != nil {
				return nil, applyErr
			}
		}
		citation, err = s.applyReferenceCandidate(citation, input.Candidate, rawReference)
		if err != nil {
			return nil, err
		}
		automatic = append(automatic, citation)
	}

	// Python extraction has already supplied DOI/title. Match the whole
	// bibliography against the local library in two batched queries before any
	// provider or queued resolution is considered.
	pendingIndexes := make([]int, 0, len(automatic))
	pendingReferences := make([]types.PaperReference, 0, len(automatic))
	for index := range automatic {
		if automatic[index].CitedDocumentID != nil {
			continue
		}
		pendingIndexes = append(pendingIndexes, index)
		pendingReferences = append(pendingReferences, automatic[index].PaperReference)
	}
	if len(pendingReferences) > 0 {
		matches, matchErr := s.findLocalReferenceMatches(ctx, pendingReferences, citingID)
		if matchErr != nil {
			return nil, matchErr
		}
		for offset, index := range pendingIndexes {
			if matches[offset] == uuid.Nil {
				continue
			}
			documentID := matches[offset]
			automatic[index].CitedDocumentID = &documentID
			automatic[index].ResolutionStatus = types.DocumentStageSucceeded
			automatic[index].ResolutionErrorCode = nil
			automatic[index].ResolutionErrorMsg = nil
			automatic[index].ResolutionUpdatedAt = time.Now().UTC()
		}
	}

	merged, err := s.citations.MergeAutomaticCitations(ctx, citingID, automatic)
	if err != nil {
		return nil, err
	}
	logger.Info(ctx, "extracted paper citations reconciled",
		"citing_document_id", citingID,
		"extracted_count", len(automatic),
		"final_count", len(merged))
	return merged, nil
}

func citationsByReferenceKey(citations []types.PaperCitation) map[string]types.PaperCitation {
	result := make(map[string]types.PaperCitation, len(citations))
	for _, citation := range citations {
		key := citation.ReferenceKey
		if key == "" {
			key = types.PaperCitationReferenceKey(citation.RawReference)
		}
		if key == "" {
			continue
		}
		_, exists := result[key]
		if !exists || citation.EffectiveOrigin() == types.PaperCitationOriginManual {
			result[key] = citation
		}
	}
	return result
}

func newPaperCitation(citingID uuid.UUID, rawReference string) types.PaperCitation {
	return types.PaperCitation{
		PaperReference: types.PaperReference{
			ReferenceID:         types.PaperReferenceIDForRaw(citingID, rawReference),
			CitingDocumentID:    citingID,
			ReferenceKey:        types.PaperCitationReferenceKey(rawReference),
			RawReference:        rawReference,
			Authors:             []types.PaperAuthor{},
			ResolutionStatus:    types.DocumentStageNotStarted,
			MetadataSource:      types.PaperMetadataSourceUnknown,
			ResolutionUpdatedAt: time.Now().UTC(),
		},
	}
}

// applyCitationTarget validates a manual target and copies its canonical
// metadata into the citation row.
func (s *paperService) applyCitationTarget(
	ctx context.Context, citation *types.PaperCitation, targetID uuid.UUID,
) error {
	if targetID == citation.CitingDocumentID {
		return types.ErrSelfPaperCitation
	}
	document, err := s.documents.Get(ctx, targetID)
	if err != nil {
		return err
	}
	if document.SourceState != types.DocumentSourceAvailable {
		return ErrCitationTargetNotResolved
	}
	patch, err := s.referenceMetadataPatchForDocument(ctx, document)
	if err != nil {
		return err
	}
	applyReferencePatch(&citation.PaperReference, patch, types.PaperMetadataSourceManual)
	citation.CitedDocumentID = &targetID
	citation.ResolutionStatus = types.DocumentStageSucceeded
	citation.ResolutionErrorCode = nil
	citation.ResolutionErrorMsg = nil
	citation.ResolutionUpdatedAt = time.Now().UTC()
	return nil
}

func paperMetadataSourceRank(source types.PaperMetadataSource) int {
	switch source {
	case types.PaperMetadataSourceManual:
		return 3
	case types.PaperMetadataSourceCatalog:
		return 2
	case types.PaperMetadataSourceExtraction:
		return 1
	default:
		return 0
	}
}

func (s *paperService) RemoveCitation(
	ctx context.Context,
	citingDocumentID string,
	referenceID string,
) error {
	citingID, _, err := s.requireCitingDocument(ctx, citingDocumentID)
	if err != nil {
		return err
	}
	refID, err := parsePaperReferenceID(referenceID)
	if err != nil {
		return err
	}
	if err := s.citations.RemoveCitation(ctx, citingID, refID); err != nil {
		return err
	}
	logger.Info(ctx, "paper citation removal completed",
		"citing_document_id", citingID,
		"reference_id", refID)
	return nil
}

func (s *paperService) ListCitations(
	ctx context.Context, documentID string,
) ([]types.PaperCitation, error) {
	id, err := parsePaperDocumentID(documentID)
	if err != nil {
		return nil, err
	}
	if _, err := s.documents.Get(ctx, id); err != nil {
		return nil, err
	}
	return s.citations.ListByCitingDocumentID(ctx, id)
}

func (s *paperService) ListCitationsPage(
	ctx context.Context, documentID string, offset, limit int,
) (types.PaperCitationPage, error) {
	id, err := parsePaperDocumentID(documentID)
	if err != nil {
		return types.PaperCitationPage{}, err
	}
	if _, err := s.documents.Get(ctx, id); err != nil {
		return types.PaperCitationPage{}, err
	}
	return s.citations.ListByCitingDocumentIDPage(ctx, id, offset, limit)
}

func (s *paperService) ListCitedBy(
	ctx context.Context, documentID string,
) ([]types.PaperCitation, error) {
	id, err := parsePaperDocumentID(documentID)
	if err != nil {
		return nil, err
	}
	if _, err := s.documents.Get(ctx, id); err != nil {
		return nil, err
	}
	return s.citations.ListByCitedDocumentID(ctx, id)
}

func (s *paperService) ResolveCitation(
	ctx context.Context,
	citingDocumentID string,
	resolvedDocumentID string,
	rawReference string,
) (*types.PaperCitation, error) {
	citingID, _, err := s.requireCitingDocument(ctx, citingDocumentID)
	if err != nil {
		return nil, err
	}
	resolvedID, err := parsePaperDocumentID(resolvedDocumentID)
	if err != nil {
		return nil, err
	}
	rawReference, err = validateRawReference(rawReference)
	if err != nil {
		return nil, err
	}
	citation := newPaperCitation(citingID, rawReference)
	previous, listErr := s.citations.ListByCitingDocumentID(ctx, citingID)
	if listErr != nil {
		return nil, listErr
	}
	for _, existing := range previous {
		if existing.ReferenceID != citation.ReferenceID {
			continue
		}
		document, getErr := s.documents.Get(ctx, resolvedID)
		if getErr != nil {
			return nil, getErr
		}
		if document.SourceState != types.DocumentSourceAvailable {
			return nil, ErrCitationTargetNotResolved
		}
		if resolvedID == citingID {
			return nil, types.ErrSelfPaperCitation
		}
		updated, _, linkErr := s.citations.LinkResolvedDocument(ctx, citation.ReferenceID, resolvedID, true)
		if linkErr != nil {
			return nil, linkErr
		}
		saved := types.PaperCitation{PaperReference: updated, Origin: existing.Origin}
		logger.Info(ctx, "paper citation resolution completed",
			"citing_document_id", citingID,
			"reference_id", saved.ReferenceID,
			"cited_document_id", resolvedID)
		return &saved, nil
	}
	citation.Origin = types.PaperCitationOriginManual
	if err := s.applyCitationTarget(ctx, &citation, resolvedID); err != nil {
		return nil, err
	}
	saved, err := s.citations.AddCitation(ctx, citation)
	if err != nil {
		return nil, err
	}
	logger.Info(ctx, "paper citation resolution completed",
		"citing_document_id", citingID,
		"reference_id", saved.ReferenceID,
		"cited_document_id", resolvedID)
	return &saved, nil
}

func (s *paperService) requireCitingDocument(
	ctx context.Context, value string,
) (uuid.UUID, types.Document, error) {
	id, err := parsePaperDocumentID(value)
	if err != nil {
		return uuid.Nil, types.Document{}, err
	}
	document, err := s.documents.Get(ctx, id)
	if err != nil {
		return uuid.Nil, types.Document{}, err
	}
	if document.SourceState != types.DocumentSourceAvailable {
		return uuid.Nil, types.Document{}, ErrPaperCannotCite
	}
	return id, document, nil
}

func validateRawReference(rawReference string) (string, error) {
	if !utf8.ValidString(rawReference) ||
		strings.TrimSpace(rawReference) == "" ||
		len(rawReference) > maxPaperRawReferenceBytes ||
		strings.ContainsRune(rawReference, '\x00') {
		return "", fmt.Errorf(
			"%w: raw reference must be valid non-empty UTF-8 up to %d bytes",
			types.ErrInvalidPaperCitation,
			maxPaperRawReferenceBytes,
		)
	}
	return rawReference, nil
}

func parsePaperDocumentID(value string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid document id: %w", err)
	}
	return id, nil
}

func normalizePaperMetadataUpdate(
	update *types.PaperMetadataUpdate,
) (*types.PaperMetadataUpdate, error) {
	if update == nil {
		return nil, nil
	}
	normalized := *update
	if normalized.MetadataSource != nil && !normalized.MetadataSource.IsValid() {
		return nil, fmt.Errorf(
			"%w: invalid metadata source %q",
			ErrInvalidPaperMetadata,
			*normalized.MetadataSource,
		)
	}
	for _, field := range []*string{
		normalized.Title,
		normalized.Abstract,
		normalized.SourceName,
		normalized.SourceURL,
		normalized.Venue,
		normalized.Volume,
		normalized.Issue,
		normalized.Pages,
		normalized.Language,
	} {
		if field == nil {
			continue
		}
		if !utf8.ValidString(*field) || strings.ContainsAny(*field, "\r\n\t") {
			return nil, fmt.Errorf("%w: text contains invalid characters", ErrInvalidPaperMetadata)
		}
		*field = strings.TrimSpace(*field)
	}
	if normalized.Tags != nil {
		tags, err := normalizePaperTags(*normalized.Tags)
		if err != nil {
			return nil, err
		}
		normalized.Tags = &tags
	}
	if normalized.Authors != nil {
		authors := make([]types.PaperAuthor, 0, len(*normalized.Authors))
		for _, author := range *normalized.Authors {
			author.Name = strings.TrimSpace(author.Name)
			author.Affiliation = strings.TrimSpace(author.Affiliation)
			if author.Name == "" ||
				!utf8.ValidString(author.Name) ||
				strings.ContainsAny(author.Name, "\r\n\t") {
				return nil, fmt.Errorf(
					"%w: authors must have a valid name",
					ErrInvalidPaperMetadata,
				)
			}
			authors = append(authors, author)
		}
		normalized.Authors = &authors
	}
	return &normalized, nil
}

func normalizePaperTags(input []string) ([]string, error) {
	const maxPaperTagCount, maxPaperTagLength = 32, 64
	if len(input) > maxPaperTagCount {
		return nil, fmt.Errorf(
			"%w: tags cannot contain more than %d values",
			ErrInvalidPaperMetadata,
			maxPaperTagCount,
		)
	}
	seen := make(map[string]struct{}, len(input))
	tags := make([]string, 0, len(input))
	for _, tag := range input {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if !utf8.ValidString(tag) ||
			tag == "" ||
			utf8.RuneCountInString(tag) > maxPaperTagLength ||
			strings.ContainsAny(tag, "\r\n\t") {
			return nil, fmt.Errorf(
				"%w: tags must be non-empty values up to 64 characters",
				ErrInvalidPaperMetadata,
			)
		}
		if _, exists := seen[tag]; exists {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	return tags, nil
}

var _ interfaces.PaperService = (*paperService)(nil)
