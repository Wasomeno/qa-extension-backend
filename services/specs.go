package services

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	gitlab "gitlab.com/gitlab-org/api/client-go"
	"golang.org/x/sync/errgroup"
)

// SpecsService provides GitLab-backed operations for managing spec files
// in a project's repository (tree listing, file CRUD, commits, diffs).
type SpecsService struct{}

func NewSpecsService() *SpecsService {
	return &SpecsService{}
}

// --- Types ---

// FileTreeNode represents a node in the specs file tree.
type FileTreeNode struct {
	Path       string          `json:"path"`
	Name       string          `json:"name"`
	Type       string          `json:"type"` // "tree" or "blob"
	Children   []*FileTreeNode `json:"children,omitempty"`
	LastCommit *SpecCommit     `json:"lastCommit,omitempty"`
}

// FileContent holds the content of a spec file.
type FileContent struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Size    int64  `json:"size"`
}

// SpecCommit represents a commit in the specs history.
type SpecCommit struct {
	Hash          string `json:"hash"`
	ShortHash     string `json:"shortHash"`
	Message       string `json:"message"`
	AuthorName    string `json:"authorName"`
	AuthorEmail   string `json:"authorEmail"`
	CommittedDate string `json:"committedDate"`
	WebURL        string `json:"webUrl,omitempty"`
}

// CommitDetail holds a single commit with its diffs.
type CommitDetail struct {
	SpecCommit
	Diffs []CommitDiff `json:"diffs"`
}

// CommitDiff represents a single file diff in a commit.
type CommitDiff struct {
	OldPath     string `json:"oldPath"`
	NewPath     string `json:"newPath"`
	Diff        string `json:"diff"`
	NewFile     bool   `json:"newFile"`
	RenamedFile bool   `json:"renamedFile"`
	DeletedFile bool   `json:"deletedFile"`
}

// FileAction represents a single file change for batch commits.
type FileAction struct {
	Action       string `json:"action"` // "create", "update", "delete", "move"
	FilePath     string `json:"filePath"`
	Content      string `json:"content,omitempty"`
	PreviousPath string `json:"previousPath,omitempty"` // for "move" action
}

// --- Tree ---

// GetFileTree returns the file tree for a given path in the repository.
// When recursive is true, it fetches every directory level individually
// to avoid missing entries that GitLab's recursive ListTree can drop.
func (s *SpecsService) GetFileTree(ctx context.Context, client *gitlab.Client, projectID string, path string, ref string, recursive bool) ([]*FileTreeNode, error) {
	if ref == "" {
		ref = "main"
	}

	if entries, err := DefaultRepoCache().ListTree(ctx, client, projectID, ref, path, recursive); err == nil {
		return repoEntriesToFileTree(entries, recursive, path), nil
	} else if !errors.Is(err, ErrRepoCacheDisabled) && !errors.Is(err, ErrRepoCacheToken) {
		log.Printf("[specs] repo cache tree fallback for project %s path %s ref %s: %v", projectID, path, ref, err)
	}

	if recursive {
		return s.getFullTree(ctx, client, projectID, path, ref)
	}

	// Non-recursive: single-level listing
	return s.listDirectory(client, projectID, path, ref)
}

// getFullTree recursively walks every subdirectory and builds the complete tree.
func (s *SpecsService) getFullTree(ctx context.Context, client *gitlab.Client, projectID string, path string, ref string) ([]*FileTreeNode, error) {
	children, err := s.listDirectory(client, projectID, path, ref)
	if err != nil {
		return nil, err
	}

	for _, node := range children {
		if node.Type == "tree" {
			subChildren, err := s.getFullTree(ctx, client, projectID, node.Path, ref)
			if err != nil {
				log.Printf("[specs] skipping subtree %s: %v", node.Path, err)
				node.Children = []*FileTreeNode{}
				continue
			}
			node.Children = subChildren
		}
	}

	return children, nil
}

