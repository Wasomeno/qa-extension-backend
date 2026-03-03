package agent

import (
	"context"
	"fmt"
	"qa-extension-backend/client"
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
		Description: "List all available projects.",
	}, listGitLabProjects)
	tools = append(tools, t1)

	t2, _ := functiontool.New(functiontool.Config{
		Name: "createGitLabIssue",
		Description: "Create a new issue in GitLab.",
	}, createGitLabIssue)
	tools = append(tools, t2)

	t3, _ := functiontool.New(functiontool.Config{
		Name: "listGitLabIssues",
		Description: "List issues from a GitLab project.",
	}, listGitLabIssues)
	tools = append(tools, t3)

	t4, _ := functiontool.New(functiontool.Config{
		Name: "updateGitLabIssue",
		Description: "Update an existing issue in GitLab.",
	}, updateGitLabIssue)
	tools = append(tools, t4)

	return tools
}

type ListProjectsArgs struct{}

func listGitLabProjects(ctx tool.Context, args ListProjectsArgs) ([]*gitlab.Project, error) {
	gitlabClient, err := getGitLabClient(ctx)
	if err != nil {
		return nil, err
	}

	projects, _, err := gitlabClient.Projects.ListProjects(&gitlab.ListProjectsOptions{
		Membership: gitlab.Ptr(true),
		Simple:     gitlab.Ptr(true),
	})
	return projects, err
}

type CreateIssueArgs struct {
	ProjectID   int      `json:"projectId"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Labels      []string `json:"labels"`
}

func createGitLabIssue(ctx tool.Context, args CreateIssueArgs) (*gitlab.Issue, error) {
	gitlabClient, err := getGitLabClient(ctx)
	if err != nil {
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
	return issue, err
}

type ListIssuesArgs struct {
	ProjectID int    `json:"projectId"`
	State     string `json:"state"`
}

func listGitLabIssues(ctx tool.Context, args ListIssuesArgs) ([]*gitlab.Issue, error) {
	gitlabClient, err := getGitLabClient(ctx)
	if err != nil {
		return nil, err
	}

	opt := &gitlab.ListProjectIssuesOptions{
		Scope: gitlab.Ptr("all"),
	}
	if args.State != "" {
		opt.State = &args.State
	}

	issues, _, err := gitlabClient.Issues.ListProjectIssues(strconv.Itoa(args.ProjectID), opt)
	return issues, err
}

type UpdateIssueArgs struct {
	ProjectID int            `json:"projectId"`
	IssueIID  int            `json:"issueIid"`
	Updates   map[string]any `json:"updates"`
}

func updateGitLabIssue(ctx tool.Context, args UpdateIssueArgs) (*gitlab.Issue, error) {
	gitlabClient, err := getGitLabClient(ctx)
	if err != nil {
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
	return issue, err
}

func getGitLabClient(ctx context.Context) (*gitlab.Client, error) {
	token, ok := ctx.Value("token").(*oauth2.Token)
	if !ok {
		return nil, fmt.Errorf("unauthorized: missing GitLab token in context")
	}

	return client.GetClient(ctx, token, nil)
}
