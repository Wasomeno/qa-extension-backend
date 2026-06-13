package agent

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"qa-extension-backend/auth"
	"qa-extension-backend/client"
	"qa-extension-backend/services"
	"strconv"
	"strings"

	gitlab "gitlab.com/gitlab-org/api/client-go"
	"golang.org/x/oauth2"
)

type ListProjectsArgs struct {
	Search  string `json:"search"`
	Owned   bool   `json:"owned"`
	Starred bool   `json:"starred"`
}

type ProjectShortInfo struct {
	ID                int64  `json:"id"`
	Name              string `json:"name"`
	PathWithNamespace string `json:"pathWithNamespace"`
	WebURL            string `json:"webUrl"`
	Description       string `json:"description"`
}

type ListProjectsResponse struct {
	Projects []ProjectShortInfo `json:"projects"`
}

func listGitLabProjects(ctx context.Context, args ListProjectsArgs) (*ListProjectsResponse, error) {
	log.Printf("[AgentTool] listGitLabProjects called with args: %+v", args)

	events := NewAgentToolEmitter(ctx)
	events.Start("Fetching GitLab projects...")

	gitlabClient, err := getGitLabClient(ctx)
	if err != nil {
		log.Printf("[AgentTool] listGitLabProjects failed to get client: %v", err)
		events.Error("Failed to get GitLab client: " + err.Error())
		return nil, err
	}

	opts := &gitlab.ListProjectsOptions{
		Membership: gitlab.Ptr(true),
		Simple:     gitlab.Ptr(true),
		ListOptions: gitlab.ListOptions{
			PerPage: 100,
		},
	}

	if args.Search != "" {
		opts.Search = &args.Search
	}
	if args.Owned {
		opts.Owned = gitlab.Ptr(true)
	}
	if args.Starred {
		opts.Starred = gitlab.Ptr(true)
	}

	projects, _, err := gitlabClient.Projects.ListProjects(opts)
	if err != nil {
		log.Printf("[AgentTool] listGitLabProjects API error: %v", err)
		events.Error("Failed to list projects: " + err.Error())
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

	log.Printf("[AgentTool] listGitLabProjects success, found %d projects", len(result))
	events.Done("Loaded %d GitLab projects", len(result))

	return &ListProjectsResponse{Projects: result}, nil
}

type CreateIssueArgs struct {
	ProjectID   int      `json:"projectId"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Labels      []string `json:"labels"`
}

func createGitLabIssue(ctx context.Context, args CreateIssueArgs) (*gitlab.Issue, error) {
	log.Printf("[AgentTool] createGitLabIssue called with args: %+v", args)

	events := NewAgentToolEmitter(ctx)
	events.Start("Creating GitLab issue...")

	gitlabClient, err := getGitLabClient(ctx)
	if err != nil {
		log.Printf("[AgentTool] createGitLabIssue failed to get client: %v", err)
		events.Error("Failed to get GitLab client: " + err.Error())
		return nil, err
	}

	opt := &gitlab.CreateIssueOptions{
		Title:       &args.Title,
		Description: &args.Description,
	}
	if len(args.Labels) > 0 {
		l := gitlab.LabelOptions(args.Labels)
		opt.Labels = &l
	}

	issue, _, err := gitlabClient.Issues.CreateIssue(strconv.Itoa(args.ProjectID), opt)
	if err != nil {
		log.Printf("[AgentTool] createGitLabIssue API error: %v", err)
		events.Error("Failed to create issue: " + err.Error())
		return nil, err
	}

	log.Printf("[AgentTool] createGitLabIssue success: issue %d created", issue.IID)
	events.Done("Created issue: %s", issue.Title)

	return issue, nil
}

type ListIssuesArgs struct {
	ProjectID int    `json:"projectId"`
	State     string `json:"state"`
}

type IssueShortInfo struct {
	ID          int64    `json:"id"`
	IID         int64    `json:"iid"`
	ProjectID   int64    `json:"projectId"`
	Title       string   `json:"title"`
	State       string   `json:"state"`
	Labels      []string `json:"labels"`
	WebURL      string   `json:"webUrl"`
	Description string   `json:"description"`
}

type ListIssuesResponse struct {
	Issues []IssueShortInfo `json:"issues"`
}

func listGitLabIssues(ctx context.Context, args ListIssuesArgs) (*ListIssuesResponse, error) {
	log.Printf("[AgentTool] listGitLabIssues called with args: %+v", args)

	events := NewAgentToolEmitter(ctx)
	events.Start("Fetching project GitLab issues...")

	gitlabClient, err := getGitLabClient(ctx)
	if err != nil {
		log.Printf("[AgentTool] listGitLabIssues failed to get client: %v", err)
		events.Error("Failed to get GitLab client: " + err.Error())
		return nil, err
	}

	opt := &gitlab.ListProjectIssuesOptions{
		Scope: gitlab.Ptr("all"),
	}
	if args.State != "" {
		opt.State = &args.State
	}

	issues, _, err := gitlabClient.Issues.ListProjectIssues(strconv.Itoa(args.ProjectID), opt)
	if err != nil {
		log.Printf("[AgentTool] listGitLabIssues API error: %v", err)
		events.Error("Failed to list issues: " + err.Error())
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

	log.Printf("[AgentTool] listGitLabIssues success, found %d issues", len(result))
	events.Done("Loaded %d project issues", len(result))

	return &ListIssuesResponse{Issues: result}, nil
}

type ListAllIssuesArgs struct {
	State string `json:"state"`
}

func listAllGitLabIssues(ctx context.Context, args ListAllIssuesArgs) (*ListIssuesResponse, error) {
	log.Printf("[AgentTool] listAllGitLabIssues called with args: %+v", args)

	events := NewAgentToolEmitter(ctx)
	events.Start("Fetching all GitLab issues...")

	gitlabClient, err := getGitLabClient(ctx)
	if err != nil {
		log.Printf("[AgentTool] listAllGitLabIssues failed to get client: %v", err)
		events.Error("Failed to get GitLab client: " + err.Error())
		return nil, err
	}

	opt := &gitlab.ListIssuesOptions{
		Scope: gitlab.Ptr("all"),
	}
	if args.State != "" {
		opt.State = &args.State
	}

	issues, err := client.ListIssuesRelatedToMe(gitlabClient, opt)
	if err != nil {
		log.Printf("[AgentTool] listAllGitLabIssues API error: %v", err)
		events.Error("Failed to list issues: " + err.Error())
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

	log.Printf("[AgentTool] listAllGitLabIssues success, found %d issues", len(result))
	events.Done("Loaded %d GitLab issues", len(result))

	return &ListIssuesResponse{Issues: result}, nil
}

type UpdateIssueArgs struct {
	ProjectID int            `json:"projectId"`
	IssueIID  int            `json:"issueIid"`
	Updates   map[string]any `json:"updates"`
}

func updateGitLabIssue(ctx context.Context, args UpdateIssueArgs) (*gitlab.Issue, error) {
	log.Printf("[AgentTool] updateGitLabIssue called with args: %+v", args)

	events := NewAgentToolEmitter(ctx)
	events.Start("Updating GitLab issue...")

	gitlabClient, err := getGitLabClient(ctx)
	if err != nil {
		log.Printf("[AgentTool] updateGitLabIssue failed to get client: %v", err)
		events.Error("Failed to get GitLab client: " + err.Error())
		return nil, err
	}

	opt := &gitlab.UpdateIssueOptions{}
	if title, ok := args.Updates["title"].(string); ok {
		opt.Title = &title
	}
	if desc, ok := args.Updates["description"].(string); ok {
		opt.Description = &desc
	}
	if state, ok := args.Updates["state"].(string); ok {
		opt.StateEvent = &state
	}

	issue, _, err := gitlabClient.Issues.UpdateIssue(strconv.Itoa(args.ProjectID), int64(args.IssueIID), opt)
	if err != nil {
		log.Printf("[AgentTool] updateGitLabIssue API error: %v", err)
		events.Error("Failed to update GitLab issue: " + err.Error())
		return nil, err
	}

	log.Printf("[AgentTool] updateGitLabIssue success, updated issue IID: %d", issue.IID)
	events.Done("Updated GitLab issue: %s", issue.Title)

	return issue, nil
}

func getGitLabClient(ctx context.Context) (*gitlab.Client, error) {
	token, ok := ctx.Value("token").(*oauth2.Token)
	if !ok {
		return nil, fmt.Errorf("unauthorized: missing GitLab token in context")
	}

	var saver client.TokenSaver
	if sessionID, ok := ctx.Value("session_id").(string); ok && strings.TrimSpace(sessionID) != "" {
		sessionID = strings.TrimSpace(sessionID)
		saver = func(ctx context.Context, t *oauth2.Token) error {
			if t != nil {
				*token = *t
			}
			return auth.UpdateSession(ctx, sessionID, t)
		}
	}

	return client.GetClient(ctx, token, saver)
}

// =============================================================================
// NEW TOOLS FOR CODE EXPLORATION
// =============================================================================

type ListRepoTreeArgs struct {
	ProjectID string `json:"projectId"`
	Path      string `json:"path"`
	Ref       string `json:"ref,omitempty"` // branch name, tag, or commit SHA. Defaults to project's default branch.
	Recursive bool   `json:"recursive"`
}

type RepoTreeNode struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"` // "tree" for directory, "blob" for file
	Path string `json:"path"`
}

type ListRepoTreeResponse struct {
	Nodes   []RepoTreeNode `json:"nodes"`
	Count   int            `json:"count"`
	Message string         `json:"message,omitempty"`
}

func listGitLabRepositoryTree(ctx context.Context, args ListRepoTreeArgs) (*ListRepoTreeResponse, error) {
	log.Printf("[AgentTool] listGitLabRepositoryTree called: project=%s, path=%s, ref=%s, recursive=%v", args.ProjectID, args.Path, args.Ref, args.Recursive)

	glClient, err := getGitLabClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get GitLab client: %w", err)
	}
	if err := ensureRepoMirrorForAgentTool(ctx, glClient, args.ProjectID); err != nil {
		return nil, err
	}

	// Resolve ref: use provided ref or fall back to project's default branch
	ref := args.Ref
	if ref == "" {
		project, _, err := glClient.Projects.GetProject(args.ProjectID, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to get project: %w", err)
		}
		ref = project.DefaultBranch
		if ref == "" {
			ref = "main"
		}
	}

	path := args.Path
	if path == "" {
		path = "."
	}

	if entries, cacheErr := services.DefaultRepoCache().ListTree(ctx, glClient, args.ProjectID, ref, path, args.Recursive); cacheErr == nil {
		result := make([]RepoTreeNode, 0, len(entries))
		for _, n := range entries {
			result = append(result, RepoTreeNode{ID: n.ID, Name: n.Name, Type: n.Type, Path: n.Path})
		}
		log.Printf("[AgentTool] listGitLabRepositoryTree cache hit: %d items at ref '%s'", len(result), ref)
		return &ListRepoTreeResponse{Nodes: result, Count: len(result)}, nil
	} else if !errors.Is(cacheErr, services.ErrRepoCacheDisabled) && !errors.Is(cacheErr, services.ErrRepoCacheToken) {
		log.Printf("[AgentTool] listGitLabRepositoryTree cache fallback: %v", cacheErr)
	}

	opt := &gitlab.ListTreeOptions{
		Path:      &path,
		Ref:       &ref,
		Recursive: &args.Recursive,
	}

	nodes, _, err := glClient.Repositories.ListTree(args.ProjectID, opt)
	if err != nil {
		log.Printf("[AgentTool] listGitLabRepositoryTree failed: %v", err)
		// Return gracefully instead of hard error so LLM doesn't get stuck
		return &ListRepoTreeResponse{
			Nodes:   []RepoTreeNode{},
			Count:   0,
			Message: fmt.Sprintf("Directory not found or access error at path '%s' (ref '%s'). Try another path like 'src/app', 'app', or '.' (root). Error: %v", path, ref, err),
		}, nil
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

	log.Printf("[AgentTool] listGitLabRepositoryTree found %d items at ref '%s'", len(result), ref)

	return &ListRepoTreeResponse{
		Nodes: result,
		Count: len(result),
	}, nil
}

type GetFileContentArgs struct {
	ProjectID string `json:"projectId"`
	FilePath  string `json:"filePath"`
	Ref       string `json:"ref,omitempty"` // branch or commit SHA
}

type FileContentResponse struct {
	FilePath string `json:"filePath"`
	Content  string `json:"content"`
	Size     int    `json:"size"`
	Encoding string `json:"encoding"`
	Message  string `json:"message,omitempty"`
}

func getGitLabFileContent(ctx context.Context, args GetFileContentArgs) (*FileContentResponse, error) {
	log.Printf("[AgentTool] getGitLabFileContent called: project=%s, file=%s", args.ProjectID, args.FilePath)

	glClient, err := getGitLabClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get GitLab client: %w", err)
	}
	if err := ensureRepoMirrorForAgentTool(ctx, glClient, args.ProjectID); err != nil {
		return nil, err
	}

	// Get project branch if not specified
	ref := args.Ref
	if ref == "" {
		project, _, err := glClient.Projects.GetProject(args.ProjectID, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to get project: %w", err)
		}
		ref = project.DefaultBranch
		if ref == "" {
			ref = "main"
		}
	}

	if content, meta, cacheErr := services.DefaultRepoCache().ReadFile(ctx, glClient, args.ProjectID, ref, args.FilePath); cacheErr == nil {
		log.Printf("[AgentTool] getGitLabFileContent cache hit: %s (%d bytes)", args.FilePath, meta.Size)
		return &FileContentResponse{
			FilePath: args.FilePath,
			Content:  content,
			Size:     int(meta.Size),
			Encoding: "text",
		}, nil
	} else if !errors.Is(cacheErr, services.ErrRepoCacheDisabled) && !errors.Is(cacheErr, services.ErrRepoCacheToken) {
		log.Printf("[AgentTool] getGitLabFileContent cache fallback: %v", cacheErr)
	}

	fileOpt := &gitlab.GetFileOptions{Ref: &ref}
	file, _, err := glClient.RepositoryFiles.GetFile(args.ProjectID, args.FilePath, fileOpt)
	if err != nil {
		log.Printf("[AgentTool] getGitLabFileContent failed: %v", err)
		return &FileContentResponse{
			FilePath: args.FilePath,
			Content:  "",
			Message:  fmt.Sprintf("File not found at '%s' (ref '%s'). Try using listGitLabRepositoryTree first to confirm the correct file path. Error: %v", args.FilePath, ref, err),
		}, nil
	}

	// Decode base64 content
	contentBytes, err := base64.StdEncoding.DecodeString(file.Content)
	if err != nil {
		return nil, fmt.Errorf("failed to decode file content: %w", err)
	}

	log.Printf("[AgentTool] getGitLabFileContent success: %s (%d bytes)", args.FilePath, len(contentBytes))

	return &FileContentResponse{
		FilePath: args.FilePath,
		Content:  string(contentBytes),
		Size:     int(file.Size),
		Encoding: file.Encoding,
	}, nil
}

