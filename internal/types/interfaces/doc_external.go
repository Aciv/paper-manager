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
