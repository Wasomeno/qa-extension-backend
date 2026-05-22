package services

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"qa-extension-backend/database"
	"qa-extension-backend/internal/models"

	"github.com/google/uuid"
)

const (
	appProjectsSetKey       = "app_projects"
	appProjectKeyPrefix     = "app_project"
	appProjectActivityLimit = 200

	// MaxProjectTestContextBytes limits the size of project-level test context markdown.
	MaxProjectTestContextBytes = 200 * 1024

	// DefaultPromptTestContextBytes limits how much context is injected into an LLM prompt.
	DefaultPromptTestContextBytes = 12 * 1024
)

// CreateAppProject stores a public QA project and records its creation.
func CreateAppProject(ctx context.Context, req models.CreateAppProjectRequest, actorID int) (*models.AppProject, error) {
	testContextMarkdown := strings.TrimSpace(req.TestContextMarkdown)
	if testContextMarkdown == "" {
		testContextMarkdown = ProjectTestContextTemplate()
	}
	if err := validateProjectTestContext(testContextMarkdown); err != nil {
		return nil, err
	}

	now := time.Now()
	project := &models.AppProject{
		ID:                  uuid.NewString(),
		Name:                req.Name,
		Description:         req.Description,
		TestContextMarkdown: testContextMarkdown,
		IssueRepoID:         req.IssueRepoID,
		SpecsRepoID:         req.SpecsRepoID,
		CreatedByID:         actorID,
		UpdatedByID:         actorID,
		CreatedAt:           now,
		UpdatedAt:           now,
	}

	if err := saveAppProject(ctx, project); err != nil {
		return nil, err
	}
	if err := database.RedisClient.SAdd(ctx, appProjectsSetKey, project.ID).Err(); err != nil {
		return nil, err
	}
	_ = AppendAppProjectActivity(ctx, project.ID, models.AppProjectActivity{
		ID:        uuid.NewString(),
		ProjectID: project.ID,
		ActorID:   actorID,
		Action:    models.AppProjectActivityCreated,
		CreatedAt: now,
	})

	return project, nil
}

// ListAppProjects returns all public QA projects.
func ListAppProjects(ctx context.Context) ([]models.AppProject, error) {
	ids, err := database.RedisClient.SMembers(ctx, appProjectsSetKey).Result()
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []models.AppProject{}, nil
	}

	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = appProjectRedisKey(id)
	}
	vals, err := database.RedisClient.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}

	projects := make([]models.AppProject, 0, len(vals))
	for _, val := range vals {
		if val == nil {
			continue
		}
		str, ok := val.(string)
		if !ok {
			continue
		}
		var project models.AppProject
		if err := json.Unmarshal([]byte(str), &project); err == nil {
			projects = append(projects, project)
		}
	}

	sort.Slice(projects, func(i, j int) bool {
		return projects[i].UpdatedAt.After(projects[j].UpdatedAt)
	})

	return projects, nil
}

// GetAppProject fetches one public QA project by ID.
func GetAppProject(ctx context.Context, id string) (*models.AppProject, error) {
	val, err := database.RedisClient.Get(ctx, appProjectRedisKey(id)).Result()
	if err != nil {
		return nil, err
	}
	var project models.AppProject
	if err := json.Unmarshal([]byte(val), &project); err != nil {
		return nil, err
	}
	return &project, nil
}

// UpdateAppProject applies partial updates and appends an audit event when fields changed.
func UpdateAppProject(ctx context.Context, id string, req models.UpdateAppProjectRequest, actorID int) (*models.AppProject, error) {
	project, err := GetAppProject(ctx, id)
	if err != nil {
		return nil, err
	}

	changes := map[string]models.AppProjectChange{}
	if req.Name != nil && *req.Name != project.Name {
		changes["name"] = models.AppProjectChange{Old: project.Name, New: *req.Name}
		project.Name = *req.Name
	}
	if req.Description != nil && *req.Description != project.Description {
		changes["description"] = models.AppProjectChange{Old: project.Description, New: *req.Description}
		project.Description = *req.Description
	}
	if req.TestContextMarkdown != nil {
		testContextMarkdown := strings.TrimSpace(*req.TestContextMarkdown)
		if testContextMarkdown != project.TestContextMarkdown {
			if err := validateProjectTestContext(testContextMarkdown); err != nil {
				return nil, err
			}
			changes["testContextMarkdown"] = models.AppProjectChange{Old: project.TestContextMarkdown, New: testContextMarkdown}
			project.TestContextMarkdown = testContextMarkdown
		}
	}
	if req.IssueRepoID != nil && *req.IssueRepoID != project.IssueRepoID {
		changes["issueRepoId"] = models.AppProjectChange{Old: project.IssueRepoID, New: *req.IssueRepoID}
		project.IssueRepoID = *req.IssueRepoID
	}
	if req.SpecsRepoID != nil && *req.SpecsRepoID != project.SpecsRepoID {
		changes["specsRepoId"] = models.AppProjectChange{Old: project.SpecsRepoID, New: *req.SpecsRepoID}
		project.SpecsRepoID = *req.SpecsRepoID
	}

	if len(changes) == 0 {
		return project, nil
	}

	project.UpdatedAt = time.Now()
	project.UpdatedByID = actorID
	if err := saveAppProject(ctx, project); err != nil {
		return nil, err
	}
	_ = AppendAppProjectActivity(ctx, project.ID, models.AppProjectActivity{
		ID:        uuid.NewString(),
		ProjectID: project.ID,
		ActorID:   actorID,
		Action:    models.AppProjectActivityUpdated,
		Changes:   changes,
		CreatedAt: project.UpdatedAt,
	})

	return project, nil
}

