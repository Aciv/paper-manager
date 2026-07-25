package types

import (
	"encoding/json"
	"fmt"
)

// JSON is a portable JSON value used by GORM JSON/JSONB columns.
type JSON json.RawMessage

// MarshalJSON preserves the raw JSON representation when GORM's JSON
// serializer persists a JSON/JSONB column. Without this method a named byte
// slice would be encoded as a base64 JSON string instead of an object/array.
func (j JSON) MarshalJSON() ([]byte, error) {
	if len(j) == 0 {
		return []byte("null"), nil
	}
	if !json.Valid(j) {
		return nil, fmt.Errorf("invalid JSON value")
	}
	return j, nil
}

// UnmarshalJSON copies a raw JSON value after validation.
func (j *JSON) UnmarshalJSON(data []byte) error {
	if !json.Valid(data) {
		return fmt.Errorf("invalid JSON value")
	}
	*j = append((*j)[:0], data...)
	return nil
}