type SearchCodeArgs struct {
	ProjectID string `json:"projectId"`
	Query     string `json:"query"`
	Path      string `json:"path,omitempty"`
	Ref       string `json:"ref,omitempty"` // branch name, tag, or commit SHA. Defaults to project's default branch.
}

type SearchResult struct {
	FilePath string `json:"filePath"`
	Ref      string `json:"ref"`
	Content  string `json:"content"`
}

type SearchCodeResponse struct {
	Results []SearchResult `json:"results"`
	Count   int            `json:"count"`
}

func searchGitLabCode(ctx context.Context, args SearchCodeArgs) (*SearchCodeResponse, error) {
	log.Printf("[AgentTool] searchGitLabCode called: project=%s, query=%s, ref=%s", args.ProjectID, args.Query, args.Ref)

	glClient, err := getGitLabClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get GitLab client: %w", err)
	}
	if err := ensureRepoMirrorForAgentTool(ctx, glClient, args.ProjectID); err != nil {
		return nil, err
	}

	// Resolve ref: use provided ref or fall back to project's default branch
	ref := args.Ref
	if ref == "" {
		project, _, err := glClient.Projects.GetProject(args.ProjectID, nil)
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

	if cached, cacheErr := services.DefaultRepoCache().Search(ctx, glClient, args.ProjectID, ref, args.Query, args.Path, 0); cacheErr == nil {
		searchResults := make([]SearchResult, 0, len(cached))
		for _, r := range cached {
			searchResults = append(searchResults, SearchResult{FilePath: r.FilePath, Ref: r.Ref, Content: r.Content})
		}
		log.Printf("[AgentTool] searchGitLabCode cache hit: %d results at ref '%s'", len(searchResults), ref)
		return &SearchCodeResponse{Results: searchResults, Count: len(searchResults)}, nil
	} else if !errors.Is(cacheErr, services.ErrRepoCacheDisabled) && !errors.Is(cacheErr, services.ErrRepoCacheToken) {
		log.Printf("[AgentTool] searchGitLabCode cache fallback: %v", cacheErr)
	}

	results, _, err := glClient.Search.BlobsByProject(args.ProjectID, args.Query, searchOpts)
	if err != nil {
		log.Printf("[AgentTool] searchGitLabCode failed: %v", err)
		return nil, fmt.Errorf("search failed at ref '%s': %w", ref, err)
	}

	var searchResults []SearchResult
	for _, r := range results {
		fileOpt := &gitlab.GetFileOptions{Ref: &ref}
		file, _, err := glClient.RepositoryFiles.GetFile(args.ProjectID, r.Filename, fileOpt)
		if err == nil {
			contentBytes, _ := base64.StdEncoding.DecodeString(file.Content)
			searchResults = append(searchResults, SearchResult{
				FilePath: r.Filename,
				Ref:      ref,
				Content:  string(contentBytes),
			})
		}
	}

	log.Printf("[AgentTool] searchGitLabCode found %d results at ref '%s'", len(searchResults), ref)

	return &SearchCodeResponse{
		Results: searchResults,
		Count:   len(searchResults),
	}, nil
}

