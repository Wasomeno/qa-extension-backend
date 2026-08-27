package models

import "testing"

func TestAppProjectToResponseIncludesAutomationRepos(t *testing.T) {
	project := &AppProject{
		ID:             "project-1",
		Name:           "QA",
		IssueRepoID:    11,
		SpecsRepoID:    22,
		BackendRepoID:  33,
		FrontendRepoID: 44,
	}

	response := project.ToResponse("", "", "backend/repo", "frontend/repo")

	if response.IssueRepoName != "11" {
		t.Fatalf("IssueRepoName = %q, want fallback 11", response.IssueRepoName)
	}
	if response.SpecsRepoName != "22" {
		t.Fatalf("SpecsRepoName = %q, want fallback 22", response.SpecsRepoName)
	}
	if response.BackendRepoName != "backend/repo" {
		t.Fatalf("BackendRepoName = %q, want backend/repo", response.BackendRepoName)
	}
	if response.FrontendRepoName != "frontend/repo" {
		t.Fatalf("FrontendRepoName = %q, want frontend/repo", response.FrontendRepoName)
	}
	if response.IssueRepoID != 11 || response.SpecsRepoID != 22 || response.BackendRepoID != 33 || response.FrontendRepoID != 44 {
		t.Fatalf("repo ids missing from response: %+v", response)
	}
}
