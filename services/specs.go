package services

import (
	"encoding/base64"
	"fmt"
	"log"
	"strings"

	gitlab "gitlab.com/gitlab-org/api/client-go"
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
	Path     string         `json:"path"`
	Name     string         `json:"name"`
	Type     string         `json:"type"` // "tree" or "blob"
	Children []*FileTreeNode `json:"children,omitempty"`
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
// If recursive is true, it fetches the full subtree.
func (s *SpecsService) GetFileTree(client *gitlab.Client, projectID string, path string, ref string, recursive bool) ([]*FileTreeNode, error) {
	if ref == "" {
		ref = "main"
	}

	opts := &gitlab.ListTreeOptions{
		Path:      gitlab.Ptr(path),
		Ref:       gitlab.Ptr(ref),
		Recursive: gitlab.Ptr(recursive),
		ListOptions: gitlab.ListOptions{
			PerPage: 100,
		},
	}

	var allNodes []*gitlab.TreeNode
	for {
		nodes, resp, err := client.Repositories.ListTree(projectID, opts)
		if err != nil {
			log.Printf("[specs] ListTree error for project %s path %s: %v", projectID, path, err)
			return nil, fmt.Errorf("failed to list tree: %w", err)
		}
		allNodes = append(allNodes, nodes...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	if recursive {
		return buildNestedTree(allNodes, path), nil
	}

	// Non-recursive: return flat list
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

// buildNestedTree converts a flat list of recursive tree nodes into a nested tree structure.
func buildNestedTree(nodes []*gitlab.TreeNode, basePath string) []*FileTreeNode {
	type nodeMap = map[string]*FileTreeNode
	root := &FileTreeNode{
		Path:     basePath,
		Name:     basePath,
		Type:     "tree",
		Children: []*FileTreeNode{},
	}

	pathToNode := nodeMap{}
	pathToNode[basePath] = root

	for _, n := range nodes {
		treeNode := &FileTreeNode{
			Path: n.Path,
			Name: n.Name,
			Type: n.Type,
		}
		if n.Type == "tree" {
			treeNode.Children = []*FileTreeNode{}
		}
		pathToNode[n.Path] = treeNode
	}

	// Ensure all ancestor directories exist in the map.
	// GitLab's recursive ListTree may omit intermediate directory entries,
	// which would cause their descendants to be silently dropped.
	for _, n := range nodes {
		if n.Type == "tree" {
			continue
		}
		dir := getParentPath(n.Path)
		for dir != "" && dir != basePath {
			if _, ok := pathToNode[dir]; !ok {
				pathToNode[dir] = &FileTreeNode{
					Path:     dir,
					Name:     getLastSegment(dir),
					Type:     "tree",
					Children: []*FileTreeNode{},
				}
			}
			dir = getParentPath(dir)
		}
	}

	// Attach children to parents
	for _, n := range nodes {
		child := pathToNode[n.Path]
		parentPath := getParentPath(n.Path)
		if parent, ok := pathToNode[parentPath]; ok {
			parent.Children = append(parent.Children, child)
		}
	}

	return root.Children
}

func getParentPath(path string) string {
	idx := strings.LastIndex(path, "/")
	if idx <= 0 {
		return ""
	}
	return path[:idx]
}

func getLastSegment(path string) string {
	idx := strings.LastIndex(path, "/")
	if idx < 0 {
		return path
	}
	return path[idx+1:]
}

// --- File CRUD ---

// GetFile retrieves a file's content from the repository.
func (s *SpecsService) GetFile(client *gitlab.Client, projectID string, filePath string, ref string) (*FileContent, error) {
	if ref == "" {
		ref = "main"
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

// SearchTree searches for files matching a query in the tree.
func (s *SpecsService) SearchTree(client *gitlab.Client, projectID string, path string, ref string, query string) ([]*FileTreeNode, error) {
	nodes, err := s.GetFileTree(client, projectID, path, ref, true)
	if err != nil {
		return nil, err
	}
	return filterTree(nodes, query), nil
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
