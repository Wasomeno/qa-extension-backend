package routes

import (
	"context"
	"fmt"
	"net/http"
	"qa-extension-backend/client"
	authHandler "qa-extension-backend/handlers"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	gitlab "gitlab.com/gitlab-org/api/client-go"
	"golang.org/x/oauth2"
)

func GetProjects(ginContext *gin.Context) {
	token := ginContext.MustGet("token").(*oauth2.Token)
	sessionID := ginContext.MustGet("session_id").(string)

	fmt.Printf("Token: ...%s\n", token.AccessToken[len(token.AccessToken)-5:])

	tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
		return authHandler.UpdateSession(ctx, sessionID, t)
	}

	gitlabClient, err := client.GetClient(ginContext, token, tokenSaver)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
		ginContext.Abort()
		return
	}

	_ = godotenv.Load() // Ignoring error as it might be already loaded or optional

	user, _, err := gitlabClient.Users.CurrentUser()
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		ginContext.Abort()
		return
	}

	projects, _, err := gitlabClient.Projects.ListUserProjects(user.ID, &gitlab.ListProjectsOptions{})
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
		return authHandler.UpdateSession(ctx, sessionID, t)
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
		return authHandler.UpdateSession(ctx, sessionID, t)
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
		return authHandler.UpdateSession(ctx, sessionID, t)
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
		return authHandler.UpdateSession(ctx, sessionID, t)
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
		return authHandler.UpdateSession(ctx, sessionID, t)
	}

	gitlabClient, err := GetGitLabClient(ginContext, token, tokenSaver)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
		ginContext.Abort()
		return
	}

	projectID := ginContext.Param("id")

	// 1. Fetch all boards
	boards, _, err := gitlabClient.Boards.ListIssueBoards(projectID, &gitlab.ListIssueBoardsOptions{})
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch boards: " + err.Error()})
		ginContext.Abort()
		return
	}
	fmt.Printf("DEBUG: Fetched %d boards\n", len(boards))

	// 2. Fetch all open issues for the project
	opt := &gitlab.ListProjectIssuesOptions{
		State: gitlab.Ptr("opened"),
	}
	allIssues, _, err := gitlabClient.Issues.ListProjectIssues(projectID, opt)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch issues: " + err.Error()})
		ginContext.Abort()
		return
	}
	fmt.Printf("DEBUG: Fetched %d issues\n", len(allIssues))
	if len(allIssues) > 0 {
		fmt.Printf("DEBUG: First issue ID: %d, Title: %s, Labels: %v\n", allIssues[0].ID, allIssues[0].Title, allIssues[0].Labels)
	}

	// 3. Fetch all labels to get details (colors)
	// We need to fetch all labels to map names to colors
	// Pagination might initially limit this to 20/100, assuming we set a high per_page or loop.
	// For this iteration, we'll request a large page size.
	labelOpts := &gitlab.ListLabelsOptions{
		ListOptions: gitlab.ListOptions{
			PerPage: 100,
		},
	}
	allLabels, _, err := gitlabClient.Labels.ListLabels(projectID, labelOpts)
	if err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch labels: " + err.Error()})
		ginContext.Abort()
		return
	}
	fmt.Printf("DEBUG: Fetched %d labels\n", len(allLabels))

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

	var boardResponses []*BoardResponse

	for _, board := range boards {
		// 4. Fetch lists for each board
		lists, _, err := gitlabClient.Boards.GetIssueBoardLists(projectID, board.ID, &gitlab.GetIssueBoardListsOptions{})
		if err != nil {
			ginContext.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to fetch lists for board %d: %s", board.ID, err.Error())})
			ginContext.Abort()
			return
		}

		listMap := make(map[int][]*IssueResponse)
		var openListIssues []*IssueResponse

		// Helper to convert gitlab.Issue to IssueResponse
		toResponse := func(i *gitlab.Issue) *IssueResponse {
			issueLabels := make([]*LabelResponse, 0)
			for _, labelName := range i.Labels {
				if l, ok := labelMap[labelName]; ok {
					issueLabels = append(issueLabels, l)
				} else {
					// Fallback if label details not found (e.g. pagination or deleted)
					issueLabels = append(issueLabels, &LabelResponse{
						Name:  labelName,
						Color: "#808080", // Default gray
					})
				}
			}

			return &IssueResponse{
				ID:          int(i.ID),
				IID:         int(i.IID),
				Title:       i.Title,
				Description: i.Description,
				State:       i.State,
				Labels:      issueLabels,
				Assignees:   i.Assignees,
				Author:      i.Author,
				CreatedAt:   i.CreatedAt,
			}
		}

		// Create a map of LabelID -> ListIndex for quick lookup
		labelToListId := make(map[string]int) // Label Name -> List ID
		
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

		for _, issue := range allIssues {
			assigned := false
			for _, labelName := range issue.Labels {
				if listID, ok := labelToListId[labelName]; ok {
					listMap[listID] = append(listMap[listID], toResponse(issue))
					assigned = true
				}
			}
			
			if !assigned {
				openListIssues = append(openListIssues, toResponse(issue))
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

	ginContext.JSON(http.StatusOK, gin.H{
		"boards": boardResponses,
	})
}