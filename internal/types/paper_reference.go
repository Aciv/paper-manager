package types

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// ErrInvalidPaperReference indicates malformed reference identity or state.
var ErrInvalidPaperReference = errors.New("invalid paper reference")

// ErrReferenceDownloadInProgress rejects a duplicate explicit download while
// the previous request is still pending. The window is a safety valve for a
// request whose worker vanished without recording a terminal failure.
var ErrReferenceDownloadInProgress = errors.New("reference download already in progress")

// ReferenceDownloadInFlightWindow bounds how long a recorded request without
// a success link or terminal error is treated as in flight. Keep the frontend
// helper in web/src/citations.ts in sync.
const ReferenceDownloadInFlightWindow = 15 * time.Minute

const (
	// MaxPaperReferenceRawBytes bounds the representative raw reference text.
	MaxPaperReferenceRawBytes = 64 << 10
	// MaxPaperReferenceErrorCodeBytes and MaxPaperReferenceErrorMessageBytes
	// keep durable error state bounded, matching document stage columns.
	MaxPaperReferenceErrorCodeBytes    = 64
	MaxPaperReferenceErrorMessageBytes = 512
	// Manual source edits are user-entered text; bound every field so one PATCH
	// cannot store an unbounded blob even though the JSON body is capped.
	MaxPaperReferenceDOIBytes              = 255
	MaxPaperReferenceTitleBytes            = 4096
	MaxPaperReferenceAuthors               = 100
	MaxPaperReferenceAuthorNameBytes       = 512
	MaxPaperReferenceVenueBytes            = 1024
	MaxPaperReferencePublicationFieldBytes = 128
	MaxPaperReferenceSourceURLBytes        = 2048
)

