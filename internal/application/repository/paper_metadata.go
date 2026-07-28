package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type paperMetadataRepository struct {
	db *gorm.DB
}

// NewPaperMetadataRepository creates the repository for bibliographic data.
func NewPaperMetadataRepository(db *gorm.DB) interfaces.PaperMetadataRepository {
	return &paperMetadataRepository{db: db}
}

func (r *paperMetadataRepository) GetByDocumentID(ctx context.Context, documentID uuid.UUID) (types.PaperMetadata, error) {
	var metadata types.PaperMetadata
	err := repositoryError(r.db.WithContext(ctx).First(&metadata, "document_id = ?", documentID).Error)
	if err != nil && err != sql.ErrNoRows {
		logger.Error(ctx, "get paper metadata failed", "document_id", documentID, "error", err)
	}
	return metadata, err
}

func (r *paperMetadataRepository) Upsert(
	ctx context.Context, documentID uuid.UUID, update types.PaperMetadataUpdate,
) (types.PaperMetadata, error) {
	var updated types.PaperMetadata
	applied := false
	incomingSource := types.PaperMetadataSourceUnknown
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		metadata := &types.PaperMetadata{}
		preparePaperMetadata(metadata, documentID)
		if err := tx.FirstOrCreate(metadata, "document_id = ?", documentID).Error; err != nil {
			return err
		}
		changes, err := paperMetadataChanges(&update)
		if err != nil {
			return err
		}
		if len(changes) > 0 {
			if update.MetadataSource != nil {
				incomingSource = *update.MetadataSource
			}
			result := tx.Model(&types.PaperMetadata{}).
				Where("document_id = ?", documentID).
				Where("metadata_source IN ?", paperMetadataWritableSources(incomingSource)).
				Updates(changes)
			if result.Error != nil {
				return result.Error
			}
			applied = result.RowsAffected > 0
		}
		return tx.First(&updated, "document_id = ?", documentID).Error
	})
	if err != nil {
		logger.Error(ctx, "upsert paper metadata failed", "document_id", documentID, "error", err)
		return types.PaperMetadata{}, repositoryError(err)
	}
	if applied {
		logger.Info(ctx, "paper metadata updated",
			"document_id", documentID,
			"metadata_source", incomingSource)
	} else {
		logger.Info(ctx, "paper metadata update skipped",
			"document_id", documentID,
			"incoming_source", incomingSource,
			"current_source", updated.MetadataSource,
			"reason", "higher-priority metadata source")
	}
	return updated, nil
}

