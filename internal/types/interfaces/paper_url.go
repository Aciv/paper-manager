package interfaces

import (
	"context"
	"time"

	"paperMaster/internal/types"
)

// PaperURLResolver converts provider landing/source identity into a trusted
// landing URL and a distinct downloadable PDF URL. It performs no download.
type PaperURLResolver interface {
	Resolve(ctx context.Context, request types.PaperURLResolveRequest) (types.ResolvedPaperURL, error)
}

// RemoteDownloadLimiter bounds remote PDF traffic independently from catalog
// provider calls. Implementations coordinate by final download host.
type RemoteDownloadLimiter interface {
	Acquire(ctx context.Context, host string) (release func(), err error)
	Cooldown(ctx context.Context, host string, delay time.Duration) error
}
