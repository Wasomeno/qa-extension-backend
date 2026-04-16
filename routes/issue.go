package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"qa-extension-backend/client"
	"qa-extension-backend/auth"
	"qa-extension-backend/database"

	"github.com/gin-gonic/gin"
	goGenai "google.golang.org/genai"
	"github.com/sony/gobreaker"
	"github.com/tmc/langchaingo/agents"
	"github.com/tmc/langchaingo/callbacks"
	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms/googleai"
	"github.com/tmc/langchaingo/tools"
	"github.com/tmc/langchaingo/tools/serpapi"
	gitlab "gitlab.com/gitlab-org/api/client-go"
	"golang.org/x/oauth2"
)

func GenerateIssueFixingPrompt(ginContext *gin.Context) {

}

func GenerateIssueFixingPromptWithAgent() {
	ctx := context.Background()
	apiKey := os.Getenv("GEMINI_API_KEY")
	llm, err := googleai.New(ctx, googleai.WithAPIKey(apiKey), googleai.WithDefaultModel("gemini-3.1-flash-lite-preview"))
	if err != nil {
		log.Fatal(err)
	}

	searchTool, err := serpapi.New()
	if err != nil {
		log.Printf("Warning: SerpApi not configured. (%v)", err)
	}

	agentTools := []tools.Tool{searchTool}

	// 3. Create Agent
	agent := agents.NewOpenAIFunctionsAgent(llm, agentTools)

	// 4. Create Executor with Logging
	executor := agents.NewExecutor(
		agent,
		agents.WithCallbacksHandler(callbacks.LogHandler{}),
	)

	// 5. Run the Agent
	// Scenario: Search for a concept, then check if the remote repo README mentions it.
	query := "Search for the purpose of a 'CONTRIBUTING.md' file in open source. Then, read the 'README.md' file in the GitLab repository and suggest if I should add a contributing section based on the search results."

	fmt.Printf("--- User Query: %s ---\n", query)

	_, err = chains.Call(ctx, executor, map[string]interface{}{
		"input": query,
	})
	if err != nil {
		log.Fatal(err)
	}
}

func SmartAutoCompleteIssueDescription(ginContext *gin.Context) {
	// Token verification handled by middleware, but we can access it if needed
	_ = ginContext.MustGet("token").(*oauth2.Token)

	// geminiApiKey := os.Getenv("GEMINI_API_KEY")
	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	location := os.Getenv("VERTEX_LOCATION")
	if location == "" {
		location = "us-central1" // default location
	}

	ctx := context.Background()
	
	// Create client config
	client, err := goGenai.NewClient(ctx, &goGenai.ClientConfig{
		Backend: goGenai.BackendVertexAI,
		Project: projectID,
		Location: location,
	})
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create Gemini client: " + err.Error()})
		ginContext.Abort()
		return
	}
	model := "gemini-3.1-flash-lite-preview"
	
	systemPrompt := `
You are a Senior QA Engineer responsible for filing bug reports and feature requests in GitLab.
Your goal is to take a rough user description and elaborate it into a professional, structured GitLab Issue.

**BOUNDARIES:**
- You must ONLY output the Markdown content. Do not include conversational filler like "Here is your issue."
- If the user prompt is vague, make reasonable professional assumptions to fill in the gaps, but mark them as [Assumed].
- Keep the tone technical, objective, and concise.

**REQUIRED MARKDOWN FORMAT:**
Use the exact structure below:

# [Type]: <Title of the Issue>

**Severity:** <Critical/Major/Minor/Trivial>
**Component:** <Frontend/Backend/Database/API>

## Summary
<A clear, high-level summary of the issue or feature.>

## Steps to Reproduce
1. <Step 1>
2. <Step 2>
3. <Step 3>

## Expected Behavior
<What should happen>

## Actual Behavior
<What actually happened>

## Technical Notes / Logs
<Any relevant error codes, logical gaps, or context.>
`

	config := &goGenai.GenerateContentConfig{
		SystemInstruction: &goGenai.Content{
			Parts: []*goGenai.Part{{Text: systemPrompt}},
		},
	}

	contents := []*goGenai.Content{
		{
			Parts: []*goGenai.Part{{Text: "i got an issue in the login page, in /auth/login. the form in there supposed to have an email and password validations. the current condition is there are no validations"}},
		},
	}
	resp, err := client.Models.GenerateContent(ctx, model, contents, config)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate content: " + err.Error()})
		ginContext.Abort()
		return
	}

	if len(resp.Candidates) == 0 {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "No candidates returned from Gemini"})
		ginContext.Abort()
		return
	}

	var description strings.Builder
	for _, part := range resp.Candidates[0].Content.Parts {
		if part.Text != "" {
			description.WriteString(part.Text)
		}
	}

	ginContext.JSON(http.StatusOK, gin.H{"message": "Issue completion Success", "issue_description": description.String()})
}

type IssueWithProject struct {
	*gitlab.Issue
	ProjectName string `json:"project_name"`
}

type ChildIssueItem struct {
	ID  string `json:"id"`
	IID int    `json:"iid"`
}

type ChildIssueInfo struct {
	Amount int              `json:"amount"`
	Items  []ChildIssueItem `json:"items"`
}

type IssueWithChild struct {
	IssueWithProject
	Child ChildIssueInfo `json:"child"`
}

