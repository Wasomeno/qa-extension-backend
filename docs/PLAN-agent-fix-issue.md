# Plan: Agent Fix Issue — Backend (qa-extension-backend)

## Overview

Implement a "Fix with Agent" feature that allows users to click a button on an issue card, and an AI coding agent (Claude Code) will fix the issue in the project repository and create a GitLab Merge Request — matching the Spotify Honk pattern.

**Architecture:**

```
User clicks "Fix with Agent"
        │
        ▼
POST /agent/fix-issue {project_id, issue_iid}
        │
        ▼
┌──────────────────────────────────────────────────────┐
│  Go orchestrator (claude_runner.go)                   │
│                                                       │
│  1. GitLab API → fetch issue description              │
│  2. git clone repo → /tmp/qa-fix-<uuid>/              │
│  3. git checkout -b fix/issue-<iid>                   │
│  4. Spawn Claude Code CLI in --print mode              │
│     with system prompt: "fix this issue"              │
│  5. Claude edits files locally in the checkout         │
│  6. Stop hook fires → signals Go to proceed           │
│  7. Go: git add + git commit + git push               │
│  8. GitLab API: create MR                             │
│  9. Stream SSE events to frontend                     │
│  10. Cleanup temp dir                                 │
└──────────────────────────────────────────────────────┘
```

---

## Component Inventory

### 1. GitLab Write Tools (`agent/tools_gitlab_write.go` — NEW FILE)

Add three new GitLab write tools to `agent/tools_gitlab.go` or a new file.

#### `createBranch(projectID, branchName, refBranch)` → GitLab API
- Endpoint: `POST /projects/:id/repository/branches`
- Body: `{"branch": branchName, "ref": refBranch}`
- Returns branch info

#### `commitFiles(projectID, branchName, actions[])` → GitLab API
- Endpoint: `POST /projects/:id/repository/commits`
- Actions: `[{action: "create"|"update", file_path, content}]`
- This replaces the need for Claude to run `git commit` — Go does it via API instead
- Alternative: use `Bash(git add && git commit)` locally, then `git push`

#### `createMergeRequest(projectID, sourceBranch, title, description)` → GitLab API
- Endpoint: `POST /merge_requests`
- Body: `{source_branch, target_branch: "develop", title, description, remove_source_branch: true}`
- Returns MR URL and IID

#### `getFileContent(projectID, path, branch)` → GitLab API
- Check if this already exists in `tools_gitlab.go` — if yes, skip

**Implementation note:** For v1, prefer using local `git add && git commit && git push` from the temp checkout rather than GitLab API commits. This is simpler and more reliable for multi-file changes.

---

### 2. Claude Runner Orchestrator (`agent/claude_runner.go` — NEW FILE)

The core orchestrator. Main responsibilities:

#### `RunFixAgent(ctx, projectID int, issueIID int) (*FixResult, <-chan FixEvent, error)`

**Input:**
- `projectID` — GitLab project ID
- `issueIID` — GitLab issue IID (not internal ID)
- Auth token from context (GitLab OAuth token)

**Process:**

1. **Fetch issue from GitLab**
   - Call `GET /projects/:id/issues/:issue_iid` to get title + description
   - Publish `stage: "fetching_issue"` event

2. **Prepare temp checkout directory**
   - Create `/tmp/qa-fix-<uuid>/`
   - `git clone <repo_url>` into that directory
   - Configure git user: `user.email`, `user.name` from config or "qa-agent@spotify.dev"
   - Publish `stage: "cloning_repo"` event

3. **Create fix branch**
   - `git checkout -b fix/issue-<iid>`
   - Publish `stage: "creating_branch"` event

4. **Spawn Claude Code subprocess**

   ```bash
   claude --print \
     --session-id <uuid> \
     --output-format stream-json \
     --include-hook-events \
     --permission-mode acceptEdits \
     --no-session-persistence \
     --add-dir /tmp/qa-fix-<uuid> \
     --system-prompt "<fix system prompt>" \
     "Fix issue: <issue title>\n\n<issue description>"
   ```

   - Use `exec.CommandContext(ctx, "claude", ...)` — if ctx is cancelled, process is killed
   - Capture stdout (stream-json output) and parse FixEvent structs
   - Publish each event to Redis pub/sub for SSE streaming
   - Publish `stage: "agent_running"` event

5. **Monitor for Stop hook signal**
   - The Stop hook is configured separately (see `claude_hooks.go`)
   - The hook writes a marker file (e.g., `/tmp/qa-fix-<uuid>/.claude-stop-done`)
   - Alternatively: parse the stream-json for hook events with type "Stop"
   - When stop is detected, proceed to step 6

