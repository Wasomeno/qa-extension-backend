package services

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestValidateRepoPath(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		allowEmpty bool
		wantErr    bool
	}{
		{name: "normal file", path: "app/page.tsx"},
		{name: "empty allowed", path: "", allowEmpty: true},
		{name: "empty rejected", path: "", wantErr: true},
		{name: "absolute rejected", path: "/tmp/file", wantErr: true},
		{name: "parent rejected", path: "../secret", wantErr: true},
		{name: "git dir rejected", path: ".git/config", wantErr: true},
		{name: "nul rejected", path: "app\x00page.tsx", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRepoPath(tt.path, tt.allowEmpty)
			if tt.wantErr && err == nil {
				t.Fatalf("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestRepoCacheLocalReadAndSearch(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	cloneDir := filepath.Join(tmp, "clone")

	runTestGit(t, "", "init", "-b", "main", src)
	runTestGit(t, src, "config", "user.email", "test@example.com")
	runTestGit(t, src, "config", "user.name", "Test User")
	if err := os.MkdirAll(filepath.Join(src, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "app", "page.tsx"), []byte("export const button = 'Submit'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, src, "add", ".")
	runTestGit(t, src, "commit", "-m", "initial")
	runTestGit(t, "", "clone", src, cloneDir)

	cache := &RepoCacheService{
		enabled:        true,
		commandTimeout: time.Minute,
		searchLimit:    10,
	}

	content, err := cache.readFileFromClone(cloneDir, "app/page.tsx")
	if err != nil {
		t.Fatalf("readFileFromClone: %v", err)
	}
	if content != "export const button = 'Submit'\n" {
		t.Fatalf("unexpected content: %q", content)
	}

	paths, err := cache.grepPaths(ctx, cloneDir, "Submit", "app", 10)
	if err != nil {
		t.Fatalf("grepPaths: %v", err)
	}
	if len(paths) != 1 || paths[0] != "app/page.tsx" {
		t.Fatalf("paths = %#v, want app/page.tsx", paths)
	}
}

func TestRepoCacheListTreeReturnsChildrenForScopedPath(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	cloneDir := filepath.Join(tmp, "clone")

	runTestGit(t, "", "init", "-b", "main", src)
	runTestGit(t, src, "config", "user.email", "test@example.com")
	runTestGit(t, src, "config", "user.name", "Test User")
	if err := os.MkdirAll(filepath.Join(src, "docs", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "docs", "intro.md"), []byte("intro\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "docs", "nested", "deep.md"), []byte("deep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, src, "add", ".")
	runTestGit(t, src, "commit", "-m", "initial")
	runTestGit(t, "", "clone", src, cloneDir)

	_ = ctx
	cache := &RepoCacheService{enabled: true, commandTimeout: time.Minute}

	entries, err := cache.listTreeFromClone(cloneDir, "docs", false)
	if err != nil {
		t.Fatalf("listTreeFromClone non-recursive: %v", err)
	}
	assertRepoTreeEntries(t, entries, []RepoTreeEntry{
		{Name: "intro.md", Path: "docs/intro.md", Type: "blob"},
		{Name: "nested", Path: "docs/nested", Type: "tree"},
	})

	recursiveEntries, err := cache.listTreeFromClone(cloneDir, "docs", true)
	if err != nil {
		t.Fatalf("listTreeFromClone recursive: %v", err)
	}
	tree := repoEntriesToFileTree(recursiveEntries, true, "docs")
	if len(tree) != 2 {
		t.Fatalf("tree length = %d, want 2: %#v", len(tree), tree)
	}
	if tree[0].Path != "docs/nested" || tree[0].Type != "tree" {
		t.Fatalf("first tree node = %#v, want docs/nested tree", tree[0])
	}
	if len(tree[0].Children) != 1 || tree[0].Children[0].Path != "docs/nested/deep.md" {
		t.Fatalf("nested children = %#v, want docs/nested/deep.md", tree[0].Children)
	}
	if tree[1].Path != "docs/intro.md" || tree[1].Type != "blob" {
		t.Fatalf("second tree node = %#v, want docs/intro.md blob", tree[1])
	}
}

func TestNewRepoCacheServiceFromEnvParsesDurations(t *testing.T) {
	t.Setenv("REPO_CACHE_DIR", t.TempDir())
	t.Setenv("REPO_CACHE_SYNC_TTL", "30m")
	t.Setenv("REPO_CACHE_COMMAND_TIMEOUT", "7m")

	cache := NewRepoCacheServiceFromEnv()
	if cache.syncTTL != 30*time.Minute {
		t.Fatalf("syncTTL = %s, want 30m", cache.syncTTL)
	}
	if cache.commandTimeout != 7*time.Minute {
		t.Fatalf("commandTimeout = %s, want 7m", cache.commandTimeout)
	}
}

func assertRepoTreeEntries(t *testing.T, got []RepoTreeEntry, want []RepoTreeEntry) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("entries length = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Name != want[i].Name || got[i].Path != want[i].Path || got[i].Type != want[i].Type {
			t.Fatalf("entry %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func runTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v: %s", args, err, string(out))
	}
}
