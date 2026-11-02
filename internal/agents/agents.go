// Package agents discovers local agent sessions (Claude, Codex, Antigravity).
package agents

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Status int

const (
	Exited Status = iota
	Idle
	Busy
)

func (s Status) String() string {
	switch s {
	case Idle:
		return "idle"
	case Busy:
		return "busy"
	}
	return "exited"
}

type Session struct {
	Agent   string // "claude", "codex", "agy"
	ID      string
	Name    string
	Cwd     string
	Updated time.Time
	Status  Status
	PID     int    // live agent process, 0 if exited
	TTY     string // e.g. "ttys023", empty if exited
	Files   []string
	// Resume is the shell command that reopens the session.
	Resume string
	// MaybeLive: an unidentified agent process runs in Cwd and could be this session.
	MaybeLive bool
	// Dead: the agent can no longer resume it (codex thread whose rollout file is gone).
	Dead bool
	// Headless: started by a program, not a person: `claude -p` (entrypoint
	// sdk-cli) or `codex exec`/`codex review` (source exec). Eligible for auto-delete.
	Headless bool

	historyDB bool // codex: turns live in thread_history sqlite (paginated mode)
	titled    bool // claude: transcript has a custom or AI title
}

// Key identifies a session across refreshes.
func (s Session) Key() string { return s.Agent + ":" + s.ID }

// Home is overridable for tests.
var Home, _ = os.UserHomeDir()

// List returns every session from every agent, newest first. One agent failing
// does not hide the others: their sessions are returned along with the error.
func List() ([]Session, error) {
	var all []Session
	var errs []error
	for _, f := range []func() ([]Session, error){listClaude, listCodex, listAgy} {
		ss, err := f()
		if err != nil {
			errs = append(errs, err)
		}
		all = append(all, ss...)
	}
	if err := flushInteractive(); err != nil {
		errs = append(errs, err)
	}
	if err := applyKeepList(all); err != nil {
		errs = append(errs, err)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Updated.After(all[j].Updated) })
	return all, errors.Join(errs...)
}

func interactiveFile() string { return filepath.Join(Home, ".muxlair", "interactive.json") }

// WithSessionLock runs fn holding muxlair's cross-process lock. Marking a
// session interactive and the final check-then-delete of auto-delete both take
// it, so another muxlair can't resume a session between those two steps.
func WithSessionLock(fn func() error) error {
	dir := filepath.Dir(interactiveFile())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "interactive.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return fn()
}

// ClaimForResume is called before resuming a headless session: under the
// session lock it checks the session still exists (an auto-delete in another
// muxlair may have won) and marks it interactive, so it is never auto-deleted.
func ClaimForResume(s Session) error {
	return WithSessionLock(func() error {
		if _, err := Recheck(s); err != nil {
			return err
		}
		return markInteractive(s.Key())
	})
}

// MarkInteractive records that the user resumed a headless session themselves,
// so it is never auto-deleted.
func MarkInteractive(key string) error {
	return WithSessionLock(func() error { return markInteractive(key) })
}

// markInteractive needs the session lock held. It publishes with a rename, so
// readers never see a half-written file.
func markInteractive(key string) error {
	keys, err := interactiveKeys()
	if err != nil {
		return err
	}
	if keys[key] {
		return nil
	}
	keys[key] = true
	b, err := json.Marshal(keys)
	if err != nil {
		return err
	}
	dir := filepath.Dir(interactiveFile())
	tmp, err := os.CreateTemp(dir, "interactive-*.json")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), interactiveFile())
}

var (
	notedMu sync.Mutex
	noted   = map[string]bool{}
)

// noteInteractive queues key for the keep-list. Listing can run under the
// session lock (Recheck), where writing would deadlock; List writes the queue
// once it is done, and callers clear Headless in memory meanwhile.
func noteInteractive(key string) {
	notedMu.Lock()
	noted[key] = true
	notedMu.Unlock()
}

func flushInteractive() error {
	notedMu.Lock()
	keys := make([]string, 0, len(noted))
	for k := range noted {
		keys = append(keys, k)
	}
	notedMu.Unlock()
	for _, k := range keys {
		if err := MarkInteractive(k); err != nil {
			return err // stays queued for the next List
		}
		notedMu.Lock()
		delete(noted, k)
		notedMu.Unlock()
	}
	return nil
}

// interactiveKeys reads the keep-list; a missing file is an empty list.
func interactiveKeys() (map[string]bool, error) {
	keys := map[string]bool{}
	b, err := os.ReadFile(interactiveFile())
	if os.IsNotExist(err) {
		return keys, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &keys); err != nil {
		return nil, fmt.Errorf("%s: %w", interactiveFile(), err)
	}
	return keys, nil
}

// applyKeepList clears Headless for kept sessions. If the list can't be read,
// nothing counts as headless, so auto-delete fails closed.
func applyKeepList(ss []Session) error {
	keep, err := interactiveKeys()
	notedMu.Lock()
	defer notedMu.Unlock()
	for i := range ss {
		// noted: observed but not yet written (writes wait until no lock is held).
		if err != nil || keep[ss[i].Key()] || noted[ss[i].Key()] {
			ss[i].Headless = false
		}
	}
	return err
}

// Recheck re-reads one session from its own agent only (cheaper than List),
// for a last check right before acting on it.
func Recheck(s Session) (Session, error) {
	list := map[string]func() ([]Session, error){"claude": listClaude, "codex": listCodex, "agy": listAgy}[s.Agent]
	if list == nil {
		return s, errors.New("unknown agent " + s.Agent)
	}
	ss, err := list()
	if err != nil {
		return s, err
	}
	for _, f := range ss {
		if f.Key() == s.Key() {
			one := []Session{f}
			if err := applyKeepList(one); err != nil {
				return one[0], err
			}
			return one[0], nil
		}
	}
	return s, errors.New("session no longer exists")
}

// WatchPaths are the files/dirs whose changes should trigger a refresh.
func WatchPaths() []string {
	paths := []string{
		filepath.Join(Home, ".claude", "sessions"),
		CodexHome(),
		filepath.Join(Home, ".claude", "projects"), // new project dirs get watched as they appear
	}
	dirs, _ := filepath.Glob(filepath.Join(Home, ".claude", "projects", "*"))
	return append(paths, dirs...)
}

func alive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

type procStat struct {
	tty     string // "" if none
	started time.Time
}

// procStats returns tty and start time for each of our pids that is running.
// A pid missing from the result could not be inspected.
func procStats(pids []int) map[int]procStat {
	res := map[int]procStat{}
	if len(pids) == 0 {
		return res
	}
	procs, err := ownProcs()
	if err != nil {
		return res
	}
	want := map[int]bool{}
	for _, p := range pids {
		want[p] = true
	}
	for _, p := range procs {
		if want[p.pid] {
			res[p.pid] = procStat{p.tty, p.started}
		}
	}
	return res
}

// proc is one of the current user's processes.
type proc struct {
	pid     int
	tty     string // "ttys023", "" if none
	started time.Time
	exe     string // executable basename
}

// parsePS parses `ps -o lstart=,tty=` output, e.g. "Fri Oct 30 12:08:29 2026  ttys029".
func parsePS(out string) (tty string, started time.Time, ok bool) {
	f := strings.Fields(out)
	if len(f) != 6 {
		return "", time.Time{}, false
	}
	started, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", strings.Join(f[:5], " "), time.Local)
	if err != nil {
		return "", time.Time{}, false
	}
	if tty = f[5]; tty == "??" {
		tty = ""
	}
	return tty, started, true
}

// shellQuote quotes s for POSIX sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
