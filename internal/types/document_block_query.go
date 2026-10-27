package types

import "errors"

var (
	ErrInvalidBlockQuery      = errors.New("invalid block query")
	ErrSourceSnapshotChanged  = errors.New("source snapshot changed")
	ErrSourceRangeUnavailable = errors.New("source section block range unavailable")
)

// BlockRange is a half-open interval in the committed parser reading order.
type BlockRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// DocumentBlockQuery selects one source section, an ordinal range, explicit IDs,
// or a page. A range response never loads block text.
type DocumentBlockQuery struct {
	SectionOrdinal     *int        `json:"ordinal,omitempty"`
	Range              *BlockRange `json:"block_range,omitempty"`
	BlockIDs           []string    `json:"block_ids,omitempty"`
	PageNumber         int         `json:"page_number,omitempty"`
	Mode               string      `json:"mode,omitempty"`
	IncludeIDs         bool        `json:"include_ids,omitempty"`
	Page               int         `json:"page,omitempty"`
	PageSize           int         `json:"page_size,omitempty"`
	ExpectedGeneration *int64      `json:"expected_generation,omitempty"`
}

type DocumentBlockPage struct {
	DocumentID            string                  `json:"document_id"`
	RecognitionGeneration int64                   `json:"recognition_generation"`
	Section               *DocumentOutlineSection `json:"section,omitempty"`
	Range                 *BlockRange             `json:"block_range,omitempty"`
	Mode                  string                  `json:"mode"`
	Items                 []Block                 `json:"items,omitempty"`
	BlockIDs              []string                `json:"block_ids,omitempty"`
	MissingIDs            []string                `json:"missing_ids,omitempty"`
	Status                string                  `json:"status"`
	Total                 int64                   `json:"total"`
	Page                  int                     `json:"page"`
	PageSize              int                     `json:"page_size"`
	HasMore               bool                    `json:"has_more"`
}
