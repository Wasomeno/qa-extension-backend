package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"reflect"
	"strings"
	"time"

	gitlab "gitlab.com/gitlab-org/api/client-go"

	"qa-extension-backend/client"
	"qa-extension-backend/database"
	"qa-extension-backend/internal/models"
	"qa-extension-backend/services"
)

// ToolExecutor is a function that executes a tool with given arguments
type ToolExecutor func(ctx context.Context, args map[string]any) (any, error)

// toolExecutorRegistry maps tool names to their executors
var toolExecutorRegistry = make(map[string]ToolExecutor)

// init registers all available tool executors
func init() {
	// GitLab tools
	registerToolExecutor("listGitLabProjects", execListGitLabProjects)
	registerTypedToolExecutor("listAppProjects", listAppProjects)
	registerTypedToolExecutor("getAppProject", getAppProject)
	registerTypedToolExecutor("getProjectTestContext", getProjectTestContext)
	registerTypedToolExecutor("updateProjectTestContext", updateProjectTestContext)
	registerToolExecutor("listAllGitLabIssues", execListAllGitLabIssues)
	registerToolExecutor("createGitLabIssue", execCreateGitLabIssue)
	registerToolExecutor("listGitLabIssues", execListGitLabIssues)
	registerToolExecutor("updateGitLabIssue", execUpdateGitLabIssue)
	registerTypedToolExecutor("getIssueComments", getIssueComments)
	registerTypedToolExecutor("createIssueComment", createIssueComment)
	registerTypedToolExecutor("createIssueEvidence", createIssueEvidence)
	registerTypedToolExecutor("getIssueLinks", getIssueLinks)

	// Test tools
	registerToolExecutor("listGitLabRepositoryTree", execListGitLabRepositoryTree)
	registerToolExecutor("getGitLabFileContent", execGetGitLabFileContent)
	registerToolExecutor("searchGitLabCode", execSearchGitLabCode)
	registerToolExecutor("listGitLabBranches", execListGitLabBranches)
	registerTypedToolExecutor("listSpecsTree", listSpecsTree)
	registerTypedToolExecutor("getSpecsFile", getSpecsFile)
	registerTypedToolExecutor("searchSpecs", searchSpecs)
	registerTypedToolExecutor("getSpecsFileBlame", getSpecsFileBlame)
	registerTypedToolExecutor("listSpecsCommits", listSpecsCommits)
	registerTypedToolExecutor("commitSpecsFiles", commitSpecsFiles)
	registerTypedToolExecutor("listKnowledgeGraphs", listKnowledgeGraphs)
	registerTypedToolExecutor("getKnowledgeGraph", getKnowledgeGraph)
	registerTypedToolExecutor("getKnowledgeGraphCoverage", getKnowledgeGraphCoverage)

	registerToolExecutor("listRecordedTests", execListRecordedTests)
	registerToolExecutor("runRecordedTest", execRunRecordedTest)
	registerToolExecutor("listTestScenarios", execListTestScenarios)
	registerToolExecutor("runTestScenario", execRunTestScenario)
	registerToolExecutor("runScenarioTestCase", execRunScenarioTestCase)
}

func registerToolExecutor(name string, executor ToolExecutor) {
	toolExecutorRegistry[name] = executor
}

func registerTypedToolExecutor[T any, R any](name string, fn func(context.Context, T) (R, error)) {
	registerToolExecutor(name, func(ctx context.Context, args map[string]any) (any, error) {
		raw, err := json.Marshal(args)
		if err != nil {
			return nil, fmt.Errorf("invalid arguments for %s: %w", name, err)
		}
		var typedArgs T
		if err := json.Unmarshal(raw, &typedArgs); err != nil {
			return nil, fmt.Errorf("invalid arguments for %s: %w", name, err)
		}
		return fn(ctx, typedArgs)
	})
}

// ExecuteTool executes a tool by name with the given arguments
func ExecuteTool(toolName string, ctx context.Context, args map[string]any) (any, error) {
	executor, ok := toolExecutorRegistry[toolName]
	if !ok {
		return nil, fmt.Errorf("tool '%s' not found in executor registry", toolName)
	}
	return executor(ctx, args)
}

// HasToolExecutor checks if a tool has an executor
func HasToolExecutor(toolName string) bool {
	_, ok := toolExecutorRegistry[toolName]
	return ok
}

// --- GitLab Tool Executors ---

