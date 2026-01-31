package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"qa-extension-backend/client"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	gitlab "gitlab.com/gitlab-org/api/client-go"
	"golang.org/x/oauth2"
)

func TestGetProjectBoards(t *testing.T) {
	// Setup Gin
	gin.SetMode(gin.TestMode)

	// Mock data
	expectedBoards := []*gitlab.IssueBoard{
		{
			ID:   1,
			Name: "Development",
		},
	}
	
	expectedLists := []*gitlab.BoardList{
		{
			ID:       10,
			Position: 0,
			Label: &gitlab.Label{
				ID:   100,
				Name: "Doing",
			},
		},
	}

	expectedIssues := []*gitlab.Issue{
		{
			ID:          1000,
			IID:         1,
			Title:       "Test Issue",
			State:       "opened",
			Labels:      []string{"Doing"},
		},
		{
			ID:          1001,
			IID:         2,
			Title:       "Backlog Issue",
			State:       "opened",
			Labels:      []string{},
		},
	}

	expectedLabels := []*gitlab.Label{
		{
			ID:    100,
			Name:  "Doing",
			Color: "#FF0000",
		},
	}

	// Mock GitLab Client
	mockGetClient := func(ctx context.Context, token *oauth2.Token, saver client.TokenSaver) (*gitlab.Client, error) {
		// Create a mock server to handle GitLab API requests
		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			
			if r.URL.Path == "/api/v4/projects/1/boards" {
				json.NewEncoder(w).Encode(expectedBoards)
				return
			}
			if r.URL.Path == "/api/v4/projects/1/issues" {
				json.NewEncoder(w).Encode(expectedIssues)
				return
			}
			if r.URL.Path == "/api/v4/projects/1/boards/1/lists" {
				json.NewEncoder(w).Encode(expectedLists)
				return
			}
			if r.URL.Path == "/api/v4/projects/1/labels" {
				json.NewEncoder(w).Encode(expectedLabels)
				return
			}
			
			http.Error(w, "Not Found", http.StatusNotFound)
		}))
		
		client, err := gitlab.NewClient("mock-token", gitlab.WithBaseURL(mockServer.URL))
		if err != nil {
			return nil, err
		}
		return client, nil
	}

	// Save original factory and restore after test
	originalGetClient := GetGitLabClient
	defer func() { GetGitLabClient = originalGetClient }()
	GetGitLabClient = mockGetClient

	// Setup Router
	r := gin.Default()
	r.Use(func(c *gin.Context) {
		c.Set("token", &oauth2.Token{AccessToken: "mock-token"})
		c.Set("session_id", "mock-session-id")
		c.Next()
	})
	r.GET("/projects/:id/boards", GetProjectBoards)

	// Perform Request
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/projects/1/boards", nil)
	r.ServeHTTP(w, req)

	// Assertions
	assert.Equal(t, http.StatusOK, w.Code)

	var result struct {
		Boards []BoardResponse `json:"boards"`
	}
	err := json.Unmarshal(w.Body.Bytes(), &result)
	assert.NoError(t, err)
	
	assert.NotEmpty(t, result.Boards)
	board := result.Boards[0]
	assert.Equal(t, "Development", board.Name)
	
	// Check Lists (Open + Doing)
	assert.Len(t, board.Lists, 2)
	
	// Open List (First) containing Backlog Issue
	openList := board.Lists[0]
	assert.Equal(t, "Open", openList.Label.Name)
	assert.NotEmpty(t, openList.Issues)
	assert.Equal(t, "Backlog Issue", openList.Issues[0].Title)

	// Doing List (Second) containing Test Issue
	doingList := board.Lists[1]
	assert.Equal(t, "Doing", doingList.Label.Name)
	assert.NotEmpty(t, doingList.Issues)
	assert.Equal(t, "Test Issue", doingList.Issues[0].Title)
	assert.Equal(t, "#FF0000", doingList.Issues[0].Labels[0].Color)
}