6. **Git push + create MR**

   ```bash
   cd /tmp/qa-fix-<uuid>
   git add -A
   git commit -m "fix: resolve issue #<iid>"
   git push -u origin fix/issue-<iid>
   ```

   - Publish `stage: "pushing_changes"` event
   - GitLab API: `POST /merge_requests` with:
     - `source_branch`: `fix/issue-<iid>`
     - `target_branch`: `develop`
     - `title`: `Fix: <issue title> (#<iid>)`
     - `description`: `Closes #<iid>. Fixed by AI agent.`
   - Publish `stage: "creating_mr"` event

7. **Cleanup**
   - Remove `/tmp/qa-fix-<uuid>/`
   - Publish `stage: "done", mr_url: "..."`

**FixResult struct:**
```go
type FixResult struct {
    Success     bool
    MRAuthor    string
    MRURL       string
    MRIID       int
    CommitSHA   string
    Error       string
}
```

**FixEvent struct (published to SSE):**
```go
type FixEvent struct {
    Stage       string  // "fetching_issue" | "cloning_repo" | "creating_branch" | "agent_running" | "pushing_changes" | "creating_mr" | "done" | "error"
    Message     string  // human-readable status
    MRURL       string  // only set when stage == "done"
    Error       string  // only set when stage == "error"
    LogLine     string  // raw log line from Claude Code stdout
    Timestamp   string  // RFC3339
}
```

**Error handling:**
- If any step fails, publish `stage: "error"` with the error message
- Cleanup temp dir on error (defer)
- Return early

---

### 3. Fix System Prompt (`agent/system_prompt_fix.go` — NEW FILE)

Defines the system prompt passed to Claude Code.

**Prompt content:**
```
You are a software engineer fixing a GitLab issue. Your task:

1. Read and understand the issue description below carefully.
2. Explore the codebase to find the relevant files.
3. Make the minimal necessary changes to fix the issue.
4. After making changes, verify your fix is correct by:
   - Running relevant tests if they exist (npm test, go test, etc.)
   - Checking that your changes compile/build without errors
5. Do NOT run git push or git commit — the wrapper will handle that.
6. Do NOT create new files outside the scope of the fix.
7. If you cannot fix the issue, describe what you attempted and why it failed.

Focus on the issue. Do not refactor unrelated code.
```

**Key constraints enforced:**
- "Do NOT run git push" — prevents auto-push
- "Run tests / check compilation" — basic self-verification
- "Minimal changes" — stay in scope

---

### 4. Claude Hooks Configuration (`agent/claude_hooks.go` — NEW FILE)

Manages Claude Code's per-session hooks configuration.

#### `PrepareSessionHook(sessionDir string) error`
- Creates a `.claude/settings.json` inside the temp checkout directory
- Configures the `Stop` hook to write a marker file:

```json
{
  "hooks": {
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "touch <sessionDir>/.claude-stop-done"
          }
        ]
      }
    ]
  }
}
```

- This way, when Claude Code finishes, it creates a marker file
- The Go runner polls for this file (or uses inotify/FSEvents) to know when to proceed

#### `WaitForStopSignal(sessionDir string, timeout time.Duration) error`
- Polls every 500ms for `<sessionDir>/.claude-stop-done` file
- Returns error if timeout exceeded (e.g., 10 minutes)
- Also checks for `.claude-stop-error` file for error cases

#### `CleanupSessionHook(sessionDir string) error`
- Removes the `.claude/settings.json` or resets hooks

---

### 5. HTTP Route (`routes/agent_fix.go` — NEW FILE)

New route handler for the fix feature.

#### `POST /agent/fix-issue`

**Request body:**
```json
{
  "project_id": 123,        // Project where the issue exists (bug tracker)
  "issue_iid": 45,          // Issue IID in the issue project
  "repo_project_id": 456,   // Optional: Project containing the code to fix (defaults to project_id)
  "target_branch": "main"   // Optional: Target branch for MR (defaults to "main")
}
```

**Examples:**

Same project (issue and code in one repo):
```json
{
  "project_id": 123,
  "issue_iid": 45
}
```

Different projects (issue in bug tracker, code in app repo):
```json
{
  "project_id": 100,        // Bug tracker project
  "issue_iid": 45,
  "repo_project_id": 200    // Application repo to fix
}
```

**Response:** Server-Sent Events (SSE) stream

**SSE event format:**
```
event: fix_event
data: {"stage": "fetching_issue", "message": "Fetching issue from GitLab...", "timestamp": "2025-12-01T10:00:00Z"}

event: fix_event
data: {"stage": "agent_running", "message": "Claude Code is fixing the issue...", "timestamp": "2025-12-01T10:00:05Z"}

event: fix_event
data: {"stage": "done", "message": "Fix complete!", "mr_url": "https://gitlab.com/project/-/merge_requests/89", "timestamp": "2025-12-01T10:02:30Z"}
```

**Implementation:**

