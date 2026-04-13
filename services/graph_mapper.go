package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"time"

	"qa-extension-backend/database"
	"qa-extension-backend/internal/models"

	"github.com/redis/go-redis/v9"
	gitlab "gitlab.com/gitlab-org/api/client-go"
	"google.golang.org/genai"
)

const (
	GraphMapCacheTTL = 24 * time.Hour
	GraphMapKeyPrefix = "graph_map"
)

// =============================================================================
// MODULE CATALOG - The enriched map structure
// =============================================================================

// ModuleCatalog is the complete enriched knowledge map for a project/branch
type ModuleCatalog struct {
	ProjectID   string                 `json:"projectId"`
	Branch      string                 `json:"branch"`
	GeneratedAt time.Time              `json:"generatedAt"`
	Modules     map[string]ModuleEntry `json:"modules"`
	RouteIndex  map[string]string      `json:"routeIndex"` // route → moduleKey
}

// ModuleEntry represents a functional module in the application
type ModuleEntry struct {
	DisplayName      string                `json:"displayName"`
	Description      string                `json:"description"`
	Features         []string              `json:"features"`          // e.g. ["list", "create", "edit", "delete", "search", "filter"]
	NavigationPath   []string              `json:"navigationPath"`   // e.g. ["Master Data", "Entity Districts"]
	Routes           map[string]RouteEntry `json:"routes"`
}

// RouteEntry represents a single route/page in the module
type RouteEntry struct {
	FilePath         string            `json:"filePath"`
	Description      string            `json:"description"`
	KeyElements      map[string]string `json:"keyElements"`      // e.g. "searchInput" → "entity-districts-search-input"
	AvailableActions []string          `json:"availableActions"` // e.g. ["search", "filter", "sort"]
}

// =============================================================================
// ROUTE MAPPER - Simple path parsing, no LLM needed
// =============================================================================

// RouteMapper converts Next.js App Router file paths to routes
type RouteMapper struct{}

// NewRouteMapper creates a new RouteMapper
func NewRouteMapper() *RouteMapper {
	return &RouteMapper{}
}

// BuildRouteMapFromFileList takes a list of file paths and returns route mappings
func (m *RouteMapper) BuildRouteMapFromFileList(paths []string) map[string]string {
	routeMap := make(map[string]string) // route → filePath

	for _, path := range paths {
		route := m.filePathToRoute(path)
		if route == "" {
			continue
		}
		routeMap[route] = path
	}

	return routeMap
}

// filePathToRoute converts a Next.js App Router file path to a route
func (m *RouteMapper) filePathToRoute(path string) string {
	// Must be in app/ or pages/ directory
	hasAppDir := strings.Contains(path, "/app/")
	hasPagesDir := strings.Contains(path, "/pages/")

	if !hasAppDir && !hasPagesDir {
		return ""
	}

	var remainder string
	if hasAppDir {
		idx := strings.Index(path, "/app/")
		remainder = path[idx+5:] // +5 to skip "/app"
	} else {
		idx := strings.Index(path, "/pages/")
		remainder = path[idx+7:] // +7 to skip "/pages"
	}

	// Handle route groups: (groupname) → stripped
	remainder = m.stripRouteGroups(remainder)

	// Split by /
	parts := strings.Split(remainder, "/")

	// Filter out empty parts and route group leftovers
	var routeParts []string
	for _, part := range parts {
		// Skip empty strings and full route group patterns like "(auth)"
		if part == "" {
			continue
		}
		// Skip route groups
		if strings.HasPrefix(part, "(") && strings.HasSuffix(part, ")") {
			continue
		}
		routeParts = append(routeParts, part)
	}

	// Remove page file if present
	if len(routeParts) > 0 {
		last := routeParts[len(routeParts)-1]
		if strings.HasPrefix(last, "page.") {
			routeParts = routeParts[:len(routeParts)-1]
		}
	}

	// Convert dynamic segments: [id] → :id, [...slug] → :slug*
	var finalParts []string
	for _, part := range routeParts {
		converted := m.convertDynamicSegments(part)
		finalParts = append(finalParts, converted)
	}

	// Build route
	if len(finalParts) == 0 {
		return "/"
	}

	route := "/" + strings.Join(finalParts, "/")
	return route
}

