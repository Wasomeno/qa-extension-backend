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
	defaultRepoCacheSyncTTL        = time.Minute
	defaultRepoCacheCommandTimeout = 2 * time.Minute
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
	MirrorDir string
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
		commandTimeout: defaultRepoCacheCommandTimeout,
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

	mirrorDir := s.mirrorDir(projectID)
	repo := &CachedRepo{ProjectID: projectID, MirrorDir: mirrorDir}
	if !force && s.isFresh(mirrorDir) && s.isGitMirror(ctx, mirrorDir) {
		_ = s.touchAccess(mirrorDir)
		return repo, nil
	}

	lock := s.localLock(projectID)
	lock.Lock()
	defer lock.Unlock()

	if !force && s.isFresh(mirrorDir) && s.isGitMirror(ctx, mirrorDir) {
		_ = s.touchAccess(mirrorDir)
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
	if err := os.MkdirAll(filepath.Dir(mirrorDir), 0o700); err != nil {
		return nil, fmt.Errorf("failed to create repo cache dir: %w", err)
	}

	if !s.isGitMirror(ctx, mirrorDir) {
		_ = os.RemoveAll(mirrorDir)
		if err := s.runGitWithToken(ctx, token.AccessToken, "", "clone", "--mirror", remoteURL, mirrorDir); err != nil {
			return nil, fmt.Errorf("failed to clone mirror: %w", err)
		}
	} else {
		if err := s.runGitWithToken(ctx, token.AccessToken, mirrorDir, "remote", "set-url", "origin", remoteURL); err != nil {
			return nil, fmt.Errorf("failed to update mirror remote: %w", err)
		}
		if err := s.runGitWithToken(ctx, token.AccessToken, mirrorDir, "fetch", "--prune", "origin", "+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*"); err != nil {
			return nil, fmt.Errorf("failed to fetch mirror: %w", err)
		}
	}

	if err := s.writeStamp(mirrorDir); err != nil {
		log.Printf("[RepoCache] failed to write sync stamp for %s: %v", projectID, err)
	}
	_ = s.touchAccess(mirrorDir)
	log.Printf("[RepoCache] synced project %s in %s", projectID, time.Since(start))
	return repo, nil
}

func (s *RepoCacheService) ListTree(ctx context.Context, glClient *gitlab.Client, projectID, ref, path string, recursive bool) ([]RepoTreeEntry, error) {
	repo, resolvedRef, err := s.ensureRepoRef(ctx, glClient, projectID, ref, false)
	if err != nil {
		return nil, err
	}
	if err := validateRepoPath(path, true); err != nil {
		return nil, err
	}

	entries, err := s.listTreeFromMirror(ctx, repo.MirrorDir, resolvedRef, path, recursive)
	if err != nil {
		repo, resolvedRef, err = s.ensureRepoRef(ctx, glClient, projectID, ref, true)
		if err != nil {
			return nil, err
		}
		entries, err = s.listTreeFromMirror(ctx, repo.MirrorDir, resolvedRef, path, recursive)
		if err != nil {
			return nil, err
		}
	}
	return entries, nil
}

func (s *RepoCacheService) listTreeFromMirror(ctx context.Context, mirrorDir, ref, path string, recursive bool) ([]RepoTreeEntry, error) {
	cleanPath := strings.Trim(strings.TrimSpace(path), "/")
	if cleanPath == "." {
		cleanPath = ""
	}

	args := []string{"ls-tree"}
	if recursive {
		args = append(args, "-r", "-t")
	}
	if cleanPath == "" {
		args = append(args, ref)
	} else {
		args = append(args, ref+":"+cleanPath)
	}

	out, err := s.runGitOutput(ctx, mirrorDir, args...)
	if err != nil {
		return nil, err
	}
	entries := parseRepoTreeEntries(out)
	if cleanPath == "" {
		return entries, nil
	}
	for i := range entries {
		entries[i].Path = cleanPath + "/" + strings.TrimPrefix(entries[i].Path, "/")
		entries[i].Name = filepath.Base(entries[i].Path)
	}
	return entries, nil
}

