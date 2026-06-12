package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	openai "github.com/openai/openai-go"
	"github.com/openai/openai-go/shared"
)

// AgentTool is an ADK-free function tool exposed to the OpenAI chat model.
type AgentTool struct {
	Name        string
	Description string
	Schema      map[string]any
	Execute     func(ctx context.Context, raw json.RawMessage) (any, error)
}

// ToolRegistry stores the function tools available to the OpenAI agent.
type ToolRegistry struct {
	tools map[string]AgentTool
	order []string
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{tools: make(map[string]AgentTool)}
}

func (r *ToolRegistry) Register(tool AgentTool) {
	if _, exists := r.tools[tool.Name]; !exists {
		r.order = append(r.order, tool.Name)
	}
	r.tools[tool.Name] = tool
}

func (r *ToolRegistry) Get(name string) (AgentTool, bool) {
	tool, ok := r.tools[name]
	return tool, ok
}

func (r *ToolRegistry) OpenAITools() []openai.ChatCompletionToolParam {
	tools := make([]openai.ChatCompletionToolParam, 0, len(r.order))
	for _, name := range r.order {
		tool := r.tools[name]
		tools = append(tools, openai.ChatCompletionToolParam{
			Function: shared.FunctionDefinitionParam{
				Name:        tool.Name,
				Description: openai.String(tool.Description),
				Parameters:  shared.FunctionParameters(tool.Schema),
			},
		})
	}
	return tools
}

func (r *ToolRegistry) Execute(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	tool, ok := r.Get(name)
	if !ok {
		return nil, fmt.Errorf("tool %q not found", name)
	}
	return tool.Execute(ctx, raw)
}

func NewQAToolRegistry() *ToolRegistry {
	r := NewToolRegistry()

	registerTypedTool(r, "listGitLabProjects", "List available GitLab projects. Call this without arguments to see all projects the authenticated user can access.", listGitLabProjects)
	registerTypedTool(r, "listAppProjects", "List QA app projects. App projects connect an issue GitLab repo, a specs GitLab repo, test scenarios, recordings, and fix sessions.", listAppProjects)
	registerTypedTool(r, "getAppProject", "Get one QA app project by appProjectId, including issueRepoId, specsRepoId, and test context.", getAppProject)
	registerTypedTool(r, "getProjectTestContext", "Read the project-level testing context markdown for an app project.", getProjectTestContext)
	registerTypedTool(r, "updateProjectTestContext", "Replace the project-level testing context markdown for an app project.", updateProjectTestContext)
	registerTypedTool(r, "createGitLabIssue", "Create a new issue in a raw GitLab issue repo. Use an app project's issueRepoId when working inside a QA app project.", createGitLabIssue)
	registerTypedTool(r, "listGitLabIssues", "List issues from a raw GitLab project/repo. Use an app project's issueRepoId for project-scoped issue work.", listGitLabIssues)
	registerTypedTool(r, "listAllGitLabIssues", "List all GitLab issues assigned to or created by the authenticated user across all projects.", listAllGitLabIssues)
	registerTypedTool(r, "updateGitLabIssue", "Update an existing GitLab issue title, description, or state in a raw GitLab issue repo.", updateGitLabIssue)
	registerTypedTool(r, "getIssueComments", "List comments/notes for a GitLab issue IID in a raw GitLab issue repo.", getIssueComments)
	registerTypedTool(r, "createIssueComment", "Create a comment/note on a GitLab issue IID in a raw GitLab issue repo.", createIssueComment)
	registerTypedTool(r, "createIssueEvidence", "Create an evidence-formatted comment on a GitLab issue IID.", createIssueEvidence)
	registerTypedTool(r, "getIssueLinks", "List issue relations/links for a GitLab issue IID.", getIssueLinks)
	registerTypedTool(r, "listGitLabRepositoryTree", "List files and directories in any raw GitLab repository. Use issueRepoId for app code/issue repos and specsRepoId for specs repos.", listGitLabRepositoryTree)
	registerTypedTool(r, "getGitLabFileContent", "Read a file from any raw GitLab repository. Use this to inspect source and find selectors such as data-testid, id, aria-label, name, and button text.", getGitLabFileContent)
	registerTypedTool(r, "searchGitLabCode", "Search code or selector patterns in any raw GitLab repository. Leave ref empty unless the user explicitly asks for a branch.", searchGitLabCode)
	registerTypedTool(r, "grepRepo", "Search the local repository mirror for a pattern and return matching lines with file path, line number, and surrounding context. Faster and richer than searchGitLabCode — prefer this for code exploration. Set fixedString=true for literal matches, leave false for regex.", grepRepo)
	registerTypedTool(r, "findFiles", "Find files in the local repository mirror whose path matches a pattern (substring or glob like '*.tsx'). Use to locate files by name without browsing directories one at a time.", findFiles)
	registerTypedTool(r, "listGitLabBranches", "List branches in a GitLab project repository.", listGitLabBranches)
	registerTypedTool(r, "listSpecsTree", "List files and folders from an app project's specs repo. Uses appProjectId and resolves specsRepoId automatically.", listSpecsTree)
	registerTypedTool(r, "getSpecsFile", "Read a file from an app project's specs repo. Uses appProjectId and resolves specsRepoId automatically.", getSpecsFile)
	registerTypedTool(r, "searchSpecs", "Search file names/paths in an app project's specs repo tree.", searchSpecs)
	registerTypedTool(r, "getSpecsFileBlame", "Get Git blame information for a specs repo file.", getSpecsFileBlame)
	registerTypedTool(r, "listSpecsCommits", "List commit history for an app project's specs repo, optionally scoped to a file path.", listSpecsCommits)
	registerTypedTool(r, "commitSpecsFiles", "Commit one or more create/update/delete/move file actions to an app project's specs repo.", commitSpecsFiles)
	registerTypedTool(r, "listKnowledgeGraphs", "List cached knowledge graph catalogs for an app project's issue/source repo.", listKnowledgeGraphs)
	registerTypedTool(r, "getKnowledgeGraph", "Get or generate a knowledge graph catalog for an app project's issue/source repo.", getKnowledgeGraph)
	registerTypedTool(r, "getKnowledgeGraphCoverage", "Get route/module/selector coverage for a cached app project knowledge graph.", getKnowledgeGraphCoverage)
	registerTypedTool(r, "listRecordedTests", "List available recorded automation tests. Optionally filter by projectID/app project or issueID.", listRecordedTests)
	registerTypedTool(r, "runRecordedTest", "Run a recorded automation test by ID. Optional overrides can replace input values.", runRecordedTest)
	registerTypedTool(r, "listTestScenarios", "List imported test scenarios for one QA app project. Requires appProjectId; use listAppProjects or getAppProject first if the project ID is unknown. Returns compact summaries, not full test case bodies.", listTestScenarios)
	registerTypedTool(r, "runTestScenario", "Run generated automation tests for a specific scenario. Supports optional sheet filtering and chained execution.", runTestScenario)
	registerTypedTool(r, "runScenarioTestCase", "Run one generated automation test case from a scenario.", runScenarioTestCase)
	registerTypedTool(r, "save_automation_test", "Save a generated E2E automation test into a test scenario. Use one call per test case with complete steps, selectors, selectorCandidates, xpath, xpathCandidates, and elementHints.", saveAutomation)
	return r
}

