package types

import "testing"

func TestProcessingRunStateClassification(t *testing.T) {
	tests := []struct {
		state    ProcessingRunState
		active   bool
		terminal bool
	}{
		{ProcessingRunQueued, true, false},
		{ProcessingRunRunning, true, false},
		{ProcessingRunCancelRequested, true, false},
		{ProcessingRunSucceeded, false, true},
		{ProcessingRunFailed, false, true},
		{ProcessingRunCancelled, false, true},
		{"unknown", false, false},
	}
	for _, test := range tests {
		if got := test.state.Active(); got != test.active {
			t.Errorf("state %q Active() = %t, want %t", test.state, got, test.active)
		}
		if got := test.state.Terminal(); got != test.terminal {
			t.Errorf("state %q Terminal() = %t, want %t", test.state, got, test.terminal)
		}
	}
}
