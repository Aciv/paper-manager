package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	appservice "paperMaster/internal/application/service"
	apperrors "paperMaster/internal/errors"
	"paperMaster/internal/types"
)

const maxToolResponseBytes = 20 << 20

var (
	errInvalidToolInput     = errors.New("invalid tool input")
	errNoteNotFound         = errors.New("reading note not found")
	errSnapshotConflict     = errors.New("source snapshot changed")
	errPayloadTooLarge      = errors.New("payload too large")
	errInvalidEvidence      = errors.New("invalid evidence")
	errLocalImportDisabled  = errors.New("local file import is disabled")
	errLocalImportForbidden = errors.New("local file path is not allowed")
)

type toolFailureDetails struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	Details   any    `json:"details,omitempty"`
}

func successResult(data any) *sdk.CallToolResult {
	envelope := map[string]any{"ok": true, "data": data}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return failureResult(toolFailureDetails{Code: "INTERNAL", Message: "failed to encode tool result"})
	}
	if len(payload) > maxToolResponseBytes {
		return failureResult(toolFailureDetails{
			Code: "PAYLOAD_TOO_LARGE", Message: "tool result exceeds the response limit",
		})
	}
	return &sdk.CallToolResult{
		Content:           []sdk.Content{&sdk.TextContent{Text: string(payload)}},
		StructuredContent: envelope,
	}
}

// successImageResult returns JSON metadata plus one MCP image content block.
// Image bytes stay out of the JSON envelope and are bounded by the caller.
func successImageResult(data any, image []byte, mimeType string) *sdk.CallToolResult {
	envelope := map[string]any{"ok": true, "data": data}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return failureResult(toolFailureDetails{Code: "INTERNAL", Message: "failed to encode tool result"})
	}
	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: string(payload)},
			&sdk.ImageContent{Data: image, MIMEType: mimeType},
		},
		StructuredContent: envelope,
	}
}

func failureResult(details toolFailureDetails) *sdk.CallToolResult {
	envelope := map[string]any{"ok": false, "error": details}
	payload, err := json.Marshal(envelope)
	if err != nil {
		payload = []byte(`{"ok":false,"error":{"code":"INTERNAL","message":"failed to encode tool error","retryable":false}}`)
	}
	return &sdk.CallToolResult{
		IsError:           true,
		Content:           []sdk.Content{&sdk.TextContent{Text: string(payload)}},
		StructuredContent: envelope,
	}
}

func errorResult(err error) *sdk.CallToolResult {
	if err == nil {
		return failureResult(toolFailureDetails{Code: "INTERNAL", Message: "unknown tool error"})
	}
	details := classifyToolError(err)
	return failureResult(details)
}

