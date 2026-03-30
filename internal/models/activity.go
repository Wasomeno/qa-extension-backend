package models

import "time"

type ActivityFeedItem struct {
	IssueID     int       `json:"issue_id"`
	IssueIID    int       `json:"issue_iid"`
	ProjectID   int       `json:"project_id"`
	Title       string    `json:"title"`
	WebURL      string    `json:"web_url"`
	ActionType  string    `json:"action_type"`
	ActorName   string    `json:"actor_name"`
	ActorAvatar string    `json:"actor_avatar"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}
