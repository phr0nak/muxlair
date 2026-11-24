package agents

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// CodexHome is $CODEX_HOME or ~/.codex, matching what the codex CLI (used for resume and rename) reads.
func CodexHome() string {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return h
	}
	return filepath.Join(Home, ".codex")
}

func codexDB() string { return filepath.Join(CodexHome(), "state_5.sqlite") }

func listCodex() ([]Session, error) {
	if _, err := os.Stat(codexDB()); err != nil {
		return nil, nil // codex not installed
	}
	db, err := sql.Open("sqlite", "file:"+codexDB()+"?mode=ro&_pragma=busy_timeout(2000)")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	// Hide subagent threads (source is a JSON object): each is a helper of a
	// listed thread, e.g. the reviewer spawned by `codex review`. Non-interactive
	// `codex exec`/`codex review` runs are listed.
	rows, err := db.Query(`SELECT id, rollout_path, cwd,
		COALESCE(NULLIF(name,''), NULLIF(title,''), first_user_message), updated_at_ms, history_mode, source
		FROM threads WHERE archived = 0 AND source NOT LIKE '{%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := codexIndexNames()
	var out []Session
	for rows.Next() {
		var s Session
		var rollout string
		var updated sql.NullInt64
		var mode, source string
		if err := rows.Scan(&s.ID, &rollout, &s.Cwd, &s.Name, &updated, &mode, &source); err != nil {
			return nil, err
		}
		s.Agent = "codex"
		s.historyDB = mode == "paginated"
		if n := names[s.ID]; n != "" {
			s.Name = n
		}
		s.Name = oneLine(firstNonEmpty(s.Name, s.ID[:min(8, len(s.ID))])) // full; the UI truncates
		s.Updated = time.UnixMilli(updated.Int64)
		if st, err := os.Stat(rollout); err == nil {
			if st.ModTime().After(s.Updated) {
				s.Updated = st.ModTime()
			}
			s.Files = []string{rollout}
		} else {
			// Codex can't resume it ("no rollout found for thread id"); still listed, so the user
			// knows it exists (`codex delete <id>` removes it).
			s.Dead = true
		}
		s.Resume = "cd " + shellQuote(s.Cwd) + " && codex resume " + shellQuote(s.ID)
		if h := os.Getenv("CODEX_HOME"); h != "" {
			// New tmux windows inherit the server's env, not ours; pin the home we listed from.
			s.Resume = "cd " + shellQuote(s.Cwd) + " && CODEX_HOME=" + shellQuote(h) + " codex resume " + shellQuote(s.ID)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	attachCodexProcs(out)
	return out, nil
}

// codexIndexNames reads $CODEX_HOME/session_index.jsonl; the last entry per id wins.
// It is appended on rename, so it can be fresher than the sqlite row.
func codexIndexNames() map[string]string {
	names := map[string]string{}
	f, err := os.Open(filepath.Join(CodexHome(), "session_index.jsonl"))
	if err != nil {
		return names
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var r struct {
			ID         string `json:"id"`
			ThreadName string `json:"thread_name"`
		}
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.ID != "" {
			names[r.ID] = r.ThreadName
		}
	}
	return names
}

type codexProc struct {
	pid      int
	tty      string
	cwd      string
	rollouts []string // open rollout files; an app-server can host several sessions
	unknown  bool     // lsof failed, so we can't tell which session this is
	resumeID string   // id from `codex resume <id>`, if any
}

// attachCodexProcs marks sessions that have a live codex process. Only hard
// evidence counts (open rollout file or resume id): a codex sitting in its
// resume picker must not mark some saved session as running.
func attachCodexProcs(ss []Session) {
	procs, err := codexProcs()
	if err != nil {
		// Can't see processes: assume anything may be live rather than open a second copy.
		for i := range ss {
			ss[i].MaybeLive = true
		}
		return
	}
	byFile, byID := map[string]int{}, map[string]int{}
	for i, s := range ss {
		if len(s.Files) > 0 {
			byFile[s.Files[0]] = i
		}
		byID[s.ID] = i
	}
	for _, p := range procs {
		matched := false
		for _, r := range p.rollouts {
			if i, ok := byFile[r]; ok {
				markCodexLive(&ss[i], p)
				matched = true
			}
		}
		// argv can be stale after an in-app /resume, so it only counts without rollout evidence.
		// It also doesn't rule out that the process moved on to another session.
		if i, ok := byID[p.resumeID]; ok && p.resumeID != "" && !matched {
			markCodexLive(&ss[i], p)
		}
		// Only an unidentified terminal codex (resume picker, sqlite-only history) blocks
		// sessions in its folder. TTY-less ones (app-server daemon, IDE) are shared
		// background processes whose cwd says nothing about which session they serve.
		// lsof failing is the exception: then nothing is known, so fail closed.
		if !matched && (p.tty != "" || p.unknown) {
			// Block opening sessions in its folder, or of all sessions if lsof failed.
			for i := range ss {
				if (p.unknown || ss[i].Cwd == p.cwd) && ss[i].PID == 0 {
					ss[i].MaybeLive = true
				}
			}
		}
	}
}

func markCodexLive(s *Session, p codexProc) {
	s.PID, s.TTY = p.pid, p.tty
	s.MaybeLive = false // identified now; an earlier unidentified process may have flagged it
	s.Status = Idle
	// Paginated threads record turns in sqlite; any rollout they have may be a stale legacy file.
	if (s.historyDB && codexHistoryBusy(s.ID)) || (!s.historyDB && len(s.Files) > 0 && codexBusy(s.Files[0])) {
		s.Status = Busy
	}
}

// codexHistoryBusy checks the latest turn of a sqlite-only (paginated history) session.
func codexHistoryBusy(id string) bool {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(CodexHome(), "thread_history_1.sqlite")+"?mode=ro&_pragma=busy_timeout(2000)")
	if err != nil {
		return false
	}
	defer db.Close()
	var status string
	db.QueryRow(`SELECT status FROM thread_turns WHERE thread_id = ? ORDER BY rollout_ordinal DESC LIMIT 1`, id).Scan(&status)
	return status == "inProgress"
}

// codexProcs finds codex processes; tty is "" for ones without a terminal (e.g. `codex exec`).
func codexProcs() ([]codexProc, error) {
	// Own processes only: other users' codex can't be inspected and would mark everything MaybeLive.
	all, err := ownProcs()
	if err != nil {
		return nil, err
	}
	var procs []codexProc
	var pids []string
	for _, p := range all {
		if p.exe == "codex" {
			procs = append(procs, codexProc{pid: p.pid, tty: p.tty})
			pids = append(pids, strconv.Itoa(p.pid))
		}
	}
	if len(procs) == 0 {
		return nil, nil
	}
	files := lsofCodex(strings.Join(pids, ",")) // one lsof for all codex processes
	kept := procs[:0]
	for _, p := range procs {
		f, ok := files[p.pid]
		if !ok && !alive(p.pid) {
			// Exited between listing and lsof (codex delete, app-server calls,
			// reviews): gone, not unknown, so it must not block every session.
			continue
		}
		p.resumeID = resumeID(procArgs(p.pid))
		p.cwd, p.rollouts, p.unknown = f.cwd, f.rollouts, !ok
		kept = append(kept, p)
	}
	return kept, nil
}

// resumeID returns the session argument of `codex resume [flags] <id>`.
// shortcut: assumes flags before the id take no value; `-c k=v` style options would be misread.
func resumeID(args []string) string {
	for i, a := range args {
		if a != "resume" {
			continue
		}
		for _, b := range args[i+1:] {
			if !strings.HasPrefix(b, "-") {
				return b
			}
		}
	}
	return ""
}

type procFiles struct {
	cwd      string
	rollouts []string
}

// lsofCodex returns the cwd and open rollout files of each listed pid
// ("1,2,3"). A pid missing from the result could not be inspected.
func lsofCodex(pids string) map[int]procFiles {
	// lsof exits non-zero when any pid has gone, yet still prints the rest.
	// -b -n -P -w: skip blocking kernel calls and name lookups (3x faster).
	out, _ := exec.Command("lsof", "-b", "-n", "-P", "-w", "-p", pids, "-Fpfn").Output()
	return parseLsof(string(out), filepath.Join(CodexHome(), "sessions")+"/")
}

func parseLsof(out, sessions string) map[int]procFiles {
	res := map[int]procFiles{}
	pid := 0
	var fd string
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(line[1:])
			res[pid] = procFiles{}
		case 'f':
			fd = line[1:]
		case 'n':
			pf := res[pid]
			name := line[1:]
			if fd == "cwd" {
				pf.cwd = name
			} else if strings.HasPrefix(name, sessions) && strings.HasSuffix(name, ".jsonl") {
				pf.rollouts = append(pf.rollouts, name)
			}
			res[pid] = pf
		}
	}
	return res
}

// codexBusy reports whether the last task event in a rollout is an unfinished task.
func codexBusy(rollout string) bool {
	f, err := os.Open(rollout)
	if err != nil {
		return false
	}
	defer f.Close()
	const tail = 256 << 10
	if st, err := f.Stat(); err == nil && st.Size() > tail {
		f.Seek(st.Size()-tail, io.SeekStart)
	}
	b, _ := io.ReadAll(f)
	busy, found := lastTaskStarted(b)
	if !found {
		// No marker in the tail means a turn long enough to push task_started out of it.
		st, err := f.Stat()
		return err == nil && st.Size() > tail
	}
	return busy
}

// lastTaskStarted reports whether the last task marker in b is a start, and whether any marker exists.
func lastTaskStarted(b []byte) (busy, found bool) {
	start := bytes.LastIndex(b, []byte(`"task_started"`))
	end := max(bytes.LastIndex(b, []byte(`"task_complete"`)), bytes.LastIndex(b, []byte(`"turn_aborted"`)))
	return start > end, start >= 0 || end >= 0
}
