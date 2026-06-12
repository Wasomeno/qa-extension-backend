package agent

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	"qa-extension-backend/internal/models"
	"qa-extension-backend/services"

	gitlab "gitlab.com/gitlab-org/api/client-go"
)

type AppProjectToolResponse struct {
	Project *models.AppProject `json:"project"`
}

type ListAppProjectsResponse struct {
	Projects []models.AppProject `json:"projects"`
}

func listAppProjects(ctx context.Context, _ struct{}) (*ListAppProjectsResponse, error) {
	log.Printf("[AgentTool] listAppProjects called")
	events := NewAgentToolEmitter(ctx)
	events.Start("Loading QA app projects...")

	projects, err := services.ListAppProjects(ctx)
	if err != nil {
		events.Error("Failed to list app projects: " + err.Error())
		return nil, err
	}

	events.Done("Loaded %d app projects", len(projects))
	return &ListAppProjectsResponse{Projects: projects}, nil
}

type AppProjectIDArgs struct {
	AppProjectID string `json:"appProjectId"`
}

func getAppProject(ctx context.Context, args AppProjectIDArgs) (*AppProjectToolResponse, error) {
	log.Printf("[AgentTool] getAppProject called with args: %+v", args)
	events := NewAgentToolEmitter(ctx)
	events.Start("Loading app project...")

	project, err := loadAppProject(ctx, args.AppProjectID)
	if err != nil {
		events.Error("Failed to load app project: " + err.Error())
		return nil, err
	}

	events.Done("Loaded app project: %s", project.Name)
	return &AppProjectToolResponse{Project: project}, nil
}

type ProjectTestContextResponse struct {
	AppProjectID string `json:"appProjectId"`
	Markdown     string `json:"markdown"`
	Template     string `json:"template"`
	MaxBytes     int    `json:"maxBytes"`
}

func getProjectTestContext(ctx context.Context, args AppProjectIDArgs) (*ProjectTestContextResponse, error) {
	log.Printf("[AgentTool] getProjectTestContext called with args: %+v", args)
	events := NewAgentToolEmitter(ctx)
	events.Start("Loading project test context...")

	project, err := loadAppProject(ctx, args.AppProjectID)
	if err != nil {
		events.Error("Failed to load project test context: " + err.Error())
		return nil, err
	}

	events.Done("Loaded project test context")
	return &ProjectTestContextResponse{
		AppProjectID: project.ID,
		Markdown:     project.TestContextMarkdown,
		Template:     services.ProjectTestContextTemplate(),
		MaxBytes:     services.MaxProjectTestContextBytes,
	}, nil
}

type UpdateProjectTestContextArgs struct {
	AppProjectID string `json:"appProjectId"`
	Markdown     string `json:"markdown"`
}

func updateProjectTestContext(ctx context.Context, args UpdateProjectTestContextArgs) (*ProjectTestContextResponse, error) {
	log.Printf("[AgentTool] updateProjectTestContext called with appProjectId=%s markdownBytes=%d", args.AppProjectID, len(args.Markdown))
	events := NewAgentToolEmitter(ctx)
	events.Start("Updating project test context...")

	if strings.TrimSpace(args.AppProjectID) == "" {
		return nil, fmt.Errorf("appProjectId is required")
	}
	project, err := services.UpdateProjectTestContext(ctx, args.AppProjectID, args.Markdown, 0)
	if err != nil {
		events.Error("Failed to update project test context: " + err.Error())
		return nil, err
	}

	events.Done("Updated project test context")
	return &ProjectTestContextResponse{
		AppProjectID: project.ID,
		Markdown:     project.TestContextMarkdown,
		Template:     services.ProjectTestContextTemplate(),
		MaxBytes:     services.MaxProjectTestContextBytes,
	}, nil
}

type SpecsTreeArgs struct {
	AppProjectID string `json:"appProjectId"`
	Path         string `json:"path"`
	Ref          string `json:"ref"`
	Recursive    bool   `json:"recursive"`
}

