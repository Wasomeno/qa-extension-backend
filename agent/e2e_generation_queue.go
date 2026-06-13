package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"qa-extension-backend/auth"
	"qa-extension-backend/database"
	"qa-extension-backend/internal/models"
	"qa-extension-backend/services"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

type E2EGenerationJobStatus string

const (
	E2EGenerationJobQueued     E2EGenerationJobStatus = "queued"
	E2EGenerationJobProcessing E2EGenerationJobStatus = "processing"
	E2EGenerationJobCompleted  E2EGenerationJobStatus = "completed"
	E2EGenerationJobFailed     E2EGenerationJobStatus = "failed"
)

const (
	e2eGenerationQueueKey      = "generation:e2e:queue"
	e2eGenerationProcessingKey = "generation:e2e:processing"
	e2eGenerationJobKeyPrefix  = "generation:e2e:job:"
	e2eGenerationInflightPref  = "generation:e2e:inflight:"
	e2eGenerationJobTTL        = 24 * time.Hour
	e2eGenerationBatchSize     = 5
)

type E2EGenerationJob struct {
	ID             string                 `json:"id"`
	Status         E2EGenerationJobStatus `json:"status"`
	ScenarioID     string                 `json:"scenarioId"`
	RepoID         string                 `json:"repoId,omitempty"`
	TestCaseIDs    []string               `json:"testCaseIds"`
	AuthSessionID  string                 `json:"authSessionId,omitempty"`
	Attempts       int                    `json:"attempts"`
	MaxAttempts    int                    `json:"maxAttempts"`
	LeaseUntil     time.Time              `json:"leaseUntil,omitempty"`
	Error          string                 `json:"error,omitempty"`
	GeneratedCount int                    `json:"generatedCount"`
	FailedCount    int                    `json:"failedCount"`
	CreatedAt      time.Time              `json:"createdAt"`
	UpdatedAt      time.Time              `json:"updatedAt"`
	StartedAt      *time.Time             `json:"startedAt,omitempty"`
	CompletedAt    *time.Time             `json:"completedAt,omitempty"`
}

type EnqueueE2EGenerationJobInput struct {
	ScenarioID    string
	RepoID        string
	TestCaseIDs   []string
	AuthSessionID string
}

func EnqueueE2EGenerationJob(ctx context.Context, input EnqueueE2EGenerationJobInput) (*E2EGenerationJob, error) {
	if strings.TrimSpace(input.ScenarioID) == "" {
		return nil, fmt.Errorf("scenario id is required")
	}
	if len(input.TestCaseIDs) == 0 {
		return nil, fmt.Errorf("test case ids are required")
	}

	now := time.Now()
	job := &E2EGenerationJob{
		ID:            uuid.NewString(),
		Status:        E2EGenerationJobQueued,
		ScenarioID:    input.ScenarioID,
		RepoID:        input.RepoID,
		TestCaseIDs:   append([]string(nil), input.TestCaseIDs...),
		AuthSessionID: input.AuthSessionID,
		MaxAttempts:   envInt("GENERATION_JOB_MAX_ATTEMPTS", 3),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := saveE2EGenerationJob(ctx, job); err != nil {
		return nil, err
	}
	if err := database.RedisClient.LPush(ctx, e2eGenerationQueueKey, job.ID).Err(); err != nil {
		return nil, fmt.Errorf("failed to enqueue e2e generation job: %w", err)
	}
	return job, nil
}

func WaitE2EGenerationJob(ctx context.Context, jobID string, interval time.Duration) (*E2EGenerationJob, error) {
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		job, err := GetE2EGenerationJob(ctx, jobID)
		if err != nil {
			return nil, err
		}
		if job.Status == E2EGenerationJobCompleted || job.Status == E2EGenerationJobFailed {
			return job, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func GetE2EGenerationJob(ctx context.Context, jobID string) (*E2EGenerationJob, error) {
	val, err := database.RedisClient.Get(ctx, e2eGenerationJobKey(jobID)).Result()
	if err != nil {
		return nil, err
	}
	var job E2EGenerationJob
	if err := json.Unmarshal([]byte(val), &job); err != nil {
		return nil, err
	}
	return &job, nil
}

func StartE2EGenerationWorkers(ctx context.Context) {
	workers := envInt("GENERATION_QUEUE_WORKERS", 2)
	if workers <= 0 {
		log.Printf("[E2EGenerationQueue] workers disabled")
		return
	}
	for i := 0; i < workers; i++ {
		go e2eGenerationWorker(ctx, i+1)
	}
	go e2eGenerationReaper(ctx)
	log.Printf("[E2EGenerationQueue] started workers=%d globalInflight=%d", workers, generationGlobalInflightLimit())
}

func e2eGenerationWorker(ctx context.Context, workerID int) {
	for {
		jobID, err := database.RedisClient.BRPopLPush(ctx, e2eGenerationQueueKey, e2eGenerationProcessingKey, 5*time.Second).Result()
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("[E2EGenerationQueue] worker=%d dequeue failed: %v", workerID, err)
			time.Sleep(time.Second)
			continue
		}
		if err := processE2EGenerationJob(ctx, workerID, jobID); err != nil {
			log.Printf("[E2EGenerationQueue] worker=%d job=%s failed: %v", workerID, jobID, err)
		}
	}
}

func e2eGenerationReaper(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			requeueStaleE2EGenerationJobs(ctx)
		}
	}
}