func GetIssues(ginContext *gin.Context) {
	startTime := time.Now()
	token := ginContext.MustGet("token").(*oauth2.Token)
	sessionID := ginContext.MustGet("session_id").(string)

	labels := ginContext.Query("labels")
	search := ginContext.Query("search")
	issueIds := ginContext.Query("issue_ids")
	assigneeId := ginContext.Query("assignee_id")
	assigneeIds := ginContext.Query("assignee_ids")
	authorId := ginContext.Query("author_id")
	state := ginContext.Query("state")
	projectIds := ginContext.Query("project_ids")

	// Optional pagination limit (default 100)
	limit := 100
	if limitStr := ginContext.Query("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 500 {
			limit = l
		}
	}

	// Generate cache key from query parameters
	cacheKey := database.GenerateIssueCacheKey(labels, search, issueIds, assigneeId, assigneeIds, authorId, state, projectIds, limit)
	ginContext.Header("X-Cache-Key", cacheKey) // Debug: see what cache key is generated

	// Check cache first - return immediately if found
	// Note: Caching is skipped when projectIds is specified as those are user-specific queries
	if projectIds == "" {
		cacheStart := time.Now()
		cachedData, cacheHit := database.GetCachedIssueResponse(ginContext, cacheKey)
		if cacheHit {
			var cached []IssueWithChild
			if err := json.Unmarshal(cachedData, &cached); err == nil {
				log.Printf("[CACHE DEBUG] CACHE HIT - serving %d issues from cache, time: %v", len(cached), time.Since(cacheStart))
				ginContext.Header("X-Cache", "HIT")
				ginContext.Header("X-Timing-Cache", time.Since(cacheStart).String())
				ginContext.Header("X-Timing-Total", time.Since(startTime).String())
				ginContext.JSON(http.StatusOK, cached)
				return
			} else {
				log.Printf("[CACHE DEBUG] CACHE GET OK but Unmarshal FAILED: %v", err)
			}
		}
		log.Printf("[CACHE DEBUG] CACHE MISS - proceeding to REST/GraphQL")
		ginContext.Header("X-Cache", "MISS")
		ginContext.Header("X-Timing-Cache", time.Since(cacheStart).String())
	}

	opts := &gitlab.ListIssuesOptions{
		WithLabelDetails: gitlab.Ptr(true),
		Scope:            gitlab.Ptr("all"),
		ListOptions:      gitlab.ListOptions{PerPage: int64(limit), Page: 1},
	}

	if issueIds != "" {
		splitIssueIds := strings.Split(issueIds, ",")
		iids := make([]int64, len(splitIssueIds))
		for i, id := range splitIssueIds {
			parsedId, err := strconv.ParseInt(id, 10, 64)
			if err != nil {
				ginContext.JSON(http.StatusBadRequest, gin.H{"error": "Invalid issue IDs"})
				ginContext.Abort()
				return
			}
			iids[i] = parsedId
		}
		opts.IIDs = &iids
	}

	if authorId != "" {
		id, err := strconv.ParseInt(authorId, 10, 64)
		if err != nil {
			ginContext.JSON(http.StatusBadRequest, gin.H{"error": "Invalid author ID"})
			ginContext.Abort()
			return
		}
		opts.AuthorID = &id
	}

	if assigneeIds != "" {
		splitAssigneeIds := strings.Split(assigneeIds, ",")
		ids := make([]int64, len(splitAssigneeIds))
		for i, idStr := range splitAssigneeIds {
			id, err := strconv.ParseInt(strings.TrimSpace(idStr), 10, 64)
			if err != nil {
				ginContext.JSON(http.StatusBadRequest, gin.H{"error": "Invalid assignee IDs"})
				ginContext.Abort()
				return
			}
			ids[i] = id
		}
		opts.AssigneeID = gitlab.AssigneeID(ids)
	} else if assigneeId != "" {
		id, err := strconv.ParseInt(assigneeId, 10, 64)
		if err != nil {
			ginContext.JSON(http.StatusBadRequest, gin.H{"error": "Invalid assignee ID"})
			ginContext.Abort()
			return
		}
		assigneeID := gitlab.AssigneeID(id)
		opts.AssigneeID = assigneeID
	}

	tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
		return auth.UpdateSession(ctx, sessionID, t)
	}

	gitlabClient, err := client.GetClient(ginContext, token, tokenSaver)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
		ginContext.Abort()
		return
	}

	if search != "" {
		opts.Search = &search
	}

	if labels != "" {
		splitLabels := strings.Split(labels, ",")
		l := gitlab.LabelOptions(splitLabels)
		opts.Labels = &l
	}

	if state != "" {
		opts.State = &state
	}

	var issues []*gitlab.Issue
	restStart := time.Now()
	if projectIds != "" {
		splitProjectIds := strings.Split(projectIds, ",")
		for _, pid := range splitProjectIds {
			pOpts := &gitlab.ListProjectIssuesOptions{
				IIDs:             opts.IIDs,
				State:            opts.State,
				Labels:           opts.Labels,
				WithLabelDetails: opts.WithLabelDetails,
				Milestone:        opts.Milestone,
				Scope:            opts.Scope,
				AuthorID:         opts.AuthorID,
				AssigneeID:       opts.AssigneeID,
				OrderBy:          opts.OrderBy,
				Sort:             opts.Sort,
				Search:           opts.Search,
				IssueType:        opts.IssueType,
			}
			projectIssues, _, err := gitlabClient.Issues.ListProjectIssues(strings.TrimSpace(pid), pOpts)
			if err != nil {
				ginContext.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to fetch issues for project %s: %s", pid, err.Error())})
				ginContext.Abort()
				return
			}
			issues = append(issues, projectIssues...)
		}
	} else {
		var err error
		issues, _, err = gitlabClient.Issues.ListIssues(opts)
		if err != nil {
			ginContext.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			ginContext.Abort()
			return
		}
	}
	ginContext.Header("X-Timing-REST", time.Since(restStart).String())

	// Prepare result list
	var issuesWithChild []IssueWithChild

	if len(issues) == 0 {
		ginContext.JSON(http.StatusOK, issuesWithChild)
		return
	}

	// 1. Concurrent GraphQL Batching with Semaphore (Primary Strategy)
	// GitLab has Max Query Complexity of ~250. Each issue with hierarchy widget uses ~14 complexity.
	// Safe batch size: 250 / 14 ≈ 17. Using 10 to have more headroom under the limit.
	const BatchSize = 10
	const MaxConcurrency = 10  // Increased to process more batches in parallel
	graphqlStart := time.Now()

	graphqlEndpoint := "https://gitlab.com/api/graphql"
	if url := os.Getenv("GITLAB_BASE_URL"); url != "" {
		graphqlEndpoint = strings.TrimRight(url, "/") + "/api/graphql"
	}

	// Context data map - Use sync.Map for concurrent writes
	type AuxData struct {
		ProjectName string
		ChildCount  int
		ChildItems  []ChildIssueItem
	}
	auxMap := sync.Map{}

	// Semaphore channels for concurrency control
	sem := make(chan struct{}, MaxConcurrency)
	var parseWg sync.WaitGroup
	var parseMu sync.Mutex
	var parseErrors []string

	ctx := ginContext.Request.Context()

	// Collect unique project IDs for Redis lookup
	projectIDs := make(map[int64]bool)
	for _, issue := range issues {
		projectIDs[issue.ProjectID] = true
	}

	// Check Redis cache for project names first
	projectNameCache := make(map[int64]string)
	for projID := range projectIDs {
		if name, ok := database.GetCachedProjectName(ctx, projID); ok {
			projectNameCache[projID] = name
		}
	}

	// Iterate issues in chunks - each chunk launches a goroutine
	for i := 0; i < len(issues); i += BatchSize {
		parseWg.Add(1)
		go func(batch []*gitlab.Issue) {
			defer parseWg.Done()
			sem <- struct{}{}        // Acquire semaphore
			defer func() { <-sem }() // Release semaphore

			var queryBuilder strings.Builder
			queryBuilder.WriteString("query {")

			for _, issue := range batch {
				gid := fmt.Sprintf("gid://gitlab/WorkItem/%d", issue.ID)
				alias := fmt.Sprintf("item_%d", issue.ID)

				queryBuilder.WriteString(fmt.Sprintf(`
				%s: workItem(id: "%s") {
					id
					widgets {
						... on WorkItemWidgetHierarchy {
							children {
								count
								nodes {
									id
									iid
								}
							}
						}
					}
				}
			`, alias, gid))
			}
			queryBuilder.WriteString("}")

			respBody, errGQL := sendGraphQLRequest(ctx, graphqlEndpoint, token.AccessToken, queryBuilder.String(), map[string]interface{}{})

			if errGQL == nil {
				var rawResp struct {
					Data   map[string]interface{} `json:"data"`
					Errors []struct {
						Message string `json:"message"`
					} `json:"errors"`
				}

				if err := json.Unmarshal(respBody, &rawResp); err == nil && len(rawResp.Errors) == 0 {
					for alias, rawNode := range rawResp.Data {
						data := AuxData{ChildItems: []ChildIssueItem{}}

						if nodeMap, ok := rawNode.(map[string]interface{}); ok {
							// 1. Extract Project Name
							if proj, ok := nodeMap["project"].(map[string]interface{}); ok {
								if name, ok := proj["nameWithNamespace"].(string); ok {
									data.ProjectName = name
								}
							}

							// 2. Extract Hierarchy
							if widgets, ok := nodeMap["widgets"].([]interface{}); ok {
								for _, w := range widgets {
									if widgetMap, ok := w.(map[string]interface{}); ok {
										if children, ok := widgetMap["children"].(map[string]interface{}); ok {
											if count, ok := children["count"].(float64); ok {
												data.ChildCount = int(count)
											}
											if nodes, ok := children["nodes"].([]interface{}); ok {
												for _, n := range nodes {
													if node, ok := n.(map[string]interface{}); ok {
														childID, _ := node["id"].(string)
														childIIDStr, _ := node["iid"].(string)

														childIID, _ := strconv.Atoi(childIIDStr)

														data.ChildItems = append(data.ChildItems, ChildIssueItem{
															ID:  childID,
															IID: childIID,
														})
													}
												}
											}
											if data.ChildCount > 0 || len(data.ChildItems) > 0 {
												break
											}
										}
									}
								}
							}
						}

						idStr := strings.TrimPrefix(alias, "item_")
						idInt, _ := strconv.Atoi(idStr)
						auxMap.Store(int64(idInt), data)
					}
				} else {
					parseMu.Lock()
					parseErrors = append(parseErrors, fmt.Sprintf("ParseError: %v", rawResp.Errors))
					parseMu.Unlock()
				}
			} else {
				parseMu.Lock()
				parseErrors = append(parseErrors, fmt.Sprintf("ReqFailed: %v", errGQL))
				parseMu.Unlock()
			}
		}(func() []*gitlab.Issue {
			end := i + BatchSize
			if end > len(issues) {
				end = len(issues)
			}
			return issues[i:end]
		}())
	}

	parseWg.Wait()
	ginContext.Header("X-Timing-GraphQL", time.Since(graphqlStart).String())

	// Add debug header if any errors occurred
	if len(parseErrors) > 0 {
		ginContext.Header("X-Debug-GraphQL-Status", strings.Join(parseErrors, "; "))
	} else {
		ginContext.Header("X-Debug-GraphQL-Status", "Success")
	}

	// 2. Construct Final Response (With REST Fallback)
	// Use pre-fetched Redis cache first, then in-memory cache, then REST fallback
	projectCache := sync.Map{}
	var cacheWg sync.WaitGroup

	for _, issue := range issues {
		// Retrieve from sync.Map
		var aux AuxData
		if v, ok := auxMap.Load(issue.ID); ok {
			aux = v.(AuxData)
		}

		// Validation: Ensure ChildItems is non-nil
		if aux.ChildItems == nil {
			aux.ChildItems = []ChildIssueItem{}
		}

		// Fallback: If ProjectName is missing from GraphQL, try Redis cache first, then in-memory, then REST
		finalProjectName := aux.ProjectName
		if finalProjectName == "" {
			// Check Redis cache first
			if name, ok := projectNameCache[issue.ProjectID]; ok {
				finalProjectName = name
			} else if cached, ok := projectCache.Load(issue.ProjectID); ok {
				// Fallback to in-memory cache
				finalProjectName = cached.(string)
			} else {
				// Fetch via REST and cache in parallel
				cacheWg.Add(1)
				go func(projID interface{}) {
					defer cacheWg.Done()
					project, _, err := gitlabClient.Projects.GetProject(projID, nil)
					if err == nil {
						projectCache.Store(projID, project.NameWithNamespace)
					}
				}(issue.ProjectID)
			}
		}

		issuesWithChild = append(issuesWithChild, IssueWithChild{
			IssueWithProject: IssueWithProject{
				Issue:       issue,
				ProjectName: finalProjectName,
			},
			Child: ChildIssueInfo{
				Amount: aux.ChildCount,
				Items:  aux.ChildItems,
			},
		})
	}
	cacheWg.Wait()

	// Populate finalProjectName from cache for any that were fetched in parallel
	for i, iwc := range issuesWithChild {
		if iwc.ProjectName == "" {
			if cached, ok := projectCache.Load(issuesWithChild[i].ProjectID); ok {
				issuesWithChild[i].ProjectName = cached.(string)
			}
		}
	}

	// Cache the response synchronously for debugging
	marshalStart := time.Now()
	if data, err := json.Marshal(issuesWithChild); err == nil {
		cacheWriteStart := time.Now()
		// Use context.Background() - Gin context may be cancelled after response starts
		if cacheErr := database.SetCachedIssueResponse(context.Background(), cacheKey, data); cacheErr != nil {
			ginContext.Header("X-Cache-Error", cacheErr.Error())
		}
		ginContext.Header("X-Timing-CacheWrite", time.Since(cacheWriteStart).String())
		ginContext.Header("X-Timing-JSONMarshal", time.Since(marshalStart).String())
		ginContext.Header("X-Issues-Count", fmt.Sprintf("%d", len(issuesWithChild)))
		ginContext.Header("X-Response-Size", fmt.Sprintf("%d bytes", len(data)))
		ginContext.Header("X-Redis-Key", database.GetRedisKeyForDebug(cacheKey))
	}

	ginContext.Header("X-Timing-Total", time.Since(startTime).String())
	ginContext.JSON(http.StatusOK, issuesWithChild)
}

