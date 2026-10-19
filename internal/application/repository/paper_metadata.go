package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

func (r *paperMetadataRepository) GetByDocumentIDs(
	ctx context.Context, documentIDs []uuid.UUID,
) (map[uuid.UUID]types.PaperMetadata, error) {
	metadataByID := make(map[uuid.UUID]types.PaperMetadata, len(documentIDs))
	if len(documentIDs) == 0 {
		return metadataByID, nil
	}
	rows := make([]types.PaperMetadata, 0, len(documentIDs))
	if err := r.db.WithContext(ctx).Where("document_id IN ?", documentIDs).Find(&rows).Error; err != nil {
		logger.Error(ctx, "batch get paper metadata failed", "document_count", len(documentIDs), "error", err)
		return nil, err
	}
	for _, row := range rows {
		metadataByID[row.DocumentID] = row
	}
	return metadataByID, nil
}

func (r *paperMetadataRepository) Upsert(
	ctx context.Context, documentID uuid.UUID, update types.PaperMetadataUpdate,
) (types.PaperMetadata, error) {
	var updated types.PaperMetadata
	incomingSource := types.PaperMetadataSourceUnknown
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		metadata := types.PaperMetadata{}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&metadata, "document_id = ?", documentID).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			preparePaperMetadata(&metadata, documentID)
			if err := tx.Create(&metadata).Error; err != nil {
				return err
			}
			// Lock the newly created row as well so every merge below uses the
			// same serialized path as an existing row.
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				First(&metadata, "document_id = ?", documentID).Error; err != nil {
				return err
			}
		}
		changes, nextSources, applied, err := mergePaperMetadataChanges(metadata.FieldSources, &update)
		if err != nil {
			return err
		}
		if len(changes) > 0 {
			if update.MetadataSource != nil {
				incomingSource = *update.MetadataSource
				changes["metadata_source"] = incomingSource
			}
			encodedSources, err := json.Marshal(nextSources)
			if err != nil {
				return fmt.Errorf("encode paper metadata field sources: %w", err)
			}
			changes["field_sources"] = gorm.Expr("?::jsonb", string(encodedSources))
			// A non-nil empty string/slice is an explicit manual clear. Automated
			// writers are filtered by mergePaperMetadataChanges before this query.
			result := tx.Model(&types.PaperMetadata{}).
				Where("document_id = ?", documentID).
				Updates(changes)
			if result.Error != nil {
				return result.Error
			}
			logger.Info(ctx, "paper metadata fields updated", "document_id", documentID,
				"metadata_source", incomingSource, "field_count", len(applied))
		}
		return tx.First(&updated, "document_id = ?", documentID).Error
	})
	if err != nil {
		logger.Error(ctx, "upsert paper metadata failed", "document_id", documentID, "error", err)
		return types.PaperMetadata{}, repositoryError(err)
	}
	logger.Info(ctx, "paper metadata update completed", "document_id", documentID, "metadata_source", incomingSource)
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
		// author_names is a trigger-maintained text projection indexed with
		// pg_trgm. Searching the projection avoids expanding every JSONB array
		// and keeps the substring predicate indexable.
		query = query.Where("author_names ILIKE ?", "%"+search.Author+"%")
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
	err := query.Order("updated_at DESC, document_id DESC").
		Offset((page - 1) * pageSize).Limit(pageSize).Find(&result.Items).Error
	if err != nil {
		logger.Error(ctx, "search paper metadata failed", "page", page, "error", err)
	}
	return result, err
}

