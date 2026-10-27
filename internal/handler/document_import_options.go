package handler

import (
	"encoding/json"
	"fmt"

	"paperMaster/internal/types"

	"github.com/gin-gonic/gin"
)

// multipartImportOptions keeps all parallel fields aligned with the original
// file list. Decoding finishes before any file is persisted.
func multipartImportOptions(c *gin.Context, count int, batch bool) ([][]types.DocumentImportOptions, error) {
	metadata, processing, policy := c.PostForm("metadata"), c.PostForm("processing"), c.PostForm("duplicate_metadata")
	if metadata == "" && processing == "" && policy == "" {
		return nil, nil
	}
	if len(metadata)+len(processing)+len(policy) > types.MaxDocumentImportPayloadBytes {
		return nil, fmt.Errorf("%w: import options exceed 1 MiB", types.ErrInvalidDocumentImport)
	}
	options := make([]types.DocumentImportOptions, count)
	if !batch {
		if metadata != "" {
			if err := json.Unmarshal([]byte(metadata), &options[0].Metadata); err != nil {
				return nil, err
			}
		}
		if processing != "" {
			if err := json.Unmarshal([]byte(processing), &options[0].Processing); err != nil {
				return nil, err
			}
		}
		options[0].DuplicateMetadata = types.DuplicateMetadataPolicy(policy)
	} else {
		if metadata != "" {
			var values []*types.PaperMetadataImport
			if err := json.Unmarshal([]byte(metadata), &values); err != nil || len(values) != count {
				return nil, fmt.Errorf("%w: metadata must contain one entry per file", types.ErrInvalidDocumentImport)
			}
			for index := range options {
				options[index].Metadata = values[index]
			}
		}
		if processing != "" {
			var values []types.DocumentImportProcessing
			if err := json.Unmarshal([]byte(processing), &values); err != nil || len(values) != count {
				return nil, fmt.Errorf("%w: processing must contain one entry per file", types.ErrInvalidDocumentImport)
			}
			for index := range options {
				options[index].Processing = values[index]
			}
		}
		if policy != "" {
			var values []types.DuplicateMetadataPolicy
			if err := json.Unmarshal([]byte(policy), &values); err != nil || len(values) != count {
				return nil, fmt.Errorf("%w: duplicate_metadata must align with files", types.ErrInvalidDocumentImport)
			}
			for index := range options {
				options[index].DuplicateMetadata = values[index]
			}
		}
	}
	for _, item := range options {
		if _, err := item.Processing.SourceLookup.Normalized(); err != nil {
			return nil, err
		}
		if _, err := item.DuplicateMetadata.Normalized(); err != nil {
			return nil, err
		}
	}
	return [][]types.DocumentImportOptions{options}, nil
}