type SpecsTreeResponse struct {
	AppProjectID string                   `json:"appProjectId"`
	SpecsRepoID  int64                    `json:"specsRepoId"`
	Tree         []*services.FileTreeNode `json:"tree"`
}

func listSpecsTree(ctx context.Context, args SpecsTreeArgs) (*SpecsTreeResponse, error) {
	log.Printf("[AgentTool] listSpecsTree called with args: %+v", args)
	events := NewAgentToolEmitter(ctx)
	events.Start("Listing specs tree...")

	project, glClient, err := loadAppProjectAndGitLab(ctx, args.AppProjectID)
	if err != nil {
		events.Error("Failed to list specs tree: " + err.Error())
		return nil, err
	}

	tree, err := services.NewSpecsService().GetFileTree(glClient, strconv.FormatInt(project.SpecsRepoID, 10), args.Path, args.Ref, args.Recursive)
	if err != nil {
		events.Error("Failed to list specs tree: " + err.Error())
		return nil, err
	}

	events.Done("Loaded %d specs tree items", len(tree))
	return &SpecsTreeResponse{AppProjectID: project.ID, SpecsRepoID: project.SpecsRepoID, Tree: tree}, nil
}

type SpecsFileArgs struct {
	AppProjectID string `json:"appProjectId"`
	Path         string `json:"path"`
	Ref          string `json:"ref"`
}

type SpecsFileResponse struct {
	AppProjectID string                `json:"appProjectId"`
	SpecsRepoID  int64                 `json:"specsRepoId"`
	File         *services.FileContent `json:"file"`
}

func getSpecsFile(ctx context.Context, args SpecsFileArgs) (*SpecsFileResponse, error) {
	log.Printf("[AgentTool] getSpecsFile called with args: %+v", args)
	events := NewAgentToolEmitter(ctx)
	events.Start("Reading specs file...")

	if strings.TrimSpace(args.Path) == "" {
		return nil, fmt.Errorf("path is required")
	}
	project, glClient, err := loadAppProjectAndGitLab(ctx, args.AppProjectID)
	if err != nil {
		events.Error("Failed to read specs file: " + err.Error())
		return nil, err
	}

	file, err := services.NewSpecsService().GetFile(glClient, strconv.FormatInt(project.SpecsRepoID, 10), args.Path, args.Ref)
	if err != nil {
		events.Error("Failed to read specs file: " + err.Error())
		return nil, err
	}

	events.Done("Read specs file: %s", file.Path)
	return &SpecsFileResponse{AppProjectID: project.ID, SpecsRepoID: project.SpecsRepoID, File: file}, nil
}

type SearchSpecsArgs struct {
	AppProjectID string `json:"appProjectId"`
	Query        string `json:"query"`
	Path         string `json:"path"`
	Ref          string `json:"ref"`
}

type SearchSpecsResponse struct {
	AppProjectID string                   `json:"appProjectId"`
	SpecsRepoID  int64                    `json:"specsRepoId"`
	Results      []*services.FileTreeNode `json:"results"`
}

func searchSpecs(ctx context.Context, args SearchSpecsArgs) (*SearchSpecsResponse, error) {
	log.Printf("[AgentTool] searchSpecs called with args: %+v", args)
	events := NewAgentToolEmitter(ctx)
	events.Start("Searching specs...")

	if strings.TrimSpace(args.Query) == "" {
		return nil, fmt.Errorf("query is required")
	}
	project, glClient, err := loadAppProjectAndGitLab(ctx, args.AppProjectID)
	if err != nil {
		events.Error("Failed to search specs: " + err.Error())
		return nil, err
	}

	results, err := services.NewSpecsService().SearchTree(glClient, strconv.FormatInt(project.SpecsRepoID, 10), args.Path, args.Ref, args.Query)
	if err != nil {
		events.Error("Failed to search specs: " + err.Error())
		return nil, err
	}

	events.Done("Found %d specs results", len(results))
	return &SearchSpecsResponse{AppProjectID: project.ID, SpecsRepoID: project.SpecsRepoID, Results: results}, nil
}

