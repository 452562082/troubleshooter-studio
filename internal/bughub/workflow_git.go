package bughub

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

var (
	ErrMergeApprovalStale = errors.New("merge approval target HEAD is stale")
	ErrGitWorktreeDirty   = errors.New("repository worktree is dirty")
	ErrGitDetachedHEAD    = errors.New("repository is on a detached HEAD")
	ErrGitRemoteNotSSH    = errors.New("push remote is not SSH")
	ErrGitMergeConflict   = errors.New("git merge would conflict")
)

type RepositoryPathResolver func(context.Context, string, string) (string, error)

// GitIntegrationService performs workflow Git writes only in worktrees below
// worktreeRoot. The configured source repository is inspected and used as the
// object store, but is never checked out, reset, cleaned, or merged in place.
type GitIntegrationService struct {
	worktreeRoot string
	resolvePath  RepositoryPathResolver
	mu           sync.Mutex
}

func NewGitIntegrationService(worktreeRoot string, resolver RepositoryPathResolver) *GitIntegrationService {
	return &GitIntegrationService{worktreeRoot: worktreeRoot, resolvePath: resolver}
}

func MergeApprovalKey(caseID, repo, fixCommit, targetBranch, targetHead string) string {
	return fmt.Sprintf("merge:%s:%s:%s:%s:%s", caseID, repo, fixCommit, targetBranch, targetHead)
}

func (s *GitIntegrationService) Inspect(ctx context.Context, req MergeRequest) (MergeInspection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inspectLocked(ctx, req)
}

func (s *GitIntegrationService) inspectLocked(ctx context.Context, req MergeRequest) (MergeInspection, error) {
	inspection := MergeInspection{MergeCommits: map[string]string{}, Repositories: map[string]MergeRepositoryResult{}}
	changes, err := normalizedMergeChanges(req)
	if err != nil {
		return inspection, err
	}
	for _, change := range changes {
		result, inspectErr := s.inspectRepo(ctx, req.CaseID, change)
		inspection.Repositories[change.Repo] = result
		if result.MergeCommit != "" {
			inspection.MergeCommits[change.Repo] = result.MergeCommit
		}
		if result.Conflict {
			inspection.Conflict = true
		}
		if result.Pushed {
			inspection.MergePushed = true
		}
		if inspectErr != nil {
			return inspection, fmt.Errorf("inspect repository %s: %w", change.Repo, inspectErr)
		}
	}
	inspection.FixPushed = len(changes) > 0
	return inspection, nil
}

func (s *GitIntegrationService) MergeAndPush(ctx context.Context, req MergeRequest) (MergeResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := MergeResult{MergeCommits: map[string]string{}, Repositories: map[string]MergeRepositoryResult{}}
	changes, err := normalizedMergeChanges(req)
	if err != nil {
		return result, err
	}
	for _, change := range changes {
		repoResult, mergeErr := s.mergeRepo(ctx, req, change)
		result.Repositories[change.Repo] = repoResult
		if repoResult.MergeCommit != "" {
			result.MergeCommits[change.Repo] = repoResult.MergeCommit
		}
		if repoResult.Conflict {
			result.Conflict = true
		}
		if mergeErr != nil {
			result.ErrorMessage = mergeErr.Error()
			return result, fmt.Errorf("merge repository %s: %w", change.Repo, mergeErr)
		}
	}
	result.Pushed = len(result.Repositories) == len(changes)
	for _, repoResult := range result.Repositories {
		result.Pushed = result.Pushed && repoResult.Pushed
	}
	return result, nil
}

