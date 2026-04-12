package agent

import (
	"context"
	"fmt"
	"log"
	"qa-extension-backend/client"
	"qa-extension-backend/database"
	"strconv"

	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	gitlab "gitlab.com/gitlab-org/api/client-go"
	"golang.org/x/oauth2"
)

func GetGitLabTools() []tool.Tool {
	tools := []tool.Tool{}
	
	t1, _ := functiontool.New(functiontool.Config{
		Name: "listGitLabProjects",
		Description: "List available GitLab projects. Call this without arguments to see all projects you have access to.",
	}, listGitLabProjects)
	tools = append(tools, t1)

	t2, _ := functiontool.New(functiontool.Config{
		Name: "createGitLabIssue",
		Description: "Create a new issue in GitLab.",
	}, createGitLabIssue)
	tools = append(tools, t2)

	t3, _ := functiontool.New(functiontool.Config{
		Name: "listGitLabIssues",
		Description: "List issues from a specific GitLab project. Requires projectId.",
	}, listGitLabIssues)
	tools = append(tools, t3)

	t5, _ := functiontool.New(functiontool.Config{
		Name: "listAllGitLabIssues",
		Description: "List all issues assigned to you or created by you across all projects.",
	}, listAllGitLabIssues)
	tools = append(tools, t5)

	t4, _ := functiontool.New(functiontool.Config{
		Name: "updateGitLabIssue",
		Description: "Update an existing issue in GitLab.",
	}, updateGitLabIssue)
	tools = append(tools, t4)

	return tools
}

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

func listGitLabProjects(ctx tool.Context, args ListProjectsArgs) (*ListProjectsResponse, error) {
	log.Printf("[AgentTool] listGitLabProjects called with args: %+v", args)

	database.PublishStreamEvent(ctx, database.StreamEvent{
		Type:    "agent",
		Stage:   "start",
		Message: "Fetching GitLab projects...",
	})

	gitlabClient, err := getGitLabClient(ctx)
	if err != nil {
		log.Printf("[AgentTool] listGitLabProjects failed to get client: %v", err)
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

	database.PublishStreamEvent(ctx, database.StreamEvent{
		Type:    "agent",
		Stage:   "done",
		Message: fmt.Sprintf("Loaded %d GitLab projects", len(result)),
	})

	return &ListProjectsResponse{Projects: result}, nil
}

type CreateIssueArgs struct {
	ProjectID   int      `json:"projectId"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Labels      []string `json:"labels"`
}

func createGitLabIssue(ctx tool.Context, args CreateIssueArgs) (*gitlab.Issue, error) {
	log.Printf("[AgentTool] createGitLabIssue called with args: %+v", args)

	database.PublishStreamEvent(ctx, database.StreamEvent{
		Type:    "agent",
		Stage:   "start",
		Message: "Creating GitLab issue...",
	})

	gitlabClient, err := getGitLabClient(ctx)
	if err != nil {
		log.Printf("[AgentTool] createGitLabIssue failed to get client: %v", err)
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
		database.PublishStreamEvent(ctx, database.StreamEvent{
			Type:    "agent",
			Stage:   "error",
			Message: "Failed to create GitLab issue",
		})
	} else {
		log.Printf("[AgentTool] createGitLabIssue success, created issue IID: %d", issue.IID)
		database.PublishStreamEvent(ctx, database.StreamEvent{
			Type:        "agent",
			Stage:       "done",
			Message:     fmt.Sprintf("Created GitLab issue: %s", issue.Title),
		})
	}
	return issue, err
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

func listGitLabIssues(ctx tool.Context, args ListIssuesArgs) (*ListIssuesResponse, error) {
	log.Printf("[AgentTool] listGitLabIssues called with args: %+v", args)

	database.PublishStreamEvent(ctx, database.StreamEvent{
		Type:    "agent",
		Stage:   "start",
		Message: "Fetching project GitLab issues...",
	})

	gitlabClient, err := getGitLabClient(ctx)
	if err != nil {
		log.Printf("[AgentTool] listGitLabIssues failed to get client: %v", err)
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

	database.PublishStreamEvent(ctx, database.StreamEvent{
		Type:    "agent",
		Stage:   "done",
		Message: fmt.Sprintf("Loaded %d project issues", len(result)),
	})

	return &ListIssuesResponse{Issues: result}, nil
}

type ListAllIssuesArgs struct {
	State string `json:"state"`
}

func listAllGitLabIssues(ctx tool.Context, args ListAllIssuesArgs) (*ListIssuesResponse, error) {
	log.Printf("[AgentTool] listAllGitLabIssues called with args: %+v", args)

	database.PublishStreamEvent(ctx, database.StreamEvent{
		Type:    "agent",
		Stage:   "start",
		Message: "Fetching all GitLab issues...",
	})

	gitlabClient, err := getGitLabClient(ctx)
	if err != nil {
		log.Printf("[AgentTool] listAllGitLabIssues failed to get client: %v", err)
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

	database.PublishStreamEvent(ctx, database.StreamEvent{
		Type:    "agent",
		Stage:   "done",
		Message: fmt.Sprintf("Loaded %d GitLab issues", len(result)),
	})

	return &ListIssuesResponse{Issues: result}, nil
}

type UpdateIssueArgs struct {
	ProjectID int            `json:"projectId"`
	IssueIID  int            `json:"issueIid"`
	Updates   map[string]any `json:"updates"`
}

func updateGitLabIssue(ctx tool.Context, args UpdateIssueArgs) (*gitlab.Issue, error) {
	log.Printf("[AgentTool] updateGitLabIssue called with args: %+v", args)

	database.PublishStreamEvent(ctx, database.StreamEvent{
		Type:    "agent",
		Stage:   "start",
		Message: "Updating GitLab issue...",
	})

	gitlabClient, err := getGitLabClient(ctx)
	if err != nil {
		log.Printf("[AgentTool] updateGitLabIssue failed to get client: %v", err)
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
		database.PublishStreamEvent(ctx, database.StreamEvent{
			Type:    "agent",
			Stage:   "error",
			Message: "Failed to update GitLab issue",
		})
	} else {
		log.Printf("[AgentTool] updateGitLabIssue success, updated issue IID: %d", issue.IID)
		database.PublishStreamEvent(ctx, database.StreamEvent{
			Type:    "agent",
			Stage:   "done",
			Message: "Updated GitLab issue",
		})
	}
	return issue, err
}

func getGitLabClient(ctx context.Context) (*gitlab.Client, error) {
	token, ok := ctx.Value("token").(*oauth2.Token)
	if !ok {
		return nil, fmt.Errorf("unauthorized: missing GitLab token in context")
	}

	return client.GetClient(ctx, token, nil)
}
