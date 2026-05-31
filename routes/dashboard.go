package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"qa-extension-backend/database"
	"qa-extension-backend/internal/models"
	"qa-extension-backend/services"

	"github.com/gin-gonic/gin"
	gitlab "gitlab.com/gitlab-org/api/client-go"
)

// GetProjectDashboard serves GET /projects/:id/dashboard.
// It aggregates real data for the six health indicator tiles and returns
// a single response so the frontend makes only one API call.
func GetProjectDashboard(c *gin.Context) {
	projectID := c.Param("id")

	// 1. Validate project exists
	project, err := services.GetAppProject(c.Request.Context(), projectID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
		return
	}

	// 2. Build GitLab client (optional — if unavailable, fall back to 0 counts)
	glClient, _ := gitLabClientFromContext(c)
	issueRepoID := project.IssueRepoID

	ctx := c.Request.Context()
	resultCh := make(chan struct {
		key string
		val interface{}
	}, 6)
	var wg sync.WaitGroup

	// --- openIssues ---
	wg.Add(1)
	go func() {
		defer wg.Done()
		count := 0
		if glClient != nil && issueRepoID > 0 {
			count = fetchOpenIssueCount(ctx, glClient, issueRepoID)
		}
		resultCh <- struct {
			key string
			val interface{}
		}{"openIssues", count}
	}()

	// --- testScenarios (SCard) ---
	wg.Add(1)
	go func() {
		defer wg.Done()
		count, _ := database.RedisClient.SCard(ctx, fmt.Sprintf("scenarios:project:%s", projectID)).Result()
		resultCh <- struct {
			key string
			val interface{}
		}{"testScenarios", int(count)}
	}()

	// --- recordings (SCard) ---
	wg.Add(1)
	go func() {
		defer wg.Done()
		count, _ := database.RedisClient.SCard(ctx, fmt.Sprintf("recordings:project:%s", projectID)).Result()
		resultCh <- struct {
			key string
			val interface{}
		}{"recordings", int(count)}
	}()

	// --- fixSessions (SCard) ---
	wg.Add(1)
	go func() {
		defer wg.Done()
		count, _ := database.RedisClient.SCard(ctx, fmt.Sprintf("fix_sessions:project:%s", projectID)).Result()
		resultCh <- struct {
			key string
			val interface{}
		}{"fixSessions", int(count)}
	}()

	// --- passRate ---
	wg.Add(1)
	go func() {
		defer wg.Done()
		pr := computePassRate(ctx, projectID)
		resultCh <- struct {
			key string
			val interface{}
		}{"passRate", pr}
	}()

	// --- issuesToday ---
	wg.Add(1)
	go func() {
		defer wg.Done()
		it := models.ProjectIssuesToday{}
		if glClient != nil && issueRepoID > 0 {
			it = fetchIssuesToday(ctx, glClient, issueRepoID)
		}
		resultCh <- struct {
			key string
			val interface{}
		}{"issuesToday", it}
	}()

	// Close results channel when all goroutines finish
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// Build response from results
	resp := models.ProjectDashboardResponse{}
	for r := range resultCh {
		switch r.key {
		case "openIssues":
			resp.OpenIssues = r.val.(int)
		case "testScenarios":
			resp.TestScenarios = r.val.(int)
		case "recordings":
			resp.Recordings = r.val.(int)
		case "fixSessions":
			resp.FixSessions = r.val.(int)
		case "passRate":
			resp.PassRate = r.val.(*models.ProjectPassRate)
		case "issuesToday":
			resp.IssuesToday = r.val.(models.ProjectIssuesToday)
		}
	}

	c.JSON(http.StatusOK, resp)
}

// fetchOpenIssueCount returns the number of open issues for a GitLab project.
// It tries the OpenIssuesCount from the cached project details first.
func fetchOpenIssueCount(ctx context.Context, glClient *gitlab.Client, projectID int64) int {
	project, _, err := glClient.Projects.GetProject(projectID, nil)
	if err == nil && project.OpenIssuesCount > 0 {
		return int(project.OpenIssuesCount)
	}

	// Fallback: list open issues just for the count
	state := "opened"
	opts := &gitlab.ListProjectIssuesOptions{
		State: &state,
		ListOptions: gitlab.ListOptions{
			PerPage: 1,
			Page:    1,
		},
	}
	_, resp, err := glClient.Issues.ListProjectIssues(projectID, opts)
	if err != nil {
		log.Printf("[Dashboard] failed to list open issues for repo %d: %v", projectID, err)
		return 0
	}
	if resp != nil && resp.TotalItems > 0 {
		return int(resp.TotalItems)
	}
	return 0
}

