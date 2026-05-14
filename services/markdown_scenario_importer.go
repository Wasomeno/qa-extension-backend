package services

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"qa-extension-backend/database"
	"qa-extension-backend/internal/models"

	gitlab "gitlab.com/gitlab-org/api/client-go"
)

const defaultTestScenarioDir = "docs/test-scenarios"

// SyncMarkdownTestScenarios imports the direct Markdown children from docs/test-scenarios
// in the project's specs repo and upserts them as stored test scenarios.
func SyncMarkdownTestScenarios(ctx context.Context, glClient *gitlab.Client, project *models.AppProject, actorID int) ([]models.TestScenario, error) {
	if glClient == nil {
		return nil, fmt.Errorf("gitlab client is required")
	}
	if project == nil {
		return nil, fmt.Errorf("project is required")
	}

	specsRepoID := strconv.FormatInt(project.SpecsRepoID, 10)
	branch := "main"
	if repo, _, err := glClient.Projects.GetProject(specsRepoID, nil); err == nil && repo != nil && repo.DefaultBranch != "" {
		branch = repo.DefaultBranch
	}

	listOpts := &gitlab.ListTreeOptions{
		Path:        gitlab.Ptr(defaultTestScenarioDir),
		Ref:         gitlab.Ptr(branch),
		Recursive:   gitlab.Ptr(false),
		ListOptions: gitlab.ListOptions{PerPage: 100},
	}
	var nodes []*gitlab.TreeNode
	for {
		pageNodes, resp, err := glClient.Repositories.ListTree(specsRepoID, listOpts)
		if err != nil {
			// A project without docs/test-scenarios is valid and simply has no scenarios.
			return []models.TestScenario{}, nil
		}
		nodes = append(nodes, pageNodes...)
		if resp == nil || resp.NextPage == 0 {
			break
		}
		listOpts.Page = resp.NextPage
	}

	var mdNodes []*gitlab.TreeNode
	for _, node := range nodes {
		if node.Type == "blob" && strings.EqualFold(filepath.Ext(node.Path), ".md") {
			mdNodes = append(mdNodes, node)
		}
	}
	sort.Slice(mdNodes, func(i, j int) bool { return mdNodes[i].Path < mdNodes[j].Path })

	imported := make([]models.TestScenario, 0, len(mdNodes))
	seenPaths := make(map[string]bool, len(mdNodes))
	for _, node := range mdNodes {
		file, _, err := glClient.RepositoryFiles.GetFile(specsRepoID, node.Path, &gitlab.GetFileOptions{Ref: gitlab.Ptr(branch)})
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", node.Path, err)
		}
		content, err := base64.StdEncoding.DecodeString(file.Content)
		if err != nil {
			return nil, fmt.Errorf("failed to decode %s: %w", node.Path, err)
		}

		scenario, ok := BuildScenarioFromMarkdown(node.Path, string(content), project, actorID)
		if !ok {
			continue
		}
		scenario.ID = deterministicID("scn", project.ID, node.Path)
		scenario.SpecsRepoID = specsRepoID
		scenario.IssueRepoID = strconv.FormatInt(project.IssueRepoID, 10)
		scenario.SourceType = "markdown"
		scenario.SourcePath = node.Path
		scenario.SourceSHA = node.ID
		mergeExistingScenarioState(ctx, &scenario)

		if err := SaveImportedScenario(ctx, &scenario); err != nil {
			return nil, err
		}
		seenPaths[node.Path] = true
		imported = append(imported, scenario)
	}

	if err := deleteRemovedMarkdownScenarios(ctx, project.ID, seenPaths); err != nil {
		return nil, err
	}

	return imported, nil
}

