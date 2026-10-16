package papercatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"paperMaster/internal/config"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const referenceSchedulerTTL = 24 * time.Hour

// NewReferenceScheduler creates the shared reference-work scheduler. Redis is
// used whenever configured so all API/worker processes consume one provider
// rotation. Lite mode keeps the same semantics within the one process.
func NewReferenceScheduler(cfg config.Config) interfaces.PaperReferenceScheduler {
	providers := configuredReferenceProviders(cfg)
	if addr := strings.TrimSpace(os.Getenv("REDIS_ADDR")); addr != "" {
		db, _ := strconv.Atoi(strings.TrimSpace(os.Getenv("REDIS_DB")))
		return &redisReferenceScheduler{
			client:    redis.NewClient(&redis.Options{Addr: addr, Username: strings.TrimSpace(os.Getenv("REDIS_USERNAME")), Password: os.Getenv("REDIS_PASSWORD"), DB: db}),
			providers: providers,
		}
	}
	return newMemoryReferenceScheduler(providers)
}

// NewMemoryReferenceScheduler creates the in-process scheduler explicitly.
// It is useful for Lite mode and focused tests that must not depend on Redis.
func NewMemoryReferenceScheduler(cfg config.Config) interfaces.PaperReferenceScheduler {
	return newMemoryReferenceScheduler(configuredReferenceProviders(cfg))
}

func newMemoryReferenceScheduler(providers []types.PaperCatalogProvider) interfaces.PaperReferenceScheduler {
	return &memoryReferenceScheduler{jobs: make(map[uuid.UUID]types.PaperReferenceJob), providers: providers}
}

func configuredReferenceProviders(cfg config.Config) []types.PaperCatalogProvider {
	available := make(map[types.PaperCatalogProvider]struct{}, len(cfg.PaperCatalogProviders))
	for _, value := range cfg.PaperCatalogProviders {
		if provider := types.NormalizePaperCatalogProvider(value); provider != "" {
			available[provider] = struct{}{}
		}
	}
	order := []types.PaperCatalogProvider{
		types.PaperCatalogProviderArXiv,
		types.PaperCatalogProviderOpenAlex,
		types.PaperCatalogProviderSemanticScholar,
		types.PaperCatalogProviderCrossref,
	}
	providers := make([]types.PaperCatalogProvider, 0, len(order))
	for _, provider := range order {
		if len(available) == 0 {
			providers = append(providers, provider)
			continue
		}
		if _, ok := available[provider]; ok {
			providers = append(providers, provider)
		}
	}
	return providers
}

func weightedReferenceProviderRing(providers []types.PaperCatalogProvider) []types.PaperCatalogProvider {
	weights := map[types.PaperCatalogProvider]int{
		types.PaperCatalogProviderArXiv:           45,
		types.PaperCatalogProviderOpenAlex:        35,
		types.PaperCatalogProviderSemanticScholar: 10,
		types.PaperCatalogProviderCrossref:        10,
	}
	if len(providers) == 0 {
		return nil
	}
	total := 0
	for _, provider := range providers {
		total += weights[provider]
	}
	if total == 0 {
		return nil
	}
	ring := make([]types.PaperCatalogProvider, 0, total)
	current := make(map[types.PaperCatalogProvider]int, len(providers))
	for range total {
		chosen := providers[0]
		for _, provider := range providers {
			current[provider] += weights[provider]
			if current[provider] > current[chosen] {
				chosen = provider
			}
		}
		current[chosen] -= total
		ring = append(ring, chosen)
	}
	return ring
}

func chooseReferenceProvider(job types.PaperReferenceJob, providers []types.PaperCatalogProvider, turn uint64, cooling func(types.PaperCatalogProvider) bool) types.PaperCatalogProvider {
	ring := weightedReferenceProviderRing(providers)
	if len(ring) == 0 {
		return ""
	}
	tried := make(map[types.PaperCatalogProvider]struct{}, len(job.TriedProviders))
	for _, provider := range job.TriedProviders {
		tried[provider] = struct{}{}
	}
	var fallback types.PaperCatalogProvider
	for offset := range ring {
		// turn for turn
		candidate := ring[(int(turn)+offset)%len(ring)]
		if _, used := tried[candidate]; used {
			continue
		}
		if fallback == "" {
			fallback = candidate
		}
		if !cooling(candidate) {
			return candidate
		}
	}
	// Every remaining provider is cooling down. Returning the next weighted
	// candidate lets the provider-level global throttle perform its
	// context-aware wait instead of dropping this durable job.
	return fallback
}

func prepareReferenceJob(job types.PaperReferenceJob) (types.PaperReferenceJob, error) {
	if err := job.Validate(); err != nil {
		return types.PaperReferenceJob{}, err
	}
	now := time.Now().UTC()
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	job.UpdatedAt = now
	job.Provider = ""
	job.InFlight = false
	job.Completed = false
	job.TriedProviders = nil
	return job, nil
}