// --- listGitLabBranches ---

type ListBranchesArgs struct {
	ProjectID string `json:"projectId"`
	Search    string `json:"search,omitempty"` // filter branches by name
}

type BranchInfo struct {
	Name               string `json:"name"`
	Protected          bool   `json:"protected"`
	Default            bool   `json:"default"`
	DevelopersCanPush  bool   `json:"developersCanPush"`
	DevelopersCanMerge bool   `json:"developersCanMerge"`
	WebURL             string `json:"webUrl"`
}

type ListBranchesResponse struct {
	Branches []BranchInfo `json:"branches"`
	Count    int          `json:"count"`
}

func listGitLabBranches(ctx context.Context, args ListBranchesArgs) (*ListBranchesResponse, error) {
	log.Printf("[AgentTool] listGitLabBranches called: project=%s, search=%s", args.ProjectID, args.Search)

	glClient, err := getGitLabClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get GitLab client: %w", err)
	}
	if err := ensureRepoMirrorForAgentTool(ctx, glClient, args.ProjectID); err != nil {
		return nil, err
	}

	opts := &gitlab.ListBranchesOptions{}
	if args.Search != "" {
		opts.Search = &args.Search
	}

	var allBranches []*gitlab.Branch
	for {
		branches, resp, err := glClient.Branches.ListBranches(args.ProjectID, opts)
		if err != nil {
			log.Printf("[AgentTool] listGitLabBranches failed: %v", err)
			return nil, fmt.Errorf("failed to list branches: %w", err)
		}
		allBranches = append(allBranches, branches...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	// Get project to identify the default branch
	project, _, projErr := glClient.Projects.GetProject(args.ProjectID, nil)
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

	log.Printf("[AgentTool] listGitLabBranches found %d branches", len(result))

	return &ListBranchesResponse{
		Branches: result,
		Count:    len(result),
	}, nil
}

// --- grepRepo ---

type GrepRepoArgs struct {
	ProjectID    string `json:"projectId"`
	Pattern      string `json:"pattern"`
	Path         string `json:"path,omitempty"`
	Ref          string `json:"ref,omitempty"`
	ContextLines int    `json:"contextLines,omitempty"`
	FixedString  bool   `json:"fixedString,omitempty"`
}

type GrepMatch struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Content string `json:"content"`
}