func requeueStaleE2EGenerationJobs(ctx context.Context) {
	jobIDs, err := database.RedisClient.LRange(ctx, e2eGenerationProcessingKey, 0, -1).Result()
	if err != nil {
		log.Printf("[E2EGenerationQueue] failed to scan processing jobs: %v", err)
		return
	}
	now := time.Now()
	for _, jobID := range jobIDs {
		job, err := GetE2EGenerationJob(ctx, jobID)
		if err != nil || job.Status != E2EGenerationJobProcessing || job.LeaseUntil.After(now) {
			continue
		}
		job.Status = E2EGenerationJobQueued
		job.UpdatedAt = now
		job.LeaseUntil = time.Time{}
		job.Error = "job lease expired and was requeued"
		if err := saveE2EGenerationJob(ctx, job); err != nil {
			log.Printf("[E2EGenerationQueue] failed to save stale job=%s: %v", jobID, err)
			continue
		}
		_ = database.RedisClient.LRem(ctx, e2eGenerationProcessingKey, 0, jobID).Err()
		if err := database.RedisClient.LPush(ctx, e2eGenerationQueueKey, jobID).Err(); err != nil {
			log.Printf("[E2EGenerationQueue] failed to requeue stale job=%s: %v", jobID, err)
		}
	}
}

func processE2EGenerationJob(ctx context.Context, workerID int, jobID string) error {
	job, err := GetE2EGenerationJob(ctx, jobID)
	if err != nil {
		_ = database.RedisClient.LRem(ctx, e2eGenerationProcessingKey, 0, jobID).Err()
		return fmt.Errorf("failed to load job: %w", err)
	}

	now := time.Now()
	job.Status = E2EGenerationJobProcessing
	job.Attempts++
	job.UpdatedAt = now
	job.LeaseUntil = now.Add(generationJobLease())
	job.StartedAt = &now
	job.Error = ""
	if err := saveE2EGenerationJob(ctx, job); err != nil {
		return err
	}

	lease := generationJobLease()
	release, refreshInflight, err := acquireGenerationInflightSlot(ctx, job.ID, lease)
	if err != nil {
		return retryOrFailE2EGenerationJob(ctx, job, fmt.Errorf("failed waiting for global generation capacity: %w", err))
	}
	defer release()
	heartbeatCtx, heartbeatCancel := context.WithCancel(ctx)
	defer heartbeatCancel()
	go heartbeatE2EGenerationJob(heartbeatCtx, job.ID, lease, refreshInflight)

	log.Printf("[E2EGenerationQueue] worker=%d processing job=%s scenario=%s testCases=%d attempt=%d",
		workerID, job.ID, job.ScenarioID, len(job.TestCaseIDs), job.Attempts)

	err = executeE2EGenerationJob(ctx, job)
	if err != nil {
		return retryOrFailE2EGenerationJob(ctx, job, err)
	}

	completedAt := time.Now()
	job.Status = E2EGenerationJobCompleted
	job.UpdatedAt = completedAt
	job.CompletedAt = &completedAt
	job.LeaseUntil = time.Time{}
	if err := saveE2EGenerationJob(ctx, job); err != nil {
		return err
	}
	_ = database.RedisClient.LRem(ctx, e2eGenerationProcessingKey, 0, job.ID).Err()
	return nil
}