func (r *paperMetadataRepository) Search(ctx context.Context, search types.PaperSearch) (types.PaperMetadataPage, error) {
	page := search.Page
	if page < 1 {
		page = 1
	}
	if page > types.MaxDocumentSearchPage {
		return types.PaperMetadataPage{}, fmt.Errorf("page exceeds maximum %d", types.MaxDocumentSearchPage)
	}
	pageSize := search.PageSize
	if pageSize <= 0 {
		pageSize = types.DocumentPageSize
	}

	query := r.db.WithContext(ctx).Model(&types.PaperMetadata{})
	if search.Title != "" {
		query = query.Where("title ILIKE ?", "%"+search.Title+"%")
	}
	if search.Author != "" {
		// Authors are stored as objects in a JSONB array. EXISTS avoids
		// duplicating a paper when multiple authors match the same query.
		query = query.Where(
			"EXISTS (SELECT 1 FROM jsonb_array_elements(authors) AS author WHERE author->>'name' ILIKE ?)",
			"%"+search.Author+"%",
		)
	}
	if len(search.Tags) > 0 {
		if search.TagMode == types.PaperTagModeAny {
			conditions := make([]string, 0, len(search.Tags))
			args := make([]interface{}, 0, len(search.Tags))
			for _, tag := range search.Tags {
				encoded, _ := json.Marshal([]string{tag})
				conditions = append(conditions, "tags @> ?::jsonb")
				args = append(args, string(encoded))
			}
			query = query.Where("("+strings.Join(conditions, " OR ")+")", args...)
		} else {
			encoded, err := json.Marshal(search.Tags)
			if err != nil {
				return types.PaperMetadataPage{}, fmt.Errorf("encode paper tags: %w", err)
			}
			query = query.Where("tags @> ?::jsonb", string(encoded))
		}
	}

	result := types.PaperMetadataPage{Page: page, PageSize: pageSize, Items: []types.PaperMetadata{}}
	if err := query.Count(&result.Total).Error; err != nil {
		logger.Error(ctx, "count paper metadata search failed", "page", page, "error", err)
		return result, err
	}
	err := query.Order("updated_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&result.Items).Error
	if err != nil {
		logger.Error(ctx, "search paper metadata failed", "page", page, "error", err)
	}
	return result, err
}

func preparePaperMetadata(metadata *types.PaperMetadata, documentID uuid.UUID) {
	metadata.DocumentID = documentID
	metadata.Authors = []types.PaperAuthor{}
	metadata.Tags = []string{}
	metadata.Metadata = types.JSON(`{}`)
	metadata.MetadataSource = types.PaperMetadataSourceUnknown
}

func paperMetadataChanges(update *types.PaperMetadataUpdate) (map[string]interface{}, error) {
	changes := map[string]interface{}{}
	if update.Title != nil {
		changes["title"] = *update.Title
	}
	if update.Abstract != nil {
		changes["abstract"] = *update.Abstract
	}
	if update.Authors != nil {
		authors := *update.Authors
		if authors == nil {
			authors = []types.PaperAuthor{}
		}
		encoded, err := json.Marshal(authors)
		if err != nil {
			return nil, fmt.Errorf("encode paper authors: %w", err)
		}
		changes["authors"] = gorm.Expr("?::jsonb", string(encoded))
	}
	if update.Tags != nil {
		tags := *update.Tags
		if tags == nil {
			tags = []string{}
		}
		encoded, err := json.Marshal(tags)
		if err != nil {
			return nil, fmt.Errorf("encode paper tags: %w", err)
		}
		changes["tags"] = gorm.Expr("?::jsonb", string(encoded))
	}
	if update.SourceName != nil {
		changes["source_name"] = *update.SourceName
	}
	if update.SourceURL != nil {
		changes["source_url"] = *update.SourceURL
	}
	if update.Venue != nil {
		changes["venue"] = *update.Venue
	}
	if update.PublishedAt != nil {
		changes["published_at"] = *update.PublishedAt
	}
	if update.Volume != nil {
		changes["volume"] = *update.Volume
	}
	if update.Issue != nil {
		changes["issue"] = *update.Issue
	}
	if update.Pages != nil {
		changes["pages"] = *update.Pages
	}
	if update.Language != nil {
		changes["language"] = *update.Language
	}
	if update.MetadataSource != nil {
		if !update.MetadataSource.IsValid() {
			return nil, fmt.Errorf("invalid paper metadata source %q", *update.MetadataSource)
		}
		changes["metadata_source"] = *update.MetadataSource
	}
	return changes, nil
}

// paperMetadataWritableSources returns the current origins that an incoming
// write may replace. The repository applies this predicate in the UPDATE
// itself so concurrent workers cannot bypass source precedence.
func paperMetadataWritableSources(
	incoming types.PaperMetadataSource,
) []types.PaperMetadataSource {
	switch incoming {
	case types.PaperMetadataSourceManual:
		return []types.PaperMetadataSource{
			types.PaperMetadataSourceUnknown,
			types.PaperMetadataSourceExtraction,
			types.PaperMetadataSourceCatalog,
			types.PaperMetadataSourceManual,
		}
	case types.PaperMetadataSourceCatalog:
		return []types.PaperMetadataSource{
			types.PaperMetadataSourceUnknown,
			types.PaperMetadataSourceExtraction,
			types.PaperMetadataSourceCatalog,
		}
	case types.PaperMetadataSourceExtraction:
		return []types.PaperMetadataSource{
			types.PaperMetadataSourceUnknown,
			types.PaperMetadataSourceExtraction,
		}
	default:
		return []types.PaperMetadataSource{types.PaperMetadataSourceUnknown}
	}
}

var _ interfaces.PaperMetadataRepository = (*paperMetadataRepository)(nil)
