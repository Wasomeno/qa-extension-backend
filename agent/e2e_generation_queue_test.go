package agent

import (
	"testing"
	"time"
)

func TestGenerationJobMaxAttemptsDefaultsToOne(t *testing.T) {
	t.Setenv("GENERATION_JOB_MAX_ATTEMPTS", "")

	if got := generationJobMaxAttempts(); got != 1 {
		t.Fatalf("generationJobMaxAttempts() = %d, want 1", got)
	}
}

func TestGenerationJobMaxAttemptsNormalizesZero(t *testing.T) {
	t.Setenv("GENERATION_JOB_MAX_ATTEMPTS", "0")

	if got := generationJobMaxAttempts(); got != 1 {
		t.Fatalf("generationJobMaxAttempts() = %d, want 1", got)
	}
}

func TestShouldRetryE2EGenerationJob(t *testing.T) {
	tests := []struct {
		name string
		job  *E2EGenerationJob
		want bool
	}{
		{name: "nil job", want: false},
		{name: "attempt below max", job: &E2EGenerationJob{Attempts: 1, MaxAttempts: 3}, want: true},
		{name: "attempt equals max", job: &E2EGenerationJob{Attempts: 3, MaxAttempts: 3}, want: false},
		{name: "zero max is one", job: &E2EGenerationJob{Attempts: 1, MaxAttempts: 0}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldRetryE2EGenerationJob(tt.job); got != tt.want {
				t.Fatalf("shouldRetryE2EGenerationJob() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGenerationRepoCacheTimeoutFromEnv(t *testing.T) {
	t.Setenv("GENERATION_REPO_CACHE_TIMEOUT", "9m")

	if got := generationRepoCacheTimeout(); got != 9*time.Minute {
		t.Fatalf("generationRepoCacheTimeout() = %s, want 9m", got)
	}
}
