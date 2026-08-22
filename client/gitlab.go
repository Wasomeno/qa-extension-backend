package client

import (
	"context"

	"qa-extension-backend/config"

	gitlab "gitlab.com/gitlab-org/api/client-go"
	"gitlab.com/gitlab-org/api/client-go/gitlaboauth2"
	"golang.org/x/oauth2"
)

// TokenSaver persists refreshed OAuth tokens (per-request).
type TokenSaver func(context.Context, *oauth2.Token) error

// NotifyTokenSource wraps an oauth2.TokenSource so each refresh is written
// back through the per-request TokenSaver (session store).
type NotifyTokenSource struct {
	ctx    context.Context
	source oauth2.TokenSource
	saver  TokenSaver
}

func (s *NotifyTokenSource) Token() (*oauth2.Token, error) {
	t, err := s.source.Token()
	if err != nil {
		return nil, err
	}
	if s.saver != nil {
		_ = s.saver(s.ctx, t)
	}
	return t, nil
}

// GetClient builds an authenticated *gitlab.Client. The optional saver is
// invoked every time the underlying token is refreshed, which is how the
// session store stays in sync with the GitLab OAuth state.
func GetClient(ctx context.Context, token *oauth2.Token, saver TokenSaver) (*gitlab.Client, error) {
	baseURL := config.GetEnv("GITLAB_BASE_URL")
	clientID := config.GetEnv("GITLAB_APPLICATION_ID")
	clientSecret := config.GetEnv("GITLAB_SECRET")
	redirectURL := config.GetEnv("GITLAB_REDIRECT_URI")
	scopes := []string{"api", "read_user"}
	configMap := gitlaboauth2.NewOAuth2Config(baseURL, clientID, redirectURL, scopes)
	configMap.ClientSecret = clientSecret

	ts := &NotifyTokenSource{
		ctx:    ctx,
		source: configMap.TokenSource(ctx, token),
		saver:  saver,
	}

	var options []gitlab.ClientOptionFunc
	if baseURL != "" {
		apiURL := baseURL
		if !endsWith(apiURL, "/api/v4") && !endsWith(apiURL, "/api/v4/") {
			apiURL = trimRight(apiURL, "/") + "/api/v4/"
		}
		options = append(options, gitlab.WithBaseURL(apiURL))
	}

	client, err := gitlab.NewAuthSourceClient(gitlab.OAuthTokenSource{TokenSource: ts}, options...)
	if err != nil {
		return nil, err
	}

	return client, nil
}

// FetchRecentIssueNotes retrieves the last N notes for an issue.
func FetchRecentIssueNotes(client *gitlab.Client, projectID int, issueID int, limit int) ([]*gitlab.Note, error) {
	orderBy := "created_at"
	sort := "desc"
	opt := &gitlab.ListIssueNotesOptions{
		OrderBy: &orderBy,
		Sort:    &sort,
		ListOptions: gitlab.ListOptions{
			Page:    1,
			PerPage: int64(limit),
		},
	}

	notes, _, err := client.Notes.ListIssueNotes(projectID, int64(issueID), opt)
	if err != nil {
		return nil, err
	}

	return notes, nil
}

func endsWith(s, suffix string) bool {
	if len(s) < len(suffix) {
		return false
	}
	return s[len(s)-len(suffix):] == suffix
}

func trimRight(s, cut string) string {
	if len(s) < len(cut) {
		return s
	}
	if s[len(s)-len(cut):] == cut {
		return s[:len(s)-len(cut)]
	}
	return s
}
