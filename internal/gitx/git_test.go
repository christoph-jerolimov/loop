package gitx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repo is a bare remote with one commit on main plus a working clone.
type repo struct {
	remote, clone string
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	r := &repo{remote: filepath.Join(root, "remote.git"), clone: filepath.Join(root, "clone")}
	git(t, root, "init", "-q", "--bare", r.remote)
	git(t, root, "clone", "-q", r.remote, r.clone)
	r.configure(t, r.clone)
	r.commit(t, r.clone, "README.md", "hi\n", "init")
	git(t, r.clone, "branch", "-M", "main")
	git(t, r.clone, "push", "-q", "-u", "origin", "main")
	return r
}

func (r *repo) configure(t *testing.T, dir string) {
	git(t, dir, "config", "user.email", "t@t")
	git(t, dir, "config", "user.name", "t")
}

func (r *repo) commit(t *testing.T, dir, file, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", msg)
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := Run(context.Background(), dir, args...)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return out
}

func TestRunReportsStderr(t *testing.T) {
	r := newRepo(t)
	_, err := Run(context.Background(), r.clone, "checkout", "no-such-branch")
	if err == nil || !strings.Contains(err.Error(), "git checkout no-such-branch") || !strings.Contains(err.Error(), "no-such-branch") {
		t.Errorf("err = %v", err)
	}
}

func TestEnsureBaseCloneClonesThenFetches(t *testing.T) {
	r := newRepo(t)
	ctx := context.Background()
	base := filepath.Join(t.TempDir(), "state", "base")
	if err := EnsureBaseClone(ctx, r.remote, base, "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(base, "README.md")); err != nil {
		t.Fatalf("clone missing README: %v", err)
	}
	// A new commit on the remote is picked up by the fetch on the second call.
	r.commit(t, r.clone, "b.txt", "b\n", "second")
	git(t, r.clone, "push", "-q", "origin", "main")
	if err := EnsureBaseClone(ctx, r.remote, base, "main"); err != nil {
		t.Fatal(err)
	}
	if got := git(t, base, "rev-parse", "origin/main"); got != git(t, r.clone, "rev-parse", "HEAD") {
		t.Errorf("fetch did not update origin/main")
	}
}

func TestEnsureRemote(t *testing.T) {
	r := newRepo(t)
	ctx := context.Background()
	fork := filepath.Join(t.TempDir(), "fork.git")
	git(t, "", "init", "-q", "--bare", fork)
	for i := 0; i < 2; i++ { // add, then no-op
		if err := EnsureRemote(ctx, r.clone, "fork", fork); err != nil {
			t.Fatal(err)
		}
	}
	if got := git(t, r.clone, "remote", "get-url", "fork"); got != fork {
		t.Errorf("fork url = %s", got)
	}
	other := filepath.Join(t.TempDir(), "other.git")
	if err := EnsureRemote(ctx, r.clone, "fork", other); err != nil {
		t.Fatal(err)
	}
	if got := git(t, r.clone, "remote", "get-url", "fork"); got != other {
		t.Errorf("set-url not applied: %s", got)
	}
}

func TestBranchExistsAndUniqueBranch(t *testing.T) {
	r := newRepo(t)
	ctx := context.Background()
	git(t, r.clone, "branch", "loop/local-only")
	git(t, r.clone, "push", "-q", "origin", "main:loop/remote-only")
	fork := filepath.Join(t.TempDir(), "fork.git")
	git(t, "", "init", "-q", "--bare", fork)
	if err := EnsureRemote(ctx, r.clone, "fork", fork); err != nil {
		t.Fatal(err)
	}
	git(t, r.clone, "push", "-q", "fork", "main:loop/fork-only")

	cases := []struct {
		branch  string
		remotes []string
		want    bool
	}{
		{"loop/local-only", nil, true},
		{"loop/remote-only", nil, true},
		{"loop/fork-only", nil, false},
		{"loop/fork-only", []string{"origin", "fork"}, true},
		{"loop/free", []string{"origin", "fork"}, false},
	}
	for _, c := range cases {
		got, err := BranchExists(ctx, r.clone, c.branch, c.remotes...)
		if err != nil || got != c.want {
			t.Errorf("BranchExists(%s, %v) = %v, %v; want %v", c.branch, c.remotes, got, err, c.want)
		}
	}
	if name, err := UniqueBranch(ctx, r.clone, "loop/free"); err != nil || name != "loop/free" {
		t.Errorf("UniqueBranch(free) = %s, %v", name, err)
	}
	git(t, r.clone, "branch", "loop/remote-only-2")
	if name, err := UniqueBranch(ctx, r.clone, "loop/remote-only"); err != nil || name != "loop/remote-only-3" {
		t.Errorf("UniqueBranch(remote-only) = %s, %v; want -3 (both -1 and -2 are taken)", name, err)
	}
	if _, err := BranchExists(ctx, r.clone, "x", "no-such-remote"); err == nil {
		t.Error("an unknown remote must be an error")
	}
}