// PaperReference is one bibliographic reference in a citing paper's
// bibliography. It is persisted as one paper_citations row: the reference does
// not exist independently of the paper that cites it. Two papers citing the
// same work keep separate rows; when both resolve they point their
// CitedDocumentID at the same documents row.
type PaperReference struct {
	ReferenceID      uuid.UUID           `gorm:"column:id;type:uuid;primaryKey" json:"referenceId"`
	CitingDocumentID uuid.UUID           `gorm:"column:citing_document_id;type:uuid;not null" json:"citingDocumentId"`
	ReferenceKey     string              `gorm:"column:reference_key;type:text;not null" json:"-"`
	DOI              string              `gorm:"column:doi;type:text;not null;default:''" json:"doi,omitempty"`
	Title            string              `gorm:"column:title;type:text;not null;default:''" json:"title,omitempty"`
	Authors          []PaperAuthor       `gorm:"column:authors;type:jsonb;serializer:json;not null" json:"authors"`
	Venue            string              `gorm:"column:venue;type:text;not null;default:''" json:"venue,omitempty"`
	PublishedAt      *time.Time          `gorm:"column:published_at;type:date" json:"publishedAt,omitempty"`
	Volume           string              `gorm:"column:volume;type:text;not null;default:''" json:"volume,omitempty"`
	Issue            string              `gorm:"column:issue;type:text;not null;default:''" json:"issue,omitempty"`
	Pages            string              `gorm:"column:pages;type:text;not null;default:''" json:"pages,omitempty"`
	SourceURL        string              `gorm:"column:source_url;type:text;not null;default:''" json:"sourceUrl,omitempty"`
	MetadataSource   PaperMetadataSource `gorm:"column:metadata_source;type:text;not null;default:'unknown'" json:"metadataSource"`
	RawReference     string              `gorm:"column:raw_reference;type:text;not null;default:''" json:"rawReference,omitempty"`

	ResolutionStatus     DocumentStageStatus `gorm:"column:resolution_status;type:text;not null;default:not_started" json:"resolutionStatus"`
	ResolutionGeneration int64               `gorm:"column:resolution_generation;not null;default:0" json:"resolutionGeneration"`
	ResolutionProvider   string              `gorm:"column:resolution_provider;type:text;not null;default:''" json:"resolutionProvider,omitempty"`
	ResolutionErrorCode  *string             `gorm:"column:resolution_error_code;type:text" json:"resolutionErrorCode,omitempty"`
	ResolutionErrorMsg   *string             `gorm:"column:resolution_error_message;type:text" json:"resolutionErrorMessage,omitempty"`
	ResolutionUpdatedAt  time.Time           `gorm:"column:resolution_updated_at;not null;default:CURRENT_TIMESTAMP" json:"resolutionUpdatedAt"`

	// Reference PDF download state. A reference can only be downloaded while
	// CitedDocumentID is NULL; success is then represented by that link. These
	// fields record the latest explicit attempt and its terminal failure so the
	// UI can distinguish "pending" from "failed with a reason".
	DownloadAttempt      *uuid.UUID `gorm:"column:download_attempt;type:uuid" json:"downloadAttempt,omitempty"`
	DownloadRequestedAt  *time.Time `gorm:"column:download_requested_at" json:"downloadRequestedAt,omitempty"`
	DownloadErrorCode    *string    `gorm:"column:download_error_code;type:text" json:"downloadErrorCode,omitempty"`
	DownloadErrorMessage *string    `gorm:"column:download_error_message;type:text" json:"downloadErrorMessage,omitempty"`
	DownloadUpdatedAt    *time.Time `gorm:"column:download_updated_at" json:"downloadUpdatedAt,omitempty"`

	// CitedDocumentID is the single resolution result. NULL means the
	// bibliography entry has no matching stored paper yet.
	CitedDocumentID *uuid.UUID `gorm:"column:cited_document_id;type:uuid" json:"citedDocumentId,omitempty"`
	CreatedAt       time.Time  `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt       time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

// TableName returns the single citation/reference table.
func (PaperReference) TableName() string { return "paper_citations" }

// PaperReferenceMetadataPatch contains bibliographic fields that extraction or
// a catalog provider may add to one citation row. Nil means unchanged.
type PaperReferenceMetadataPatch struct {
	DOI            *string
	Title          *string
	Venue          *string
	Volume         *string
	Issue          *string
	Pages          *string
	SourceURL      *string
	Authors        *[]PaperAuthor
	PublishedAt    *time.Time
	MetadataSource *PaperMetadataSource
}

// HasChanges reports whether the patch changes at least one bibliographic
// value or its provenance.
func (patch *PaperReferenceMetadataPatch) HasChanges() bool {
	return patch != nil && (patch.DOI != nil || patch.Title != nil || patch.Venue != nil ||
		patch.Volume != nil || patch.Issue != nil || patch.Pages != nil ||
		patch.SourceURL != nil || patch.Authors != nil || patch.PublishedAt != nil ||
		patch.MetadataSource != nil)
}

// PaperReferenceSourceUpdate is a partial manual edit of one reference's
// bibliographic fields. A nil field is unchanged; an empty string (or empty
// authors list) explicitly clears it. RawReference and all resolution/download
// state are intentionally not editable here.
type PaperReferenceSourceUpdate struct {
	DOI         *string        `json:"doi"`
	Title       *string        `json:"title"`
	Authors     *[]PaperAuthor `json:"authors"`
	Venue       *string        `json:"venue"`
	PublishedAt *string        `json:"publishedAt"` // YYYY-MM-DD or RFC3339; empty clears
	Volume      *string        `json:"volume"`
	Issue       *string        `json:"issue"`
	Pages       *string        `json:"pages"`
	SourceURL   *string        `json:"sourceUrl"`
}

// HasChanges reports whether the request edits at least one field.
func (update PaperReferenceSourceUpdate) HasChanges() bool {
	return update.DOI != nil || update.Title != nil || update.Authors != nil ||
		update.Venue != nil || update.PublishedAt != nil || update.Volume != nil ||
		update.Issue != nil || update.Pages != nil || update.SourceURL != nil
}

// PaperReferenceSourceEdit is the validated repository-facing form of a
// manual source edit. PublishedAtSet separates "field omitted" from "clear the
// date", which a nil *time.Time cannot express.
type PaperReferenceSourceEdit struct {
	DOI            *string
	Title          *string
	Authors        *[]PaperAuthor
	Venue          *string
	PublishedAt    *time.Time
	PublishedAtSet bool
	Volume         *string
	Issue          *string
	Pages          *string
	SourceURL      *string
}

// PaperReferenceIDForRaw derives the stable identity of one reference inside
// one citing paper. The same normalized raw text in two different papers yields
// two different IDs by design.
func PaperReferenceIDForRaw(citingDocumentID uuid.UUID, rawReference string) uuid.UUID {
	normalized := PaperCitationReferenceKey(rawReference)
	return uuid.NewSHA1(
		uuid.NameSpaceURL,
		[]byte("paperMaster:paper-reference:"+citingDocumentID.String()+":"+normalized),
	)
}

// Validate checks persisted reference invariants that all writers share.
func (reference PaperReference) Validate() error {
	if reference.ReferenceID == uuid.Nil {
		return fmt.Errorf("%w: id is required", ErrInvalidPaperReference)
	}
	if reference.CitingDocumentID == uuid.Nil {
		return fmt.Errorf("%w: citing document id is required", ErrInvalidPaperReference)
	}
	if strings.TrimSpace(reference.RawReference) == "" {
		return fmt.Errorf("%w: raw reference is required", ErrInvalidPaperReference)
	}
	if !utf8.ValidString(reference.RawReference) ||
		len(reference.RawReference) > MaxPaperReferenceRawBytes ||
		strings.ContainsRune(reference.RawReference, '\x00') {
		return fmt.Errorf("%w: invalid raw reference", ErrInvalidPaperReference)
	}
	if !reference.ResolutionStatus.Valid() {
		return fmt.Errorf("%w: invalid resolution status %q", ErrInvalidPaperReference, reference.ResolutionStatus)
	}
	if reference.ResolutionGeneration < 0 {
		return fmt.Errorf("%w: resolution generation cannot be negative", ErrInvalidPaperReference)
	}
	if reference.MetadataSource != "" && !reference.MetadataSource.IsValid() {
		return fmt.Errorf("%w: invalid metadata source %q", ErrInvalidPaperReference, reference.MetadataSource)
	}
	if reference.ResolutionErrorCode != nil && len(*reference.ResolutionErrorCode) > MaxPaperReferenceErrorCodeBytes {
		return fmt.Errorf("%w: resolution error code is too long", ErrInvalidPaperReference)
	}
	if reference.ResolutionErrorMsg != nil && len(*reference.ResolutionErrorMsg) > MaxPaperReferenceErrorMessageBytes {
		return fmt.Errorf("%w: resolution error message is too long", ErrInvalidPaperReference)
	}
	return nil
}

// EffectiveMetadataSource treats an unset source as unknown.
func (reference PaperReference) EffectiveMetadataSource() PaperMetadataSource {
	if reference.MetadataSource == "" {
		return PaperMetadataSourceUnknown
	}
	return reference.MetadataSource
}
