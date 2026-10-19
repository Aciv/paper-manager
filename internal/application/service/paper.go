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

const maxPaperRawReferenceBytes = 64 << 10

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
	tags, err := normalizePaperTags(search.Tags)
	if err != nil {
		return types.PaperMetadataPage{}, err
	}
	search.Tags = tags
	return s.metadata.Search(ctx, search)
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
		// Python extraction has already supplied DOI/title. Match the local
		// library immediately, before any provider or queued resolution.
		if citation.CitedDocumentID == nil {
			if documentID, found, matchErr := s.FindLocalReferenceMatch(ctx, citation.PaperReference, citingID); matchErr != nil {
				return nil, matchErr
			} else if found {
				citation.CitedDocumentID = &documentID
				citation.ResolutionStatus = types.DocumentStageSucceeded
				citation.ResolutionErrorCode = nil
				citation.ResolutionErrorMsg = nil
				citation.ResolutionUpdatedAt = time.Now().UTC()
			}
		}
		automatic = append(automatic, citation)
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

// applyReferenceCandidate merges non-empty parsed fields into one citation
// row. It never overwrites a manual/catalog field with extraction.
func (s *paperService) applyReferenceCandidate(
	citation types.PaperCitation,
	candidate *types.PaperReferenceCandidate,
	rawReference string,
) (types.PaperCitation, error) {
	var patch *types.PaperReferenceMetadataPatch
	if candidate != nil {
		if err := candidate.Validate(); err != nil {
			return citation, err
		}
		if compactPaperText(candidate.RawReference) != compactPaperText(rawReference) {
			return citation, fmt.Errorf("%w: reference candidate does not match raw reference", types.ErrInvalidPaperExtraction)
		}
		patch = referenceMetadataPatchFromCandidate(*candidate)
	}
	if patch == nil {
		patch = &types.PaperReferenceMetadataPatch{}
	}
	// Rules extraction may provide only raw text. Parse a DOI/title/URL from it
	// so immediate local matching still works without an external lookup.
	fallback := referenceLookupQuery(rawReference)
	if patch.DOI == nil && fallback.DOI != "" {
		doi := fallback.DOI
		patch.DOI = &doi
	}
	if patch.Title == nil && fallback.Title != "" {
		title := fallback.Title
		patch.Title = &title
	}
	if patch.SourceURL == nil && fallback.SourceURL != "" {
		sourceURL := fallback.SourceURL
		patch.SourceURL = &sourceURL
	}
	if !patch.HasChanges() {
		return citation, nil
	}
	if patch.MetadataSource == nil {
		source := types.PaperMetadataSourceExtraction
		patch.MetadataSource = &source
	}
	applyReferencePatch(&citation.PaperReference, patch, types.PaperMetadataSourceExtraction)
	return citation, nil
}

