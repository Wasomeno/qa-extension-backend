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
func FetchCodebaseContext(client *gitlab.Client, projectID string, targetKeyword string) (*CodebaseContext, error) {
	// First, fetch project details
	project, _, err := client.Projects.GetProject(projectID, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get project: %w", err)
	}

	// Fetch repository tree (recursively)
	var allFiles []*gitlab.TreeNode

	// We only care about specific directories to save time
	searchDirs := []string{"", "src", "pages", "components", "app", "views", "routes", "api", "commons", "modules"}
	
	for _, dir := range searchDirs {
		var pathPtr *string
		if dir != "" {
			pathPtr = gitlab.Ptr(dir)
		}
		
		opt := &gitlab.ListTreeOptions{
			Path:      pathPtr,
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
	// Deduplicate files by path before sorting (since root "" search might overlap with "src")
	uniqueFiles := make(map[string]*gitlab.TreeNode)
	for _, node := range allFiles {
		uniqueFiles[node.Path] = node
	}
	
	var deduplicated []*gitlab.TreeNode
	for _, node := range uniqueFiles {
		deduplicated = append(deduplicated, node)
	}

	allFiles = sortFilesByTargetedRelevance(deduplicated, targetKeyword)

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
	if strings.Contains(lowerPath, ".test.") || strings.Contains(lowerPath, ".spec.") {
		return false
	}

	// Always allow framework config files for LLM detection
	if strings.Contains(lowerPath, "vite.config") || strings.Contains(lowerPath, "next.config") {
		return true
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

func sortFilesByTargetedRelevance(files []*gitlab.TreeNode, targetKeyword string) []*gitlab.TreeNode {
	targetKeyword = strings.ToLower(targetKeyword)

	var configs []*gitlab.TreeNode
	var constantsMatch []*gitlab.TreeNode
	var exactApiMatch []*gitlab.TreeNode
	var exactUiMatch []*gitlab.TreeNode
	var others []*gitlab.TreeNode

	for _, f := range files {
		lowerPath := strings.ToLower(f.Path)

		// 1. Framework configs (needed for framework detection)
		if strings.Contains(lowerPath, "vite.config") || strings.Contains(lowerPath, "next.config") {
			configs = append(configs, f)
			continue
		}

		// 2. Constants (always needed for routing and endpoints)
		if strings.Contains(lowerPath, "constants/route") || strings.Contains(lowerPath, "endpoint") {
			constantsMatch = append(constantsMatch, f)
			continue
		}

		// If no target keyword provided, fallback to basic UI sorting
		if targetKeyword == "" {
			if strings.Contains(lowerPath, "/pages/") || strings.Contains(lowerPath, "/routes/") || strings.Contains(lowerPath, "page.") {
				exactUiMatch = append(exactUiMatch, f)
			} else if strings.Contains(lowerPath, "/components/") || strings.Contains(lowerPath, "/views/") {
				constantsMatch = append(constantsMatch, f) // hack to put components high
			} else {
				others = append(others, f)
			}
			continue
		}

		// 3. Exact match on API folder (e.g., src/api/auth/api.ts)
		if strings.Contains(lowerPath, "/api/") && strings.Contains(lowerPath, targetKeyword) {
			exactApiMatch = append(exactApiMatch, f)
			continue
		}

		// 4. Exact match on UI folder
		if (strings.Contains(lowerPath, "/pages/") || strings.Contains(lowerPath, "/components/") || strings.Contains(lowerPath, "/modules/")) && 
			strings.Contains(lowerPath, targetKeyword) {
			exactUiMatch = append(exactUiMatch, f)
			continue
		}

		// 5. Everything else
		others = append(others, f)
	}

	var result []*gitlab.TreeNode
	result = append(result, configs...)
	result = append(result, constantsMatch...)
	result = append(result, exactApiMatch...)
	result = append(result, exactUiMatch...)
	result = append(result, others...)

	return result
}
