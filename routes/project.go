package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"qa-extension-backend/client"
	"qa-extension-backend/auth"
	"qa-extension-backend/database"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"golang.org/x/sync/errgroup"
	gitlab "gitlab.com/gitlab-org/api/client-go"
	"golang.org/x/oauth2"
)

func GetProjects(ginContext *gin.Context) {
	token := ginContext.MustGet("token").(*oauth2.Token)
	sessionID := ginContext.MustGet("session_id").(string)

	fmt.Printf("Token: ...%s\n", token.AccessToken[len(token.AccessToken)-5:])

	tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
		return auth.UpdateSession(ctx, sessionID, t)
	}

	gitlabClient, err := client.GetClient(ginContext, token, tokenSaver)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
		ginContext.Abort()
		return
	}

	_ = godotenv.Load() // Ignoring error as it might be already loaded or optional

	search := ginContext.Query("search")

	opts := &gitlab.ListProjectsOptions{
		Membership: gitlab.Ptr(true),
	}

	if search != "" {
		opts.Search = &search
	}

	projects, _, err := gitlabClient.Projects.ListProjects(opts)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		ginContext.Abort()
		return
	}

	ginContext.JSON(http.StatusOK, gin.H{
		"projects": projects,
	})
}

func GetProject(ginContext *gin.Context) {
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
	project, _, err := gitlabClient.Projects.GetProject(projectID, &gitlab.GetProjectOptions{})
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		ginContext.Abort()
		return
	}

	ginContext.JSON(http.StatusOK, gin.H{
		"project": project,
	})
}

func GetProjectLabels(ginContext *gin.Context) {
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

func GetProjectMembers(ginContext *gin.Context) {
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
	members, _, err := gitlabClient.ProjectMembers.ListAllProjectMembers(projectID, &gitlab.ListProjectMembersOptions{})
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		ginContext.Abort()
		return
	}

	ginContext.JSON(http.StatusOK, gin.H{
		"members": members,
	})
}


func GetProjectIssues(ginContext *gin.Context) {
	token := ginContext.MustGet("token").(*oauth2.Token)
	sessionID := ginContext.MustGet("session_id").(string)

	projectID := ginContext.Param("id")
	issueIds := ginContext.Query("issue_ids")
	labels := ginContext.Query("labels")
	search := ginContext.Query("search")
	assigneeId := ginContext.Query("assignee_id")
	authorId := ginContext.Query("author_id")
	state := ginContext.Query("state")


	opts := &gitlab.ListProjectIssuesOptions{
		WithLabelDetails: gitlab.Ptr(true),
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

	if assigneeId != "" {
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

	issues, _, err := gitlabClient.Issues.ListProjectIssues(projectID, opts)

	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		ginContext.Abort()
		return
	}

	ginContext.JSON(http.StatusOK, issues)
}

// GetGitLabClient is a variable to allow mocking in tests
var GetGitLabClient = client.GetClient

// Response structures
type LabelResponse struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Color     string `json:"color"`
	TextColor string `json:"text_color"`
}

type IssueResponse struct {
	ID          int                     `json:"id"`
	IID         int                     `json:"iid"`
	Title       string                  `json:"title"`
	Description string                  `json:"description"`
	State       string                  `json:"state"`
	Labels      []*LabelResponse        `json:"labels"`
	Assignees   []*gitlab.IssueAssignee `json:"assignees"`
	Author      *gitlab.IssueAuthor     `json:"author"`
	CreatedAt   *time.Time              `json:"created_at"`
}

type BoardListResponse struct {
	ID       int              `json:"id"`
	Label    *gitlab.Label    `json:"label"`
	Position int              `json:"position"`
	Issues   []*IssueResponse `json:"issues"`
}

type BoardResponse struct {
	ID    int                  `json:"id"`
	Name  string               `json:"name"`
	Lists []*BoardListResponse `json:"lists"`
}

