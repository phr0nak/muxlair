// Package agents discovers local agent sessions (Claude, Codex, OpenCode).
package agents

import (
	"errors"
	"os"
	"sort"
	"strings"
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
	Agent   string // Agent.Name(): "claude", "codex", "opencode"
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

	historyDB bool // codex: turns live in thread_history sqlite (paginated mode)
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
	for _, a := range registry {
		ss, err := a.List()
		if err != nil {
			errs = append(errs, err)
		}
		all = append(all, ss...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Updated.After(all[j].Updated) })
	return all, errors.Join(errs...)
}

// WatchPaths are the files/dirs whose changes should trigger a refresh, for every agent.
func WatchPaths() []string {
	var paths []string
	for _, a := range registry {
		paths = append(paths, a.WatchPaths()...)
	}
	return paths
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
