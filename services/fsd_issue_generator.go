package services

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"qa-extension-backend/tracker"
)

const fsdIssueGenerationFeature = "fsd_issue_generation"

// FSDIssueSource contains the content of one selected FSD file.
type FSDIssueSource struct {
	Path    string `json:"path"`
	Ref     string `json:"ref,omitempty"`
	Content string `json:"content"`
}

// FSDIssueDraft is a GitLab-ready issue card generated from one FSD.
type FSDIssueDraft struct {
	SourcePath  string   `json:"sourcePath"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Labels      []string `json:"labels"`
}

// FSDIssueLLMFunc is the LLM dependency used by the FSD issue generator.
type FSDIssueLLMFunc func(context.Context, OpenAILLMRequest) (*OpenAILLMResponse, error)

// GenerateFSDIssueDrafts generates exactly one GitLab issue draft for each FSD source.
func GenerateFSDIssueDrafts(ctx context.Context, sources []FSDIssueSource, actorID int, generateText FSDIssueLLMFunc) ([]FSDIssueDraft, error) {
	if generateText == nil {
		generateText = GenerateOpenAIText
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("at least one FSD is required")
	}

	drafts := make([]FSDIssueDraft, 0, len(sources))
	for _, source := range sources {
		source.Path = strings.TrimSpace(source.Path)
		source.Content = strings.TrimSpace(source.Content)
		if source.Path == "" {
			return nil, fmt.Errorf("FSD path is required")
		}
		if source.Content == "" {
			return nil, fmt.Errorf("FSD content is empty for %s", source.Path)
		}

		draft, err := generateFSDIssueDraft(ctx, source, actorID, generateText)
		if err != nil {
			return nil, err
		}
		drafts = append(drafts, draft)
	}

	return drafts, nil
}

// ValidateFSDIssueDrafts validates previewed or user-edited issue drafts before creation.
func ValidateFSDIssueDrafts(drafts []FSDIssueDraft) error {
	if len(drafts) == 0 {
		return fmt.Errorf("at least one issue draft is required")
	}
	for i := range drafts {
		drafts[i].SourcePath = strings.TrimSpace(drafts[i].SourcePath)
		drafts[i].Title = strings.TrimSpace(drafts[i].Title)
		drafts[i].Description = strings.TrimSpace(drafts[i].Description)
		drafts[i].Labels = cleanLabels(drafts[i].Labels)
		if drafts[i].Title == "" {
			return fmt.Errorf("issue draft %d title is required", i+1)
		}
		if drafts[i].Description == "" {
			return fmt.Errorf("issue draft %d description is required", i+1)
		}
	}
	return nil
}

func generateFSDIssueDraft(ctx context.Context, source FSDIssueSource, actorID int, generateText FSDIssueLLMFunc) (FSDIssueDraft, error) {
	prompt := buildFSDIssuePrompt(source)
	start := time.Now()
	resp, err := generateText(ctx, OpenAILLMRequest{
		Feature:     fsdIssueGenerationFeature,
		Prompt:      prompt,
		Temperature: 0.2,
		JSONMode:    true,
	})
	duration := time.Since(start)
	if err != nil {
		return FSDIssueDraft{}, fmt.Errorf("failed to generate issue draft for %s: %w", source.Path, err)
	}
	if resp == nil || strings.TrimSpace(resp.Text) == "" {
		return FSDIssueDraft{}, fmt.Errorf("empty issue draft generated for %s", source.Path)
	}

	tracker.Log(ctx, tracker.TokenUsage{
		Feature:      fsdIssueGenerationFeature,
		Model:        resp.Model,
		InputTokens:  resp.InputTokens,
		OutputTokens: resp.OutputTokens,
		TotalTokens:  resp.TotalTokens,
		UserID:       actorID,
		Duration:     duration,
	})

	var draft FSDIssueDraft
	if err := json.Unmarshal([]byte(cleanFSDIssueJSON(resp.Text)), &draft); err != nil {
		return FSDIssueDraft{}, fmt.Errorf("failed to parse issue draft for %s: %w", source.Path, err)
	}
	draft.SourcePath = source.Path
	draft.Title = strings.TrimSpace(draft.Title)
	draft.Description = strings.TrimSpace(draft.Description)
	draft.Labels = cleanLabels(draft.Labels)
	if !strings.Contains(draft.Description, source.Path) {
		draft.Description = strings.TrimSpace(draft.Description) + "\n\n---\nSource FSD: `" + source.Path + "`"
	}
	if err := ValidateFSDIssueDrafts([]FSDIssueDraft{draft}); err != nil {
		return FSDIssueDraft{}, fmt.Errorf("invalid issue draft for %s: %w", source.Path, err)
	}
	return draft, nil
}

func buildFSDIssuePrompt(source FSDIssueSource) string {
	titleHint := strings.TrimSuffix(filepath.Base(source.Path), filepath.Ext(source.Path))
	return fmt.Sprintf(`You are a senior product engineer creating GitLab issue cards from Functional Specification Documents (FSDs).

Create exactly one GitLab issue for the FSD below.

Return ONLY valid JSON with this shape:
{
  "title": "short imperative issue title",
  "description": "GitLab markdown issue description",
  "labels": ["fsd", "feature"]
}

Rules:
- The title must be concise and actionable.
- The description must be useful as an implementation issue and include Summary, Requirements, Acceptance Criteria, and Notes sections.
- Do not invent assignees, milestones, due dates, or weights.
- Labels must be short GitLab label names; include "fsd" unless clearly inappropriate.
- Do not include markdown fences or explanatory text outside the JSON object.

FSD path: %s
Title hint: %s

FSD content:
%s`, source.Path, titleHint, source.Content)
}

func cleanFSDIssueJSON(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```json") || strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(s, "```")
	}
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		return strings.TrimSpace(s[start : end+1])
	}
	return strings.TrimSpace(s)
}

func cleanLabels(labels []string) []string {
	seen := make(map[string]bool, len(labels))
	cleaned := make([]string, 0, len(labels))
	for _, label := range labels {
		label = strings.TrimSpace(label)
		if label == "" || seen[label] {
			continue
		}
		seen[label] = true
		cleaned = append(cleaned, label)
	}
	return cleaned
}
