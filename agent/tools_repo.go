package agent

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"qa-extension-backend/services"
)

const (
	repoLSDefaultDepth     = 1
	repoLSMaxDepth         = 3
	repoLSDefaultLimit     = 100
	repoLSMaxLimit         = 200
	repoFindDefaultLimit   = 50
	repoFindMaxLimit       = 100
	repoGrepDefaultContext = 2
	repoGrepMaxContext     = 5
	repoGrepDefaultLimit   = 80
	repoGrepMaxLimit       = 150
	repoReadDefaultLines   = 160
	repoReadMaxLines       = 200
	repoReadMaxBytes       = 48 * 1024
	repoBranchesDefault    = 50
	repoBranchesMax        = 100
)

type RepoLSArgs struct {
	ProjectID string `json:"projectId"`
	Path      string `json:"path,omitempty"`
	Depth     int    `json:"depth,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type RepoEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"`
}

type RepoLSResponse struct {
	Entries   []RepoEntry `json:"entries"`
	Count     int         `json:"count"`
	Truncated bool        `json:"truncated"`
	Path      string      `json:"path"`
}

func repoLS(ctx context.Context, args RepoLSArgs) (*RepoLSResponse, error) {
	log.Printf("[AgentTool] repo_ls called: project=%s path=%s depth=%d limit=%d", args.ProjectID, args.Path, args.Depth, args.Limit)
	if strings.TrimSpace(args.ProjectID) == "" {
		return nil, fmt.Errorf("projectId is required")
	}
	if err := validateAgentRepoPath(args.Path, true); err != nil {
		return nil, err
	}

	glClient, err := getGitLabClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get GitLab client: %w", err)
	}
	if err := ensureRepoMirrorForAgentTool(ctx, glClient, args.ProjectID); err != nil {
		return nil, err
	}

	depth := clampIntDefault(args.Depth, repoLSDefaultDepth, 1, repoLSMaxDepth)
	limit := clampIntDefault(args.Limit, repoLSDefaultLimit, 1, repoLSMaxLimit)
	path := normalizeAgentRepoPath(args.Path)
	recursive := depth > 1
	entries, err := services.DefaultRepoCache().ListTree(ctx, glClient, args.ProjectID, "", path, recursive)
	if err != nil {
		return nil, fmt.Errorf("repo_ls requires local repo cache: %w", err)
	}

	filtered := make([]RepoEntry, 0, len(entries))
	for _, entry := range entries {
		if recursive && repoEntryDepth(path, entry.Path) > depth {
			continue
		}
		filtered = append(filtered, RepoEntry{Name: entry.Name, Path: entry.Path, Type: entry.Type})
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].Type == filtered[j].Type {
			return filtered[i].Path < filtered[j].Path
		}
		return filtered[i].Type == "tree"
	})
	truncated := len(filtered) > limit
	if truncated {
		filtered = filtered[:limit]
	}

	logRepoToolResult("repo_ls", len(filtered), truncated, approxJSONBytes(filtered))
	return &RepoLSResponse{Entries: filtered, Count: len(filtered), Truncated: truncated, Path: path}, nil
}