type SpecsFileBlameResponse struct {
	AppProjectID string `json:"appProjectId"`
	SpecsRepoID  int64  `json:"specsRepoId"`
	Blame        any    `json:"blame"`
}

func getSpecsFileBlame(ctx context.Context, args SpecsFileArgs) (*SpecsFileBlameResponse, error) {
	log.Printf("[AgentTool] getSpecsFileBlame called with args: %+v", args)
	events := NewAgentToolEmitter(ctx)
	events.Start("Loading specs file blame...")

	if strings.TrimSpace(args.Path) == "" {
		return nil, fmt.Errorf("path is required")
	}
	project, glClient, err := loadAppProjectAndGitLab(ctx, args.AppProjectID)
	if err != nil {
		events.Error("Failed to load specs file blame: " + err.Error())
		return nil, err
	}

	blame, err := services.NewSpecsService().GetFileBlame(glClient, strconv.FormatInt(project.SpecsRepoID, 10), args.Path, args.Ref)
	if err != nil {
		events.Error("Failed to load specs file blame: " + err.Error())
		return nil, err
	}

	events.Done("Loaded specs file blame")
	return &SpecsFileBlameResponse{AppProjectID: project.ID, SpecsRepoID: project.SpecsRepoID, Blame: blame}, nil
}

type SpecsCommitsArgs struct {
	AppProjectID string `json:"appProjectId"`
	Path         string `json:"path"`
	Ref          string `json:"ref"`
	PerPage      int    `json:"perPage"`
	Page         int    `json:"page"`
}

type SpecsCommitsResponse struct {
	AppProjectID string                `json:"appProjectId"`
	SpecsRepoID  int64                 `json:"specsRepoId"`
	Commits      []services.SpecCommit `json:"commits"`
}

func listSpecsCommits(ctx context.Context, args SpecsCommitsArgs) (*SpecsCommitsResponse, error) {
	log.Printf("[AgentTool] listSpecsCommits called with args: %+v", args)
	events := NewAgentToolEmitter(ctx)
	events.Start("Loading specs commits...")

	project, glClient, err := loadAppProjectAndGitLab(ctx, args.AppProjectID)
	if err != nil {
		events.Error("Failed to load specs commits: " + err.Error())
		return nil, err
	}

	commits, err := services.NewSpecsService().GetCommits(glClient, strconv.FormatInt(project.SpecsRepoID, 10), args.Path, args.Ref, args.PerPage, args.Page)
	if err != nil {
		events.Error("Failed to load specs commits: " + err.Error())
		return nil, err
	}

	events.Done("Loaded %d specs commits", len(commits))
	return &SpecsCommitsResponse{AppProjectID: project.ID, SpecsRepoID: project.SpecsRepoID, Commits: commits}, nil
}

type CommitSpecsFilesArgs struct {
	AppProjectID  string                `json:"appProjectId"`
	Branch        string                `json:"branch"`
	CommitMessage string                `json:"commitMessage"`
	Actions       []services.FileAction `json:"actions"`
	AuthorName    string                `json:"authorName"`
	AuthorEmail   string                `json:"authorEmail"`
}

type CommitSpecsFilesResponse struct {
	AppProjectID string               `json:"appProjectId"`
	SpecsRepoID  int64                `json:"specsRepoId"`
	Commit       *services.SpecCommit `json:"commit"`
}

