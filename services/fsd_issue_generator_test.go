package services

import (
	"context"
	"strings"
	"testing"
)

func TestGenerateFSDIssueDraftsParsesJSONAndAddsSource(t *testing.T) {
	llm := func(ctx context.Context, req OpenAILLMRequest) (*OpenAILLMResponse, error) {
		if req.Feature != fsdIssueGenerationFeature {
			t.Fatalf("unexpected feature %q", req.Feature)
		}
		if !req.JSONMode {
			t.Fatal("expected JSONMode")
		}
		return &OpenAILLMResponse{
			Text:  `{"title":"Implement payroll approvals","description":"## Summary\nBuild payroll approvals.","labels":["fsd","feature","fsd"]}`,
			Model: "test-model",
		}, nil
	}

	got, err := GenerateFSDIssueDrafts(context.Background(), []FSDIssueSource{{
		Path:    "docs/fsd/payroll.md",
		Content: "# Payroll approvals",
	}}, 42, llm)
	if err != nil {
		t.Fatalf("GenerateFSDIssueDrafts returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected one draft, got %d", len(got))
	}
	if got[0].SourcePath != "docs/fsd/payroll.md" {
		t.Fatalf("unexpected source path %q", got[0].SourcePath)
	}
	if got[0].Title != "Implement payroll approvals" {
		t.Fatalf("unexpected title %q", got[0].Title)
	}
	if !strings.Contains(got[0].Description, "Source FSD: `docs/fsd/payroll.md`") {
		t.Fatalf("expected source footer, got:\n%s", got[0].Description)
	}
	if len(got[0].Labels) != 2 || got[0].Labels[0] != "fsd" || got[0].Labels[1] != "feature" {
		t.Fatalf("expected deduplicated labels, got %#v", got[0].Labels)
	}
}

func TestGenerateFSDIssueDraftsRejectsMalformedJSON(t *testing.T) {
	llm := func(ctx context.Context, req OpenAILLMRequest) (*OpenAILLMResponse, error) {
		return &OpenAILLMResponse{Text: `not json`, Model: "test-model"}, nil
	}

	_, err := GenerateFSDIssueDrafts(context.Background(), []FSDIssueSource{{
		Path:    "docs/fsd/payroll.md",
		Content: "# Payroll approvals",
	}}, 0, llm)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "failed to parse issue draft") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateFSDIssueDraftsRejectsMissingTitle(t *testing.T) {
	err := ValidateFSDIssueDrafts([]FSDIssueDraft{{
		Description: "## Summary\nBuild it",
	}})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "title is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}
