package services

import (
	"encoding/base64"
	"fmt"
	"strings"

	gitlab "gitlab.com/gitlab-org/api/client-go"
)

// CodebaseContext represents the fetched source code for AI context
type CodebaseContext struct {
	ProjectName string       `json:"projectName"`
	Files       []SourceFile `json:"files"`
	TotalTokens int          `json:"totalTokens"`
}

// SourceFile represents a single source code file
type SourceFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Configurable limits
const MaxFilesToFetch = 30
const MaxTokensApprox = 80000 // Very rough estimate (1 token ~ 4 chars)

// FetchCodebaseContext fetches relevant source code from a GitLab project
func FetchCodebaseContext(client *gitlab.Client, projectID string) (*CodebaseContext, error) {
	// First, fetch project details
	project, _, err := client.Projects.GetProject(projectID, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get project: %w", err)
	}

	// Fetch repository tree (recursively)
	var allFiles []*gitlab.TreeNode

	// We only care about specific directories to save time
	searchDirs := []string{"src", "pages", "components", "app", "views", "routes"}
	
	for _, dir := range searchDirs {
		opt := &gitlab.ListTreeOptions{
			Path:      gitlab.Ptr(dir),
			Recursive: gitlab.Ptr(true),
			ListOptions: gitlab.ListOptions{
				PerPage: 100,
			},
		}

		for {
			treeNode, resp, err := client.Repositories.ListTree(projectID, opt)
			if err != nil {
				// E.g. directory doesn't exist, just suppress error and continue to next dir
				break
			}

			// Filter for files we care about
			for _, node := range treeNode {
				if node.Type == "blob" && isRelevantFile(node.Path) {
					allFiles = append(allFiles, node)
				}
			}

			if resp.NextPage == 0 {
				break
			}
			opt.Page = resp.NextPage
		}
	}

	// If no files found in specific dirs, try fetching from root (non-recursive to avoid huge trees, maybe just root-level stuff)
	if len(allFiles) == 0 {
		opt := &gitlab.ListTreeOptions{
			Recursive: gitlab.Ptr(true),
			ListOptions: gitlab.ListOptions{
				PerPage: 100,
			},
		}
		for {
			treeNode, resp, err := client.Repositories.ListTree(projectID, opt)
			if err != nil {
				break
			}
			for _, node := range treeNode {
				if node.Type == "blob" && isRelevantFile(node.Path) {
					allFiles = append(allFiles, node)
				}
			}
			if resp.NextPage == 0 {
				break
			}
			opt.Page = resp.NextPage
		}
	}

	// Sort files by relevance (heuristic: pages and routes are more important than utils)
	allFiles = sortFilesByRelevance(allFiles)

	// Fetch file contents up to budget
	var fetchedFiles []SourceFile
	totalChars := 0

	for _, node := range allFiles {
		if len(fetchedFiles) >= MaxFilesToFetch {
			break
		}

		// Estimate tokens based on characters so far (approx 4 chars per token)
		if totalChars/4 >= MaxTokensApprox {
			break
		}

		fileOpt := &gitlab.GetFileOptions{Ref: gitlab.Ptr(project.DefaultBranch)}
		file, _, err := client.RepositoryFiles.GetFile(projectID, node.Path, fileOpt)
		if err != nil {
			continue
		}

		contentBytes, err := base64.StdEncoding.DecodeString(file.Content)
		if err != nil {
			continue
		}

		contentStr := string(contentBytes)
		
		fetchedFiles = append(fetchedFiles, SourceFile{
			Path:    node.Path,
			Content: contentStr,
		})

		totalChars += len(contentStr)
	}

	return &CodebaseContext{
		ProjectName: project.Name,
		Files:       fetchedFiles,
		TotalTokens: totalChars / 4,
	}, nil
}

func isRelevantFile(path string) bool {
	lowerPath := strings.ToLower(path)
	
	// Skip tests, distinct from what we want the AI to read to generate scenarios
	// But as per user request, we might want to include them later if needed. For now, focus on source.
	if strings.Contains(lowerPath, ".test.") || strings.Contains(lowerPath, ".spec.") {
		return false
	}

	// Extensions
	validExts := []string{".tsx", ".jsx", ".vue", ".svelte", ".html", ".ts", ".js"}
	for _, ext := range validExts {
		if strings.HasSuffix(lowerPath, ext) {
			return true
		}
	}

	return false
}

func sortFilesByRelevance(files []*gitlab.TreeNode) []*gitlab.TreeNode {
	// A simple heuristic sorting: files in "pages" or "routes" first, then "components", then the rest
	var pages, components, others []*gitlab.TreeNode

	for _, f := range files {
		lowerPath := strings.ToLower(f.Path)
		if strings.Contains(lowerPath, "/pages/") || strings.Contains(lowerPath, "/routes/") || strings.Contains(lowerPath, "page.") || strings.Contains(lowerPath, "route.") {
			pages = append(pages, f)
		} else if strings.Contains(lowerPath, "/components/") || strings.Contains(lowerPath, "/views/") {
			components = append(components, f)
		} else {
			others = append(others, f)
		}
	}

	var result []*gitlab.TreeNode
	result = append(result, pages...)
	result = append(result, components...)
	result = append(result, others...)

	return result
}