func execListGitLabProjects(ctx context.Context, args map[string]any) (any, error) {
	log.Printf("[ToolExecutor] execListGitLabProjects called with args: %+v", args)

	events := NewAgentToolEmitter(ctx)
	events.Start("Fetching GitLab projects...")

	gitlabClient, err := getGitLabClientDirect(ctx)
	if err != nil {
		events.Error("Failed to fetch GitLab projects: " + err.Error())
		return nil, err
	}

	search, _ := args["search"].(string)
	owned, _ := args["owned"].(bool)
	starred, _ := args["starred"].(bool)

	opts := &gitlab.ListProjectsOptions{
		Membership: gitlab.Ptr(true),
		Simple:     gitlab.Ptr(true),
		ListOptions: gitlab.ListOptions{
			PerPage: 100,
		},
	}

	if search != "" {
		opts.Search = &search
	}
	if owned {
		opts.Owned = gitlab.Ptr(true)
	}
	if starred {
		opts.Starred = gitlab.Ptr(true)
	}

	projects, _, err := gitlabClient.Projects.ListProjects(opts)
	if err != nil {
		events.Error("Failed to list GitLab projects: " + err.Error())
		return nil, err
	}

	var result []ProjectShortInfo
	for _, p := range projects {
		result = append(result, ProjectShortInfo{
			ID:                p.ID,
			Name:              p.Name,
			PathWithNamespace: p.PathWithNamespace,
			WebURL:            p.WebURL,
			Description:       p.Description,
		})
	}

	events.Done("Loaded %d GitLab projects", len(result))

	return &ListProjectsResponse{Projects: result}, nil
}

func execListAllGitLabIssues(ctx context.Context, args map[string]any) (any, error) {
	log.Printf("[ToolExecutor] execListAllGitLabIssues called with args: %+v", args)

	events := NewAgentToolEmitter(ctx)
	events.Start("Fetching all GitLab issues...")

	gitlabClient, err := getGitLabClientDirect(ctx)
	if err != nil {
		events.Error("Failed to fetch GitLab issues: " + err.Error())
		return nil, err
	}

	state, _ := args["state"].(string)
	opt := &gitlab.ListIssuesOptions{
		Scope: gitlab.Ptr("all"),
	}
	if state != "" {
		opt.State = &state
	}

	issues, err := client.ListIssuesRelatedToMe(gitlabClient, opt)
	if err != nil {
		events.Error("Failed to list GitLab issues: " + err.Error())
		return nil, err
	}

	var result []IssueShortInfo
	for _, i := range issues {
		result = append(result, IssueShortInfo{
			ID:          i.ID,
			IID:         i.IID,
			ProjectID:   i.ProjectID,
			Title:       i.Title,
			State:       i.State,
			Labels:      i.Labels,
			WebURL:      i.WebURL,
			Description: i.Description,
		})
	}

	events.Done("Loaded %d GitLab issues", len(result))

	return &ListIssuesResponse{Issues: result}, nil
}

func execCreateGitLabIssue(ctx context.Context, args map[string]any) (any, error) {
	log.Printf("[ToolExecutor] execCreateGitLabIssue called with args: %+v", args)

	events := NewAgentToolEmitter(ctx)
	events.Start("Creating GitLab issue...")

	gitlabClient, err := getGitLabClientDirect(ctx)
	if err != nil {
		events.Error("Failed to create GitLab issue: " + err.Error())
		return nil, err
	}

	projectID, _ := args["projectId"].(float64)
	title, _ := args["title"].(string)
	description, _ := args["description"].(string)
	labels, _ := args["labels"].([]any)

	opt := &gitlab.CreateIssueOptions{
		Title:       &title,
		Description: &description,
	}

	if len(labels) > 0 {
		var strLabels []string
		for _, l := range labels {
			if s, ok := l.(string); ok {
				strLabels = append(strLabels, s)
			}
		}
		l := gitlab.LabelOptions(strLabels)
		opt.Labels = &l
	}

	issue, _, err := gitlabClient.Issues.CreateIssue(fmt.Sprintf("%d", int(projectID)), opt)
	if err != nil {
		events.Error("Failed to create GitLab issue: " + err.Error())
		return nil, err
	}

	events.Done("Created GitLab issue: %s", issue.Title)

	return issue, nil
}

func execListGitLabIssues(ctx context.Context, args map[string]any) (any, error) {
	log.Printf("[ToolExecutor] execListGitLabIssues called with args: %+v", args)

	events := NewAgentToolEmitter(ctx)
	events.Start("Fetching project GitLab issues...")

	gitlabClient, err := getGitLabClientDirect(ctx)
	if err != nil {
		events.Error("Failed to fetch project GitLab issues: " + err.Error())
		return nil, err
	}

	projectID, _ := args["projectID"].(float64)
	state, _ := args["state"].(string)

	opt := &gitlab.ListProjectIssuesOptions{
		Scope: gitlab.Ptr("all"),
	}
	if state != "" {
		opt.State = &state
	}

	issues, _, err := gitlabClient.Issues.ListProjectIssues(fmt.Sprintf("%d", int(projectID)), opt)
	if err != nil {
		events.Error("Failed to list project GitLab issues: " + err.Error())
		return nil, err
	}

	var result []IssueShortInfo
	for _, i := range issues {
		result = append(result, IssueShortInfo{
			ID:          i.ID,
			IID:         i.IID,
			ProjectID:   i.ProjectID,
			Title:       i.Title,
			State:       i.State,
			Labels:      i.Labels,
			WebURL:      i.WebURL,
			Description: i.Description,
		})
	}

	events.Done("Loaded %d project issues", len(result))

	return &ListIssuesResponse{Issues: result}, nil
}

