package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/xuri/excelize/v2"
)

// TestCase represents a test case extracted from Excel
type TestCase struct {
	SheetName   string `json:"sheet_name"`
	TestID      string `json:"test_id"`
	TestType    string `json:"test_type"`
	TestScenario string `json:"test_scenario"`
	PreCondition string `json:"pre_condition"`
	TestStep    string `json:"test_step"`
	Result      string `json:"result"`
	Status      string `json:"status"`
}

// RouteMapping represents a matched route for a test case
type RouteMapping struct {
	SheetName      string           `json:"sheet_name"`
	Route          string           `json:"route"`
	Confidence     float64          `json:"confidence"`
	MatchReason    string           `json:"match_reason"`
	MatchSource    string           `json:"match_source"` // "sheet_name" or "test_step"
	TestCases      []TestCase       `json:"test_cases"`
	TestStepRoutes []TestStepRoute `json:"test_step_routes,omitempty"`
}

// TestStepRoute represents route identified from analyzing test steps
type TestStepRoute struct {
	TestID     string  `json:"test_id"`
	Route      string  `json:"route"`
	Confidence float64 `json:"confidence"`
}

// PageRoute represents a page route from the frontend
type PageRoute struct {
	Route        string   `json:"route"`
	PathSegments []string `json:"path_segments"`
	Keywords     []string `json:"keywords"`
}

func main() {
	if len(os.Args) < 3 {
		fmt.Println("Usage: test-scenario-router <excel_file> <frontend_src_dir>")
		fmt.Println("Example: test-scenario-router ../Downloads/Test.xlsx ../frontend-service/src/app")
		os.Exit(1)
	}

	excelPath := os.Args[1]
	frontendSrc := os.Args[2]

	fmt.Println("=== Test Scenario Router ===")
	fmt.Printf("Excel file: %s\n", excelPath)
	fmt.Printf("Frontend source: %s\n\n", frontendSrc)

	// Extract routes from frontend
	routes := extractRoutesFromFrontend(frontendSrc)
	fmt.Printf("Found %d routes in frontend\n\n", len(routes))

	// Parse Excel file
	testCases := parseExcelFile(excelPath)
	fmt.Printf("Found %d test cases across %d sheets\n\n", 
		len(testCases), countSheets(testCases))

	// Match test cases to routes
	mappings := matchTestCasesToRoutes(testCases, routes)

	// Output results
	fmt.Println("=== Route Mappings ===")
	printMappings(mappings)

	// Save to JSON if requested
	if outputPath := os.Getenv("OUTPUT_JSON"); outputPath != "" {
		saveJSON(mappings, outputPath)
		fmt.Printf("\nSaved results to %s\n", outputPath)
	}
}

func extractRoutesFromFrontend(srcDir string) []PageRoute {
	var routes []PageRoute

	// Find all page.tsx files using WalkDir
	routeSet := make(map[string]bool)

	err := filepath.WalkDir(srcDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // Skip errors
		}
		
		// Only process page.tsx files
		if d.IsDir() || d.Name() != "page.tsx" {
			return nil
		}

		// Get relative path from app directory
		relPath := strings.TrimPrefix(path, srcDir)
		relPath = strings.TrimPrefix(relPath, string(filepath.Separator))
		
		// Remove page.tsx to get route
		route := strings.TrimSuffix(relPath, "/page.tsx")
		route = strings.TrimSuffix(route, "\\page.tsx")
		
		if route == "" || route == "app" {
			return nil
		}

		// Skip duplicates
		if routeSet[route] {
			return nil
		}
		routeSet[route] = true

		// Extract keywords from route
		segments := strings.Split(route, string(filepath.Separator))
		if len(segments) == 0 {
			return nil
		}

		// Clean up route (remove group folders like (authenticated))
		cleanSegments := cleanRouteSegments(segments)
		
		var keywords []string
		for _, seg := range cleanSegments {
			keywords = append(keywords, extractKeywords(seg)...)
		}

		routes = append(routes, PageRoute{
			Route:        route,
			PathSegments: cleanSegments,
			Keywords:     keywords,
		})

		return nil
	})
	
	if err != nil {
		fmt.Printf("Error walking directory: %v\n", err)
	}

	return routes
}

