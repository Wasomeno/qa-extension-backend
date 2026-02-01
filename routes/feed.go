package routes

import (
	"log"
	"strings"
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

			// Fetch batch of recent notes (limit 20)
			notes, err := client.FetchRecentIssueNotes(gitlabClient, int(iss.ProjectID), int(iss.IID), 20)
			
			var bestNote *gitlab.Note
			if err != nil {
				// Fallback: log error but return basic item
				log.Printf("Failed to fetch activity for issue %d: %v", iss.IID, err)
				item.ActionType = "issue_update"
				item.Description = "Activity fetch failed"
			} else {
				bestNote = SelectBestNote(notes)
			}

			if bestNote != nil {
				item.ActionType = "comment"
				if bestNote.System {
					item.ActionType = "system_note"
				}
				item.ActorName = bestNote.Author.Name
				item.ActorAvatar = bestNote.Author.AvatarURL
				item.Description = bestNote.Body
				if bestNote.CreatedAt != nil {
					item.CreatedAt = *bestNote.CreatedAt
				}
			} else if err == nil {
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

// SelectBestNote applies heuristics to pick the most relevant note from a batch.
// Priority: User Comment > Important System Event > Newest System Event (Fallback)
func SelectBestNote(notes []*gitlab.Note) *gitlab.Note {
	if len(notes) == 0 {
		return nil
	}

	for _, note := range notes {
		if !note.System {
			return note // User comment found!
		}
		if isImportantSystemNote(note) {
			return note // Important system event found!
		}
	}

	// Fallback: return the newest note (index 0) if it exists
	return notes[0]
}

func isImportantSystemNote(note *gitlab.Note) bool {
	body := strings.ToLower(note.Body)
	// Check for closure/reopening
	if strings.Contains(body, "closed") || strings.Contains(body, "reopened") {
		return true
	}
	// Check for label changes
	// GitLab format: "added ~LabelName label" or "changed label from X to Y"
	if strings.Contains(body, "label") {
		return true
	}
	// Check for mentions
	if strings.Contains(body, "mentioned in") {
		return true
	}
	// Check for title changes
	if strings.Contains(body, "changed title") {
		return true
	}
	return false
}