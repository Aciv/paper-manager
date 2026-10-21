package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"paperMaster/internal/config"
	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
)

type bibliographyService struct {
	repository       interfaces.BibliographySnapshotRepository
	doi              interfaces.DOICitationClient
	tasks            interfaces.TaskEnqueuer
	taskInspector    interfaces.TaskResultInspector
	spans            interfaces.SpanRecorder
	maxEntries       int
	concurrency      int
	totalTimeout     time.Duration
	maxOutputBytes   int64
	maxResponseBytes int64
	taskTimeout      time.Duration
	taskMaxRetry     int
	resultRetention  time.Duration
}

type bibliographyWork struct {
	citation types.DOICitation
	err      error
}

func NewBibliographyService(
	repository interfaces.BibliographySnapshotRepository,
	doi interfaces.DOICitationClient,
	cfg config.Config,
) *bibliographyService {
	return newBibliographyService(repository, doi, nil, nil, nil, cfg)
}

func NewBibliographyServiceWithTaskSystem(
	repository interfaces.BibliographySnapshotRepository,
	doi interfaces.DOICitationClient,
	tasks interfaces.TaskEnqueuer,
	taskInspector interfaces.TaskResultInspector,
	spans interfaces.SpanRecorder,
	cfg config.Config,
) interfaces.BibliographyService {
	return newBibliographyService(repository, doi, tasks, taskInspector, spans, cfg)
}

func newBibliographyService(
	repository interfaces.BibliographySnapshotRepository,
	doi interfaces.DOICitationClient,
	tasks interfaces.TaskEnqueuer,
	taskInspector interfaces.TaskResultInspector,
	spans interfaces.SpanRecorder,
	cfg config.Config,
) *bibliographyService {
	if cfg.BibliographyMaxEntries <= 0 {
		cfg.BibliographyMaxEntries = 200
	}
	if cfg.BibliographyDOIConcurrency <= 0 {
		cfg.BibliographyDOIConcurrency = 4
	}
	if cfg.BibliographyTotalTimeout <= 0 {
		cfg.BibliographyTotalTimeout = 30 * time.Second
	}
	if cfg.BibliographyMaxOutputBytes <= 0 {
		cfg.BibliographyMaxOutputBytes = 8 << 20
	}
	if cfg.BibliographyMaxResponseBytes <= 0 {
		cfg.BibliographyMaxResponseBytes = 12 << 20
	}
	if cfg.BibliographyTaskTimeout <= 0 {
		cfg.BibliographyTaskTimeout = 45 * time.Second
	}
	if cfg.BibliographyTaskMaxRetry <= 0 {
		cfg.BibliographyTaskMaxRetry = 2
	}
	if cfg.BibliographyResultRetention <= 0 {
		cfg.BibliographyResultRetention = 30 * time.Minute
	}
	return &bibliographyService{
		repository: repository, doi: doi, tasks: tasks, taskInspector: taskInspector, spans: spans,
		maxEntries:  cfg.BibliographyMaxEntries,
		concurrency: cfg.BibliographyDOIConcurrency, totalTimeout: cfg.BibliographyTotalTimeout,
		maxOutputBytes: cfg.BibliographyMaxOutputBytes, maxResponseBytes: cfg.BibliographyMaxResponseBytes,
		taskTimeout: cfg.BibliographyTaskTimeout, taskMaxRetry: cfg.BibliographyTaskMaxRetry,
		resultRetention: cfg.BibliographyResultRetention,
	}
}

func (s *bibliographyService) generateBaseBibliography(
	ctx context.Context, rawBaseID string,
) (types.BibliographySnapshot, error) {
	baseID, err := uuid.Parse(strings.TrimSpace(rawBaseID))
	if err != nil {
		return types.BibliographySnapshot{}, fmt.Errorf("invalid document base id: %w", err)
	}
	source, err := s.repository.GetBibliographySnapshot(ctx, baseID, s.maxEntries)
	if err != nil {
		return types.BibliographySnapshot{}, err
	}
	return s.generateBibliographySnapshot(ctx, source)
}

