package bughub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// FixWorkspaceManager turns the configured env -> repository -> branch map
// into host-owned, standalone Git repositories. The fixer is never allowed to
// use the user's currently checked-out branch as an implicit base, and every
// repository keeps its .git metadata inside the Agent-approved filesystem root.
type FixWorkspaceManager struct {
	root        string
	resolvePath RepositoryPathResolver
}

type fixWorkspaceBinding struct {
	Repo                    string
	SourcePath              string
	Worktree                string
	Remote                  string
	BaseBranch              string
	BaseCommit              string
	TargetEnvironmentBranch string
}

type FixWorkspaceLease struct {
	bindings []fixWorkspaceBinding
	root     string
}

type fixWorkspaceFilesystemRoot struct {
	Repo string
	Path string
}

func (l *FixWorkspaceLease) filesystemRoots() []fixWorkspaceFilesystemRoot {
	if l == nil {
		return nil
	}
	roots := make([]fixWorkspaceFilesystemRoot, 0, len(l.bindings))
	for _, binding := range l.bindings {
		roots = append(roots, fixWorkspaceFilesystemRoot{Repo: binding.Repo, Path: binding.Worktree})
	}
	return roots
}

type generatedEnvironmentBranchMap struct {
	Environments map[string]struct {
		Repos map[string]string `yaml:"repos"`
	} `yaml:"environments"`
}

func NewFixWorkspaceManager(root string, resolver RepositoryPathResolver) *FixWorkspaceManager {
	return &FixWorkspaceManager{root: strings.TrimSpace(root), resolvePath: resolver}
}

func (m *FixWorkspaceManager) Prepare(ctx context.Context, caseID, attemptID, environment string, bot BotRef, inputJSON []byte) (*FixWorkspaceLease, error) {
	if m == nil || strings.TrimSpace(m.root) == "" || m.resolvePath == nil {
		return nil, errors.New("fix workspace manager requires a root and repository path resolver")
	}
	targetBranches, err := loadEnvironmentBranches(bot.Path, environment)
	if err != nil {
		return nil, err
	}
	if len(targetBranches) == 0 {
		return nil, fmt.Errorf("environment %q has no repository branch mappings", environment)
	}
	sourceBaselines, err := resolveFixSourceBaselines(bot.Path, environment, inputJSON)
	if err != nil {
		return nil, err
	}
	managerRoot, err := filepath.Abs(m.root)
	if err != nil {
		return nil, fmt.Errorf("resolve fix workspace root: %w", err)
	}
	root := filepath.Join(filepath.Clean(managerRoot), safeFixWorkspaceName(attemptID))
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create fix workspace root: %w", err)
	}
	lease := &FixWorkspaceLease{root: root}
	repos := make([]string, 0, len(sourceBaselines))
	for repo := range sourceBaselines {
		repos = append(repos, repo)
	}
	sort.Strings(repos)
	for _, repo := range repos {
		sourceBranch := strings.TrimSpace(sourceBaselines[repo])
		targetBranch := strings.TrimSpace(targetBranches[repo])
		if targetBranch == "" {
			_ = lease.Close(context.Background())
			_ = os.RemoveAll(root)
			return nil, fmt.Errorf("repository %q has no target branch mapping for environment %q", repo, environment)
		}
		binding, prepareErr := m.prepareRepository(ctx, root, caseID, repo, sourceBranch, targetBranch)
		if prepareErr != nil {
			_ = lease.Close(context.Background())
			_ = os.RemoveAll(root)
			return nil, prepareErr
		}
		lease.bindings = append(lease.bindings, binding)
	}
	if len(lease.bindings) == 0 {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("environment %q has no usable repository branch mappings", environment)
	}
	return lease, nil
}

