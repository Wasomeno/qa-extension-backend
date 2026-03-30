// ============================================
// Commands Module API - TypeScript Types
// ============================================

export interface CustomCommand {
  id: string;
  userId: string;
  name: string;           // stored with "/" prefix, e.g. "/projects"
  description: string;
  toolName: string;
  promptTemplate: string;
}

export interface CreateCustomCommandRequest {
  name: string;           // without "/", e.g. "projects" → "/projects"
  description?: string;
  tool_name: string;       // required
  prompt_template: string; // required
}

export interface ListCustomCommandsResponse {
  commands: CustomCommand[];
}

export interface DeleteCustomCommandResponse {
  message: string;
}

// ============================================
// Available Tools for Custom Commands
// ============================================

export const AVAILABLE_TOOLS = [
  'listGitLabProjects',
  'listAllGitLabIssues',
  'createGitLabIssue',
  'listGitLabIssues',
  'updateGitLabIssue',
  'listRecordedTests',
  'runRecordedTest',
  'listTestScenarios',
  'runTestScenario',
  'runScenarioTestCase',
] as const;

export type AvailableTool = typeof AVAILABLE_TOOLS[number];

// ============================================
// API Functions
// ============================================

const API_BASE = '/api/agent';

export async function listCustomCommands(
  fetchFn: typeof fetch = fetch
): Promise<CustomCommand[]> {
  const res = await fetchFn(`${API_BASE}/commands`, {
    method: 'GET',
    credentials: 'include',
  });
  
  if (!res.ok) {
    throw new Error(`Failed to list commands: ${res.statusText}`);
  }
  
  const data: ListCustomCommandsResponse = await res.json();
  return data.commands;
}

export async function createCustomCommand(
  request: CreateCustomCommandRequest,
  fetchFn: typeof fetch = fetch
): Promise<CustomCommand> {
  const res = await fetchFn(`${API_BASE}/commands`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    credentials: 'include',
    body: JSON.stringify(request),
  });
  
  if (!res.ok) {
    const error = await res.json().catch(() => ({ error: 'Unknown error' }));
    throw new Error(error.error || `Failed to create command: ${res.statusText}`);
  }
  
  return res.json();
}

export async function deleteCustomCommand(
  commandId: string,
  fetchFn: typeof fetch = fetch
): Promise<void> {
  const res = await fetchFn(`${API_BASE}/commands/${commandId}`, {
    method: 'DELETE',
    credentials: 'include',
  });
  
  if (!res.ok) {
    throw new Error(`Failed to delete command: ${res.statusText}`);
  }
}

// ============================================
// Endpoint Summary
// ============================================

/*
| Method | Endpoint                    | Request Body              | Response            |
|--------|-----------------------------|---------------------------|----------------------|
| GET    | /api/agent/commands          | -                         | { commands: [] }     |
| POST   | /api/agent/commands          | CreateCustomCommandRequest| CustomCommand        |
| DELETE | /api/agent/commands/:id      | -                         | { message: string }  |

// Example usage:
// const commands = await listCustomCommands();
// const newCmd = await createCustomCommand({ name: 'projects', tool_name: 'listGitLabProjects', prompt_template: '{{input}}' });
// await deleteCustomCommand('uuid-here');
*/
