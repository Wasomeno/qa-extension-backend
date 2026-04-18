package agent

import (
	"context"
	"fmt"
	"log"
	"qa-extension-backend/client"

	gitlab "gitlab.com/gitlab-org/api/client-go"
	"golang.org/x/oauth2"
)

// CreateBranch creates a new branch in a GitLab project
func CreateBranch(ctx context.Context, projectID interface{}, branchName, refBranch string) (*gitlab.Branch, error) {
	log.Printf("[GitLabWrite] Creating branch %s from %s in project %v", branchName, refBranch, projectID)

	token, ok := ctx.Value("token").(*oauth2.Token)
	if !ok {
		return nil, fmt.Errorf("no GitLab token in context")
	}

	gitlabClient, err := client.GetClient(ctx, token, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get GitLab client: %w", err)
	}

	opts := &gitlab.CreateBranchOptions{
		Branch: gitlab.Ptr(branchName),
		Ref:    gitlab.Ptr(refBranch),
	}

	branch, _, err := gitlabClient.Branches.CreateBranch(projectID, opts)
	if err != nil {
		log.Printf("[GitLabWrite] Failed to create branch: %v", err)
		return nil, err
	}

	log.Printf("[GitLabWrite] Branch created: %s (commit: %s)", branch.Name, branch.Commit.ID)
	return branch, nil
}

// CreateMergeRequest creates a new merge request in a GitLab project
func CreateMergeRequest(ctx context.Context, projectID interface{}, sourceBranch, targetBranch, title, description string) (*gitlab.MergeRequest, error) {
	log.Printf("[GitLabWrite] Creating MR from %s to %s in project %v", sourceBranch, targetBranch, projectID)

	token, ok := ctx.Value("token").(*oauth2.Token)
	if !ok {
		return nil, fmt.Errorf("no GitLab token in context")
	}

	gitlabClient, err := client.GetClient(ctx, token, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get GitLab client: %w", err)
	}

	opts := &gitlab.CreateMergeRequestOptions{
		SourceBranch:       gitlab.Ptr(sourceBranch),
		TargetBranch:       gitlab.Ptr(targetBranch),
		Title:              gitlab.Ptr(title),
		Description:        gitlab.Ptr(description),
		RemoveSourceBranch: gitlab.Ptr(true),
	}

	mr, _, err := gitlabClient.MergeRequests.CreateMergeRequest(projectID, opts)
	if err != nil {
		log.Printf("[GitLabWrite] Failed to create MR: %v", err)
		return nil, err
	}

	log.Printf("[GitLabWrite] MR created: !%d (%s)", mr.IID, mr.WebURL)
	return mr, nil
}

// GetProject retrieves project information including the HTTP URL for cloning
func GetProject(ctx context.Context, projectID interface{}) (*gitlab.Project, error) {
	log.Printf("[GitLabWrite] Getting project info for %v", projectID)

	token, ok := ctx.Value("token").(*oauth2.Token)
	if !ok {
		return nil, fmt.Errorf("no GitLab token in context")
	}

	gitlabClient, err := client.GetClient(ctx, token, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get GitLab client: %w", err)
	}

	project, _, err := gitlabClient.Projects.GetProject(projectID, nil)
	if err != nil {
		log.Printf("[GitLabWrite] Failed to get project: %v", err)
		return nil, err
	}

	return project, nil
}

// GetIssue retrieves a specific issue by project ID and IID
func GetIssue(ctx context.Context, projectID interface{}, issueIID int64) (*gitlab.Issue, error) {
	log.Printf("[GitLabWrite] Getting issue #%d from project %v", issueIID, projectID)

	token, ok := ctx.Value("token").(*oauth2.Token)
	if !ok {
		return nil, fmt.Errorf("no GitLab token in context")
	}

	gitlabClient, err := client.GetClient(ctx, token, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get GitLab client: %w", err)
	}

	issue, _, err := gitlabClient.Issues.GetIssue(projectID, issueIID)
	if err != nil {
		log.Printf("[GitLabWrite] Failed to get issue: %v", err)
		return nil, err
	}

	return issue, nil
}

// GetCurrentUser fetches the authenticated user's profile from GitLab
func GetCurrentUser(ctx context.Context) (*GitUser, error) {
	log.Printf("[GitLabWrite] Getting current user info")

	token, ok := ctx.Value("token").(*oauth2.Token)
	if !ok {
		return nil, fmt.Errorf("no GitLab token in context")
	}

	gitlabClient, err := client.GetClient(ctx, token, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get GitLab client: %w", err)
	}

	user, _, err := gitlabClient.Users.CurrentUser()
	if err != nil {
		return nil, fmt.Errorf("failed to get current user: %w", err)
	}

	log.Printf("[GitLabWrite] Current user: %s (%s)", user.Name, user.Email)

	return &GitUser{
		Name:  user.Name,
		Email: user.Email,
	}, nil
}
