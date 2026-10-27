package types

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// SourceLookupMode controls automatic catalog lookup only. A manual catalog
// retry may run once without changing the saved mode.
type SourceLookupMode string

const (
	SourceLookupAuto SourceLookupMode = "auto"
	SourceLookupSkip SourceLookupMode = "skip"
)

var ErrInvalidDocumentImport = errors.New("invalid document import options")

// Normalized supplies the backwards-compatible default. Invalid persisted or
// supplied values must fail rather than silently disable a workflow stage.
func (mode SourceLookupMode) Normalized() (SourceLookupMode, error) {
	switch mode {
	case "", SourceLookupAuto:
		return SourceLookupAuto, nil
	case SourceLookupSkip:
		return SourceLookupSkip, nil
	default:
		return "", fmt.Errorf("%w: source_lookup must be auto or skip", ErrInvalidDocumentImport)
	}
}

// DocumentImportProcessing deliberately exposes only the source lookup switch.
// Parsing, metadata extraction and reference lookup retain their existing policy.
type DocumentImportProcessing struct {
	SourceLookup SourceLookupMode `json:"source_lookup,omitempty"`
}

func (processing *DocumentImportProcessing) UnmarshalJSON(data []byte) error {
	type plain DocumentImportProcessing
	var decoded plain
	if err := decodeImportObject(data, &decoded); err != nil {
		return err
	}
	if _, err := decoded.SourceLookup.Normalized(); err != nil {
		return err
	}
	*processing = DocumentImportProcessing(decoded)
	return nil
}

// DuplicateMetadataPolicy applies only to an already imported document. It
// never changes that document's saved processing mode or restarts its tasks.
type DuplicateMetadataPolicy string

const (
	DuplicateMetadataNone        DuplicateMetadataPolicy = "none"
	DuplicateMetadataFillMissing DuplicateMetadataPolicy = "fill_missing"
	DuplicateMetadataReplace     DuplicateMetadataPolicy = "replace"
)

func (policy DuplicateMetadataPolicy) Normalized() (DuplicateMetadataPolicy, error) {
	switch policy {
	case "", DuplicateMetadataFillMissing:
		return DuplicateMetadataFillMissing, nil
	case DuplicateMetadataNone, DuplicateMetadataReplace:
		return policy, nil
	default:
		return "", fmt.Errorf("%w: duplicate_metadata must be none, fill_missing or replace", ErrInvalidDocumentImport)
	}
}

const (
	MaxDocumentImportMetadataBytes = 64 << 10
	MaxDocumentImportPayloadBytes  = 1 << 20
)

// PaperMetadataImport uses the same public field names and optional patch
// semantics as metadata editing. DOI belongs to documents and is promoted
// before deduplication. Provenance is assigned by the service, never clients.
type PaperMetadataImport struct {
	PaperMetadataUpdate
	DOI string `json:"doi,omitempty"`
}

func (metadata *PaperMetadataImport) UnmarshalJSON(data []byte) error {
	if len(data) > MaxDocumentImportMetadataBytes {
		return fmt.Errorf("%w: metadata exceeds 64 KiB", ErrInvalidDocumentImport)
	}
	type plain PaperMetadataImport
	var decoded plain
	wire := struct {
		*plain
		PublishedAt json.RawMessage `json:"publishedAt"`
	}{plain: &decoded}
	if err := decodeImportObject(data, &wire); err != nil {
		return err
	}
	if len(wire.PublishedAt) > 0 && string(wire.PublishedAt) != "null" {
		var value string
		if err := json.Unmarshal(wire.PublishedAt, &value); err != nil {
			return fmt.Errorf("%w: publishedAt must be a date string", ErrInvalidDocumentImport)
		}
		date, err := time.Parse(time.RFC3339, value)
		if err != nil {
			date, err = time.Parse(time.DateOnly, value)
		}
		if err != nil {
			return fmt.Errorf("%w: publishedAt must be YYYY-MM-DD or RFC3339", ErrInvalidDocumentImport)
		}
		decoded.PublishedAt = &date
	}
	*metadata = PaperMetadataImport(decoded)
	return nil
}

// DocumentImportOptions is shared by local files, multipart uploads and URL
// commands. Missing options preserve the established automatic pipeline.
type DocumentImportOptions struct {
	Metadata          *PaperMetadataImport     `json:"metadata,omitempty"`
	Processing        DocumentImportProcessing `json:"processing,omitempty"`
	DuplicateMetadata DuplicateMetadataPolicy  `json:"duplicate_metadata,omitempty"`
}

// DocumentImportMetadataResult is independent of source acceptance and parsing.
// A failed metadata patch does not discard a successfully stored document.
type DocumentImportMetadataResult struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

func (command DocumentURLImportCommand) ImportOptions() DocumentImportOptions {
	return DocumentImportOptions{
		Metadata: command.Metadata, Processing: command.Processing,
		DuplicateMetadata: command.DuplicateMetadata,
	}
}

func (options *DocumentImportOptions) UnmarshalJSON(data []byte) error {
	type plain DocumentImportOptions
	var decoded plain
	if err := decodeImportObject(data, &decoded); err != nil {
		return err
	}
	if _, err := decoded.DuplicateMetadata.Normalized(); err != nil {
		return err
	}
	*options = DocumentImportOptions(decoded)
	return nil
}

func decodeImportObject(data []byte, target any) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf("%w: expected an object", ErrInvalidDocumentImport)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: malformed or unknown import field", ErrInvalidDocumentImport)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("%w: expected one object", ErrInvalidDocumentImport)
	}
	return nil
}