func commitSpecsFiles(ctx context.Context, args CommitSpecsFilesArgs) (*CommitSpecsFilesResponse, error) {
	log.Printf("[AgentTool] commitSpecsFiles called with appProjectId=%s branch=%s actions=%d", args.AppProjectID, args.Branch, len(args.Actions))
	events := NewAgentToolEmitter(ctx)
	events.Start("Committing specs files...")

	if strings.TrimSpace(args.CommitMessage) == "" {
		return nil, fmt.Errorf("commitMessage is required")
	}
	if len(args.Actions) == 0 {
		return nil, fmt.Errorf("at least one file action is required")
	}
	project, glClient, err := loadAppProjectAndGitLab(ctx, args.AppProjectID)
	if err != nil {
		events.Error("Failed to commit specs files: " + err.Error())
		return nil, err
	}

	commit, err := services.NewSpecsService().CommitFiles(glClient, strconv.FormatInt(project.SpecsRepoID, 10), args.Branch, args.CommitMessage, args.Actions, args.AuthorName, args.AuthorEmail)
	if err != nil {
		events.Error("Failed to commit specs files: " + err.Error())
		return nil, err
	}

	events.Done("Committed specs changes: %s", commit.ShortHash)
	return &CommitSpecsFilesResponse{AppProjectID: project.ID, SpecsRepoID: project.SpecsRepoID, Commit: commit}, nil
}

type KnowledgeGraphArgs struct {
	AppProjectID string `json:"appProjectId"`
	Branch       string `json:"branch"`
	ForceRefresh bool   `json:"forceRefresh"`
}

type KnowledgeGraphResponse struct {
	AppProjectID string                  `json:"appProjectId"`
	IssueRepoID  int64                   `json:"issueRepoId"`
	Branch       string                  `json:"branch"`
	FromCache    bool                    `json:"fromCache"`
	Catalog      *services.ModuleCatalog `json:"catalog"`
}

func getKnowledgeGraph(ctx context.Context, args KnowledgeGraphArgs) (*KnowledgeGraphResponse, error) {
	log.Printf("[AgentTool] getKnowledgeGraph called with args: %+v", args)
	events := NewAgentToolEmitter(ctx)
	events.Start("Loading knowledge graph...")

	project, glClient, branch, err := loadAppProjectGitLabAndBranch(ctx, args.AppProjectID, args.Branch)
	if err != nil {
		events.Error("Failed to load knowledge graph: " + err.Error())
		return nil, err
	}

	graphMapper := services.NewGraphMapper()
	var catalog *services.ModuleCatalog
	fromCache := false
	if !args.ForceRefresh {
		catalog, err = graphMapper.GetCachedCatalog(ctx, strconv.FormatInt(project.IssueRepoID, 10), branch)
		if err != nil {
			events.Error("Failed to check knowledge graph cache: " + err.Error())
			return nil, err
		}
		fromCache = catalog != nil
	}
	if catalog == nil {
		catalog, err = graphMapper.FetchAndEnrichCatalog(ctx, glClient, strconv.FormatInt(project.IssueRepoID, 10), branch)
		if err != nil {
			events.Error("Failed to generate knowledge graph: " + err.Error())
			return nil, err
		}
	}

	events.Done("Loaded knowledge graph")
	return &KnowledgeGraphResponse{AppProjectID: project.ID, IssueRepoID: project.IssueRepoID, Branch: branch, FromCache: fromCache, Catalog: catalog}, nil
}

type ListKnowledgeGraphsResponse struct {
	AppProjectID string                   `json:"appProjectId"`
	IssueRepoID  int64                    `json:"issueRepoId"`
	Catalogs     []services.ModuleCatalog `json:"catalogs"`
	Count        int                      `json:"count"`
}

func listKnowledgeGraphs(ctx context.Context, args AppProjectIDArgs) (*ListKnowledgeGraphsResponse, error) {
	log.Printf("[AgentTool] listKnowledgeGraphs called with args: %+v", args)
	events := NewAgentToolEmitter(ctx)
	events.Start("Listing knowledge graphs...")

	project, err := loadAppProject(ctx, args.AppProjectID)
	if err != nil {
		events.Error("Failed to list knowledge graphs: " + err.Error())
		return nil, err
	}
	catalogs, err := services.NewGraphMapper().ListCachedCatalogs(ctx, strconv.FormatInt(project.IssueRepoID, 10))
	if err != nil {
		events.Error("Failed to list knowledge graphs: " + err.Error())
		return nil, err
	}

	events.Done("Loaded %d knowledge graphs", len(catalogs))
	return &ListKnowledgeGraphsResponse{AppProjectID: project.ID, IssueRepoID: project.IssueRepoID, Catalogs: catalogs, Count: len(catalogs)}, nil
}