func (m *RouteMapper) stripRouteGroups(path string) string {
	re := regexp.MustCompile(`\([^)]+\)/?`)
	return re.ReplaceAllString(path, "")
}

func (m *RouteMapper) convertDynamicSegments(part string) string {
	// [...slug] → :slug* (catch-all)
	if strings.HasPrefix(part, "[...") && strings.HasSuffix(part, "]") {
		slug := part[4 : len(part)-1]
		return ":" + slug + "*"
	}
	// [id] → :id
	if strings.HasPrefix(part, "[") && strings.HasSuffix(part, "]") {
		slug := part[1 : len(part)-1]
		return ":" + slug
	}
	return part
}

// =============================================================================
// GRAPH MAPPER - LLM enrichment for module descriptions
// =============================================================================

// GraphMapper generates enriched module catalog using LLM
type GraphMapper struct {
	routeMapper *RouteMapper
}

// NewGraphMapper creates a new GraphMapper
func NewGraphMapper() *GraphMapper {
	return &GraphMapper{
		routeMapper: NewRouteMapper(),
	}
}

// FetchAndEnrichCatalog fetches the file tree, builds route map, enriches with LLM, and returns the catalog
func (m *GraphMapper) FetchAndEnrichCatalog(
	ctx context.Context,
	glClient *gitlab.Client,
	projectID string,
	branch string,
) (*ModuleCatalog, error) {

	// Check cache first
	catalog, err := m.GetCachedCatalog(ctx, projectID, branch)
	if err == nil && catalog != nil {
		log.Printf("[GraphMapper] Cache HIT for %s/%s", projectID, branch)
		return catalog, nil
	}

	log.Printf("[GraphMapper] Cache MISS for %s/%s, generating...", projectID, branch)

	// Fetch file tree
	files, err := m.fetchFileTree(glClient, projectID, branch)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch file tree: %w", err)
	}

	// Build route map (instant, no LLM)
	routeMap := m.routeMapper.BuildRouteMapFromFileList(files)
	if len(routeMap) == 0 {
		return nil, fmt.Errorf("no routes found in project")
	}

	// Fetch source files for enrichment (app/, pages/, components/)
	sourceFiles, err := m.fetchSourceFilesForEnrichment(ctx, glClient, projectID, branch, files)
	if err != nil {
		log.Printf("[GraphMapper] Warning: failed to fetch source files: %v", err)
		// Continue anyway - LLM can work with minimal context
	}

	// Enrich with LLM
	catalog, err = m.enrichWithLLM(ctx, projectID, branch, routeMap, sourceFiles)
	if err != nil {
		return nil, fmt.Errorf("failed to enrich with LLM: %w", err)
	}

	// Cache it
	if err := m.CacheCatalog(ctx, catalog); err != nil {
		log.Printf("[GraphMapper] Warning: failed to cache catalog: %v", err)
	}

	return catalog, nil
}

// fetchFileTree fetches the complete file tree from GitLab
func (m *GraphMapper) fetchFileTree(glClient *gitlab.Client, projectID, branch string) ([]string, error) {
	var allFiles []string

	opt := &gitlab.ListTreeOptions{
		Recursive: gitlab.Ptr(true),
		ListOptions: gitlab.ListOptions{
			PerPage: 100,
		},
	}

	for {
		treeNode, resp, err := glClient.Repositories.ListTree(projectID, opt)
		if err != nil {
			return nil, err
		}

		for _, node := range treeNode {
			if node.Type == "blob" {
				allFiles = append(allFiles, node.Path)
			}
		}

		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}

	return allFiles, nil
}