func GetIssueComments(ginContext *gin.Context) {
	token := ginContext.MustGet("token").(*oauth2.Token)
	sessionID := ginContext.MustGet("session_id").(string)

	tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
		return auth.UpdateSession(ctx, sessionID, t)
	}

	gitlabClient, err := client.GetClient(ginContext, token, tokenSaver)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
		ginContext.Abort()
		return
	}

	projectID := ginContext.Param("id")
	issueID, err := strconv.ParseInt(ginContext.Param("issue_id"), 10, 64)
	if err != nil {
		ginContext.JSON(http.StatusBadRequest, gin.H{"error": "Invalid issue ID"})
		ginContext.Abort()
		return
	}

	var orderBy = "created_at"
	var sort = "desc"

	notes, _, err := gitlabClient.Notes.ListIssueNotes(projectID, issueID, &gitlab.ListIssueNotesOptions{OrderBy: &orderBy, Sort: &sort})
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		ginContext.Abort()
		return
	}

	ginContext.JSON(http.StatusOK, notes)
}

func CreateIssue(ginContext *gin.Context) {
	token := ginContext.MustGet("token").(*oauth2.Token)
	sessionID := ginContext.MustGet("session_id").(string)

	tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
		return auth.UpdateSession(ctx, sessionID, t)
	}

	gitlabClient, err := client.GetClient(ginContext, token, tokenSaver)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
		ginContext.Abort()
		return
	}

	projectID := ginContext.Param("id")

	var issue gitlab.CreateIssueOptions
	if err := ginContext.BindJSON(&issue); err != nil {
		ginContext.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		ginContext.Abort()
		return
	}

	newIssue, _, err := gitlabClient.Issues.CreateIssue(projectID, &issue)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		ginContext.Abort()
		return
	}

	ginContext.JSON(http.StatusCreated, gin.H{"message": "Issue created successfully", "issue": newIssue})
}

