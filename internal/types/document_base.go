package types

import (
	"time"

	"github.com/google/uuid"
)

// DocumentBase is a named collection of source documents. Operations on a
// base affect only its memberships and never alter the source document row.
type DocumentBase struct {
	ID          uuid.UUID `gorm:"column:id;type:uuid;primaryKey" json:"id"`
	Name        string    `gorm:"column:name;type:text;not null" json:"name"`
	Description string    `gorm:"column:description;type:text;not null;default:''" json:"description"`
	Settings    JSON      `gorm:"column:settings;type:jsonb;serializer:json;not null" json:"settings,omitempty"`
	CreatedAt   time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	UpdatedAt   time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
}

func (DocumentBase) TableName() string { return "document_bases" }

// DocumentBaseFile is the many-to-many membership record. Metadata is
// deliberately base-local, for example reading state, annotations, or a
// display-title override; it must not change paper_metadata.
type DocumentBaseFile struct {
	DocumentBaseID uuid.UUID `gorm:"column:document_base_id;type:uuid;primaryKey" json:"documentBaseId"`
	DocumentID     uuid.UUID `gorm:"column:document_id;type:uuid;primaryKey" json:"documentId"`
	AddedAt        time.Time `gorm:"column:added_at;autoCreateTime" json:"addedAt"`
	SortOrder      int       `gorm:"column:sort_order;not null;default:0" json:"sortOrder"`
	Metadata       JSON      `gorm:"column:metadata;type:jsonb;serializer:json;not null" json:"metadata,omitempty"`
}

func (DocumentBaseFile) TableName() string { return "document_base_files" }