func (s *GitIntegrationService) ResumePush(ctx context.Context, req MergeRequest) (MergeResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := MergeResult{MergeCommits: map[string]string{}, Repositories: map[string]MergeRepositoryResult{}}
	changes, err := normalizedMergeChanges(req)
	if err != nil {
		return result, err
	}
	for _, change := range changes {
		if strings.TrimSpace(change.MergeCommit) == "" {
			return result, fmt.Errorf("repository %s has no recorded local merge commit", change.Repo)
		}
		path, remote, err := s.validateRepository(ctx, req.CaseID, change)
		if err != nil {
			return result, err
		}
		if err := gitRun(ctx, path, "cat-file", "-e", change.MergeCommit+"^{commit}"); err != nil {
			return result, err
		}
		remoteHead, err := fetchTargetHead(ctx, path, remote, change.TargetEnvironmentBranch)
		if err != nil {
			return result, err
		}
		if ancestor, _ := gitAncestor(ctx, path, change.MergeCommit, remoteHead); ancestor {
			result.Repositories[change.Repo] = MergeRepositoryResult{MergeCommit: change.MergeCommit, TargetHead: remoteHead, Pushed: true}
			result.MergeCommits[change.Repo] = change.MergeCommit
			continue
		}
		if approvedHead := strings.TrimSpace(req.TargetHeads[change.Repo]); approvedHead == "" || approvedHead != remoteHead {
			repoResult := MergeRepositoryResult{MergeCommit: change.MergeCommit, TargetHead: remoteHead, Error: ErrMergeApprovalStale.Error()}
			result.Repositories[change.Repo] = repoResult
			result.MergeCommits[change.Repo] = change.MergeCommit
			return result, fmt.Errorf("repository %s: %w", change.Repo, ErrMergeApprovalStale)
		}
		if err := gitRun(ctx, path, "push", remote, change.MergeCommit+":refs/heads/"+change.TargetEnvironmentBranch); err != nil {
			return result, fmt.Errorf("push repository %s: %w", change.Repo, err)
		}
		result.Repositories[change.Repo] = MergeRepositoryResult{MergeCommit: change.MergeCommit, TargetHead: remoteHead, Pushed: true}
		result.MergeCommits[change.Repo] = change.MergeCommit
	}
	result.Pushed = len(result.Repositories) == len(changes)
	return result, nil
}

func (s *GitIntegrationService) InspectFix(ctx context.Context, req FixInspectionRequest) (FixInspection, error) {
	result := FixInspection{Complete: true, Changes: make([]CodeChange, len(req.Changes))}
	for i, change := range req.Changes {
		path, _, err := s.validateRepository(ctx, req.CaseID, change)
		if err != nil {
			result.Complete = false
			result.ErrorMessage = err.Error()
			return result, err
		}
		if err := gitRun(ctx, path, "cat-file", "-e", change.FixCommit+"^{commit}"); err != nil {
			result.Complete = false
			result.ErrorMessage = err.Error()
			return result, err
		}
		result.Changes[i] = change.Clone()
	}
	return result, nil
}

func (s *GitIntegrationService) inspectRepo(ctx context.Context, caseID string, change CodeChange) (MergeRepositoryResult, error) {
	path, remote, err := s.validateRepository(ctx, caseID, change)
	if err != nil {
		return MergeRepositoryResult{Error: err.Error()}, err
	}
	targetHead, err := fetchTargetHead(ctx, path, remote, change.TargetEnvironmentBranch)
	if err != nil {
		return MergeRepositoryResult{Error: err.Error()}, err
	}
	result := MergeRepositoryResult{TargetHead: targetHead, ApprovalKey: MergeApprovalKey(caseID, change.Repo, change.FixCommit, change.TargetEnvironmentBranch, targetHead)}
	if err := gitRun(ctx, path, "cat-file", "-e", change.FixCommit+"^{commit}"); err != nil {
		result.Error = err.Error()
		return result, err
	}
	if ancestor, ancestorErr := gitAncestor(ctx, path, change.FixCommit, targetHead); ancestorErr != nil {
		result.Error = ancestorErr.Error()
		return result, ancestorErr
	} else if ancestor {
		result.MergeCommit, result.Pushed = targetHead, true
		return result, nil
	}
	fixHead, err := fetchFixHead(ctx, path, remote, change.FixBranch)
	if err != nil {
		result.Error = err.Error()
		return result, err
	}
	if fixHead != change.FixCommit {
		result.Error = "remote fix branch does not point at the approved commit"
		return result, errors.New(result.Error)
	}
	cmd := exec.CommandContext(ctx, "git", "merge-tree", "--write-tree", targetHead, change.FixCommit)
	cmd.Dir = path
	cmd.Env = gitEnvironment()
	output, mergeErr := cmd.CombinedOutput()
	if mergeErr != nil {
		result.Conflict = true
		result.Error = strings.TrimSpace(string(output))
		return result, nil
	}
	return result, nil
}

