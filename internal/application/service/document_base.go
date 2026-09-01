package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
)

var ErrInvalidDocumentBaseName = errors.New("invalid document base name")

type documentBaseService struct {
	bases     interfaces.DocumentBaseRepository
	documents interfaces.DocumentRepository
}

// NewDocumentBaseService wires collection persistence with document existence
// checks so foreign-key errors never escape as opaque API failures.
func NewDocumentBaseService(
	bases interfaces.DocumentBaseRepository, documents interfaces.DocumentRepository,
) interfaces.DocumentBaseService {
	return &documentBaseService{bases: bases, documents: documents}
}

func (s *documentBaseService) CreateBase(ctx context.Context, name, description string) (*types.DocumentBase, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 120 || strings.ContainsAny(name, "\r\n\t") {
		return nil, ErrInvalidDocumentBaseName
	}
	created, err := s.bases.CreateBase(ctx, types.DocumentBase{ID: uuid.New(), Name: name, Description: strings.TrimSpace(description), Settings: types.JSON(`{}`)})
	if err != nil {
		return nil, err
	}
	return &created, nil
}

func (s *documentBaseService) GetBase(ctx context.Context, id string) (*types.DocumentBase, error) {
	parsed, err := parseDocumentBaseID(id)
	if err != nil {
		return nil, err
	}
	base, err := s.bases.GetBase(ctx, parsed)
	if err != nil {
		return nil, err
	}
	return &base, nil
}

func (s *documentBaseService) ListBases(ctx context.Context) ([]types.DocumentBase, error) {
	return s.bases.ListBases(ctx)
}

func (s *documentBaseService) UpdateBase(ctx context.Context, id string, name, description *string) (*types.DocumentBase, error) {
	if name == nil && description == nil {
		return nil, errors.New("at least one document base field is required")
	}
	current, err := s.GetBase(ctx, id)
	if err != nil {
		return nil, err
	}
	if name != nil {
		current.Name = strings.TrimSpace(*name)
		if current.Name == "" || len([]rune(current.Name)) > 120 || strings.ContainsAny(current.Name, "\r\n\t") {
			return nil, ErrInvalidDocumentBaseName
		}
	}
	if description != nil {
		current.Description = strings.TrimSpace(*description)
	}
	updated, err := s.bases.UpdateBase(ctx, current.ID, current.Name, current.Description)
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

func (s *documentBaseService) DeleteBase(ctx context.Context, id string) error {
	parsed, err := parseDocumentBaseID(id)
	if err != nil {
		return err
	}
	return s.bases.DeleteBase(ctx, parsed)
}

func (s *documentBaseService) AddDocumentToBase(ctx context.Context, baseID, documentID string) error {
	base, err := parseDocumentBaseID(baseID)
	if err != nil {
		return err
	}
	document, err := uuid.Parse(strings.TrimSpace(documentID))
	if err != nil {
		return fmt.Errorf("invalid document id: %w", err)
	}
	if _, err := s.bases.GetBase(ctx, base); err != nil {
		return err
	}
	if _, err := s.documents.Get(ctx, document); err != nil {
		return err
	}
	return s.bases.AddDocument(ctx, types.DocumentBaseFile{DocumentBaseID: base, DocumentID: document, Metadata: types.JSON(`{}`)})
}

func (s *documentBaseService) RemoveDocumentFromBase(ctx context.Context, baseID, documentID string) error {
	base, err := parseDocumentBaseID(baseID)
	if err != nil {
		return err
	}
	document, err := uuid.Parse(strings.TrimSpace(documentID))
	if err != nil {
		return fmt.Errorf("invalid document id: %w", err)
	}
	return s.bases.RemoveDocument(ctx, base, document)
}

func (s *documentBaseService) ListBaseDocuments(
	ctx context.Context, baseID string, page int, excludeUnknown bool,
) (types.DocumentPage, error) {
	base, err := parseDocumentBaseID(baseID)
	if err != nil {
		return types.DocumentPage{}, err
	}
	if _, err := s.bases.GetBase(ctx, base); err != nil {
		return types.DocumentPage{}, err
	}
	return s.bases.ListDocuments(ctx, base, page, types.DocumentPageSize, excludeUnknown)
}

func parseDocumentBaseID(id string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(id))
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid document base id: %w", err)
	}
	return parsed, nil
}

var _ interfaces.DocumentBaseService = (*documentBaseService)(nil)