func mergeExistingScenarioState(ctx context.Context, scenario *models.TestScenario) {
	val, err := database.RedisClient.Get(ctx, fmt.Sprintf("scenario:%s", scenario.ID)).Result()
	if err != nil {
		return
	}
	var existing models.TestScenario
	if err := json.Unmarshal([]byte(val), &existing); err != nil {
		return
	}
	scenario.CreatedAt = existing.CreatedAt
	if scenario.CreatorID == 0 {
		scenario.CreatorID = existing.CreatorID
		scenario.CreatedBy = existing.CreatedBy
	}

	existingCases := make(map[string]models.TestCase)
	for _, section := range existing.Sections {
		for _, tc := range section.TestCases {
			existingCases[tc.ID] = tc
		}
	}
	for si := range scenario.Sections {
		for ti := range scenario.Sections[si].TestCases {
			tc := &scenario.Sections[si].TestCases[ti]
			old, ok := existingCases[tc.ID]
			if !ok {
				continue
			}
			tc.AutomationType = old.AutomationType
			tc.AutomationTest = old.AutomationTest
			tc.Note = old.Note
			tc.CreatedAt = old.CreatedAt
		}
	}
	scenario.ComputeStats()
}

// BuildScenarioFromMarkdown converts one Markdown file into one TestScenario.
func BuildScenarioFromMarkdown(path string, content string, project *models.AppProject, creatorID int) (models.TestScenario, bool) {
	now := time.Now()
	title := markdownTitle(content)
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	preconditions := markdownSection(content, "Preconditions")
	testCases := parseMarkdownScenarioCases(path, content, preconditions, now)
	if len(testCases) == 0 {
		return models.TestScenario{}, false
	}

	createdBy := ""
	if creatorID != 0 {
		createdBy = fmt.Sprintf("User %d", creatorID)
	}

	sectionTitle := strings.TrimPrefix(title, "Test Scenarios:")
	sectionTitle = strings.TrimSpace(sectionTitle)
	if sectionTitle == "" {
		sectionTitle = title
	}

	scenario := models.TestScenario{
		Title:       title,
		Description: fmt.Sprintf("Imported from %s", path),
		ProjectID:   project.ID,
		ProjectName: project.Name,
		Status:      models.ScenarioStatusReady,
		CreatorID:   creatorID,
		CreatedAt:   now,
		UpdatedAt:   now,
		CreatedBy:   createdBy,
		Sections: []models.TestSection{{
			ID:          deterministicID("sec", project.ID, path),
			Order:       1,
			Title:       sectionTitle,
			Description: preconditions,
			TestCases:   testCases,
		}},
	}
	scenario.ComputeStats()
	return scenario, true
}

// SaveImportedScenario persists a scenario and maintains the Redis indexes used by scenario APIs.
func SaveImportedScenario(ctx context.Context, scenario *models.TestScenario) error {
	data, err := json.Marshal(scenario)
	if err != nil {
		return err
	}
	if err := database.RedisClient.Set(ctx, fmt.Sprintf("scenario:%s", scenario.ID), data, 0).Err(); err != nil {
		return err
	}
	if err := database.RedisClient.SAdd(ctx, "scenarios", scenario.ID).Err(); err != nil {
		return err
	}
	if scenario.ProjectID != "" {
		if err := database.RedisClient.SAdd(ctx, fmt.Sprintf("scenarios:project:%s", scenario.ProjectID), scenario.ID).Err(); err != nil {
			return err
		}
	}
	if scenario.CreatorID != 0 {
		return database.RedisClient.SAdd(ctx, fmt.Sprintf("scenarios:user:%d", scenario.CreatorID), scenario.ID).Err()
	}
	return database.RedisClient.SAdd(ctx, "scenarios:legacy", scenario.ID).Err()
}

func deleteRemovedMarkdownScenarios(ctx context.Context, projectID string, seenPaths map[string]bool) error {
	ids, err := database.RedisClient.SMembers(ctx, fmt.Sprintf("scenarios:project:%s", projectID)).Result()
	if err != nil {
		return err
	}
	for _, id := range ids {
		val, err := database.RedisClient.Get(ctx, fmt.Sprintf("scenario:%s", id)).Result()
		if err != nil {
			continue
		}
		var scenario models.TestScenario
		if err := json.Unmarshal([]byte(val), &scenario); err != nil {
			continue
		}
		if scenario.SourceType != "markdown" || scenario.SourcePath == "" || seenPaths[scenario.SourcePath] {
			continue
		}
		database.RedisClient.Del(ctx, fmt.Sprintf("scenario:%s", id))
		database.RedisClient.SRem(ctx, "scenarios", id)
		database.RedisClient.SRem(ctx, fmt.Sprintf("scenarios:project:%s", scenario.ProjectID), id)
		if scenario.CreatorID != 0 {
			database.RedisClient.SRem(ctx, fmt.Sprintf("scenarios:user:%d", scenario.CreatorID), id)
		} else {
			database.RedisClient.SRem(ctx, "scenarios:legacy", id)
		}
	}
	return nil
}

