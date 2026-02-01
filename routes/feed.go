package routes

import (
	"qa-extension-backend/client"
	gitlab "gitlab.com/gitlab-org/api/client-go"
	"qa-extension-backend/models"
)

// ConcurrentFeedAggregator is a wrapper around the client's unified activity fetcher.
func ConcurrentFeedAggregator(gitlabClient *gitlab.Client, issues []*gitlab.Issue) []models.ActivityFeedItem {
	return client.FetchUnifiedActivities(gitlabClient, issues)
}
