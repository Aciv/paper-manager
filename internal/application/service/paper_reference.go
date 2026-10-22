package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"

	"github.com/google/uuid"
)

// DeleteReference removes one bibliography row by its reference ID. The
// reference table owns the citing document, so the caller does not need to
// repeat it; this is the reference-scoped counterpart of RemoveCitation.
func (s *paperService) DeleteReference(ctx context.Context, referenceID string) error {
	refID, err := parsePaperReferenceID(referenceID)
	if err != nil {
		return err
	}
	reference, err := s.citations.GetReference(ctx, refID)
	if err != nil {
		return err
	}
	if err := s.citations.RemoveCitation(ctx, reference.CitingDocumentID, refID); err != nil {
		return err
	}
	logger.Info(ctx, "paper reference deleted",
		"reference_id", refID,
		"citing_document_id", reference.CitingDocumentID)
	return nil
}

// UpdateReferenceSource applies a manual edit to one reference's bibliographic
// fields. The repository owns the row lock and the resolution fence; this
// method normalizes and bounds every client-supplied value first.
func (s *paperService) UpdateReferenceSource(
	ctx context.Context,
	referenceID string,
	update types.PaperReferenceSourceUpdate,
) (*types.PaperReference, error) {
	refID, err := parsePaperReferenceID(referenceID)
	if err != nil {
		return nil, err
	}
	edit, err := normalizePaperReferenceSourceUpdate(update)
	if err != nil {
		return nil, err
	}
	reference, err := s.citations.UpdateReferenceSource(ctx, refID, *edit)
	if err != nil {
		return nil, err
	}
	logger.Info(ctx, "paper reference source updated",
		"reference_id", refID,
		"metadata_source", reference.MetadataSource,
		"resolution_status", reference.ResolutionStatus,
		"resolution_generation", reference.ResolutionGeneration)
	return &reference, nil
}

func normalizePaperReferenceSourceUpdate(
	update types.PaperReferenceSourceUpdate,
) (*types.PaperReferenceSourceEdit, error) {
	if !update.HasChanges() {
		return nil, fmt.Errorf("%w: at least one source field is required", types.ErrInvalidPaperReference)
	}
	edit := &types.PaperReferenceSourceEdit{}
	if update.DOI != nil {
		doi, err := normalizeReferenceSourceText(*update.DOI, "doi", types.MaxPaperReferenceDOIBytes)
		if err != nil {
			return nil, err
		}
		doi = normalizePaperDOI(doi)
		if utf8.RuneCountInString(doi) > types.MaxPaperReferenceDOIBytes ||
			strings.ContainsAny(doi, " \t\r\n") {
			return nil, fmt.Errorf("%w: doi is invalid or too long", types.ErrInvalidPaperReference)
		}
		edit.DOI = &doi
	}
	if update.Title != nil {
		title, err := normalizeReferenceSourceText(*update.Title, "title", types.MaxPaperReferenceTitleBytes)
		if err != nil {
			return nil, err
		}
		edit.Title = &title
	}
	if update.Authors != nil {
		if len(*update.Authors) > types.MaxPaperReferenceAuthors {
			return nil, fmt.Errorf(
				"%w: authors cannot contain more than %d entries",
				types.ErrInvalidPaperReference,
				types.MaxPaperReferenceAuthors,
			)
		}
		authors := make([]types.PaperAuthor, 0, len(*update.Authors))
		for index, author := range *update.Authors {
			name, err := normalizeReferenceSourceText(author.Name, "author name", types.MaxPaperReferenceAuthorNameBytes)
			if err != nil {
				return nil, err
			}
			if name == "" {
				return nil, fmt.Errorf(
					"%w: author %d must have a name",
					types.ErrInvalidPaperReference,
					index+1,
				)
			}
			affiliation, err := normalizeReferenceSourceText(
				author.Affiliation,
				"author affiliation",
				types.MaxPaperReferenceAuthorNameBytes,
			)
			if err != nil {
				return nil, err
			}
			authors = append(authors, types.PaperAuthor{Name: name, Affiliation: affiliation})
		}
		edit.Authors = &authors
	}
	if update.Venue != nil {
		venue, err := normalizeReferenceSourceText(*update.Venue, "venue", types.MaxPaperReferenceVenueBytes)
		if err != nil {
			return nil, err
		}
		edit.Venue = &venue
	}
	if update.PublishedAt != nil {
		raw := strings.TrimSpace(*update.PublishedAt)
		edit.PublishedAtSet = true
		if raw != "" {
			parsed, err := parseReferencePublishedAt(raw)
			if err != nil {
				return nil, err
			}
			edit.PublishedAt = &parsed
		}
	}
	if update.Volume != nil {
		volume, err := normalizeReferenceSourceText(
			*update.Volume, "volume", types.MaxPaperReferencePublicationFieldBytes,
		)
		if err != nil {
			return nil, err
		}
		edit.Volume = &volume
	}
	if update.Issue != nil {
		issue, err := normalizeReferenceSourceText(
			*update.Issue, "issue", types.MaxPaperReferencePublicationFieldBytes,
		)
		if err != nil {
			return nil, err
		}
		edit.Issue = &issue
	}
	if update.Pages != nil {
		pages, err := normalizeReferenceSourceText(
			*update.Pages, "pages", types.MaxPaperReferencePublicationFieldBytes,
		)
		if err != nil {
			return nil, err
		}
		edit.Pages = &pages
	}
	if update.SourceURL != nil {
		sourceURL, err := normalizeReferenceSourceText(
			*update.SourceURL, "source url", types.MaxPaperReferenceSourceURLBytes,
		)
		if err != nil {
			return nil, err
		}
		if sourceURL != "" {
			parsed, parseErr := url.Parse(sourceURL)
			if parseErr != nil ||
				(parsed.Scheme != "http" && parsed.Scheme != "https") ||
				parsed.Host == "" ||
				parsed.User != nil {
				return nil, fmt.Errorf(
					"%w: source url must be an http(s) URL",
					types.ErrInvalidPaperReference,
				)
			}
		}
		edit.SourceURL = &sourceURL
	}
	return edit, nil
}