type KnowledgeGraphCoverageResponse struct {
	AppProjectID string `json:"appProjectId"`
	IssueRepoID  int64  `json:"issueRepoId"`
	Branch       string `json:"branch"`
	Coverage     any    `json:"coverage"`
}

func getKnowledgeGraphCoverage(ctx context.Context, args KnowledgeGraphArgs) (*KnowledgeGraphCoverageResponse, error) {
	log.Printf("[AgentTool] getKnowledgeGraphCoverage called with args: %+v", args)
	events := NewAgentToolEmitter(ctx)
	events.Start("Loading knowledge graph coverage...")

	project, glClient, branch, err := loadAppProjectGitLabAndBranch(ctx, args.AppProjectID, args.Branch)
	if err != nil {
		events.Error("Failed to load knowledge graph coverage: " + err.Error())
		return nil, err
	}

	graphMapper := services.NewGraphMapper()
	catalog, err := graphMapper.GetCachedCatalog(ctx, strconv.FormatInt(project.IssueRepoID, 10), branch)
	if err != nil {
		events.Error("Failed to load knowledge graph cache: " + err.Error())
		return nil, err
	}
	if catalog == nil {
		return nil, fmt.Errorf("no knowledge graph found for appProjectId %s branch %s; call getKnowledgeGraph first", project.ID, branch)
	}

	files, err := graphMapper.FetchFileTree(glClient, strconv.FormatInt(project.IssueRepoID, 10), branch)
	if err != nil {
		events.Error("Failed to fetch route files: " + err.Error())
		return nil, err
	}
	routeMap := services.NewRouteMapper().BuildRouteMapFromFileList(files)
	coverage := graphMapper.GenerateCoverageReport(routeMap, catalog, catalog.Selectors, 1, 0)

	events.Done("Loaded knowledge graph coverage")
	return &KnowledgeGraphCoverageResponse{AppProjectID: project.ID, IssueRepoID: project.IssueRepoID, Branch: branch, Coverage: coverage}, nil
}

type IssueIIDArgs struct {
	ProjectID int `json:"projectId"`
	IssueIID  int `json:"issueIid"`
}

type CreateIssueCommentArgs struct {
	ProjectID int    `json:"projectId"`
	IssueIID  int    `json:"issueIid"`
	Body      string `json:"body"`
}

func getIssueComments(ctx context.Context, args IssueIIDArgs) ([]*gitlab.Note, error) {
	log.Printf("[AgentTool] getIssueComments called with args: %+v", args)
	events := NewAgentToolEmitter(ctx)
	events.Start("Loading issue comments...")

	glClient, err := getGitLabClient(ctx)
	if err != nil {
		events.Error("Failed to load issue comments: " + err.Error())
		return nil, err
	}
	orderBy := "created_at"
	sort := "desc"
	notes, _, err := glClient.Notes.ListIssueNotes(strconv.Itoa(args.ProjectID), int64(args.IssueIID), &gitlab.ListIssueNotesOptions{OrderBy: &orderBy, Sort: &sort})
	if err != nil {
		events.Error("Failed to load issue comments: " + err.Error())
		return nil, err
	}

	events.Done("Loaded %d issue comments", len(notes))
	return notes, nil
}

func createIssueComment(ctx context.Context, args CreateIssueCommentArgs) (*gitlab.Note, error) {
	log.Printf("[AgentTool] createIssueComment called with projectId=%d issueIid=%d bodyBytes=%d", args.ProjectID, args.IssueIID, len(args.Body))
	events := NewAgentToolEmitter(ctx)
	events.Start("Creating issue comment...")

	if strings.TrimSpace(args.Body) == "" {
		return nil, fmt.Errorf("body is required")
	}
	glClient, err := getGitLabClient(ctx)
	if err != nil {
		events.Error("Failed to create issue comment: " + err.Error())
		return nil, err
	}

	note, _, err := glClient.Notes.CreateIssueNote(strconv.Itoa(args.ProjectID), int64(args.IssueIID), &gitlab.CreateIssueNoteOptions{Body: gitlab.Ptr(args.Body)})
	if err != nil {
		events.Error("Failed to create issue comment: " + err.Error())
		return nil, err
	}

	events.Done("Created issue comment")
	return note, nil
}

