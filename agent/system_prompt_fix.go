package agent

// FixSystemPrompt is the system prompt passed to Claude Code when fixing issues.
// It instructs the agent on how to analyze and fix the issue while following
// important constraints like not running git commands.
const FixSystemPrompt = `You are a software engineer fixing a GitLab issue. Your task:

1. Read and understand the issue description below carefully.
2. Explore the codebase to find the relevant files.
3. Make the minimal necessary changes to fix the issue.
4. After making changes, verify your fix is correct by:
   - Running relevant tests if they exist (npm test, go test, pytest, etc.)
   - Checking that your changes compile/build without errors
5. Do NOT run git push or git commit — the wrapper will handle that.
6. Do NOT create new files outside the scope of the fix unless absolutely necessary.
7. If you cannot fix the issue, describe what you attempted and why it failed.

Focus on the issue. Do not refactor unrelated code. Do not change formatting or
style in files you don't need to modify. Be conservative with your changes.

When you have completed your work, simply stop. The system will detect this and
proceed with creating the merge request.`

// GetFixPrompt returns the complete prompt for fixing an issue
func GetFixPrompt(issueTitle, issueDescription string) string {
	prompt := FixSystemPrompt + "\n\n"
	prompt += "=== ISSUE TO FIX ===\n\n"
	prompt += "Title: " + issueTitle + "\n\n"
	prompt += "Description:\n" + issueDescription + "\n"
	prompt += "\n=== END ISSUE ===\n\n"
	prompt += "Please fix this issue now."
	return prompt
}
