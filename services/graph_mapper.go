package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"qa-extension-backend/database"
	"qa-extension-backend/internal/models"

	"github.com/redis/go-redis/v9"
	gitlab "gitlab.com/gitlab-org/api/client-go"
	"google.golang.org/genai"
)

// =============================================================================
// CONFIGURATION
// =============================================================================

const (
	GraphMapCacheTTL = 24 * time.Hour
	GraphMapKeyPrefix = "graph_map"
	
	// LLM Model - change here to switch models
	// Options: gemini-3.1-pro-preview, gemini-3-flash-preview, gemini-2.0-flash-exp
	LLMModel = "gemini-3-flash-preview"
	
	// File fetching - fetch ALL files for complete coverage
	MaxLLMCallsPerCatalog = 3
	SelectorConfidenceThreshold = 0.7
)

// =============================================================================
// COVERAGE REPORTING
// =============================================================================

// CoverageReport contains statistics about the knowledge graph generation
type CoverageReport struct {
	TotalRoutes          int                   `json:"totalRoutes"`
	CoveredRoutes        int                   `json:"coveredRoutes"`
	TotalModules         int                   `json:"totalModules"`
	TotalSelectors       int                   `json:"totalSelectors"`
	PagesWithSelectors   int                   `json:"pagesWithSelectors"`
	PagesWithoutSelectors int                  `json:"pagesWithoutSelectors"`
	InvalidSelectors     int                   `json:"invalidSelectors"`
	LLMCalls             int                   `json:"llmCalls"`
	GenerationTimeMs     int64                 `json:"generationTimeMs"`
	ModuleStats          map[string]ModuleCoverage `json:"moduleStats"`
	SelectorBreakdown    map[string]int        `json:"selectorBreakdown"` // testid, id, placeholder, etc.
}

// ModuleCoverage contains coverage stats per module
type ModuleCoverage struct {
	TotalRoutes   int     `json:"totalRoutes"`
	CoveredRoutes int     `json:"coveredRoutes"`
	SelectorCount int     `json:"selectorCount"`
	CoverageRatio float64 `json:"coverageRatio"`
}

// GenerateCoverageReport creates a coverage report from the catalog
func (m *GraphMapper) GenerateCoverageReport(
	routeMap map[string]string,
	catalog *ModuleCatalog,
	selectorIndex map[string][]ExtractedSelector,
	llmCalls int,
	generationTimeMs int64,
) *CoverageReport {
	report := &CoverageReport{
		TotalRoutes:      len(routeMap),
		CoveredRoutes:    len(catalog.RouteIndex),
		TotalModules:     len(catalog.Modules),
		LLMCalls:         llmCalls,
		GenerationTimeMs: generationTimeMs,
		ModuleStats:     make(map[string]ModuleCoverage),
	}

	// Count selectors
	for _, selectors := range selectorIndex {
		report.TotalSelectors += len(selectors)
		if len(selectors) > 0 {
			report.PagesWithSelectors++
		}
	}

	// Count pages without selectors
	report.PagesWithoutSelectors = report.CoveredRoutes - report.PagesWithSelectors
	if report.PagesWithoutSelectors < 0 {
		report.PagesWithoutSelectors = 0
	}

	// Build module stats
	for moduleKey, module := range catalog.Modules {
		moduleRoutes := len(module.Routes)
		selectorCount := 0
		for routePath := range module.Routes {
			if filePath, ok := routeMap[routePath]; ok {
				if selectors, ok := selectorIndex[filePath]; ok {
					selectorCount += len(selectors)
				}
			}
		}
		
		coverage := 0.0
		if moduleRoutes > 0 {
			coverage = float64(selectorCount) / float64(moduleRoutes)
		}
		
		report.ModuleStats[moduleKey] = ModuleCoverage{
			TotalRoutes:   moduleRoutes,
			CoveredRoutes: moduleRoutes,
			SelectorCount: selectorCount,
			CoverageRatio: coverage,
		}
	}

	return report
}

// LogCoverageReport logs the coverage report with formatting
func (r *CoverageReport) Log() {
	log.Printf("=============================================")
	log.Printf("    KNOWLEDGE GRAPH COVERAGE REPORT")
	log.Printf("=============================================")
	log.Printf("Total Routes Discovered:    %d", r.TotalRoutes)
	log.Printf("Routes in Catalog:          %d (%.1f%%)", r.CoveredRoutes, 
		float64(r.CoveredRoutes)*100/float64(maxInt(r.TotalRoutes, 1)))
	log.Printf("Total Modules:              %d", r.TotalModules)
	log.Printf("Total Selectors Found:      %d", r.TotalSelectors)
	log.Printf("Pages with Selectors:       %d", r.PagesWithSelectors)
	log.Printf("Pages without Selectors:    %d ⚠️", r.PagesWithoutSelectors)
	log.Printf("LLM Calls:                  %d", r.LLMCalls)
	log.Printf("Generation Time:            %dms", r.GenerationTimeMs)
	log.Printf("---------------------------------------------")
	log.Printf("Module Coverage:")
	for moduleKey, stats := range r.ModuleStats {
		log.Printf("  %s: %d routes, %d selectors (%.1f%%)", 
			moduleKey, stats.TotalRoutes, stats.SelectorCount, stats.CoverageRatio*100)
	}
	log.Printf("=============================================")
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// sortPaths sorts paths by module for diverse coverage
func sortPaths(paths []string) {
	sort.Slice(paths, func(i, j int) bool {
		moduleI := getModuleFromPath(paths[i])
		moduleJ := getModuleFromPath(paths[j])
		if moduleI != moduleJ {
			return moduleI < moduleJ
		}
		return paths[i] < paths[j]
	})
}

// getModuleFromPath extracts the module name from a file path
func getModuleFromPath(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if part == "app" && i+1 < len(parts) {
			next := parts[i+1]
			if strings.HasPrefix(next, "(") && strings.HasSuffix(next, ")") {
				if i+2 < len(parts) {
					return parts[i+2]
				}
			}
			return next
		}
	}
	return "unknown"
}

