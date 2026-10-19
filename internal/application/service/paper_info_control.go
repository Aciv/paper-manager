package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
)

// The methods in this file are paperInfoService's HTTP-facing control plane.
// Current execution state comes from the four durable document stages; queue
// and span data are cleanup and observation inputs only.

func (s *paperInfoService) RetryExtraction(
	ctx context.Context,
	documentID string,
) error {
	document, _, err := s.loadPaperStatusSource(ctx, documentID)
	if err != nil {
		return err
	}
	if document.SourceState == types.DocumentSourceDeleting || document.SourceState == types.DocumentSourceMerged {
		return ErrPaperInfoDeleting
	}
	if document.SourceState != types.DocumentSourceAvailable {
		return ErrPaperInfoExtractionUnavailable
	}
	stage, err := s.documents.GetStage(ctx, document.ID, types.DocumentStageMetadataExtraction)
	if err != nil {
		return err
	}
	if stage.Status.Active() {
		return ErrPaperInfoAttemptActive
	}
	_, err = s.documents.GetMarkdown(ctx, document.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPaperInfoExtractionUnavailable
	}
	if err != nil {
		return err
	}
	stage, applied, err := s.documents.QueueStageNewGeneration(ctx, document.ID, types.DocumentStageMetadataExtraction)
	if err != nil {
		return err
	}
	if !applied {
		return ErrPaperInfoAttemptActive
	}
	err = s.enqueueMetadataStage(ctx, document.ID, "", stage.Generation)
	logger.Info(ctx, "paper metadata extraction retry queued", "document_id", document.ID)
	return err
}

func (s *paperInfoService) RetryCatalogResolution(
	ctx context.Context,
	documentID string,
) error {
	document, metadata, err := s.loadPaperStatusSource(ctx, documentID)
	if err != nil {
		return err
	}
	if document.SourceState == types.DocumentSourceDeleting || document.SourceState == types.DocumentSourceMerged {
		return ErrPaperInfoDeleting
	}
	stage, err := s.documents.GetStage(ctx, document.ID, types.DocumentStageSourceLookup)
	if err != nil {
		return err
	}
	if stage.Status.Active() {
		return ErrPaperInfoAttemptActive
	}
	if !hasUsablePaperLookupIdentity(document.DOI, metadata.Title) {
		return ErrPaperInfoCatalogUnavailable
	}
	stage, applied, err := s.documents.QueueStageNewGeneration(ctx, document.ID, types.DocumentStageSourceLookup)
	if err != nil {
		return err
	}
	if !applied {
		return ErrPaperInfoAttemptActive
	}
	err = s.enqueueSourceStage(ctx, document.ID, "", stage.Generation)
	logger.Info(ctx, "paper catalog resolution retry queued", "document_id", document.ID)
	return err
}

// RetryReferenceResolution retries only the bibliography dispatcher for a
// source document. The dispatcher reloads citations and submits reference jobs
// to the in-process catalog dispatcher; it does not repeat extraction or
// source-paper lookup.
func (s *paperInfoService) RetryReferenceResolution(ctx context.Context, documentID string) error {
	document, _, err := s.loadPaperStatusSource(ctx, documentID)
	if err != nil {
		return err
	}
	if document.SourceState == types.DocumentSourceDeleting || document.SourceState == types.DocumentSourceMerged {
		return ErrPaperInfoDeleting
	}
	if document.SourceState != types.DocumentSourceAvailable {
		return ErrPaperInfoCatalogUnavailable
	}
	stage, err := s.documents.GetStage(ctx, document.ID, types.DocumentStageReferenceLookup)
	if err != nil {
		return err
	}
	if stage.Status.Active() {
		return ErrPaperInfoAttemptActive
	}
	_, err = s.documents.GetMarkdown(ctx, document.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPaperInfoCatalogUnavailable
	}
	if err != nil {
		return err
	}
	stage, applied, err := s.documents.QueueStageNewGeneration(ctx, document.ID, types.DocumentStageReferenceLookup)
	if err != nil {
		return err
	}
	if !applied {
		return ErrPaperInfoAttemptActive
	}
	if err := s.enqueueReferenceStage(ctx, document.ID, "", stage.Generation); err != nil {
		return err
	}
	logger.Info(ctx, "paper reference resolution retry queued", "document_id", document.ID)
	return nil
}

func (s *paperInfoService) enqueueSourceLookupIfNeeded(ctx context.Context, documentID uuid.UUID, requestID string) error {
	state, err := s.documents.GetStage(ctx, documentID, types.DocumentStageSourceLookup)
	if err != nil {
		return err
	}
	if state.Status == types.DocumentStageNotStarted {
		state, _, err = s.documents.QueueStageNewGeneration(ctx, documentID, types.DocumentStageSourceLookup)
		if err != nil {
			return err
		}
	}
	if state.Status != types.DocumentStageQueued {
		return nil
	}
	return s.enqueueSourceStage(ctx, documentID, requestID, state.Generation)
}

func (s *paperInfoService) enqueueReferenceLookupIfNeeded(ctx context.Context, documentID uuid.UUID, requestID string) error {
	state, err := s.documents.GetStage(ctx, documentID, types.DocumentStageReferenceLookup)
	if err != nil {
		return err
	}
	if state.Status == types.DocumentStageNotStarted {
		state, _, err = s.documents.QueueStageNewGeneration(ctx, documentID, types.DocumentStageReferenceLookup)
		if err != nil {
			return err
		}
	}
	if state.Status != types.DocumentStageQueued {
		return nil
	}
	return s.enqueueReferenceStage(ctx, documentID, requestID, state.Generation)
}

