package models

import (
	"strconv"
	"time"
)

// AppProject is the public QA workspace project that groups GitLab issues,
// issue boards, specs, test scenarios, recordings, and fix sessions.
type AppProject struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`

	// TestContextMarkdown is a project-level testing knowledge base used as
	// additional factual context when generating API and E2E automation tests.
	TestContextMarkdown string `json:"testContextMarkdown,omitempty"`

	IssueRepoID int64 `json:"issueRepoId"`
	SpecsRepoID int64 `json:"specsRepoId"`

	CreatedByID int       `json:"createdById,omitempty"`
	UpdatedByID int       `json:"updatedById,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// AppProjectResponse is the API response for app projects, with repo names
// instead of repo IDs. Repo names are in the format "group/subgroup/repo-name".
type AppProjectResponse struct {
	ID                  string    `json:"id"`
	Name                string    `json:"name"`
	Description         string    `json:"description"`
	TestContextMarkdown string    `json:"testContextMarkdown,omitempty"`
	IssueRepoName       string    `json:"issueRepoName"`
	SpecsRepoName       string    `json:"specsRepoName"`
	CreatedByID         int       `json:"createdById,omitempty"`
	UpdatedByID         int       `json:"updatedById,omitempty"`
	CreatedAt           time.Time `json:"createdAt"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

// ToResponse converts an AppProject to an AppProjectResponse, using the provided
// repo names. If a repo name is empty, it falls back to the repo ID as a string.
func (p *AppProject) ToResponse(issueRepoName, specsRepoName string) AppProjectResponse {
	if issueRepoName == "" {
		issueRepoName = strconv.FormatInt(p.IssueRepoID, 10)
	}
	if specsRepoName == "" {
		specsRepoName = strconv.FormatInt(p.SpecsRepoID, 10)
	}
	return AppProjectResponse{
		ID:                  p.ID,
		Name:                p.Name,
		Description:         p.Description,
		TestContextMarkdown: p.TestContextMarkdown,
		IssueRepoName:       issueRepoName,
		SpecsRepoName:       specsRepoName,
		CreatedByID:         p.CreatedByID,
		UpdatedByID:         p.UpdatedByID,
		CreatedAt:           p.CreatedAt,
		UpdatedAt:           p.UpdatedAt,
	}
}

// CreateAppProjectRequest is the payload for creating a public QA project.
type CreateAppProjectRequest struct {
	Name                string `json:"name" binding:"required"`
	Description         string `json:"description"`
	TestContextMarkdown string `json:"testContextMarkdown,omitempty"`
	IssueRepoID         int64  `json:"issueRepoId" binding:"required"`
	SpecsRepoID         int64  `json:"specsRepoId" binding:"required"`
}

// UpdateAppProjectRequest is the payload for partial project updates.
type UpdateAppProjectRequest struct {
	Name                *string `json:"name"`
	Description         *string `json:"description"`
	TestContextMarkdown *string `json:"testContextMarkdown"`
	IssueRepoID         *int64  `json:"issueRepoId"`
	SpecsRepoID         *int64  `json:"specsRepoId"`
}

// UpdateProjectTestContextRequest is the payload for replacing a project's
// testing knowledge base markdown.
type UpdateProjectTestContextRequest struct {
	Markdown string `json:"markdown"`
}

type AppProjectActivityAction string

const (
	AppProjectActivityCreated               AppProjectActivityAction = "created"
	AppProjectActivityUpdated               AppProjectActivityAction = "updated"
	AppProjectActivityDeleted               AppProjectActivityAction = "deleted"
	AppProjectActivityScenarioSyncStarted   AppProjectActivityAction = "scenario_sync_started"
	AppProjectActivityScenarioSyncCompleted AppProjectActivityAction = "scenario_sync_completed"
	AppProjectActivityScenarioSyncFailed    AppProjectActivityAction = "scenario_sync_failed"
)

// AppProjectChange stores old/new values for an audited project field change.
type AppProjectChange struct {
	Old any `json:"old"`
	New any `json:"new"`
}

// AppProjectActivity tracks public project changes for auditability.
type AppProjectActivity struct {
	ID        string                      `json:"id"`
	ProjectID string                      `json:"projectId"`
	ActorID   int                         `json:"actorId,omitempty"`
	Action    AppProjectActivityAction    `json:"action"`
	Changes   map[string]AppProjectChange `json:"changes,omitempty"`
	CreatedAt time.Time                   `json:"createdAt"`
}

// ProjectDetails contains essential GitLab project information for frontend display
type ProjectDetails struct {
	ID                int64      `json:"id"`
	Name              string     `json:"name"`
	NameWithNamespace string     `json:"nameWithNamespace"`
	Path              string     `json:"path"`
	PathWithNamespace string     `json:"pathWithNamespace"`
	Description       string     `json:"description"`
	WebURL            string     `json:"webUrl"`
	AvatarURL         string     `json:"avatarUrl,omitempty"`
	DefaultBranch     string     `json:"defaultBranch,omitempty"`
	Visibility        string     `json:"visibility"`
	Namespace         *Namespace `json:"namespace,omitempty"`
}

// Namespace represents the project namespace
type Namespace struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Kind      string `json:"kind"` // "user" or "group"
	AvatarURL string `json:"avatarUrl,omitempty"`
	WebURL    string `json:"webUrl,omitempty"`
}
