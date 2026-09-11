// Package gitx wraps the git command line for clones, worktrees, branches
// and pushes.
package gitx

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Run executes git in dir and returns trimmed stdout.
func Run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return strings.TrimSpace(stdout.String()), fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// EnsureBaseClone clones url into dir once and fetches afterwards.
func EnsureBaseClone(ctx context.Context, url, dir, base string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		_, err := Run(ctx, dir, "fetch", "--prune", "origin")
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	_, err := Run(ctx, "", "clone", "--branch", base, url, dir)
	return err
}

// BranchExists reports whether the branch exists locally or on origin.
func BranchExists(ctx context.Context, repo, branch string) (bool, error) {
	if out, err := Run(ctx, repo, "branch", "--list", branch); err != nil {
		return false, err
	} else if out != "" {
		return true, nil
	}
	out, err := Run(ctx, repo, "ls-remote", "--heads", "origin", branch)
	if err != nil {
		return false, err
	}
	return out != "", nil
}

// UniqueBranch appends -2, -3, ... until the name is free.
func UniqueBranch(ctx context.Context, repo, want string) (string, error) {
	name := want
	for i := 2; i < 100; i++ {
		exists, err := BranchExists(ctx, repo, name)
		if err != nil {
			return "", err
		}
		if !exists {
			return name, nil
		}
		name = fmt.Sprintf("%s-%d", want, i)
	}
	return "", fmt.Errorf("could not find a free branch name for %s", want)
}

// AddWorktree creates a new branch from origin/base in a fresh worktree.
func AddWorktree(ctx context.Context, repo, path, branch, base string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	_, err := Run(ctx, repo, "worktree", "add", "-b", branch, path, "origin/"+base)
	return err
}

// RemoveWorktree unregisters and deletes a worktree.
func RemoveWorktree(ctx context.Context, repo, path string) error {
	if _, err := os.Stat(path); err == nil {
		if _, err := Run(ctx, repo, "worktree", "remove", "--force", path); err != nil {
			_ = os.RemoveAll(path)
		}
	}
	_, _ = Run(ctx, repo, "worktree", "prune")
	return nil
}

// Clone creates a standalone clone on a new branch from base.
func Clone(ctx context.Context, url, path, branch, base string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if _, err := Run(ctx, "", "clone", "--branch", base, url, path); err != nil {
		return err
	}
	_, err := Run(ctx, path, "checkout", "-b", branch)
	return err
}

// HasUncommitted reports whether the worktree has changes.
func HasUncommitted(ctx context.Context, dir string) (bool, error) {
	out, err := Run(ctx, dir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return out != "", nil
}

// CommitAll stages everything and commits. A clean tree is not an error.
func CommitAll(ctx context.Context, dir, message string) error {
	dirty, err := HasUncommitted(ctx, dir)
	if err != nil || !dirty {
		return err
	}
	if _, err := Run(ctx, dir, "add", "-A"); err != nil {
		return err
	}
	_, err = Run(ctx, dir, "commit", "-m", message)
	return err
}

// AheadOfBase returns the number of commits on HEAD that are not on origin/base.
func AheadOfBase(ctx context.Context, dir, base string) (int, error) {
	if _, err := Run(ctx, dir, "fetch", "origin", base); err != nil {
		return 0, err
	}
	out, err := Run(ctx, dir, "rev-list", "--count", "origin/"+base+"..HEAD")
	if err != nil {
		return 0, err
	}
	var n int
	_, err = fmt.Sscan(out, &n)
	return n, err
}

// Push pushes the branch and sets upstream.
func Push(ctx context.Context, dir, branch string) error {
	_, err := Run(ctx, dir, "push", "-u", "origin", branch)
	return err
}

// HeadSHA returns the current commit.
func HeadSHA(ctx context.Context, dir string) (string, error) {
	return Run(ctx, dir, "rev-parse", "HEAD")
}

// MergeBase merges origin/base into the branch. On conflict it returns
// the conflicting files and leaves the merge in progress.
func MergeBase(ctx context.Context, dir, base string) (conflicts []string, err error) {
	if _, err := Run(ctx, dir, "fetch", "origin", base); err != nil {
		return nil, err
	}
	if _, err := Run(ctx, dir, "merge", "--no-edit", "origin/"+base); err == nil {
		return nil, nil
	}
	out, lerr := Run(ctx, dir, "diff", "--name-only", "--diff-filter=U")
	if lerr != nil {
		return nil, lerr
	}
	if out == "" {
		return nil, err
	}
	return strings.Split(out, "\n"), nil
}

// AbortMerge cancels an in-progress merge.
func AbortMerge(ctx context.Context, dir string) {
	_, _ = Run(ctx, dir, "merge", "--abort")
}

// MergeInProgress reports whether MERGE_HEAD exists.
func MergeInProgress(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git", "MERGE_HEAD"))
	if err == nil {
		return true
	}
	// worktrees keep MERGE_HEAD in the git dir of the worktree
	out, err := Run(context.Background(), dir, "rev-parse", "--git-path", "MERGE_HEAD")
	if err != nil {
		return false
	}
	if !filepath.IsAbs(out) {
		out = filepath.Join(dir, out)
	}
	_, err = os.Stat(out)
	return err == nil
}

// DeleteLocalBranch removes a branch from the base clone.
func DeleteLocalBranch(ctx context.Context, repo, branch string) {
	_, _ = Run(ctx, repo, "branch", "-D", branch)
}