func execUpdateGitLabIssue(ctx context.Context, args map[string]any) (any, error) {
	log.Printf("[ToolExecutor] execUpdateGitLabIssue called with args: %+v", args)

	events := NewAgentToolEmitter(ctx)
	events.Start("Updating GitLab issue...")

	gitlabClient, err := getGitLabClientDirect(ctx)
	if err != nil {
		events.Error("Failed to update GitLab issue: " + err.Error())
		return nil, err
	}

	projectID, _ := args["projectId"].(float64)
	issueIID, _ := args["issueIid"].(float64)
	updates, _ := args["updates"].(map[string]any)

	opt := &gitlab.UpdateIssueOptions{}
	if title, ok := updates["title"].(string); ok {
		opt.Title = &title
	}
	if desc, ok := updates["description"].(string); ok {
		opt.Description = &desc
	}
	if state, ok := updates["state"].(string); ok {
		opt.StateEvent = &state
	}

	issue, _, err := gitlabClient.Issues.UpdateIssue(fmt.Sprintf("%d", int(projectID)), int64(issueIID), opt)
	if err != nil {
		events.Error("Failed to update GitLab issue: " + err.Error())
		return nil, err
	}

	events.Done("Updated GitLab issue: %s", issue.Title)

	return issue, nil
}

func getGitLabClientDirect(ctx context.Context) (*gitlab.Client, error) {
	return getGitLabClient(ctx)
}

// --- Repository Explorer Tool Executors ---

func execListGitLabRepositoryTree(ctx context.Context, args map[string]any) (any, error) {
	log.Printf("[ToolExecutor] execListGitLabRepositoryTree called with args: %+v", args)

	events := NewAgentToolEmitter(ctx)
	events.Start("Listing repository tree...")

	gitlabClient, err := getGitLabClientDirect(ctx)
	if err != nil {
		events.Error("Failed to list repository tree: " + err.Error())
		return nil, err
	}

	projectID, _ := args["projectId"].(string)
	path, _ := args["path"].(string)
	ref, _ := args["ref"].(string)
	recursive, _ := args["recursive"].(bool)

	if projectID == "" {
		return nil, fmt.Errorf("projectId is required")
	}
	if err := ensureRepoMirrorForAgentTool(ctx, gitlabClient, projectID); err != nil {
		return nil, err
	}

	// Resolve ref
	if ref == "" {
		project, _, err := gitlabClient.Projects.GetProject(projectID, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to get project: %w", err)
		}
		ref = project.DefaultBranch
		if ref == "" {
			ref = "main"
		}
	}

	if path == "" {
		path = "."
	}

	if entries, cacheErr := services.DefaultRepoCache().ListTree(ctx, gitlabClient, projectID, ref, path, recursive); cacheErr == nil {
		result := make([]RepoTreeNode, 0, len(entries))
		for _, n := range entries {
			result = append(result, RepoTreeNode{ID: n.ID, Name: n.Name, Type: n.Type, Path: n.Path})
		}
		events.Done("Found %d cached items at ref '%s'", len(result), ref)
		return &ListRepoTreeResponse{Nodes: result, Count: len(result)}, nil
	} else if !errors.Is(cacheErr, services.ErrRepoCacheDisabled) && !errors.Is(cacheErr, services.ErrRepoCacheToken) {
		log.Printf("[ToolExecutor] repository tree cache fallback: %v", cacheErr)
	}

	opt := &gitlab.ListTreeOptions{
		Path:      &path,
		Ref:       &ref,
		Recursive: &recursive,
	}

	nodes, _, err := gitlabClient.Repositories.ListTree(projectID, opt)
	if err != nil {
		events.Error("Failed to list repository tree: " + err.Error())
		return nil, fmt.Errorf("failed to list repository tree at ref '%s': %w", ref, err)
	}

	var result []RepoTreeNode
	for _, n := range nodes {
		result = append(result, RepoTreeNode{
			ID:   n.ID,
			Name: n.Name,
			Type: n.Type,
			Path: n.Path,
		})
	}

	events.Done("Found %d items at ref '%s'", len(result), ref)

	return &ListRepoTreeResponse{Nodes: result, Count: len(result)}, nil
}

