package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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
	// ErrUnknownPaperCannotCite prevents bibliographic placeholders from acting
	// as source papers in the citation graph.
	ErrUnknownPaperCannotCite = errors.New("unknown paper placeholder cannot cite documents")
	// ErrCitationTargetNotResolved means ResolveCitation was given another
	// unknown placeholder instead of a stored source document.
	ErrCitationTargetNotResolved = errors.New("citation target is not a resolved document")
)

type paperService struct {
	documents interfaces.DocumentRepository
	metadata  interfaces.PaperMetadataRepository
	citations interfaces.PaperCitationRepository
}

// NewPaperService creates the paper-domain boundary from its three persistence
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
	if !update.HasChanges() {
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
	targetID, created, err := s.resolveCitationTarget(ctx, input.CitedDocumentID, rawReference)
	if err != nil {
		return nil, err
	}
	citation := types.PaperCitation{
		CitingDocumentID: citingID,
		CitedDocumentID:  targetID,
		RawReference:     rawReference,
	}
	if err := s.citations.AddCitation(ctx, citation); err != nil {
		if created {
			s.cleanupUnknown(ctx, targetID)
		}
		return nil, err
	}
	logger.Info(ctx, "paper citation add completed",
		"citing_document_id", citingID,
		"cited_document_id", targetID,
		"unresolved", input.CitedDocumentID == nil)
	return &citation, nil
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

	replacements := make([]types.PaperCitation, 0, len(inputs))
	createdUnknowns := make(map[uuid.UUID]struct{})
	seenTargets := make(map[uuid.UUID]struct{}, len(inputs))
	for _, input := range inputs {
		rawReference, validateErr := validateRawReference(input.RawReference)
		if validateErr != nil {
			s.cleanupUnknownSet(ctx, createdUnknowns)
			return nil, validateErr
		}
		targetID, created, resolveErr := s.resolveCitationTarget(ctx, input.CitedDocumentID, rawReference)
		if resolveErr != nil {
			s.cleanupUnknownSet(ctx, createdUnknowns)
			return nil, resolveErr
		}
		if created {
			createdUnknowns[targetID] = struct{}{}
		}
		if _, exists := seenTargets[targetID]; exists {
			continue
		}
		seenTargets[targetID] = struct{}{}
		replacements = append(replacements, types.PaperCitation{
			CitingDocumentID: citingID,
			CitedDocumentID:  targetID,
			RawReference:     rawReference,
		})
	}

	if err := s.citations.ReplaceCitations(ctx, citingID, replacements); err != nil {
		s.cleanupUnknownSet(ctx, createdUnknowns)
		return nil, err
	}
	for _, citation := range previous {
		s.cleanupUnknown(ctx, citation.CitedDocumentID)
	}
	logger.Info(ctx, "paper citation replacement completed",
		"citing_document_id", citingID,
		"citation_count", len(replacements))
	return replacements, nil
}

func (s *paperService) RemoveCitation(
	ctx context.Context,
	citingDocumentID string,
	citedDocumentID string,
) error {
	citingID, _, err := s.requireCitingDocument(ctx, citingDocumentID)
	if err != nil {
		return err
	}
	citedID, err := parsePaperDocumentID(citedDocumentID)
	if err != nil {
		return err
	}
	if _, err := s.documents.Get(ctx, citedID); err != nil {
		return err
	}
	if err := s.citations.RemoveCitation(ctx, citingID, citedID); err != nil {
		return err
	}
	s.cleanupUnknown(ctx, citedID)
	logger.Info(ctx, "paper citation removal completed",
		"citing_document_id", citingID,
		"cited_document_id", citedID)
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
	resolved, err := s.documents.Get(ctx, resolvedID)
	if err != nil {
		return nil, err
	}
	if resolved.Status == types.DocumentStatusUnknown {
		return nil, ErrCitationTargetNotResolved
	}
	rawReference, err = validateRawReference(rawReference)
	if err != nil {
		return nil, err
	}
	placeholderID := unknownPaperDocumentID(rawReference)
	placeholder, err := s.documents.Get(ctx, placeholderID)
	if err != nil {
		return nil, err
	}
	if placeholder.Status != types.DocumentStatusUnknown {
		return nil, types.ErrUnknownDocumentConflict
	}
	if err := s.citations.RepointCitation(
		ctx,
		citingID,
		placeholderID,
		resolvedID,
		rawReference,
	); err != nil {
		return nil, err
	}
	s.cleanupUnknown(ctx, placeholderID)
	citation := &types.PaperCitation{
		CitingDocumentID: citingID,
		CitedDocumentID:  resolvedID,
		RawReference:     rawReference,
	}
	logger.Info(ctx, "paper citation resolution completed",
		"citing_document_id", citingID,
		"cited_document_id", resolvedID,
		"placeholder_document_id", placeholderID)
	return citation, nil
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
	if document.Status == types.DocumentStatusUnknown {
		return uuid.Nil, types.Document{}, ErrUnknownPaperCannotCite
	}
	return id, document, nil
}

func (s *paperService) resolveCitationTarget(
	ctx context.Context,
	citedDocumentID *uuid.UUID,
	rawReference string,
) (uuid.UUID, bool, error) {
	if citedDocumentID != nil {
		if *citedDocumentID == uuid.Nil {
			return uuid.Nil, false, types.ErrInvalidPaperCitation
		}
		if _, err := s.documents.Get(ctx, *citedDocumentID); err != nil {
			return uuid.Nil, false, err
		}
		return *citedDocumentID, false, nil
	}
	placeholderID := unknownPaperDocumentID(rawReference)
	_, created, err := s.documents.EnsureUnknown(ctx, placeholderID)
	return placeholderID, created, err
}

func (s *paperService) cleanupUnknown(ctx context.Context, documentID uuid.UUID) {
	if _, err := s.documents.DeleteUnknownIfUnreferenced(ctx, documentID); err != nil {
		logger.Warn(ctx, "unknown document cleanup deferred", "document_id", documentID, "error", err)
	}
}

func (s *paperService) cleanupUnknownSet(
	ctx context.Context, documentIDs map[uuid.UUID]struct{},
) {
	for documentID := range documentIDs {
		s.cleanupUnknown(ctx, documentID)
	}
}

func unknownPaperDocumentID(rawReference string) uuid.UUID {
	normalized := strings.ToLower(strings.Join(strings.Fields(rawReference), " "))
	return uuid.NewSHA1(
		uuid.NameSpaceURL,
		[]byte("paperMaster:unknown-paper-reference:"+normalized),
	)
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