func cleanRouteSegments(segments []string) []string {
	var clean []string
	skipGroups := map[string]bool{
		"(authenticated)": true,
		"(unauthenticated)": true,
	}

	for _, seg := range segments {
		if skipGroups[seg] {
			continue
		}
		// Skip dynamic route params
		if strings.HasPrefix(seg, "[") && strings.HasSuffix(seg, "]") {
			continue
		}
		clean = append(clean, seg)
	}
	return clean
}

func extractKeywords(segment string) []string {
	var keywords []string
	
	// Remove common suffixes
	segment = strings.TrimSuffix(segment, "/")
	
	// Split by common separators
	parts := regexp.MustCompile(`[-_]`).Split(segment, -1)
	
	for _, part := range parts {
		// Skip if it's a number or empty
		if matched, _ := regexp.MatchString(`^\d+$`, part); matched || part == "" {
			continue
		}
		
		// Add the part as keyword
		if part != "" {
			keywords = append(keywords, strings.ToLower(part))
			
			// Also add variations
			keywords = append(keywords, strings.ToLower(regexp.MustCompile(`[-_]`).ReplaceAllString(part, " ")))
		}
	}

	return keywords
}

func parseExcelFile(path string) map[string][]TestCase {
	sheets := make(map[string][]TestCase)

	f, err := excelize.OpenFile(path)
	if err != nil {
		fmt.Printf("Error opening Excel file: %v\n", err)
		return sheets
	}
	defer f.Close()

	for _, sheetName := range f.GetSheetList() {
		rows, err := f.GetRows(sheetName)
		if err != nil {
			fmt.Printf("Error reading sheet %s: %v\n", sheetName, err)
			continue
		}

		// Find header row (row with "User Story", "Test ID", etc.)
		headerRow := -1
		for i, row := range rows {
			if len(row) > 6 && containsTestHeader(row) {
				headerRow = i
				break
			}
		}

		if headerRow == -1 {
			continue
		}

		// Extract test cases from rows after header
		for i := headerRow + 1; i < len(rows); i++ {
			row := rows[i]
			if len(row) < 6 || row[1] == "" || row[1] == "Test ID" {
				continue
			}

			tc := TestCase{
				SheetName:   sheetName,
				TestID:      getString(row, 1),
				TestType:    getString(row, 2),
				TestScenario: getString(row, 3),
				PreCondition: getString(row, 4),
				TestStep:    getString(row, 5),
				Result:      getString(row, 6),
				Status:      getString(row, 7),
			}

			sheets[sheetName] = append(sheets[sheetName], tc)
		}
	}

	return sheets
}

func getString(row []string, index int) string {
	if index < len(row) {
		return strings.TrimSpace(row[index])
	}
	return ""
}

