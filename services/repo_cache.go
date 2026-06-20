package services

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"qa-extension-backend/database"

	gitlab "gitlab.com/gitlab-org/api/client-go"
	"golang.org/x/oauth2"
)

const (
	defaultRepoCacheDir            = "/var/cache/qa-extension/repos"
	defaultRepoCacheSyncTTL        = 15 * time.Minute
	defaultRepoCacheCommandTimeout = 5 * time.Minute
	defaultRepoCacheSearchLimit    = 50
)

var (
	ErrRepoCacheDisabled = errors.New("repo cache is disabled")
	ErrRepoCacheToken    = errors.New("missing GitLab OAuth token for repo cache")
)

type RepoCacheService struct {
	rootDir        string
	enabled        bool
	syncTTL        time.Duration
	commandTimeout time.Duration
	searchLimit    int
	syncSlots      chan struct{}
	locks          sync.Map
}

type CachedRepo struct {
	ProjectID string
	CloneDir  string
}

type RepoTreeEntry struct {
	ID   string
	Name string
	Type string
	Path string
}

type RepoFileMeta struct {
	Path string
	Size int64
	Ref  string
}

type RepoSearchResult struct {
	FilePath string
	Ref      string
	Content  string
}

var defaultRepoCache = NewRepoCacheServiceFromEnv()

func NewRepoCacheServiceFromEnv() *RepoCacheService {
	root := strings.TrimSpace(os.Getenv("REPO_CACHE_DIR"))
	if root == "" {
		root = defaultRepoCacheDir
	}

	enabled := true
	if raw := strings.TrimSpace(os.Getenv("REPO_CACHE_ENABLED")); raw != "" {
		enabled = strings.EqualFold(raw, "true") || raw == "1" || strings.EqualFold(raw, "yes")
	}

	syncTTL := defaultRepoCacheSyncTTL
	if raw := strings.TrimSpace(os.Getenv("REPO_CACHE_SYNC_TTL")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			syncTTL = d
		} else if seconds, err := strconv.Atoi(raw); err == nil {
			syncTTL = time.Duration(seconds) * time.Second
		}
	}

	searchLimit := defaultRepoCacheSearchLimit
	if raw := strings.TrimSpace(os.Getenv("REPO_CACHE_SEARCH_LIMIT")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			searchLimit = n
		}
	}

	commandTimeout := defaultRepoCacheCommandTimeout
	if raw := strings.TrimSpace(os.Getenv("REPO_CACHE_COMMAND_TIMEOUT")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			commandTimeout = d
		} else if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
			commandTimeout = time.Duration(seconds) * time.Second
		}
	}

	concurrency := 3
	if raw := strings.TrimSpace(os.Getenv("REPO_CACHE_MAX_SYNC_CONCURRENCY")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			concurrency = n
		}
	}

	return &RepoCacheService{
		rootDir:        root,
		enabled:        enabled,
		syncTTL:        syncTTL,
		commandTimeout: commandTimeout,
		searchLimit:    searchLimit,
		syncSlots:      make(chan struct{}, concurrency),
	}
}

func DefaultRepoCache() *RepoCacheService {
	return defaultRepoCache
}

func (s *RepoCacheService) Enabled() bool {
	return s != nil && s.enabled
}

