package service

import (
	"context"
	"errors"
	"strings"
	"sync"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"

	"github.com/google/uuid"
)

// catalogResultKeyLocks serializes compare-and-update for one source document.
// Reference rows already use FinishResolution's row lock and generation check.
var catalogResultKeyLocks sync.Map // map[string]*sync.Mutex

func lockCatalogResult(key string) func() {
	value, _ := catalogResultKeyLocks.LoadOrStore(key, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}

// QueryApply consumes one durable post-processing task emitted by the
// in-process provider dispatcher. It is intentionally idempotent: duplicate
// results either fail the generation check or lose the score comparison.
func (s *paperInfoService) QueryApply(ctx context.Context, command types.PaperQueryApplyCommand) error {
	if err := validateCatalogResultCommand(command); err != nil {
		return err
	}
	ctx = paperInfoContext(ctx, command.RequestID, command.DocumentID)
	switch command.Kind {
	case types.CatalogJobSource:
		return s.paperSourceQueryApply(ctx, command)
	case types.CatalogJobReference:
		return s.referenceQueryApply(ctx, command)
	default:
		return types.ErrInvalidPaperInfoTaskPayload
	}
}

func validateCatalogResultCommand(command types.PaperQueryApplyCommand) error {
	if command.Generation < 1 {
		return errors.New("catalog result generation must be positive")
	}
	switch command.Kind {
	case types.CatalogJobSource:
		if command.DocumentID == uuid.Nil || command.ReferenceID != uuid.Nil {
			return errors.New("source catalog result requires document id")
		}
	case types.CatalogJobReference:
		if command.ReferenceID == uuid.Nil || command.DocumentID == uuid.Nil {
			return errors.New("reference catalog result requires reference and citing document")
		}
	default:
		return errors.New("catalog result kind is invalid")
	}
	if command.Final {
		switch command.Outcome {
		case types.CatalogOutcomeMatched, types.CatalogOutcomeNoMatch, types.CatalogOutcomeFailed:
		default:
			return errors.New("final catalog result outcome is invalid")
		}
	}
	return nil
}

func (s *paperInfoService) paperSourceQueryApply(ctx context.Context, command types.PaperQueryApplyCommand) error {
	unlock := lockCatalogResult("source:" + command.DocumentID.String())
	defer unlock()

	document, err := s.documents.Get(ctx, command.DocumentID)
	if err != nil {
		return err
	}
	if document.SourceState != types.DocumentSourceAvailable {
		logger.Info(ctx, "catalog result ignored after source state change",
			"document_id", command.DocumentID, "source_state", document.SourceState)
		return nil
	}
	stage, err := s.documents.GetStage(ctx, command.DocumentID, types.DocumentStageSourceLookup)
	if err != nil {
		return err
	}
	if stage.Generation != command.Generation {
		logger.Info(ctx, "catalog result ignored after generation change",
			"document_id", command.DocumentID, "generation", command.Generation,
			"current_generation", stage.Generation)
		return nil
	}
	if stage.Status != types.DocumentStageRunning && stage.Status != types.DocumentStageQueued {
		// A duplicate final task or an apply task that lost the race with a
		// completed/cancelled stage must not mutate metadata.
		return nil
	}

	if command.Candidate != nil {
		metadata, err := s.papers.GetMetadata(ctx, command.DocumentID.String())
		if err != nil {
			return err
		}
		if s.sourceCandidateBetter(document, metadata, command.Query, *command.Candidate) {
			if _, err := s.applySourceCatalogCandidate(ctx, document, *command.Candidate); err != nil {
				return err
			}
		} else {
			logger.Debug(ctx, "catalog candidate skipped by score comparison",
				"document_id", command.DocumentID, "provider", command.Candidate.Provider)
		}
	}

	if !command.Final {
		return nil
	}
	status := types.DocumentStageSucceeded
	code, message := "", ""
	if command.Outcome == types.CatalogOutcomeFailed {
		status = types.DocumentStageFailed
		code = strings.TrimSpace(command.ErrorCode)
		if code == "" {
			code = types.ProcessingErrorServiceUnavailable
		}
		message = strings.TrimSpace(command.ErrorMessage)
		if message == "" {
			message = "paper catalog lookup failed"
		}
	}
	_, applied, err := s.documents.FinishStage(
		ctx, command.DocumentID, types.DocumentStageSourceLookup,
		command.Generation, status, code, message,
	)
	if err != nil {
		return err
	}
	if !applied {
		logger.Info(ctx, "catalog stage finish ignored after state change",
			"document_id", command.DocumentID, "generation", command.Generation)
		return nil
	}
	logger.Info(ctx, "paper catalog source result applied",
		"document_id", command.DocumentID, "generation", command.Generation,
		"outcome", command.Outcome)
	return nil
}

// sourceCandidateBetter compares one provider candidate with the current
// durable metadata using the same scoring function that ranks providers.
// Equal scores skip the write, keeping duplicate task execution idempotent.
func (s *paperInfoService) sourceCandidateBetter(
	document types.Document, metadata *types.PaperMetadata,
	query types.PaperLookupQuery, candidate types.PaperCatalogCandidate,
) bool {
	candidateScore, exact := scorePaperCatalogCandidate(query, candidate)
	if exact {
		return true
	}
	if candidateScore < minPaperCatalogMatchScore {
		return false
	}
	current := types.PaperCatalogCandidate{
		Provider: "current", ProviderID: "current", DOI: document.DOI,
		Title: metadata.Title, Authors: metadata.Authors,
		Venue: metadata.Venue, PublishedAt: metadata.PublishedAt,
	}
	currentScore, currentExact := scorePaperCatalogCandidate(query, current)
	if currentExact {
		return false
	}
	return candidateScore > currentScore
}

func (s *paperInfoService) referenceQueryApply(ctx context.Context, command types.PaperQueryApplyCommand) error {
	reference, err := s.papers.GetReference(ctx, command.ReferenceID.String())
	if err != nil {
		return err
	}
	if reference.ResolutionGeneration != command.Generation {
		logger.Info(ctx, "reference catalog result ignored after generation change",
			"reference_id", command.ReferenceID, "generation", command.Generation,
			"current_generation", reference.ResolutionGeneration)
		return nil
	}
	if !command.Final {
		return nil
	}

	status := types.DocumentStageSucceeded
	code, message := "", ""
	if command.Outcome == types.CatalogOutcomeFailed {
		status = types.DocumentStageFailed
		code = strings.TrimSpace(command.ErrorCode)
		if code == "" {
			code = types.ProcessingErrorServiceUnavailable
		}
		message = strings.TrimSpace(command.ErrorMessage)
		if message == "" {
			message = "paper catalog lookup failed"
		}
	}

	var patch *types.PaperReferenceMetadataPatch
	var resolvedDocumentID *uuid.UUID
	if command.Outcome == types.CatalogOutcomeMatched {
		if command.ResolvedDocumentID != uuid.Nil {
			id := command.ResolvedDocumentID
			resolvedDocumentID = &id
		}
		if command.Candidate != nil {
			patch = referenceMetadataPatchFromCatalog(*command.Candidate)
			if resolvedDocumentID == nil {
				candidateReference := *reference
				if command.Candidate.DOI != "" {
					candidateReference.DOI = command.Candidate.DOI
				}
				if command.Candidate.Title != "" {
					candidateReference.Title = command.Candidate.Title
				}
				if len(command.Candidate.Authors) > 0 {
					candidateReference.Authors = command.Candidate.Authors
				}
				if command.Candidate.PublishedAt != nil {
					candidateReference.PublishedAt = command.Candidate.PublishedAt
				}
				documentID, found, matchErr := s.papers.FindLocalReferenceMatch(
					ctx, candidateReference, reference.CitingDocumentID,
				)
				if matchErr != nil {
					return matchErr
				}
				if found {
					resolvedDocumentID = &documentID
				}
			}
		}
	}

	_, applied, err := s.papers.FinishReferenceResolution(
		ctx, command.ReferenceID, command.Generation,
		status, command.Provider, code, message, patch, resolvedDocumentID,
	)
	if err != nil {
		return err
	}
	if !applied {
		logger.Info(ctx, "reference catalog result ignored after generation change",
			"reference_id", command.ReferenceID, "generation", command.Generation)
		return nil
	}
	logger.Info(ctx, "paper catalog reference result applied",
		"reference_id", command.ReferenceID, "generation", command.Generation,
		"outcome", command.Outcome, "resolved", resolvedDocumentID != nil)
	return nil
}
