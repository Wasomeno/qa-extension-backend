# Plan: Implement FetchLastActivityNote

## Overview
Implement the `FetchLastActivityNote` helper function in `client/gitlab.go`. This function is a prerequisite for the Recent Activity Feed feature. It abstracts the logic of fetching the most recent note (comment or system activity) for a specific issue.

## Current State Analysis
- **File**: `client/gitlab.go`
- **Context**: Currently contains `GetClient` and token management.
- **Requirement**: `FetchLastActivityNote` is needed to avoid code duplication in controllers.

## Implementation Approach
Add a public function `FetchLastActivityNote` to `client/gitlab.go`.

## Phase 1: Implementation
### Overview
Add the function to `client/gitlab.go`.

### Changes Required:
#### 1. `client/gitlab.go`
**Changes**: Add `FetchLastActivityNote` function.

```go
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

	notes, _, err := client.Notes.ListIssueNotes(projectID, issueID, opt)
	if err != nil {
		return nil, err
	}

	if len(notes) == 0 {
		return nil, nil
	}

	return notes[0], nil
}
```

### Success Criteria:
#### Automated:
- [ ] `go build ./client/...` passes.
#### Manual:
- [ ] Verify function signature matches requirements.

## Next Steps
- Once implemented, this function will be used by `routes/dashboard.go` (in a future task).