type GrepRepoResponse struct {
	Matches []GrepMatch `json:"matches"`
	Count   int         `json:"count"`
	Ref     string      `json:"ref"`
}

func grepRepo(ctx context.Context, args GrepRepoArgs) (*GrepRepoResponse, error) {
	log.Printf("[AgentTool] grepRepo called: project=%s, pattern=%q, path=%s, ref=%s", args.ProjectID, args.Pattern, args.Path, args.Ref)

	glClient, err := getGitLabClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get GitLab client: %w", err)
	}
	if err := ensureRepoMirrorForAgentTool(ctx, glClient, args.ProjectID); err != nil {
		return nil, err
	}

	ref := args.Ref
	if ref == "" {
		project, _, err := glClient.Projects.GetProject(args.ProjectID, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to get project: %w", err)
		}
		ref = project.DefaultBranch
		if ref == "" {
			ref = "main"
		}
	}

	contextLines := args.ContextLines
	if contextLines <= 0 {
		contextLines = 2
	}

	cached, cacheErr := services.DefaultRepoCache().GrepLines(ctx, glClient, args.ProjectID, ref, args.Pattern, args.Path, contextLines, args.FixedString)
	if cacheErr != nil {
		log.Printf("[AgentTool] grepRepo cache error: %v", cacheErr)
		return nil, fmt.Errorf("grep failed: %w", cacheErr)
	}

	matches := make([]GrepMatch, 0, len(cached))
	for _, m := range cached {
		matches = append(matches, GrepMatch{File: m.FilePath, Line: m.Line, Content: m.Content})
	}
	log.Printf("[AgentTool] grepRepo found %d lines at ref '%s'", len(matches), ref)
	return &GrepRepoResponse{Matches: matches, Count: len(matches), Ref: ref}, nil
}

