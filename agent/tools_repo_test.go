package agent

import (
	"strings"
	"testing"
)

func TestBuildRepoReadWindowBoundsLinesAndBytes(t *testing.T) {
	content := ""
	for i := 1; i <= repoReadMaxLines+10; i++ {
		content += "line\n"
	}

	got := buildRepoReadWindow(content, 2, repoReadMaxLines+50)

	if got.StartLine != 2 {
		t.Fatalf("StartLine = %d, want 2", got.StartLine)
	}
	if got.EndLine != repoReadMaxLines+1 {
		t.Fatalf("EndLine = %d, want %d", got.EndLine, repoReadMaxLines+1)
	}
	if got.TotalLines != repoReadMaxLines+10 {
		t.Fatalf("TotalLines = %d, want %d", got.TotalLines, repoReadMaxLines+10)
	}
	if !got.Truncated {
		t.Fatal("Truncated = false, want true")
	}
	if strings.Count(got.Content, "\n") != repoReadMaxLines {
		t.Fatalf("content line count = %d, want %d", strings.Count(got.Content, "\n"), repoReadMaxLines)
	}
}

func TestBuildRepoReadWindowBoundsBytesWithoutBreakingUTF8(t *testing.T) {
	content := strings.Repeat("ą", repoReadMaxBytes)

	got := buildRepoReadWindow(content, 1, 1)

	if len(got.Content) > repoReadMaxBytes {
		t.Fatalf("content length = %d, want <= %d", len(got.Content), repoReadMaxBytes)
	}
	if !got.Truncated {
		t.Fatal("Truncated = false, want true")
	}
	if strings.ContainsRune(got.Content, '\uFFFD') {
		t.Fatal("content contains replacement rune")
	}
}

func TestFilterRepoFilesSupportsPathAndGlob(t *testing.T) {
	files := []string{
		"app/page.tsx",
		"app/users/page.tsx",
		"components/UserForm.tsx",
		"README.md",
	}

	got := filterRepoFiles(files, "*.tsx", "app")

	want := []string{"app/page.tsx", "app/users/page.tsx"}
	if len(got) != len(want) {
		t.Fatalf("len(got) = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRepoEntryDepth(t *testing.T) {
	if got := repoEntryDepth("", "app/users/page.tsx"); got != 3 {
		t.Fatalf("root depth = %d, want 3", got)
	}
	if got := repoEntryDepth("app", "app/users/page.tsx"); got != 2 {
		t.Fatalf("scoped depth = %d, want 2", got)
	}
}

func TestValidateAgentRepoPath(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		allowEmpty bool
		wantErr    bool
	}{
		{name: "normal", path: "app/page.tsx"},
		{name: "empty allowed", path: "", allowEmpty: true},
		{name: "empty rejected", path: "", wantErr: true},
		{name: "absolute rejected", path: "/tmp/file", wantErr: true},
		{name: "parent rejected", path: "../secret", wantErr: true},
		{name: "git rejected", path: ".git/config", wantErr: true},
		{name: "backslash rejected", path: `app\page.tsx`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAgentRepoPath(tt.path, tt.allowEmpty)
			if tt.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
