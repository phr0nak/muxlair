package agents

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GitInfo is what a session's folder looks like to git right now. muxlair reads
// git's own files (.git, HEAD, commondir) instead of running git, since this
// runs for every folder on every refresh.
type GitInfo struct {
	Repo     bool   // the folder is inside a git checkout
	Missing  bool   // the folder no longer exists
	Branch   string // current branch, or "detached@<short sha>"
	Worktree bool   // a linked worktree (git worktree add), not the main checkout
	Main     string // for a worktree: the main checkout's folder
}

type gitCacheEntry struct {
	info  GitInfo
	stamp string    // what HEAD and .git looked like when read
	at    time.Time // when read; non-git results are probed again after a while
}

var (
	gitCacheMu sync.Mutex
	gitCache   = map[string]gitCacheEntry{}
)

// gitReprobe is how long a "not a git folder" answer is trusted, so a
// folder that becomes a checkout shows up without a restart.
const gitReprobe = 30 * time.Second

// FolderGit returns the git state of dir, cached until its HEAD changes.
func FolderGit(dir string) GitInfo {
	if dir == "" {
		return GitInfo{}
	}
	gitCacheMu.Lock()
	c, ok := gitCache[dir]
	gitCacheMu.Unlock()
	// A deleted subfolder of a checkout would keep the checkout's stamp: check it first.
	if _, err := os.Stat(dir); err != nil {
		ok = false
	}
	if ok {
		if c.stamp == "" && time.Since(c.at) < gitReprobe {
			return c.info
		}
		if c.stamp != "" && c.stamp == gitStamp(dir, c.info) {
			return c.info
		}
	}
	info := readGit(dir)
	gitCacheMu.Lock()
	gitCache[dir] = gitCacheEntry{info, gitStamp(dir, info), time.Now()}
	gitCacheMu.Unlock()
	return info
}

// gitStamp fingerprints the files a checkout's answer depends on; "" when dir
// is not a checkout (or is gone).
func gitStamp(dir string, info GitInfo) string {
	if !info.Repo {
		return ""
	}
	dotgit, gitdir := findGit(dir)
	if dotgit == "" {
		return "gone"
	}
	stamp := dotgit
	for _, p := range []string{dotgit, filepath.Join(gitdir, "HEAD")} {
		if st, err := os.Stat(p); err == nil {
			stamp += "|" + st.ModTime().String() + ":" + strconv.FormatInt(st.Size(), 10)
		}
	}
	return stamp
}

// findGit walks up from dir to the nearest .git. It returns that .git path and
// the git directory holding HEAD: .git itself for a main checkout, or the
// directory a linked worktree's .git file points to.
func findGit(dir string) (dotgit, gitdir string) {
	for d := dir; ; {
		p := filepath.Join(d, ".git")
		if st, err := os.Stat(p); err == nil {
			if st.IsDir() {
				return p, p
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return "", ""
			}
			target, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
			if !ok {
				return "", ""
			}
			target = strings.TrimSpace(target)
			if !filepath.IsAbs(target) {
				target = filepath.Join(d, target) // relative to the folder holding .git
			}
			return p, filepath.Clean(target)
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", ""
		}
		d = parent
	}
}

func readGit(dir string) GitInfo {
	if _, err := os.Stat(dir); err != nil {
		return GitInfo{Missing: true}
	}
	dotgit, gitdir := findGit(dir)
	if dotgit == "" {
		return GitInfo{}
	}
	info := GitInfo{Repo: true}
	head, err := os.ReadFile(filepath.Join(gitdir, "HEAD"))
	if err != nil {
		return GitInfo{} // a broken .git: treat as not a checkout
	}
	h := strings.TrimSpace(string(head))
	if ref, ok := strings.CutPrefix(h, "ref: "); ok {
		info.Branch = strings.TrimPrefix(ref, "refs/heads/")
	} else if len(h) >= 7 {
		info.Branch = "detached@" + h[:7]
	}
	// A linked worktree's git directory has a commondir file pointing to the
	// main repository's .git; the main checkout is the folder holding that.
	if gitdir != dotgit {
		if b, err := os.ReadFile(filepath.Join(gitdir, "commondir")); err == nil {
			common := strings.TrimSpace(string(b))
			if !filepath.IsAbs(common) {
				common = filepath.Join(gitdir, common)
			}
			info.Worktree = true
			info.Main = filepath.Dir(filepath.Clean(common))
		}
	}
	return info
}
