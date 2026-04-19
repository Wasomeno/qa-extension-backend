# Pi Runner Implementation

## Overview

This document describes the implementation of the Pi coding agent runner, which runs alongside the existing Claude Code runner, allowing per-request selection of which agent to use.

## Architecture

```
POST /agent/fix-issue
{
  "project_id": 123,
  "issue_iid": 456,
  "runner": "claude" | "pi"    <-- NEW FIELD (defaults to "claude")
}
        │
        ▼
routes/agent_fix.go
        │
        ├── runner="claude" → agent/claude_runner.go → Claude Code CLI
        │
        └── runner="pi"     → agent/pi_runner.go     → Pi (RPC mode)
```

## Execution Modes

### Local Mode (Default)
```
┌─────────────────────────────────────────────────────────────┐
│  Server (where qa-extension-backend runs)                   │
│                                                             │
│  ┌─────────────────┐        ┌─────────────────────────────┐│
│  │  Go Backend     │  RPC   │  Pi (installed locally)     ││
│  │  pi_runner.go   │───────►│  pi --mode rpc              ││
│  │                 │◄───────│  (stdin/stdout JSON)        ││
│  └─────────────────┘        └─────────────────────────────┘│
└─────────────────────────────────────────────────────────────┘
```

### Remote Mode (SSH with RPC)
```
┌────────────────────────┐           ┌────────────────────────┐
│  Go Backend Server     │    SSH    │  Remote Server         │
│                        │──────────►│  (FIX_SSH_HOST)        │
│  ┌──────────────────┐  │           │  ┌──────────────────┐  │
│  │  pi_runner.go    │  │   RPC     │  │  pi --mode rpc   │  │
│  │  ├─ SSH Client   │──┼──────────►│  │  (stdin/stdout)  │  │
│  │  └─ RPC Client   │◄─┼───────────│  │                  │  │
│  └──────────────────┘  │           │  └──────────────────┘  │
└────────────────────────┘           └────────────────────────┘
```

**Both modes use RPC protocol** for consistent behavior:
- Real-time streaming events
- Two-way communication
- Ability to send multiple commands
- Ability to interact during execution (abort, steer, etc.)

## Files Changed

### 1. `agent/pi_runner.go` (NEW)
- **~750 lines** - Complete Pi runner implementation
- Runs Pi in RPC mode for headless operation
- Supports both local and remote (SSH) execution modes
- Implements JSON-RPC protocol over stdin/stdout
- Handles:
  - GitLab issue fetching
  - Repository cloning and branching
  - Prompt building
  - Pi agent execution (RPC for both local and remote)
  - Change detection and commit/push
  - Merge request creation

### 2. `agent/ssh_client.go` (NEW)
- **~120 lines** - SSH client wrapper
- Proper SSH connection using `golang.org/x/crypto/ssh`
- Support for private key and password authentication
- Session management for RPC communication

### 3. `agent/claude_runner.go` (MODIFIED)
- Renamed `RunFixAgent` → `RunFixWithClaude`
- Added new `RunFixAgent` as router function
- No other changes to existing Claude runner logic

### 4. `routes/agent_fix.go` (MODIFIED)
- Added `runner` field to request payload
- Added validation for runner value ("claude" or "pi")
- Passes runner to `agent.RunFixAgent()`
- Updated response to include runner in message

## API Changes

### Request Payload

```json
{
  "project_id": 123,           // Required
  "issue_iid": 456,            // Required
  "repo_project_id": 789,      // Optional (defaults to project_id)
  "target_branch": "main",     // Optional (defaults to "main")
  "additional_context": "...", // Optional
  "runner": "pi"               // NEW: Optional (defaults to "claude")
}
```

### Response

```json
{
  "message": "pi fix agent started",
  "session_id": "fix_123_456_abc12345",
  "runner": "pi"
}
```

## Configuration

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `PI_BINARY_PATH` | Path to Pi binary | `"pi"` |
| `PI_MODEL` | Model to use | First available |
| `ANTHROPIC_API_KEY` | Anthropic API key | - |
| `OPENAI_API_KEY` | OpenAI API key | - |

### For Remote (SSH) Mode

| Variable | Description | Required |
|----------|-------------|----------|
| `FIX_SSH_HOST` | SSH host for remote execution | Yes |
| `FIX_SSH_USER` | SSH user | No (default: "root") |
| `FIX_SSH_PORT` | SSH port | No (default: "22") |
| `FIX_SSH_KEY_PATH` | Path to SSH private key | No (default: ~/.ssh/id_rsa) |
| `FIX_SSH_PASSWORD` | SSH password (if not using key) | No |

## RPC Protocol

The Pi runner communicates with Pi using JSON-RPC over stdin/stdout (both local and remote):

### Commands

```json
{"id": "req-123", "type": "prompt", "message": "Fix issue..."}
{"id": "req-124", "type": "get_last_assistant_text"}
```

### Responses

```json
{"id": "req-123", "type": "response", "success": true}
{"id": "req-124", "type": "response", "success": true, "data": {"text": "..."}}
```

### Events

Events are streamed as they occur and forwarded to the FixEvent channel for SSE streaming.

## System Prompt

The Pi runner uses a custom system prompt (`PiFixSystemPrompt`) that instructs the agent to:
1. Understand the issue
2. Explore the codebase first
3. Make targeted, minimal changes
4. Verify the fix
5. Never run git commands (handled by wrapper)

## Testing

### Manual Testing

1. **With Claude (default):**
```bash
curl -X POST http://localhost:8080/agent/fix-issue \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"project_id": 123, "issue_iid": 456}'
```

2. **With Pi (local):**
```bash
curl -X POST http://localhost:8080/agent/fix-issue \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"project_id": 123, "issue_iid": 456, "runner": "pi"}'
```

3. **With Pi (remote SSH):**
```bash
# Set SSH configuration
export FIX_SSH_HOST=192.168.1.100
export FIX_SSH_USER=root
export FIX_SSH_KEY_PATH=~/.ssh/id_rsa

# Make request
curl -X POST http://localhost:8080/agent/fix-issue \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"project_id": 123, "issue_iid": 456, "runner": "pi"}'
```

### Prerequisites for Pi

1. Install Pi:
```bash
npm install -g @mariozechner/pi-coding-agent
```

2. Set up API keys:
```bash
export ANTHROPIC_API_KEY=sk-ant-...
# or
export OPENAI_API_KEY=sk-...
```

3. For remote mode, ensure Pi is installed on the remote server.

## Error Handling

- Invalid runner value returns HTTP 400
- Missing Pi binary returns error in agent stage
- Pi execution failures are captured and reported
- No changes detected returns specific error
- Only config changes returns specific error
- SSH connection failures are reported with details

## Rollback

To disable Pi runner:
1. Simply use `"runner": "claude"` in requests
2. Or omit the runner field (defaults to Claude)

## Comparison: Local vs Remote

| Aspect | Local Mode | Remote Mode |
|--------|-----------|-------------|
| Pi Location | Same server as Go backend | Different server |
| Connection | Direct stdin/stdout | SSH session pipes |
| Protocol | RPC | RPC (same) |
| Resource Usage | On backend server | On remote server |
| SSH Required | No | Yes |
| Prerequisites | Pi installed locally | Pi installed on remote |

## Future Improvements

1. **Session Management**: Store Pi sessions for resumption
2. **Model Selection**: Add `pi_model` field for per-request model
3. **Health Check**: Endpoint to verify Pi installation and configuration
4. **Timeout Configuration**: Per-request timeout settings