func (m *FixWorkspaceManager) prepareRepository(ctx context.Context, root, caseID, repo, sourceBranch, targetBranch string) (fixWorkspaceBinding, error) {
	path, err := m.resolvePath(ctx, caseID, repo)
	if err != nil {
		return fixWorkspaceBinding{}, fmt.Errorf("resolve repository %s: %w", repo, err)
	}
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "." || !filepath.IsAbs(path) {
		return fixWorkspaceBinding{}, fmt.Errorf("repository %s path must be absolute", repo)
	}
	if err := gitRun(ctx, path, "check-ref-format", "--branch", sourceBranch); err != nil {
		return fixWorkspaceBinding{}, fmt.Errorf("source baseline %s/%s is invalid: %w", repo, sourceBranch, err)
	}
	if err := gitRun(ctx, path, "check-ref-format", "--branch", targetBranch); err != nil {
		return fixWorkspaceBinding{}, fmt.Errorf("target environment branch %s/%s is invalid: %w", repo, targetBranch, err)
	}
	remote := "origin"
	refspec := "+refs/heads/" + sourceBranch + ":refs/remotes/" + remote + "/" + sourceBranch
	if err := gitRun(ctx, path, "fetch", "--no-tags", remote, refspec); err != nil {
		return fixWorkspaceBinding{}, fmt.Errorf("fetch source baseline %s/%s: %w", repo, sourceBranch, err)
	}
	baseCommit, err := gitOutput(ctx, path, "rev-parse", "refs/remotes/"+remote+"/"+sourceBranch+"^{commit}")
	if err != nil {
		return fixWorkspaceBinding{}, fmt.Errorf("resolve source baseline commit %s/%s: %w", repo, sourceBranch, err)
	}
	worktree := filepath.Join(root, safeFixWorkspaceName(repo))
	if _, statErr := os.Lstat(worktree); statErr == nil {
		return fixWorkspaceBinding{}, fmt.Errorf("dedicated fix workspace already exists for %s", repo)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fixWorkspaceBinding{}, fmt.Errorf("inspect dedicated fix workspace for %s: %w", repo, statErr)
	}
	fetchURL, err := gitOutput(ctx, path, "remote", "get-url", remote)
	if err != nil {
		return fixWorkspaceBinding{}, fmt.Errorf("resolve fetch remote for %s: %w", repo, err)
	}
	pushURL, pushErr := gitOutput(ctx, path, "remote", "get-url", "--push", remote)
	if pushErr != nil || strings.TrimSpace(pushURL) == "" {
		pushURL = fetchURL
	}
	// --no-local is mandatory: a normal local clone may hard-link objects or
	// use external object storage, recreating the same sandbox escape that a
	// linked worktree causes. The resulting .git directory is self-contained.
	if err := gitRun(ctx, root, "clone", "--no-checkout", "--no-local", path, worktree); err != nil {
		return fixWorkspaceBinding{}, fmt.Errorf("create standalone fix workspace for %s: %w", repo, err)
	}
	cleanupWorkspace := func() { _ = os.RemoveAll(worktree) }
	if err := gitRun(ctx, worktree, "remote", "set-url", remote, fetchURL); err != nil {
		cleanupWorkspace()
		return fixWorkspaceBinding{}, fmt.Errorf("bind fetch remote for %s: %w", repo, err)
	}
	if err := gitRun(ctx, worktree, "remote", "set-url", "--push", remote, pushURL); err != nil {
		cleanupWorkspace()
		return fixWorkspaceBinding{}, fmt.Errorf("bind push remote for %s: %w", repo, err)
	}
	if err := configureStandaloneGitIdentity(ctx, path, worktree); err != nil {
		cleanupWorkspace()
		return fixWorkspaceBinding{}, fmt.Errorf("configure standalone fix workspace for %s: %w", repo, err)
	}
	if err := gitRun(ctx, worktree, "checkout", "--detach", baseCommit); err != nil {
		cleanupWorkspace()
		return fixWorkspaceBinding{}, fmt.Errorf("lock standalone fix workspace for %s: %w", repo, err)
	}
	if err := os.Chmod(worktree, 0o700); err != nil {
		cleanupWorkspace()
		return fixWorkspaceBinding{}, fmt.Errorf("protect standalone fix workspace for %s: %w", repo, err)
	}
	return fixWorkspaceBinding{Repo: repo, SourcePath: path, Worktree: worktree, Remote: remote, BaseBranch: sourceBranch, BaseCommit: strings.TrimSpace(baseCommit), TargetEnvironmentBranch: targetBranch}, nil
}

