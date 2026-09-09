package repository

import (
	"testing"

	"paperMaster/internal/types"
)

func TestAggregateDisplayStatusPriority(t *testing.T) {
	tests := []struct {
		name                      string
		active, failed, cancelled int64
		want                      types.ProcessingSpanStatus
	}{
		{name: "success", want: types.ProcessingSpanSucceeded},
		{name: "failure", failed: 1, want: types.ProcessingSpanFailed},
		{name: "cancellation wins over failure", failed: 1, cancelled: 1, want: types.ProcessingSpanCancelled},
		{name: "active remains open", active: 1, failed: 1, cancelled: 1, want: types.ProcessingSpanRunning},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, _, _ := aggregateDisplayStatus(test.active, test.failed, test.cancelled)
			if status != test.want {
				t.Fatalf("aggregate status = %q, want %q", status, test.want)
			}
		})
	}
}