func (s *bibliographyService) generateBibliographySnapshot(
	ctx context.Context, source types.BibliographySnapshotSource,
) (types.BibliographySnapshot, error) {
	works := make([]bibliographyWork, len(source.Entries))
	lookupCtx, cancel := context.WithTimeout(ctx, s.totalTimeout)
	defer cancel()
	jobs := make(chan int)
	workers := s.concurrency
	if workers > len(source.Entries) {
		workers = len(source.Entries)
	}
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := range jobs {
				entry := source.Entries[index]
				if entry.SourceState != types.DocumentSourceAvailable || strings.TrimSpace(entry.DOI) == "" {
					continue
				}
				works[index].citation, works[index].err = s.doi.FetchBibTeX(lookupCtx, entry.DOI)
			}
		}()
	}
	for index := range source.Entries {
		jobs <- index
	}
	close(jobs)
	wait.Wait()
	if err := ctx.Err(); err != nil {
		return types.BibliographySnapshot{}, err
	}

	remoteKeys := make(map[string]int)
	for _, work := range works {
		if work.err == nil && work.citation.CitationKey != "" {
			remoteKeys[strings.ToLower(work.citation.CitationKey)]++
		}
	}
	usedKeys := make(map[string]struct{}, len(remoteKeys)+len(source.Entries))
	for key := range remoteKeys {
		usedKeys[key] = struct{}{}
	}

	reports := make([]types.BibliographyEntryReport, len(source.Entries))
	entries := make([]string, 0, len(source.Entries))
	exportEntries := make([]types.BibliographyExportEntry, 0, len(source.Entries))
	generated, skipped, failed := 0, 0, 0
	for index, entry := range source.Entries {
		report := types.BibliographyEntryReport{
			DocumentID: entry.DocumentID, Title: strings.TrimSpace(entry.Metadata.Title),
			Kind: types.BibliographyKindPaper, MissingFields: []string{}, Warnings: []string{},
		}
		if entry.SourceState != types.DocumentSourceAvailable {
			report.Outcome = types.BibliographyEntrySkipped
			report.Warnings = append(report.Warnings, "document_unavailable")
			reports[index] = report
			skipped++
			continue
		}
		work := works[index]
		if strings.TrimSpace(entry.DOI) != "" && work.err == nil && work.citation.BibTeX != "" {
			report.Outcome = types.BibliographyEntryGenerated
			report.Source = types.BibliographySourceDOIOriginal
			report.CitationKey = work.citation.CitationKey
			if remoteKeys[strings.ToLower(work.citation.CitationKey)] > 1 {
				report.Warnings = append(report.Warnings, "duplicate_citation_key")
			}
			entries = append(entries, work.citation.BibTeX)
			exportEntries = append(exportEntries, types.BibliographyExportEntry{
				Source: types.BibliographyExportSourceDOI, Kind: types.BibliographyExportKindArticle,
				BibTeX: work.citation.BibTeX,
			})
			reports[index] = report
			generated++
			continue
		}

		if strings.TrimSpace(entry.DOI) == "" {
			report.Source = types.BibliographySourceLocalNoDOI
		} else {
			report.Source = types.BibliographySourceLocalDOIFailed
			report.DOIErrorCode = doiErrorCode(work.err)
		}
		report.MissingFields = bibliographyMissingLocalFields(entry.Metadata)
		baseKey := bibliographyLocalKeyBase(entry.Metadata)
		citationKey := allocateBibliographyKey(baseKey, usedKeys)
		text, renderErr := renderLocalBibliographyEntry(citationKey, types.NormalizeDOI(entry.DOI), entry.Metadata)
		if renderErr != nil {
			report.Outcome = types.BibliographyEntryFailed
			report.Warnings = append(report.Warnings, "local_metadata_insufficient")
			failed++
		} else {
			report.Outcome = types.BibliographyEntryGenerated
			report.CitationKey = citationKey
			entries = append(entries, text)
			exportEntries = append(exportEntries, types.BibliographyExportEntry{
				Source: types.BibliographyExportSourceAuto, Kind: types.BibliographyExportKindArticle,
				BibTeX: text,
			})
			generated++
		}
		reports[index] = report
	}

	output := strings.Join(entries, "\n\n")
	if int64(len(output)) > s.maxOutputBytes {
		return types.BibliographySnapshot{}, types.ErrBibliographyOutputTooLarge
	}
	result := types.BibliographySnapshot{
		BaseID: source.Base.ID, BaseName: source.Base.Name, Kind: types.BibliographyKindPaper,
		Filename: bibliographyFilename(source.Base.Name), BibTeX: output, Entries: reports,
		ExportEntries: exportEntries,
		Generated:     generated, Skipped: skipped, Failed: failed,
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return types.BibliographySnapshot{}, fmt.Errorf("encode bibliography response: %w", err)
	}
	if int64(len(encoded)) > s.maxResponseBytes {
		return types.BibliographySnapshot{}, types.ErrBibliographyOutputTooLarge
	}
	logger.Info(ctx, "base bibliography generated", "document_base_id", source.Base.ID,
		"entry_count", len(reports), "generated", generated, "skipped", skipped, "failed", failed,
		"output_bytes", len(output))
	return result, nil
}

func allocateBibliographyKey(base string, used map[string]struct{}) string {
	if base == "" {
		base = "paperndwork"
	}
	if _, exists := used[strings.ToLower(base)]; !exists {
		used[strings.ToLower(base)] = struct{}{}
		return base
	}
	for suffix := 1; ; suffix++ {
		candidate := fmt.Sprintf("%s-%c", base, 'a'+rune(suffix-1))
		if suffix > 26 {
			candidate = fmt.Sprintf("%s-%d", base, suffix)
		}
		if _, exists := used[strings.ToLower(candidate)]; !exists {
			used[strings.ToLower(candidate)] = struct{}{}
			return candidate
		}
	}
}

func doiErrorCode(err error) string {
	if err == nil {
		return "lookup_budget_exhausted"
	}
	var citationErr *types.DOICitationError
	if errors.As(err, &citationErr) && citationErr.Code != "" {
		return citationErr.Code
	}
	return "unknown"
}

var _ interfaces.BibliographyService = (*bibliographyService)(nil)