func (l *FixWorkspaceLease) Prompt() string {
	if l == nil || len(l.bindings) == 0 {
		return ""
	}
	var out strings.Builder
	out.WriteString("\n## Studio locked fix workspaces (mandatory)\n\n")
	out.WriteString("Studio fetched and locked the user-approved development baseline for each affected repository before this Agent started. Make every code change, commit, test, and push inside the dedicated standalone repository listed below. Its Git metadata is contained inside the approved directory. The target environment branch is a separate later integration target; never use it as the fix base unless the user explicitly selected the same branch. The configured source checkout is read-only context: do not switch it, merge into it, or create the fix branch from its current HEAD.\n\n")
	for _, binding := range l.bindings {
		fmt.Fprintf(&out, "- repo: `%s`\n  dedicated_workspace: `%s`\n  source_baseline_branch: `%s`\n  locked_source_baseline_commit: `%s`\n  target_environment_branch: `%s`\n  push_remote: `%s`\n", binding.Repo, binding.Worktree, binding.BaseBranch, binding.BaseCommit, binding.TargetEnvironmentBranch, binding.Remote)
	}
	out.WriteString("\nInside each selected dedicated workspace, first verify `git rev-parse HEAD` equals `locked_source_baseline_commit`, then create the dedicated fix branch with `git switch -c <fix-branch>`. Do not merge or rebase unrelated branches into the fix branch. Report `base_branch` as `source_baseline_branch` and `target_environment_branch` exactly as bound above. Studio rejects results whose reported branches differ or whose commit is not a linear descendant of the locked source baseline.\n")
	return out.String()
}

func (l *FixWorkspaceLease) ValidateResult(ctx context.Context, result PhaseResult) error {
	if l == nil || result.Outcome != PhaseOutcomeFixPushed {
		return nil
	}
	bindings := make(map[string]fixWorkspaceBinding, len(l.bindings))
	for _, binding := range l.bindings {
		bindings[binding.Repo] = binding
	}
	if len(result.CodeChanges) == 0 {
		return errors.New("fixed result has no code changes to validate against locked source baselines")
	}
	if len(result.CodeChanges) != len(bindings) {
		return fmt.Errorf("fixed result covers %d repositories; approval locked %d source baselines", len(result.CodeChanges), len(bindings))
	}
	seen := make(map[string]struct{}, len(result.CodeChanges))
	for _, change := range result.CodeChanges {
		if _, duplicate := seen[change.Repo]; duplicate {
			return fmt.Errorf("fixed result contains duplicate repository %q", change.Repo)
		}
		seen[change.Repo] = struct{}{}
		binding, ok := bindings[change.Repo]
		if !ok {
			return fmt.Errorf("repository %s is not bound to a locked source-baseline workspace", change.Repo)
		}
		if change.BaseBranch != binding.BaseBranch {
			return fmt.Errorf("repository %s reported base %q; locked source baseline is %q", change.Repo, change.BaseBranch, binding.BaseBranch)
		}
		if change.TargetEnvironmentBranch != binding.TargetEnvironmentBranch {
			return fmt.Errorf("repository %s reported target %q; configured environment branch is %q", change.Repo, change.TargetEnvironmentBranch, binding.TargetEnvironmentBranch)
		}
		if change.PushRemote != binding.Remote {
			return fmt.Errorf("repository %s reported push remote %q; locked remote is %q", change.Repo, change.PushRemote, binding.Remote)
		}
		if err := validateLinearFixAncestry(ctx, binding.Worktree, binding.BaseCommit, change.FixCommit); err != nil {
			return fmt.Errorf("repository %s fix commit is based on the wrong source baseline history: %w", change.Repo, err)
		}
	}
	return nil
}

func parseFixSourceBaselines(inputJSON []byte) (map[string]string, error) {
	var input struct {
		SourceBaselines map[string]string `json:"source_baselines"`
	}
	if len(strings.TrimSpace(string(inputJSON))) == 0 {
		return map[string]string{}, nil
	}
	if err := json.Unmarshal(inputJSON, &input); err != nil {
		return nil, fmt.Errorf("parse fix approval input: %w", err)
	}
	result := make(map[string]string, len(input.SourceBaselines))
	for repo, branch := range input.SourceBaselines {
		repo, branch = strings.TrimSpace(repo), strings.TrimSpace(branch)
		if repo == "" {
			return nil, errors.New("source_baselines requires non-empty repository names")
		}
		if _, exists := result[repo]; exists {
			return nil, fmt.Errorf("source_baselines contains duplicate repository %q", repo)
		}
		result[repo] = branch
	}
	return result, nil
}