func execGetGitLabFileContent(ctx context.Context, args map[string]any) (any, error) {
	log.Printf("[ToolExecutor] execGetGitLabFileContent called with args: %+v", args)

	events := NewAgentToolEmitter(ctx)
	events.Start("Reading file content...")

	gitlabClient, err := getGitLabClientDirect(ctx)
	if err != nil {
		events.Error("Failed to read file: " + err.Error())
		return nil, err
	}

	projectID, _ := args["projectId"].(string)
	filePath, _ := args["filePath"].(string)
	ref, _ := args["ref"].(string)

	if projectID == "" || filePath == "" {
		return nil, fmt.Errorf("projectId and filePath are required")
	}
	if err := ensureRepoMirrorForAgentTool(ctx, gitlabClient, projectID); err != nil {
		return nil, err
	}

	// Resolve ref
	if ref == "" {
		project, _, err := gitlabClient.Projects.GetProject(projectID, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to get project: %w", err)
		}
		ref = project.DefaultBranch
		if ref == "" {
			ref = "main"
		}
	}

	if content, meta, cacheErr := services.DefaultRepoCache().ReadFile(ctx, gitlabClient, projectID, ref, filePath); cacheErr == nil {
		events.Done("Read %d cached bytes from %s", meta.Size, filePath)
		return &FileContentResponse{
			FilePath: filePath,
			Content:  content,
			Size:     int(meta.Size),
			Encoding: "text",
		}, nil
	} else if !errors.Is(cacheErr, services.ErrRepoCacheDisabled) && !errors.Is(cacheErr, services.ErrRepoCacheToken) {
		log.Printf("[ToolExecutor] file content cache fallback: %v", cacheErr)
	}

	fileOpt := &gitlab.GetFileOptions{Ref: &ref}
	file, _, err := gitlabClient.RepositoryFiles.GetFile(projectID, filePath, fileOpt)
	if err != nil {
		events.Error("Failed to read file: " + err.Error())
		return nil, fmt.Errorf("failed to get file '%s' at ref '%s': %w", filePath, ref, err)
	}

	contentBytes, err := base64.StdEncoding.DecodeString(file.Content)
	if err != nil {
		return nil, fmt.Errorf("failed to decode file content: %w", err)
	}

	events.Done("Read %d bytes from %s", len(contentBytes), filePath)

	return &FileContentResponse{
		FilePath: filePath,
		Content:  string(contentBytes),
		Size:     int(file.Size),
		Encoding: file.Encoding,
	}, nil
}

func execSearchGitLabCode(ctx context.Context, args map[string]any) (any, error) {
	log.Printf("[ToolExecutor] execSearchGitLabCode called with args: %+v", args)

	events := NewAgentToolEmitter(ctx)
	events.Start("Searching code...")

	gitlabClient, err := getGitLabClientDirect(ctx)
	if err != nil {
		events.Error("Failed to search code: " + err.Error())
		return nil, err
	}

	projectID, _ := args["projectId"].(string)
	query, _ := args["query"].(string)
	ref, _ := args["ref"].(string)

	if projectID == "" || query == "" {
		return nil, fmt.Errorf("projectId and query are required")
	}
	if err := ensureRepoMirrorForAgentTool(ctx, gitlabClient, projectID); err != nil {
		return nil, err
	}

	// Resolve ref
	if ref == "" {
		project, _, err := gitlabClient.Projects.GetProject(projectID, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to get project: %w", err)
		}
		ref = project.DefaultBranch
		if ref == "" {
			ref = "main"
		}
	}

	searchOpts := &gitlab.SearchOptions{
		Ref: &ref,
	}

	if cached, cacheErr := services.DefaultRepoCache().Search(ctx, gitlabClient, projectID, ref, query, "", 0); cacheErr == nil {
		searchResults := make([]SearchResult, 0, len(cached))
		for _, r := range cached {
			searchResults = append(searchResults, SearchResult{FilePath: r.FilePath, Ref: r.Ref, Content: r.Content})
		}
		events.Done("Found %d cached results at ref '%s'", len(searchResults), ref)
		return &SearchCodeResponse{Results: searchResults, Count: len(searchResults)}, nil
	} else if !errors.Is(cacheErr, services.ErrRepoCacheDisabled) && !errors.Is(cacheErr, services.ErrRepoCacheToken) {
		log.Printf("[ToolExecutor] code search cache fallback: %v", cacheErr)
	}

	results, _, err := gitlabClient.Search.BlobsByProject(projectID, query, searchOpts)
	if err != nil {
		events.Error("Search failed: " + err.Error())
		return nil, fmt.Errorf("search failed at ref '%s': %w", ref, err)
	}

	var searchResults []SearchResult
	for _, r := range results {
		fileOpt := &gitlab.GetFileOptions{Ref: &ref}
		file, _, err := gitlabClient.RepositoryFiles.GetFile(projectID, r.Filename, fileOpt)
		if err == nil {
			contentBytes, _ := base64.StdEncoding.DecodeString(file.Content)
			searchResults = append(searchResults, SearchResult{
				FilePath: r.Filename,
				Ref:      ref,
				Content:  string(contentBytes),
			})
		}
	}

	events.Done("Found %d results at ref '%s'", len(searchResults), ref)

	return &SearchCodeResponse{Results: searchResults, Count: len(searchResults)}, nil
}

