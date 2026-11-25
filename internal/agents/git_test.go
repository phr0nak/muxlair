package agents

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A real repository with a linked worktree, a detached checkout, a non-git
// folder and a removed one.
func TestFolderGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	os.MkdirAll(filepath.Join(repo, "sub"), 0o700)
	run(repo, "init", "-q", "-b", "main")
	run(repo, "commit", "-q", "--allow-empty", "-m", "one")
	wt := filepath.Join(root, "wt")
	run(repo, "worktree", "add", "-q", "-b", "feat/login", wt)

	if g := FolderGit(filepath.Join(repo, "sub")); !g.Repo || g.Branch != "main" || g.Worktree {
		t.Fatalf("main checkout, from a subfolder: %+v", g)
	}
	g := FolderGit(wt)
	if !g.Repo || g.Branch != "feat/login" || !g.Worktree {
		t.Fatalf("worktree: %+v", g)
	}
	if main, _ := filepath.EvalSymlinks(g.Main); main != mustEval(t, repo) {
		t.Fatalf("worktree main checkout: %q, want %q", g.Main, repo)
	}
	// A branch switch is seen on the next read (the cache follows HEAD).
	run(repo, "checkout", "-q", "-b", "other")
	if g := FolderGit(repo); g.Branch != "other" {
		t.Fatalf("after checkout: %+v", g)
	}
	sha := run(repo, "rev-parse", "HEAD")
	run(repo, "checkout", "-q", "--detach")
	if g := FolderGit(repo); g.Branch != "detached@"+sha[:7] {
		t.Fatalf("detached: %+v", g)
	}
	plain := filepath.Join(root, "plain")
	os.MkdirAll(plain, 0o700)
	if g := FolderGit(plain); g.Repo || g.Missing || g.Branch != "" {
		t.Fatalf("not git: %+v", g)
	}
	if g := FolderGit(filepath.Join(root, "gone")); !g.Missing || g.Repo {
		t.Fatalf("missing: %+v", g)
	}
	// A subfolder of a checkout, read once and then deleted, shows as gone.
	os.RemoveAll(filepath.Join(repo, "sub"))
	if g := FolderGit(filepath.Join(repo, "sub")); !g.Missing || g.Repo {
		t.Fatalf("deleted subfolder: %+v", g)
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	e, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestBranchLabel(t *testing.T) {
	for _, c := range []struct {
		s    Session
		want string
		cur  bool
	}{
		{Session{Branch: "feat/x", Git: GitInfo{Repo: true, Branch: "main"}}, "feat/x", false},
		{Session{Git: GitInfo{Repo: true, Branch: "main"}}, "main", true},
		{Session{Branch: "feat/x", Git: GitInfo{Repo: true, Worktree: true}}, "↳ feat/x", false},
		{Session{}, "—", true},
	} {
		if got := c.s.BranchLabel(); got != c.want {
			t.Errorf("%+v: label %q, want %q", c.s, got, c.want)
		}
		if _, cur := c.s.ShownBranch(); cur != c.cur {
			t.Errorf("%+v: current %v, want %v", c.s, cur, c.cur)
		}
	}
}

func TestJSONStringField(t *testing.T) {
	line := []byte(`{"type":"user","gitBranch":"feat/a\"b","cwd":"/p"}`)
	if v, ok := jsonStringField(line, `"gitBranch":`); !ok || v != `feat/a"b` {
		t.Fatalf("%q %v", v, ok)
	}
	if _, ok := jsonStringField([]byte(`{"type":"user"}`), `"gitBranch":`); ok {
		t.Fatal("found a missing field")
	}
}
