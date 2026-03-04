package routes

import (
	"sort"
	gitlab "gitlab.com/gitlab-org/api/client-go"
)

func sortIssuesByUpdatedAt(issues []*gitlab.Issue) {
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].UpdatedAt != nil && issues[j].UpdatedAt != nil {
			return issues[i].UpdatedAt.After(*issues[j].UpdatedAt)
		}
		return issues[i].ID > issues[j].ID
	})
}