func classifyToolError(err error) toolFailureDetails {
	switch {
	case errors.Is(err, errNoteNotFound):
		return toolFailureDetails{Code: "NOTE_NOT_FOUND", Message: "reading note does not exist"}
	case errors.Is(err, errInvalidToolInput):
		return toolFailureDetails{Code: "INVALID_ARGUMENT", Message: "invalid tool input"}
	case errors.Is(err, errInvalidEvidence):
		return toolFailureDetails{Code: "INVALID_EVIDENCE", Message: "evidence link is invalid or cannot be resolved"}
	case errors.Is(err, errLocalImportDisabled):
		return toolFailureDetails{Code: "FORBIDDEN", Message: "local file import is disabled; configure MCP_LOCAL_IMPORT_ROOT"}
	case errors.Is(err, errLocalImportForbidden):
		return toolFailureDetails{Code: "FORBIDDEN", Message: "local file path is outside the allowed import root"}
	case errors.Is(err, appservice.ErrInvalidDocumentFilename),
		errors.Is(err, appservice.ErrInvalidDocumentDOI),
		errors.Is(err, appservice.ErrInvalidDocumentBaseName),
		errors.Is(err, appservice.ErrInvalidPaperMetadata),
		errors.Is(err, types.ErrInvalidPaperReference),
		errors.Is(err, types.ErrInvalidPaperCitation),
		errors.Is(err, types.ErrSelfPaperCitation),
		errors.Is(err, types.ErrInvalidDocumentSelection),
		errors.Is(err, types.ErrInvalidPDFRegionRender):
		return toolFailureDetails{Code: "INVALID_ARGUMENT", Message: "invalid request"}
	case errors.Is(err, errSnapshotConflict):
		return toolFailureDetails{Code: "CONFLICT", Message: "source or note snapshot changed; reload before retrying", Retryable: true}
	case errors.Is(err, errPayloadTooLarge),
		errors.Is(err, types.ErrDocumentMarkdownTooLarge),
		errors.Is(err, types.ErrBibliographyBaseTooLarge),
		errors.Is(err, types.ErrBibliographyOutputTooLarge),
		errors.Is(err, appservice.ErrDocumentStructureTooLarge):
		return toolFailureDetails{Code: "PAYLOAD_TOO_LARGE", Message: "request or response exceeds the MCP limit"}
	case errors.Is(err, appservice.ErrDocumentParseActive),
		errors.Is(err, appservice.ErrPaperInfoAttemptActive),
		errors.Is(err, appservice.ErrReferenceAlreadyResolved),
		errors.Is(err, types.ErrReferenceDownloadInProgress),
		errors.Is(err, appservice.ErrCitationTargetNotResolved),
		errors.Is(err, appservice.ErrPaperCannotCite):
		return toolFailureDetails{Code: "CONFLICT", Message: "resource state conflicts with this operation"}
	case errors.Is(err, appservice.ErrDocumentDeleting), errors.Is(err, appservice.ErrPaperInfoDeleting):
		return toolFailureDetails{Code: "CONFLICT", Message: "document is being deleted"}
	case errors.Is(err, appservice.ErrDocumentParseFinished):
		return toolFailureDetails{Code: "CONFLICT", Message: "document parsing has already finished"}
	case errors.Is(err, appservice.ErrPaperInfoExtractionUnavailable),
		errors.Is(err, appservice.ErrPaperInfoCatalogUnavailable),
		errors.Is(err, appservice.ErrDocumentProcessQueue):
		return toolFailureDetails{Code: "UNAVAILABLE", Message: "processing service is temporarily unavailable", Retryable: true}
	case errors.Is(err, types.ErrBibliographyTaskNotFound):
		return toolFailureDetails{Code: "NOT_FOUND", Message: "bibliography task was not found"}
	case errors.Is(err, types.ErrBibliographyTaskUnavailable):
		return toolFailureDetails{Code: "UNAVAILABLE", Message: "bibliography task status is unavailable", Retryable: true}
	case errors.Is(err, types.ErrRemotePDFUnavailable):
		return toolFailureDetails{Code: "UNAVAILABLE", Message: "reference PDF is not available", Retryable: true}
	case errors.Is(err, context.Canceled):
		return toolFailureDetails{Code: "CANCELLED", Message: "tool call was cancelled"}
	case errors.Is(err, context.DeadlineExceeded):
		return toolFailureDetails{Code: "UNAVAILABLE", Message: "tool call exceeded its deadline", Retryable: true}
	case errors.Is(err, sql.ErrNoRows):
		return toolFailureDetails{Code: "NOT_FOUND", Message: "requested resource was not found"}
	case errors.Is(err, types.ErrReadingNoteConflict):
		return toolFailureDetails{Code: "REVISION_CONFLICT", Message: "reading note revision changed"}
	case errors.Is(err, types.ErrReadingNoteUnavailable):
		return toolFailureDetails{Code: "UNAVAILABLE", Message: "reading note is temporarily unavailable", Retryable: true}
	case errors.Is(err, types.ErrInvalidReadingNote):
		return toolFailureDetails{Code: "INVALID_ARGUMENT", Message: "invalid reading note request"}
	case errors.Is(err, types.ErrInvalidRemotePDFRequest):
		return toolFailureDetails{Code: "INVALID_ARGUMENT", Message: "invalid URL import request"}
	case errors.Is(err, types.ErrDuplicateDOI),
		errors.Is(err, types.ErrDuplicateFileHash),
		errors.Is(err, types.ErrDocumentNotProcessing):
		return toolFailureDetails{Code: "CONFLICT", Message: "document state conflicts with this operation"}
	case isInvalidIdentifierError(err):
		return toolFailureDetails{Code: "INVALID_ARGUMENT", Message: "invalid resource identifier"}
	default:
		var serverErr *apperrors.ServerError
		if errors.As(err, &serverErr) {
			switch serverErr.Code {
			case apperrors.ErrBadRequest, apperrors.ErrValidation, apperrors.ErrUnsupportedMedia:
				return toolFailureDetails{Code: "INVALID_ARGUMENT", Message: "invalid request"}
			case apperrors.ErrUnauthorized, apperrors.ErrForbidden:
				return toolFailureDetails{Code: "FORBIDDEN", Message: "access denied"}
			case apperrors.ErrNotFound:
				return toolFailureDetails{Code: "NOT_FOUND", Message: "requested resource was not found"}
			case apperrors.ErrConflict:
				return toolFailureDetails{Code: "CONFLICT", Message: "resource state conflict"}
			case apperrors.ErrPayloadTooLarge:
				return toolFailureDetails{Code: "PAYLOAD_TOO_LARGE", Message: "request or response exceeds the size limit"}
			case apperrors.ErrServiceUnavailable, apperrors.ErrTimeout:
				return toolFailureDetails{Code: "UNAVAILABLE", Message: "service is temporarily unavailable", Retryable: true}
			}
		}
		return toolFailureDetails{Code: "INTERNAL", Message: "internal tool error"}
	}
}

// Several application services wrap UUID parsing failures in plain errors
// rather than a shared sentinel. Keep those user-input failures out of the
// INTERNAL bucket so hosts know not to retry them.
func isInvalidIdentifierError(err error) bool {
	message := err.Error()
	for _, marker := range []string{
		"invalid document id",
		"invalid block id",
		"invalid reference id",
		"invalid document base id",
		"invalid section id",
		"invalid task id",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