func execListGitLabBranches(ctx context.Context, args map[string]any) (any, error) {
	log.Printf("[ToolExecutor] execListGitLabBranches called with args: %+v", args)

	events := NewAgentToolEmitter(ctx)
	events.Start("Fetching branches...")

	gitlabClient, err := getGitLabClientDirect(ctx)
	if err != nil {
		events.Error("Failed to fetch branches: " + err.Error())
		return nil, err
	}

	projectID, _ := args["projectId"].(string)
	search, _ := args["search"].(string)

	if projectID == "" {
		return nil, fmt.Errorf("projectId is required")
	}
	if err := ensureRepoMirrorForAgentTool(ctx, gitlabClient, projectID); err != nil {
		return nil, err
	}

	opts := &gitlab.ListBranchesOptions{
		ListOptions: gitlab.ListOptions{
			PerPage: 100,
		},
	}
	if search != "" {
		opts.Search = &search
	}

	var allBranches []*gitlab.Branch
	for {
		branches, resp, err := gitlabClient.Branches.ListBranches(projectID, opts)
		if err != nil {
			events.Error("Failed to list branches: " + err.Error())
			return nil, fmt.Errorf("failed to list branches: %w", err)
		}
		allBranches = append(allBranches, branches...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	// Get project to identify the default branch
	project, _, projErr := gitlabClient.Projects.GetProject(projectID, nil)
	defaultBranch := ""
	if projErr == nil && project != nil {
		defaultBranch = project.DefaultBranch
	}

	var result []BranchInfo
	for _, b := range allBranches {
		result = append(result, BranchInfo{
			Name:               b.Name,
			Protected:          b.Protected,
			Default:            b.Name == defaultBranch,
			DevelopersCanPush:  b.DevelopersCanPush,
			DevelopersCanMerge: b.DevelopersCanMerge,
			WebURL:             b.WebURL,
		})
	}

	events.Done("Found %d branches", len(result))

	return &ListBranchesResponse{Branches: result, Count: len(result)}, nil
}

// --- Test Tool Executors ---

func execListRecordedTests(ctx context.Context, args map[string]any) (any, error) {
	log.Printf("[ToolExecutor] execListRecordedTests called with args: %+v", args)

	events := NewAgentToolEmitter(ctx)
	events.Start("Fetching recorded tests...")

	result, err := listRecordedTestsDirect(ctx, args)
	if err != nil {
		events.Error("Failed to fetch recorded tests: " + err.Error())
		return nil, err
	}

	events.Done("Loaded %d recorded tests", len(result.Recordings))

	return result, nil
}

func execRunRecordedTest(ctx context.Context, args map[string]any) (any, error) {
	testID, _ := args["testID"].(string)
	log.Printf("[ToolExecutor] execRunRecordedTest called with testID: %s", testID)

	events := NewAgentEmitter(ctx, testID)
	events.Start("Running recorded test '%s'...", testID)

	result, err := runRecordedTestDirect(ctx, args)
	if err != nil {
		events.Error(fmt.Sprintf("Test '%s' failed: %v", testID, err))
		return nil, err
	}

	events.Done("Test '%s' finished: %s", testID, result.Status)

	return result, nil
}

func execListTestScenarios(ctx context.Context, args map[string]any) (any, error) {
	log.Printf("[ToolExecutor] execListTestScenarios called with args: %+v", args)

	events := NewAgentToolEmitter(ctx)
	events.Start("Fetching test scenarios...")

	result, err := listTestScenariosDirect(ctx, ListTestScenariosArgs{
		AppProjectID: stringArg(args, "appProjectId"),
		ProjectID:    stringArg(args, "projectId"),
		Search:       stringArg(args, "search"),
		Limit:        intArg(args, "limit"),
	})
	if err != nil {
		events.Error("Failed to fetch test scenarios: " + err.Error())
		return nil, err
	}

	events.Done("Loaded %d test scenarios", len(result.Scenarios))

	return result, nil
}

func execRunTestScenario(ctx context.Context, args map[string]any) (any, error) {
	scenarioID, _ := args["scenarioID"].(string)
	log.Printf("[ToolExecutor] execRunTestScenario called with scenarioID: %s", scenarioID)

	events := NewAgentEmitter(ctx, scenarioID)
	events.Start("Running test scenario '%s'...", scenarioID)

	result, err := runTestScenarioDirect(ctx, args)
	if err != nil {
		events.Error(fmt.Sprintf("Scenario '%s' failed: %v", scenarioID, err))
		return nil, err
	}

	events.Done("Scenario '%s' finished: %s", scenarioID, result.Summary)

	return result, nil
}

func execRunScenarioTestCase(ctx context.Context, args map[string]any) (any, error) {
	scenarioID, _ := args["scenarioID"].(string)
	testCaseID, _ := args["testCaseID"].(string)
	resourceID := scenarioID + ":" + testCaseID

	log.Printf("[ToolExecutor] execRunScenarioTestCase called with scenarioID: %s, testCaseID: %s", scenarioID, testCaseID)

	events := NewAgentEmitter(ctx, resourceID)
	events.Start("Running test case '%s' from scenario '%s'...", testCaseID, scenarioID)

	result, err := runScenarioTestCaseDirect(ctx, args)
	if err != nil {
		events.Error(fmt.Sprintf("Test case '%s' failed: %v", testCaseID, err))
		return nil, err
	}

	events.Done("Test case '%s' finished: %s", testCaseID, result.Status)

	return result, nil
}

// Direct implementations that don't use ADK tool.Context

func listRecordedTestsDirect(ctx context.Context, args map[string]any) (*ListRecordedTestsResponse, error) {
	projectID, _ := args["projectID"].(string)
	issueID, _ := args["issueID"].(string)

	var ids []string
	var err error

	if issueID != "" {
		ids, err = database.RedisClient.SMembers(ctx, fmt.Sprintf("recordings:issue:%s", issueID)).Result()
	} else if projectID != "" {
		ids, err = database.RedisClient.SMembers(ctx, fmt.Sprintf("recordings:project:%s", projectID)).Result()
	} else {
		ids, err = database.RedisClient.SMembers(ctx, "recordings").Result()
	}

	if err != nil {
		return nil, fmt.Errorf("failed to fetch recordings: %w", err)
	}

	// Return summaries instead of full recordings to keep response size manageable
	var summaries []models.ManualRecordingSummary
	for _, id := range ids {
		val, err := database.RedisClient.Get(ctx, fmt.Sprintf("recording:%s", id)).Result()
		if err != nil {
			continue
		}

		var r models.ManualRecording
		if err := json.Unmarshal([]byte(val), &r); err != nil {
			continue
		}

		summaries = append(summaries, models.ManualRecordingSummary{
			ID:          r.ID,
			Name:        r.Name,
			Description: r.Description,
			Status:      r.Status,
			ProjectID:   r.ProjectID,
			IssueID:     r.IssueID,
			CreatorID:   r.CreatorID,
			VideoURL:    r.VideoURL,
			StepCount:   len(r.Steps),
			CreatedAt:   r.CreatedAt,
		})
	}

	return &ListRecordedTestsResponse{Recordings: summaries}, nil
}

func runRecordedTestDirect(ctx context.Context, args map[string]any) (*models.TestResult, error) {
	testID, _ := args["testID"].(string)
	overrides, _ := args["overrides"].([]any)

	val, err := database.RedisClient.Get(ctx, fmt.Sprintf("recording:%s", testID)).Result()
	if err != nil {
		return nil, fmt.Errorf("recording not found: %s", testID)
	}

	var recording models.ManualRecording
	if err := json.Unmarshal([]byte(val), &recording); err != nil {
		return nil, fmt.Errorf("failed to parse recording: %w", err)
	}

	// Apply overrides
	if len(overrides) > 0 {
		for i, step := range recording.Steps {
			if step.Action == "type" || step.Action == "fill" {
				for _, o := range overrides {
					if override, ok := o.(map[string]any); ok {
						desc, _ := override["selectorDescription"].(string)
						newValue, _ := override["newValue"].(string)
						if desc != "" && newValue != "" {
							if strings.Contains(strings.ToLower(step.Value), strings.ToLower(desc)) ||
								strings.Contains(strings.ToLower(step.Description), strings.ToLower(desc)) {
								recording.Steps[i].Value = newValue
							}
						}
					}
				}
			}
		}
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	run := &models.TestRun{ID: recording.ID, Name: recording.Name, Steps: recording.Steps}
	return RunTest(timeoutCtx, run)
}

func listTestScenariosDirect(ctx context.Context, args ListTestScenariosArgs) (*ListTestScenariosResponse, error) {
	projectID := strings.TrimSpace(args.AppProjectID)
	if projectID == "" {
		projectID = strings.TrimSpace(args.ProjectID)
	}
	if projectID == "" {
		return nil, fmt.Errorf("appProjectId is required to list project test scenarios")
	}
	limit := args.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	ids, err := database.RedisClient.SMembers(ctx, fmt.Sprintf("scenarios:project:%s", projectID)).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to list scenarios: %w", err)
	}
	if len(ids) == 0 {
		return &ListTestScenariosResponse{Scenarios: []models.TestScenario{}, Count: 0, ProjectID: projectID}, nil
	}

	var scenarios []models.TestScenario
	keys := make([]string, 0, len(ids))
	seenIDs := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || seenIDs[id] {
			continue
		}
		seenIDs[id] = true
		keys = append(keys, fmt.Sprintf("scenario:%s", id))
	}

	values, err := database.RedisClient.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to load scenarios: %w", err)
	}

	search := strings.ToLower(strings.TrimSpace(args.Search))
	for _, raw := range values {
		val, ok := raw.(string)
		if !ok || val == "" {
			continue
		}
		var s models.TestScenario
		if err := json.Unmarshal([]byte(val), &s); err != nil {
			continue
		}
		if s.ProjectID != projectID {
			continue
		}
		if search != "" &&
			!strings.Contains(strings.ToLower(s.Title), search) &&
			!strings.Contains(strings.ToLower(s.ProjectName), search) &&
			!strings.Contains(strings.ToLower(s.SourcePath), search) {
			continue
		}
		scenarios = append(scenarios, compactScenarioForAgentList(s))
		if len(scenarios) >= limit {
			break
		}
	}

	return &ListTestScenariosResponse{Scenarios: scenarios, Count: len(scenarios), ProjectID: projectID}, nil
}