// --- findFiles ---

type FindFilesArgs struct {
	ProjectID string `json:"projectId"`
	Pattern   string `json:"pattern"`
	Ref       string `json:"ref,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type FindFilesResponse struct {
	Files []string `json:"files"`
	Count int      `json:"count"`
	Ref   string   `json:"ref"`
}

func findFiles(ctx context.Context, args FindFilesArgs) (*FindFilesResponse, error) {
	log.Printf("[AgentTool] findFiles called: project=%s, pattern=%q, ref=%s", args.ProjectID, args.Pattern, args.Ref)

	glClient, err := getGitLabClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get GitLab client: %w", err)
	}
	if err := ensureRepoMirrorForAgentTool(ctx, glClient, args.ProjectID); err != nil {
		return nil, err
	}

	ref := args.Ref
	if ref == "" {
		project, _, err := glClient.Projects.GetProject(args.ProjectID, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to get project: %w", err)
		}
		ref = project.DefaultBranch
		if ref == "" {
			ref = "main"
		}
	}

	limit := args.Limit
	if limit <= 0 {
		limit = 50
	}

	allFiles, cacheErr := services.DefaultRepoCache().ListFiles(ctx, glClient, args.ProjectID, ref)
	if cacheErr != nil {
		log.Printf("[AgentTool] findFiles cache error: %v", cacheErr)
		return nil, fmt.Errorf("could not list files: %w", cacheErr)
	}

	pattern := strings.ToLower(args.Pattern)
	var matched []string
	for _, f := range allFiles {
		lf := strings.ToLower(f)
		ok := false
		if strings.ContainsAny(pattern, "*?[") {
			ok, _ = filepath.Match(pattern, filepath.Base(lf))
			if !ok {
				ok, _ = filepath.Match(pattern, lf)
			}
		} else {
			ok = strings.Contains(lf, pattern)
		}
		if ok {
			matched = append(matched, f)
			if len(matched) >= limit {
				break
			}
		}
	}

	log.Printf("[AgentTool] findFiles found %d files matching %q at ref '%s'", len(matched), args.Pattern, ref)
	return &FindFilesResponse{Files: matched, Count: len(matched), Ref: ref}, nil
}
