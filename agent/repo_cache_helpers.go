package agent

import (
	"context"
	"errors"
	"fmt"

	"qa-extension-backend/services"

	gitlab "gitlab.com/gitlab-org/api/client-go"
)

func ensureRepoMirrorForAgentTool(ctx context.Context, glClient *gitlab.Client, projectID string) error {
	_, err := services.DefaultRepoCache().EnsureRepo(ctx, glClient, projectID, false)
	if err == nil || errors.Is(err, services.ErrRepoCacheDisabled) {
		return nil
	}
	return fmt.Errorf("failed to prepare repository cache for project %s: %w", projectID, err)
}

func refreshRepoMirrorForAgentTool(ctx context.Context, glClient *gitlab.Client, projectID string) {
	_, _ = services.DefaultRepoCache().EnsureRepo(ctx, glClient, projectID, true)
}