// =============================================================================
// SELECTOR EXTRACTION - Programmatic extraction of actual selectors from code
// =============================================================================

// ExtractedSelector represents a UI element that can be selected
type ExtractedSelector struct {
	ElementType  string   // button, input, div, etc.
	Testid       string   // data-testid value (if exists)
	ID           string   // id attribute (if exists)
	Placeholder  string   // placeholder attribute (if exists)
	AriaLabel    string   // aria-label attribute (if exists)
	Text         string   // visible text content
	Title        string   // title attribute (for tooltips)
	Role         string   // role attribute (for accessibility)
	Classes      []string // class names
	Name         string   // name attribute (for form elements)
	InputType    string   // type attribute (for inputs)
	HTMLType     string   // htmlType attribute (for Ant Design buttons)
	FilePath     string   // which file this was found in
	LineNumber   int      // approximate line number
}

// ExtractSelectorsFromFile scans a full source file and extracts all selectable elements
func (m *GraphMapper) ExtractSelectorsFromFile(content, filePath string) []ExtractedSelector {
	var selectors []ExtractedSelector
	lines := strings.Split(content, "\n")

	// Regex patterns for extraction
	testidRe := regexp.MustCompile(`data-testid\s*=\s*["']([^"']+)["']`)
	idRe := regexp.MustCompile(`\bid\s*=\s*["']([^"']+)["']`)
	placeholderRe := regexp.MustCompile(`placeholder\s*=\s*["']([^"']+)["']`)
	ariaLabelRe := regexp.MustCompile(`aria-label\s*=\s*["']([^"']+)["']`)
	nameRe := regexp.MustCompile(`\bname\s*=\s*["']([^"']+)["']`)
	classRe := regexp.MustCompile(`className?\s*=\s*["']([^"']+)["']`)
	
	// Additional patterns for better coverage
	titleRe := regexp.MustCompile(`\btitle\s*=\s*["']([^"']+)["']`)
	roleRe := regexp.MustCompile(`\brole\s*=\s*["']([^"']+)["']`)
	typeRe := regexp.MustCompile(`\btype\s*=\s*["']([^"']+)["']`)
	htmlTypeRe := regexp.MustCompile(`\bhtmlType\s*=\s*["']([^"']+)["']`)
	
	// Ant Design component patterns
	antComponentRe := regexp.MustCompile(`<(Button|Input|Select|DatePicker|Form\.Item|Table|Tabs|Tab|Tooltip|Modal|Drawer|Dropdown|Menu|checkbox|Checkbox|Radio|Radio\.Group|Switch|InputNumber|TreeSelect|Cascader|TextArea|Textarea)[^>]*>`)
	antClosingRe := regexp.MustCompile(`</(Button|Input|Select|DatePicker|Form\.Item|Table|Tabs|Tab|Tooltip|Modal|Drawer|Dropdown|Menu|checkbox|Checkbox|Radio|Radio\.Group|Switch|InputNumber|TreeSelect|Cascader|TextArea|Textarea)`)

	for i, line := range lines {
		lineNum := i + 1

		// Skip comments
		if strings.Contains(line, "//") || strings.Contains(line, "/*") {
			continue
		}
		
		// Skip lines without JSX
		if !strings.Contains(line, "<") {
			continue
		}

		sel := ExtractedSelector{
			FilePath:   filePath,
			LineNumber: lineNum,
		}

		// Extract element type - check for Ant Design components first
		antMatch := antComponentRe.FindStringSubmatch(line)
		if len(antMatch) > 1 {
			sel.ElementType = antMatch[1]
		} else {
			// Standard HTML elements
			tagMatch := regexp.MustCompile(`<(button|input|div|span|a|form|table|tr|td|th|ul|li|label|select|option|textarea|img|h[1-6])[\s>]`).FindStringSubmatch(line)
			if len(tagMatch) > 1 {
				sel.ElementType = tagMatch[1]
			} else {
				// Try closing tag
				closingMatch := antClosingRe.FindStringSubmatch(line)
				if len(closingMatch) > 1 {
					sel.ElementType = closingMatch[1]
				} else {
					tagMatch = regexp.MustCompile(`</([a-zA-Z]+)`).FindStringSubmatch(line)
					if len(tagMatch) > 1 {
						sel.ElementType = tagMatch[1]
					}
				}
			}
		}

		// Extract attributes
		if matches := testidRe.FindStringSubmatch(line); len(matches) > 1 {
			sel.Testid = matches[1]
		}
		if matches := idRe.FindStringSubmatch(line); len(matches) > 1 {
			sel.ID = matches[1]
		}
		if matches := placeholderRe.FindStringSubmatch(line); len(matches) > 1 {
			sel.Placeholder = matches[1]
		}
		if matches := ariaLabelRe.FindStringSubmatch(line); len(matches) > 1 {
			sel.AriaLabel = matches[1]
		}
		if matches := nameRe.FindStringSubmatch(line); len(matches) > 1 {
			sel.Name = matches[1]
		}
		if matches := classRe.FindStringSubmatch(line); len(matches) > 1 {
			classStr := matches[1]
			for _, c := range strings.Fields(classStr) {
				if c != "" {
					sel.Classes = append(sel.Classes, c)
				}
			}
		}
		if matches := titleRe.FindStringSubmatch(line); len(matches) > 1 {
			sel.Title = matches[1]
		}
		if matches := roleRe.FindStringSubmatch(line); len(matches) > 1 {
			sel.Role = matches[1]
		}
		
		// Extract input type
		if matches := typeRe.FindStringSubmatch(line); len(matches) > 1 {
			sel.InputType = matches[1]
		}
		if matches := htmlTypeRe.FindStringSubmatch(line); len(matches) > 1 {
			sel.HTMLType = matches[1]
		}

		// Extract text content for elements with visible text
		sel.Text = extractTextFromLine(line)

		// Only add if it has at least one useful selector
		if sel.hasSelector() {
			selectors = append(selectors, sel)
		}
	}

	return selectors
}