func TestWorktreeLifecycle(t *testing.T) {
	r := newRepo(t)
	ctx := context.Background()
	wt := filepath.Join(t.TempDir(), "wt", "auth")
	if err := AddWorktree(ctx, r.clone, wt, "loop/auth", "main"); err != nil {
		t.Fatal(err)
	}
	if got := git(t, wt, "rev-parse", "--abbrev-ref", "HEAD"); got != "loop/auth" {
		t.Errorf("worktree branch = %s", got)
	}
	if dirty, _ := HasUncommitted(ctx, wt); dirty {
		t.Error("fresh worktree must be clean")
	}
	if err := CommitAll(ctx, wt, "nothing"); err != nil {
		t.Errorf("CommitAll on a clean tree must be a no-op: %v", err)
	}
	os.WriteFile(filepath.Join(wt, "feature.txt"), []byte("f\n"), 0o644)
	if dirty, _ := HasUncommitted(ctx, wt); !dirty {
		t.Error("new file must show as uncommitted")
	}
	r.configure(t, wt)
	if err := CommitAll(ctx, wt, "feature"); err != nil {
		t.Fatal(err)
	}
	if n, err := AheadOfBase(ctx, wt, "main"); err != nil || n != 1 {
		t.Errorf("AheadOfBase = %d, %v; want 1", n, err)
	}
	if err := Push(ctx, wt, "origin", "loop/auth"); err != nil {
		t.Fatal(err)
	}
	sha, err := HeadSHA(ctx, wt)
	if err != nil || len(sha) != 40 {
		t.Errorf("HeadSHA = %q, %v", sha, err)
	}
	if got := git(t, r.clone, "ls-remote", "--heads", "origin", "loop/auth"); !strings.HasPrefix(got, sha) {
		t.Errorf("push did not land: %s", got)
	}
	if MergeInProgress(wt) {
		t.Error("no merge in progress")
	}
	if err := RemoveWorktree(ctx, r.clone, wt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree folder still exists")
	}
	if err := RemoveWorktree(ctx, r.clone, wt); err != nil {
		t.Errorf("removing twice must be fine: %v", err)
	}
	DeleteLocalBranch(ctx, r.clone, "loop/auth")
	if exists, _ := BranchExists(ctx, r.clone, "loop/auth", "no-such-remote"); exists {
		t.Error("local branch must be gone")
	}
}

func TestCloneOnNewBranch(t *testing.T) {
	r := newRepo(t)
	dir := filepath.Join(t.TempDir(), "clones", "auth")
	if err := Clone(context.Background(), r.remote, dir, "loop/auth", "main"); err != nil {
		t.Fatal(err)
	}
	if got := git(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); got != "loop/auth" {
		t.Errorf("branch = %s", got)
	}
	if err := Clone(context.Background(), r.remote, dir, "loop/auth", "nope"); err == nil {
		t.Error("cloning a missing base branch must fail")
	}
}

func TestMergeBaseCleanAndConflict(t *testing.T) {
	r := newRepo(t)
	ctx := context.Background()
	wt := filepath.Join(t.TempDir(), "wt")
	if err := AddWorktree(ctx, r.clone, wt, "loop/auth", "main"); err != nil {
		t.Fatal(err)
	}
	r.configure(t, wt)
	r.commit(t, wt, "feature.txt", "feature\n", "feature")

	// Base moves on in another file: the merge is clean.
	r.commit(t, r.clone, "other.txt", "other\n", "other")
	git(t, r.clone, "push", "-q", "origin", "main")
	conflicts, err := MergeBase(ctx, wt, "main")
	if err != nil || conflicts != nil {
		t.Fatalf("clean merge: conflicts=%v err=%v", conflicts, err)
	}
	if _, err := os.Stat(filepath.Join(wt, "other.txt")); err != nil {
		t.Error("merge did not bring other.txt")
	}

	// Base changes the same file: conflict is reported and left in progress.
	r.commit(t, r.clone, "feature.txt", "upstream\n", "clash")
	git(t, r.clone, "push", "-q", "origin", "main")
	conflicts, err = MergeBase(ctx, wt, "main")
	if err != nil || len(conflicts) != 1 || conflicts[0] != "feature.txt" {
		t.Fatalf("conflict merge: conflicts=%v err=%v", conflicts, err)
	}
	if !MergeInProgress(wt) {
		t.Error("MERGE_HEAD must exist in the worktree's git dir")
	}
	AbortMerge(ctx, wt)
	if MergeInProgress(wt) {
		t.Error("merge must be aborted")
	}
	if dirty, _ := HasUncommitted(ctx, wt); dirty {
		t.Error("abort must restore a clean tree")
	}

	// A missing base branch is a fetch error, not a conflict.
	if _, err := MergeBase(ctx, wt, "nope"); err == nil {
		t.Error("fetching a missing base must fail")
	}
}
