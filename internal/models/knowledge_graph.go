package models

// KnowledgeGraph represents the knowledge graph for a module
// Used by test_generator.go and graph_mapper.go
type KnowledgeGraph struct {
	Git struct {
		CommitSHA string `json:"commit_sha"`
		Branch    string `json:"branch"`
	} `json:"git"`
	RouteSummary  map[string]RouteInfo    `json:"route_summary"`
	SelectorIndex map[string]SelectorEntry `json:"selector_index"`
	Stats         KnowledgeGraphStats     `json:"stats"`
}

// KnowledgeGraphStats contains statistics about the extracted knowledge graph
type KnowledgeGraphStats struct {
	TotalSelectors int `json:"totalSelectors"`
	TotalPages     int `json:"totalPages"`
	TotalHooks     int `json:"totalHooks"`
	TotalForms     int `json:"totalForms"`
}

// RouteInfo contains all knowledge about a specific route/page
type RouteInfo struct {
	PageID  string         `json:"pageId"`
	Module  string         `json:"module"`
	Testids []TestidEntry  `json:"testids"`
	Forms   []FormEntry    `json:"forms"`
	Hooks   []HookEntry    `json:"hooks"`
	APIs    []APIEntry     `json:"apis"`
}

// TestidEntry represents a single data-testid on a page
type TestidEntry struct {
	Testid             string              `json:"testid"`
	ElementType        string              `json:"elementType"`
	FieldName          *string             `json:"fieldName"`
	Action             string              `json:"action"`
	BoundAction        *string             `json:"boundAction"`
	ParentFormTestid   *string             `json:"parentFormTestid"`
	SuggestedSelectors []SuggestedSelector `json:"suggestedSelectors"`
}

// SuggestedSelector represents a candidate selector with confidence score
type SuggestedSelector struct {
	Type       string  `json:"type"`
	Value      string  `json:"value"`
	Confidence float64 `json:"confidence"`
}

// FormEntry represents a form on the page
type FormEntry struct {
	SchemaName string      `json:"schemaName"`
	Fields     []FieldInfo `json:"fields"`
}

// FieldInfo represents a field within a form
type FieldInfo struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Required bool   `json:"required"`
}

// HookEntry represents a React hook used on the page
type HookEntry struct {
	HookName string `json:"hookName"`
	HookType string `json:"hookType"`
	CallsAPI string `json:"callsApi"`
}

// APIEntry represents an API call chain triggered by a hook
type APIEntry struct {
	FunctionName string `json:"functionName"`
	HTTPMethod   string `json:"httpMethod"`
	Endpoint     string `json:"endpoint"`
}

// SelectorEntry is an index entry for quick selector lookup
type SelectorEntry struct {
	PageID      string `json:"pageId"`
	SelectorID  string `json:"selectorId"`
	ElementType string `json:"elementType"`
}

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
}

// ModuleCoverage contains coverage stats per module
type ModuleCoverage struct {
	TotalRoutes   int     `json:"totalRoutes"`
	CoveredRoutes int     `json:"coveredRoutes"`
	SelectorCount int     `json:"selectorCount"`
	CoverageRatio float64 `json:"coverageRatio"`
}

// GetRouteInfo retrieves route information from the graph
func (g *KnowledgeGraph) GetRouteInfo(route string) (*RouteInfo, bool) {
	info, ok := g.RouteSummary[route]
	return &info, ok
}

// GetTestidInfo finds testid information across all routes
func (g *KnowledgeGraph) GetTestidInfo(testid string) *TestidEntry {
	for _, routeInfo := range g.RouteSummary {
		for _, td := range routeInfo.Testids {
			if td.Testid == testid {
				return &td
			}
		}
	}
	return nil
}

// ValidateSelector checks if a selected selector exists in the graph's suggested list
func (g *KnowledgeGraph) ValidateSelector(testid string, selectedSelector string) (string, bool) {
	td := g.GetTestidInfo(testid)
	if td == nil {
		return selectedSelector, true
	}
	for _, sel := range td.SuggestedSelectors {
		if sel.Value == selectedSelector {
			return selectedSelector, true
		}
	}
	if len(td.SuggestedSelectors) > 0 {
		top := td.SuggestedSelectors[0]
		return top.Value, false
	}
	return selectedSelector, true
}

// GetAllTestidsForRoute returns all testids available for a given route
func (g *KnowledgeGraph) GetAllTestidsForRoute(route string) []TestidEntry {
	if info, ok := g.RouteSummary[route]; ok {
		return info.Testids
	}
	return nil
}

// GetFormFieldsForRoute returns all form fields for a given route
func (g *KnowledgeGraph) GetFormFieldsForRoute(route string) []FormEntry {
	if info, ok := g.RouteSummary[route]; ok {
		return info.Forms
	}
	return nil
}

// HasRoute checks if a route exists in the graph
func (g *KnowledgeGraph) HasRoute(route string) bool {
	_, ok := g.RouteSummary[route]
	return ok
}