// SearchTitleSeeds batches the per-reference title probe used by local
// reference matching. One query drives a LATERAL trigram lookup per seed, so
// the round-trip count no longer grows with the bibliography size. The
// per-seed limit is applied before the available-source filter, matching the
// original single-reference search-then-check order.
func (r *paperMetadataRepository) SearchTitleSeeds(
	ctx context.Context, seeds []string, excludeDocumentID uuid.UUID, perSeedLimit int,
) (map[string][]types.PaperMetadata, error) {
	matches := make(map[string][]types.PaperMetadata, len(seeds))
	if len(seeds) == 0 {
		return matches, nil
	}
	limit := perSeedLimit
	if limit <= 0 {
		limit = types.DocumentPageSize
	}
	if limit > 200 {
		limit = 200
	}
	encodedSeeds, err := json.Marshal(seeds)
	if err != nil {
		return nil, fmt.Errorf("encode title search seeds: %w", err)
	}
	var rows []struct {
		Seed        string
		DocumentID  uuid.UUID
		Title       string
		Authors     []byte
		PublishedAt *time.Time
	}
	err = r.db.WithContext(ctx).Raw(`
		WITH candidates AS MATERIALIZED (
			SELECT seed_text AS seed,
			       metadata.document_id,
			       metadata.title,
			       metadata.authors,
			       metadata.published_at
			FROM jsonb_array_elements_text(?::jsonb) AS seed_text
			JOIN LATERAL (
				SELECT pm.document_id, pm.title, pm.authors, pm.published_at
				FROM paper_metadata pm
				WHERE pm.title ILIKE '%' || seed_text || '%'
				ORDER BY pm.updated_at DESC, pm.document_id DESC
				LIMIT ?
			) AS metadata ON TRUE
		)
		SELECT candidates.seed,
		       candidates.document_id,
		       candidates.title,
		       candidates.authors,
		       candidates.published_at
		FROM candidates
		JOIN documents d ON d.id = candidates.document_id
		WHERE d.source_state = ?
		  AND d.id <> ?`,
		string(encodedSeeds), limit, types.DocumentSourceAvailable, excludeDocumentID,
	).Scan(&rows).Error
	if err != nil {
		logger.Error(ctx, "batch paper title lookup failed", "seed_count", len(seeds), "error", err)
		return nil, err
	}
	for _, row := range rows {
		authors := []types.PaperAuthor{}
		if len(row.Authors) > 0 {
			if err := json.Unmarshal(row.Authors, &authors); err != nil {
				return nil, fmt.Errorf("decode paper title candidate authors: %w", err)
			}
		}
		matches[row.Seed] = append(matches[row.Seed], types.PaperMetadata{
			DocumentID: row.DocumentID, Title: row.Title,
			Authors: authors, PublishedAt: row.PublishedAt,
		})
	}
	return matches, nil
}

func preparePaperMetadata(metadata *types.PaperMetadata, documentID uuid.UUID) {
	metadata.DocumentID = documentID
	metadata.Authors = []types.PaperAuthor{}
	metadata.Tags = []string{}
	metadata.Metadata = types.JSON(`{}`)
	metadata.MetadataSource = types.PaperMetadataSourceUnknown
	metadata.FieldSources = types.PaperMetadataFieldSources{}
}

// paperMetadataFieldNames are both JSON keys in field_sources and column names
// in paper_metadata. Keeping one canonical list prevents a newly editable
// field from silently bypassing manual-edit protection.
var paperMetadataFieldNames = []string{
	"title", "abstract", "authors", "tags", "source_name", "source_url",
	"venue", "published_at", "volume", "issue", "pages", "language",
}

func paperMetadataChanges(update *types.PaperMetadataUpdate) (map[string]interface{}, error) {
	changes := map[string]interface{}{}
	if update == nil {
		return changes, nil
	}
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
	return changes, nil
}

// mergePaperMetadataChanges builds an atomic patch from one incoming update.
// Manual fields are sticky: automatic extraction/catalog patches cannot alter
// them, including with an empty candidate. A manual patch may explicitly clear
// a field and remains protected after that clear.
func mergePaperMetadataChanges(
	existing types.PaperMetadataFieldSources,
	update *types.PaperMetadataUpdate,
) (map[string]interface{}, types.PaperMetadataFieldSources, []string, error) {
	changes, err := paperMetadataChanges(update)
	if err != nil {
		return nil, nil, nil, err
	}
	incoming := types.PaperMetadataSourceUnknown
	if update != nil && update.MetadataSource != nil {
		incoming = *update.MetadataSource
		if !incoming.IsValid() {
			return nil, nil, nil, fmt.Errorf("invalid paper metadata source %q", incoming)
		}
	}
	next := make(types.PaperMetadataFieldSources, len(existing)+len(changes))
	for field, source := range existing {
		if source.IsValid() {
			next[field] = source
		}
	}
	applied := make([]string, 0, len(changes))
	for _, field := range paperMetadataFieldNames {
		_, present := changes[field]
		if !present {
			continue
		}
		if incoming != types.PaperMetadataSourceManual && next[field] == types.PaperMetadataSourceManual {
			delete(changes, field)
			continue
		}
		next[field] = incoming
		applied = append(applied, field)
	}
	return changes, next, applied, nil
}

var _ interfaces.PaperMetadataRepository = (*paperMetadataRepository)(nil)
