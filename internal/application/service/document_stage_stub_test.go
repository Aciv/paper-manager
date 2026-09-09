package service

import (
	"time"

	"paperMaster/internal/types"
)

func queueStubDocumentStage(document *types.Document, stage types.DocumentStage) (types.DocumentStageState, bool, error) {
	state, err := document.StageState(stage)
	if err != nil {
		return state, false, err
	}
	if state.Status == "" {
		state.Status = types.DocumentStageNotStarted
	}
	if state.Status.Active() {
		return state, false, nil
	}
	state.Status = types.DocumentStageQueued
	state.Generation++
	state.UpdatedAt = time.Now().UTC()
	setStubDocumentStage(document, state)
	return state, true, nil
}

func transitionStubDocumentStage(document *types.Document, stage types.DocumentStage, generation int64, from []types.DocumentStageStatus, to types.DocumentStageStatus) (types.DocumentStageState, bool, error) {
	state, err := document.StageState(stage)
	if err != nil {
		return state, false, err
	}
	allowed := false
	for _, status := range from {
		allowed = allowed || state.Status == status
	}
	if state.Generation != generation || !allowed {
		return state, false, nil
	}
	state.Status, state.UpdatedAt = to, time.Now().UTC()
	setStubDocumentStage(document, state)
	return state, true, nil
}

func setStubDocumentStage(document *types.Document, state types.DocumentStageState) {
	switch state.Stage {
	case types.DocumentStageMarkdown:
		document.MarkdownStatus, document.MarkdownGeneration, document.MarkdownUpdatedAt = state.Status, state.Generation, state.UpdatedAt
	case types.DocumentStageMetadataExtraction:
		document.MetadataExtractionStatus, document.MetadataExtractionGeneration, document.MetadataExtractionUpdatedAt = state.Status, state.Generation, state.UpdatedAt
	case types.DocumentStageSourceLookup:
		document.SourceLookupStatus, document.SourceLookupGeneration, document.SourceLookupUpdatedAt = state.Status, state.Generation, state.UpdatedAt
	case types.DocumentStageReferenceLookup:
		document.ReferenceLookupStatus, document.ReferenceLookupGeneration, document.ReferenceLookupUpdatedAt = state.Status, state.Generation, state.UpdatedAt
	}
}