type RepoFindArgs struct {
	ProjectID string `json:"projectId"`
	Pattern   string `json:"pattern"`
	Path      string `json:"path,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type RepoFindResponse struct {
	Files     []string `json:"files"`
	Count     int      `json:"count"`
	Truncated bool     `json:"truncated"`
}

func repoFind(ctx context.Context, args RepoFindArgs) (*RepoFindResponse, error) {
	log.Printf("[AgentTool] repo_find called: project=%s pattern=%q path=%s limit=%d", args.ProjectID, args.Pattern, args.Path, args.Limit)
	if strings.TrimSpace(args.ProjectID) == "" {
		return nil, fmt.Errorf("projectId is required")
	}
	if strings.TrimSpace(args.Pattern) == "" {
		return nil, fmt.Errorf("pattern is required")
	}
	if err := validateAgentRepoPath(args.Path, true); err != nil {
		return nil, err
	}

	glClient, err := getGitLabClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get GitLab client: %w", err)
	}
	if err := ensureRepoMirrorForAgentTool(ctx, glClient, args.ProjectID); err != nil {
		return nil, err
	}

	allFiles, err := services.DefaultRepoCache().ListFiles(ctx, glClient, args.ProjectID, "")
	if err != nil {
		return nil, fmt.Errorf("repo_find requires local repo cache: %w", err)
	}

	limit := clampIntDefault(args.Limit, repoFindDefaultLimit, 1, repoFindMaxLimit)
	path := normalizeAgentRepoPath(args.Path)
	matched := filterRepoFiles(allFiles, args.Pattern, path)
	truncated := len(matched) > limit
	if truncated {
		matched = matched[:limit]
	}

	logRepoToolResult("repo_find", len(matched), truncated, approxJSONBytes(matched))
	return &RepoFindResponse{Files: matched, Count: len(matched), Truncated: truncated}, nil
}

type RepoGrepArgs struct {
	ProjectID    string `json:"projectId"`
	Pattern      string `json:"pattern"`
	Path         string `json:"path,omitempty"`
	ContextLines int    `json:"contextLines,omitempty"`
	FixedString  bool   `json:"fixedString,omitempty"`
	Limit        int    `json:"limit,omitempty"`
}

type RepoGrepMatch struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Content string `json:"content"`
}

type RepoGrepResponse struct {
	Matches   []RepoGrepMatch `json:"matches"`
	Count     int             `json:"count"`
	Truncated bool            `json:"truncated"`
}

func repoGrep(ctx context.Context, args RepoGrepArgs) (*RepoGrepResponse, error) {
	log.Printf("[AgentTool] repo_grep called: project=%s pattern=%q path=%s context=%d limit=%d", args.ProjectID, args.Pattern, args.Path, args.ContextLines, args.Limit)
	if strings.TrimSpace(args.ProjectID) == "" {
		return nil, fmt.Errorf("projectId is required")
	}
	if strings.TrimSpace(args.Pattern) == "" {
		return nil, fmt.Errorf("pattern is required")
	}

	glClient, err := getGitLabClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get GitLab client: %w", err)
	}
	if err := ensureRepoMirrorForAgentTool(ctx, glClient, args.ProjectID); err != nil {
		return nil, err
	}

	contextLines := clampIntDefault(args.ContextLines, repoGrepDefaultContext, 0, repoGrepMaxContext)
	limit := clampIntDefault(args.Limit, repoGrepDefaultLimit, 1, repoGrepMaxLimit)
	cached, err := services.DefaultRepoCache().GrepLines(ctx, glClient, args.ProjectID, "", args.Pattern, args.Path, contextLines, args.FixedString)
	if err != nil {
		return nil, fmt.Errorf("repo_grep requires local repo cache: %w", err)
	}
	truncated := len(cached) > limit
	if truncated {
		cached = cached[:limit]
	}

	matches := make([]RepoGrepMatch, 0, len(cached))
	for _, m := range cached {
		matches = append(matches, RepoGrepMatch{File: m.FilePath, Line: m.Line, Content: m.Content})
	}
	logRepoToolResult("repo_grep", len(matches), truncated, approxJSONBytes(matches))
	return &RepoGrepResponse{Matches: matches, Count: len(matches), Truncated: truncated}, nil
}

type RepoReadArgs struct {
	ProjectID string `json:"projectId"`
	Path      string `json:"path"`
	StartLine int    `json:"startLine,omitempty"`
	LineCount int    `json:"lineCount,omitempty"`
}

type RepoReadResponse struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	StartLine  int    `json:"startLine"`
	EndLine    int    `json:"endLine"`
	TotalLines int    `json:"totalLines"`
	Size       int    `json:"size"`
	Truncated  bool   `json:"truncated"`
}

func repoRead(ctx context.Context, args RepoReadArgs) (*RepoReadResponse, error) {
	log.Printf("[AgentTool] repo_read called: project=%s path=%s start=%d lines=%d", args.ProjectID, args.Path, args.StartLine, args.LineCount)
	if strings.TrimSpace(args.ProjectID) == "" {
		return nil, fmt.Errorf("projectId is required")
	}
	if err := validateAgentRepoPath(args.Path, false); err != nil {
		return nil, err
	}

	glClient, err := getGitLabClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get GitLab client: %w", err)
	}
	if err := ensureRepoMirrorForAgentTool(ctx, glClient, args.ProjectID); err != nil {
		return nil, err
	}

	content, meta, err := services.DefaultRepoCache().ReadFile(ctx, glClient, args.ProjectID, "", args.Path)
	if err != nil {
		return nil, fmt.Errorf("repo_read requires local repo cache: %w", err)
	}
	if isBinaryLike(content) {
		return nil, fmt.Errorf("repo_read rejected binary or non-utf8 file %q", args.Path)
	}

	window := buildRepoReadWindow(content, args.StartLine, args.LineCount)
	logRepoToolResult("repo_read", 1, window.Truncated, len(window.Content))
	return &RepoReadResponse{
		Path:       meta.Path,
		Content:    window.Content,
		StartLine:  window.StartLine,
		EndLine:    window.EndLine,
		TotalLines: window.TotalLines,
		Size:       int(meta.Size),
		Truncated:  window.Truncated,
	}, nil
}

type RepoBranchesArgs struct {
	ProjectID string `json:"projectId"`
	Search    string `json:"search,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type RepoBranchInfo struct {
	Name    string `json:"name"`
	Default bool   `json:"default"`
}