func applyRegistration(existing types.PaperReferenceJob, incoming types.PaperReferenceJob) (types.PaperReferenceJob, bool) {
	if incoming.Generation > existing.Generation {
		return incoming, true
	}
	if incoming.Generation < existing.Generation {
		return existing, false
	}
	if existing.Completed {
		return incoming, true
	}
	existing.UpdatedAt = time.Now().UTC()
	return existing, false
}

// get provide, claim job in flight
func applyClaim(job types.PaperReferenceJob, providers []types.PaperCatalogProvider, turn uint64, cooling func(types.PaperCatalogProvider) bool) (types.PaperReferenceJob, types.PaperReferenceClaim) {
	if job.Completed || job.InFlight {
		return job, types.PaperReferenceClaim{Job: job}
	}
	provider := chooseReferenceProvider(job, providers, turn, cooling)
	if provider == "" {
		job.Completed = true
		job.UpdatedAt = time.Now().UTC()
		return job, types.PaperReferenceClaim{Job: job}
	}
	job.Provider = provider
	job.InFlight = true
	job.UpdatedAt = time.Now().UTC()
	return job, types.PaperReferenceClaim{Job: job, Provider: provider}
}

// shutdown inflight and report
func applyReport(job types.PaperReferenceJob, provider types.PaperCatalogProvider, outcome types.PaperReferenceOutcome) (types.PaperReferenceJob, types.PaperReferenceReport, error) {
	if job.Provider != provider || !job.InFlight {
		return job, types.PaperReferenceReport{}, fmt.Errorf("reference scheduler claim is no longer active")
	}
	job.InFlight = false
	job.Provider = ""
	job.UpdatedAt = time.Now().UTC()
	if outcome == types.PaperReferenceOutcomeMatched || outcome == types.PaperReferenceOutcomeTerminalFailure {
		job.Completed = true
		return job, types.PaperReferenceReport{Completed: true}, nil
	}
	for _, tried := range job.TriedProviders {
		if tried == provider {
			return job, types.PaperReferenceReport{Requeue: true}, nil
		}
	}
	job.TriedProviders = append(job.TriedProviders, provider)
	return job, types.PaperReferenceReport{Requeue: true}, nil
}

type memoryReferenceScheduler struct {
	mu        sync.Mutex
	jobs      map[uuid.UUID]types.PaperReferenceJob
	providers []types.PaperCatalogProvider
	turn      uint64
	cooldown  map[types.PaperCatalogProvider]time.Time
}

func (s *memoryReferenceScheduler) Register(_ context.Context, job types.PaperReferenceJob) (bool, error) {
	job, err := prepareReferenceJob(job)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.jobs[job.TargetDocumentID]; ok {
		registered, queue := applyRegistration(existing, job)
		s.jobs[job.TargetDocumentID] = registered
		return queue, nil
	}
	s.jobs[job.TargetDocumentID] = job
	return true, nil
}
func (s *memoryReferenceScheduler) Claim(_ context.Context, target uuid.UUID) (types.PaperReferenceClaim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[target]
	if !ok {
		return types.PaperReferenceClaim{}, fmt.Errorf("reference scheduler job not found")
	}
	if s.cooldown == nil {
		s.cooldown = make(map[types.PaperCatalogProvider]time.Time)
	}
	job, claim := applyClaim(job, s.providers, s.turn, func(provider types.PaperCatalogProvider) bool {
		return s.cooldown[provider].After(time.Now())
	})
	s.turn++
	s.jobs[target] = job
	return claim, nil
}
func (s *memoryReferenceScheduler) Report(_ context.Context, target uuid.UUID, provider types.PaperCatalogProvider, outcome types.PaperReferenceOutcome) (types.PaperReferenceReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[target]
	if !ok {
		return types.PaperReferenceReport{}, fmt.Errorf("reference scheduler job not found")
	}
	job, report, err := applyReport(job, provider, outcome)
	if err != nil {
		return report, err
	}
	s.jobs[target] = job
	return report, nil
}

// mark
func (s *memoryReferenceScheduler) Cooldown(_ context.Context, provider types.PaperCatalogProvider, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cooldown == nil {
		s.cooldown = make(map[types.PaperCatalogProvider]time.Time)
	}
	until := time.Now().Add(delay)
	if s.cooldown[provider].Before(until) {
		s.cooldown[provider] = until
	}
	return nil
}

type redisReferenceScheduler struct {
	client    *redis.Client
	providers []types.PaperCatalogProvider
}