func (s *GitIntegrationService) mergeRepo(ctx context.Context, req MergeRequest, change CodeChange) (MergeRepositoryResult, error) {
	inspection, err := s.inspectRepo(ctx, req.CaseID, change)
	if err != nil {
		return inspection, err
	}
	if inspection.Conflict {
		return inspection, ErrGitMergeConflict
	}
	// A previous call may have pushed successfully while its result was lost.
	// Observing the exact fix commit on the remote target is authoritative and
	// makes replay safe even though the remote HEAD necessarily moved.
	if inspection.Pushed {
		return inspection, nil
	}
	approvedHead := strings.TrimSpace(req.TargetHeads[change.Repo])
	if approvedHead == "" || approvedHead != inspection.TargetHead {
		inspection.Error = ErrMergeApprovalStale.Error()
		return inspection, ErrMergeApprovalStale
	}
	path, _, err := s.validateRepository(ctx, req.CaseID, change)
	if err != nil {
		return inspection, err
	}
	worktree := s.worktreePath(req.CaseID, change.Repo, inspection.TargetHead, change.FixCommit)
	if err := prepareStudioWorktreeRoot(s.worktreeRoot); err != nil {
		return inspection, err
	}
	if info, statErr := os.Lstat(worktree); errors.Is(statErr, os.ErrNotExist) {
		if err := gitRun(ctx, path, "worktree", "add", "--detach", worktree, inspection.TargetHead); err != nil {
			return inspection, err
		}
	} else if statErr != nil {
		return inspection, statErr
	} else if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return inspection, errors.New("Studio worktree path is not a real directory")
	} else if _, gitErr := os.Lstat(filepath.Join(worktree, ".git")); gitErr != nil {
		return inspection, errors.New("existing Studio worktree is not registered with Git")
	}
	if err := gitRun(ctx, worktree, "config", "user.name", "Troubleshooter Studio"); err != nil {
		return inspection, err
	}
	if err := gitRun(ctx, worktree, "config", "user.email", "studio@localhost"); err != nil {
		return inspection, err
	}
	if err := gitRun(ctx, worktree, "merge", "--no-edit", change.FixCommit); err != nil {
		inspection.Conflict = true
		inspection.Error = err.Error()
		return inspection, ErrGitMergeConflict
	}
	mergeCommit, err := gitOutput(ctx, worktree, "rev-parse", "HEAD")
	if err != nil {
		return inspection, err
	}
	remote := normalizedRemote(change.PushRemote)
	if strings.HasPrefix(remote, "-") || strings.ContainsAny(remote, " \t\r\n") {
		return inspection, errors.New("push remote name is invalid")
	}
	if err := gitRun(ctx, worktree, "push", remote, mergeCommit+":refs/heads/"+change.TargetEnvironmentBranch); err != nil {
		inspection.MergeCommit = mergeCommit
		inspection.Error = err.Error()
		return inspection, err
	}
	inspection.MergeCommit, inspection.Pushed = mergeCommit, true
	return inspection, nil
}

func (s *GitIntegrationService) validateRepository(ctx context.Context, caseID string, change CodeChange) (string, string, error) {
	if s.resolvePath == nil {
		return "", "", errors.New("repository path resolver is unavailable")
	}
	path, err := s.resolvePath(ctx, caseID, change.Repo)
	if err != nil {
		return "", "", err
	}
	path, err = filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return "", "", err
	}
	status, err := gitOutput(ctx, path, "status", "--porcelain")
	if err != nil {
		return "", "", err
	}
	if status != "" {
		return "", "", ErrGitWorktreeDirty
	}
	branch, err := gitOutput(ctx, path, "symbolic-ref", "--short", "HEAD")
	if err != nil || branch == "" {
		return "", "", ErrGitDetachedHEAD
	}
	remote := normalizedRemote(change.PushRemote)
	if strings.HasPrefix(remote, "-") || strings.ContainsAny(remote, " \t\r\n") {
		return "", "", errors.New("push remote name is invalid")
	}
	url, err := gitOutput(ctx, path, "config", "--get", "remote."+remote+".url")
	if err != nil {
		return "", "", err
	}
	if !isSSHRemote(url) {
		return "", "", ErrGitRemoteNotSSH
	}
	return path, remote, nil
}

