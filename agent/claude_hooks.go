package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	stopMarkerFile = ".claude-stop-done"
	errorMarkerFile = ".claude-stop-error"
	hookConfigFile = ".claude/settings.json"
	defaultHookTimeout = 10 * time.Minute
	pollInterval = 500 * time.Millisecond
)

// ClaudeHookConfig represents the per-session Claude Code hooks configuration
type ClaudeHookConfig struct {
	Hooks HooksConfig `json:"hooks"`
}

type HooksConfig struct {
	Stop []StopHook `json:"Stop"`
}

type StopHook struct {
	Type    string            `json:"type"`
	Command string            `json:"command,omitempty"`
	Hooks   []NestedStopHook  `json:"hooks,omitempty"`
}

type NestedStopHook struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

// PrepareSessionHook creates the hooks configuration inside the session directory.
// When Claude Code finishes, it will execute the Stop hook which creates a marker file.
func PrepareSessionHook(sessionDir string) error {
	hookDir := filepath.Join(sessionDir, ".claude")
	if err := os.MkdirAll(hookDir, 0755); err != nil {
		return fmt.Errorf("failed to create .claude directory: %w", err)
	}

	config := ClaudeHookConfig{
		Hooks: HooksConfig{
			Stop: []StopHook{
				{
					Type: "command",
					Command: fmt.Sprintf("touch %s/%s", sessionDir, stopMarkerFile),
				},
			},
		},
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal hook config: %w", err)
	}

	configPath := filepath.Join(hookDir, "settings.json")
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write hook config: %w", err)
	}

	return nil
}

// WaitForStopSignal polls for the stop marker file created by the Stop hook.
// Returns nil when the marker file is found, or an error on timeout or if an error marker is found.
func WaitForStopSignal(sessionDir string, timeout time.Duration) error {
	if timeout == 0 {
		timeout = defaultHookTimeout
	}

	startTime := time.Now()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Check for error marker first
			errorPath := filepath.Join(sessionDir, errorMarkerFile)
			if _, err := os.Stat(errorPath); err == nil {
				return fmt.Errorf("claude session ended with an error (found %s)", errorMarkerFile)
			}

			// Check for stop marker
			stopPath := filepath.Join(sessionDir, stopMarkerFile)
			if _, err := os.Stat(stopPath); err == nil {
				// Remove the marker files
				os.Remove(stopPath)
				os.Remove(errorPath)
				return nil
			}

			// Check timeout
			if time.Since(startTime) > timeout {
				return fmt.Errorf("timeout waiting for Claude Code to finish after %v", timeout)
			}
		}
	}
}

// CleanupSessionHook removes the hooks configuration from the session directory.
func CleanupSessionHook(sessionDir string) error {
	configPath := filepath.Join(sessionDir, hookConfigFile)
	if err := os.Remove(configPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove hook config: %w", err)
	}
	return nil
}

// CreateErrorMarker creates an error marker file to signal that the session ended with an error.
func CreateErrorMarker(sessionDir string) error {
	errorPath := filepath.Join(sessionDir, errorMarkerFile)
	return os.WriteFile(errorPath, []byte{}, 0644)
}