type CreateIssueWithChildRequest struct {
	gitlab.CreateIssueOptions
	ChildIssues []gitlab.CreateIssueOptions `json:"child_issues"`
}

type ChildIssueResult struct {
	Title  string        `json:"title"`
	Status string        `json:"status"` // "success" or "failed"
	Issue  *gitlab.Issue `json:"issue,omitempty"`
	Error  string        `json:"error,omitempty"`
}

func CreateIssueWithChild(ginContext *gin.Context) {
	token := ginContext.MustGet("token").(*oauth2.Token)
	sessionID := ginContext.MustGet("session_id").(string)

	tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
		return auth.UpdateSession(ctx, sessionID, t)
	}

	gitlabClient, err := client.GetClient(ginContext, token, tokenSaver)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
		ginContext.Abort()
		return
	}

	projectID := ginContext.Param("id")

	var request CreateIssueWithChildRequest
	if err := ginContext.BindJSON(&request); err != nil {
		ginContext.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		ginContext.Abort()
		return
	}

	// 1. Create Parent Issue
	parentIssue, _, err := gitlabClient.Issues.CreateIssue(projectID, &request.CreateIssueOptions)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create parent issue: " + err.Error()})
		ginContext.Abort()
		return
	}

	// 2. Process Child Issues
	var childResults []ChildIssueResult
	for _, childOpts := range request.ChildIssues {
		// Enforce IssueType as "task" if not specified, to emulate "child task" behavior if supported
		if childOpts.IssueType == nil {
			childOpts.IssueType = gitlab.Ptr("task")
		}

		childIssue, _, err := gitlabClient.Issues.CreateIssue(projectID, &childOpts)
		if err != nil {
			childResults = append(childResults, ChildIssueResult{
				Title:  *childOpts.Title,
				Status: "failed",
				Error:  err.Error(),
			})
			continue
		}

		// 3. Link Child to Parent via GraphQL (Hierarchy Widget)
		// We use GraphQL because REST API does not support establishing strictly "Child" hierarchy (Work Items) easily.
		// Note: CreateIssue returns int, linkChildTask expects int64 or needs conversion
		errLink := linkChildTask(ginContext, token.AccessToken, int64(parentIssue.IID), int64(childIssue.IID), int64(parentIssue.ProjectID))
		if errLink != nil {
			// If linking fails, we still mark the child creation as success but note the linking error.
			childResults = append(childResults, ChildIssueResult{
				Title:  *childOpts.Title,
				Status: "success_unlinked",
				Issue:  childIssue,
				Error:  "Failed to link to parent: " + errLink.Error(),
			})
		} else {
			childResults = append(childResults, ChildIssueResult{
				Title:  *childOpts.Title,
				Status: "success",
				Issue:  childIssue,
			})
		}
	}

	ginContext.JSON(http.StatusCreated, gin.H{
		"message":       "Issue creation process completed",
		"parent_issue":  parentIssue,
		"child_results": childResults,
	})
}

