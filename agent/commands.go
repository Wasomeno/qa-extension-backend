package agent

import (
	"context"
	"regexp"
)

type SlashCommand struct {
	Pattern      string
	Description  string
	ToolName     string
	ArgExtractor func(input string) map[string]any
}

func GetSlashCommands() []SlashCommand {
	return []SlashCommand{
		{
			Pattern:     `^/projects\s*$`,
			Description: "List all accessible GitLab projects",
			ToolName:    "listGitLabProjects",
			ArgExtractor: func(input string) map[string]any {
				return map[string]any{}
			},
		},
		{
			Pattern:     `^/myissues\s*$`,
			Description: "List all issues assigned to or created by you",
			ToolName:    "listAllGitLabIssues",
			ArgExtractor: func(input string) map[string]any {
				return map[string]any{}
			},
		},
		{
			Pattern:     `^/search\s+(.+)$`,
			Description: "Search for projects matching the query",
			ToolName:    "listGitLabProjects",
			ArgExtractor: func(input string) map[string]any {
				re := regexp.MustCompile(`^/search\s+(.+)$`)
				matches := re.FindStringSubmatch(input)
				if len(matches) > 1 {
					return map[string]any{"search": matches[1]}
				}
				return map[string]any{}
			},
		},
		{
			Pattern:     `^/new\s+(.+)$`,
			Description: "Create a new issue with the given title",
			ToolName:    "createGitLabIssue",
			ArgExtractor: func(input string) map[string]any {
				re := regexp.MustCompile(`^/new\s+(.+)$`)
				matches := re.FindStringSubmatch(input)
				if len(matches) > 1 {
					return map[string]any{
						"projectId":   0,
						"title":       matches[1],
						"description": "",
						"labels":      []string{},
					}
				}
				return map[string]any{}
			},
		},
		{
			Pattern:     `^/help\s*$`,
			Description: "Display available slash commands",
			ToolName:    "",
			ArgExtractor: func(input string) map[string]any {
				return map[string]any{}
			},
		},
	}
}

func MatchSlashCommand(input string) (*SlashCommand, map[string]any, bool) {
	// Check built-in commands first
	commands := GetSlashCommands()
	for i := range commands {
		cmd := &commands[i]
		re := regexp.MustCompile(cmd.Pattern)
		if re.MatchString(input) {
			return cmd, cmd.ArgExtractor(input), true
		}
	}
	return nil, nil, false
}

// MatchCustomSlashCommand checks if the input matches a custom command
// Returns the CustomCommand, extracted args, and whether it matched
func MatchCustomSlashCommand(ctx context.Context, input string) (*CustomCommand, map[string]any, bool) {
	// Extract command name (everything after / and before space)
	re := regexp.MustCompile(`^/(\S+)`)
	matches := re.FindStringSubmatch(input)
	if len(matches) < 2 {
		return nil, nil, false
	}

	commandName := matches[1]
	// Custom commands are stored with / prefix
	fullName := "/" + commandName

	// Get user's custom commands using session_id from context
	sessionID, _ := ctx.Value("session_id").(string)
	if sessionID == "" {
		sessionID = "user" // fallback for backwards compatibility
	}
	customCommands, err := GetUserCustomCommands(ctx, sessionID)
	if err != nil {
		return nil, nil, false
	}

	for _, cmd := range customCommands {
		if cmd.Name == fullName {
			// Extract arguments after the command name
			argsRe := regexp.MustCompile(`^/\S+\s*(.*)$`)
			argsMatch := argsRe.FindStringSubmatch(input)
			var argString string
			if len(argsMatch) > 1 {
				argString = argsMatch[1]
			}

			// Parse arguments into map
			args := parseCommandArgs(cmd.PromptTemplate, argString)
			return cmd, args, true
		}
	}

	return nil, nil, false
}

// parseCommandArgs extracts arguments from the input based on the prompt template
// This is a simple implementation - more sophisticated parsing could be added
func parseCommandArgs(promptTemplate string, argString string) map[string]any {
	args := make(map[string]any)

	if argString == "" {
		return args
	}

	// Split by space and treat remaining as single argument
	// More sophisticated parsing could be implemented here
	args["input"] = argString

	return args
}

// IsSlashCommand checks if the input starts with a slash command
func IsSlashCommand(input string) bool {
	return len(input) > 0 && input[0] == '/'
}

// GetAllSlashCommands returns all available slash commands (built-in + custom)
// for help display purposes
func GetAllSlashCommands(ctx context.Context) []SlashCommand {
	commands := GetSlashCommands()

	// Get user's custom commands using session_id from context
	sessionID, _ := ctx.Value("session_id").(string)
	if sessionID == "" {
		sessionID = "user" // fallback for backwards compatibility
	}
	customCommands, err := GetUserCustomCommands(ctx, sessionID)
	if err != nil {
		return commands
	}

	for _, cmd := range customCommands {
		slashCmd := SlashCommand{
			Pattern:     `^` + regexp.QuoteMeta(cmd.Name) + `\s*$`,
			Description: cmd.Description,
			ToolName:    cmd.ToolName,
			ArgExtractor: func(input string) map[string]any {
				argsRe := regexp.MustCompile(`^/\S+\s*(.*)$`)
				argsMatch := argsRe.FindStringSubmatch(input)
				var argString string
				if len(argsMatch) > 1 {
					argString = argsMatch[1]
				}
				return parseCommandArgs(cmd.PromptTemplate, argString)
			},
		}
		commands = append(commands, slashCmd)
	}

	return commands
}
