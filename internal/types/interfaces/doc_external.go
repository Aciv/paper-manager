package interfaces

import (
	"context"

	"paperMaster/internal/types"
)

// DocumentRecognizer recognizes one document source. Read is invoked inside
// the Asynq worker, not during HTTP upload. The worker supplies a bounded
// context and uses RecognitionRequest.Validate before calling the provider.
// Implementations should stop network I/O when ctx is cancelled.
type DocumentRecognizer interface {
	// Read converts the requested source into normalized Markdown and optional
	// paper metadata. Request.Content is runtime-only and is never an Asynq
	// payload.
	Read(ctx context.Context, request *types.RecognitionRequest) (*types.RecognitionResult, error)
}

// StorageKeyRecognizer is an optional capability of recognizers which can
// resolve stored inputs without receiving bytes. Existing providers remain
// bytes-only unless they explicitly opt in.
type StorageKeyRecognizer interface {
	DocumentRecognizer
	SupportsStorageKey() bool
}
