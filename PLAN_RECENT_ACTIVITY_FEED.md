# Plan: Recent Personal Activity Feed Implementation

## 1. Goal
Enhance the `@routes/dashboard.go` endpoint (`GetDashboardStats`) to provide a rich "Activity Feed" for issues assigned to the current user. Instead of just listing issues, it will show *what* recently happened to them (e.g., "Alice changed label to 'Urgent'", "Bob mentioned this in MR !123").

## 2. Requirements
- **Scope**: Issues assigned to the authenticated user (`scope=assigned_to_me`).
- **Data**: Top 5 most recently updated issues.
- **Enrichment**: For each issue, fetch the latest significant "System Note" (activity log).
- **Performance**: Use concurrency to prevent linear API latency (N+1 problem).

## 3. Architecture & Data Structures

### 3.1. New Response Structure
We will introduce a simplified `Activity` struct to send to the frontend.

```go
type ActivityFeedItem struct {
    IssueID       int       `json:"issue_id"`
    IssueIID      int       `json:"issue_iid"`
    ProjectID     int       `json:"project_id"`
    Title         string    `json:"title"`
    WebURL        string    `json:"web_url"`
    ActionType    string    `json:"action_type"`    // e.g., "comment", "label_change", "state_change", "system"
    ActorName     string    `json:"actor_name"`
    ActorAvatar   string    `json:"actor_avatar"`
    Description   string    `json:"description"`    // Human readable: "changed label to 'Bug'"
    CreatedAt     time.Time `json:"created_at"`
}
```

### 3.2. Logic Flow
1.  **Fetch Recent Issues**: Call `gitlabClient.Issues.ListIssues` with `OrderBy=updated_at`, `Sort=desc`, `Scope=assigned_to_me`, `Limit=5`.
2.  **Concurrent Enrichment**:
    -   Create a `sync.WaitGroup`.
    -   For each issue, spawn a goroutine.
    -   **Fetch Notes**: Call `gitlabClient.Notes.ListIssueNotes` for that issue.
        -   We specifically look for the *latest* note.
        -   If it's a `System: true` note, parse the body (often contains "changed label...", "mentioned in...").
        -   If it's a regular comment, we might simply say "User X commented".
3.  **Aggregation**: Collect results, sort again by `CreatedAt` (just in case the latest note on the 2nd updated issue is actually newer than the latest note on the 1st, though `updated_at` on the issue usually correlates strongly).
4.  **Response**: Add to the JSON response map.

## 4. Implementation Steps

### Step 1: Update `routes/dashboard.go`
- [ ] Define the `ActivityFeedItem` struct inside `routes/` (or `models/` if reusable, but local is fine for now).
- [ ] Modify `GetDashboardStats` to initialize a channel or slice for activities.

### Step 2: Implement "Last Activity" Logic
- [ ] Create a helper function:
  ```go
  func fetchLastActivity(client *gitlab.Client, projectID int, issueID int) (*gitlab.Note, error)
  ```
- [ ] This function will:
  -   List notes sorted by `created_at` `desc`.
  -   Return the first one.

### Step 3: Concurrency
- [ ] Loop through the `recentIssues`.
- [ ] Launch Go routines to call `fetchLastActivity`.
- [ ] Use a `Mutex` to safely append to the results slice.
- [ ] Add error handling: if fetching notes fails for one issue, just show the issue creation/update generic message, don't fail the request.

### Step 4: Formatting
- [ ] Parse the `Note.Body`. System notes in GitLab are plain text like "changed title from **X** to **Y**". We can display this directly or sanitize it.
- [ ] Map `ActivityFeedItem` fields.

## 5. Risk Assessment
-   **Rate Limits**: 5 concurrent requests + 3 initial requests = 8 requests per dashboard load. This is within standard GitLab limits, but we should be mindful.
-   **Parsing**: System note bodies can be complex. We will treat them as "Description" strings for now without deep parsing.

## 6. Verification Plan
-   **Manual Test**: Run the backend, hit the dashboard endpoint.
-   **Check**: Ensure JSON contains `recent_activities` array populated with real data.
-   **Check**: Verify "Actor" matches the user who performed the action, not necessarily the assignee.