// computePassRate scans all test scenarios in the project and computes the
// pass rate from AutomationTest results within the last 7 days.
func computePassRate(ctx context.Context, projectID string) *models.ProjectPassRate {
	scenarioIDs, err := database.RedisClient.SMembers(ctx, fmt.Sprintf("scenarios:project:%s", projectID)).Result()
	if err != nil || len(scenarioIDs) == 0 {
		return nil
	}

	now := time.Now().UTC()
	endOfToday := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, time.UTC)
	recentCutoff := endOfToday.AddDate(0, 0, -7)   // 7 days ago
	priorCutoff := endOfToday.AddDate(0, 0, -14)     // 14 days ago

	var recentPassed, recentTotal int
	var priorPassed, priorTotal int

	for _, sid := range scenarioIDs {
		val, err := database.RedisClient.Get(ctx, fmt.Sprintf("scenario:%s", sid)).Result()
		if err != nil {
			continue
		}
		var scenario models.TestScenario
		if err := json.Unmarshal([]byte(val), &scenario); err != nil {
			continue
		}

		for _, section := range scenario.Sections {
			for _, tc := range section.TestCases {
				at := tc.AutomationTest
				if at == nil || at.LastRunAt == "" {
					continue
				}
				lastRun, parseErr := time.Parse(time.RFC3339, at.LastRunAt)
				if parseErr != nil {
					continue
				}

				switch {
				case lastRun.After(recentCutoff):
					recentTotal++
					if at.Status == models.AutomationStatusPass {
						recentPassed++
					}
				case lastRun.After(priorCutoff) && lastRun.Before(recentCutoff):
					priorTotal++
					if at.Status == models.AutomationStatusPass {
						priorPassed++
					}
				}
			}
		}
	}

	if recentTotal == 0 {
		return nil
	}

	value := float64(recentPassed) * 100.0 / float64(recentTotal)

	trend := "flat"
	if priorTotal > 0 {
		recentRate := float64(recentPassed) / float64(recentTotal)
		priorRate := float64(priorPassed) / float64(priorTotal)
		if recentRate > priorRate+0.05 {
			trend = "up"
		} else if recentRate < priorRate-0.05 {
			trend = "down"
		}
	}

	return &models.ProjectPassRate{
		Value:      &value,
		Trend:      trend,
		TrendLabel: "Last 7 days",
	}
}

// fetchIssuesToday returns the number of issues opened and closed today
// for the given GitLab project.
func fetchIssuesToday(ctx context.Context, glClient *gitlab.Client, projectID int64) models.ProjectIssuesToday {
	now := time.Now().UTC()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	// Issues opened today
	opened := countOpenedIssuesToday(ctx, glClient, projectID, midnight)

	// Issues closed today
	closed := countClosedIssuesToday(ctx, glClient, projectID, midnight)

	// Determine status
	status := "neutral"
	if opened == 0 {
		status = "success"
	} else if closed > 0 && opened <= closed*2 {
		status = "warning"
	}

	return models.ProjectIssuesToday{
		Opened: opened,
		Closed: closed,
		Status: status,
	}
}

// countOpenedIssuesToday counts issues created after midnight today.
func countOpenedIssuesToday(ctx context.Context, glClient *gitlab.Client, projectID int64, midnight time.Time) int {
	stateAll := "all"
	opts := &gitlab.ListProjectIssuesOptions{
		State:        &stateAll,
		CreatedAfter: &midnight,
		ListOptions: gitlab.ListOptions{
			PerPage: 100,
			Page:    1,
		},
	}
	total := 0
	for {
		issues, resp, err := glClient.Issues.ListProjectIssues(projectID, opts)
		if err != nil {
			log.Printf("[Dashboard] failed to count opened issues for repo %d: %v", projectID, err)
			return total
		}
		total += len(issues)
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = int64(resp.NextPage)
	}
	return total
}

// countClosedIssuesToday counts issues closed today by filtering locally on closed_at.
func countClosedIssuesToday(ctx context.Context, glClient *gitlab.Client, projectID int64, midnight time.Time) int {
	stateClosed := "closed"
	opts := &gitlab.ListProjectIssuesOptions{
		State: &stateClosed,
		ListOptions: gitlab.ListOptions{
			PerPage: 100,
			Page:    1,
		},
	}
	count := 0
	for {
		issues, resp, err := glClient.Issues.ListProjectIssues(projectID, opts)
		if err != nil {
			log.Printf("[Dashboard] failed to count closed issues for repo %d: %v", projectID, err)
			return count
		}
		for _, iss := range issues {
			if iss.ClosedAt != nil && iss.ClosedAt.After(midnight) {
				count++
			}
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = int64(resp.NextPage)
	}
	return count
}