// UpdateProjectTestContext replaces the project-level testing knowledge base.
func UpdateProjectTestContext(ctx context.Context, id string, markdown string, actorID int) (*models.AppProject, error) {
	return UpdateAppProject(ctx, id, models.UpdateAppProjectRequest{TestContextMarkdown: &markdown}, actorID)
}

// GetProjectTestContext fetches the raw project-level testing knowledge base.
func GetProjectTestContext(ctx context.Context, projectID string) (string, error) {
	project, err := GetAppProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	return project.TestContextMarkdown, nil
}

// ProjectTestContextTemplate returns a suggested markdown structure for users.
func ProjectTestContextTemplate() string {
	return strings.TrimSpace(`# Project Test Context

## Test Users
- Admin user:
  - email:
  - password:
  - role: admin
- Regular user:
  - email:
  - password:
  - role: user

## Authentication
- Login endpoint:
- Token/header format:

## Common Fixtures
- Existing entities that tests can rely on:
- How to create required data:

## Business Rules
- Important permissions, validations, and workflow rules:

## API Notes
- Base URL:
- Relevant endpoints and request/response details:

## E2E Selectors and UI Notes
- Stable selectors, labels, routes, and UI behavior:

## Known Edge Cases
- Cases the automation generator should explicitly cover:
`)
}

func validateProjectTestContext(markdown string) error {
	if len(markdown) > MaxProjectTestContextBytes {
		return fmt.Errorf("project test context is too large: maximum is %d bytes", MaxProjectTestContextBytes)
	}
	return nil
}

// RelevantProjectTestContext returns a compact subset of the markdown that is
// likely relevant to the provided test scenario/test case query. It keeps the
// markdown human-authored while preventing the entire knowledge base from being
// injected into every LLM prompt.
func RelevantProjectTestContext(markdown string, query string, maxBytes int) string {
	markdown = strings.TrimSpace(markdown)
	if markdown == "" {
		return ""
	}
	if maxBytes <= 0 {
		maxBytes = DefaultPromptTestContextBytes
	}

	sections := splitMarkdownSections(markdown)
	if len(sections) == 0 {
		return truncateString(markdown, maxBytes)
	}

	queryTerms := keywordSet(query)
	type scoredSection struct {
		text  string
		score int
		idx   int
	}

	scored := make([]scoredSection, 0, len(sections))
	for idx, section := range sections {
		score := 0
		sectionLower := strings.ToLower(section)
		for term := range queryTerms {
			if strings.Contains(sectionLower, term) {
				score++
			}
		}
		if idx == 0 && !strings.HasPrefix(strings.TrimSpace(section), "#") {
			score += 2 // preserve document intro/global notes
		}
		if isAlwaysUsefulTestContextSection(section) {
			score += 2
		}
		scored = append(scored, scoredSection{text: strings.TrimSpace(section), score: score, idx: idx})
	}

	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			return scored[i].idx < scored[j].idx
		}
		return scored[i].score > scored[j].score
	})

	selected := make([]scoredSection, 0, len(scored))
	used := 0
	for _, section := range scored {
		if section.text == "" {
			continue
		}
		if section.score <= 0 && len(selected) > 0 {
			continue
		}
		sectionLen := len(section.text)
		if used+sectionLen+2 > maxBytes {
			remaining := maxBytes - used - 2
			if remaining > 200 {
				section.text = truncateString(section.text, remaining)
				selected = append(selected, section)
			}
			break
		}
		selected = append(selected, section)
		used += sectionLen + 2
	}

	if len(selected) == 0 {
		return truncateString(markdown, maxBytes)
	}

	sort.SliceStable(selected, func(i, j int) bool { return selected[i].idx < selected[j].idx })
	parts := make([]string, 0, len(selected))
	for _, section := range selected {
		parts = append(parts, section.text)
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func splitMarkdownSections(markdown string) []string {
	lines := strings.Split(markdown, "\n")
	sections := make([]string, 0)
	current := make([]string, 0)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") && len(current) > 0 {
			sections = append(sections, strings.TrimSpace(strings.Join(current, "\n")))
			current = []string{line}
			continue
		}
		current = append(current, line)
	}
	if len(current) > 0 {
		sections = append(sections, strings.TrimSpace(strings.Join(current, "\n")))
	}
	return sections
}