func referenceSchedulerJobKey(id uuid.UUID) string {
	return "papers:reference:scheduler:job:" + id.String()
}
func referenceSchedulerLockKey(id uuid.UUID) string {
	return "papers:reference:scheduler:lock:" + id.String()
}
func referenceSchedulerCooldownKey(provider types.PaperCatalogProvider) string {
	return "papers:reference:scheduler:provider:" + provider.String() + ":cooldown"
}

const referenceSchedulerTurnKey = "papers:reference:scheduler:turn"

func (s *redisReferenceScheduler) Register(ctx context.Context, job types.PaperReferenceJob) (bool, error) {
	job, err := prepareReferenceJob(job)
	if err != nil {
		return false, err
	}
	encoded, err := json.Marshal(job)
	if err != nil {
		return false, err
	}
	key := referenceSchedulerJobKey(job.TargetDocumentID)
	// atomic set k-v with expire time when k not exist,
	created, err := s.client.SetNX(ctx, key, encoded, referenceSchedulerTTL).Result()
	if err != nil {
		return false, fmt.Errorf("register reference scheduler job: %w", err)
	}
	if !created {
		// A scheduler key may outlive the Asynq task that owned it. The durable
		// stage generation distinguishes a duplicate producer from an explicit
		// retry that must take over stale in-flight/nonterminal state.
		shouldQueue := false
		if err := s.withJobLock(ctx, job.TargetDocumentID, func() error {
			existing, err := s.load(ctx, job.TargetDocumentID)
			if err != nil {
				return err
			}
			registered, queue := applyRegistration(existing, job)
			shouldQueue = queue
			return s.save(ctx, registered)
		}); err != nil {
			return false, err
		}
		created = shouldQueue
	}
	return created, nil
}
func (s *redisReferenceScheduler) load(ctx context.Context, id uuid.UUID) (types.PaperReferenceJob, error) {
	raw, err := s.client.Get(ctx, referenceSchedulerJobKey(id)).Bytes()
	if err != nil {
		return types.PaperReferenceJob{}, err
	}
	var job types.PaperReferenceJob
	if err := json.Unmarshal(raw, &job); err != nil {
		return types.PaperReferenceJob{}, err
	}
	if err := job.Validate(); err != nil {
		return types.PaperReferenceJob{}, fmt.Errorf("unsupported reference scheduler job state: %w", err)
	}
	return job, nil
}
func (s *redisReferenceScheduler) save(ctx context.Context, job types.PaperReferenceJob) error {
	raw, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return s.client.Set(ctx, referenceSchedulerJobKey(job.TargetDocumentID), raw, referenceSchedulerTTL).Err()
}
func (s *redisReferenceScheduler) withJobLock(ctx context.Context, id uuid.UUID, work func() error) error {
	key := referenceSchedulerLockKey(id)
	for {
		locked, err := s.client.SetNX(ctx, key, "1", 15*time.Second).Result()
		if err != nil {
			return err
		}
		if locked {
			defer s.client.Del(context.Background(), key)
			return work()
		}
		if err := waitForReferenceSchedulerLock(ctx, 25*time.Millisecond); err != nil {
			return err
		}
	}
}

func waitForReferenceSchedulerLock(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *redisReferenceScheduler) Claim(ctx context.Context, target uuid.UUID) (types.PaperReferenceClaim, error) {
	var claim types.PaperReferenceClaim
	err := s.withJobLock(ctx, target, func() error {
		job, err := s.load(ctx, target)
		if err != nil {
			return err
		}
		turn, err := s.client.Incr(ctx, referenceSchedulerTurnKey).Uint64()
		if err != nil {
			return err
		}
		job, claim = applyClaim(job, s.providers, turn, func(provider types.PaperCatalogProvider) bool {
			remaining, err := s.client.PTTL(ctx, referenceSchedulerCooldownKey(provider)).Result()
			return err == nil && remaining > 0
		})
		return s.save(ctx, job)
	})
	return claim, err
}
func (s *redisReferenceScheduler) Report(ctx context.Context, target uuid.UUID, provider types.PaperCatalogProvider, outcome types.PaperReferenceOutcome) (types.PaperReferenceReport, error) {
	var report types.PaperReferenceReport
	err := s.withJobLock(ctx, target, func() error {
		job, err := s.load(ctx, target)
		if err != nil {
			return err
		}
		job, report, err = applyReport(job, provider, outcome)
		if err != nil {
			return err
		}
		return s.save(ctx, job)
	})
	return report, err
}
func (s *redisReferenceScheduler) Cooldown(ctx context.Context, provider types.PaperCatalogProvider, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	return s.client.Set(ctx, referenceSchedulerCooldownKey(provider), "1", delay).Err()
}

var _ interfaces.PaperReferenceScheduler = (*memoryReferenceScheduler)(nil)
var _ interfaces.PaperReferenceScheduler = (*redisReferenceScheduler)(nil)
