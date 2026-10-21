package repository

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"paperMaster/internal/types"

	"github.com/google/uuid"
)

func TestBibliographySnapshotReadsWholeBaseInStableOrderPostgres(t *testing.T) {
	db, cleanup := openPostgresTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	documents := NewDocumentRepository(db)
	bases := NewDocumentBaseRepository(db)
	base := types.DocumentBase{ID: uuid.New(), Name: "Bibliography snapshot", Settings: types.JSON(`{}`)}
	if err := db.Create(&base).Error; err != nil {
		t.Fatal(err)
	}
	addedAt := time.Now().UTC().Truncate(time.Microsecond)
	wantIDs := make([]string, 0, 12)
	for index := 0; index < 12; index++ {
		id := uuid.New()
		document := sourceDocumentForCanonicalTest(id, "paper.pdf", "bibliography/"+id.String(), "")
		document.DOI = "10.1000/" + id.String()
		if _, err := documents.Create(ctx, document); err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&types.PaperMetadata{
			DocumentID: id, Title: "Title " + id.String(), Authors: []types.PaperAuthor{{Name: "Author"}},
			Tags: []string{}, Metadata: types.JSON(`{}`), FieldSources: types.PaperMetadataFieldSources{},
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&types.DocumentBaseFile{
			DocumentBaseID: base.ID, DocumentID: id, AddedAt: addedAt, SortOrder: 0, Metadata: types.JSON(`{}`),
		}).Error; err != nil {
			t.Fatal(err)
		}
		wantIDs = append(wantIDs, id.String())
	}
	sort.Strings(wantIDs)
	if _, err := bases.GetBibliographySnapshot(ctx, base.ID, 10); !errors.Is(err, types.ErrBibliographyBaseTooLarge) {
		t.Fatalf("small limit error = %v", err)
	}
	snapshot, err := bases.GetBibliographySnapshot(ctx, base.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 12 {
		t.Fatalf("entries = %d", len(snapshot.Entries))
	}
	for index, entry := range snapshot.Entries {
		if entry.DocumentID.String() != wantIDs[index] || entry.Metadata.Title == "" || entry.DOI == "" {
			t.Fatalf("entry %d = %#v, want id %s", index, entry, wantIDs[index])
		}
	}
}
