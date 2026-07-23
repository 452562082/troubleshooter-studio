package bughub

import (
	"bytes"
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
	ErrMergeApprovalStale       = errors.New("merge approval target HEAD is stale")
	ErrGitDetachedHEAD          = errors.New("repository is on a detached HEAD")
	ErrGitRemoteNotSSH          = errors.New("push remote is not SSH")
	ErrGitMergeConflict         = errors.New("git merge would conflict")
	ErrStudioWorktree           = errors.New("Studio dedicated worktree is invalid")
	ErrFixRemoteMismatch        = errors.New("remote fix branch does not match the checkpoint")
	ErrFixInspectionUnavailable = errors.New("remote fix inspection is temporarily unavailable")
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
			if cleanupErr := s.cleanupStudioWorktree(ctx, path, s.worktreePath(req.CaseID, change.Repo, req.TargetHeads[change.Repo], change.FixCommit)); cleanupErr != nil {
				return result, cleanupErr
			}
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
		if cleanupErr := s.cleanupStudioWorktree(ctx, path, s.worktreePath(req.CaseID, change.Repo, req.TargetHeads[change.Repo], change.FixCommit)); cleanupErr != nil {
			return result, cleanupErr
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
		if strings.TrimSpace(change.Repo) == "" || strings.TrimSpace(change.FixBranch) == "" || !isFullGitObjectID(change.FixCommit) {
			result.Complete = false
			result.ErrorMessage = "fix checkpoint repository, branch, and full commit are required"
			return result, fmt.Errorf("%w: %s", ErrFixRemoteMismatch, result.ErrorMessage)
		}
		path, remote, err := s.validateRepository(ctx, req.CaseID, change)
		if err != nil {
			result.Complete = false
			result.ErrorMessage = err.Error()
			return result, fmt.Errorf("%w: %v", ErrFixInspectionUnavailable, err)
		}
		remoteHead, err := remoteExactBranchHead(ctx, path, remote, change.FixBranch)
		if err != nil {
			result.Complete = false
			result.ErrorMessage = err.Error()
			return result, err
		}
		if remoteHead != change.FixCommit {
			result.Complete = false
			result.ErrorMessage = "remote fix branch does not point at the checkpoint commit"
			return result, fmt.Errorf("%w: %s", ErrFixRemoteMismatch, result.ErrorMessage)
		}
		fetchedHead, err := fetchFixHead(ctx, path, remote, change.FixBranch)
		if err != nil {
			result.Complete = false
			result.ErrorMessage = err.Error()
			if _, confirmErr := remoteExactBranchHead(ctx, path, remote, change.FixBranch); errors.Is(confirmErr, ErrFixRemoteMismatch) {
				return result, confirmErr
			}
			return result, fmt.Errorf("%w: fetch exact fix ref: %v", ErrFixInspectionUnavailable, err)
		}
		if fetchedHead != remoteHead || fetchedHead != change.FixCommit {
			result.Complete = false
			result.ErrorMessage = "fetched fix branch changed during inspection"
			return result, fmt.Errorf("%w: %s", ErrFixRemoteMismatch, result.ErrorMessage)
		}
		// Fetch is the source of truth and materializes the exact remote object in
		// a freshly cloned or garbage-collected local repository. Only validate
		// object type after the remote ref has been fetched and matched.
		if err := gitRun(ctx, path, "cat-file", "-e", remoteHead+"^{commit}"); err != nil {
			result.Complete = false
			result.ErrorMessage = err.Error()
			return result, fmt.Errorf("%w: remote fix ref is not a commit: %v", ErrFixRemoteMismatch, err)
		}
		result.Changes[i] = change.Clone()
	}
	return result, nil
}