func retryOrFailE2EGenerationJob(ctx context.Context, job *E2EGenerationJob, err error) error {
	now := time.Now()
	job.Error = err.Error()
	job.UpdatedAt = now
	job.LeaseUntil = time.Time{}
	if job.Attempts < job.MaxAttempts {
		job.Status = E2EGenerationJobQueued
		if saveErr := saveE2EGenerationJob(ctx, job); saveErr != nil {
			return saveErr
		}
		_ = database.RedisClient.LRem(ctx, e2eGenerationProcessingKey, 0, job.ID).Err()
		backoff := time.Duration(job.Attempts*job.Attempts) * time.Second
		go func(jobID string, delay time.Duration) {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				if pushErr := database.RedisClient.LPush(context.Background(), e2eGenerationQueueKey, jobID).Err(); pushErr != nil {
					log.Printf("[E2EGenerationQueue] failed to requeue job=%s: %v", jobID, pushErr)
				}
			}
		}(job.ID, backoff)
		return err
	}

	completedAt := now
	job.Status = E2EGenerationJobFailed
	job.CompletedAt = &completedAt
	if saveErr := saveE2EGenerationJob(ctx, job); saveErr != nil {
		return saveErr
	}
	_ = database.RedisClient.LRem(ctx, e2eGenerationProcessingKey, 0, job.ID).Err()
	markE2EJobFailed(ctx, job, err.Error())
	return err
}

func executeE2EGenerationJob(ctx context.Context, job *E2EGenerationJob) error {
	events := NewGenerationEmitter(ctx, job.ScenarioID)
	events.SetTotalSteps(len(job.TestCaseIDs))
	_ = events.Start("Generating %d e2e automation test%s...", len(job.TestCaseIDs), pluralizeCount(len(job.TestCaseIDs)))

	token, err := auth.GetSession(ctx, job.AuthSessionID)
	if err != nil {
		_ = events.ErrorWithCode("generation_auth_failed", "Generation failed: auth session expired or is invalid", err.Error())
		return fmt.Errorf("failed to load auth session: %w", err)
	}

	var allAutomations []models.GeneratedAutomation
	var allFailedIDs []string
	for i := 0; i < len(job.TestCaseIDs); i += e2eGenerationBatchSize {
		end := i + e2eGenerationBatchSize
		if end > len(job.TestCaseIDs) {
			end = len(job.TestCaseIDs)
		}
		batchIDs := job.TestCaseIDs[i:end]
		_ = events.Progressf("Generating e2e automations for batch %d to %d (of %d)...", i+1, end, len(job.TestCaseIDs))

		result, err := RunAgentForTestGenerationWithLLM(ctx, AutomationAgentInput{
			ScenarioID:      job.ScenarioID,
			RepoID:          job.RepoID,
			TestCaseIDs:     batchIDs,
			AuthSessionID:   job.AuthSessionID,
			GenerationJobID: job.ID,
		}, token)
		if err != nil {
			log.Printf("[E2EGenerationQueue] batch failed job=%s scenario=%s testCaseIds=%v: %v", job.ID, job.ScenarioID, batchIDs, err)
			allFailedIDs = append(allFailedIDs, batchIDs...)
			continue
		}
		if result != nil {
			allAutomations = append(allAutomations, result.Automations...)
			allFailedIDs = append(allFailedIDs, result.FailedIDs...)
		}
	}

	scenario, err := services.GetScenarioFromRedis(ctx, job.ScenarioID)
	if err != nil {
		_ = events.ErrorFromErr(err)
		return fmt.Errorf("failed to reload scenario after e2e generation: %w", err)
	}
	for _, auto := range allAutomations {
		services.LinkAutomation(scenario, &auto)
	}
	setGeneratedE2ERepo(scenario, job.TestCaseIDs, job.RepoID)
	markMissingE2EFailed(scenario, job.TestCaseIDs, allFailedIDs)

	job.GeneratedCount = len(allAutomations)
	job.FailedCount = len(allFailedIDs)
	success := !(len(allAutomations) == 0 && len(allFailedIDs) == len(job.TestCaseIDs))
	if success {
		scenario.Error = ""
	} else {
		scenario.Error = "failed to generate e2e tests"
	}
	scenario.ComputeStats()
	if err := services.SaveScenarioToRedis(ctx, scenario); err != nil {
		_ = events.ErrorFromErr(err)
		return fmt.Errorf("failed to save e2e generation result: %w", err)
	}

	if success {
		_ = events.Done("Successfully generated %d e2e automation test%s", len(allAutomations), pluralizeCount(len(allAutomations)))
		return nil
	}
	_ = events.Error(scenario.Error)
	return errors.New(scenario.Error)
}

func markE2EJobFailed(ctx context.Context, job *E2EGenerationJob, message string) {
	scenario, err := services.GetScenarioFromRedis(ctx, job.ScenarioID)
	if err != nil {
		return
	}
	markMissingE2EFailed(scenario, job.TestCaseIDs, job.TestCaseIDs)
	scenario.Error = message
	scenario.ComputeStats()
	_ = services.SaveScenarioToRedis(ctx, scenario)
	events := NewGenerationEmitter(ctx, job.ScenarioID)
	_ = events.Error(message)
}