type RepoBranchesResponse struct {
	Branches  []RepoBranchInfo `json:"branches"`
	Count     int              `json:"count"`
	Truncated bool             `json:"truncated"`
}

func repoBranches(ctx context.Context, args RepoBranchesArgs) (*RepoBranchesResponse, error) {
	log.Printf("[AgentTool] repo_branches called: project=%s search=%q limit=%d", args.ProjectID, args.Search, args.Limit)
	if strings.TrimSpace(args.ProjectID) == "" {
		return nil, fmt.Errorf("projectId is required")
	}

	glClient, err := getGitLabClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get GitLab client: %w", err)
	}
	repo, err := services.DefaultRepoCache().EnsureRepo(ctx, glClient, args.ProjectID, false)
	if err != nil {
		return nil, fmt.Errorf("repo_branches requires local repo cache: %w", err)
	}
	project, _, err := glClient.Projects.GetProject(args.ProjectID, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get project: %w", err)
	}

	branches, err := listCloneBranches(ctx, repo.CloneDir, strings.TrimSpace(args.Search), project.DefaultBranch)
	if err != nil {
		return nil, err
	}
	limit := clampIntDefault(args.Limit, repoBranchesDefault, 1, repoBranchesMax)
	truncated := len(branches) > limit
	if truncated {
		branches = branches[:limit]
	}

	logRepoToolResult("repo_branches", len(branches), truncated, approxJSONBytes(branches))
	return &RepoBranchesResponse{Branches: branches, Count: len(branches), Truncated: truncated}, nil
}

type repoReadWindow struct {
	Content    string
	StartLine  int
	EndLine    int
	TotalLines int
	Truncated  bool
}

func buildRepoReadWindow(content string, startLine int, lineCount int) repoReadWindow {
	lines := splitLinesPreserve(content)
	total := len(lines)
	if startLine <= 0 {
		startLine = 1
	}
	lineCount = clampIntDefault(lineCount, repoReadDefaultLines, 1, repoReadMaxLines)
	if total == 0 || startLine > total {
		return repoReadWindow{Content: "", StartLine: startLine, EndLine: startLine - 1, TotalLines: total, Truncated: startLine <= total}
	}

	startIdx := startLine - 1
	endIdx := startIdx + lineCount
	if endIdx > total {
		endIdx = total
	}
	selected := strings.Join(lines[startIdx:endIdx], "")
	lineTruncated := endIdx < total
	byteTruncated := len(selected) > repoReadMaxBytes
	if byteTruncated {
		selected = truncateUTF8Bytes(selected, repoReadMaxBytes)
	}
	return repoReadWindow{
		Content:    selected,
		StartLine:  startLine,
		EndLine:    endIdx,
		TotalLines: total,
		Truncated:  lineTruncated || byteTruncated,
	}
}

