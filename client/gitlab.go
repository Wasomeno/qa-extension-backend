package client

import (
	"context"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"qa-extension-backend/models"

	gitlab "gitlab.com/gitlab-org/api/client-go"
	"gitlab.com/gitlab-org/api/client-go/gitlaboauth2"
	"golang.org/x/oauth2"
)

type TokenSaver func(context.Context, *oauth2.Token) error

type NotifyTokenSource struct {
	ctx    context.Context
	source oauth2.TokenSource
	saver  TokenSaver
}

func (s *NotifyTokenSource) Token() (*oauth2.Token, error) {
	t, err := s.source.Token()
	if err != nil {
		return nil, err
	}
	if s.saver != nil {
		_ = s.saver(s.ctx, t)
	}
	return t, nil
}

func GetClient(ctx context.Context, token *oauth2.Token, saver TokenSaver) (*gitlab.Client, error) {
	clientID := os.Getenv("GITLAB_APPLICATION_ID")
	clientSecret := os.Getenv("GITLAB_SECRET")
	redirectURL := "http://localhost:3000/auth/callback"
	scopes := []string{"api", "read_user"}
	config := gitlaboauth2.NewOAuth2Config("", clientID, redirectURL, scopes)
	config.ClientSecret = clientSecret

	ts := &NotifyTokenSource{
		ctx:    ctx,
		source: config.TokenSource(ctx, token),
		saver:  saver,
	}

	client, err := gitlab.NewAuthSourceClient(gitlab.OAuthTokenSource{TokenSource: ts})
	if err != nil {
		return nil, err
	}

	return client, nil
}

// FetchRecentIssueNotes retrieves the last N notes for an issue.
func FetchRecentIssueNotes(client *gitlab.Client, projectID int, issueID int, limit int) ([]*gitlab.Note, error) {
	orderBy := "created_at"
	sort := "desc"
	opt := &gitlab.ListIssueNotesOptions{
		OrderBy: &orderBy,
		Sort:    &sort,
		ListOptions: gitlab.ListOptions{
			Page:    1,
			PerPage: int64(limit),
		},
	}

	notes, _, err := client.Notes.ListIssueNotes(projectID, int64(issueID), opt)
	if err != nil {
		return nil, err
	}

	return notes, nil
}

// FetchUnifiedActivities fetches the last activity note for multiple issues concurrently.
func FetchUnifiedActivities(gitlabClient *gitlab.Client, issues []*gitlab.Issue) []models.ActivityFeedItem {
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

			// Parallel fetch of Notes and Label Events
			var notes []*gitlab.Note
			var labelEvents []*gitlab.LabelEvent
			var nErr, lErr error
			var subWg sync.WaitGroup

			subWg.Add(2)
			go func() {
				defer subWg.Done()
				notes, nErr = FetchRecentIssueNotes(gitlabClient, int(iss.ProjectID), int(iss.IID), 20)
			}()
			go func() {
				defer subWg.Done()
				opt := &gitlab.ListLabelEventsOptions{ListOptions: gitlab.ListOptions{Page: 1, PerPage: 10}}
				labelEvents, _, lErr = gitlabClient.ResourceLabelEvents.ListIssueLabelEvents(int(iss.ProjectID), int64(iss.IID), opt)
			}()
			subWg.Wait()

			if nErr != nil || lErr != nil {
				log.Printf("Activity fetch failed for issue %d: notesErr=%v, labelErr=%v", iss.IID, nErr, lErr)
				item.ActionType = "issue_update"
				item.Description = "Activity fetch partially failed"
			}

			bestNote := SelectBestActivity(notes, labelEvents)

			if bestNote != nil {
				item.ActionType = bestNote.ActionType
				item.ActorName = bestNote.ActorName
				item.ActorAvatar = bestNote.ActorAvatar
				item.Description = bestNote.Description
				item.CreatedAt = bestNote.CreatedAt
			} else if nErr == nil {
				item.ActionType = "issue_update"
				item.Description = "No recent activity"
			}
			
			results[idx] = item
		}(i, issue)
	}

	wg.Wait()
	return results
}

type InternalActivity struct {
	ActionType  string
	ActorName   string
	ActorAvatar string
	Description string
	CreatedAt   time.Time
	IsSystem    bool
	Priority    int // Higher is better
}

func SelectBestActivity(notes []*gitlab.Note, labels []*gitlab.LabelEvent) *InternalActivity {
	var activities []InternalActivity

	for _, n := range notes {
		act := InternalActivity{
			ActionType:  "comment",
			ActorName:   n.Author.Name,
			ActorAvatar: n.Author.AvatarURL,
			Description: n.Body,
			CreatedAt:   *n.CreatedAt,
			IsSystem:    n.System,
			Priority:    1,
		}
		if n.System {
			act.ActionType = "system_note"
			act.Priority = 0
			if isImportantSystemNote(n) {
				act.Priority = 2
			}
		} else {
			act.Priority = 3 // User comment
		}
		activities = append(activities, act)
	}

	for _, l := range labels {
		desc := "added label " + l.Label.Name
		if l.Action == "remove" {
			desc = "removed label " + l.Label.Name
		}
		act := InternalActivity{
			ActionType:  "label_event",
			ActorName:   l.User.Name,
			ActorAvatar: l.User.AvatarURL,
			Description: desc,
			CreatedAt:   *l.CreatedAt,
			IsSystem:    true,
			Priority:    2, // Label events are important
		}
		activities = append(activities, act)
	}

	if len(activities) == 0 {
		return nil
	}

	// Sort by priority (desc) then by time (desc)
	sort.Slice(activities, func(i, j int) bool {
		if activities[i].Priority != activities[j].Priority {
			return activities[i].Priority > activities[j].Priority
		}
		return activities[i].CreatedAt.After(activities[j].CreatedAt)
	})

	return &activities[0]
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