func (s *RepoCacheService) ListFiles(ctx context.Context, glClient *gitlab.Client, projectID, ref string) ([]string, error) {
	repo, resolvedRef, err := s.ensureRepoRef(ctx, glClient, projectID, ref, false)
	if err != nil {
		return nil, err
	}
	out, err := s.runGitOutput(ctx, repo.MirrorDir, "ls-tree", "-r", "--name-only", resolvedRef)
	if err != nil {
		repo, resolvedRef, err = s.ensureRepoRef(ctx, glClient, projectID, ref, true)
		if err != nil {
			return nil, err
		}
		out, err = s.runGitOutput(ctx, repo.MirrorDir, "ls-tree", "-r", "--name-only", resolvedRef)
		if err != nil {
			return nil, err
		}
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

func (s *RepoCacheService) ReadFile(ctx context.Context, glClient *gitlab.Client, projectID, ref, filePath string) (string, RepoFileMeta, error) {
	if err := validateRepoPath(filePath, false); err != nil {
		return "", RepoFileMeta{}, err
	}
	repo, resolvedRef, err := s.ensureRepoRef(ctx, glClient, projectID, ref, false)
	if err != nil {
		return "", RepoFileMeta{}, err
	}
	content, err := s.showFile(ctx, repo.MirrorDir, resolvedRef, filePath)
	if err != nil {
		repo, resolvedRef, err = s.ensureRepoRef(ctx, glClient, projectID, ref, true)
		if err != nil {
			return "", RepoFileMeta{}, err
		}
		content, err = s.showFile(ctx, repo.MirrorDir, resolvedRef, filePath)
		if err != nil {
			return "", RepoFileMeta{}, err
		}
	}
	return content, RepoFileMeta{Path: filePath, Size: int64(len(content)), Ref: resolvedRef}, nil
}

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

	repo, resolvedRef, err := s.ensureRepoRef(ctx, glClient, projectID, ref, false)
	if err != nil {
		return nil, err
	}

	matches, err := s.grepPaths(ctx, repo.MirrorDir, resolvedRef, query, path, limit)
	if err != nil {
		repo, resolvedRef, err = s.ensureRepoRef(ctx, glClient, projectID, ref, true)
		if err != nil {
			return nil, err
		}
		matches, err = s.grepPaths(ctx, repo.MirrorDir, resolvedRef, query, path, limit)
		if err != nil {
			return nil, err
		}
	}

	results := make([]RepoSearchResult, 0, len(matches))
	for _, filePath := range matches {
		content, err := s.showFile(ctx, repo.MirrorDir, resolvedRef, filePath)
		if err != nil {
			continue
		}
		results = append(results, RepoSearchResult{FilePath: filePath, Ref: resolvedRef, Content: content})
	}
	return results, nil
}

func (s *RepoCacheService) CreateWorktree(ctx context.Context, glClient *gitlab.Client, projectID, ref, sessionID string) (string, func(), error) {
	repo, resolvedRef, err := s.ensureRepoRef(ctx, glClient, projectID, ref, false)
	if err != nil {
		return "", nil, err
	}
	if sessionID == "" {
		sessionID = strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	workDir := filepath.Join(os.TempDir(), "qa-repo-worktree-"+sanitizePathComponent(projectID)+"-"+sanitizePathComponent(sessionID))
	_ = os.RemoveAll(workDir)
	if err := s.runGit(ctx, repo.MirrorDir, "worktree", "add", "--detach", workDir, resolvedRef); err != nil {
		return "", nil, fmt.Errorf("failed to create worktree: %w", err)
	}
	cleanup := func() {
		_ = s.runGit(context.Background(), repo.MirrorDir, "worktree", "remove", "--force", workDir)
		_ = os.RemoveAll(workDir)
	}
	return workDir, cleanup, nil
}

func (s *RepoCacheService) ensureRepoRef(ctx context.Context, glClient *gitlab.Client, projectID, ref string, force bool) (*CachedRepo, string, error) {
	repo, err := s.EnsureRepo(ctx, glClient, projectID, force)
	if err != nil {
		return nil, "", err
	}
	resolvedRef, err := s.resolveRef(ctx, repo.MirrorDir, ref)
	if err != nil {
		return nil, "", err
	}
	return repo, resolvedRef, nil
}

func (s *RepoCacheService) resolveRef(ctx context.Context, mirrorDir, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	candidates := []string{ref}
	if ref == "" {
		candidates = []string{"HEAD"}
	} else if !strings.HasPrefix(ref, "refs/") {
		candidates = []string{"refs/heads/" + ref, "refs/tags/" + ref, ref}
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if err := s.runGit(ctx, mirrorDir, "cat-file", "-e", candidate+"^{commit}"); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("ref %q not found", ref)
}

func (s *RepoCacheService) showFile(ctx context.Context, mirrorDir, ref, filePath string) (string, error) {
	return s.runGitOutput(ctx, mirrorDir, "show", ref+":"+filePath)
}

type RepoGrepMatch struct {
	FilePath string `json:"filePath"`
	Line     int    `json:"line"`
	Content  string `json:"content"`
}

// GrepLines runs git grep -n on the local mirror and returns matching lines with file path and line number.
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

	repo, resolvedRef, err := s.ensureRepoRef(ctx, glClient, projectID, ref, false)
	if err != nil {
		return nil, err
	}

	matches, err := s.grepLines(ctx, repo.MirrorDir, resolvedRef, pattern, path, contextLines, fixedString)
	if err != nil {
		repo, resolvedRef, err = s.ensureRepoRef(ctx, glClient, projectID, ref, true)
		if err != nil {
			return nil, err
		}
		matches, err = s.grepLines(ctx, repo.MirrorDir, resolvedRef, pattern, path, contextLines, fixedString)
		if err != nil {
			return nil, err
		}
	}
	return matches, nil
}

func (s *RepoCacheService) grepLines(ctx context.Context, mirrorDir, ref, pattern, path string, contextLines int, fixedString bool) ([]RepoGrepMatch, error) {
	args := []string{"grep", "-n", "-I"}
	if fixedString {
		args = append(args, "-F")
	}
	if contextLines > 0 {
		args = append(args, fmt.Sprintf("--context=%d", contextLines))
	}
	args = append(args, pattern, ref)
	if strings.TrimSpace(path) != "" && path != "." {
		args = append(args, "--", strings.TrimSpace(path))
	}
	out, err := s.runGitOutput(ctx, mirrorDir, args...)
	if err != nil {
		if strings.TrimSpace(out) == "" {
			return []RepoGrepMatch{}, nil
		}
		return nil, err
	}
	return parseGrepLines(out), nil
}

// parseGrepLines parses git grep -n output:
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
		// Determine separator: match uses ':', context uses '-'
		// Format: <file><sep><linenum><sep><content>
		// File paths may contain '-' so we look for the numeric linenum segment.
		colonIdx := strings.Index(line, ":")
		dashIdx := strings.Index(line, "-")
		if colonIdx < 0 && dashIdx < 0 {
			continue
		}
		// Pick whichever separator comes first
		sep := byte(':')
		sepIdx := colonIdx
		if dashIdx >= 0 && (colonIdx < 0 || dashIdx < colonIdx) {
			sep = '-'
			sepIdx = dashIdx
		}
		file := line[:sepIdx]
		rest := line[sepIdx+1:]
		// Find next separator to split linenum from content
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

func (s *RepoCacheService) grepPaths(ctx context.Context, mirrorDir, ref, query, path string, limit int) ([]string, error) {
	args := []string{"grep", "-l", "-I", "--fixed-strings", query, ref}
	if strings.TrimSpace(path) != "" && path != "." {
		args = append(args, "--", strings.TrimSpace(path))
	}
	out, err := s.runGitOutput(ctx, mirrorDir, args...)
	if err != nil {
		if strings.TrimSpace(out) == "" {
			return []string{}, nil
		}
		return nil, err
	}
	seen := map[string]bool{}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if idx := strings.Index(line, ":"); idx >= 0 {
			line = line[idx+1:]
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

func (s *RepoCacheService) mirrorDir(projectID string) string {
	base := strings.TrimSpace(os.Getenv("GITLAB_BASE_URL"))
	if base == "" {
		base = "https://gitlab.com"
	}
	sum := sha1.Sum([]byte(base))
	return filepath.Join(s.rootDir, hex.EncodeToString(sum[:])[:12], sanitizePathComponent(projectID)+".git")
}

func (s *RepoCacheService) isGitMirror(ctx context.Context, mirrorDir string) bool {
	if _, err := os.Stat(filepath.Join(mirrorDir, "HEAD")); err != nil {
		return false
	}
	return s.runGit(ctx, mirrorDir, "rev-parse", "--is-bare-repository") == nil
}

func (s *RepoCacheService) isFresh(mirrorDir string) bool {
	if s.syncTTL <= 0 {
		return false
	}
	info, err := os.Stat(filepath.Join(mirrorDir, ".qa-cache-last-sync"))
	if err != nil {
		return false
	}
	return time.Since(info.ModTime()) < s.syncTTL
}

func (s *RepoCacheService) writeStamp(mirrorDir string) error {
	return os.WriteFile(filepath.Join(mirrorDir, ".qa-cache-last-sync"), []byte(time.Now().Format(time.RFC3339Nano)), 0o600)
}

func (s *RepoCacheService) touchAccess(mirrorDir string) error {
	return os.WriteFile(filepath.Join(mirrorDir, ".qa-cache-last-access"), []byte(time.Now().Format(time.RFC3339Nano)), 0o600)
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

// parseRepoTreeEntries parses the default git ls-tree output:
//
//	<mode> <type> <hash>\t<path>
func parseRepoTreeEntries(out string) []RepoTreeEntry {
	var entries []RepoTreeEntry
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		tabIdx := strings.IndexByte(line, '\t')
		if tabIdx < 0 {
			continue
		}
		meta := strings.Fields(line[:tabIdx])
		if len(meta) < 3 {
			continue
		}
		entryPath := line[tabIdx+1:]
		entryType := "blob"
		if meta[1] == "tree" {
			entryType = "tree"
		}
		entries = append(entries, RepoTreeEntry{
			ID:   meta[2],
			Name: filepath.Base(entryPath),
			Type: entryType,
			Path: entryPath,
		})
	}
	return entries
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