type CreateIssueEvidenceArgs struct {
	ProjectID int    `json:"projectId"`
	IssueIID  int    `json:"issueIid"`
	Comment   string `json:"comment"`
	Evidence  string `json:"evidence"`
}

func createIssueEvidence(ctx context.Context, args CreateIssueEvidenceArgs) (*gitlab.Note, error) {
	log.Printf("[AgentTool] createIssueEvidence called with projectId=%d issueIid=%d evidenceBytes=%d", args.ProjectID, args.IssueIID, len(args.Evidence))
	events := NewAgentToolEmitter(ctx)
	events.Start("Creating issue evidence...")

	if strings.TrimSpace(args.Evidence) == "" {
		return nil, fmt.Errorf("evidence is required")
	}
	body := "**Evidence:**\n" + args.Evidence + "\n\n" + args.Comment
	glClient, err := getGitLabClient(ctx)
	if err != nil {
		events.Error("Failed to create issue evidence: " + err.Error())
		return nil, err
	}

	note, _, err := glClient.Notes.CreateIssueNote(strconv.Itoa(args.ProjectID), int64(args.IssueIID), &gitlab.CreateIssueNoteOptions{Body: gitlab.Ptr(body)})
	if err != nil {
		events.Error("Failed to create issue evidence: " + err.Error())
		return nil, err
	}

	events.Done("Created issue evidence")
	return note, nil
}

func getIssueLinks(ctx context.Context, args IssueIIDArgs) ([]*gitlab.IssueRelation, error) {
	log.Printf("[AgentTool] getIssueLinks called with args: %+v", args)
	events := NewAgentToolEmitter(ctx)
	events.Start("Loading issue links...")

	glClient, err := getGitLabClient(ctx)
	if err != nil {
		events.Error("Failed to load issue links: " + err.Error())
		return nil, err
	}
	links, _, err := glClient.IssueLinks.ListIssueRelations(strconv.Itoa(args.ProjectID), int64(args.IssueIID), nil)
	if err != nil {
		events.Error("Failed to load issue links: " + err.Error())
		return nil, err
	}

	events.Done("Loaded %d issue links", len(links))
	return links, nil
}

func loadAppProject(ctx context.Context, appProjectID string) (*models.AppProject, error) {
	if strings.TrimSpace(appProjectID) == "" {
		return nil, fmt.Errorf("appProjectId is required")
	}
	return services.GetAppProject(ctx, appProjectID)
}

func loadAppProjectAndGitLab(ctx context.Context, appProjectID string) (*models.AppProject, *gitlab.Client, error) {
	project, err := loadAppProject(ctx, appProjectID)
	if err != nil {
		return nil, nil, err
	}
	glClient, err := getGitLabClient(ctx)
	if err != nil {
		return nil, nil, err
	}
	return project, glClient, nil
}

func loadAppProjectGitLabAndBranch(ctx context.Context, appProjectID, branch string) (*models.AppProject, *gitlab.Client, string, error) {
	project, glClient, err := loadAppProjectAndGitLab(ctx, appProjectID)
	if err != nil {
		return nil, nil, "", err
	}
	if branch == "" {
		gitlabProject, _, err := glClient.Projects.GetProject(strconv.FormatInt(project.IssueRepoID, 10), nil)
		if err != nil {
			return nil, nil, "", fmt.Errorf("failed to resolve default branch: %w", err)
		}
		branch = gitlabProject.DefaultBranch
	}
	if branch == "" {
		branch = "main"
	}
	return project, glClient, branch, nil
}
