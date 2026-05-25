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
	registerTypedTool(r, "createGitLabIssue", "Create a new issue in a GitLab project.", createGitLabIssue)
	registerTypedTool(r, "listGitLabIssues", "List issues from a specific GitLab project. Requires projectId.", listGitLabIssues)
	registerTypedTool(r, "listAllGitLabIssues", "List all GitLab issues assigned to or created by the authenticated user across all projects.", listAllGitLabIssues)
	registerTypedTool(r, "updateGitLabIssue", "Update an existing GitLab issue title, description, or state.", updateGitLabIssue)
	registerTypedTool(r, "listGitLabRepositoryTree", "List files and directories in a GitLab repository. For automation generation, leave ref empty to use the default branch.", listGitLabRepositoryTree)
	registerTypedTool(r, "getGitLabFileContent", "Read a file from a GitLab repository. Use this to inspect React/Next/Vite source and find selectors such as data-testid, id, aria-label, name, and button text.", getGitLabFileContent)
	registerTypedTool(r, "searchGitLabCode", "Search code or selector patterns in a GitLab repository. For automation generation, leave ref empty unless the user explicitly asks for a branch.", searchGitLabCode)
	registerTypedTool(r, "listGitLabBranches", "List branches in a GitLab project repository.", listGitLabBranches)
	registerTypedTool(r, "listRecordedTests", "List available recorded automation tests. Optionally filter by projectID or issueID.", listRecordedTests)
	registerTypedTool(r, "runRecordedTest", "Run a recorded automation test by ID. Optional overrides can replace input values.", runRecordedTest)
	registerTypedTool(r, "listTestScenarios", "List uploaded test scenarios from XLSX documents.", listTestScenarios)
	registerTypedTool(r, "runTestScenario", "Run all generated tests for a specific scenario. Supports optional sheet filtering and chained execution.", runTestScenario)
	registerTypedTool(r, "runScenarioTestCase", "Run a specific generated test case from a scenario.", runScenarioTestCase)
	registerTypedTool(r, "save_automation_test", "Save a generated automation test to Redis. Use one call per test case. Include framework, complete steps, selectors, selectorCandidates, xpath, xpathCandidates, and elementHints.", saveAutomation)

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