func registerTypedTool[T any, R any](r *ToolRegistry, name, description string, fn func(context.Context, T) (R, error)) {
	r.Register(AgentTool{
		Name:        name,
		Description: description,
		Schema:      schemaFromType(reflect.TypeOf((*T)(nil)).Elem()),
		Execute: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var args T
			if len(strings.TrimSpace(string(raw))) > 0 {
				if err := json.Unmarshal(raw, &args); err != nil {
					return nil, fmt.Errorf("invalid arguments for %s: %w", name, err)
				}
			}
			return fn(ctx, args)
		},
	})
}

func schemaFromType(t reflect.Type) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return map[string]any{"type": "object", "additionalProperties": true}
	}

	properties := make(map[string]any)

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" { // unexported
			continue
		}
		name, _, skip := jsonFieldName(field)
		if skip {
			continue
		}
		properties[name] = schemaForField(field.Type)
	}

	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
}

func jsonFieldName(field reflect.StructField) (name string, omitempty bool, skip bool) {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return "", false, true
	}
	parts := strings.Split(tag, ",")
	if parts[0] != "" {
		name = parts[0]
	} else {
		name = field.Name
	}
	for _, part := range parts[1:] {
		if part == "omitempty" {
			omitempty = true
			break
		}
	}
	return name, omitempty, false
}

func schemaForField(t reflect.Type) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		return map[string]any{"type": "array", "items": schemaForField(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": schemaForField(t.Elem())}
	case reflect.Struct:
		return schemaFromType(t)
	case reflect.Interface:
		return map[string]any{"type": "object", "additionalProperties": true}
	default:
		return map[string]any{"type": "string"}
	}
}

func jsonRawMessageFromString(s string) json.RawMessage {
	if strings.TrimSpace(s) == "" {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(s)
}