func linkChildTask(ctx context.Context, accessToken string, parentIID int64, childIID int64, projectID int64) error {

	graphqlEndpoint := "https://gitlab.com/api/graphql" // DANGER: Hardcoded. Should come from config/env.
	if url := os.Getenv("GITLAB_BASE_URL"); url != "" {
		graphqlEndpoint = strings.TrimRight(url, "/") + "/api/graphql"
	}

	query := `
		query($projectPath: ID!, $childIID: String!, $parentIID: String!) {
		  project(fullPath: $projectPath) {
			parent: workItem(iid: $parentIID) {
			  id
			}
			child: workItem(iid: $childIID) {
			  id
			}
		  }
		}
	`
	projectGlobalID := fmt.Sprintf("gid://gitlab/Project/%d", projectID)
	query = `
		query($projectIds: [ID!]!, $childIID: String!, $parentIID: String!) {
		  projects(ids: $projectIds) {
			nodes {
			  parent: workItems(iids: [$parentIID]) {
				nodes { id }
			  }
			  child: workItems(iids: [$childIID]) {
				nodes { id }
			  }
			}
		  }
		}
	`

	variables := map[string]interface{}{
		"projectIds": []string{projectGlobalID},
		"childIID":   fmt.Sprintf("%d", childIID),
		"parentIID":  fmt.Sprintf("%d", parentIID),
	}

	var respBody []byte
	var errGraph error
	respBody, errGraph = sendGraphQLRequest(ctx, graphqlEndpoint, accessToken, query, variables)
	if errGraph != nil {
		return fmt.Errorf("failed to query work item GIDs: %w", errGraph)
	}

	var queryResp struct {
		Data struct {
			Projects struct {
				Nodes []struct {
					Parent struct {
						Nodes []struct {
							ID string `json:"id"`
						} `json:"nodes"`
					} `json:"parent"`
					Child struct {
						Nodes []struct {
							ID string `json:"id"`
						} `json:"nodes"`
					} `json:"child"`
				} `json:"nodes"`
			} `json:"projects"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}

	if err := json.Unmarshal(respBody, &queryResp); err != nil {
		return fmt.Errorf("failed to parse GraphQL response: %w", err)
	}
	if len(queryResp.Errors) > 0 {
		return fmt.Errorf("graphql query error: %s", queryResp.Errors[0].Message)
	}

	if len(queryResp.Data.Projects.Nodes) == 0 {
		return fmt.Errorf("project not found")
	}

	projectNode := queryResp.Data.Projects.Nodes[0]

	if len(projectNode.Parent.Nodes) == 0 {
		return fmt.Errorf("parent work item not found")
	}
	if len(projectNode.Child.Nodes) == 0 {
		return fmt.Errorf("child work item not found")
	}

	parentWorkItemID := projectNode.Parent.Nodes[0].ID
	childWorkItemID := projectNode.Child.Nodes[0].ID

	if parentWorkItemID == "" || childWorkItemID == "" {
		return fmt.Errorf("could not resolve Work Item IDs. Parent: %s, Child: %s", parentWorkItemID, childWorkItemID)
	}

	mutation := `
		mutation($id: WorkItemID!, $parentId: WorkItemID) {
		  workItemUpdate(input: {id: $id, hierarchyWidget: {parentId: $parentId}}) {
			errors
		  }
		}
	`
	mutVars := map[string]interface{}{
		"id":       childWorkItemID,
		"parentId": parentWorkItemID,
	}

	var mutRespBody []byte
	var errMut error
	mutRespBody, errMut = sendGraphQLRequest(ctx, graphqlEndpoint, accessToken, mutation, mutVars)
	if errMut != nil {
		return fmt.Errorf("failed to execute linkage mutation: %w", errMut)
	}

	var mutResp struct {
		Data struct {
			WorkItemUpdate struct {
				Errors []string `json:"errors"`
			} `json:"workItemUpdate"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}

	if err := json.Unmarshal(mutRespBody, &mutResp); err != nil {
		return fmt.Errorf("failed to parse mutation response: %w", err)
	}

	if len(mutResp.Errors) > 0 {
		return fmt.Errorf("graphql mutation error: %s", mutResp.Errors[0].Message)
	}
	if len(mutResp.Data.WorkItemUpdate.Errors) > 0 {
		return fmt.Errorf("work item update error: %v", mutResp.Data.WorkItemUpdate.Errors)
	}

	return nil
}

func sendGraphQLRequest(ctx context.Context, endpoint, token, query string, variables map[string]interface{}) ([]byte, error) {
	return sendGraphQLRequestWithClient(ctx, endpoint, token, query, variables, client.GetGraphQLHTTPClient(), client.CircuitBreakerGraphQL)
}

// sendGraphQLRequestWithClient uses a shared connection-pooled HTTP client for GraphQL requests
// with circuit breaker protection. This replaces the previous implementation that created
// a new http.Client per request, which caused TCP handshake overhead on every call.
// The circuit breaker prevents cascade failures when GitLab API is struggling.
func sendGraphQLRequestWithClient(ctx context.Context, endpoint, token, query string, variables map[string]interface{}, httpClient *http.Client, circuitBreakerName string) ([]byte, error) {
	// Execute through circuit breaker
	cb := client.GetCircuitBreaker(circuitBreakerName)

	result, err := cb.Execute(func() (interface{}, error) {
		return sendGraphQLRaw(ctx, endpoint, token, query, variables, httpClient)
	})

	if err != nil {
		if errors.Is(err, gobreaker.ErrOpenState) {
			return nil, client.ErrCircuitOpen
		}
		return nil, err
	}

	return result.([]byte), nil
}

// sendGraphQLRaw performs the actual GraphQL HTTP request without circuit breaker
func sendGraphQLRaw(ctx context.Context, endpoint, token, query string, variables map[string]interface{}, httpClient *http.Client) ([]byte, error) {
	reqBody, err := json.Marshal(map[string]interface{}{
		"query":     query,
		"variables": variables,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	// Use shared connection-pooled client instead of creating new one per request
	if httpClient == nil {
		httpClient = client.GetGraphQLHTTPClient()
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := ioutil.ReadAll(resp.Body)
		return nil, fmt.Errorf("graphql request failed with status %d: %s", resp.StatusCode, string(body))
	}

	return ioutil.ReadAll(resp.Body)
}

func UpdateIssue(ginContext *gin.Context) {
	token := ginContext.MustGet("token").(*oauth2.Token)
	sessionID := ginContext.MustGet("session_id").(string)

	tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
		return auth.UpdateSession(ctx, sessionID, t)
	}

	gitlabClient, err := client.GetClient(ginContext, token, tokenSaver)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
		ginContext.Abort()
		return
	}

	projectID := ginContext.Param("id")
	issueIDStr := ginContext.Param("issue_id")
	issueID, err := strconv.ParseInt(issueIDStr, 10, 64)
	if err != nil {
		ginContext.JSON(http.StatusBadRequest, gin.H{"error": "Invalid issue ID"})
		ginContext.Abort()
		return
	}

	var issue gitlab.UpdateIssueOptions
	if err := ginContext.BindJSON(&issue); err != nil {
		ginContext.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		ginContext.Abort()
		return
	}

	updatedIssue, _, err := gitlabClient.Issues.UpdateIssue(projectID, issueID, &issue)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		ginContext.Abort()
		return
	}

	// Invalidate board cache since issue labels may have changed
	database.InvalidateBoardCache(ginContext.Request.Context(), projectID)

	ginContext.JSON(http.StatusOK, updatedIssue)
}