func (s *paperInfoService) enqueueMetadataStage(ctx context.Context, documentID uuid.UUID, requestID string, generation int64) error {
	payload := types.PaperMetadataExtractPayload{
		RequestID: requestID, DocumentID: documentID.String(),
		Stage: types.DocumentStageMetadataExtraction, Generation: generation,
	}
	task, options, err := NewPaperMetadataExtractTask(payload, s.cfg.PaperExtractionTaskTimeout, s.cfg.PaperExtractionTaskMaxRetry)
	if err == nil {
		err = enqueueDocumentStageTask(ctx, s.tasks, task, options, documentID.String())
	}
	if err != nil {
		_, _, _ = s.documents.FinishStage(context.WithoutCancel(ctx), documentID,
			types.DocumentStageMetadataExtraction, generation, types.DocumentStageFailed, types.ProcessingErrorTaskFailed, "processing task could not be queued")
	}
	return err
}

func (s *paperInfoService) enqueueSourceStage(ctx context.Context, documentID uuid.UUID, requestID string, generation int64) error {
	payload := types.PaperSourceQueryEnqueuePayload{
		RequestID: requestID, DocumentID: documentID.String(),
		Stage: types.DocumentStageSourceLookup, Generation: generation,
	}
	task, options, err := NewPaperSourceQueryEnqueueTask(payload, s.cfg.PaperCatalogTaskTimeout, s.cfg.PaperCatalogTaskMaxRetry)
	if err == nil {
		err = enqueueDocumentStageTask(ctx, s.tasks, task, options, documentID.String())
	}
	if err != nil {
		_, _, _ = s.documents.FinishStage(context.WithoutCancel(ctx), documentID,
			types.DocumentStageSourceLookup, generation, types.DocumentStageFailed, types.ProcessingErrorTaskFailed, "processing task could not be queued")
	}
	return err
}

func (s *paperInfoService) enqueueReferenceStage(ctx context.Context, documentID uuid.UUID, requestID string, generation int64) error {
	payload := types.PaperReferenceQueryEnqueuePayload{
		RequestID: requestID, DocumentID: documentID.String(),
		Stage: types.DocumentStageReferenceLookup, Generation: generation,
	}
	task, options, err := NewPaperReferenceQueryEnqueueTask(payload, s.cfg.PaperCatalogTaskTimeout, s.cfg.PaperCatalogTaskMaxRetry)
	if err == nil {
		err = enqueueDocumentStageTask(ctx, s.tasks, task, options, documentID.String())
	}
	if err != nil {
		_, _, _ = s.documents.FinishStage(context.WithoutCancel(ctx), documentID,
			types.DocumentStageReferenceLookup, generation, types.DocumentStageFailed, types.ProcessingErrorTaskFailed, "processing task could not be queued")
	}
	return err
}

// Cancel fences only the source document's active reference-lookup stage.
// Metadata extraction, source lookup, and already-dispatched target work are
// independent and continue to their own terminal states.
func (s *paperInfoService) Cancel(ctx context.Context, documentID string) error {
	document, _, err := s.loadPaperStatusSource(ctx, documentID)
	if err != nil {
		return err
	}
	if document.SourceState == types.DocumentSourceDeleting || document.SourceState == types.DocumentSourceMerged {
		return ErrPaperInfoDeleting
	}
	stage, err := s.documents.GetStage(ctx, document.ID, types.DocumentStageReferenceLookup)
	if err != nil {
		return err
	}
	if stage.Status.Active() {
		cancelledStage, applied, err := s.documents.CancelStage(ctx, document.ID, types.DocumentStageReferenceLookup, stage.Generation)
		if err != nil {
			return err
		}
		if applied {
			stage = cancelledStage
		} else {
			stage, err = s.documents.GetStage(ctx, document.ID, types.DocumentStageReferenceLookup)
			if err != nil {
				return err
			}
		}
	}
	// Do not touch target source/PDF tasks: the source stage owns only this
	// document's reference discovery/dispatch envelope.
	if stage.Status == types.DocumentStageCancelled {
		cancelStageProjectionAndTasks(ctx, s.inspector, s.spans, document.ID, types.DocumentStageReferenceLookup, stage.Generation)
	}
	logger.Info(ctx, "reference lookup cancellation completed", "document_id", document.ID, "generation", stage.Generation)
	return nil
}

func (s *paperInfoService) loadPaperStatusSource(
	ctx context.Context,
	documentID string,
) (types.Document, *types.PaperMetadata, error) {
	id, err := uuid.Parse(strings.TrimSpace(documentID))
	if err != nil {
		return types.Document{}, nil, fmt.Errorf("invalid document id: %w", err)
	}
	if id == uuid.Nil {
		return types.Document{}, nil, errors.New("invalid document id: UUID must be non-zero")
	}
	document, err := s.documents.Get(ctx, id)
	if err != nil {
		return types.Document{}, nil, err
	}
	metadata, err := s.papers.GetMetadata(ctx, document.ID.String())
	if err != nil {
		return types.Document{}, nil, err
	}
	return document, metadata, nil
}

var _ interfaces.PaperInfoService = (*paperInfoService)(nil)
