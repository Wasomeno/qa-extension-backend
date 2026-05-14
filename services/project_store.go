package services

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"qa-extension-backend/database"
	"qa-extension-backend/internal/models"

	"github.com/google/uuid"
)

const (
	appProjectsSetKey       = "app_projects"
	appProjectKeyPrefix     = "app_project"
	appProjectActivityLimit = 200
)

// CreateAppProject stores a public QA project and records its creation.
func CreateAppProject(ctx context.Context, req models.CreateAppProjectRequest, actorID int) (*models.AppProject, error) {
	now := time.Now()
	project := &models.AppProject{
		ID:          uuid.NewString(),
		Name:        req.Name,
		Description: req.Description,
		IssueRepoID: req.IssueRepoID,
		SpecsRepoID: req.SpecsRepoID,
		CreatedByID: actorID,
		UpdatedByID: actorID,
		CreatedAt:   now,
		UpdatedAt:   now,
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