// fetchSourceFilesForEnrichment fetches key source files for LLM enrichment
func (m *GraphMapper) fetchSourceFilesForEnrichment(
	ctx context.Context,
	glClient *gitlab.Client,
	projectID, branch string,
	allFiles []string,
) (map[string]string, error) {
	sourceFiles := make(map[string]string)

	// Priority directories
	priorityDirs := []string{"app/", "pages/", "components/"}

	// Filter to relevant files
	var relevantPaths []string
	for _, path := range allFiles {
		for _, dir := range priorityDirs {
			if strings.HasPrefix(path, dir) && isSourceFile(path) {
				relevantPaths = append(relevantPaths, path)
				break
			}
		}
	}

	// Limit to prevent huge context
	maxFiles := 30
	if len(relevantPaths) > maxFiles {
		relevantPaths = relevantPaths[:maxFiles]
	}

	// Fetch each file
	for _, path := range relevantPaths {
		content, err := m.fetchFileContent(glClient, projectID, branch, path)
		if err != nil {
			continue
		}
		sourceFiles[path] = content
	}

	return sourceFiles, nil
}

func (m *GraphMapper) fetchFileContent(glClient *gitlab.Client, projectID, branch, path string) (string, error) {
	fileOpt := &gitlab.GetFileOptions{
		Ref: gitlab.Ptr(branch),
	}
	file, _, err := glClient.RepositoryFiles.GetFile(projectID, path, fileOpt)
	if err != nil {
		return "", err
	}

	contentBytes, err := decodeBase64(file.Content)
	if err != nil {
		return "", err
	}

	return string(contentBytes), nil
}

func decodeBase64(s string) ([]byte, error) {
	// Handle standard base64
	if len(s)%4 == 0 {
		if decoded, err := base64.StdEncoding.DecodeString(s); err == nil {
			return decoded, nil
		}
	}
	// Try URL-safe base64
	if decoded, err := base64.URLEncoding.DecodeString(s); err == nil {
		return decoded, nil
	}
	// Try raw base64
	return base64.RawStdEncoding.DecodeString(s)
}