// normalizeReferenceSourceText trims and bounds one optional text field. Empty
// is allowed and means "clear this field"; control characters are never valid
// in a single-line bibliographic value.
func normalizeReferenceSourceText(value, field string, maxRunes int) (string, error) {
	trimmed := strings.TrimSpace(value)
	if !utf8.ValidString(trimmed) ||
		utf8.RuneCountInString(trimmed) > maxRunes ||
		strings.ContainsRune(trimmed, '\x00') ||
		strings.ContainsAny(trimmed, "\r\n\t") {
		return "", fmt.Errorf(
			"%w: %s is invalid or exceeds %d characters",
			types.ErrInvalidPaperReference,
			field,
			maxRunes,
		)
	}
	return trimmed, nil
}

func parseReferencePublishedAt(value string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02", time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 0, 0, 0, 0, time.UTC), nil
		}
	}
	return time.Time{}, fmt.Errorf(
		"%w: publishedAt must be YYYY-MM-DD or RFC3339",
		types.ErrInvalidPaperReference,
	)
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
	matches, err := s.findLocalReferenceMatches(ctx, []types.PaperReference{reference}, excludeDocumentID)
	if err != nil {
		return uuid.Nil, false, err
	}
	if len(matches) == 0 || matches[0] == uuid.Nil {
		return uuid.Nil, false, nil
	}
	return matches[0], true, nil
}

