package interfaces

import (
	"context"

	"paperMaster/internal/types"
)

// CatalogDispatcher is the in-process priority queue that owns provider
// allocation. Source jobs always take precedence over reference jobs.
// Provider execution and durable post-processing happen outside this
// interface; Submit only places a job in the bounded queue.
type CatalogDispatcher interface {
	SubmitSource(ctx context.Context, job types.CatalogSourceJob) error
	SubmitReference(ctx context.Context, job types.CatalogReferenceJob) error
}