func markdownTitle(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}

func markdownSection(content string, name string) string {
	lines := strings.Split(content, "\n")
	wanted := strings.ToLower(name)
	var out []string
	inSection := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			heading := strings.TrimSpace(strings.TrimPrefix(trimmed, "## "))
			if inSection {
				break
			}
			inSection = strings.EqualFold(heading, wanted)
			continue
		}
		if inSection {
			out = append(out, line)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func parseMarkdownScenarioCases(path string, content string, preconditions string, now time.Time) []models.TestCase {
	lines := strings.Split(content, "\n")
	headingRe := regexp.MustCompile(`^###\s+Scenario\s+\d+\s*:\s*(.+)$`)
	var cases []models.TestCase
	var currentTitle string
	var currentRows []string

	flush := func() {
		if currentTitle == "" {
			return
		}
		steps := parseMarkdownStepTable(currentRows)
		if len(steps) == 0 {
			currentTitle = ""
			currentRows = nil
			return
		}
		idx := len(cases) + 1
		nowStr := now.Format(time.RFC3339)
		parsed := models.ParsedTestCase{Name: currentTitle, PreCondition: preconditions, Steps: parsedStepsFromV2(steps)}
		cases = append(cases, models.TestCase{
			ID:           deterministicID("tc", path, currentTitle),
			Order:        idx,
			Code:         fmt.Sprintf("TC-%03d", idx),
			Title:        currentTitle,
			PreCondition: preconditions,
			Steps:        steps,
			Tags:         inferTags(parsed),
			Priority:     inferPriority(parsed),
			Type:         inferTestType(parsed),
			Status:       models.TCStatusReady,
			CreatedAt:    nowStr,
			UpdatedAt:    nowStr,
		})
		currentTitle = ""
		currentRows = nil
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if m := headingRe.FindStringSubmatch(trimmed); len(m) == 2 {
			flush()
			currentTitle = strings.TrimSpace(m[1])
			continue
		}
		if currentTitle != "" {
			currentRows = append(currentRows, line)
		}
	}
	flush()
	return cases
}

func parseMarkdownStepTable(lines []string) []models.TestStepV2 {
	var steps []models.TestStepV2
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "|") || !strings.HasSuffix(trimmed, "|") {
			continue
		}
		cols := splitMarkdownTableRow(trimmed)
		if len(cols) < 3 {
			continue
		}
		first := strings.ToLower(strings.TrimSpace(cols[0]))
		if first == "step" || strings.Contains(first, "---") {
			continue
		}
		order := len(steps) + 1
		if n, err := strconv.Atoi(strings.TrimSpace(cols[0])); err == nil {
			order = n
		}
		action := strings.TrimSpace(cols[1])
		expected := strings.TrimSpace(cols[2])
		if action == "" && expected == "" {
			continue
		}
		steps = append(steps, models.TestStepV2{
			ID:       fmt.Sprintf("st-%d", len(steps)+1),
			Order:    order,
			Action:   action,
			Expected: expected,
		})
	}
	for i := range steps {
		steps[i].Order = i + 1
	}
	return steps
}

func splitMarkdownTableRow(line string) []string {
	line = strings.Trim(line, "|")
	parts := strings.Split(line, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func parsedStepsFromV2(steps []models.TestStepV2) []models.ParsedStep {
	parsed := make([]models.ParsedStep, 0, len(steps))
	for _, step := range steps {
		parsed = append(parsed, models.ParsedStep{Action: step.Action, ExpectedResult: step.Expected})
	}
	return parsed
}

func deterministicID(prefix string, parts ...string) string {
	h := sha1.New()
	_, _ = h.Write([]byte(strings.Join(parts, "|")))
	return fmt.Sprintf("%s-%s", prefix, hex.EncodeToString(h.Sum(nil))[:12])
}
