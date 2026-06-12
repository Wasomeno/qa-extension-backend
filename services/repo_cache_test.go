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

func TestRepoCacheLocalGitReadAndSearch(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	mirror := filepath.Join(tmp, "repo.git")

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
	runTestGit(t, "", "clone", "--bare", src, mirror)

	cache := &RepoCacheService{
		enabled:        true,
		commandTimeout: time.Minute,
		searchLimit:    10,
	}

	ref, err := cache.resolveRef(ctx, mirror, "main")
	if err != nil {
		t.Fatalf("resolveRef: %v", err)
	}
	if ref != "refs/heads/main" {
		t.Fatalf("ref = %q, want refs/heads/main", ref)
	}

	content, err := cache.showFile(ctx, mirror, ref, "app/page.tsx")
	if err != nil {
		t.Fatalf("showFile: %v", err)
	}
	if content != "export const button = 'Submit'\n" {
		t.Fatalf("unexpected content: %q", content)
	}

	paths, err := cache.grepPaths(ctx, mirror, ref, "Submit", "app", 10)
	if err != nil {
		t.Fatalf("grepPaths: %v", err)
	}
	if len(paths) != 1 || paths[0] != "app/page.tsx" {
		t.Fatalf("paths = %#v, want app/page.tsx", paths)
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