func GetLabels(ginContext *gin.Context) {
	token := ginContext.MustGet("token").(*oauth2.Token)
	sessionID := ginContext.MustGet("session_id").(string)

	tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
		return auth.UpdateSession(ctx, sessionID, t)
	}

	gitlabClient, err := client.GetClient(ginContext, token, tokenSaver)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
		ginContext.Abort()
		return
	}

	projectID := ginContext.Param("id")

	labels, _, err := gitlabClient.Labels.ListLabels(projectID, &gitlab.ListLabelsOptions{})
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		ginContext.Abort()
		return
	}

	ginContext.JSON(http.StatusOK, labels)
}

func CreateIssueComment(ginContext *gin.Context) {
	token := ginContext.MustGet("token").(*oauth2.Token)
	sessionID := ginContext.MustGet("session_id").(string)

	tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
		return auth.UpdateSession(ctx, sessionID, t)
	}

	gitlabClient, err := client.GetClient(ginContext, token, tokenSaver)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
		ginContext.Abort()
		return
	}

	projectID := ginContext.Param("id")
	issueID, err := strconv.ParseInt(ginContext.Param("issue_id"), 10, 64)
	if err != nil {
		ginContext.JSON(http.StatusBadRequest, gin.H{"error": "Invalid issue ID"})
		ginContext.Abort()
		return
	}

	var opt gitlab.CreateIssueNoteOptions
	if err := ginContext.BindJSON(&opt); err != nil {
		ginContext.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		ginContext.Abort()
		return
	}

	note, _, err := gitlabClient.Notes.CreateIssueNote(projectID, issueID, &opt)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		ginContext.Abort()
		return
	}

	ginContext.JSON(http.StatusCreated, note)
}

type CreateIssueEvidenceRequest struct {
	Comment  string `json:"comment"`
	Evidence string `json:"evidence"`
}

func CreateIssueEvidence(ginContext *gin.Context) {
	token := ginContext.MustGet("token").(*oauth2.Token)
	sessionID := ginContext.MustGet("session_id").(string)

	tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
		return auth.UpdateSession(ctx, sessionID, t)
	}

	gitlabClient, err := client.GetClient(ginContext, token, tokenSaver)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
		ginContext.Abort()
		return
	}

	projectID := ginContext.Param("id")
	issueID, err := strconv.ParseInt(ginContext.Param("issue_id"), 10, 64)
	if err != nil {
		ginContext.JSON(http.StatusBadRequest, gin.H{"error": "Invalid issue ID"})
		ginContext.Abort()
		return
	}

	var req CreateIssueEvidenceRequest
	if err := ginContext.BindJSON(&req); err != nil {
		ginContext.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		ginContext.Abort()
		return
	}

	var formattedBody strings.Builder
	formattedBody.WriteString("**Evidence:**\n")
	formattedBody.WriteString(req.Evidence)
	formattedBody.WriteString("\n\n")

	formattedBody.WriteString(req.Comment)

	opt := &gitlab.CreateIssueNoteOptions{
		Body: gitlab.Ptr(formattedBody.String()),
	}

	note, _, err := gitlabClient.Notes.CreateIssueNote(projectID, issueID, opt)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		ginContext.Abort()
		return
	}

	ginContext.JSON(http.StatusCreated, note)
}

func UpdateIssueComment(ginContext *gin.Context) {
	token := ginContext.MustGet("token").(*oauth2.Token)
	sessionID := ginContext.MustGet("session_id").(string)

	tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
		return auth.UpdateSession(ctx, sessionID, t)
	}

	gitlabClient, err := client.GetClient(ginContext, token, tokenSaver)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
		ginContext.Abort()
		return
	}

	projectID := ginContext.Param("id")
	issueID, _ := strconv.Atoi(ginContext.Param("issue_id"))
	noteID, _ := strconv.Atoi(ginContext.Param("note_id"))

	var opt gitlab.UpdateIssueNoteOptions
	if err := ginContext.BindJSON(&opt); err != nil {
		ginContext.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		ginContext.Abort()
		return
	}

	note, _, err := gitlabClient.Notes.UpdateIssueNote(projectID, int64(issueID), int64(noteID), &opt)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		ginContext.Abort()
		return
	}

	ginContext.JSON(http.StatusOK, note)
}

func DeleteIssueComment(ginContext *gin.Context) {
	token := ginContext.MustGet("token").(*oauth2.Token)
	sessionID := ginContext.MustGet("session_id").(string)

	tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
		return auth.UpdateSession(ctx, sessionID, t)
	}

	gitlabClient, err := client.GetClient(ginContext, token, tokenSaver)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
		ginContext.Abort()
		return
	}

	projectID := ginContext.Param("id")
	issueID, _ := strconv.Atoi(ginContext.Param("issue_id"))
	noteID, _ := strconv.Atoi(ginContext.Param("note_id"))

	_, err = gitlabClient.Notes.DeleteIssueNote(projectID, int64(issueID), int64(noteID))
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		ginContext.Abort()
		return
	}

	ginContext.Status(http.StatusNoContent)
}