// EnrichWithLLM calls the LLM to generate module descriptions and features
func (m *GraphMapper) enrichWithLLM(
	ctx context.Context,
	projectID, branch string,
	routeMap map[string]string,
	sourceFiles map[string]string,
) (*ModuleCatalog, error) {

	projectIDEnv := os.Getenv("GOOGLE_CLOUD_PROJECT")
	location := os.Getenv("VERTEX_LOCATION")
	if location == "" {
		location = "us-central1"
	}

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:  genai.BackendVertexAI,
		Project:  projectIDEnv,
		Location: location,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create genai client: %w", err)
	}

	// Build the prompt
	routesJSON, _ := json.MarshalIndent(routeMap, "", "  ")

	filesSummary := m.buildFilesSummary(sourceFiles)

	prompt := fmt.Sprintf(`You are a code structure analyzer. Analyze the provided source code files and route map to create a module catalog.

### ROUTE MAP (route → file path):
%s

### SOURCE FILES SUMMARY:
%s

### INSTRUCTIONS:
1. Group the routes into functional MODULES based on their paths and code content
2. For each module, determine:
   - displayName: Human-readable name (e.g., "Master Data - Entity Districts")
   - description: What the module does (1-2 sentences)
   - features: List of available features (e.g., ["list", "create", "edit", "delete", "search", "filter", "sort"])
   - navigationPath: How to navigate to this module (menu hierarchy, e.g., ["Master Data", "Entity Districts"])
3. For each route in a module, determine:
   - description: What the page does
   - keyElements: Important UI elements (search inputs, filters, tables, action buttons) with semantic names
   - availableActions: What actions users can perform on this page

### OUTPUT FORMAT:
Return ONLY a JSON object with this exact structure:
{
  "modules": {
    "module-key": {
      "displayName": "string",
      "description": "string",
      "features": ["list", "create", ...],
      "navigationPath": ["Menu", "Submenu"],
      "routes": {
        "/route/path": {
          "filePath": "app/path/page.tsx",
          "description": "string",
          "keyElements": {
            "searchInput": "element-testid",
            "dataTable": "table-testid",
            ...
          },
          "availableActions": ["search", "filter", ...]
        }
      }
    }
  },
  "routeIndex": {
    "/route/path": "module-key",
    ...
  }
}

Rules:
- Use lowercase-with-hyphens for module keys (e.g., "entity-districts", "invoice-otc")
- keyElements should map semantic names to actual testid values found in the code
- availableActions should match the features list but scoped to what's available on THIS specific page
- Be specific about testid values - they must match exactly what's in the source code
`, routesJSON, filesSummary)

	// Call LLM
	config := &genai.GenerateContentConfig{
		Temperature:      genai.Ptr(float32(0.3)),
		ResponseMIMEType: "application/json",
	}

	// Use gemini-3.1-pro for the heavy lifting (smarter model for structured output)
	resp, err := client.Models.GenerateContent(
		ctx,
		"gemini-3.1-pro-preview",
		genai.Text(prompt),
		config,
	)
	if err != nil {
		return nil, fmt.Errorf("LLM call failed: %w", err)
	}

	responseText := extractResponseText(resp)
	if responseText == "" {
		log.Printf("[GraphMapper] LLM returned empty response. Prompt length: %d chars, Route count: %d, Files: %d",
			len(prompt), len(routeMap), len(sourceFiles))
		return nil, fmt.Errorf("empty response from LLM")
	}

	log.Printf("[GraphMapper] LLM response length: %d chars", len(responseText))

	// Parse into a raw map first
	var rawResult map[string]interface{}
	if err := json.Unmarshal([]byte(responseText), &rawResult); err != nil {
		log.Printf("[GraphMapper] Failed to parse LLM response. Response preview: %.200s...", responseText)
		return nil, fmt.Errorf("failed to parse LLM response: %w", err)
	}

	// Convert to ModuleCatalog
	catalog := &ModuleCatalog{
		ProjectID:   projectID,
		Branch:      branch,
		GeneratedAt: time.Now(),
		Modules:     make(map[string]ModuleEntry),
		RouteIndex:  make(map[string]string),
	}

	if modulesRaw, ok := rawResult["modules"].(map[string]interface{}); ok {
		for key, moduleValue := range modulesRaw {
			moduleEntry := ModuleEntry{
				Routes: make(map[string]RouteEntry),
			}
			if moduleMap, ok := moduleValue.(map[string]interface{}); ok {
				if displayName, ok := moduleMap["displayName"].(string); ok {
					moduleEntry.DisplayName = displayName
				}
				if description, ok := moduleMap["description"].(string); ok {
					moduleEntry.Description = description
				}
				if features, ok := moduleMap["features"].([]interface{}); ok {
					for _, f := range features {
						if s, ok := f.(string); ok {
							moduleEntry.Features = append(moduleEntry.Features, s)
						}
					}
				}
				if navPath, ok := moduleMap["navigationPath"].([]interface{}); ok {
					for _, n := range navPath {
						if s, ok := n.(string); ok {
							moduleEntry.NavigationPath = append(moduleEntry.NavigationPath, s)
						}
					}
				}
				if routesRaw, ok := moduleMap["routes"].(map[string]interface{}); ok {
					for routePath, routeValue := range routesRaw {
						routeEntry := RouteEntry{}
						if routeMap_, ok := routeValue.(map[string]interface{}); ok {
							if filePath, ok := routeMap_["filePath"].(string); ok {
								routeEntry.FilePath = filePath
							}
							if desc, ok := routeMap_["description"].(string); ok {
								routeEntry.Description = desc
							}
							if keyEl, ok := routeMap_["keyElements"].(map[string]interface{}); ok {
								routeEntry.KeyElements = make(map[string]string)
								for k, v := range keyEl {
									if s, ok := v.(string); ok {
										routeEntry.KeyElements[k] = s
									}
								}
							}
							if actions, ok := routeMap_["availableActions"].([]interface{}); ok {
								for _, a := range actions {
									if s, ok := a.(string); ok {
										routeEntry.AvailableActions = append(routeEntry.AvailableActions, s)
									}
								}
							}
						}
						moduleEntry.Routes[routePath] = routeEntry
						catalog.RouteIndex[routePath] = key
					}
				}
			}
			catalog.Modules[key] = moduleEntry
		}
	}

	if len(catalog.Modules) == 0 {
		return nil, fmt.Errorf("LLM returned no modules")
	}

	return catalog, nil
}