// remediationFixRepositories returns the repositories the approved plan will
// actually modify. New results provide the structured list directly. For
// durable results created before that field existed, match the remediation
// target against the evidence-backed call chain and conservatively fall back
// to its repository scope.
func remediationFixRepositories(result InvestigationResult) []string {
	unique := func(values []string) []string {
		seen := make(map[string]struct{}, len(values))
		out := make([]string, 0, len(values))
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			if _, exists := seen[value]; exists {
				continue
			}
			seen[value] = struct{}{}
			out = append(out, value)
		}
		return out
	}
	if repositories := unique(result.Remediation.Repositories); len(repositories) > 0 {
		return repositories
	}
	callChainRepositories := make([]string, 0, len(result.CallChain))
	for _, hop := range result.CallChain {
		callChainRepositories = append(callChainRepositories, hop.Repo)
	}
	callChainRepositories = unique(callChainRepositories)
	target := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(result.Remediation.Target), "\\", "/"))
	matched := make([]string, 0, len(callChainRepositories))
	for _, repo := range callChainRepositories {
		if target != "" && strings.Contains(target, strings.ToLower(repo)) {
			matched = append(matched, repo)
		}
	}
	if len(matched) > 0 {
		return matched
	}
	return callChainRepositories
}

// RemediationFixRepositories exposes the host-owned repository scope selected
// by an investigation result without exposing repository paths or allowing a
// UI caller to expand that scope.
func RemediationFixRepositories(result InvestigationResult) []string {
	return remediationFixRepositories(result)
}

func resolveRemediationFixSourceBaselines(botPath, environment string, inputJSON []byte, result InvestigationResult) (map[string]string, error) {
	repositories := remediationFixRepositories(result)
	if len(repositories) == 0 {
		return resolveFixSourceBaselines(botPath, environment, inputJSON)
	}
	requested, err := parseFixSourceBaselines(inputJSON)
	if err != nil {
		return nil, err
	}
	expected := make(map[string]struct{}, len(repositories))
	for _, repo := range repositories {
		expected[repo] = struct{}{}
	}
	if len(requested) == 0 {
		requested = make(map[string]string, len(repositories))
		for _, repo := range repositories {
			requested[repo] = ""
		}
	} else {
		if len(requested) != len(expected) {
			return nil, errors.New("source_baselines must match remediation repositories")
		}
		for repo := range requested {
			if _, ok := expected[repo]; !ok {
				return nil, fmt.Errorf("repository %q is outside the approved remediation scope", repo)
			}
		}
	}
	targets, err := loadEnvironmentBranches(botPath, environment)
	if err != nil {
		return nil, fmt.Errorf("validate remediation repository scope: %w", err)
	}
	for _, repo := range repositories {
		if strings.TrimSpace(targets[repo]) == "" {
			return nil, fmt.Errorf("remediation repository %q is not configured for environment %q", repo, environment)
		}
	}
	canonicalInput, err := withFixSourceBaselines(inputJSON, requested)
	if err != nil {
		return nil, err
	}
	return resolveFixSourceBaselines(botPath, environment, canonicalInput)
}

// resolveFixSourceBaselines applies the product default at the trusted host
// boundary. A blank branch means "use this repository's branch for the Case
// environment". An entirely absent mapping selects every repository mapped to
// that environment, which keeps CLI/API callers safe when they do not provide
// the desktop dialog's narrower affected-repository scope.
func resolveFixSourceBaselines(botPath, environment string, inputJSON []byte) (map[string]string, error) {
	requested, err := parseFixSourceBaselines(inputJSON)
	if err != nil {
		return nil, err
	}
	needsDefaults := len(requested) == 0
	for _, branch := range requested {
		needsDefaults = needsDefaults || strings.TrimSpace(branch) == ""
	}
	if !needsDefaults {
		return requested, nil
	}
	targets, err := loadEnvironmentBranches(botPath, environment)
	if err != nil {
		return nil, fmt.Errorf("resolve default fix baselines: %w", err)
	}
	if len(requested) == 0 {
		requested = make(map[string]string, len(targets))
		for repo, branch := range targets {
			requested[strings.TrimSpace(repo)] = strings.TrimSpace(branch)
		}
		return requested, nil
	}
	for repo, branch := range requested {
		if strings.TrimSpace(branch) != "" {
			continue
		}
		target := strings.TrimSpace(targets[repo])
		if target == "" {
			return nil, fmt.Errorf("repository %q has no target branch mapping for environment %q", repo, environment)
		}
		requested[repo] = target
	}
	return requested, nil
}