func setGeneratedE2ERepo(scenario *models.TestScenario, targetIDs []string, frontendRepoID string) {
	targets := make(map[string]bool, len(targetIDs))
	for _, id := range targetIDs {
		targets[id] = true
	}
	for si := range scenario.Sections {
		for ti := range scenario.Sections[si].TestCases {
			tc := &scenario.Sections[si].TestCases[ti]
			if !targets[tc.ID] || tc.AutomationTest == nil {
				continue
			}
			tc.AutomationType = models.AutomationCategoryPtr(models.AutomationCategoryE2E)
			tc.AutomationTest.Category = models.AutomationCategoryE2E
			tc.AutomationTest.RepoID = frontendRepoID
		}
	}
}

func markMissingE2EFailed(scenario *models.TestScenario, targetIDs []string, failedIDs []string) {
	targets := make(map[string]bool, len(targetIDs))
	for _, id := range targetIDs {
		targets[id] = true
	}
	failed := make(map[string]bool, len(failedIDs))
	for _, id := range failedIDs {
		failed[id] = true
	}
	for si := range scenario.Sections {
		for ti := range scenario.Sections[si].TestCases {
			tc := &scenario.Sections[si].TestCases[ti]
			if !targets[tc.ID] || tc.AutomationTest == nil {
				continue
			}
			if tc.AutomationTest.Status == models.AutomationStatusRunning || failed[tc.ID] {
				tc.AutomationType = models.AutomationCategoryPtr(models.AutomationCategoryE2E)
				tc.AutomationTest.Category = models.AutomationCategoryE2E
				tc.AutomationTest.Status = models.AutomationStatusFail
				tc.AutomationTest.ErrorMessage = "Failed to generate e2e automation for this test case."
			}
		}
	}
}

func saveE2EGenerationJob(ctx context.Context, job *E2EGenerationJob) error {
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return database.RedisClient.Set(ctx, e2eGenerationJobKey(job.ID), data, e2eGenerationJobTTL).Err()
}

func e2eGenerationJobKey(jobID string) string {
	return e2eGenerationJobKeyPrefix + jobID
}

func generationJobLease() time.Duration {
	return time.Duration(envInt("GENERATION_JOB_LEASE_SECONDS", 900)) * time.Second
}

func generationGlobalInflightLimit() int {
	return envInt("GENERATION_GLOBAL_INFLIGHT", 3)
}

func acquireGenerationInflightSlot(ctx context.Context, owner string, ttl time.Duration) (func(), func(context.Context, time.Duration), error) {
	limit := generationGlobalInflightLimit()
	if limit <= 0 {
		return func() {}, func(context.Context, time.Duration) {}, nil
	}
	for {
		for i := 0; i < limit; i++ {
			key := fmt.Sprintf("%s%d", e2eGenerationInflightPref, i)
			ok, err := database.RedisClient.SetNX(ctx, key, owner, ttl).Result()
			if err != nil {
				return nil, nil, err
			}
			if ok {
				return func() {
						releaseGenerationInflightSlot(context.Background(), key, owner)
					}, func(ctx context.Context, ttl time.Duration) {
						refreshGenerationInflightSlot(ctx, key, owner, ttl)
					}, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func heartbeatE2EGenerationJob(ctx context.Context, jobID string, lease time.Duration, refreshInflight func(context.Context, time.Duration)) {
	interval := lease / 3
	if interval <= 0 || interval > time.Minute {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			job, err := GetE2EGenerationJob(ctx, jobID)
			if err == nil && job.Status == E2EGenerationJobProcessing {
				now := time.Now()
				job.UpdatedAt = now
				job.LeaseUntil = now.Add(lease)
				_ = saveE2EGenerationJob(ctx, job)
			}
			refreshInflight(ctx, lease)
		}
	}
}

func releaseGenerationInflightSlot(ctx context.Context, key, owner string) {
	const script = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0
	`
	_ = database.RedisClient.Eval(ctx, script, []string{key}, owner).Err()
}

func refreshGenerationInflightSlot(ctx context.Context, key, owner string, ttl time.Duration) {
	const script = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("PEXPIRE", KEYS[1], ARGV[2])
end
return 0
`
	_ = database.RedisClient.Eval(ctx, script, []string{key}, owner, ttl.Milliseconds()).Err()
}

func envInt(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return fallback
	}
	return n
}

func pluralizeCount(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