func (m *GraphMapper) buildFilesSummary(sourceFiles map[string]string) string {
	var lines []string
	for path, content := range sourceFiles {
		// Truncate each file to first 80 lines (testids are usually at top of file)
		lineSlice := strings.Split(content, "\n")
		if len(lineSlice) > 80 {
			lineSlice = lineSlice[:80]
		}
		truncated := strings.Join(lineSlice, "\n")

		lines = append(lines, fmt.Sprintf("=== FILE: %s ===\n%s\n", path, truncated))
	}
	return strings.Join(lines, "\n")
}

// =============================================================================
// CACHE OPERATIONS
// =============================================================================

// GetCachedCatalog retrieves the catalog from Redis cache
func (m *GraphMapper) GetCachedCatalog(ctx context.Context, projectID, branch string) (*ModuleCatalog, error) {
	key := m.cacheKey(projectID, branch)

	val, err := database.RedisClient.Get(ctx, key).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var catalog ModuleCatalog
	if err := json.Unmarshal([]byte(val), &catalog); err != nil {
		return nil, err
	}

	return &catalog, nil
}

// CacheCatalog stores the catalog in Redis
func (m *GraphMapper) CacheCatalog(ctx context.Context, catalog *ModuleCatalog) error {
	key := m.cacheKey(catalog.ProjectID, catalog.Branch)

	data, err := json.Marshal(catalog)
	if err != nil {
		return err
	}

	return database.RedisClient.Set(ctx, key, data, GraphMapCacheTTL).Err()
}

// InvalidateCatalog removes the catalog from cache
func (m *GraphMapper) InvalidateCatalog(ctx context.Context, projectID, branch string) error {
	key := m.cacheKey(projectID, branch)
	return database.RedisClient.Del(ctx, key).Err()
}

func (m *GraphMapper) cacheKey(projectID, branch string) string {
	return fmt.Sprintf("%s:%s:%s", GraphMapKeyPrefix, projectID, branch)
}

// =============================================================================
// LOOKUP HELPERS (for test generator)
// =============================================================================

// GetModuleForRoute finds the module containing a given route
func (c *ModuleCatalog) GetModuleForRoute(route string) *ModuleEntry {
	moduleKey, ok := c.RouteIndex[route]
	if !ok {
		return nil
	}
	module, ok := c.Modules[moduleKey]
	if !ok {
		return nil
	}
	return &module
}

// GetRouteEntry finds the route entry for a given route
func (c *ModuleCatalog) GetRouteEntry(route string) *RouteEntry {
	module := c.GetModuleForRoute(route)
	if module == nil {
		return nil
	}
	routeEntry, ok := module.Routes[route]
	if !ok {
		return nil
	}
	return &routeEntry
}

// FindModuleByName finds a module by its display name or key (fuzzy match)
func (c *ModuleCatalog) FindModuleByName(query string) *ModuleEntry {
	queryLower := strings.ToLower(query)

	// Direct match on key
	if module, ok := c.Modules[query]; ok {
		return &module
	}

	// Fuzzy match on display name
	for _, module := range c.Modules {
		displayLower := strings.ToLower(module.DisplayName)
		if strings.Contains(displayLower, queryLower) || strings.Contains(queryLower, displayLower) {
			return &module
		}
	}

	// Also match on description keywords
	for _, module := range c.Modules {
		descLower := strings.ToLower(module.Description)
		if strings.Contains(descLower, queryLower) || strings.Contains(queryLower, descLower) {
			return &module
		}
	}

	// Match on features
	for _, module := range c.Modules {
		for _, feature := range module.Features {
			if strings.Contains(strings.ToLower(feature), queryLower) {
				return &module
			}
		}
	}

	return nil
}