func (s *RepoCacheService) EnsureRepo(ctx context.Context, glClient *gitlab.Client, projectID string, force bool) (*CachedRepo, error) {
	if !s.Enabled() {
		return nil, ErrRepoCacheDisabled
	}
	if glClient == nil {
		return nil, fmt.Errorf("gitlab client is required")
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, fmt.Errorf("projectID is required")
	}

	token, err := gitLabTokenFromContext(ctx)
	if err != nil {
		return nil, err
	}

	project, _, err := glClient.Projects.GetProject(projectID, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get project: %w", err)
	}
	remoteURL := strings.TrimSpace(project.HTTPURLToRepo)
	if remoteURL == "" {
		return nil, fmt.Errorf("project %s does not expose an HTTP clone URL", projectID)
	}

	cloneDir := s.cloneDir(projectID)
	repo := &CachedRepo{ProjectID: projectID, CloneDir: cloneDir}
	if !force && s.isFresh(cloneDir) && s.isGitClone(ctx, cloneDir) {
		_ = s.touchAccess(cloneDir)
		return repo, nil
	}

	lock := s.localLock(projectID)
	lock.Lock()
	defer lock.Unlock()

	if !force && s.isFresh(cloneDir) && s.isGitClone(ctx, cloneDir) {
		_ = s.touchAccess(cloneDir)
		return repo, nil
	}

	unlock, locked := s.redisLock(ctx, projectID)
	if locked {
		defer unlock()
	}

	select {
	case s.syncSlots <- struct{}{}:
		defer func() { <-s.syncSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	start := time.Now()
	if err := os.MkdirAll(filepath.Dir(cloneDir), 0o700); err != nil {
		return nil, fmt.Errorf("failed to create repo cache dir: %w", err)
	}

	if !s.isGitClone(ctx, cloneDir) {
		_ = os.RemoveAll(cloneDir)
		if err := s.runGitWithToken(ctx, token.AccessToken, "", "clone", remoteURL, cloneDir); err != nil {
			return nil, fmt.Errorf("failed to clone repo: %w", err)
		}
	} else {
		if err := s.runGitWithToken(ctx, token.AccessToken, cloneDir, "remote", "set-url", "origin", remoteURL); err != nil {
			return nil, fmt.Errorf("failed to update remote: %w", err)
		}
		if err := s.runGitWithToken(ctx, token.AccessToken, cloneDir, "fetch", "--prune", "origin"); err != nil {
			return nil, fmt.Errorf("failed to fetch: %w", err)
		}
		if err := s.runGit(ctx, cloneDir, "reset", "--hard", "FETCH_HEAD"); err != nil {
			return nil, fmt.Errorf("failed to reset to FETCH_HEAD: %w", err)
		}
	}

	if err := s.writeStamp(cloneDir); err != nil {
		log.Printf("[RepoCache] failed to write sync stamp for %s: %v", projectID, err)
	}
	_ = s.touchAccess(cloneDir)
	log.Printf("[RepoCache] synced project %s in %s", projectID, time.Since(start))
	return repo, nil
}

// ListTree lists directory entries from the local clone working tree.
// The ref parameter is accepted for API compatibility but ignored — always reads the checked-out branch.
func (s *RepoCacheService) ListTree(ctx context.Context, glClient *gitlab.Client, projectID, ref, path string, recursive bool) ([]RepoTreeEntry, error) {
	repo, err := s.EnsureRepo(ctx, glClient, projectID, false)
	if err != nil {
		return nil, err
	}
	if err := validateRepoPath(path, true); err != nil {
		return nil, err
	}

	entries, err := s.listTreeFromClone(repo.CloneDir, path, recursive)
	if err != nil {
		repo, err = s.EnsureRepo(ctx, glClient, projectID, true)
		if err != nil {
			return nil, err
		}
		entries, err = s.listTreeFromClone(repo.CloneDir, path, recursive)
		if err != nil {
			return nil, err
		}
	}
	return entries, nil
}

func (s *RepoCacheService) listTreeFromClone(cloneDir, path string, recursive bool) ([]RepoTreeEntry, error) {
	cleanPath := strings.Trim(strings.TrimSpace(path), "/")
	if cleanPath == "." {
		cleanPath = ""
	}

	root := cloneDir
	if cleanPath != "" {
		root = filepath.Join(cloneDir, filepath.FromSlash(cleanPath))
	}

	var entries []RepoTreeEntry
	if !recursive {
		infos, err := os.ReadDir(root)
		if err != nil {
			return nil, err
		}
		for _, info := range infos {
			if info.Name() == ".git" {
				continue
			}
			entryType := "blob"
			if info.IsDir() {
				entryType = "tree"
			}
			entryPath := info.Name()
			if cleanPath != "" {
				entryPath = cleanPath + "/" + info.Name()
			}
			entries = append(entries, RepoTreeEntry{
				Name: info.Name(),
				Type: entryType,
				Path: entryPath,
			})
		}
	} else {
		err := filepath.Walk(root, func(fpath string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if info.IsDir() && info.Name() == ".git" {
				return filepath.SkipDir
			}
			rel, relErr := filepath.Rel(cloneDir, fpath)
			if relErr != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if rel == "." {
				return nil
			}
			entryType := "blob"
			if info.IsDir() {
				entryType = "tree"
			}
			entries = append(entries, RepoTreeEntry{
				Name: info.Name(),
				Type: entryType,
				Path: rel,
			})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return entries, nil
}

// ListFiles returns all file paths in the local clone working tree.
// The ref parameter is accepted for API compatibility but ignored — always reads the checked-out branch.
func (s *RepoCacheService) ListFiles(ctx context.Context, glClient *gitlab.Client, projectID, ref string) ([]string, error) {
	repo, err := s.EnsureRepo(ctx, glClient, projectID, false)
	if err != nil {
		return nil, err
	}
	files, err := s.listFilesFromClone(repo.CloneDir)
	if err != nil {
		repo, err = s.EnsureRepo(ctx, glClient, projectID, true)
		if err != nil {
			return nil, err
		}
		files, err = s.listFilesFromClone(repo.CloneDir)
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

func (s *RepoCacheService) listFilesFromClone(cloneDir string) ([]string, error) {
	var files []string
	err := filepath.Walk(cloneDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(cloneDir, path)
		if relErr != nil {
			return nil
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	return files, err
}

// ReadFile reads a file from the local clone working tree.
// The ref parameter is accepted for API compatibility but ignored — always reads the checked-out branch.
func (s *RepoCacheService) ReadFile(ctx context.Context, glClient *gitlab.Client, projectID, ref, filePath string) (string, RepoFileMeta, error) {
	if err := validateRepoPath(filePath, false); err != nil {
		return "", RepoFileMeta{}, err
	}
	repo, err := s.EnsureRepo(ctx, glClient, projectID, false)
	if err != nil {
		return "", RepoFileMeta{}, err
	}
	content, err := s.readFileFromClone(repo.CloneDir, filePath)
	if err != nil {
		repo, err = s.EnsureRepo(ctx, glClient, projectID, true)
		if err != nil {
			return "", RepoFileMeta{}, err
		}
		content, err = s.readFileFromClone(repo.CloneDir, filePath)
		if err != nil {
			return "", RepoFileMeta{}, err
		}
	}
	return content, RepoFileMeta{Path: filePath, Size: int64(len(content))}, nil
}

func (s *RepoCacheService) readFileFromClone(cloneDir, filePath string) (string, error) {
	abs := filepath.Join(cloneDir, filepath.FromSlash(filePath))
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Search finds files matching query and returns their full content.
// The ref parameter is accepted for API compatibility but ignored — always searches the checked-out branch.
func (s *RepoCacheService) Search(ctx context.Context, glClient *gitlab.Client, projectID, ref, query, path string, limit int) ([]RepoSearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if err := validateRepoPath(path, true); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > s.searchLimit {
		limit = s.searchLimit
	}

	repo, err := s.EnsureRepo(ctx, glClient, projectID, false)
	if err != nil {
		return nil, err
	}

	matches, err := s.grepPaths(ctx, repo.CloneDir, query, path, limit)
	if err != nil {
		repo, err = s.EnsureRepo(ctx, glClient, projectID, true)
		if err != nil {
			return nil, err
		}
		matches, err = s.grepPaths(ctx, repo.CloneDir, query, path, limit)
		if err != nil {
			return nil, err
		}
	}

	results := make([]RepoSearchResult, 0, len(matches))
	for _, filePath := range matches {
		content, readErr := s.readFileFromClone(repo.CloneDir, filePath)
		if readErr != nil {
			continue
		}
		results = append(results, RepoSearchResult{FilePath: filePath, Content: content})
	}
	return results, nil
}

func (s *RepoCacheService) CreateWorktree(ctx context.Context, glClient *gitlab.Client, projectID, ref, sessionID string) (string, func(), error) {
	repo, err := s.EnsureRepo(ctx, glClient, projectID, false)
	if err != nil {
		return "", nil, err
	}
	resolvedRef, err := s.resolveRef(ctx, repo.CloneDir, ref)
	if err != nil {
		return "", nil, err
	}
	if sessionID == "" {
		sessionID = strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	workDir := filepath.Join(os.TempDir(), "qa-repo-worktree-"+sanitizePathComponent(projectID)+"-"+sanitizePathComponent(sessionID))
	_ = os.RemoveAll(workDir)
	if err := s.runGit(ctx, repo.CloneDir, "worktree", "add", "--detach", workDir, resolvedRef); err != nil {
		return "", nil, fmt.Errorf("failed to create worktree: %w", err)
	}
	cleanup := func() {
		_ = s.runGit(context.Background(), repo.CloneDir, "worktree", "remove", "--force", workDir)
		_ = os.RemoveAll(workDir)
	}
	return workDir, cleanup, nil
}

func (s *RepoCacheService) resolveRef(ctx context.Context, cloneDir, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	candidates := []string{ref}
	if ref == "" {
		candidates = []string{"HEAD"}
	} else if !strings.HasPrefix(ref, "refs/") {
		candidates = []string{
			"refs/heads/" + ref,
			"refs/remotes/origin/" + ref,
			"refs/tags/" + ref,
			ref,
		}
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if err := s.runGit(ctx, cloneDir, "cat-file", "-e", candidate+"^{commit}"); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("ref %q not found", ref)
}

type RepoGrepMatch struct {
	FilePath string `json:"filePath"`
	Line     int    `json:"line"`
	Content  string `json:"content"`
}

// GrepLines searches file contents using native grep on the local clone working tree.
// The ref parameter is accepted for API compatibility but ignored — always searches the checked-out branch.
func (s *RepoCacheService) GrepLines(ctx context.Context, glClient *gitlab.Client, projectID, ref, pattern, path string, contextLines int, fixedString bool) ([]RepoGrepMatch, error) {
	if !s.enabled {
		return nil, ErrRepoCacheDisabled
	}
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, fmt.Errorf("pattern is required")
	}
	if err := validateRepoPath(path, true); err != nil {
		return nil, err
	}

	repo, err := s.EnsureRepo(ctx, glClient, projectID, false)
	if err != nil {
		return nil, err
	}

	matches, err := s.grepLines(ctx, repo.CloneDir, pattern, path, contextLines, fixedString)
	if err != nil {
		repo, err = s.EnsureRepo(ctx, glClient, projectID, true)
		if err != nil {
			return nil, err
		}
		matches, err = s.grepLines(ctx, repo.CloneDir, pattern, path, contextLines, fixedString)
		if err != nil {
			return nil, err
		}
	}
	return matches, nil
}

func (s *RepoCacheService) grepLines(ctx context.Context, cloneDir, pattern, path string, contextLines int, fixedString bool) ([]RepoGrepMatch, error) {
	args := []string{"-n", "--no-heading", "--with-filename"}
	if fixedString {
		args = append(args, "-F")
	}
	if contextLines > 0 {
		args = append(args, fmt.Sprintf("-A%d", contextLines), fmt.Sprintf("-B%d", contextLines))
	}
	args = append(args, "--", pattern)
	if strings.TrimSpace(path) != "" && path != "." {
		args = append(args, strings.TrimSpace(path))
	} else {
		args = append(args, ".")
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, s.commandTimeout)
	defer cancel()

	cmd := exec.CommandContext(timeoutCtx, "rg", args...)
	cmd.Dir = cloneDir
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			// exit code 1 means no matches — not an error
			return []RepoGrepMatch{}, nil
		}
		return nil, fmt.Errorf("rg failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return parseGrepLines(string(out)), nil
}

// parseGrepLines parses grep -n output:
//
//	match line:   <file>:<linenum>:<content>
//	context line: <file>-<linenum>-<content>
//	separator:    --
func parseGrepLines(out string) []RepoGrepMatch {
	var matches []RepoGrepMatch
	for _, line := range strings.Split(out, "\n") {
		if line == "" || line == "--" {
			continue
		}
		colonIdx := strings.Index(line, ":")
		dashIdx := strings.Index(line, "-")
		if colonIdx < 0 && dashIdx < 0 {
			continue
		}
		sep := byte(':')
		sepIdx := colonIdx
		if dashIdx >= 0 && (colonIdx < 0 || dashIdx < colonIdx) {
			sep = '-'
			sepIdx = dashIdx
		}
		file := strings.TrimPrefix(line[:sepIdx], "./")
		rest := line[sepIdx+1:]
		nextSep := strings.IndexByte(rest, sep)
		if nextSep < 0 {
			continue
		}
		lineNumStr := rest[:nextSep]
		content := rest[nextSep+1:]
		lineNum, err := strconv.Atoi(lineNumStr)
		if err != nil {
			continue
		}
		matches = append(matches, RepoGrepMatch{
			FilePath: file,
			Line:     lineNum,
			Content:  content,
		})
	}
	return matches
}

func (s *RepoCacheService) grepPaths(ctx context.Context, cloneDir, query, path string, limit int) ([]string, error) {
	searchPath := "."
	if strings.TrimSpace(path) != "" && path != "." {
		searchPath = strings.TrimSpace(path)
	}
	args := []string{"-l", "-F", "--", query, searchPath}

	timeoutCtx, cancel := context.WithTimeout(ctx, s.commandTimeout)
	defer cancel()

	cmd := exec.CommandContext(timeoutCtx, "rg", args...)
	cmd.Dir = cloneDir
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return []string{}, nil
		}
		return nil, fmt.Errorf("rg failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	seen := map[string]bool{}
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimPrefix(strings.TrimSpace(line), "./")
		if line == "" {
			continue
		}
		if !seen[line] {
			seen[line] = true
			paths = append(paths, line)
			if len(paths) >= limit {
				break
			}
		}
	}
	return paths, nil
}

func (s *RepoCacheService) runGit(ctx context.Context, dir string, args ...string) error {
	_, err := s.runGitOutput(ctx, dir, args...)
	return err
}

func (s *RepoCacheService) runGitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, s.commandTimeout)
	defer cancel()

	cmd := exec.CommandContext(timeoutCtx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s failed: %w: %s", safeGitArgs(args), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func (s *RepoCacheService) runGitWithToken(ctx context.Context, token, dir string, args ...string) error {
	if strings.TrimSpace(token) == "" {
		return ErrRepoCacheToken
	}
	askpass, cleanup, err := writeGitAskPass(token)
	if err != nil {
		return err
	}
	defer cleanup()

	timeoutCtx, cancel := context.WithTimeout(ctx, s.commandTimeout)
	defer cancel()

	cmd := exec.CommandContext(timeoutCtx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS="+askpass,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s failed: %w: %s", safeGitArgs(args), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (s *RepoCacheService) localLock(projectID string) *sync.Mutex {
	val, _ := s.locks.LoadOrStore(projectID, &sync.Mutex{})
	return val.(*sync.Mutex)
}

func (s *RepoCacheService) redisLock(ctx context.Context, projectID string) (func(), bool) {
	if database.RedisClient == nil {
		return func() {}, false
	}
	key := "repo-cache:lock:" + projectID
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		ok, err := database.RedisClient.SetNX(waitCtx, key, "1", 2*time.Minute).Result()
		if err != nil {
			return func() {}, false
		}
		if ok {
			return func() { _ = database.RedisClient.Del(context.Background(), key).Err() }, true
		}
		select {
		case <-waitCtx.Done():
			return func() {}, false
		case <-ticker.C:
		}
	}
}

func (s *RepoCacheService) cloneDir(projectID string) string {
	base := strings.TrimSpace(os.Getenv("GITLAB_BASE_URL"))
	if base == "" {
		base = "https://gitlab.com"
	}
	sum := sha1.Sum([]byte(base))
	return filepath.Join(s.rootDir, hex.EncodeToString(sum[:])[:12], sanitizePathComponent(projectID))
}

func (s *RepoCacheService) isGitClone(ctx context.Context, cloneDir string) bool {
	if _, err := os.Stat(filepath.Join(cloneDir, ".git")); err != nil {
		return false
	}
	return s.runGit(ctx, cloneDir, "rev-parse", "--is-inside-work-tree") == nil
}

func (s *RepoCacheService) isFresh(cloneDir string) bool {
	if s.syncTTL <= 0 {
		return false
	}
	info, err := os.Stat(filepath.Join(cloneDir, ".git", "qa-cache-last-sync"))
	if err != nil {
		return false
	}
	return time.Since(info.ModTime()) < s.syncTTL
}

func (s *RepoCacheService) writeStamp(cloneDir string) error {
	return os.WriteFile(filepath.Join(cloneDir, ".git", "qa-cache-last-sync"), []byte(time.Now().Format(time.RFC3339Nano)), 0o600)
}

func (s *RepoCacheService) touchAccess(cloneDir string) error {
	return os.WriteFile(filepath.Join(cloneDir, ".git", "qa-cache-last-access"), []byte(time.Now().Format(time.RFC3339Nano)), 0o600)
}

func gitLabTokenFromContext(ctx context.Context) (*oauth2.Token, error) {
	token, ok := ctx.Value("token").(*oauth2.Token)
	if !ok || token == nil || token.AccessToken == "" {
		return nil, ErrRepoCacheToken
	}
	return token, nil
}

func validateRepoPath(path string, allowEmpty bool) error {
	path = strings.TrimSpace(path)
	if path == "" || path == "." {
		if allowEmpty {
			return nil
		}
		return fmt.Errorf("path is required")
	}
	if strings.Contains(path, "\x00") || filepath.IsAbs(path) || strings.Contains(path, "\\") {
		return fmt.Errorf("invalid repository path")
	}
	cleaned := filepath.Clean(path)
	if cleaned == "." && allowEmpty {
		return nil
	}
	if strings.HasPrefix(cleaned, "..") || strings.Contains(cleaned, string(filepath.Separator)+".."+string(filepath.Separator)) || strings.HasPrefix(cleaned, ".git") || strings.Contains(cleaned, "/.git") {
		return fmt.Errorf("invalid repository path")
	}
	return nil
}

func writeGitAskPass(token string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "qa-git-askpass-*")
	if err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, "askpass.sh")
	script := "#!/bin/sh\ncase \"$1\" in\n*Username*) printf '%s\\n' 'oauth2' ;;\n*) printf '%s\\n' '" + strings.ReplaceAll(token, "'", "'\"'\"'") + "' ;;\nesac\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, err
	}
	return path, func() { _ = os.RemoveAll(dir) }, nil
}

func sanitizePathComponent(value string) string {
	value = strings.TrimSpace(value)
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

func safeGitArgs(args []string) string {
	if len(args) == 0 {
		return ""
	}
	safe := make([]string, len(args))
	copy(safe, args)
	for i, arg := range safe {
		if strings.Contains(arg, "oauth2:") || strings.Contains(arg, "@") && strings.Contains(arg, "://") {
			safe[i] = "<redacted-url>"
		}
	}
	return strings.Join(safe, " ")
}