func compactScenarioForAgentList(s models.TestScenario) models.TestScenario {
	s.ComputeAutomationSummary(5)
	s.Sections = nil
	s.Sheets = nil
	s.AuthConfig = models.AuthConfig{}
	return s
}

func stringArg(args map[string]any, key string) string {
	if value, ok := args[key].(string); ok {
		return value
	}
	return ""
}

func intArg(args map[string]any, key string) int {
	switch value := args[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func runTestScenarioDirect(ctx context.Context, args map[string]any) (*RunTestScenarioResponse, error) {
	scenarioID, _ := args["scenarioID"].(string)

	val, err := database.RedisClient.Get(ctx, fmt.Sprintf("scenario:%s", scenarioID)).Result()
	if err != nil {
		return nil, fmt.Errorf("scenario not found: %s", scenarioID)
	}

	var scenario models.TestScenario
	if err := json.Unmarshal([]byte(val), &scenario); err != nil {
		return nil, fmt.Errorf("failed to parse scenario: %w", err)
	}

	if scenario.Stats != nil && scenario.Stats.AutomatedCount == 0 {
		return nil, fmt.Errorf("no tests have been generated for this scenario yet")
	}

	var runs []models.TestRun
	for _, section := range scenario.Sections {
		for _, tc := range section.TestCases {
			if tc.AutomationTest != nil && len(tc.AutomationTest.Steps) > 0 {
				runs = append(runs, models.TestRun{
					ID:    tc.AutomationTest.ID,
					Name:  tc.AutomationTest.Name,
					Steps: tc.AutomationTest.Steps,
				})
			}
		}
	}

	if len(runs) == 0 {
		return nil, fmt.Errorf("failed to load any automation tests for the scenario")
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	results := RunTestsParallel(timeoutCtx, runs)

	passed := 0
	failed := 0
	for _, res := range results {
		if res.Status == "passed" {
			passed++
		} else {
			failed++
		}
		_ = database.SaveTestResult(ctx, res)
		// Update the scenario's AutomationTest inline with the run result
		for si := range scenario.Sections {
			for ti := range scenario.Sections[si].TestCases {
				at := scenario.Sections[si].TestCases[ti].AutomationTest
				if at != nil && at.ID == res.TestID {
					scenario.Sections[si].TestCases[ti].AutomationTest.Status = mapResultStatus(res.Status)
					scenario.Sections[si].TestCases[ti].AutomationTest.LastRunAt = time.Now().Format(time.RFC3339)
					scenario.Sections[si].TestCases[ti].AutomationTest.RunDurationMs = res.RunDurationMs
					scenario.Sections[si].TestCases[ti].AutomationTest.VideoURL = res.VideoURL
					scenario.Sections[si].TestCases[ti].AutomationTest.Log = res.Log
					scenario.Sections[si].TestCases[ti].AutomationTest.ErrorMessage = ""
					mergeStepResults(scenario.Sections[si].TestCases[ti].AutomationTest, res.StepResults)
					if res.Status == "failed" {
						for _, sr := range res.StepResults {
							if sr.Status == "failure" {
								scenario.Sections[si].TestCases[ti].AutomationTest.ErrorMessage = sr.Error
								break
							}
						}
					}
				}
			}
		}
	}

	scenario.ComputeStats()
	_ = saveScenarioToRedis(ctx, &scenario)

	summary := fmt.Sprintf("Execution completed. Passed: %d, Failed: %d", passed, failed)
	return &RunTestScenarioResponse{Summary: summary, Results: results}, nil
}

func runScenarioTestCaseDirect(ctx context.Context, args map[string]any) (*models.TestResult, error) {
	scenarioID, _ := args["scenarioID"].(string)
	testCaseID, _ := args["testCaseID"].(string)

	val, err := database.RedisClient.Get(ctx, fmt.Sprintf("scenario:%s", scenarioID)).Result()
	if err != nil {
		return nil, fmt.Errorf("scenario not found: %s", scenarioID)
	}

	var scenario models.TestScenario
	if err := json.Unmarshal([]byte(val), &scenario); err != nil {
		return nil, fmt.Errorf("failed to parse scenario: %w", err)
	}

	var targetCase *models.TestCase
	for _, section := range scenario.Sections {
		for _, tc := range section.TestCases {
			if tc.ID == testCaseID {
				tcCopy := tc // Create a local copy to take address of safely in loop
				targetCase = &tcCopy
				break
			}
		}
		if targetCase != nil {
			break
		}
	}

	if targetCase == nil {
		return nil, fmt.Errorf("test case %s not found in scenario %s", testCaseID, scenarioID)
	}

	if targetCase.AutomationTest == nil || len(targetCase.AutomationTest.Steps) == 0 {
		return nil, fmt.Errorf("this test case has not been generated yet")
	}

	run := &models.TestRun{
		ID:    targetCase.AutomationTest.ID,
		Name:  targetCase.AutomationTest.Name,
		Steps: targetCase.AutomationTest.Steps,
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	result, err := RunTest(timeoutCtx, run)
	if err != nil {
		return nil, err
	}

	_ = database.SaveTestResult(ctx, result)

	// Update the scenario's AutomationTest inline with the run result
	for si := range scenario.Sections {
		for ti := range scenario.Sections[si].TestCases {
			at := scenario.Sections[si].TestCases[ti].AutomationTest
			if at != nil && at.ID == result.TestID {
				scenario.Sections[si].TestCases[ti].AutomationTest.Status = mapResultStatus(result.Status)
				scenario.Sections[si].TestCases[ti].AutomationTest.LastRunAt = time.Now().Format(time.RFC3339)
				scenario.Sections[si].TestCases[ti].AutomationTest.RunDurationMs = result.RunDurationMs
				scenario.Sections[si].TestCases[ti].AutomationTest.VideoURL = result.VideoURL
				scenario.Sections[si].TestCases[ti].AutomationTest.Log = result.Log
				scenario.Sections[si].TestCases[ti].AutomationTest.ErrorMessage = ""
				mergeStepResults(scenario.Sections[si].TestCases[ti].AutomationTest, result.StepResults)
				if result.Status == "failed" {
					for _, sr := range result.StepResults {
						if sr.Status == "failure" {
							scenario.Sections[si].TestCases[ti].AutomationTest.ErrorMessage = sr.Error
							break
						}
					}
				}
			}
		}
	}

	scenario.ComputeStats()
	_ = saveScenarioToRedis(ctx, &scenario)

	return result, nil
}

// RegisterToolFromFunc registers a tool executor from a reflection-based function
// This allows us to use the existing tool functions with minimal duplication
func RegisterToolFromFunc(toolName string, fn interface{}, argsType reflect.Type) {
	executor := func(ctx context.Context, args map[string]any) (any, error) {
		// Convert map to the appropriate struct type
		argsJson, err := json.Marshal(args)
		if err != nil {
			return nil, err
		}

		// Create a new instance of the args type
		argsVal := reflect.New(argsType)
		if err := json.Unmarshal(argsJson, argsVal.Interface()); err != nil {
			return nil, err
		}

		// Get the function value
		fnVal := reflect.ValueOf(fn)

		// Call the function with (tool.Context, argsType)
		results := fnVal.Call([]reflect.Value{
			reflect.ValueOf(ctx),
			argsVal.Elem(),
		})

		// Get the result and error
		var result, errVal any
		if len(results) >= 1 && !results[0].IsNil() {
			result = results[0].Interface()
		}
		if len(results) >= 2 && !results[1].IsNil() {
			errVal = results[1].Interface().(error)
		}

		if errVal != nil {
			return nil, errVal.(error)
		}
		return result, nil
	}

	registerToolExecutor(toolName, executor)
}