```go
func FixIssueWithAgent(c *gin.Context) {
    var req struct {
        ProjectID int `json:"project_id"`
        IssueIID  int `json:"issue_iid"`
    }
    if err := c.BindJSON(&req); err != nil {
        c.JSON(400, gin.H{"error": err.Error()})
        return
    }

    token := c.MustGet("token").(*oauth2.Token)
    ctx := context.WithValue(c.Request.Context(), "token", token)

    // Set SSE headers
    c.Header("Content-Type", "text/event-stream")
    c.Header("Cache-Control", "no-cache")
    c.Header("Connection", "keep-alive")

    // Start the fix agent in a goroutine
    eventCh := make(chan agent.FixEvent)

    // Stream events to SSE
    // ... (similar pattern to ChatWithAgent)
}
```

**Note:** This route uses a different event channel than the existing `ChatWithAgent`. Register it under the existing agent router or as a new sub-router.

---

### 6. Route Registration (`routes/agent.go` — MODIFY)

Add the new route to the existing agent routes.

In `routes/agent.go` or wherever routes are registered:

```go
agentRouter.POST("/fix-issue", FixIssueWithAgent)
```

---

### 7. Optional: GitLab API File Update Tool (`agent/tools_gitlab.go` — ADD if missing)

If not already present, add `getFileContent` and `searchGitLabCode` to help the agent understand the codebase before making changes. (Already exists based on earlier grep results — confirm.)

---

## File Summary

| File | Action | Description |
|------|--------|-------------|
| `agent/claude_runner.go` | **NEW** | Core orchestrator: git clone, spawn Claude, handle stop hook, push, create MR |
| `agent/system_prompt_fix.go` | **NEW** | System prompt for the fix agent |
| `agent/claude_hooks.go` | **NEW** | Per-session Claude Code hooks configuration |
| `routes/agent_fix.go` | **NEW** | `POST /agent/fix-issue` SSE route |
| `agent/tools_gitlab_write.go` | **NEW** | GitLab write tools (branch, commit, MR) |
| `routes/agent.go` | **MODIFY** | Register `/fix-issue` route |
| `agent/tools_gitlab.go` | **CHECK** | Verify write tools don't already exist |
| `database/database.go` | **CHECK** | Verify Redis pub/sub works for new event types |

---

## Event Stages (for SSE)

| Stage | Description |
|-------|-------------|
| `fetching_issue` | Fetching issue from GitLab API |
| `cloning_repo` | Cloning repository to temp directory |
| `creating_branch` | Creating fix branch |
| `agent_running` | Claude Code is actively editing files |
| `pushing_changes` | Committing and pushing changes |
| `creating_mr` | Creating GitLab Merge Request |
| `done` | Complete — `mr_url` field populated |
| `error` | Failed — `error` field populated |

---

## Configuration / Environment Variables

No new environment variables needed — reusing existing:
- `GITLAB_BASE_URL` — GitLab instance URL
- `GITLAB_APPLICATION_ID` / `GITLAB_SECRET` — OAuth app
- Existing GitLab OAuth token (from session)

---

## Dependency Checklist

- [x] Node.js (`node --version` → v25.8.1)
- [x] Claude Code (`claude --version` → 2.1.97)
- [x] Git available on server (`git --version`)
- [x] Redis client (already in use)
- [x] GitLab OAuth client (already in use)

---

## Error Handling Strategy

1. **Git clone fails** → retry once, then return error with stage `error`
2. **Claude Code crashes** → capture stderr, return error
3. **Claude Code timeout** (10 min) → kill process, cleanup, return error
4. **Git push fails** (e.g., branch exists) → force push or return error
5. **MR creation fails** → branch already pushed, return MR URL from push output
6. **Temp dir cleanup fails** → log warning, don't fail the whole operation

---

## Implementation Order

1. **First:** Add GitLab write tools (`createBranch`, `createMergeRequest`) — verify they work
2. **Second:** Write `system_prompt_fix.go` and `claude_hooks.go` — test Claude Code spawning manually
3. **Third:** Write `claude_runner.go` — the core orchestrator, test end-to-end
4. **Fourth:** Add `routes/agent_fix.go` — wire up SSE
5. **Fifth:** Register route and test with frontend

---

## Testing Strategy

- Manual test: `cd /tmp && git clone <repo> && cd <repo> && claude --print --permission-mode acceptEdits "fix: ..."` to verify Claude works in non-interactive mode
- Test stop hook: verify `.claude-stop-done` file appears when Claude finishes
- Test the full flow: `curl -X POST http://localhost:<port>/agent/fix-issue -d '{"project_id":123,"issue_iid":45}'`
- Verify MR is created and link is returned in SSE stream

---

## Security Considerations

1. **Token scope** — Ensure GitLab OAuth token has `api` scope (already configured per `client/gitlab.go`)
2. **Temp dir isolation** — Each run uses a unique UUID-based temp dir
3. **No git push from Claude** — Enforced via system prompt, not Claude's own permissions
4. **Branch naming** — Use `fix/issue-<iid>` pattern to avoid collisions
5. **Cleanup** — Always defer cleanup of temp dir even on error
6. **Timeout** — Max 10 minutes per fix attempt to prevent runaway processes