func keywordSet(value string) map[string]struct{} {
	terms := make(map[string]struct{})
	for _, raw := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '_' && r != '-'
	}) {
		term := strings.TrimSpace(raw)
		if len(term) < 3 || isContextStopWord(term) {
			continue
		}
		terms[term] = struct{}{}
	}
	return terms
}

func isAlwaysUsefulTestContextSection(section string) bool {
	firstLine := strings.ToLower(strings.TrimSpace(strings.Split(section, "\n")[0]))
	useful := []string{"user", "auth", "credential", "role", "fixture", "business rule", "api", "selector", "environment", "base url"}
	for _, term := range useful {
		if strings.Contains(firstLine, term) {
			return true
		}
	}
	return false
}

func isContextStopWord(term string) bool {
	switch term {
	case "the", "and", "for", "with", "from", "that", "this", "then", "when", "user", "test", "case", "step", "expected", "input", "data", "should":
		return true
	default:
		return false
	}
}

// ExtractBaseURLFromTestContext parses the project test context markdown and returns
// the base URL if one is defined. It supports two formats:
//
//   ## Base URL
//   https://staging.example.com
//
// or an inline line:
//
//   base url: https://staging.example.com
func ExtractBaseURLFromTestContext(markdown string) string {
	if markdown == "" {
		return ""
	}
	// First, look for a ## Base URL (or ## Base URL / ## Base url) section
	lines := strings.Split(markdown, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") && strings.Contains(strings.ToLower(trimmed), "base url") {
			// Collect the content lines until next heading or blank-block separator
			for j := i + 1; j < len(lines); j++ {
				next := strings.TrimSpace(lines[j])
				if next == "" {
					continue
				}
				if strings.HasPrefix(next, "## ") {
					break
				}
				// Skip list markers, bold markers, etc.
				urlCandidate := strings.TrimRight(strings.TrimLeft(next, "-* \t"), " \t")
				if urlCandidate != "" && strings.HasPrefix(urlCandidate, "http") {
					return urlCandidate
				}
			}
		}
	}
	// Fallback: scan for "base url:" inline
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if idx := strings.Index(strings.ToLower(trimmed), "base url"); idx >= 0 {
			rest := strings.TrimSpace(trimmed[idx+8:])
			rest = strings.TrimLeft(rest, ": \t")
			if strings.HasPrefix(rest, "http") {
				return rest
			}
		}
	}
	return ""
}

func truncateString(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	if maxBytes <= len("\n\n[Project test context truncated]") {
		return value[:maxBytes]
	}
	return strings.TrimSpace(value[:maxBytes-len("\n\n[Project test context truncated]")]) + "\n\n[Project test context truncated]"
}

// DeleteAppProject deletes a project and all Redis-backed child resources grouped under it.
func DeleteAppProject(ctx context.Context, id string, actorID int) error {
	if _, err := GetAppProject(ctx, id); err != nil {
		return err
	}

	if err := deleteProjectScenarios(ctx, id); err != nil {
		return err
	}
	if err := deleteProjectRecordings(ctx, id); err != nil {
		return err
	}
	if err := deleteProjectFixSessions(ctx, id); err != nil {
		return err
	}

	_ = AppendAppProjectActivity(ctx, id, models.AppProjectActivity{
		ID:        uuid.NewString(),
		ProjectID: id,
		ActorID:   actorID,
		Action:    models.AppProjectActivityDeleted,
		CreatedAt: time.Now(),
	})

	if err := database.RedisClient.SRem(ctx, appProjectsSetKey, id).Err(); err != nil {
		return err
	}
	return database.RedisClient.Del(ctx, appProjectRedisKey(id), appProjectActivityRedisKey(id)).Err()
}

