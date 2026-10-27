package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"
)

type blockQueryRepo struct {
	interfaces.DocumentRepository
	query    types.DocumentBlockQuery
	manifest types.DocumentMarkdown
}

func (r *blockQueryRepo) QueryBlocks(
	_ context.Context, _ uuid.UUID, q types.DocumentBlockQuery,
) (types.DocumentBlockPage, error) {
	r.query = q
	return types.DocumentBlockPage{Status: "succeeded"}, nil
}
func (r *blockQueryRepo) GetMarkdown(context.Context, uuid.UUID) (types.DocumentMarkdown, error) {
	return r.manifest, nil
}

func TestDocumentBlockQueryValidation(t *testing.T) {
	repo := &blockQueryRepo{manifest: types.DocumentMarkdown{PageCount: 8}}
	svc := NewDocumentReadingService(repo, nil, nil)
	id := uuid.NewString()
	ordinal := 0
	for _, q := range []types.DocumentBlockQuery{
		{PageSize: 201}, {Page: -1}, {Mode: "markdown"}, {PageNumber: -1},
		{Range: &types.BlockRange{Start: 3, End: 2}},
		{SectionOrdinal: &ordinal, Range: &types.BlockRange{Start: 0, End: 2}},
		{BlockIDs: []string{"wrong"}},
		{BlockIDs: []string{id, id}},
	} {
		_, err := svc.QueryDocumentBlocks(context.Background(), id, q)
		if !errors.Is(err, types.ErrInvalidBlockQuery) {
			t.Fatalf("%+v: %v", q, err)
		}
	}
	query := types.DocumentBlockQuery{SectionOrdinal: &ordinal}
	if _, err := svc.QueryDocumentBlocks(context.Background(), id, query); err != nil {
		t.Fatal(err)
	}
	if repo.query.Page != 1 || repo.query.PageSize != 50 || repo.query.Mode != "blocks" {
		t.Fatalf("defaults: %+v", repo.query)
	}
	if count, err := svc.GetDocumentPageCount(context.Background(), id); err != nil || count != 8 {
		t.Fatalf("manifest only: %d %v", count, err)
	}
}
