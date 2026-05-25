package services

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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
	log.Printf("[ProjectCreation] markdown sync start projectID=%s specsRepoID=%s branch=%s path=%s", project.ID, specsRepoID, branch, defaultTestScenarioDir)

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
			if resp != nil && resp.StatusCode == http.StatusNotFound {
				// A project without docs/test-scenarios is valid and simply has no scenarios.
				log.Printf("[ProjectCreation] markdown sync no scenario directory projectID=%s specsRepoID=%s path=%s", project.ID, specsRepoID, defaultTestScenarioDir)
				return []models.TestScenario{}, nil
			}
			log.Printf("[ProjectCreation] markdown sync list failed projectID=%s specsRepoID=%s error=%v", project.ID, specsRepoID, err)
			return nil, fmt.Errorf("failed to list %s in specs repo %s: %w", defaultTestScenarioDir, specsRepoID, err)
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
	log.Printf("[ProjectCreation] markdown sync discovered projectID=%s totalNodes=%d markdownFiles=%d", project.ID, len(nodes), len(mdNodes))

	imported := make([]models.TestScenario, 0, len(mdNodes))
	seenPaths := make(map[string]bool, len(mdNodes))
	for _, node := range mdNodes {
		log.Printf("[ProjectCreation] importing markdown scenario projectID=%s path=%s sha=%s", project.ID, node.Path, node.ID)
		file, _, err := glClient.RepositoryFiles.GetFile(specsRepoID, node.Path, &gitlab.GetFileOptions{Ref: gitlab.Ptr(branch)})
		if err != nil {
			log.Printf("[ProjectCreation] failed to read markdown scenario projectID=%s path=%s error=%v", project.ID, node.Path, err)
			return nil, fmt.Errorf("failed to read %s: %w", node.Path, err)
		}
		content, err := base64.StdEncoding.DecodeString(file.Content)
		if err != nil {
			log.Printf("[ProjectCreation] failed to decode markdown scenario projectID=%s path=%s error=%v", project.ID, node.Path, err)
			return nil, fmt.Errorf("failed to decode %s: %w", node.Path, err)
		}

		scenario, ok := BuildScenarioFromMarkdown(node.Path, string(content), project, actorID)
		if !ok {
			log.Printf("[ProjectCreation] skipped markdown scenario projectID=%s path=%s reason=parse_failed", project.ID, node.Path)
			continue
		}
		scenario.ID = deterministicID("scn", project.ID, node.Path)
		scenario.SpecsRepoID = specsRepoID
		scenario.IssueRepoID = strconv.FormatInt(project.IssueRepoID, 10)
		scenario.SourceType = "markdown"
		scenario.SourcePath = node.Path
		scenario.SourceSHA = node.ID
		mergeExistingScenarioState(ctx, &scenario)

		// Generate a concise LLM description for new scenarios
		if scenario.Description == "" {
			descStart := time.Now()
			log.Printf("[ProjectCreation] LLM description start projectID=%s scenarioID=%s path=%s title=%q contentBytes=%d", project.ID, scenario.ID, node.Path, scenario.Title, len(content))
			if desc, err := GenerateScenarioDescription(ctx, scenario.Title, string(content)); err == nil {
				scenario.Description = desc
				log.Printf("[ProjectCreation] LLM description success projectID=%s scenarioID=%s path=%s duration=%s descriptionLen=%d", project.ID, scenario.ID, node.Path, time.Since(descStart), len(desc))
			} else {
				log.Printf("[ProjectCreation] LLM description failed projectID=%s scenarioID=%s path=%s duration=%s error=%v", project.ID, scenario.ID, node.Path, time.Since(descStart), err)
			}
		} else {
			log.Printf("[ProjectCreation] LLM description skipped projectID=%s scenarioID=%s path=%s reason=description_present", project.ID, scenario.ID, node.Path)
		}

		if err := SaveImportedScenario(ctx, &scenario); err != nil {
			log.Printf("[ProjectCreation] failed to save imported scenario projectID=%s scenarioID=%s path=%s error=%v", project.ID, scenario.ID, node.Path, err)
			return nil, err
		}
		log.Printf("[ProjectCreation] imported scenario saved projectID=%s scenarioID=%s path=%s title=%q", project.ID, scenario.ID, node.Path, scenario.Title)
		seenPaths[node.Path] = true
		imported = append(imported, scenario)
	}

	if err := deleteRemovedMarkdownScenarios(ctx, project.ID, seenPaths); err != nil {
		log.Printf("[ProjectCreation] markdown sync cleanup failed projectID=%s error=%v", project.ID, err)
		return nil, err
	}

	log.Printf("[ProjectCreation] markdown sync complete projectID=%s imported=%d", project.ID, len(imported))
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
// It handles both old-format (flat list of scenarios) and new-format
// (suite-grouped test cases with metadata tables) markdown documents.
func BuildScenarioFromMarkdown(path string, content string, project *models.AppProject, creatorID int) (models.TestScenario, bool) {
	now := time.Now()
	title := cleanScenarioTitle(markdownTitle(content))
	if title == "" {
		title = cleanScenarioTitle(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	}
	preconditions := markdownSection(content, "Preconditions")

	// Parse into sections (handles both old format and new format with suites)
	sections := parseMarkdownIntoSections(path, content, preconditions, now)
	if len(sections) == 0 {
		return models.TestScenario{}, false
	}

	createdBy := ""
	if creatorID != 0 {
		createdBy = fmt.Sprintf("User %d", creatorID)
	}

	// For old-format documents (single section with inferred title),
	// derive a fallback section title from the H1 heading.
	for i := range sections {
		if sections[i].Title == "" || sections[i].Title == fmt.Sprintf("Section %d", i+1) {
			sectionTitle := strings.TrimSpace(title)
			if sectionTitle == "" {
				sectionTitle = title
			}
			sections[i].Title = sectionTitle
		}
	}

	scenario := models.TestScenario{
		Title:         title,
		SourceDisplay: fmt.Sprintf("Imported from %s", path),
		ProjectID:     project.ID,
		ProjectName:   project.Name,
		CreatorID:     creatorID,
		CreatedAt:     now,
		UpdatedAt:     now,
		CreatedBy:     createdBy,
		Sections:      sections,
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

func cleanScenarioTitle(title string) string {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return ""
	}

	re := regexp.MustCompile(`(?i)^test scenarios?\s*[:\-–—]\s*`)
	cleaned := strings.TrimSpace(re.ReplaceAllString(trimmed, ""))
	if cleaned == "" || cleaned == trimmed {
		return trimmed
	}
	return capitalizeFirstRune(cleaned)
}

func capitalizeFirstRune(value string) string {
	r, size := utf8.DecodeRuneInString(value)
	if r == utf8.RuneError && size == 0 {
		return value
	}
	return string(unicode.ToUpper(r)) + value[size:]
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

// suiteHeadingRe matches H2 suite headers like:
//
//	## 📁 Suite 1: 4.1 Daftar Komponen Global
//	## Suite 2: Something
var suiteHeadingRe = regexp.MustCompile(`^##\s+(?:[^\w\s]\s*)?(Suite\s+\d+\s*:\s*.+)$`)

// parseMarkdownScenarioCases parses markdown content and returns flat test cases.
// It handles both the old format (### Scenario N: Title + 3-column step table)
// and the new format (### CODE - Type: Title + metadata table + bold sections).
//
// Deprecated: prefer parseMarkdownIntoSections which also handles suite grouping.
func parseMarkdownScenarioCases(path string, content string, preconditions string, now time.Time) []models.TestCase {
	sections := parseMarkdownIntoSections(path, content, preconditions, now)
	if len(sections) == 0 {
		return nil
	}
	var cases []models.TestCase
	for _, sec := range sections {
		cases = append(cases, sec.TestCases...)
	}
	return cases
}

// parseMarkdownIntoSections parses markdown content and returns test sections,
// each containing test cases grouped by H2 suite headings (when present).
//
// Supports two formats:
//
// Old format (flat, no suites):
//
//	# Title
//	## Preconditions
//	## Scenarios
//	### Scenario 1: Title
//	| Step | Action | Expected Result |
//
// New format (suite-grouped):
//
//	# Title
//	## 📁 Suite 1: Section Name
//	### 📄 CODE - Positive/Negative: Title
//	| Field | Detail |
//	**Pre-Condition**
//	**Test Step**
//	**Expected Result**
func parseMarkdownIntoSections(path string, content string, preconditions string, now time.Time) []models.TestSection {
	lines := strings.Split(content, "\n")
	oldHeadingRe := regexp.MustCompile(`^###\s+Scenario\s+\d+\s*:\s*(.+)$`)
	// Accept hyphen, en dash, or em dash between code and Positive/Negative
	newHeadingRe := regexp.MustCompile(`^###\s+(?:[^\w\s]\s*)?([A-Z]+-\w+\d+)\s*[—–-]\s*(Positive|Negative)\s*:\s*(.+)$`)

	type sectionAccum struct {
		title     string
		testCases []models.TestCase
	}
	var sections []sectionAccum
	var currentSection *sectionAccum
	var currentTitle string
	var currentTcCode string
	var currentTcType string
	var currentRows []string
	var currentFormat string // "old" or "new"

	// flushPending finalizes the current in-progress test case and appends it
	// to the current section (creating one if needed).
	flushPending := func() {
		if currentTitle == "" {
			return
		}
		if currentSection == nil {
			sections = append(sections, sectionAccum{title: ""})
			currentSection = &sections[len(sections)-1]
		}
		if currentFormat == "new" {
			tc, ok := parseNewFormatTestCase(path, currentTcCode, currentTcType, currentTitle, currentRows, preconditions, now)
			if !ok {
				currentTitle = ""
				currentTcCode = ""
				currentTcType = ""
				currentRows = nil
				currentFormat = ""
				return
			}
			tc.Order = len(currentSection.testCases) + 1
			currentSection.testCases = append(currentSection.testCases, tc)
		} else {
			steps := parseMarkdownStepTable(currentRows)
			if len(steps) == 0 {
				currentTitle = ""
				currentRows = nil
				return
			}
			idx := len(currentSection.testCases) + 1
			nowStr := now.Format(time.RFC3339)
			parsed := models.ParsedTestCase{Name: currentTitle, PreCondition: preconditions, Steps: parsedStepsFromV2(steps)}
			currentSection.testCases = append(currentSection.testCases, models.TestCase{
				ID:           deterministicID("tc", path, currentTitle),
				Order:        idx,
				Code:         fmt.Sprintf("TC-%03d", idx),
				Title:        currentTitle,
				PreCondition: preconditions,
				Steps:        steps,
				Tags:         inferTags(parsed),
				Priority:     inferPriority(parsed),
				Type:         inferTestType(parsed),
				CreatedAt:    nowStr,
				UpdatedAt:    nowStr,
			})
		}
		currentTitle = ""
		currentTcCode = ""
		currentTcType = ""
		currentRows = nil
		currentFormat = ""
	}

	// startSection creates or switches to a section with the given title.
	// If title is empty, it will be inferred later from H1.
	startSection := func(title string) {
		if currentSection != nil && title != "" {
			if currentSection.title == title {
				return
			}
		}
		flushPending()
		sections = append(sections, sectionAccum{title: title})
		currentSection = &sections[len(sections)-1]
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Check for H2 suite heading
		if m := suiteHeadingRe.FindStringSubmatch(trimmed); len(m) == 2 {
			flushPending()
			startSection(strings.TrimSpace(m[1]))
			continue
		}

		// Check for old-format scenario heading
		if m := oldHeadingRe.FindStringSubmatch(trimmed); len(m) == 2 {
			flushPending()
			currentTitle = strings.TrimSpace(m[1])
			currentFormat = "old"
			continue
		}

		// Check for new-format test case heading
		if m := newHeadingRe.FindStringSubmatch(trimmed); len(m) == 4 {
			flushPending()
			currentTcCode = strings.TrimSpace(m[1])
			currentTcType = strings.TrimSpace(m[2])
			currentTitle = strings.TrimSpace(m[3])
			currentFormat = "new"
			continue
		}

		// Accumulate content lines for the current test case
		if currentTitle != "" {
			currentRows = append(currentRows, line)
		}
	}
	flushPending()

	// Convert accumulated sections into []models.TestSection
	result := make([]models.TestSection, 0, len(sections))
	for i, sa := range sections {
		if len(sa.testCases) == 0 {
			continue
		}
		sectionTitle := sa.title
		if sectionTitle == "" {
			sectionTitle = fmt.Sprintf("Section %d", i+1)
		}
		result = append(result, models.TestSection{
			ID:        deterministicID("sec", path, fmt.Sprintf("section-%d", i)),
			Order:     i + 1,
			Title:     sectionTitle,
			TestCases: sa.testCases,
		})
	}
	return result
}

// testCaseMetadata holds fields extracted from the 2-column metadata table
// in the new-format markdown:
//
//	| Field               | Detail |
//	|---------------------|--------|
//	| **User Story**      | ...    |
//	| **Category**        | ...    |
//	| **Status**          | ...    |
//	| **Additional Note** | ...    |
type testCaseMetadata struct {
	UserStory      string
	Category       string
	Status         string
	AdditionalNote string
}

// parseNewFormatMetadataTable extracts metadata from a 2-column markdown table.
// It looks for rows like | **Field Name** | Value | and maps known keys.
func parseNewFormatMetadataTable(content string) testCaseMetadata {
	var meta testCaseMetadata
	tableRe := regexp.MustCompile(`^\|\s*\*\*(.+?)\*\*\s*\|\s*(.+?)\s*\|$`)
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		m := tableRe.FindStringSubmatch(trimmed)
		if len(m) != 3 {
			continue
		}
		fieldName := strings.TrimSpace(m[1])
		value := strings.TrimSpace(m[2])
		switch strings.ToLower(fieldName) {
		case "user story":
			meta.UserStory = value
		case "category":
			meta.Category = value
		case "status":
			meta.Status = value
		case "additional note":
			meta.AdditionalNote = value
		}
	}
	return meta
}

// parseNewFormatTestCase parses a test case in the alternative format:
//
//	### 📄 CODE - Positive/Negative: Title
//	| Field | Detail |  (2-column metadata table)
//	**Pre-Condition**
//	1. step
//	**Test Step**
//	1. action
//	**Expected Result**
//	> expected text
func parseNewFormatTestCase(path string, tcCode string, tcType string, title string, lines []string, filePreconditions string, now time.Time) (models.TestCase, bool) {
	contentStr := strings.Join(lines, "\n")

	// Parse metadata table (User Story, Category, Status, Additional Note)
	meta := parseNewFormatMetadataTable(contentStr)

	// Parse Pre-Condition section
	preCondition := parseNewFormatBoldSection(contentStr, "Pre-Condition")
	if preCondition == "" {
		preCondition = filePreconditions
	}
	// Merge file-level preconditions with per-test-case preconditions
	if filePreconditions != "" && preCondition != filePreconditions {
		preCondition = filePreconditions + "\n" + preCondition
	}

	// Parse Test Steps
	stepLines := parseNewFormatBoldSectionLines(contentStr, "Test Step")
	var steps []models.TestStepV2
	for _, line := range stepLines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Remove numbering prefix like "1. ", "2. "
		action := regexp.MustCompile(`^\d+\.\s*`).ReplaceAllString(trimmed, "")
		if action == "" {
			continue
		}
		steps = append(steps, models.TestStepV2{
			ID:       fmt.Sprintf("st-%d", len(steps)+1),
			Order:    len(steps) + 1,
			Action:   action,
			Expected: "",
		})
	}

	// Parse Expected Result section
	expectedLines := parseNewFormatBoldSectionLines(contentStr, "Expected Result")

	// Build a flat list of expected results (strip > and numbering)
	var flatExpecteds []string
	for _, line := range expectedLines {
		trimmed := strings.TrimSpace(line)
		trimmed = strings.TrimPrefix(trimmed, ">")
		trimmed = strings.TrimSpace(trimmed)
		// Strip top-level numbering like "1. ", "2. " for cleaner output
		numbered := regexp.MustCompile(`^\d+\.\s*`).ReplaceAllString(trimmed, "")
		if numbered != "" {
			flatExpecteds = append(flatExpecteds, numbered)
		} else if trimmed != "" {
			flatExpecteds = append(flatExpecteds, trimmed)
		}
	}

	// Distribute expected results across steps:
	// - If we have exactly N expected items and N steps, distribute 1:1.
	// - If fewer expected items than steps, put everything on the last step.
	// - If more expected items than steps, fill 1:1 and append remaining to last step.
	if len(steps) > 0 && len(flatExpecteds) > 0 {
		if len(flatExpecteds) < len(steps) {
			// Fewer expected results than steps: put all on last step (preserves old behavior)
			steps[len(steps)-1].Expected = strings.Join(flatExpecteds, " ")
		} else {
			// One expected result per step (or more), distribute 1:1
			for i := 0; i < len(steps); i++ {
				if i < len(flatExpecteds) {
					steps[i].Expected = flatExpecteds[i]
				}
			}
			// If more expected results than steps, append remaining to last step
			if len(flatExpecteds) > len(steps) {
				steps[len(steps)-1].Expected = strings.Join(flatExpecteds[len(steps)-1:], " ")
			}
		}
	}

	if len(steps) == 0 {
		return models.TestCase{}, false
	}

	nowStr := now.Format(time.RFC3339)
	testCaseName := fmt.Sprintf("%s - %s: %s", tcCode, tcType, title)

	parsed := models.ParsedTestCase{
		Name:         testCaseName,
		UserStory:    meta.UserStory,
		PreCondition: preCondition,
		Steps:        parsedStepsFromV2(steps),
	}

	tc := models.TestCase{
		ID:           deterministicID("tc", path, testCaseName),
		Order:        0, // Caller sets this
		Code:         tcCode,
		Title:        testCaseName,
		Description:  meta.UserStory,
		PreCondition: preCondition,
		Steps:        steps,
		Tags:         inferTags(parsed),
		Priority:     inferPriority(parsed),
		Type:         inferTestType(parsed),
		Note:         meta.AdditionalNote,
		CreatedAt:    nowStr,
		UpdatedAt:    nowStr,
	}

	return tc, true
}

// parseNewFormatBoldSection extracts content after a bold heading like **Pre-Condition**
// until the next bold heading or end of content.
func parseNewFormatBoldSection(content string, heading string) string {
	lines := strings.Split(content, "\n")
	var out []string
	inSection := false
	boldHeadingRe := regexp.MustCompile(`^\*\*(.+?)\*\*\s*$`)

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if m := boldHeadingRe.FindStringSubmatch(trimmed); len(m) >= 2 {
			if inSection {
				break
			}
			inSection = strings.EqualFold(strings.TrimSpace(m[1]), heading)
			continue
		}
		if inSection && trimmed != "" {
			out = append(out, line)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// parseNewFormatBoldSectionLines extracts non-empty lines from a bold section.
func parseNewFormatBoldSectionLines(content string, heading string) []string {
	section := parseNewFormatBoldSection(content, heading)
	if section == "" {
		return nil
	}
	rawLines := strings.Split(section, "\n")
	var result []string
	for _, line := range rawLines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
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