// AppendAppProjectActivity records a project audit event.
func AppendAppProjectActivity(ctx context.Context, projectID string, activity models.AppProjectActivity) error {
	if activity.ID == "" {
		activity.ID = uuid.NewString()
	}
	if activity.ProjectID == "" {
		activity.ProjectID = projectID
	}
	if activity.CreatedAt.IsZero() {
		activity.CreatedAt = time.Now()
	}
	data, err := json.Marshal(activity)
	if err != nil {
		return err
	}
	key := appProjectActivityRedisKey(projectID)
	pipe := database.RedisClient.TxPipeline()
	pipe.LPush(ctx, key, data)
	pipe.LTrim(ctx, key, 0, appProjectActivityLimit-1)
	_, err = pipe.Exec(ctx)
	return err
}

// ListAppProjectActivity returns audit events for a project.
func ListAppProjectActivity(ctx context.Context, projectID string) ([]models.AppProjectActivity, error) {
	vals, err := database.RedisClient.LRange(ctx, appProjectActivityRedisKey(projectID), 0, appProjectActivityLimit-1).Result()
	if err != nil {
		return nil, err
	}
	activities := make([]models.AppProjectActivity, 0, len(vals))
	for _, val := range vals {
		var activity models.AppProjectActivity
		if err := json.Unmarshal([]byte(val), &activity); err == nil {
			activities = append(activities, activity)
		}
	}
	return activities, nil
}

func saveAppProject(ctx context.Context, project *models.AppProject) error {
	data, err := json.Marshal(project)
	if err != nil {
		return err
	}
	return database.RedisClient.Set(ctx, appProjectRedisKey(project.ID), data, 0).Err()
}

func appProjectRedisKey(id string) string {
	return fmt.Sprintf("%s:%s", appProjectKeyPrefix, id)
}

func appProjectActivityRedisKey(id string) string {
	return fmt.Sprintf("%s:%s:activity", appProjectKeyPrefix, id)
}

func deleteProjectScenarios(ctx context.Context, projectID string) error {
	setKey := fmt.Sprintf("scenarios:project:%s", projectID)
	ids, err := database.RedisClient.SMembers(ctx, setKey).Result()
	if err != nil {
		return err
	}
	for _, id := range ids {
		val, err := database.RedisClient.Get(ctx, fmt.Sprintf("scenario:%s", id)).Result()
		if err == nil {
			var scenario models.TestScenario
			if json.Unmarshal([]byte(val), &scenario) == nil {
				if scenario.CreatorID != 0 {
					database.RedisClient.SRem(ctx, fmt.Sprintf("scenarios:user:%d", scenario.CreatorID), id)
				} else {
					database.RedisClient.SRem(ctx, "scenarios:legacy", id)
				}
			}
		}
		database.RedisClient.Del(ctx, fmt.Sprintf("scenario:%s", id))
		database.RedisClient.SRem(ctx, "scenarios", id)
	}
	return database.RedisClient.Del(ctx, setKey).Err()
}

func deleteProjectRecordings(ctx context.Context, projectID string) error {
	setKey := fmt.Sprintf("recordings:project:%s", projectID)
	ids, err := database.RedisClient.SMembers(ctx, setKey).Result()
	if err != nil {
		return err
	}
	for _, id := range ids {
		val, err := database.RedisClient.Get(ctx, fmt.Sprintf("recording:%s", id)).Result()
		if err == nil {
			var recording models.ManualRecording
			if json.Unmarshal([]byte(val), &recording) == nil {
				if recording.CreatorID != 0 {
					database.RedisClient.SRem(ctx, fmt.Sprintf("recordings:user:%d", recording.CreatorID), id)
				} else {
					database.RedisClient.SRem(ctx, "recordings:legacy", id)
				}
				if recording.IssueID != "" {
					database.RedisClient.SRem(ctx, fmt.Sprintf("recordings:issue:%s", recording.IssueID), id)
				}
			}
		}
		database.RedisClient.Del(ctx, fmt.Sprintf("recording:%s", id))
		database.RedisClient.SRem(ctx, "recordings", id)
	}
	return database.RedisClient.Del(ctx, setKey).Err()
}

func deleteProjectFixSessions(ctx context.Context, projectID string) error {
	setKey := fmt.Sprintf("fix_sessions:project:%s", projectID)
	ids, err := database.RedisClient.SMembers(ctx, setKey).Result()
	if err != nil {
		return err
	}
	for _, id := range ids {
		database.RedisClient.Del(ctx, fmt.Sprintf("fix_session:%s", id))
	}
	return database.RedisClient.Del(ctx, setKey).Err()
}
