package mcp

import (
	"encoding/json"
	"fmt"

	"paperMaster/internal/types"
)

type importOptionsInput struct {
	Metadata          *types.PaperMetadataImport     `json:"metadata"`
	Processing        types.DocumentImportProcessing `json:"processing"`
	DuplicateMetadata types.DuplicateMetadataPolicy  `json:"duplicate_metadata"`
}

func (input importOptionsInput) options() []types.DocumentImportOptions {
	if input.Metadata == nil && input.Processing.SourceLookup == "" && input.DuplicateMetadata == "" {
		return nil
	}
	return []types.DocumentImportOptions{{
		Metadata: input.Metadata, Processing: input.Processing, DuplicateMetadata: input.DuplicateMetadata,
	}}
}

func validateMCPImportOptions(raw json.RawMessage, count int, options []types.DocumentImportOptions) error {
	if len(raw) > types.MaxDocumentImportPayloadBytes {
		return fmt.Errorf("%w: import arguments exceed 1 MiB", errInvalidToolInput)
	}
	if options != nil && len(options) != count {
		return fmt.Errorf("%w: options must align with paths or urls", errInvalidToolInput)
	}
	return nil
}