// listDirectory returns the immediate children of a single directory (non-recursive, paginated).
func (s *SpecsService) listDirectory(client *gitlab.Client, projectID string, path string, ref string) ([]*FileTreeNode, error) {
	opts := &gitlab.ListTreeOptions{
		Path:      gitlab.Ptr(path),
		Ref:       gitlab.Ptr(ref),
		Recursive: gitlab.Ptr(false),
		ListOptions: gitlab.ListOptions{
			PerPage: 100,
		},
	}

	var allNodes []*gitlab.TreeNode
	for {
		nodes, resp, err := client.Repositories.ListTree(projectID, opts)
		if err != nil {
			log.Printf("[specs] ListTree error for project %s path %s ref %s: %v", projectID, path, ref, err)
			return nil, fmt.Errorf("failed to list tree at %q: %w", path, err)
		}
		allNodes = append(allNodes, nodes...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	result := make([]*FileTreeNode, len(allNodes))
	for i, node := range allNodes {
		result[i] = &FileTreeNode{
			Path: node.Path,
			Name: node.Name,
			Type: node.Type,
		}
	}
	return result, nil
}

// --- File CRUD ---

// GetFile retrieves a file's content from the repository.
func (s *SpecsService) GetFile(ctx context.Context, client *gitlab.Client, projectID string, filePath string, ref string) (*FileContent, error) {
	if ref == "" {
		ref = "main"
	}

	if content, meta, err := DefaultRepoCache().ReadFile(ctx, client, projectID, ref, filePath); err == nil {
		return &FileContent{Path: meta.Path, Content: content, Size: meta.Size}, nil
	} else if !errors.Is(err, ErrRepoCacheDisabled) && !errors.Is(err, ErrRepoCacheToken) {
		log.Printf("[specs] repo cache file fallback for project %s file %s ref %s: %v", projectID, filePath, ref, err)
	}

	file, _, err := client.RepositoryFiles.GetFile(projectID, filePath, &gitlab.GetFileOptions{
		Ref: gitlab.Ptr(ref),
	})
	if err != nil {
		log.Printf("[specs] GetFile error for %s: %v", filePath, err)
		return nil, fmt.Errorf("failed to get file: %w", err)
	}

	// Content is base64 encoded
	content, err := base64.StdEncoding.DecodeString(file.Content)
	if err != nil {
		return nil, fmt.Errorf("failed to decode file content: %w", err)
	}

	return &FileContent{
		Path:    file.FilePath,
		Content: string(content),
		Size:    file.Size,
	}, nil
}

// CreateFile creates a new file in the repository.
func (s *SpecsService) CreateFile(client *gitlab.Client, projectID string, filePath string, content string, branch string, commitMessage string, authorName string, authorEmail string) error {
	if branch == "" {
		branch = "main"
	}
	if commitMessage == "" {
		commitMessage = fmt.Sprintf("Create %s", filePath)
	}

	opts := &gitlab.CreateFileOptions{
		Branch:        gitlab.Ptr(branch),
		Content:       gitlab.Ptr(content),
		CommitMessage: gitlab.Ptr(commitMessage),
	}
	if authorName != "" {
		opts.AuthorName = gitlab.Ptr(authorName)
	}
	if authorEmail != "" {
		opts.AuthorEmail = gitlab.Ptr(authorEmail)
	}

	_, _, err := client.RepositoryFiles.CreateFile(projectID, filePath, opts)
	if err != nil {
		log.Printf("[specs] CreateFile error for %s: %v", filePath, err)
		return fmt.Errorf("failed to create file: %w", err)
	}
	return nil
}

// UpdateFile updates an existing file in the repository.
func (s *SpecsService) UpdateFile(client *gitlab.Client, projectID string, filePath string, content string, branch string, commitMessage string, authorName string, authorEmail string) error {
	if branch == "" {
		branch = "main"
	}
	if commitMessage == "" {
		commitMessage = fmt.Sprintf("Update %s", filePath)
	}

	opts := &gitlab.UpdateFileOptions{
		Branch:        gitlab.Ptr(branch),
		Content:       gitlab.Ptr(content),
		CommitMessage: gitlab.Ptr(commitMessage),
	}
	if authorName != "" {
		opts.AuthorName = gitlab.Ptr(authorName)
	}
	if authorEmail != "" {
		opts.AuthorEmail = gitlab.Ptr(authorEmail)
	}

	_, _, err := client.RepositoryFiles.UpdateFile(projectID, filePath, opts)
	if err != nil {
		log.Printf("[specs] UpdateFile error for %s: %v", filePath, err)
		return fmt.Errorf("failed to update file: %w", err)
	}
	return nil
}

// DeleteFile deletes a file from the repository.
func (s *SpecsService) DeleteFile(client *gitlab.Client, projectID string, filePath string, branch string, commitMessage string, authorName string, authorEmail string) error {
	if branch == "" {
		branch = "main"
	}
	if commitMessage == "" {
		commitMessage = fmt.Sprintf("Delete %s", filePath)
	}

	opts := &gitlab.DeleteFileOptions{
		Branch:        gitlab.Ptr(branch),
		CommitMessage: gitlab.Ptr(commitMessage),
	}
	if authorName != "" {
		opts.AuthorName = gitlab.Ptr(authorName)
	}
	if authorEmail != "" {
		opts.AuthorEmail = gitlab.Ptr(authorEmail)
	}

	_, err := client.RepositoryFiles.DeleteFile(projectID, filePath, opts)
	if err != nil {
		log.Printf("[specs] DeleteFile error for %s: %v", filePath, err)
		return fmt.Errorf("failed to delete file: %w", err)
	}
	return nil
}

// --- Batch Commit ---

// CommitFiles performs a batch commit with multiple file actions.
// This is the GitLab "Create a commit with multiple files and actions" API.
func (s *SpecsService) CommitFiles(client *gitlab.Client, projectID string, branch string, commitMessage string, actions []FileAction, authorName string, authorEmail string) (*SpecCommit, error) {
	if branch == "" {
		branch = "main"
	}

	commitActions := make([]*gitlab.CommitActionOptions, len(actions))
	for i, a := range actions {
		actionOpts := &gitlab.CommitActionOptions{
			FilePath: gitlab.Ptr(a.FilePath),
			Content:  gitlab.Ptr(a.Content),
		}

		switch a.Action {
		case "create":
			actionOpts.Action = gitlab.Ptr(gitlab.FileCreate)
		case "update":
			actionOpts.Action = gitlab.Ptr(gitlab.FileUpdate)
		case "delete":
			actionOpts.Action = gitlab.Ptr(gitlab.FileDelete)
		case "move":
			actionOpts.Action = gitlab.Ptr(gitlab.FileMove)
			if a.PreviousPath != "" {
				actionOpts.PreviousPath = gitlab.Ptr(a.PreviousPath)
			}
		default:
			return nil, fmt.Errorf("unknown file action: %s", a.Action)
		}

		commitActions[i] = actionOpts
	}

	opts := &gitlab.CreateCommitOptions{
		Branch:        gitlab.Ptr(branch),
		CommitMessage: gitlab.Ptr(commitMessage),
		Actions:       commitActions,
	}
	if authorName != "" {
		opts.AuthorName = gitlab.Ptr(authorName)
	}
	if authorEmail != "" {
		opts.AuthorEmail = gitlab.Ptr(authorEmail)
	}

	commit, _, err := client.Commits.CreateCommit(projectID, opts)
	if err != nil {
		log.Printf("[specs] CommitFiles error: %v", err)
		return nil, fmt.Errorf("failed to create commit: %w", err)
	}

	return &SpecCommit{
		Hash:          commit.ID,
		ShortHash:     commit.ShortID,
		Message:       commit.Message,
		AuthorName:    commit.AuthorName,
		AuthorEmail:   commit.AuthorEmail,
		CommittedDate: commit.CommittedDate.Format("2006-01-02T15:04:05Z"),
		WebURL:        commit.WebURL,
	}, nil
}

// --- Commits / History ---

// GetCommits returns commit history for a given path in the repository.
func (s *SpecsService) GetCommits(client *gitlab.Client, projectID string, path string, ref string, perPage int, page int) ([]SpecCommit, error) {
	if ref == "" {
		ref = "main"
	}
	if perPage <= 0 {
		perPage = 20
	}
	if page <= 0 {
		page = 1
	}

	opts := &gitlab.ListCommitsOptions{
		RefName: gitlab.Ptr(ref),
		ListOptions: gitlab.ListOptions{
			PerPage: int64(perPage),
			Page:    int64(page),
		},
	}
	if path != "" {
		opts.Path = gitlab.Ptr(path)
	}

	commits, _, err := client.Commits.ListCommits(projectID, opts)
	if err != nil {
		log.Printf("[specs] ListCommits error for project %s: %v", projectID, err)
		return nil, fmt.Errorf("failed to list commits: %w", err)
	}

	result := make([]SpecCommit, len(commits))
	for i, c := range commits {
		dateStr := ""
		if c.CommittedDate != nil {
			dateStr = c.CommittedDate.Format("2006-01-02T15:04:05Z")
		}
		result[i] = SpecCommit{
			Hash:          c.ID,
			ShortHash:     c.ShortID,
			Message:       c.Message,
			AuthorName:    c.AuthorName,
			AuthorEmail:   c.AuthorEmail,
			CommittedDate: dateStr,
			WebURL:        c.WebURL,
		}
	}
	return result, nil
}

// GetCommitDetail returns a single commit with its diffs.
func (s *SpecsService) GetCommitDetail(client *gitlab.Client, projectID string, commitSHA string) (*CommitDetail, error) {
	commit, _, err := client.Commits.GetCommit(projectID, commitSHA, nil)
	if err != nil {
		log.Printf("[specs] GetCommit error for %s: %v", commitSHA, err)
		return nil, fmt.Errorf("failed to get commit: %w", err)
	}

	diffs, _, err := client.Commits.GetCommitDiff(projectID, commitSHA, nil)
	if err != nil {
		log.Printf("[specs] GetCommitDiff error for %s: %v", commitSHA, err)
		return nil, fmt.Errorf("failed to get commit diff: %w", err)
	}

	dateStr := ""
	if commit.CommittedDate != nil {
		dateStr = commit.CommittedDate.Format("2006-01-02T15:04:05Z")
	}

	result := &CommitDetail{
		SpecCommit: SpecCommit{
			Hash:          commit.ID,
			ShortHash:     commit.ShortID,
			Message:       commit.Message,
			AuthorName:    commit.AuthorName,
			AuthorEmail:   commit.AuthorEmail,
			CommittedDate: dateStr,
			WebURL:        commit.WebURL,
		},
		Diffs: make([]CommitDiff, len(diffs)),
	}

	for i, d := range diffs {
		result.Diffs[i] = CommitDiff{
			OldPath:     d.OldPath,
			NewPath:     d.NewPath,
			Diff:        d.Diff,
			NewFile:     d.NewFile,
			RenamedFile: d.RenamedFile,
			DeletedFile: d.DeletedFile,
		}
	}

	return result, nil
}

// --- Search ---

// SearchTree searches for files matching a query in the tree (path/name).
func (s *SpecsService) SearchTree(ctx context.Context, client *gitlab.Client, projectID string, path string, ref string, query string) ([]*FileTreeNode, error) {
	nodes, err := s.GetFileTree(ctx, client, projectID, path, ref, true)
	if err != nil {
		return nil, err
	}
	return filterTree(nodes, query), nil
}

// SearchResult is a tree hit that may include a content-match preview.
type SearchResult struct {
	*FileTreeNode
	MatchLine    int    `json:"matchLine,omitempty"`
	MatchPreview string `json:"matchPreview,omitempty"`
	MatchSource  string `json:"matchSource,omitempty"` // "path" | "content"
}

// SearchSpecs combines path/name tree filtering with GitLab blob search when available.
// degraded is true when blob search failed and only path matches are returned.
func (s *SpecsService) SearchSpecs(ctx context.Context, client *gitlab.Client, projectID string, path string, ref string, query string) (results []*SearchResult, degraded bool, err error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, false, fmt.Errorf("query is required")
	}

	pathHits, err := s.SearchTree(ctx, client, projectID, path, ref, query)
	if err != nil {
		return nil, false, err
	}

	seen := map[string]*SearchResult{}
	for _, n := range pathHits {
		if n == nil || n.Type != "blob" {
			continue
		}
		seen[n.Path] = &SearchResult{FileTreeNode: n, MatchSource: "path"}
	}

	blobHits, blobErr := s.searchBlobs(ctx, client, projectID, path, ref, query)
	if blobErr != nil {
		log.Printf("[specs] blob search degraded for project %s: %v", projectID, blobErr)
		degraded = true
	} else {
		for _, hit := range blobHits {
			if existing, ok := seen[hit.Path]; ok {
				if existing.MatchPreview == "" && hit.MatchPreview != "" {
					existing.MatchLine = hit.MatchLine
					existing.MatchPreview = hit.MatchPreview
					existing.MatchSource = "content"
				}
				continue
			}
			seen[hit.Path] = hit
		}
	}

	out := make([]*SearchResult, 0, len(seen))
	for _, r := range seen {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Path < out[j].Path
	})
	return out, degraded, nil
}