func normalizedMergeChanges(req MergeRequest) ([]CodeChange, error) {
	byRepo := make(map[string]CodeChange, len(req.Changes))
	for _, change := range req.Changes {
		byRepo[change.Repo] = change.Clone()
	}
	changes := make([]CodeChange, 0, len(req.FixCommits))
	for repo, commit := range req.FixCommits {
		change, ok := byRepo[repo]
		if !ok {
			change = CodeChange{Repo: repo}
		}
		change.FixCommit = commit
		change.TargetEnvironmentBranch = req.TargetBranches[repo]
		if strings.TrimSpace(change.Repo) == "" || strings.TrimSpace(change.FixCommit) == "" || strings.TrimSpace(change.TargetEnvironmentBranch) == "" {
			return nil, errors.New("merge request repository, fix commit, and target branch are required")
		}
		if !isFullGitObjectID(change.FixCommit) {
			return nil, fmt.Errorf("repository %s fix commit must be a full hexadecimal object ID", repo)
		}
		if strings.TrimSpace(change.FixBranch) == "" {
			return nil, fmt.Errorf("repository %s fix branch is required", repo)
		}
		changes = append(changes, change)
	}
	if len(changes) == 0 || len(req.FixCommits) != len(req.TargetBranches) {
		return nil, errors.New("merge request scopes must cover the same non-empty repositories")
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Repo < changes[j].Repo })
	return changes, nil
}

func fetchTargetHead(ctx context.Context, path, remote, branch string) (string, error) {
	if err := gitRun(ctx, path, "check-ref-format", "--branch", branch); err != nil {
		return "", err
	}
	ref := "refs/remotes/" + remote + "/" + branch
	if err := gitRun(ctx, path, "fetch", "--no-tags", remote, "+refs/heads/"+branch+":"+ref); err != nil {
		return "", err
	}
	return gitOutput(ctx, path, "rev-parse", ref)
}

func fetchFixHead(ctx context.Context, path, remote, branch string) (string, error) {
	if err := gitRun(ctx, path, "check-ref-format", "--branch", branch); err != nil {
		return "", err
	}
	ref := "refs/remotes/" + remote + "/" + branch
	if err := gitRun(ctx, path, "fetch", "--no-tags", remote, "+refs/heads/"+branch+":"+ref); err != nil {
		return "", err
	}
	return gitOutput(ctx, path, "rev-parse", ref)
}

func gitAncestor(ctx context.Context, path, ancestor, descendant string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", ancestor, descendant)
	cmd.Dir = path
	cmd.Env = gitEnvironment()
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

func gitRun(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnvironment()
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnvironment()
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func gitEnvironment() []string {
	return append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_MERGE_AUTOEDIT=no")
}
func normalizedRemote(remote string) string {
	if strings.TrimSpace(remote) == "" {
		return "origin"
	}
	return strings.TrimSpace(remote)
}
func isFullGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, ch := range value {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
			return false
		}
	}
	return true
}
func isSSHRemote(url string) bool {
	url = strings.TrimSpace(url)
	return strings.HasPrefix(url, "ssh://") || (!strings.Contains(url, "://") && strings.Contains(url, "@") && strings.Contains(url, ":"))
}
func (s *GitIntegrationService) worktreePath(caseID, repo, targetHead, fixCommit string) string {
	sum := sha256.Sum256([]byte(caseID + "\x00" + repo + "\x00" + targetHead + "\x00" + fixCommit))
	return filepath.Join(s.worktreeRoot, fmt.Sprintf("%x", sum[:12]))
}

func prepareStudioWorktreeRoot(root string) error {
	if strings.TrimSpace(root) == "" {
		return errors.New("Studio worktree root is required")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("Studio worktree root must be a real directory")
	}
	return os.Chmod(root, 0o700)
}