func GetIssue(ginContext *gin.Context) {
	token := ginContext.MustGet("token").(*oauth2.Token)
	sessionID := ginContext.MustGet("session_id").(string)

	tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
		return auth.UpdateSession(ctx, sessionID, t)
	}

	gitlabClient, err := client.GetClient(ginContext, token, tokenSaver)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
		ginContext.Abort()
		return
	}

	projectID := ginContext.Param("id")
	issueID, err := strconv.ParseInt(ginContext.Param("issue_id"), 10, 64)
	if err != nil {
		ginContext.JSON(http.StatusBadRequest, gin.H{"error": "Invalid issue ID"})
		ginContext.Abort()
		return
	}

	issue, _, err := gitlabClient.Issues.GetIssue(projectID, issueID)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	project, _, err := gitlabClient.Projects.GetProject(issue.ProjectID, nil)
	projectName := ""
	if err == nil {
		projectName = project.NameWithNamespace
	}

    graphqlEndpoint := "https://gitlab.com/api/graphql"
    if url := os.Getenv("GITLAB_BASE_URL"); url != "" {
        graphqlEndpoint = strings.TrimRight(url, "/") + "/api/graphql"
    }

    gid := fmt.Sprintf("gid://gitlab/WorkItem/%d", issue.ID)
    query := "query($id: WorkItemID!) { workItem(id: $id) { widgets { ... on WorkItemWidgetHierarchy { children { count nodes { id iid } } } } } }"
    variables := map[string]interface{}{ "id": gid }

    var childInfo ChildIssueInfo
    childInfo.Items = []ChildIssueItem{}

    respBody, errGQL := sendGraphQLRequest(ginContext, graphqlEndpoint, token.AccessToken, query, variables)
    if errGQL == nil {
        var rawResp struct {
            Data struct {
                WorkItem struct {
                    Widgets []struct {
                        Children struct {
                            Count float64 `json:"count"`
                            Nodes []struct {
                                ID  string `json:"id"`
                                IID string `json:"iid"`
                            } `json:"nodes"`
                        } `json:"children"`
                    } `json:"widgets"`
                } `json:"workItem"`
            } `json:"data"`
        }
        if err := json.Unmarshal(respBody, &rawResp); err == nil {
             if rawResp.Data.WorkItem.Widgets != nil {
                 for _, w := range rawResp.Data.WorkItem.Widgets {
                     if w.Children.Count > 0 || len(w.Children.Nodes) > 0 {
                         childInfo.Amount = int(w.Children.Count)
                         for _, n := range w.Children.Nodes {
                             iidInt, _ := strconv.Atoi(n.IID)
                             childInfo.Items = append(childInfo.Items, ChildIssueItem{
                                 ID:  n.ID,
                                 IID: iidInt,
                             })
                         }
                     }
                 }
             }
        }
    }

	result := IssueWithChild{
		IssueWithProject: IssueWithProject{
			Issue:       issue,
			ProjectName: projectName,
		},
        Child: childInfo,
	}

	ginContext.JSON(http.StatusOK, result)
}

func CreateIssueLink(ginContext *gin.Context) {
	token := ginContext.MustGet("token").(*oauth2.Token)
	sessionID := ginContext.MustGet("session_id").(string)

	tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
		return auth.UpdateSession(ctx, sessionID, t)
	}

	gitlabClient, err := client.GetClient(ginContext, token, tokenSaver)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
		ginContext.Abort()
		return
	}

	projectID := ginContext.Param("id")
	issueID, _ := strconv.ParseInt(ginContext.Param("issue_id"), 10, 64)

	var opt gitlab.CreateIssueLinkOptions
	if err := ginContext.BindJSON(&opt); err != nil {
		ginContext.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		ginContext.Abort()
		return
	}

	link, _, err := gitlabClient.IssueLinks.CreateIssueLink(projectID, issueID, &opt)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ginContext.JSON(http.StatusCreated, link)
}

func DeleteIssueLink(ginContext *gin.Context) {
	token := ginContext.MustGet("token").(*oauth2.Token)
	sessionID := ginContext.MustGet("session_id").(string)

	tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
		return auth.UpdateSession(ctx, sessionID, t)
	}

	gitlabClient, err := client.GetClient(ginContext, token, tokenSaver)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
		ginContext.Abort()
		return
	}

	projectID := ginContext.Param("id")
	issueID, _ := strconv.ParseInt(ginContext.Param("issue_id"), 10, 64)
	linkID, _ := strconv.ParseInt(ginContext.Param("link_id"), 10, 64)

	link, _, err := gitlabClient.IssueLinks.DeleteIssueLink(projectID, issueID, linkID)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ginContext.JSON(http.StatusOK, link)
}

func GetIssueLinks(ginContext *gin.Context) {
	token := ginContext.MustGet("token").(*oauth2.Token)
	sessionID := ginContext.MustGet("session_id").(string)

	tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
		return auth.UpdateSession(ctx, sessionID, t)
	}

	gitlabClient, err := client.GetClient(ginContext, token, tokenSaver)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
		ginContext.Abort()
		return
	}

	projectID := ginContext.Param("id")
	issueID, _ := strconv.ParseInt(ginContext.Param("issue_id"), 10, 64)

	links, _, err := gitlabClient.IssueLinks.ListIssueRelations(projectID, issueID, nil)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ginContext.JSON(http.StatusOK, links)
}

type CreateChildIssueRequest struct {
    gitlab.CreateIssueOptions
    ExistingChildIID *int `json:"existing_child_iid"`
}

