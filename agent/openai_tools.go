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
	return r.OpenAIToolsForContext(context.Background())
}

func (r *ToolRegistry) OpenAIToolsForContext(ctx context.Context) []openai.ChatCompletionToolParam {
	tools := make([]openai.ChatCompletionToolParam, 0, len(r.order))
	for _, name := range r.order {
		if !toolAllowedInContext(ctx, name) {
			continue
		}
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
	if !toolAllowedInContext(ctx, name) {
		return nil, fmt.Errorf("tool %q is not allowed in this generation context", name)
	}
	tool, ok := r.Get(name)
	if !ok {
		return nil, fmt.Errorf("tool %q not found", name)
	}
	return tool.Execute(ctx, raw)
}

func toolAllowedInContext(ctx context.Context, name string) bool {
	allowlist, ok := ctx.Value(generationToolAllowlistContextKey{}).(map[string]bool)
	if !ok || len(allowlist) == 0 {
		return true
	}
	return allowlist[name]
}

// NewQAToolRegistry exposes the tools that the background test-generation
// worker actually invokes. The historical LLM toolkit (app/specs/issue CRUD,
// recordings, scenarios, GitLab writes, etc.) was retired together with the
// scenario/recording/chat endpoints and no longer needs to be wired in here.
func NewQAToolRegistry() *ToolRegistry {
	r := NewToolRegistry()

	registerTypedTool(r, "repo_ls", "List bounded files and directories from a repository mirror. Use path, shallow depth, and limits to navigate incrementally.", repoLS)
	registerTypedTool(r, "repo_find", "Find repository files by path substring or glob pattern using the local mirror. Returns bounded file paths only.", repoFind)
	registerTypedTool(r, "repo_grep", "Search repository text using the local mirror. Returns bounded matching lines with optional surrounding context.", repoGrep)
	registerTypedTool(r, "repo_read", "Read a bounded line window from one repository file. Use startLine and lineCount instead of reading full files.", repoRead)
	registerTypedTool(r, "repo_branches", "List bounded branch names from the repository mirror.", repoBranches)
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
		return json.RawMessage("{}")
	}
	return json.RawMessage(s)
}