func filterRepoFiles(files []string, pattern string, path string) []string {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	path = normalizeAgentRepoPath(path)
	var matched []string
	for _, f := range files {
		if path != "" && f != path && !strings.HasPrefix(f, path+"/") {
			continue
		}
		lf := strings.ToLower(f)
		base := filepath.Base(lf)
		ok := false
		if strings.ContainsAny(pattern, "*?[") {
			ok, _ = filepath.Match(pattern, base)
			if !ok {
				ok, _ = filepath.Match(pattern, lf)
			}
		} else {
			ok = strings.Contains(lf, pattern)
		}
		if ok {
			matched = append(matched, f)
		}
	}
	sort.Strings(matched)
	return matched
}

func repoEntryDepth(basePath string, entryPath string) int {
	basePath = normalizeAgentRepoPath(basePath)
	rel := strings.Trim(strings.TrimSpace(entryPath), "/")
	if basePath != "" {
		rel = strings.TrimPrefix(rel, basePath)
		rel = strings.TrimPrefix(rel, "/")
	}
	if rel == "" {
		return 0
	}
	return strings.Count(rel, "/") + 1
}


func listCloneBranches(ctx context.Context, cloneDir string, search string, defaultBranch string) ([]RepoBranchInfo, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(timeoutCtx, "git", "for-each-ref", "--format=%(refname:short)", "refs/remotes/origin/")
	cmd.Dir = cloneDir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git branch listing failed: %w: %s", err, strings.TrimSpace(string(out)))
	}

	search = strings.ToLower(search)
	var branches []RepoBranchInfo
	for _, line := range strings.Split(string(out), "\n") {
		// strip "origin/" prefix; skip the synthetic HEAD pointer
		name := strings.TrimPrefix(strings.TrimSpace(line), "origin/")
		if name == "" || name == "HEAD" {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(name), search) {
			continue
		}
		branches = append(branches, RepoBranchInfo{Name: name, Default: name == defaultBranch})
	}
	sort.SliceStable(branches, func(i, j int) bool {
		if branches[i].Default != branches[j].Default {
			return branches[i].Default
		}
		return branches[i].Name < branches[j].Name
	})
	return branches, nil
}

func validateAgentRepoPath(path string, allowEmpty bool) error {
	path = strings.TrimSpace(path)
	if path == "" || path == "." {
		if allowEmpty {
			return nil
		}
		return fmt.Errorf("path is required")
	}
	if strings.Contains(path, "\x00") || filepath.IsAbs(path) || strings.Contains(path, "\\") {
		return fmt.Errorf("invalid repository path")
	}
	cleaned := filepath.Clean(path)
	if cleaned == "." && allowEmpty {
		return nil
	}
	if strings.HasPrefix(cleaned, "..") || strings.Contains(cleaned, string(filepath.Separator)+".."+string(filepath.Separator)) || strings.HasPrefix(cleaned, ".git") || strings.Contains(cleaned, "/.git") {
		return fmt.Errorf("invalid repository path")
	}
	return nil
}

func normalizeAgentRepoPath(path string) string {
	path = strings.Trim(strings.TrimSpace(path), "/")
	if path == "." {
		return ""
	}
	return path
}

func splitLinesPreserve(content string) []string {
	if content == "" {
		return []string{}
	}
	lines := strings.SplitAfter(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func truncateUTF8Bytes(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) && len(value) > 0 {
		value = value[:len(value)-1]
	}
	return value
}

func isBinaryLike(content string) bool {
	return strings.Contains(content, "\x00") || !utf8.ValidString(content)
}

func clampIntDefault(value int, def int, min int, max int) int {
	if value <= 0 {
		value = def
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func approxJSONBytes(value any) int {
	return len(fmt.Sprintf("%v", value))
}

func logRepoToolResult(tool string, count int, truncated bool, bytes int) {
	log.Printf("[AgentTool] %s returned count=%d truncated=%v approxBytes=%d", tool, count, truncated, bytes)
}