// InferRouteFromName tries to find the best matching route for a test case name
// It uses the module context + action words to narrow down the specific route
func (c *ModuleCatalog) InferRouteFromName(testCaseName string, module *ModuleEntry) string {
	if module == nil {
		return ""
	}

	testNameLower := strings.ToLower(testCaseName)
	words := strings.Fields(testNameLower)

	// Action words that indicate the route type
	actionWords := map[string]string{
		"list":    "/list",
		"create":  "/create",
		"new":     "/create",
		"add":     "/create",
		"edit":    "/edit",
		"update":  "/edit",
		"delete":  "/delete",
		"remove":  "/delete",
		"view":    "/view",
		"detail":  "/view",
		"show":    "/view",
		"search":  "/list",
		"filter":  "/list",
		"sort":    "/list",
	}

	// First, try to find a route that matches the action
	for _, word := range words {
		if action, ok := actionWords[word]; ok {
			// Look for a route ending with this action
			for route := range module.Routes {
				if strings.HasSuffix(route, action) {
					return route
				}
			}
			// Also try without leading slash
			for route := range module.Routes {
				if strings.HasSuffix(route, action) || strings.HasSuffix(route, "/"+word) {
					return route
				}
			}
		}
	}

	// If no action match, try keyword matching on route path
	for route := range module.Routes {
		routeLower := strings.ToLower(route)
		matchCount := 0
		for _, word := range words {
			if len(word) > 2 && strings.Contains(routeLower, word) {
				matchCount++
			}
		}
		// If multiple words match, likely the right route
		if matchCount >= 2 {
			return route
		}
	}

	// Fallback: return the first route (usually the list page)
	for route := range module.Routes {
		return route
	}

	return ""
}

// InferRouteFromSheetName finds routes that belong to a module matching the sheet name
func (c *ModuleCatalog) InferRoutesFromSheetName(sheetName string) []string {
	module := c.FindModuleByName(sheetName)
	if module == nil {
		return nil
	}

	var routes []string
	for route := range module.Routes {
		routes = append(routes, route)
	}
	return routes
}

// InferRouteFromSheetAndTestCase combines sheet context with test case name to find the best route
func (c *ModuleCatalog) InferRouteFromSheetAndTestCase(sheetName, testCaseName string) string {
	module := c.FindModuleByName(sheetName)
	if module == nil {
		// Try just the test case name
		module = c.FindModuleByName(testCaseName)
	}
	if module == nil {
		return ""
	}

	return c.InferRouteFromName(testCaseName, module)
}

// =============================================================================
// UTILITIES
// =============================================================================

func isSourceFile(path string) bool {
	lowerPath := strings.ToLower(path)
	validExts := []string{".tsx", ".ts", ".jsx", ".js"}
	for _, ext := range validExts {
		if strings.HasSuffix(lowerPath, ext) {
			return true
		}
	}
	return false
}

// FetchCodebaseWithCatalog fetches only the files relevant to the given routes using the catalog
// It returns a CodebaseContext and a converted KnowledgeGraph for backward compatibility with test_generator
func (m *GraphMapper) FetchCodebaseWithCatalog(
	ctx context.Context,
	glClient *gitlab.Client,
	projectID, branch string,
	catalog *ModuleCatalog,
	routes []string,
) (*CodebaseContext, *models.KnowledgeGraph, error) {

	// Collect unique file paths needed for these routes
	filePaths := make(map[string]bool)
	for _, route := range routes {
		routeEntry := catalog.GetRouteEntry(route)
		if routeEntry != nil && routeEntry.FilePath != "" {
			filePaths[routeEntry.FilePath] = true
		}
	}

	// Fetch each file
	var files []SourceFile
	totalChars := 0

	for filePath := range filePaths {
		content, err := m.fetchFileContent(glClient, projectID, branch, filePath)
		if err != nil {
			log.Printf("[GraphMapper] Warning: failed to fetch %s: %v", filePath, err)
			continue
		}

		files = append(files, SourceFile{
			Path:    filePath,
			Content: content,
		})
		totalChars += len(content)
	}

	codebaseCtx := &CodebaseContext{
		ProjectName: projectID,
		Files:       files,
		TotalTokens: totalChars / 4,
	}

	// Convert catalog to KnowledgeGraph for backward compatibility with test_generator
	kg := m.catalogToKnowledgeGraph(catalog)

	return codebaseCtx, kg, nil
}

