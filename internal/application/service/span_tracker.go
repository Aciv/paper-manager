package service

import "context"

// SpanTracker is the minimal hook used by terminal task callbacks.
type SpanTracker interface {
	FinalizeAttempt(context.Context, string, int, string, error, string, string)
}