func containsTestHeader(row []string) bool {
	expected := []string{"User Story", "Test ID", "Test Type", "Test Scenario"}
	for _, exp := range expected {
		found := false
		for _, cell := range row {
			if strings.EqualFold(strings.TrimSpace(cell), exp) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func countSheets(tcs map[string][]TestCase) int {
	count := 0
	for range tcs {
		count++
	}
	return count
}

func matchTestCasesToRoutes(testCases map[string][]TestCase, routes []PageRoute) []RouteMapping {
	var mappings []RouteMapping

	for sheetName, cases := range testCases {
		mapping := RouteMapping{
			SheetName:      sheetName,
			Route:           "",
			Confidence:      0,
			MatchReason:     "",
			MatchSource:     "",
			TestCases:       cases,
			TestStepRoutes: []TestStepRoute{},
		}

		// Clean sheet name for matching
		sheetKeywords := extractKeywordsFromSheetName(sheetName)
		
		bestMatch := -1
		bestConfidence := 0.0
		bestReason := ""

		for i, route := range routes {
			confidence, reason := calculateMatchConfidence(sheetKeywords, route)
			
			if confidence > bestConfidence {
				bestConfidence = confidence
				bestMatch = i
				bestReason = reason
			}
		}

		if bestMatch >= 0 && bestConfidence > 0.3 {
			mapping.Route = routes[bestMatch].Route
			mapping.Confidence = bestConfidence
			mapping.MatchReason = bestReason
			mapping.MatchSource = "sheet_name"
		}

		// Also analyze individual test steps
		for _, tc := range cases {
			if tc.TestStep != "" {
				route, conf := analyzeTestSteps(tc.TestStep, routes)
				if route != "" && conf > 0.3 {
					mapping.TestStepRoutes = append(mapping.TestStepRoutes, TestStepRoute{
						TestID:     tc.TestID,
						Route:      route,
						Confidence: conf,
					})
				}
			}
		}

		mappings = append(mappings, mapping)
	}

	return mappings
}

func extractKeywordsFromSheetName(sheetName string) []string {
	// Clean up sheet name
	clean := strings.TrimSpace(sheetName)
	clean = strings.ToLower(clean)
	clean = strings.TrimSuffix(clean, " ")
	
	// Split by spaces, underscores, and hyphens
	parts := regexp.MustCompile(`[\s_\-]+`).Split(clean, -1)
	
	// Also keep original segments (for phrase matching)
	var keywords []string
	for _, part := range parts {
		if part != "" {
			keywords = append(keywords, part)
		}
	}
	
	return keywords
}

func calculateMatchConfidence(sheetKeywords []string, route PageRoute) (float64, string) {
	if len(sheetKeywords) == 0 {
		return 0, ""
	}

	var matchedKeywords []string

	// Clean sheet keywords
	var cleanSheetKw []string
	for _, kw := range sheetKeywords {
		clean := strings.ToLower(strings.TrimSpace(kw))
		if clean != "" && !isCommonWord(clean) && len(clean) >= 2 {
			cleanSheetKw = append(cleanSheetKw, clean)
		}
	}

	if len(cleanSheetKw) == 0 {
		return 0, ""
	}

	// Score based on segment matches (not necessarily consecutive)
	matchCount := 0
	matchedSegs := make(map[int]bool) // Track which segments are matched
	
	for _, sheetKw := range cleanSheetKw {
		for j := 0; j < len(route.PathSegments); j++ {
			// Skip already matched segments
			if matchedSegs[j] {
				continue
			}
			
			seg := route.PathSegments[j]
			segClean := strings.ReplaceAll(strings.ToLower(seg), "-", " ")
			
			// Exact match
			if segClean == sheetKw {
				matchCount++
				matchedSegs[j] = true
				matchedKeywords = append(matchedKeywords, sheetKw+" (exact)")
				break
			}
			
			// Segment contains keyword as word (not substring)
			// e.g., "period-plans" contains "period" and "plans"
			hasWord := strings.Contains(" "+segClean, " "+sheetKw) || 
			           strings.Contains(" "+segClean, sheetKw+" ") ||
			           strings.HasSuffix(segClean, " "+sheetKw)
			if hasWord {
				matchCount++
				matchedSegs[j] = true
				matchedKeywords = append(matchedKeywords, sheetKw+" (in "+seg+")")
				break
			}
		}
	}

	// Calculate confidence as ratio of matched keywords
	numKeywords := len(cleanSheetKw)
	confidence := float64(matchCount) / float64(numKeywords)
	
	// Minimum threshold
	if confidence < 0.5 {
		return 0, ""
	}

	reason := fmt.Sprintf("%d/%d keywords matched: %v", matchCount, numKeywords, matchedKeywords)
	return confidence, reason
}

func containsKeyword(matched []string, kw string) bool {
	for _, m := range matched {
		if strings.Contains(m, kw) {
			return true
		}
	}
	return false
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// preferSimplerRoute helps choose between multiple routes with same confidence
func preferSimplerRoute(route1, route2 string) string {
	// Prefer routes without [id] or dynamic params
	route1Params := strings.Count(route1, "[")
	route2Params := strings.Count(route2, "[")
	
	if route1Params < route2Params {
		return route1
	}
	if route2Params < route1Params {
		return route2
	}
	
	// Prefer shorter routes
	if len(route1) < len(route2) {
		return route1
	}
	
	return route2
}

// analyzeTestSteps looks at test steps to find specific route hints
func analyzeTestSteps(testStep string, routes []PageRoute) (string, float64) {
	if testStep == "" {
		return "", 0
	}

	// Clean the test step
	stepLower := strings.ToLower(testStep)
	
	bestRoute := ""
	bestScore := 0.0

	for _, route := range routes {
		routeLower := strings.ToLower(route.Route)
		
		// Count how many route segments appear in test steps
		score := 0.0
		for _, seg := range route.PathSegments {
			if strings.Contains(stepLower, strings.ToLower(seg)) {
				score += 1.0
			}
		}

		// Bonus for exact route mention
		if strings.Contains(stepLower, strings.ReplaceAll(routeLower, "/", " ")) {
			score += 5.0
		}

		if score > bestScore {
			bestScore = score
			bestRoute = route.Route
		}
	}

	if bestScore < 1.0 {
		return "", 0
	}

	// Normalize score
	normalizedScore := bestScore / float64(len(routes))
	if normalizedScore > 1.0 {
		normalizedScore = 1.0
	}
	return bestRoute, normalizedScore
}

func isCommonWord(word string) bool {
	common := map[string]bool{
		"the": true, "and": true, "or": true, "a": true, "an": true,
		"to": true, "of": true, "in": true, "on": true, "for": true,
		"by": true, "with": true, "data": true, "list": true, "master": true,
	}
	return common[word]
}

func containsMatch(a, b string) bool {
	// Check if one contains the other (for partial matches)
	return strings.Contains(a, b) || strings.Contains(b, a) ||
		fuzzyMatch(a, b) > 0.7
}

func fuzzyMatch(a, b string) float64 {
	// Simple fuzzy matching based on character overlap
	if len(a) == 0 || len(b) == 0 {
		return 0
	}

	aRunes := []rune(a)
	bRunes := []rune(b)

	intersection := 0
	for _, ar := range aRunes {
		for _, br := range bRunes {
			if ar == br {
				intersection++
				break
			}
		}
	}

	union := len(aRunes) + len(bRunes) - intersection
	if union == 0 {
		return 0
	}

	return float64(intersection) / float64(union)
}

func printMappings(mappings []RouteMapping) {
	fmt.Println()
	fmt.Println("╔══════════════════════════════════════════════════════════════════════════════════════╗")
	fmt.Println("║                         TEST CASE → ROUTE MAPPINGS                                   ║")
	fmt.Println("╠══════════════════════════════════════════════════════════════════════════════════════╣")
	
	highConf := 0
	medConf := 0
	lowConf := 0
	noMatch := 0

	for _, m := range mappings {
		route := m.Route
		if route == "" {
			route = "⚠️  NO MATCH"
			noMatch++
		} else if m.Confidence >= 0.8 {
			highConf++
		} else if m.Confidence >= 0.5 {
			medConf++
		} else {
			lowConf++
		}
		
		confIcon := "🔴"
		if m.Confidence >= 0.8 {
			confIcon = "🟢"
		} else if m.Confidence >= 0.5 {
			confIcon = "🟡"
		}

		// Clean route for display (remove group folders)
		displayRoute := strings.ReplaceAll(route, "(authenticated)/", "/")
		
		fmt.Printf("║ %s %-15s │ %-60s ║\n", 
			confIcon, truncate(m.SheetName, 15), truncate(displayRoute, 60))
		
		if m.Confidence > 0 {
			fmt.Printf("║   Confidence: %3.0f%%       │ Match: %-56s ║\n", 
				m.Confidence*100, truncate(m.MatchReason, 56))
		}

		// Show test step routes if available
		if len(m.TestStepRoutes) > 0 {
			fmt.Printf("║   ─── Test Step Analysis ───                                                   ║\n")
			for _, tsr := range m.TestStepRoutes {
				tsRoute := strings.ReplaceAll(tsr.Route, "(authenticated)/", "/")
				fmt.Printf("║     %-8s → %-50s (%.0f%%)     ║\n", 
					tsr.TestID, truncate(tsRoute, 50), tsr.Confidence*100)
			}
		}
		
		fmt.Println("╠══════════════════════════════════════════════════════════════════════════════════════╣")
	}
	
	fmt.Println("╚══════════════════════════════════════════════════════════════════════════════════════╝")
	
	fmt.Println()
	fmt.Println("=== Summary ===")
	fmt.Printf("  🟢 High Confidence (≥80%%): %d\n", highConf)
	fmt.Printf("  🟡 Medium Confidence (50-79%%): %d\n", medConf)
	fmt.Printf("  🔴 Low Confidence (<50%%): %d\n", lowConf)
	fmt.Printf("  ⚠️  No Match: %d\n", noMatch)
	fmt.Printf("  Total Sheets: %d\n", len(mappings))
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func saveJSON(mappings []RouteMapping, path string) {
	data, err := json.MarshalIndent(mappings, "", "  ")
	if err != nil {
		fmt.Printf("Error marshaling JSON: %v\n", err)
		return
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		fmt.Printf("Error writing JSON file: %v\n", err)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