func applyReferencePatch(
	reference *types.PaperReference,
	patch *types.PaperReferenceMetadataPatch,
	source types.PaperMetadataSource,
) {
	if patch == nil || !patch.HasChanges() {
		return
	}
	if patch.MetadataSource != nil {
		source = *patch.MetadataSource
	}
	overwrite := paperMetadataSourceRank(source) >= paperMetadataSourceRank(reference.EffectiveMetadataSource())
	setString := func(destination *string, value *string) {
		if value == nil || strings.TrimSpace(*value) == "" {
			return
		}
		if *destination == "" || overwrite {
			*destination = strings.TrimSpace(*value)
		}
	}
	setString(&reference.DOI, patch.DOI)
	if reference.DOI != "" {
		reference.DOI = strings.ToLower(reference.DOI)
	}
	setString(&reference.Title, patch.Title)
	setString(&reference.Venue, patch.Venue)
	setString(&reference.Volume, patch.Volume)
	setString(&reference.Issue, patch.Issue)
	setString(&reference.Pages, patch.Pages)
	setString(&reference.SourceURL, patch.SourceURL)
	if patch.Authors != nil && len(*patch.Authors) > 0 && (len(reference.Authors) == 0 || overwrite) {
		reference.Authors = *patch.Authors
	}
	if patch.PublishedAt != nil && (reference.PublishedAt == nil || overwrite) {
		reference.PublishedAt = patch.PublishedAt
	}
	if reference.MetadataSource == "" || overwrite {
		reference.MetadataSource = source
	}
	reference.UpdatedAt = time.Now().UTC()
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

func referenceMetadataPatchFromCandidate(candidate types.PaperReferenceCandidate) *types.PaperReferenceMetadataPatch {
	patch := &types.PaperReferenceMetadataPatch{}
	if doi := normalizePaperDOI(candidate.DOI); doi != "" {
		patch.DOI = &doi
	}
	if arxivID := paperArXivID(candidate.ArXivID); arxivID != "" {
		url := "https://arxiv.org/abs/" + arxivID
		patch.SourceURL = &url
	}
	if title := strings.TrimSpace(candidate.Title); title != "" {
		patch.Title = &title
	}
	if venue := strings.TrimSpace(candidate.Venue); venue != "" {
		patch.Venue = &venue
	}
	if volume := strings.TrimSpace(candidate.Volume); volume != "" {
		patch.Volume = &volume
	}
	if pages := strings.TrimSpace(candidate.Pages); pages != "" {
		patch.Pages = &pages
	}
	authors := make([]types.PaperAuthor, 0, len(candidate.Authors))
	for _, value := range candidate.Authors {
		if name := strings.TrimSpace(value); name != "" {
			authors = append(authors, types.PaperAuthor{Name: name})
		}
	}
	if len(authors) > 0 {
		patch.Authors = &authors
	}
	if candidate.PublishedYear >= 1000 {
		publishedAt := time.Date(candidate.PublishedYear, time.January, 1, 0, 0, 0, 0, time.UTC)
		patch.PublishedAt = &publishedAt
	}
	if patch.HasChanges() {
		source := types.PaperMetadataSourceExtraction
		patch.MetadataSource = &source
	}
	return patch
}

func (s *paperService) referenceMetadataPatchForDocument(
	ctx context.Context, document types.Document,
) (*types.PaperReferenceMetadataPatch, error) {
	metadata, err := s.metadata.GetByDocumentID(ctx, document.ID)
	if errors.Is(err, sql.ErrNoRows) {
		metadata = types.PaperMetadata{DocumentID: document.ID, Authors: []types.PaperAuthor{}}
	} else if err != nil {
		return nil, err
	}
	patch := &types.PaperReferenceMetadataPatch{}
	if doi := normalizePaperDOI(document.DOI); doi != "" {
		patch.DOI = &doi
	}
	if title := strings.TrimSpace(metadata.Title); title != "" {
		patch.Title = &title
	}
	if len(metadata.Authors) > 0 {
		authors := metadata.Authors
		patch.Authors = &authors
	}
	if venue := strings.TrimSpace(metadata.Venue); venue != "" {
		patch.Venue = &venue
	}
	if volume := strings.TrimSpace(metadata.Volume); volume != "" {
		patch.Volume = &volume
	}
	if issue := strings.TrimSpace(metadata.Issue); issue != "" {
		patch.Issue = &issue
	}
	if pages := strings.TrimSpace(metadata.Pages); pages != "" {
		patch.Pages = &pages
	}
	if sourceURL := strings.TrimSpace(metadata.SourceURL); sourceURL != "" {
		patch.SourceURL = &sourceURL
	}
	if metadata.PublishedAt != nil {
		patch.PublishedAt = metadata.PublishedAt
	}
	source := types.PaperMetadataSourceManual
	patch.MetadataSource = &source
	return patch, nil
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

func (s *paperService) GetReference(
	ctx context.Context, referenceID string,
) (*types.PaperReference, error) {
	id, err := parsePaperReferenceID(referenceID)
	if err != nil {
		return nil, err
	}
	reference, err := s.citations.GetReference(ctx, id)
	if err != nil {
		return nil, err
	}
	return &reference, nil
}

func (s *paperService) BeginReferenceDownload(
	ctx context.Context, referenceID, attemptID string,
) (*types.PaperReference, error) {
	id, err := parsePaperReferenceID(referenceID)
	if err != nil {
		return nil, err
	}
	attempt, err := uuid.Parse(strings.TrimSpace(attemptID))
	if err != nil || attempt == uuid.Nil {
		return nil, fmt.Errorf("%w: invalid download attempt", types.ErrInvalidPaperReference)
	}
	reference, applied, err := s.citations.QueueDownload(ctx, id, attempt)
	if err != nil {
		return nil, err
	}
	if !applied {
		return &reference, ErrReferenceAlreadyResolved
	}
	return &reference, nil
}

func (s *paperService) FailReferenceDownload(
	ctx context.Context, referenceID, attemptID, errorCode, errorMessage string,
) error {
	id, err := parsePaperReferenceID(referenceID)
	if err != nil {
		return err
	}
	attempt, err := uuid.Parse(strings.TrimSpace(attemptID))
	if err != nil || attempt == uuid.Nil {
		return fmt.Errorf("%w: invalid download attempt", types.ErrInvalidPaperReference)
	}
	_, err = s.citations.FailDownload(ctx, id, attempt, errorCode, errorMessage)
	return err
}

func (s *paperService) ResolveReference(
	ctx context.Context, referenceID, resolvedDocumentID string,
) (*types.PaperReference, error) {
	refID, err := parsePaperReferenceID(referenceID)
	if err != nil {
		return nil, err
	}
	documentID, err := parsePaperDocumentID(resolvedDocumentID)
	if err != nil {
		return nil, err
	}
	document, err := s.documents.Get(ctx, documentID)
	if err != nil {
		return nil, err
	}
	if document.SourceState != types.DocumentSourceAvailable {
		return nil, ErrCitationTargetNotResolved
	}
	reference, applied, err := s.citations.LinkResolvedDocument(ctx, refID, documentID, true)
	if err != nil {
		return nil, err
	}
	if applied {
		logger.Info(ctx, "paper reference manually resolved",
			"reference_id", refID, "document_id", documentID)
	}
	return &reference, nil
}

// RematchReference retries the same local DOI/title matching used after
// extraction and before provider lookup. It never creates a document and never
// calls an external provider.
func (s *paperService) RematchReference(
	ctx context.Context, referenceID string,
) (*types.PaperReference, bool, error) {
	id, err := parsePaperReferenceID(referenceID)
	if err != nil {
		return nil, false, err
	}
	reference, err := s.citations.GetReference(ctx, id)
	if err != nil {
		return nil, false, err
	}
	if reference.CitedDocumentID != nil {
		return &reference, false, nil
	}
	// Raw-only rows still get a local match attempt: the same heuristic used by
	// extraction derives a DOI/title without persisting or querying a provider.
	matchReference := reference
	if strings.TrimSpace(matchReference.DOI) == "" && strings.TrimSpace(matchReference.Title) == "" {
		fallback := referenceLookupQuery(matchReference.RawReference)
		matchReference.DOI = fallback.DOI
		matchReference.Title = fallback.Title
	}
	documentID, found, err := s.FindLocalReferenceMatch(ctx, matchReference, matchReference.CitingDocumentID)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return &reference, false, nil
	}
	updated, applied, err := s.citations.LinkResolvedDocument(ctx, id, documentID, false)
	if err != nil {
		return nil, false, err
	}
	if applied {
		logger.Info(ctx, "paper reference rematched to local document",
			"reference_id", id, "document_id", documentID)
	}
	return &updated, applied, nil
}

func (s *paperService) LinkReferenceDocument(
	ctx context.Context, referenceID, documentID uuid.UUID, overwrite bool,
) (*types.PaperReference, error) {
	reference, applied, err := s.citations.LinkResolvedDocument(ctx, referenceID, documentID, overwrite)
	if err != nil {
		return nil, err
	}
	if applied {
		logger.Info(ctx, "paper reference linked to document",
			"reference_id", referenceID, "document_id", documentID)
	}
	return &reference, nil
}

func (s *paperService) FindLocalReferenceMatch(
	ctx context.Context, reference types.PaperReference, excludeDocumentID uuid.UUID,
) (uuid.UUID, bool, error) {
	if reference.CitedDocumentID != nil {
		return *reference.CitedDocumentID, true, nil
	}
	if excludeDocumentID == uuid.Nil {
		excludeDocumentID = reference.CitingDocumentID
	}
	exclude := make([]uuid.UUID, 0, 1)
	if excludeDocumentID != uuid.Nil {
		exclude = append(exclude, excludeDocumentID)
	}
	if doi := normalizePaperDOI(reference.DOI); doi != "" {
		page, err := s.documents.Search(ctx, types.DocumentSearch{
			DOI: doi, SourceState: types.DocumentSourceAvailable,
			ExcludeIDs: exclude, Page: 1, PageSize: 2,
		})
		if err != nil {
			return uuid.Nil, false, err
		}
		for _, document := range page.Items {
			if document.SourceState == types.DocumentSourceAvailable {
				return document.ID, true, nil
			}
		}
	}
	titleKey := normalizedPaperTitle(reference.Title)
	if titleKey == "" {
		return uuid.Nil, false, nil
	}
	result, err := s.metadata.Search(ctx, types.PaperSearch{
		Title: paperTitleSearchSeed(titleKey), Page: 1, PageSize: localPaperTitleSearchSize,
	})
	if err != nil {
		return uuid.Nil, false, err
	}
	for _, metadata := range result.Items {
		if metadata.DocumentID == excludeDocumentID ||
			normalizedPaperTitle(metadata.Title) != titleKey {
			continue
		}
		document, err := s.documents.Get(ctx, metadata.DocumentID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return uuid.Nil, false, err
		}
		if document.SourceState != types.DocumentSourceAvailable {
			continue
		}
		if len(reference.Authors) > 0 && len(metadata.Authors) > 0 &&
			paperAuthorSimilarity(paperAuthorNames(reference.Authors), metadata.Authors) == 0 {
			continue
		}
		if reference.PublishedAt != nil && metadata.PublishedAt != nil {
			delta := reference.PublishedAt.Year() - metadata.PublishedAt.Year()
			if delta < 0 {
				delta = -delta
			}
			if delta > 1 {
				continue
			}
		}
		return document.ID, true, nil
	}
	return uuid.Nil, false, nil
}

func (s *paperService) QueueReferenceResolution(
	ctx context.Context, referenceID uuid.UUID,
) (types.PaperReference, bool, error) {
	return s.citations.QueueResolution(ctx, referenceID)
}

func (s *paperService) StartReferenceResolution(
	ctx context.Context, referenceID uuid.UUID, generation int64,
) (types.PaperReference, bool, error) {
	return s.citations.StartResolution(ctx, referenceID, generation)
}

func (s *paperService) FinishReferenceResolution(
	ctx context.Context,
	referenceID uuid.UUID,
	generation int64,
	status types.DocumentStageStatus,
	provider, errorCode, errorMessage string,
	update *types.PaperReferenceMetadataPatch,
	resolvedDocumentID *uuid.UUID,
) (types.PaperReference, bool, error) {
	return s.citations.FinishResolution(
		ctx, referenceID, generation, status, provider, errorCode, errorMessage, update, resolvedDocumentID,
	)
}

func parsePaperReferenceID(value string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid reference id: %w", err)
	}
	return id, nil
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