func remoteExactBranchHead(ctx context.Context, path, remote, branch string) (string, error) {
	if err := gitRun(ctx, path, "check-ref-format", "--branch", branch); err != nil {
		return "", fmt.Errorf("%w: invalid fix branch", ErrFixRemoteMismatch)
	}
	ref := "refs/heads/" + branch
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--exit-code", remote, ref)
	cmd.Dir = path
	cmd.Env = gitEnvironment()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 2 && len(bytes.TrimSpace(stdout.Bytes())) == 0 {
			return "", fmt.Errorf("%w: remote fix branch is absent", ErrFixRemoteMismatch)
		}
		return "", fmt.Errorf("%w: git ls-remote exit failed", ErrFixInspectionUnavailable)
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 1 {
		return "", fmt.Errorf("%w: exact remote ref response is ambiguous", ErrFixInspectionUnavailable)
	}
	fields := strings.Fields(lines[0])
	if len(fields) != 2 || fields[1] != ref || !isFullGitObjectID(fields[0]) {
		return "", fmt.Errorf("%w: exact remote ref response is invalid", ErrFixInspectionUnavailable)
	}
	return fields[0], nil
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
	approvedHead := strings.TrimSpace(change.MergeBaseHead)
	// MergeRequest.TargetHeads is projected into MergeBaseHead by
	// normalizedMergeChanges. A target advance invalidates and removes the old
	// recoverable worktree; a matching scope may recover a merge whose process
	// result was lost before CaseStore persistence.
	if approvedHead != "" && approvedHead != targetHead {
		if cleanupErr := s.cleanupStudioWorktree(ctx, path, s.worktreePath(caseID, change.Repo, approvedHead, change.FixCommit)); cleanupErr != nil {
			result.Error = cleanupErr.Error()
			return result, cleanupErr
		}
	} else if approvedHead == targetHead {
		mergeCommit, found, recoverErr := s.inspectStudioWorktree(ctx, path, caseID, change, approvedHead)
		if recoverErr != nil {
			result.Error = recoverErr.Error()
			return result, recoverErr
		}
		if found {
			result.MergeCommit = mergeCommit
			return result, nil
		}
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
		path, _, pathErr := s.validateRepository(ctx, req.CaseID, change)
		if pathErr != nil {
			return inspection, pathErr
		}
		if cleanupErr := s.cleanupStudioWorktree(ctx, path, s.worktreePath(req.CaseID, change.Repo, strings.TrimSpace(req.TargetHeads[change.Repo]), change.FixCommit)); cleanupErr != nil {
			return inspection, cleanupErr
		}
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
		if err := os.Chmod(worktree, 0o700); err != nil {
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
	if cleanupErr := s.cleanupStudioWorktree(ctx, path, worktree); cleanupErr != nil {
		inspection.Error = cleanupErr.Error()
		return inspection, cleanupErr
	}
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
	// The configured checkout is only the Git object store and remote source.
	// All merges happen in a Studio-owned dedicated worktree, so user edits and
	// untracked files in this checkout are unrelated and must remain untouched.
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
		change.MergeBaseHead = req.TargetHeads[repo]
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

func (s *GitIntegrationService) inspectStudioWorktree(ctx context.Context, source, caseID string, change CodeChange, targetHead string) (string, bool, error) {
	worktree := s.worktreePath(caseID, change.Repo, targetHead, change.FixCommit)
	info, err := os.Lstat(worktree)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	invalid := func(cause error) (string, bool, error) {
		cleanupErr := s.cleanupStudioWorktree(ctx, source, worktree)
		return "", false, errors.Join(fmt.Errorf("%w: %v", ErrStudioWorktree, cause), cleanupErr)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return invalid(errors.New("path is not a real directory"))
	}
	if info.Mode().Perm() != 0o700 {
		return invalid(fmt.Errorf("directory mode is %o, want 700", info.Mode().Perm()))
	}
	registered, commonMatch, identityErr := worktreeIdentity(ctx, source, worktree)
	if identityErr != nil || !registered || !commonMatch {
		return invalid(errors.New("worktree is not registered to the configured repository"))
	}
	if detached, detachedErr := gitDetachedHEAD(ctx, worktree); detachedErr != nil || !detached {
		return invalid(errors.New("worktree HEAD is attached to a branch"))
	}
	status, statusErr := gitOutput(ctx, worktree, "status", "--porcelain")
	if statusErr != nil || status != "" {
		return invalid(errors.New("worktree is dirty"))
	}
	head, headErr := gitOutput(ctx, worktree, "rev-parse", "HEAD")
	if headErr != nil {
		return invalid(headErr)
	}
	if head == targetHead {
		if cleanupErr := s.cleanupStudioWorktree(ctx, source, worktree); cleanupErr != nil {
			return "", false, cleanupErr
		}
		return "", false, nil
	}
	expectedTree, treeErr := mergeTreeObject(ctx, source, targetHead, change.FixCommit)
	if treeErr != nil {
		return invalid(treeErr)
	}
	headTree, treeErr := gitOutput(ctx, worktree, "rev-parse", head+"^{tree}")
	if treeErr != nil || headTree != expectedTree {
		return invalid(errors.New("HEAD tree does not match the approved merge"))
	}
	if fastForward, ancestorErr := gitAncestor(ctx, source, targetHead, change.FixCommit); ancestorErr == nil && fastForward {
		if head != change.FixCommit {
			return invalid(errors.New("fast-forward HEAD is not the exact fix commit"))
		}
		return head, true, nil
	}
	parents, parentErr := gitOutput(ctx, worktree, "rev-list", "--parents", "-n", "1", head)
	fields := strings.Fields(parents)
	if parentErr != nil || len(fields) != 3 || fields[0] != head || fields[1] != targetHead || fields[2] != change.FixCommit {
		return invalid(errors.New("merge commit parents do not match target HEAD and exact fix commit"))
	}
	return head, true, nil
}

func worktreeIdentity(ctx context.Context, source, worktree string) (bool, bool, error) {
	sourceCommon, err := resolvedGitPath(ctx, source, "--git-common-dir")
	if err != nil {
		return false, false, err
	}
	worktreeCommon, err := resolvedGitPath(ctx, worktree, "--git-common-dir")
	if err != nil {
		return false, false, err
	}
	listing, err := gitOutput(ctx, source, "worktree", "list", "--porcelain")
	if err != nil {
		return false, false, err
	}
	want, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		return false, false, err
	}
	registered := false
	for _, line := range strings.Split(listing, "\n") {
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		candidate, resolveErr := filepath.EvalSymlinks(strings.TrimPrefix(line, "worktree "))
		if resolveErr == nil && candidate == want {
			registered = true
			break
		}
	}
	return registered, sourceCommon == worktreeCommon, nil
}

func gitDetachedHEAD(ctx context.Context, dir string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "symbolic-ref", "-q", "HEAD")
	cmd.Dir = dir
	cmd.Env = gitEnvironment()
	err := cmd.Run()
	if err == nil {
		return false, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return true, nil
	}
	return false, err
}

func resolvedGitPath(ctx context.Context, dir, arg string) (string, error) {
	value, err := gitOutput(ctx, dir, "rev-parse", arg)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(dir, value)
	}
	return filepath.EvalSymlinks(filepath.Clean(value))
}

