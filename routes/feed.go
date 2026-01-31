package routes

import (
	"log"
	"sync"

	"qa-extension-backend/client"
	"qa-extension-backend/models"

	gitlab "gitlab.com/gitlab-org/api/client-go"
)

// ConcurrentFeedAggregator fetches the last activity note for multiple issues concurrently.
func ConcurrentFeedAggregator(gitlabClient *gitlab.Client, issues []*gitlab.Issue) []models.ActivityFeedItem {
	var wg sync.WaitGroup
	results := make([]models.ActivityFeedItem, len(issues))

	for i, issue := range issues {
		wg.Add(1)
		go func(idx int, iss *gitlab.Issue) {
			defer wg.Done()

			item := models.ActivityFeedItem{
				IssueID:   int(iss.ID),
				IssueIID:  int(iss.IID),
				ProjectID: int(iss.ProjectID),
				Title:     iss.Title,
				WebURL:    iss.WebURL,
			}
			if iss.UpdatedAt != nil {
				item.CreatedAt = *iss.UpdatedAt
			}

			note, err := client.FetchLastActivityNote(gitlabClient, int(iss.ProjectID), int(iss.IID))
			if err != nil {
				// Fallback: log error but return basic item
				log.Printf("Failed to fetch activity for issue %d: %v", iss.IID, err)
				item.ActionType = "issue_update"
				item.Description = "Activity fetch failed"
			} else if note != nil {
				item.ActionType = "comment"
				if note.System {
					item.ActionType = "system_note"
				}
				item.ActorName = note.Author.Name
				item.ActorAvatar = note.Author.AvatarURL
				item.Description = note.Body
				if note.CreatedAt != nil {
					item.CreatedAt = *note.CreatedAt
				}
			} else {
				// No note found, assume standard update
				item.ActionType = "issue_update"
				item.Description = "No recent activity"
			}
			
			results[idx] = item
		}(i, issue)
	}

	wg.Wait()
	return results
}
