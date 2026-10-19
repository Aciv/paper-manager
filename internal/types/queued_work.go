package types

import (
	"strconv"
)

// QueuedWorkSnapshot is one bounded observation of Asynq's queued/active
// stages. Housekeeping uses it to classify every stale candidate from a single
// queue scan instead of probing the whole backlog once per candidate.
//
// Complete is false when a scan hit its page/task budget. Callers must not
// classify rows as orphaned from an incomplete snapshot.
type QueuedWorkSnapshot struct {
	StageTasks     map[string]struct{}
	ReferenceTasks map[string]struct{}
	Complete       bool
}

// NewQueuedWorkSnapshot creates an empty complete snapshot.
func NewQueuedWorkSnapshot() QueuedWorkSnapshot {
	return QueuedWorkSnapshot{
		StageTasks:     make(map[string]struct{}),
		ReferenceTasks: make(map[string]struct{}),
		Complete:       true,
	}
}

// QueuedStageKey identifies one document stage generation in the snapshot.
func QueuedStageKey(documentID string, stage DocumentStage, generation int64) string {
	return documentID + "\x00" + string(stage) + "\x00" + strconv.FormatInt(generation, 10)
}

// QueuedReferenceKey identifies one reference resolution generation.
func QueuedReferenceKey(referenceID string, generation int64) string {
	return referenceID + "\x00" + strconv.FormatInt(generation, 10)
}

// HasStage reports whether the exact document stage generation is still queued.
func (snapshot QueuedWorkSnapshot) HasStage(documentID string, stage DocumentStage, generation int64) bool {
	_, found := snapshot.StageTasks[QueuedStageKey(documentID, stage, generation)]
	return found
}

// HasReference reports whether the exact reference generation still has work.
func (snapshot QueuedWorkSnapshot) HasReference(referenceID string, generation int64) bool {
	_, found := snapshot.ReferenceTasks[QueuedReferenceKey(referenceID, generation)]
	return found
}