func mergeTreeObject(ctx context.Context, source, targetHead, fixCommit string) (string, error) {
	output, err := gitOutput(ctx, source, "merge-tree", "--write-tree", targetHead, fixCommit)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(output)
	if len(fields) == 0 || !isFullGitObjectID(fields[0]) {
		return "", errors.New("merge-tree did not return a tree object")
	}
	return fields[0], nil
}

func (s *GitIntegrationService) cleanupStudioWorktree(ctx context.Context, source, worktree string) error {
	if strings.TrimSpace(worktree) == "" {
		return nil
	}
	rootAbs, err := filepath.Abs(s.worktreeRoot)
	if err != nil {
		return err
	}
	pathAbs, err := filepath.Abs(worktree)
	if err != nil {
		return err
	}
	if filepath.Dir(pathAbs) != filepath.Clean(rootAbs) {
		return errors.New("refusing to clean a path outside the Studio worktree root")
	}
	if _, statErr := os.Lstat(pathAbs); errors.Is(statErr, os.ErrNotExist) {
		registered, listErr := worktreePathRegistered(ctx, source, pathAbs)
		if listErr != nil {
			return listErr
		}
		if registered {
			return errors.New("registered Studio worktree path is missing; refusing global metadata cleanup")
		}
		return nil
	} else if statErr != nil {
		return statErr
	}
	registered, commonMatch, identityErr := worktreeIdentity(ctx, source, pathAbs)
	if identityErr != nil || !registered || !commonMatch {
		return errors.Join(errors.New("refusing to remove worktree without exact Studio ownership"), identityErr)
	}
	return gitRun(ctx, source, "worktree", "remove", "--force", pathAbs)
}

func worktreePathRegistered(ctx context.Context, source, want string) (bool, error) {
	listing, err := gitOutput(ctx, source, "worktree", "list", "--porcelain")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(listing, "\n") {
		if strings.HasPrefix(line, "worktree ") && filepath.Clean(strings.TrimPrefix(line, "worktree ")) == filepath.Clean(want) {
			return true, nil
		}
	}
	return false, nil
}
