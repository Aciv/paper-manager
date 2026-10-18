package repository

import (
	"strings"
	"time"

	"paperMaster/internal/types"
)

// mergeReferenceMetadata applies incoming citation metadata to one bibliography
// row. Resolution state and citing ownership are deliberately untouched.
func mergeReferenceMetadata(
	existing types.PaperReference,
	incoming types.PaperReference,
	patch *types.PaperReferenceMetadataPatch,
) types.PaperReference {
	if existing.Authors == nil {
		existing.Authors = []types.PaperAuthor{}
	}
	source := incoming.EffectiveMetadataSource()
	if patch != nil && patch.MetadataSource != nil {
		source = *patch.MetadataSource
	}
	var effective types.PaperReferenceMetadataPatch
	if patch != nil {
		effective = *patch
	}
	patch = &effective
	existingSource := existing.EffectiveMetadataSource()
	overwrite := referenceSourceRank(source) >= referenceSourceRank(existingSource)

	setString := func(dst *string, value *string) {
		if value == nil {
			return
		}
		trimmed := strings.TrimSpace(*value)
		if trimmed == "" {
			return
		}
		if *dst == "" || overwrite {
			*dst = trimmed
		}
	}
	// Values carried directly on the incoming row behave like a lower-priority
	// patch; the explicit patch (extraction or catalog) is applied after.
	base := incoming
	if patch.DOI == nil && base.DOI != "" {
		patch.DOI = &base.DOI
	}
	if patch.Title == nil && base.Title != "" {
		patch.Title = &base.Title
	}
	if patch.Venue == nil && base.Venue != "" {
		patch.Venue = &base.Venue
	}
	if patch.Volume == nil && base.Volume != "" {
		patch.Volume = &base.Volume
	}
	if patch.Issue == nil && base.Issue != "" {
		patch.Issue = &base.Issue
	}
	if patch.Pages == nil && base.Pages != "" {
		patch.Pages = &base.Pages
	}
	if patch.SourceURL == nil && base.SourceURL != "" {
		patch.SourceURL = &base.SourceURL
	}
	if patch.Authors == nil && len(base.Authors) > 0 {
		authors := base.Authors
		patch.Authors = &authors
	}
	if patch.PublishedAt == nil && base.PublishedAt != nil {
		patch.PublishedAt = base.PublishedAt
	}

	setString(&existing.DOI, patch.DOI)
	if existing.DOI != "" {
		existing.DOI = strings.ToLower(existing.DOI)
	}
	setString(&existing.Title, patch.Title)
	setString(&existing.Venue, patch.Venue)
	setString(&existing.Volume, patch.Volume)
	setString(&existing.Issue, patch.Issue)
	setString(&existing.Pages, patch.Pages)
	setString(&existing.SourceURL, patch.SourceURL)
	if patch.Authors != nil && len(*patch.Authors) > 0 && (len(existing.Authors) == 0 || overwrite) {
		existing.Authors = *patch.Authors
	}
	if patch.PublishedAt != nil && (existing.PublishedAt == nil || overwrite) {
		existing.PublishedAt = patch.PublishedAt
	}
	if existing.RawReference == "" && base.RawReference != "" {
		existing.RawReference = base.RawReference
	}
	if source != "" && (existingSource == "" || overwrite) {
		existing.MetadataSource = source
	}
	existing.UpdatedAt = time.Now().UTC()
	return existing
}

func applyReferenceMetadata(
	reference types.PaperReference, patch *types.PaperReferenceMetadataPatch,
) types.PaperReference {
	if patch == nil || !patch.HasChanges() {
		return reference
	}
	incoming := types.PaperReference{
		DOI:            derefString(patch.DOI),
		Title:          derefString(patch.Title),
		Venue:          derefString(patch.Venue),
		Volume:         derefString(patch.Volume),
		Issue:          derefString(patch.Issue),
		Pages:          derefString(patch.Pages),
		SourceURL:      derefString(patch.SourceURL),
		PublishedAt:    patch.PublishedAt,
		MetadataSource: reference.MetadataSource,
	}
	if patch.Authors != nil {
		incoming.Authors = *patch.Authors
	}
	if patch.MetadataSource != nil {
		incoming.MetadataSource = *patch.MetadataSource
	}
	return mergeReferenceMetadata(reference, incoming, patch)
}

func nullableReferenceError(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func referenceSourceRank(source types.PaperMetadataSource) int {
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

func normalizeReferenceSlice(reference *types.PaperReference) {
	if reference.Authors == nil {
		reference.Authors = []types.PaperAuthor{}
	}
}