func (s *SpecsService) searchBlobs(ctx context.Context, client *gitlab.Client, projectID string, path string, ref string, query string) ([]*SearchResult, error) {
	// GitLab project search with scope=blobs (content search).
	// client-go exposes this as Search.BlobsByProject in recent versions.
	opts := &gitlab.SearchOptions{
		ListOptions: gitlab.ListOptions{PerPage: 50, Page: 1},
	}
	if strings.TrimSpace(ref) != "" {
		opts.Ref = gitlab.Ptr(ref)
	}

	blobs, _, err := client.Search.BlobsByProject(projectID, query, opts, gitlab.WithContext(ctx))
	if err != nil {
		return nil, err
	}

	base := strings.Trim(strings.TrimSpace(path), "/")
	out := make([]*SearchResult, 0, len(blobs))
	for _, b := range blobs {
		if b == nil {
			continue
		}
		filePath := strings.TrimSpace(b.Filename)
		if filePath == "" {
			filePath = strings.TrimSpace(b.Path)
		}
		if filePath == "" {
			continue
		}
		if base != "" && !strings.HasPrefix(filePath, base+"/") && filePath != base {
			continue
		}
		name := filepath.Base(filePath)
		preview := strings.TrimSpace(b.Data)
		if len(preview) > 200 {
			preview = preview[:200] + "…"
		}
		out = append(out, &SearchResult{
			FileTreeNode: &FileTreeNode{Path: filePath, Name: name, Type: "blob"},
			MatchLine:    int(b.Startline),
			MatchPreview: preview,
			MatchSource:  "content",
		})
	}
	return out, nil
}

