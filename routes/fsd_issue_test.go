package routes

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"qa-extension-backend/client"
	"qa-extension-backend/internal/models"
	"qa-extension-backend/services"

	"github.com/gin-gonic/gin"
	gitlab "gitlab.com/gitlab-org/api/client-go"
	"golang.org/x/oauth2"
)

func TestPreviewFSDIssuesReadsSpecsRepoAndReturnsDrafts(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/22":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":             22,
				"default_branch": "develop",
			})
		case "/api/v4/projects/22/repository/files/docs%2Ffsd%2Fpayroll%2Emd":
			if got := r.URL.Query().Get("ref"); got != "develop" {
				t.Fatalf("expected default ref develop, got %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"file_path": "docs/fsd/payroll.md",
				"size":      18,
				"encoding":  "base64",
				"content":   base64.StdEncoding.EncodeToString([]byte("# Payroll FSD")),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockServer.Close()

	restore := overrideFSDIssueRouteDeps(t, mockServer.URL)
	defer restore()
	generateFSDIssueDrafts = func(ctx context.Context, sources []services.FSDIssueSource, actorID int, generateText services.FSDIssueLLMFunc) ([]services.FSDIssueDraft, error) {
		if len(sources) != 1 {
			t.Fatalf("expected one source, got %d", len(sources))
		}
		if sources[0].Path != "docs/fsd/payroll.md" || sources[0].Ref != "develop" || sources[0].Content != "# Payroll FSD" {
			t.Fatalf("unexpected source: %#v", sources[0])
		}
		return []services.FSDIssueDraft{{
			SourcePath:  sources[0].Path,
			Title:       "Implement payroll",
			Description: "## Summary\nBuild payroll.",
			Labels:      []string{"fsd", "feature"},
		}}, nil
	}

	router := fsdIssueTestRouter()
	body := bytes.NewBufferString(`{"fsds":[{"path":"docs/fsd/payroll.md"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/projects/app-1/fsd-issues/preview", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		IssueRepoID int64                    `json:"issueRepoId"`
		SpecsRepoID int64                    `json:"specsRepoId"`
		Issues      []services.FSDIssueDraft `json:"issues"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if resp.IssueRepoID != 11 || resp.SpecsRepoID != 22 {
		t.Fatalf("unexpected repo ids: %#v", resp)
	}
	if len(resp.Issues) != 1 || resp.Issues[0].Title != "Implement payroll" {
		t.Fatalf("unexpected issues: %#v", resp.Issues)
	}
}

func TestCreateFSDIssuesCreatesInIssueRepo(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var createdPath string
	var createdBody string
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v4/projects/11/issues" {
			http.NotFound(w, r)
			return
		}
		createdPath = r.URL.Path
		raw := new(bytes.Buffer)
		_, _ = raw.ReadFrom(r.Body)
		createdBody = raw.String()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":          1001,
			"iid":         7,
			"project_id":  11,
			"title":       "Implement payroll",
			"description": "## Summary\nBuild payroll.",
			"state":       "opened",
			"created_at":  time.Now().Format(time.RFC3339),
		})
	}))
	defer mockServer.Close()

	restore := overrideFSDIssueRouteDeps(t, mockServer.URL)
	defer restore()

	router := fsdIssueTestRouter()
	body := bytes.NewBufferString(`{"issues":[{"sourcePath":"docs/fsd/payroll.md","title":"Implement payroll","description":"## Summary\nBuild payroll.","labels":["fsd","feature"]}]}`)
	req := httptest.NewRequest(http.MethodPost, "/projects/app-1/fsd-issues", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d body=%s", w.Code, w.Body.String())
	}
	if createdPath != "/api/v4/projects/11/issues" {
		t.Fatalf("expected issue repo path, got %q", createdPath)
	}
	if !strings.Contains(createdBody, "Implement+payroll") && !strings.Contains(createdBody, "Implement payroll") {
		t.Fatalf("expected created body to contain title, got %q", createdBody)
	}

	var resp struct {
		CreatedCount int                    `json:"createdCount"`
		FailedCount  int                    `json:"failedCount"`
		Results      []fsdIssueCreateResult `json:"results"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if resp.CreatedCount != 1 || resp.FailedCount != 0 || len(resp.Results) != 1 || resp.Results[0].Status != "success" {
		t.Fatalf("unexpected response: %#v", resp)
	}
}

func overrideFSDIssueRouteDeps(t *testing.T, gitlabBaseURL string) func() {
	t.Helper()
	t.Setenv("GITLAB_BASE_URL", gitlabBaseURL)
	t.Setenv("GITLAB_APPLICATION_ID", "test-client")
	t.Setenv("GITLAB_SECRET", "test-secret")
	t.Setenv("GITLAB_REDIRECT_URI", "http://localhost/callback")

	originalGetProject := getAppProjectForFSDIssues
	originalGenerate := generateFSDIssueDrafts
	originalGetClient := GetGitLabClient
	originalGetCurrentUserID := getCurrentUserIDForFSDIssues

	getAppProjectForFSDIssues = func(ctx context.Context, id string) (*models.AppProject, error) {
		return &models.AppProject{
			ID:          id,
			Name:        "QA Project",
			IssueRepoID: 11,
			SpecsRepoID: 22,
		}, nil
	}
	getCurrentUserIDForFSDIssues = func(c *gin.Context) (int, error) {
		return 42, nil
	}
	GetGitLabClient = func(ctx context.Context, token *oauth2.Token, saver client.TokenSaver) (*gitlab.Client, error) {
		return gitlab.NewClient("mock-token", gitlab.WithBaseURL(gitlabBaseURL+"/api/v4/"))
	}

	return func() {
		getAppProjectForFSDIssues = originalGetProject
		generateFSDIssueDrafts = originalGenerate
		GetGitLabClient = originalGetClient
		getCurrentUserIDForFSDIssues = originalGetCurrentUserID
	}
}

func fsdIssueTestRouter() *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("token", &oauth2.Token{AccessToken: "mock-token"})
		c.Set("session_id", "mock-session")
		c.Next()
	})
	router.POST("/projects/:id/fsd-issues/preview", PreviewFSDIssues)
	router.POST("/projects/:id/fsd-issues", CreateFSDIssues)
	return router
}