// findLocalReferenceMatches resolves a whole bibliography in two batched
// lookups: DOIs first, then title candidates for the remaining references.
// The returned slice is aligned with references; uuid.Nil means no match.
func (s *paperService) findLocalReferenceMatches(
	ctx context.Context,
	references []types.PaperReference,
	excludeDocumentID uuid.UUID,
) ([]uuid.UUID, error) {
	matches := make([]uuid.UUID, len(references))
	if excludeDocumentID == uuid.Nil {
		for _, reference := range references {
			if reference.CitingDocumentID != uuid.Nil {
				excludeDocumentID = reference.CitingDocumentID
				break
			}
		}
	}

	pending := make([]int, 0, len(references))
	doiSet := make(map[string]struct{}, len(references))
	for index, reference := range references {
		if reference.CitedDocumentID != nil {
			matches[index] = *reference.CitedDocumentID
			continue
		}
		pending = append(pending, index)
		if doi := normalizePaperDOI(reference.DOI); doi != "" {
			doiSet[doi] = struct{}{}
		}
	}
	if len(doiSet) > 0 {
		dois := make([]string, 0, len(doiSet))
		for doi := range doiSet {
			dois = append(dois, doi)
		}
		sort.Strings(dois)
		byDOI, err := s.documents.FindAvailableByDOIs(ctx, dois, excludeDocumentID)
		if err != nil {
			return nil, err
		}
		remaining := make([]int, 0, len(pending))
		for _, index := range pending {
			doi := normalizePaperDOI(references[index].DOI)
			if documentID, found := byDOI[doi]; found && doi != "" {
				matches[index] = documentID
				continue
			}
			remaining = append(remaining, index)
		}
		pending = remaining
	}

	seedSet := make(map[string]struct{}, len(pending))
	for _, index := range pending {
		titleKey := normalizedPaperTitle(references[index].Title)
		if titleKey == "" {
			continue
		}
		if seed := paperTitleSearchSeed(titleKey); seed != "" {
			seedSet[seed] = struct{}{}
		}
	}
	if len(seedSet) == 0 {
		return matches, nil
	}
	seeds := make([]string, 0, len(seedSet))
	for seed := range seedSet {
		seeds = append(seeds, seed)
	}
	sort.Strings(seeds)
	candidates := make(map[string][]types.PaperMetadata, len(seeds))
	for start := 0; start < len(seeds); start += localTitleSeedBatchSize {
		end := start + localTitleSeedBatchSize
		if end > len(seeds) {
			end = len(seeds)
		}
		batch, err := s.metadata.SearchTitleSeeds(
			ctx, seeds[start:end], excludeDocumentID, localPaperTitleSearchSize,
		)
		if err != nil {
			return nil, err
		}
		for seed, items := range batch {
			candidates[seed] = append(candidates[seed], items...)
		}
	}
	for _, index := range pending {
		reference := references[index]
		titleKey := normalizedPaperTitle(reference.Title)
		if titleKey == "" {
			continue
		}
		for _, metadata := range candidates[paperTitleSearchSeed(titleKey)] {
			if metadata.DocumentID == excludeDocumentID ||
				normalizedPaperTitle(metadata.Title) != titleKey {
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
			matches[index] = metadata.DocumentID
			break
		}
	}
	return matches, nil
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

// ReferenceQueryEnqueue persists queued resolution rows for the current
// bibliography and submits each unresolved reference to the in-process catalog
// dispatcher. It never performs provider HTTP; provider choice, fallback, and
// admission caps are owned by CatalogDispatcher.
func (s *paperInfoService) ReferenceQueryEnqueue(
	ctx context.Context,
	command types.PaperReferenceQueryEnqueueCommand,
) (outputErr error) {
	if err := command.Validate(); err != nil {
		return err
	}
	_, err := startDocumentStageTask(ctx, s.documents, command.DocumentID, command.Stage, command.Generation)
	if err != nil {
		return err
	}
	defer func() {
		finishDocumentStageTask(ctx, s.documents, command.DocumentID, command.Stage, command.Generation, outputErr)
	}()
	ctx = paperInfoContext(ctx, command.RequestID, command.DocumentID)
	logger.Info(ctx, "paper reference dispatch started")

	document, err := s.documents.Get(ctx, command.DocumentID)
	if err != nil {
		return err
	}
	if err := s.ensureCatalogSourceCurrent(ctx, document); err != nil {
		return err
	}
	if err := ensureDocumentStageWork(ctx, s.documents, command.DocumentID, command.Stage, command.Generation); err != nil {
		return err
	}
	if err := s.submitReferenceQueries(ctx, document, command); err != nil {
		return err
	}
	return nil
}

// referenceLookupQueryFor builds the provider-neutral query for one citation.
func referenceLookupQueryFor(reference types.PaperReference) types.PaperLookupQuery {
	query := types.PaperLookupQuery{
		DOI:       normalizePaperDOI(reference.DOI),
		Title:     strings.TrimSpace(reference.Title),
		SourceURL: strings.TrimSpace(reference.SourceURL),
		Venue:     strings.TrimSpace(reference.Venue),
		Limit:     types.DefaultPaperCatalogLimit,
	}
	query.Authors = paperCatalogAuthorNames(reference.Authors)
	if reference.PublishedAt != nil {
		query.PublishedYear = reference.PublishedAt.Year()
	}
	if query.Validate() != nil {
		return referenceLookupQuery(reference.RawReference)
	}
	return query
}

func referenceMetadataPatchFromCatalog(candidate types.PaperCatalogCandidate) *types.PaperReferenceMetadataPatch {
	patch := &types.PaperReferenceMetadataPatch{}
	if doi := normalizePaperDOI(candidate.DOI); doi != "" {
		patch.DOI = &doi
	}
	if title := compactPaperText(candidate.Title); title != "" {
		patch.Title = &title
	}
	authors := make([]types.PaperAuthor, 0, len(candidate.Authors))
	for _, author := range candidate.Authors {
		author.Name = compactPaperText(author.Name)
		author.Affiliation = compactPaperText(author.Affiliation)
		if author.Name != "" {
			authors = append(authors, author)
		}
	}
	if len(authors) > 0 {
		patch.Authors = &authors
	}
	setCatalogMetadataText(&patch.Venue, candidate.Venue)
	setCatalogMetadataText(&patch.Volume, candidate.Volume)
	setCatalogMetadataText(&patch.Issue, candidate.Issue)
	setCatalogMetadataText(&patch.Pages, candidate.Pages)
	setCatalogMetadataURL(&patch.SourceURL, candidate.SourceURL)
	if candidate.PublishedAt != nil {
		patch.PublishedAt = candidate.PublishedAt
	}
	if patch.HasChanges() {
		source := types.PaperMetadataSourceCatalog
		patch.MetadataSource = &source
	}
	return patch
}

// submitReferenceQueries queues each unique unresolved citation row in the
// in-process dispatcher. Provider choice, fallback, and admission caps are
// owned by that component; no per-reference Asynq task is enqueued here.
func (s *paperInfoService) submitReferenceQueries(
	ctx context.Context,
	document types.Document,
	command types.PaperReferenceQueryEnqueueCommand,
) error {
	if s.dispatcher == nil {
		return ErrPaperInfoCatalogUnavailable
	}
	citations, err := s.papers.ListCitations(ctx, document.ID.String())
	if err != nil {
		return err
	}
	limit := s.cfg.PaperReferenceCatalogMaxPerDocument
	if limit <= 0 {
		limit = defaultReferenceCatalogMaxPerDocument
	}
	seen := make(map[uuid.UUID]struct{}, len(citations))
	candidates := make([]types.PaperReference, 0, len(citations))
	for _, citation := range citations {
		if len(candidates) >= limit {
			break
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if citation.CitedDocumentID != nil {
			continue
		}
		if _, exists := seen[citation.ReferenceID]; exists {
			continue
		}
		seen[citation.ReferenceID] = struct{}{}
		candidates = append(candidates, citation.PaperReference)
	}
	if len(candidates) == 0 {
		logger.Info(ctx, "reference dispatch completed without unresolved references", "document_id", document.ID)
		return nil
	}
	queued := 0
	for _, reference := range candidates {
		updated, applied, queueErr := s.papers.QueueReferenceResolution(ctx, reference.ReferenceID)
		if queueErr != nil {
			return queueErr
		}
		if !applied {
			continue
		}
		query := referenceLookupQueryFor(updated)
		if err := query.Validate(); err != nil {
			_, _, _ = s.papers.FinishReferenceResolution(
				context.WithoutCancel(ctx), updated.ReferenceID, updated.ResolutionGeneration,
				types.DocumentStageSucceeded, "", "", "", nil, nil,
			)
			continue
		}
		if submitErr := s.dispatcher.SubmitReference(ctx, types.CatalogReferenceJob{
			RequestID: command.RequestID, Reference: updated,
			Generation: updated.ResolutionGeneration, Query: query,
		}); submitErr != nil {
			_, _, _ = s.papers.FinishReferenceResolution(
				context.WithoutCancel(ctx), updated.ReferenceID, updated.ResolutionGeneration,
				types.DocumentStageFailed, "", types.ProcessingErrorServiceUnavailable,
				"reference lookup could not be queued", nil, nil,
			)
			return submitErr
		}
		queued++
	}
	logger.Info(ctx, "independent references submitted",
		"document_id", document.ID, "queued_count", queued, "citation_count", len(citations), "limit", limit)
	return nil
}

// reference recognition
func referenceLookupQuery(rawReference string) types.PaperLookupQuery {
	query := types.PaperLookupQuery{Limit: types.DefaultPaperCatalogLimit}
	if match := doiPattern.FindString(rawReference); match != "" {
		query.DOI = normalizePaperDOI(match)
	}
	if match := urlPattern.FindString(rawReference); match != "" {
		query.SourceURL = strings.TrimRight(match, ".,;:")
	}
	if match := arxivIDPattern.FindStringSubmatch(rawReference); len(match) == 2 {
		query.SourceURL = "https://arxiv.org/abs/" + strings.ToLower(match[1])
	}
	if match := yearTitlePattern.FindStringSubmatch(rawReference); len(match) == 2 {
		query.Title = strings.Trim(match[1], " \t\r\n_*")
	} else if query.DOI == "" && query.SourceURL == "" {
		query.Title = strings.Trim(strings.TrimSpace(rawReference), "-*_ ")
	}
	return query
}

// RetryReferenceResolution retries only the bibliography dispatcher for a
// source document. The dispatcher reloads citations and submits reference jobs
// to the in-process catalog dispatcher; it does not repeat extraction or
// source-paper lookup.
func (s *paperInfoService) RetryReferenceResolution(ctx context.Context, documentID string) error {
	document, _, err := s.loadPaperStatusSource(ctx, documentID)
	if err != nil {
		return err
	}
	if document.SourceState == types.DocumentSourceDeleting || document.SourceState == types.DocumentSourceMerged {
		return ErrPaperInfoDeleting
	}
	if document.SourceState != types.DocumentSourceAvailable {
		return ErrPaperInfoCatalogUnavailable
	}
	stage, err := s.documents.GetStage(ctx, document.ID, types.DocumentStageReferenceLookup)
	if err != nil {
		return err
	}
	if stage.Status.Active() {
		return ErrPaperInfoAttemptActive
	}
	_, err = s.documents.GetMarkdown(ctx, document.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPaperInfoCatalogUnavailable
	}
	if err != nil {
		return err
	}
	stage, applied, err := s.documents.QueueStageNewGeneration(ctx, document.ID, types.DocumentStageReferenceLookup)
	if err != nil {
		return err
	}
	if !applied {
		return ErrPaperInfoAttemptActive
	}
	if err := s.enqueueReferenceStage(ctx, document.ID, "", stage.Generation); err != nil {
		return err
	}
	logger.Info(ctx, "paper reference resolution retry queued", "document_id", document.ID)
	return nil
}

func (s *paperInfoService) enqueueReferenceLookupIfNeeded(ctx context.Context, documentID uuid.UUID, requestID string) error {
	state, err := s.documents.GetStage(ctx, documentID, types.DocumentStageReferenceLookup)
	if err != nil {
		return err
	}
	if state.Status == types.DocumentStageNotStarted {
		state, _, err = s.documents.QueueStageNewGeneration(ctx, documentID, types.DocumentStageReferenceLookup)
		if err != nil {
			return err
		}
	}
	if state.Status != types.DocumentStageQueued {
		return nil
	}
	return s.enqueueReferenceStage(ctx, documentID, requestID, state.Generation)
}

func (s *paperInfoService) enqueueReferenceStage(ctx context.Context, documentID uuid.UUID, requestID string, generation int64) error {
	payload := types.PaperReferenceQueryEnqueuePayload{
		RequestID: requestID, DocumentID: documentID.String(),
		Stage: types.DocumentStageReferenceLookup, Generation: generation,
	}
	task, options, err := NewPaperReferenceQueryEnqueueTask(payload, s.cfg.PaperCatalogTaskTimeout, s.cfg.PaperCatalogTaskMaxRetry)
	if err == nil {
		err = enqueueDocumentStageTask(ctx, s.tasks, task, options, documentID.String())
	}
	if err != nil {
		_, _, _ = s.documents.FinishStage(context.WithoutCancel(ctx), documentID,
			types.DocumentStageReferenceLookup, generation, types.DocumentStageFailed, types.ProcessingErrorTaskFailed, "processing task could not be queued")
	}
	return err
}

func (s *paperInfoService) referenceQueryApply(ctx context.Context, command types.PaperQueryApplyCommand) error {
	reference, err := s.papers.GetReference(ctx, command.ReferenceID.String())
	if err != nil {
		return err
	}
	if reference.ResolutionGeneration != command.Generation {
		logger.Info(ctx, "reference catalog result ignored after generation change",
			"reference_id", command.ReferenceID, "generation", command.Generation,
			"current_generation", reference.ResolutionGeneration)
		return nil
	}
	if !command.Final {
		return nil
	}

	status := types.DocumentStageSucceeded
	code, message := "", ""
	if command.Outcome == types.CatalogOutcomeFailed {
		status = types.DocumentStageFailed
		code = strings.TrimSpace(command.ErrorCode)
		if code == "" {
			code = types.ProcessingErrorServiceUnavailable
		}
		message = strings.TrimSpace(command.ErrorMessage)
		if message == "" {
			message = "paper catalog lookup failed"
		}
	}

	var patch *types.PaperReferenceMetadataPatch
	var resolvedDocumentID *uuid.UUID
	if command.Outcome == types.CatalogOutcomeMatched {
		if command.ResolvedDocumentID != uuid.Nil {
			id := command.ResolvedDocumentID
			resolvedDocumentID = &id
		}
		if command.Candidate != nil {
			patch = referenceMetadataPatchFromCatalog(*command.Candidate)
			if resolvedDocumentID == nil {
				candidateReference := *reference
				if command.Candidate.DOI != "" {
					candidateReference.DOI = command.Candidate.DOI
				}
				if command.Candidate.Title != "" {
					candidateReference.Title = command.Candidate.Title
				}
				if len(command.Candidate.Authors) > 0 {
					candidateReference.Authors = command.Candidate.Authors
				}
				if command.Candidate.PublishedAt != nil {
					candidateReference.PublishedAt = command.Candidate.PublishedAt
				}
				documentID, found, matchErr := s.papers.FindLocalReferenceMatch(
					ctx, candidateReference, reference.CitingDocumentID,
				)
				if matchErr != nil {
					return matchErr
				}
				if found {
					resolvedDocumentID = &documentID
				}
			}
		}
	}

	_, applied, err := s.papers.FinishReferenceResolution(
		ctx, command.ReferenceID, command.Generation,
		status, command.Provider, code, message, patch, resolvedDocumentID,
	)
	if err != nil {
		return err
	}
	if !applied {
		logger.Info(ctx, "reference catalog result ignored after generation change",
			"reference_id", command.ReferenceID, "generation", command.Generation)
		return nil
	}
	logger.Info(ctx, "paper catalog reference result applied",
		"reference_id", command.ReferenceID, "generation", command.Generation,
		"outcome", command.Outcome, "resolved", resolvedDocumentID != nil)
	return nil
}