const (
	specsEnrichMaxBlobs   = 100
	specsEnrichConcurrency = 8
	specsEnrichCacheTTL   = 10 * time.Minute
)

// EnrichTreeLastCommits attaches last-commit metadata to blob nodes (spec-like files).
// Caps work to specsEnrichMaxBlobs and uses a short in-process cache.
func (s *SpecsService) EnrichTreeLastCommits(ctx context.Context, client *gitlab.Client, projectID string, ref string, nodes []*FileTreeNode) {
	if ref == "" {
		ref = "main"
	}
	blobs := collectSpecBlobs(nodes)
	if len(blobs) > specsEnrichMaxBlobs {
		blobs = blobs[:specsEnrichMaxBlobs]
	}
	if len(blobs) == 0 {
		return
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(specsEnrichConcurrency)
	var mu sync.Mutex

	for _, node := range blobs {
		node := node
		g.Go(func() error {
			commit, err := s.lastCommitForPath(gctx, client, projectID, node.Path, ref)
			if err != nil || commit == nil {
				return nil
			}
			mu.Lock()
			node.LastCommit = commit
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()
}

func collectSpecBlobs(nodes []*FileTreeNode) []*FileTreeNode {
	var out []*FileTreeNode
	var walk func([]*FileTreeNode)
	walk = func(list []*FileTreeNode) {
		for _, n := range list {
			if n == nil {
				continue
			}
			if n.Type == "blob" && isSpecLikePath(n.Path) {
				out = append(out, n)
			}
			if len(n.Children) > 0 {
				walk(n.Children)
			}
		}
	}
	walk(nodes)
	return out
}

func isSpecLikePath(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, ".md") ||
		strings.HasSuffix(lower, ".feature") ||
		strings.HasSuffix(lower, ".gherkin")
}

var (
	specsMetaCache   = map[string]specsMetaCacheEntry{}
	specsMetaCacheMu sync.Mutex
)

type specsMetaCacheEntry struct {
	commit    *SpecCommit
	expiresAt time.Time
}

func (s *SpecsService) lastCommitForPath(ctx context.Context, client *gitlab.Client, projectID, path, ref string) (*SpecCommit, error) {
	cacheKey := projectID + "|" + ref + "|" + path
	specsMetaCacheMu.Lock()
	if ent, ok := specsMetaCache[cacheKey]; ok && time.Now().Before(ent.expiresAt) {
		commit := ent.commit
		specsMetaCacheMu.Unlock()
		return commit, nil
	}
	specsMetaCacheMu.Unlock()

	opts := &gitlab.ListCommitsOptions{
		Path:    gitlab.Ptr(path),
		RefName: gitlab.Ptr(ref),
		ListOptions: gitlab.ListOptions{
			PerPage: 1,
			Page:    1,
		},
	}
	commits, _, err := client.Commits.ListCommits(projectID, opts, gitlab.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	if len(commits) == 0 {
		specsMetaCacheMu.Lock()
		specsMetaCache[cacheKey] = specsMetaCacheEntry{commit: nil, expiresAt: time.Now().Add(specsEnrichCacheTTL)}
		specsMetaCacheMu.Unlock()
		return nil, nil
	}
	c := commits[0]
	out := &SpecCommit{
		Hash:        c.ID,
		ShortHash:   c.ShortID,
		Message:     strings.TrimSpace(c.Title),
		AuthorName:  c.AuthorName,
		AuthorEmail: c.AuthorEmail,
		WebURL:      c.WebURL,
	}
	if c.CommittedDate != nil {
		out.CommittedDate = c.CommittedDate.Format(time.RFC3339)
	}
	specsMetaCacheMu.Lock()
	specsMetaCache[cacheKey] = specsMetaCacheEntry{commit: out, expiresAt: time.Now().Add(specsEnrichCacheTTL)}
	specsMetaCacheMu.Unlock()
	return out, nil
}

func repoEntriesToFileTree(entries []RepoTreeEntry, recursive bool, basePath string) []*FileTreeNode {
	if !recursive {
		nodes := make([]*FileTreeNode, 0, len(entries))
		for _, entry := range entries {
			nodes = append(nodes, &FileTreeNode{Path: entry.Path, Name: entry.Name, Type: entry.Type})
		}
		return nodes
	}

	root := map[string]*FileTreeNode{}
	for _, entry := range entries {
		parts := strings.Split(entry.Path, "/")
		for i := range parts {
			currentPath := strings.Join(parts[:i+1], "/")
			if _, ok := root[currentPath]; ok {
				continue
			}
			nodeType := "tree"
			if i == len(parts)-1 {
				nodeType = entry.Type
			}
			root[currentPath] = &FileTreeNode{Path: currentPath, Name: parts[i], Type: nodeType}
		}
	}

	basePath = strings.Trim(strings.TrimSpace(basePath), "/")
	if basePath == "." {
		basePath = ""
	}
	var top []*FileTreeNode
	for path, node := range root {
		parentPath := ""
		if idx := strings.LastIndex(path, "/"); idx >= 0 {
			parentPath = path[:idx]
		}
		if parentPath == basePath {
			top = append(top, node)
			continue
		}
		if parent, ok := root[parentPath]; ok {
			parent.Children = append(parent.Children, node)
		}
	}
	sortFileTree(top)
	return top
}

func sortFileTree(nodes []*FileTreeNode) {
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Type != nodes[j].Type {
			return nodes[i].Type == "tree"
		}
		return nodes[i].Name < nodes[j].Name
	})
	for _, node := range nodes {
		sortFileTree(node.Children)
	}
}

func filterTree(nodes []*FileTreeNode, query string) []*FileTreeNode {
	lower := strings.ToLower(query)
	var result []*FileTreeNode
	for _, n := range nodes {
		if strings.Contains(strings.ToLower(n.Name), lower) || strings.Contains(strings.ToLower(n.Path), lower) {
			result = append(result, n)
		}
		if n.Type == "tree" && n.Children != nil {
			matches := filterTree(n.Children, query)
			result = append(result, matches...)
		}
	}
	return result
}

// --- Blame ---

// GetFileBlame returns blame information for a file.
func (s *SpecsService) GetFileBlame(client *gitlab.Client, projectID string, filePath string, ref string) (interface{}, error) {
	if ref == "" {
		ref = "main"
	}

	opts := &gitlab.GetFileBlameOptions{
		Ref: gitlab.Ptr(ref),
	}

	blame, _, err := client.RepositoryFiles.GetFileBlame(projectID, filePath, opts)
	if err != nil {
		log.Printf("[specs] GetFileBlame error for %s: %v", filePath, err)
		return nil, fmt.Errorf("failed to get file blame: %w", err)
	}

	return blame, nil
}
