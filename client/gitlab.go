package client

import (
	"context"
	"os"

	gitlab "gitlab.com/gitlab-org/api/client-go"
	"gitlab.com/gitlab-org/api/client-go/gitlaboauth2"
	"golang.org/x/oauth2"
)

type TokenSaver func(context.Context, *oauth2.Token) error

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

func GetClient(ctx context.Context, token *oauth2.Token, saver TokenSaver) (*gitlab.Client, error) {
	clientID := os.Getenv("GITLAB_APPLICATION_ID")
	clientSecret := os.Getenv("GITLAB_SECRET")
	redirectURL := "http://localhost:3000/auth/callback"
	scopes := []string{"api", "read_user"}
	config := gitlaboauth2.NewOAuth2Config("", clientID, redirectURL, scopes)
	config.ClientSecret = clientSecret

	ts := &NotifyTokenSource{
		ctx:    ctx,
		source: config.TokenSource(ctx, token),
		saver:  saver,
	}

	client, err := gitlab.NewAuthSourceClient(gitlab.OAuthTokenSource{TokenSource: ts})
	if err != nil {
		return nil, err
	}

	return client, nil
}

// FetchLastActivityNote retrieves the most recent note (system or user) for an issue.
func FetchLastActivityNote(client *gitlab.Client, projectID int, issueID int) (*gitlab.Note, error) {
	orderBy := "created_at"
	sort := "desc"
	opt := &gitlab.ListIssueNotesOptions{
		OrderBy: &orderBy,
		Sort:    &sort,
		ListOptions: gitlab.ListOptions{
			Page:    1,
			PerPage: 1,
		},
	}

	notes, _, err := client.Notes.ListIssueNotes(projectID, int64(issueID), opt)
	if err != nil {
		return nil, err
	}

	if len(notes) == 0 {
		return nil, nil
	}

	return notes[0], nil
}