func GetProjectBoards(ginContext *gin.Context) {
	token := ginContext.MustGet("token").(*oauth2.Token)
	sessionID := ginContext.MustGet("session_id").(string)

	tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
		return auth.UpdateSession(ctx, sessionID, t)
	}

	gitlabClient, err := GetGitLabClient(ginContext, token, tokenSaver)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
		ginContext.Abort()
		return
	}

	projectID := ginContext.Param("id")

	// Check cache first
	ctx := ginContext.Request.Context()
	if cachedData, ok := database.GetCachedBoardResponse(ctx, projectID); ok {
		var cached []*BoardResponse
		if err := json.Unmarshal(cachedData, &cached); err == nil {
			ginContext.Header("X-Cache", "HIT")
			ginContext.JSON(http.StatusOK, gin.H{"boards": cached})
			return
		}
	}

	// 1. Fetch all boards
	boards, _, err := gitlabClient.Boards.ListIssueBoards(projectID, &gitlab.ListIssueBoardsOptions{})
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch boards: " + err.Error()})
		ginContext.Abort()
		return
	}

	// 2. Fetch all open issues for the project WITH PARALLEL PAGINATION
	// Fetch pages concurrently to reduce latency
	var allIssues []*gitlab.Issue
	perPage := 100
	
	// First, fetch page 1 to get total pages
	firstOpt := &gitlab.ListProjectIssuesOptions{
		State: gitlab.Ptr("opened"),
		ListOptions: gitlab.ListOptions{
			PerPage: int64(perPage),
			Page:    1,
		},
	}
	issues, resp, err := gitlabClient.Issues.ListProjectIssues(projectID, firstOpt)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch issues: " + err.Error()})
		ginContext.Abort()
		return
	}
	allIssues = append(allIssues, issues...)
	
	// Fetch remaining pages in parallel
	if resp.NextPage > 0 {
		g2, _ := errgroup.WithContext(ctx)
		g2.SetLimit(3) // Max 3 concurrent page fetches
		
		for page := resp.NextPage; page <= resp.TotalPages; page++ {
			page := page
			g2.Go(func() error {
				opt := &gitlab.ListProjectIssuesOptions{
					State: gitlab.Ptr("opened"),
					ListOptions: gitlab.ListOptions{
						PerPage: int64(perPage),
						Page:    int64(page),
					},
				}
				pageIssues, _, err := gitlabClient.Issues.ListProjectIssues(projectID, opt)
				if err != nil {
					return err
				}
				allIssues = append(allIssues, pageIssues...)
				return nil
			})
		}
		
		if err := g2.Wait(); err != nil {
			ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch issues: " + err.Error()})
			ginContext.Abort()
			return
		}
	}

	// 3. Fetch all labels WITH PAGINATION to get details (colors)
	var allLabels []*gitlab.Label
	labelPerPage := 100
	labelPage := 1
	for {
		labelOpts := &gitlab.ListLabelsOptions{
			ListOptions: gitlab.ListOptions{
				PerPage: int64(labelPerPage),
				Page:    int64(labelPage),
			},
		}
		labels, resp, err := gitlabClient.Labels.ListLabels(projectID, labelOpts)
		if err != nil {
			ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch labels: " + err.Error()})
			ginContext.Abort()
			return
		}
		allLabels = append(allLabels, labels...)
		if resp.NextPage == 0 {
			break
		}
		labelPage++
	}

	// Create a map for quick label lookup by name
	labelMap := make(map[string]*LabelResponse)
	for _, l := range allLabels {
		labelMap[l.Name] = &LabelResponse{
			ID:        int(l.ID),
			Name:      l.Name,
			Color:     l.Color,
			TextColor: l.TextColor,
		}
	}

	// 4. Fetch board lists IN PARALLEL using errgroup
	// Previous implementation fetched sequentially per board - O(n) API calls
	// Now we parallelize with semaphore limit to avoid overwhelming GitLab API
	type boardListResult struct {
		board   *gitlab.IssueBoard
		lists   []*gitlab.BoardList
		listErr error
	}

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(5) // Semaphore: max 5 concurrent board list fetches

	results := make([]*boardListResult, len(boards))

	for i, board := range boards {
		i, board := i, board
		g.Go(func() error {
			lists, _, err := gitlabClient.Boards.GetIssueBoardLists(projectID, board.ID, &gitlab.GetIssueBoardListsOptions{})
			results[i] = &boardListResult{
				board:   board,
				lists:   lists,
				listErr: err,
			}
			if err != nil {
				return err
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch board lists: " + err.Error()})
		ginContext.Abort()
		return
	}

	// OPTIMIZATION: Convert all issues ONCE before the board loop
	// Previous implementation converted the same issue multiple times (once per board)
	// This is O(boards × issues) - now it's O(issues)
	convertedIssues := make([]*IssueResponse, len(allIssues))
	for i, issue := range allIssues {
		issueLabels := make([]*LabelResponse, 0)
		for _, labelName := range issue.Labels {
			if l, ok := labelMap[labelName]; ok {
				issueLabels = append(issueLabels, l)
			} else {
				issueLabels = append(issueLabels, &LabelResponse{
					Name:  labelName,
					Color: "#808080",
				})
			}
		}
		convertedIssues[i] = &IssueResponse{
			ID:          int(issue.ID),
			IID:         int(issue.IID),
			Title:       issue.Title,
			Description: issue.Description,
			State:       issue.State,
			Labels:      issueLabels,
			Assignees:   issue.Assignees,
			Author:      issue.Author,
			CreatedAt:   issue.CreatedAt,
		}
	}

	var boardResponses []*BoardResponse

	for _, result := range results {
		board := result.board
		lists := result.lists

		listMap := make(map[int][]*IssueResponse)
		var openListIssues []*IssueResponse

		// Create a map of LabelID -> ListIndex for quick lookup
		labelToListId := make(map[string]int)

		var boardListResponses []*BoardListResponse

		openBoardList := &BoardListResponse{
			ID:       0,
			Label:    &gitlab.Label{Name: "Open", Color: "#808080"},
			Position: -1,
			Issues:   []*IssueResponse{},
		}

		for _, list := range lists {
			if list.Label != nil {
				labelToListId[list.Label.Name] = int(list.ID)
				listMap[int(list.ID)] = []*IssueResponse{}

				boardListResponses = append(boardListResponses, &BoardListResponse{
					ID:       int(list.ID),
					Label:    list.Label,
					Position: int(list.Position),
					Issues:   []*IssueResponse{},
				})
			}
		}

		// Use pre-converted issues instead of converting on-the-fly
		for i, issue := range allIssues {
			assigned := false
			for _, labelName := range issue.Labels {
				if listID, ok := labelToListId[labelName]; ok {
					listMap[listID] = append(listMap[listID], convertedIssues[i])
					assigned = true
				}
			}

			if !assigned {
				openListIssues = append(openListIssues, convertedIssues[i])
			}
		}

		openBoardList.Issues = openListIssues

		finalLists := []*BoardListResponse{openBoardList}

		for _, bl := range boardListResponses {
			bl.Issues = listMap[bl.ID]
			finalLists = append(finalLists, bl)
		}

		boardResponses = append(boardResponses, &BoardResponse{
			ID:    int(board.ID),
			Name:  board.Name,
			Lists: finalLists,
		})
	}

	// Cache the response asynchronously
	go func() {
		if data, err := json.Marshal(boardResponses); err == nil {
			database.SetCachedBoardResponse(context.Background(), projectID, data)
		}
	}()

	ginContext.JSON(http.StatusOK, gin.H{
		"boards": boardResponses,
	})
}

func GetProjectBranches(ginContext *gin.Context) {
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
	search := ginContext.Query("search")

	listOpts := &gitlab.ListBranchesOptions{}
	if search != "" {
		listOpts.Search = gitlab.Ptr(search)
	}

	branches, _, err := gitlabClient.Branches.ListBranches(projectID, listOpts)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		ginContext.Abort()
		return
	}

	ginContext.JSON(http.StatusOK, gin.H{
		"branches": branches,
	})
}