// catalogToKnowledgeGraph converts a ModuleCatalog to the legacy KnowledgeGraph format
func (m *GraphMapper) catalogToKnowledgeGraph(catalog *ModuleCatalog) *models.KnowledgeGraph {
	kg := &models.KnowledgeGraph{
		Git: struct {
			CommitSHA string `json:"commit_sha"`
			Branch    string `json:"branch"`
		}{
			Branch: catalog.Branch,
		},
		RouteSummary:  make(map[string]models.RouteInfo),
		SelectorIndex: make(map[string]models.SelectorEntry),
		Stats: models.KnowledgeGraphStats{},
	}

	for _, module := range catalog.Modules {
		for route, routeEntry := range module.Routes {
			ri := models.RouteInfo{
				PageID:  routeEntry.FilePath,
				Module:  module.DisplayName,
				Testids: []models.TestidEntry{},
				Forms:   []models.FormEntry{},
				Hooks:   []models.HookEntry{},
				APIs:    []models.APIEntry{},
			}

			// Convert key elements to testid entries
			for elemName, testid := range routeEntry.KeyElements {
				// Determine element type from name heuristics
				elementType := "div"
				if strings.Contains(elemName, "input") || strings.Contains(elemName, "search") || strings.Contains(elemName, "filter") {
					elementType = "input"
				} else if strings.Contains(elemName, "button") || strings.Contains(elemName, "btn") {
					elementType = "button"
				} else if strings.Contains(elemName, "table") || strings.Contains(elemName, "row") {
					elementType = "table"
				} else if strings.Contains(elemName, "select") || strings.Contains(elemName, "dropdown") {
					elementType = "select"
				}

				te := models.TestidEntry{
					Testid:      testid,
					ElementType: elementType,
					Action:      inferActionFromName(elemName),
					SuggestedSelectors: []models.SuggestedSelector{
						{Type: "testid", Value: fmt.Sprintf("[data-testid='%s']", testid), Confidence: 0.95},
						{Type: "css", Value: fmt.Sprintf("%s[data-testid='%s']", elementType, testid), Confidence: 0.8},
					},
				}
				ri.Testids = append(ri.Testids, te)

				// Add to selector index
				kg.SelectorIndex[testid] = models.SelectorEntry{
					PageID:      route,
					SelectorID:  testid,
					ElementType: elementType,
				}
			}

			kg.RouteSummary[route] = ri
			kg.Stats.TotalSelectors += len(ri.Testids)
		}

		kg.Stats.TotalPages += len(module.Routes)
		kg.Stats.TotalForms += len(module.Routes) // Approximate
	}

	return kg
}

// inferActionFromName guesses the action type from element name
func inferActionFromName(name string) string {
	nameLower := strings.ToLower(name)
	if strings.Contains(nameLower, "search") || strings.Contains(nameLower, "input") {
		return "fill"
	}
	if strings.Contains(nameLower, "button") || strings.Contains(nameLower, "btn") {
		if strings.Contains(nameLower, "submit") || strings.Contains(nameLower, "save") {
			return "submit"
		}
		if strings.Contains(nameLower, "delete") {
			return "delete"
		}
		if strings.Contains(nameLower, "edit") || strings.Contains(nameLower, "update") {
			return "edit"
		}
		return "click"
	}
	if strings.Contains(nameLower, "filter") || strings.Contains(nameLower, "select") {
		return "select"
	}
	if strings.Contains(nameLower, "table") || strings.Contains(nameLower, "row") {
		return "view"
	}
	return "click"
}

func extractResponseText(resp *genai.GenerateContentResponse) string {
	if resp == nil || len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		return ""
	}
	var b strings.Builder
	for _, part := range resp.Candidates[0].Content.Parts {
		if part.Text != "" {
			b.WriteString(part.Text)
		}
	}
	res := strings.TrimSpace(b.String())
	res = strings.TrimPrefix(res, "```json")
	res = strings.TrimPrefix(res, "```")
	res = strings.TrimSuffix(res, "```")
	res = strings.TrimSuffix(res, "'''")
	return strings.TrimSpace(res)
}