func withFixSourceBaselines(inputJSON []byte, sourceBaselines map[string]string) (json.RawMessage, error) {
	input := map[string]any{}
	if len(strings.TrimSpace(string(inputJSON))) != 0 {
		if err := json.Unmarshal(inputJSON, &input); err != nil {
			return nil, fmt.Errorf("parse fix approval input: %w", err)
		}
	}
	input["source_baselines"] = sourceBaselines
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("encode resolved fix approval input: %w", err)
	}
	return encoded, nil
}

func validateLinearFixAncestry(ctx context.Context, repoPath, baseCommit, fixCommit string) error {
	baseCommit = strings.TrimSpace(baseCommit)
	fixCommit = strings.TrimSpace(fixCommit)
	if baseCommit == "" || fixCommit == "" {
		return errors.New("base and fix commits are required")
	}
	if err := gitRun(ctx, repoPath, "cat-file", "-e", fixCommit+"^{commit}"); err != nil {
		return fmt.Errorf("fix commit %s is unavailable: %w", fixCommit, err)
	}
	mergeBase, err := gitOutput(ctx, repoPath, "merge-base", baseCommit, fixCommit)
	if err != nil || strings.TrimSpace(mergeBase) != baseCommit {
		return fmt.Errorf("locked base %s is not the merge base of fix commit %s", baseCommit, fixCommit)
	}
	history, err := gitOutput(ctx, repoPath, "rev-list", "--parents", baseCommit+".."+fixCommit)
	if err != nil {
		return fmt.Errorf("inspect fix history: %w", err)
	}
	if strings.TrimSpace(history) == "" {
		return errors.New("fix commit does not contain any change after the locked environment base")
	}
	for _, line := range strings.Split(strings.TrimSpace(history), "\n") {
		if fields := strings.Fields(line); len(fields) != 2 {
			return fmt.Errorf("fix history contains a merge or disconnected commit: %s", line)
		}
	}
	return nil
}

func (l *FixWorkspaceLease) Close(ctx context.Context) error {
	if l == nil {
		return nil
	}
	var errs []error
	for index := len(l.bindings) - 1; index >= 0; index-- {
		binding := l.bindings[index]
		if err := removeStandaloneFixWorkspace(l.root, binding.Worktree); err != nil {
			errs = append(errs, fmt.Errorf("remove fix workspace for %s: %w", binding.Repo, err))
		}
	}
	if l.root != "" {
		if err := os.Remove(l.root); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove fix workspace root: %w", err))
		}
	}
	return errors.Join(errs...)
}

func removeStandaloneFixWorkspace(root, workspace string) error {
	root = filepath.Clean(strings.TrimSpace(root))
	workspace = filepath.Clean(strings.TrimSpace(workspace))
	if root == "." || workspace == "." || !filepath.IsAbs(root) || !filepath.IsAbs(workspace) {
		return errors.New("standalone fix workspace paths must be absolute")
	}
	relative, err := filepath.Rel(root, workspace)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.Dir(relative) != "." {
		return errors.New("refusing to remove a path outside the fix workspace root")
	}
	return os.RemoveAll(workspace)
}

func loadEnvironmentBranches(botPath, environment string) (map[string]string, error) {
	path, err := findBotReferenceFile(botPath, "env-branch-map.yaml")
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read environment branch map: %w", err)
	}
	var document generatedEnvironmentBranchMap
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("parse environment branch map: %w", err)
	}
	env, ok := document.Environments[strings.TrimSpace(environment)]
	if !ok {
		return nil, fmt.Errorf("environment %q is absent from env-branch-map.yaml", environment)
	}
	return env.Repos, nil
}

func findBotReferenceFile(botPath, name string) (string, error) {
	botPath = filepath.Clean(strings.TrimSpace(botPath))
	if botPath == "." {
		return "", errors.New("bot workspace path is required")
	}
	if info, err := os.Stat(botPath); err == nil && !info.IsDir() {
		botPath = filepath.Dir(botPath)
	}
	candidates := []string{
		filepath.Join(botPath, "skills", "routing", "references", name),
		filepath.Join(botPath, "routing", "references", name),
		filepath.Join(filepath.Dir(botPath), "skills", "routing", "references", name),
		filepath.Join(filepath.Dir(botPath), "routing", "references", name),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s is unavailable under bot workspace %s", name, botPath)
}

func safeFixWorkspaceName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	var out strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			out.WriteRune(r)
		} else {
			out.WriteByte('-')
		}
	}
	cleaned := strings.Trim(out.String(), ".-")
	if cleaned == "" {
		return "unknown"
	}
	return cleaned
}