// hasSelector returns true if the selector has at least one identifying attribute
func (s *ExtractedSelector) hasSelector() bool {
	return s.Testid != "" || s.ID != "" || s.Placeholder != "" || 
	       s.AriaLabel != "" || s.Name != "" || s.Title != "" || s.Role != ""
}

// extractTextFromLine extracts visible text from a JSX line
func extractTextFromLine(line string) string {
	// Remove JSX expressions {expr}
	result := line
	for {
		start := strings.Index(result, "{")
		if start == -1 {
			break
		}
		end := findMatchingBrace(result[start:])
		if end == -1 {
			break
		}
		result = result[:start] + result[start+end+1:]
	}
	
	// Extract text between > and <
	textMatch := regexp.MustCompile(`>([^<]+)<`).FindStringSubmatch(result)
	if len(textMatch) > 1 {
		text := strings.TrimSpace(textMatch[1])
		// Clean up whitespace
		text = regexp.MustCompile(`\s+`).ReplaceAllString(text, " ")
		return text
	}
	return ""
}

// findMatchingBrace finds the matching closing brace for an opening brace
func findMatchingBrace(s string) int {
	if len(s) == 0 || s[0] != '{' {
		return -1
	}
	depth := 1
	for i := 1; i < len(s); i++ {
		if s[i] == '{' {
			depth++
		} else if s[i] == '}' {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// BuildSelectorIndexFromFiles extracts selectors from all source files and builds an index
func (m *GraphMapper) BuildSelectorIndexFromFiles(sourceFiles map[string]string) map[string][]ExtractedSelector {
	selectorIndex := make(map[string][]ExtractedSelector)

	for filePath, content := range sourceFiles {
		selectors := m.ExtractSelectorsFromFile(content, filePath)
		selectorIndex[filePath] = selectors
	}

	return selectorIndex
}

// GeneratePlaywrightSelector creates a Playwright-compatible selector string for an ExtractedSelector
func (s *ExtractedSelector) GeneratePlaywrightSelector() string {
	// Priority order: testid > id > aria-label > placeholder > name > text
	if s.Testid != "" {
		return fmt.Sprintf("[data-testid='%s']", s.Testid)
	}
	if s.ID != "" {
		return fmt.Sprintf("#%s", s.ID)
	}
	if s.AriaLabel != "" {
		return fmt.Sprintf("[aria-label='%s']", s.AriaLabel)
	}
	if s.Placeholder != "" {
		return fmt.Sprintf("[placeholder='%s']", s.Placeholder)
	}
	if s.Name != "" {
		return fmt.Sprintf("[name='%s']", s.Name)
	}
	if s.Text != "" {
		return fmt.Sprintf("text('%s')", s.Text)
	}
	// Fallback to element type with classes
	if len(s.Classes) > 0 {
		return fmt.Sprintf("%s.%s", s.ElementType, strings.Join(s.Classes[:min(2, len(s.Classes))], "."))
	}
	return s.ElementType
}

// FormatSelectorForPrompt creates a human-readable selector string for the LLM prompt
func (s *ExtractedSelector) FormatSelectorForPrompt() string {
	var parts []string

	if s.Testid != "" {
		parts = append(parts, fmt.Sprintf("data-testid='%s'", s.Testid))
	}
	if s.ID != "" {
		parts = append(parts, fmt.Sprintf("id='%s'", s.ID))
	}
	if s.Placeholder != "" {
		parts = append(parts, fmt.Sprintf("placeholder='%s'", s.Placeholder))
	}
	if s.AriaLabel != "" {
		parts = append(parts, fmt.Sprintf("aria-label='%s'", s.AriaLabel))
	}
	if s.Text != "" {
		parts = append(parts, fmt.Sprintf("text='%s'", s.Text))
	}
	if s.Name != "" {
		parts = append(parts, fmt.Sprintf("name='%s'", s.Name))
	}

	if len(parts) == 0 {
		return fmt.Sprintf("<%s>", s.ElementType)
	}

	return fmt.Sprintf("<%s %s>", s.ElementType, strings.Join(parts, ", "))
}

// =============================================================================
// MODULE CATALOG - The enriched map structure
// =============================================================================

// ModuleCatalog is the complete enriched knowledge map for a project/branch
type ModuleCatalog struct {
	ProjectID    string                     `json:"projectId"`
	Branch       string                     `json:"branch"`
	GeneratedAt  time.Time                  `json:"generatedAt"`
	Modules      map[string]ModuleEntry     `json:"modules"`
	RouteIndex   map[string]string          `json:"routeIndex"` // route → moduleKey
	Selectors    map[string][]ExtractedSelector `json:"selectors"` // filePath → selectors
}

// ModuleEntry represents a functional module in the application
type ModuleEntry struct {
	DisplayName    string                `json:"displayName"`
	Description    string                `json:"description"`
	Features       []string              `json:"features"`          // e.g. ["list", "create", "create", "delete", "search", "filter"]
	NavigationPath []string              `json:"navigationPath"`   // e.g. ["Master Data", "Entity Districts"]
	Routes         map[string]RouteEntry `json:"routes"`
}

// RouteEntry represents a single route/page in the module
type RouteEntry struct {
	FilePath         string            `json:"filePath"`
	Description      string            `json:"description"`
	KeyElements      map[string]string `json:"keyElements"`      // semantic name → selector value
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
	startTime := time.Now()

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
	log.Printf("[GraphMapper] Found %d files in repository", len(files))

	// Build route map (instant, no LLM)
	routeMap := m.routeMapper.BuildRouteMapFromFileList(files)
	if len(routeMap) == 0 {
		return nil, fmt.Errorf("no routes found in project")
	}
	log.Printf("[GraphMapper] Mapped %d routes from file tree", len(routeMap))

	// Fetch source files for enrichment (app/, pages/, components/)
	sourceFiles, err := m.fetchSourceFilesForEnrichment(ctx, glClient, projectID, branch, files)
	if err != nil {
		log.Printf("[GraphMapper] Warning: failed to fetch source files: %v", err)
		// Continue anyway - LLM can work with minimal context
	}

	// Extract actual selectors from the full source files
	selectorIndex := m.BuildSelectorIndexFromFiles(sourceFiles)
	totalSelectors := 0
	for _, sels := range selectorIndex {
		totalSelectors += len(sels)
	}
	log.Printf("[GraphMapper] Extracted %d selectors from %d files", totalSelectors, len(selectorIndex))

	// Enrich with LLM (pass route map and selector index)
	llmCallCount := 1 // Track LLM calls (currently 1 per catalog)
	catalog, err = m.enrichWithLLM(ctx, projectID, branch, routeMap, sourceFiles, selectorIndex)
	if err != nil {
		return nil, fmt.Errorf("failed to enrich with LLM: %w", err)
	}

	// Attach selector index to catalog
	catalog.Selectors = selectorIndex

	// Generate and log coverage report
	generationTimeMs := time.Since(startTime).Milliseconds()
	coverageReport := m.GenerateCoverageReport(routeMap, catalog, selectorIndex, llmCallCount, generationTimeMs)
	coverageReport.Log()

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

// FetchFileTree is the exported version for use by routes
func (m *GraphMapper) FetchFileTree(glClient *gitlab.Client, projectID, branch string) ([]string, error) {
	return m.fetchFileTree(glClient, projectID, branch)
}

// fetchSourceFilesForEnrichment fetches ALL source files for complete selector coverage
func (m *GraphMapper) fetchSourceFilesForEnrichment(
	ctx context.Context,
	glClient *gitlab.Client,
	projectID, branch string,
	allFiles []string,
) (map[string]string, error) {
	sourceFiles := make(map[string]string)

	// Priority directories
	priorityDirs := []string{"app/", "pages/", "components/"}

	// Filter to relevant files (page files first, then other source files)
	var pageFiles []string
	var otherFiles []string
	for _, path := range allFiles {
		if !isSourceFile(path) {
			continue
		}
		isPrioritized := false
		for _, dir := range priorityDirs {
			if strings.HasPrefix(path, dir) {
				isPrioritized = true
				break
			}
		}
		if !isPrioritized {
			continue
		}
		
		// Prioritize page.tsx files for route coverage
		if strings.HasSuffix(path, "page.tsx") || strings.HasSuffix(path, "page.ts") {
			pageFiles = append(pageFiles, path)
		} else {
			otherFiles = append(otherFiles, path)
		}
	}

	// Sort pages by module to get diverse coverage
	sortPaths(pageFiles)
	sortPaths(otherFiles)

	// Combine: pages first, then supporting files
	allRelevant := append(pageFiles, otherFiles...)
	
	// FETCH ALL FILES - no limit for complete coverage
	log.Printf("[GraphMapper] Fetching ALL %d source files (no limit) for complete selector extraction", 
		len(allRelevant))

	// Fetch each file with error tracking
	fetchedCount := 0
	failedCount := 0
	for _, path := range allRelevant {
		content, err := m.fetchFileContent(glClient, projectID, branch, path)
		if err != nil {
			failedCount++
			if failedCount <= 10 { // Only log first 10 failures
				log.Printf("[GraphMapper] Warning: failed to fetch %s: %v", path, err)
			}
			continue
		}
		sourceFiles[path] = content
		fetchedCount++
		
		// Log progress
		if fetchedCount%100 == 0 {
			log.Printf("[GraphMapper] Progress: %d/%d files fetched", fetchedCount, len(allRelevant))
		}
	}

	if failedCount > 10 {
		log.Printf("[GraphMapper] ... and %d more failed fetches (omitted)", failedCount-10)
	}
	log.Printf("[GraphMapper] Source files fetched: %d success, %d failed", fetchedCount, failedCount)
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
// It uses the pre-extracted selector index to ensure LLM only references EXISTING selectors
// Now processes routes in batches to cover ALL routes, not just a subset
func (m *GraphMapper) enrichWithLLM(
	ctx context.Context,
	projectID, branch string,
	routeMap map[string]string,
	sourceFiles map[string]string,
	selectorIndex map[string][]ExtractedSelector,
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

	// Build selector summary from the extracted selectors
	selectorSummary := m.buildSelectorSummaryForPrompt(selectorIndex)

	// First, group routes deterministically by module (first path segment)
	moduleGroups := m.groupRoutesByModule(routeMap)
	log.Printf("[GraphMapper] Grouped %d routes into %d modules deterministically", 
		len(routeMap), len(moduleGroups))

	// Build the catalog with deterministic structure
	catalog := &ModuleCatalog{
		ProjectID:   projectID,
		Branch:      branch,
		GeneratedAt: time.Now(),
		Modules:     make(map[string]ModuleEntry),
		RouteIndex:  make(map[string]string),
		Selectors:   selectorIndex,
	}

	// Process each module - use LLM to get descriptions for each module batch
	llmCallCount := 0
	for moduleKey, routes := range moduleGroups {
		moduleEntry := ModuleEntry{
			DisplayName: m.formatModuleName(moduleKey),
			Description: m.inferModuleDescription(moduleKey, routes),
			Features:    m.inferModuleFeatures(routes),
			NavigationPath: m.inferNavigationPath(moduleKey),
			Routes:      make(map[string]RouteEntry),
		}

		// Get selectors for routes in this module
		moduleSelectors := m.getSelectorsForRoutes(routes, routeMap, selectorIndex)

		// Process routes in batches (20 routes per batch to fit in LLM context)
		batchSize := 20
		for i := 0; i < len(routes); i += batchSize {
			end := i + batchSize
			if end > len(routes) {
				end = len(routes)
			}
			batch := routes[i:end]
			
			// Build route context for this batch
			routeContexts := m.buildRouteContexts(batch, routeMap, moduleSelectors)
			
			// Call LLM for this batch
			enrichedRoutes, err := m.enrichRoutesBatch(ctx, client, routeContexts, selectorSummary)
			if err != nil {
				log.Printf("[GraphMapper] Warning: LLM batch failed for %s batch %d: %v", 
					moduleKey, i/batchSize, err)
				// Use fallback route entries
				for _, route := range batch {
					catalog.RouteIndex[route] = moduleKey
					moduleEntry.Routes[route] = m.createFallbackRouteEntry(route, routeMap[route], moduleSelectors)
				}
			} else {
				llmCallCount++
				for route, entry := range enrichedRoutes {
					catalog.RouteIndex[route] = moduleKey
					moduleEntry.Routes[route] = entry
				}
			}
		}

		catalog.Modules[moduleKey] = moduleEntry
	}

	log.Printf("[GraphMapper] LLM enrichment complete: %d calls", llmCallCount)

	// Validate keyElements against actual selectors - filter out hallucinated values
	var invalidSelectorCount int
	catalog, invalidSelectorCount = m.validateAndFilterKeyElements(catalog, selectorIndex)
	log.Printf("[GraphMapper] Total invalid selectors filtered: %d", invalidSelectorCount)
	
	return catalog, nil
}

// groupRoutesByModule deterministically groups routes by first path segment
func (m *GraphMapper) groupRoutesByModule(routeMap map[string]string) map[string][]string {
	modules := make(map[string][]string)
	
	for route := range routeMap {
		parts := strings.Split(strings.TrimPrefix(route, "/"), "/")
		if len(parts) > 0 && parts[0] != "" {
			moduleKey := parts[0]
			modules[moduleKey] = append(modules[moduleKey], route)
		}
	}
	
	// Sort routes within each module
	for _, routes := range modules {
		sort.Strings(routes)
	}
	
	return modules
}

// formatModuleName creates a human-readable name from module key
func (m *GraphMapper) formatModuleName(moduleKey string) string {
	// Convert kebab-case or snake_case to Title Case
	name := strings.ReplaceAll(moduleKey, "-", " ")
	name = strings.ReplaceAll(name, "_", " ")
	name = strings.Title(name)
	return name
}

// inferModuleDescription guesses description based on module name
func (m *GraphMapper) inferModuleDescription(moduleKey string, routes []string) string {
	// Map common module keys to descriptions
	descriptions := map[string]string{
		"auth":          "Authentication and user session management",
		"dashboard":     "Main dashboard with overview metrics and navigation",
		"master-data":   "Master data management for core business entities",
		"capex":         "Capital expenditure budget planning and management",
		"opex":          "Operational expenditure budget management",
		"invoice":       "Invoice processing and management",
		"otc":           "Order to cash process management",
		"tax-system":    "Tax compliance and reporting system",
		"tax-monitoring": "Tax project monitoring and approval workflows",
		"wbs":           "Work breakdown structure management",
		"iam":           "Identity and access management",
		"supplier":      "Supplier management and contracts",
		"peminjaman":    "Asset borrowing and loan management",
	}
	
	if desc, ok := descriptions[moduleKey]; ok {
		return desc
	}
	
	return fmt.Sprintf("%s module - %d routes", m.formatModuleName(moduleKey), len(routes))
}

// inferModuleFeatures guesses features from route patterns
func (m *GraphMapper) inferModuleFeatures(routes []string) []string {
	features := []string{"list", "view"}
	hasCreate := false
	hasEdit := false
	
	for _, route := range routes {
		if strings.Contains(route, "/create") || strings.Contains(route, "/new") {
			hasCreate = true
		}
		if strings.Contains(route, "/edit") || strings.Contains(route, "/[id]/edit") {
			hasEdit = true
		}
	}
	
	if hasCreate {
		features = append(features, "create")
	}
	if hasEdit {
		features = append(features, "edit")
	}
	features = append(features, "search", "filter")
	
	return features
}

// inferNavigationPath guesses menu hierarchy from module key
func (m *GraphMapper) inferNavigationPath(moduleKey string) []string {
	// Map to common navigation paths
	navPaths := map[string][]string{
		"auth":           {"Authentication"},
		"dashboard":      {"Dashboard"},
		"master-data":    {"Master Data"},
		"capex":          {"CAPEX"},
		"opex":           {"OPEX"},
		"invoice":        {"Invoices"},
		"invoice-noi-external": {"Invoices", "Non-PO External"},
		"invoice-noi-internal": {"Invoices", "Non-PO Internal"},
		"invoice-oi":     {"Invoices", "Operating"},
		"otc":            {"OTC"},
		"tax-system":     {"Tax System"},
		"tax-monitoring":  {"Tax Monitoring"},
		"wbs":            {"WBS"},
		"iam":            {"IAM"},
		"supplier":       {"Supplier"},
		"peminjaman":     {"Loan Management"},
	}
	
	if path, ok := navPaths[moduleKey]; ok {
		return path
	}
	
	return []string{m.formatModuleName(moduleKey)}
}

// getSelectorsForRoutes builds selector context for a set of routes
func (m *GraphMapper) getSelectorsForRoutes(routes []string, routeMap map[string]string, selectorIndex map[string][]ExtractedSelector) map[string][]ExtractedSelector {
	result := make(map[string][]ExtractedSelector)
	
	for _, route := range routes {
		filePath, ok := routeMap[route]
		if !ok {
			continue
		}
		if selectors, ok := selectorIndex[filePath]; ok {
			result[route] = selectors
		}
	}
	
	return result
}

// buildRouteContexts creates route context strings for LLM
func (m *GraphMapper) buildRouteContexts(routes []string, routeMap map[string]string, selectorIndex map[string][]ExtractedSelector) string {
	var lines []string
	
	for _, route := range routes {
		filePath := routeMap[route]
		selectors := selectorIndex[filePath]
		
		line := fmt.Sprintf("Route: %s\nFile: %s", route, filePath)
		
		if len(selectors) > 0 {
			var selLines []string
			for _, sel := range selectors {
				if sel.Testid != "" {
					selLines = append(selLines, fmt.Sprintf("  [data-testid='%s']", sel.Testid))
				}
				if sel.ID != "" {
					selLines = append(selLines, fmt.Sprintf("  #%s", sel.ID))
				}
				if sel.Name != "" {
					selLines = append(selLines, fmt.Sprintf("  [name='%s']", sel.Name))
				}
				if sel.Placeholder != "" {
					selLines = append(selLines, fmt.Sprintf("  [placeholder='%s']", sel.Placeholder))
				}
				if sel.Text != "" && len(sel.Text) < 50 {
					selLines = append(selLines, fmt.Sprintf("  text('%s')", sel.Text))
				}
			}
			if len(selLines) > 0 {
				line += "\nSelectors:\n" + strings.Join(selLines, "\n")
			}
		}
		
		lines = append(lines, line)
	}
	
	return strings.Join(lines, "\n\n")
}

// enrichRoutesBatch calls LLM to get enriched route entries
func (m *GraphMapper) enrichRoutesBatch(ctx context.Context, client *genai.Client, routeContexts string, selectorSummary string) (map[string]RouteEntry, error) {
	prompt := fmt.Sprintf(`Analyze these routes and return JSON with route details.

Routes:
%s

AVAILABLE SELECTORS (use these ONLY):
%s

Return JSON like:
{
  "/route1": {"description": "...", "keyElements": {"searchInput": "selector-value"}, "availableActions": ["search"]},
  "/route2": {"description": "...", "keyElements": {}, "availableActions": ["view"]}
}`, routeContexts, selectorSummary)

	config := &genai.GenerateContentConfig{
		Temperature:      genai.Ptr(float32(0.2)),
		ResponseMIMEType: "application/json",
	}

	resp, err := client.Models.GenerateContent(ctx, LLMModel, genai.Text(prompt), config)
	if err != nil {
		return nil, err
	}

	responseText := extractResponseText(resp)
	
	var result map[string]RouteEntry
	if err := json.Unmarshal([]byte(responseText), &result); err != nil {
		return nil, err
	}
	
	return result, nil
}

// createFallbackRouteEntry creates a basic route entry when LLM fails
func (m *GraphMapper) createFallbackRouteEntry(route, filePath string, selectorIndex map[string][]ExtractedSelector) RouteEntry {
	entry := RouteEntry{
		FilePath:         filePath,
		Description:      fmt.Sprintf("Page at %s", route),
		KeyElements:      make(map[string]string),
		AvailableActions: []string{"view"},
	}
	
	// Try to add selectors from the file
	if sels, ok := selectorIndex[filePath]; ok {
		for _, sel := range sels {
			if sel.Testid != "" {
				if entry.KeyElements == nil {
					entry.KeyElements = make(map[string]string)
				}
				entry.KeyElements["testid"] = sel.Testid
				break
			}
		}
	}
	
	return entry
}

// validateAndFilterKeyElements checks that keyElements reference actual selectors from the codebase
// Returns the filtered catalog and count of invalid selectors
func (m *GraphMapper) validateAndFilterKeyElements(catalog *ModuleCatalog, selectorIndex map[string][]ExtractedSelector) (*ModuleCatalog, int) {
	totalInvalid := 0
	
	// Build a set of all valid selector values for quick lookup
	validSelectors := make(map[string]bool)
	for _, selectors := range selectorIndex {
		for _, sel := range selectors {
			if sel.Testid != "" {
				validSelectors[sel.Testid] = true
			}
			if sel.ID != "" {
				validSelectors[sel.ID] = true
			}
			if sel.Placeholder != "" {
				validSelectors[sel.Placeholder] = true
			}
			if sel.AriaLabel != "" {
				validSelectors[sel.AriaLabel] = true
			}
			if sel.Name != "" {
				validSelectors[sel.Name] = true
			}
			if sel.Text != "" {
				validSelectors[sel.Text] = true
			}
		}
	}

	log.Printf("[GraphMapper] Valid selector pool has %d entries", len(validSelectors))

	// Filter each route's keyElements
	for moduleKey, module := range catalog.Modules {
		for routePath, route := range module.Routes {
			validKeyElements := make(map[string]string)
			invalidCount := 0
			
			for semanticName, selectorValue := range route.KeyElements {
				if validSelectors[selectorValue] {
					validKeyElements[semanticName] = selectorValue
				} else {
					// Check if it's a compound selector format
					if strings.HasPrefix(selectorValue, "[data-testid='") || 
					   strings.HasPrefix(selectorValue, "#") ||
					   strings.HasPrefix(selectorValue, "[placeholder='") ||
					   strings.HasPrefix(selectorValue, "[aria-label='") ||
					   strings.HasPrefix(selectorValue, "text('") {
						// Extract the value from the selector format
						extracted := extractValueFromSelector(selectorValue)
						if extracted != "" && validSelectors[extracted] {
							validKeyElements[semanticName] = extracted
							continue
						}
					}
					invalidCount++
					log.Printf("[GraphMapper] ⚠️ Invalid selector '%s' for key '%s' in route %s", selectorValue, semanticName, routePath)
				}
			}
			
			if invalidCount > 0 {
				log.Printf("[GraphMapper] Route %s: %d/%d keyElements were filtered out (not found in codebase)", 
					routePath, invalidCount, len(route.KeyElements))
				totalInvalid += invalidCount
			}
			
			route.KeyElements = validKeyElements
			catalog.Modules[moduleKey].Routes[routePath] = route
		}
	}

	return catalog, totalInvalid
}

// extractValueFromSelector extracts the actual value from a Playwright selector format
func extractValueFromSelector(selector string) string {
	// [data-testid='value'] → value
	re := regexp.MustCompile(`\[data-testid=['"]([^'"]+)['"]\]`)
	if matches := re.FindStringSubmatch(selector); len(matches) > 1 {
		return matches[1]
	}
	// #id → id
	re = regexp.MustCompile(`#([a-zA-Z0-9_-]+)`)
	if matches := re.FindStringSubmatch(selector); len(matches) > 1 {
		return matches[1]
	}
	// [placeholder='value'] → value
	re = regexp.MustCompile(`\[placeholder=['"]([^'"]+)['"]\]`)
	if matches := re.FindStringSubmatch(selector); len(matches) > 1 {
		return matches[1]
	}
	// [aria-label='value'] → value
	re = regexp.MustCompile(`\[aria-label=['"]([^'"]+)['"]\]`)
	if matches := re.FindStringSubmatch(selector); len(matches) > 1 {
		return matches[1]
	}
	// text('value') → value
	re = regexp.MustCompile(`text\(['"]([^'"]+)['"]\)`)
	if matches := re.FindStringSubmatch(selector); len(matches) > 1 {
		return matches[1]
	}
	return ""
}

// buildFilesSummaryForPrompt creates a summary of source files for the LLM prompt
// Uses full file content (no truncation) to preserve selector context
func (m *GraphMapper) buildFilesSummaryForPrompt(sourceFiles map[string]string) string {
	var lines []string
	for path, content := range sourceFiles {
		// Use full file content - selectors can be anywhere in the file
		lines = append(lines, fmt.Sprintf("=== FILE: %s ===\n%s\n", path, content))
	}
	return strings.Join(lines, "\n")
}

// buildSelectorSummaryForPrompt formats the extracted selectors into a readable prompt section
// This ensures the LLM only references selectors that actually exist in the codebase
func (m *GraphMapper) buildSelectorSummaryForPrompt(selectorIndex map[string][]ExtractedSelector) string {
	var lines []string
	
	lines = append(lines, "Available selectors grouped by file:")
	lines = append(lines, "")
	
	// Group selectors by file
	for filePath, selectors := range selectorIndex {
		lines = append(lines, fmt.Sprintf("--- %s (%d elements) ---", filePath, len(selectors)))
		
		// Group by element type for readability
		typeGroups := make(map[string][]string)
		for _, sel := range selectors {
			key := sel.ElementType
			if key == "" {
				key = "element"
			}
			typeGroups[key] = append(typeGroups[key], sel.FormatSelectorForPrompt())
		}
		
		for elemType, items := range typeGroups {
			lines = append(lines, fmt.Sprintf("  %s: [%s]", elemType, strings.Join(items, ", ")))
		}
		lines = append(lines, "")
	}
	
	// Add selector format reference
	lines = append(lines, "")
	lines = append(lines, "Selector formats you can use:")
	lines = append(lines, "  - data-testid='value' → [data-testid='value']")
	lines = append(lines, "  - id='myId' → #myId")
	lines = append(lines, "  - placeholder='Search...' → [placeholder='Search...']")
	lines = append(lines, "  - aria-label='Close' → [aria-label='Close']")
	lines = append(lines, "  - text='Submit' → text('Submit')")
	lines = append(lines, "  - name='email' → [name='email']")
	lines = append(lines, "  - Compound: button:has-text('Save')")
	lines = append(lines, "  - Nth fallback: >> nth=0")
	
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

// ListCachedCatalogs returns all cached knowledge graphs for a given project
func (m *GraphMapper) ListCachedCatalogs(ctx context.Context, projectID string) ([]ModuleCatalog, error) {
	pattern := fmt.Sprintf("%s:%s:*", GraphMapKeyPrefix, projectID)
	
	var cursor uint64
	var catalogs []ModuleCatalog
	
	for {
		keys, nextCursor, err := database.RedisClient.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return nil, err
		}
		
		for _, key := range keys {
			val, err := database.RedisClient.Get(ctx, key).Result()
			if err != nil {
				continue
			}
			var catalog ModuleCatalog
			if err := json.Unmarshal([]byte(val), &catalog); err == nil {
				catalogs = append(catalogs, catalog)
			}
		}
		
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
	
	return catalogs, nil
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
	
	// Handle markdown code blocks - strip outer ```json ... ``` 
	res = strings.TrimPrefix(res, "```json")
	res = strings.TrimPrefix(res, "```")
	res = strings.TrimSuffix(res, "```")
	res = strings.TrimSuffix(res, "'''")
	
	// Handle embedded code blocks inside the JSON (e.g., example selectors)
	// Find the first { and last } to get the actual JSON boundaries
	firstBrace := strings.Index(res, "{")
	lastBrace := strings.LastIndex(res, "}")
	
	if firstBrace != -1 && lastBrace > firstBrace {
		res = res[firstBrace : lastBrace+1]
	}
	
	// Remove any remaining markdown formatting
	res = strings.ReplaceAll(res, "\\`", "`")
	res = strings.ReplaceAll(res, "```", "")
	
	return strings.TrimSpace(res)
}

// extractJSONFromResponse attempts to extract valid JSON from a response that may contain extra content
func extractJSONFromResponse(response string) string {
	// Try to find JSON boundaries
	firstBrace := strings.Index(response, "{")
	lastBrace := strings.LastIndex(response, "}")
	
	if firstBrace == -1 || lastBrace == -1 || lastBrace <= firstBrace {
		return response
	}
	
	// Extract content between first { and last }
	result := response[firstBrace : lastBrace+1]
	
	// Validate it's parseable
	if len(result) < 10 {
		return response
	}
	
	return result
}