func CreateChildIssue(ginContext *gin.Context) {
    token := ginContext.MustGet("token").(*oauth2.Token)
    sessionID := ginContext.MustGet("session_id").(string)

    tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
        return auth.UpdateSession(ctx, sessionID, t)
    }

    gitlabClient, err := client.GetClient(ginContext, token, tokenSaver)
    if err != nil {
        ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
        ginContext.Abort()
        return
    }

    projectID := ginContext.Param("id")
    parentIID, err := strconv.ParseInt(ginContext.Param("issue_id"), 10, 64)
    if err != nil {
        ginContext.JSON(http.StatusBadRequest, gin.H{"error": "Invalid parent issue ID"})
        ginContext.Abort()
        return
    }

    parentProjectID, _ := strconv.ParseInt(projectID, 10, 64)

    var request CreateChildIssueRequest
    if err := ginContext.BindJSON(&request); err != nil {
        ginContext.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
        ginContext.Abort()
        return
    }

    var childIID int64
    var childIssue *gitlab.Issue

    if request.ExistingChildIID != nil {
        childIID = int64(*request.ExistingChildIID)
    } else {
        if request.IssueType == nil {
            request.IssueType = gitlab.Ptr("task")
        }
        newIssue, _, err := gitlabClient.Issues.CreateIssue(projectID, &request.CreateIssueOptions)
        if err != nil {
            ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create child issue: " + err.Error()})
            return
        }
        childIID = int64(newIssue.IID)
        childIssue = newIssue
    }

    errLink := linkChildTask(ginContext, token.AccessToken, parentIID, childIID, parentProjectID)
    if errLink != nil {
        status := "success_unlinked"
        if request.ExistingChildIID != nil {
            status = "failed"
            ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to link existing issue: " + errLink.Error()})
            return
        }
        ginContext.JSON(http.StatusCreated, gin.H{
            "message": "Child issue created but failed to link",
            "issue":   childIssue,
            "status":  status,
            "error":   errLink.Error(),
        })
        return
    }

    if request.ExistingChildIID != nil {
        ginContext.JSON(http.StatusOK, gin.H{"message": "Child issue linked successfully"})
    } else {
        ginContext.JSON(http.StatusCreated, gin.H{"message": "Child issue created and linked successfully", "issue": childIssue})
    }
}

func UnlinkChildIssue(ginContext *gin.Context) {
    token := ginContext.MustGet("token").(*oauth2.Token)
    sessionID := ginContext.MustGet("session_id").(string)

    tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
        return auth.UpdateSession(ctx, sessionID, t)
    }

    _, err := client.GetClient(ginContext, token, tokenSaver)
    if err != nil {
        ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
        ginContext.Abort()
        return
    }

    projectIDStr := ginContext.Param("id")
    projectID, _ := strconv.ParseInt(projectIDStr, 10, 64)

    childIID, err := strconv.ParseInt(ginContext.Param("child_id"), 10, 64)
    if err != nil {
        ginContext.JSON(http.StatusBadRequest, gin.H{"error": "Invalid child issue ID"})
        ginContext.Abort()
        return
    }

    errUnlink := unlinkChildTask(ginContext, token.AccessToken, childIID, projectID)
    if errUnlink != nil {
        ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to unlink child issue: " + errUnlink.Error()})
        return
    }

    ginContext.Status(http.StatusNoContent)
}

func unlinkChildTask(ctx context.Context, accessToken string, childIID int64, projectID int64) error {
    graphqlEndpoint := "https://gitlab.com/api/graphql"
    if url := os.Getenv("GITLAB_BASE_URL"); url != "" {
        graphqlEndpoint = strings.TrimRight(url, "/") + "/api/graphql"
    }

    projectGlobalID := fmt.Sprintf("gid://gitlab/Project/%d", projectID)
    query := `
        query($projectIds: [ID!]!, $childIID: String!) {
          projects(ids: $projectIds) {
            nodes {
              child: workItems(iids: [$childIID]) {
                nodes { id }
              }
            }
          }
        }
    `

    variables := map[string]interface{}{
        "projectIds": []string{projectGlobalID},
        "childIID":   fmt.Sprintf("%d", childIID),
    }

    var respBody []byte
    var errGraph error
    respBody, errGraph = sendGraphQLRequest(ctx, graphqlEndpoint, accessToken, query, variables)
    if errGraph != nil {
        return fmt.Errorf("failed to query work item GID: %w", errGraph)
    }

    var queryResp struct {
        Data struct {
            Projects struct {
                Nodes []struct {
                    Child struct {
                        Nodes []struct {
                            ID string `json:"id"`
                        } `json:"nodes"`
                    } `json:"child"`
                } `json:"nodes"`
            } `json:"projects"`
        } `json:"data"`
        Errors []struct {
            Message string `json:"message"`
        } `json:"errors"`
    }

    if err := json.Unmarshal(respBody, &queryResp); err != nil {
        return fmt.Errorf("failed to parse GraphQL response: %w", err)
    }
    if len(queryResp.Errors) > 0 {
        return fmt.Errorf("graphql query error: %s", queryResp.Errors[0].Message)
    }

    if len(queryResp.Data.Projects.Nodes) == 0 || len(queryResp.Data.Projects.Nodes[0].Child.Nodes) == 0 {
        return fmt.Errorf("child work item not found")
    }

    childWorkItemID := queryResp.Data.Projects.Nodes[0].Child.Nodes[0].ID

    mutation := `
        mutation($id: WorkItemID!) {
          workItemUpdate(input: {id: $id, hierarchyWidget: {parentId: null}}) {
            errors
          }
        }
    `
    mutVars := map[string]interface{}{
        "id": childWorkItemID,
    }

    var mutRespBody []byte
    var errMut error
    mutRespBody, errMut = sendGraphQLRequest(ctx, graphqlEndpoint, accessToken, mutation, mutVars)
    if errMut != nil {
        return fmt.Errorf("failed to execute unlinked mutation: %w", errMut)
    }

    var mutResp struct {
        Data struct {
            WorkItemUpdate struct {
                Errors []string `json:"errors"`
            } `json:"workItemUpdate"`
        } `json:"data"`
    }

    if err := json.Unmarshal(mutRespBody, &mutResp); err != nil {
        return fmt.Errorf("failed to parse mutation response: %w", err)
    }

    if len(mutResp.Data.WorkItemUpdate.Errors) > 0 {
        return fmt.Errorf("work item update error: %v", mutResp.Data.WorkItemUpdate.Errors)
    }

    return nil
}

