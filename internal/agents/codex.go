package agents

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

// CodexHome is $CODEX_HOME or ~/.codex, matching what the codex CLI (used for resume/delete) reads.
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
		s.Headless = source == "exec"
		if n := names[s.ID]; n != "" {
			s.Name = n
		}
		s.Name = oneLine(firstNonEmpty(s.Name, Unnamed)) // full; the UI truncates
		s.Updated = time.UnixMilli(updated.Int64)
		if st, err := os.Stat(rollout); err == nil {
			if st.ModTime().After(s.Updated) {
				s.Updated = st.ModTime()
			}
			s.Files = []string{rollout}
		} else {
			// Codex can't resume it ("no rollout found for thread id"); still listed so it can be deleted.
			s.Dead = true
		}
		if s.Headless && len(s.Files) > 0 && rolloutInteractive(s.Files[0]) {
			s.Headless = false
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
	// Paginated threads keep turns in sqlite; a resume there may not touch the
	// rollout, so their turn history must also look untouched.
	var paged []string
	for _, s := range out {
		if s.Headless && s.historyDB {
			paged = append(paged, s.ID)
		}
	}
	untouched, err := codexUntouched(paged)
	for i := range out {
		if out[i].Headless && out[i].historyDB && (err != nil || !untouched[out[i].ID]) {
			out[i].Headless = false // err: can't tell, so keep it
		}
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
	headless bool     // started as `codex exec` / `codex review`
}

// attachCodexProcs marks sessions that have a live codex process. Only hard
// evidence counts (open rollout file or resume id): a codex sitting in its
// resume picker must not mark some saved session as running.
func attachCodexProcs(ss []Session) {
	procs, err := codexProcs()
	if err != nil {
		// Can't see processes: assume anything may be live rather than allow deletes.
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
			// Block open/delete of sessions in its folder, or of all sessions if lsof failed.
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
	if s.Headless && p.tty != "" && !p.headless {
		// Someone has it open in a terminal: an interactive resume. Remember it,
		// since nothing in the thread may show that once they exit.
		noteInteractive(s.Key())
		s.Headless = false
	}
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
		args := procArgs(p.pid)
		p.resumeID, p.headless = resumeID(args), headlessArgs(args)
		p.cwd, p.rollouts, p.unknown = f.cwd, f.rollouts, !ok
		kept = append(kept, p)
	}
	return kept, nil
}

// headlessArgs reports whether argv is a non-interactive codex invocation
// (`codex exec`, `codex e`, `codex review`), which can run attached to a terminal.
func headlessArgs(args []string) bool {
	for _, a := range args[min(1, len(args)):] {
		switch a {
		case "exec", "e", "review":
			return true
		}
	}
	return false
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

// exportCodexThread writes every sqlite row belonging to thread id as JSON lines,
// so `codex delete` can't destroy the only copy of a history-backed session.
func exportCodexThread(path, id string) error {
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	enc := json.NewEncoder(out)
	for _, src := range []struct{ db, col string }{
		{"state_5.sqlite", "id"},
		{"state_5.sqlite", "thread_id"}, // attachments, dynamic tools: cascade-deleted with the thread
		{"thread_history_1.sqlite", "thread_id"},
	} {
		dbPath := filepath.Join(CodexHome(), src.db)
		if _, err := os.Stat(dbPath); err != nil {
			continue
		}
		db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=busy_timeout(2000)")
		if err != nil {
			return err
		}
		err = exportRows(db, src.db, src.col, id, enc)
		db.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func exportRows(db *sql.DB, dbName, col, id string, enc *json.Encoder) error {
	// Tables keyed by thread: `threads` in state, any table with thread_id in history.
	tables, err := db.Query(`SELECT m.name FROM sqlite_master m JOIN pragma_table_info(m.name) p
		WHERE m.type = 'table' AND p.name = ?`, col)
	if err != nil {
		return err
	}
	var names []string
	for tables.Next() {
		var n string
		tables.Scan(&n)
		if col == "id" && n != "threads" {
			continue // many state tables have an unrelated id column
		}
		names = append(names, n)
	}
	tables.Close()
	for _, t := range names {
		rows, err := db.Query(`SELECT * FROM "`+t+`" WHERE "`+col+`" = ?`, id)
		if err != nil {
			return err
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				return err
			}
			row := map[string]any{}
			for i, c := range cols {
				if b, ok := vals[i].([]byte); ok {
					if utf8.Valid(b) {
						vals[i] = string(b)
					} else {
						vals[i] = map[string]string{"base64": base64.StdEncoding.EncodeToString(b)}
					}
				}
				row[c] = vals[i]
			}
			if err := enc.Encode(map[string]any{"db": dbName, "table": t, "row": row}); err != nil {
				rows.Close()
				return err
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	return nil
}

// codexHelpersToDelete returns every hidden helper below thread id, deepest
// first, so deleting in order never strands a descendant. A helper the user
// resumed themselves is kept with its whole subtree, like any interactive session.
func codexHelpersToDelete(id string) ([]string, error) {
	keep, err := interactiveKeys()
	if err != nil {
		return nil, err
	}
	var out []string
	seen := map[string]bool{id: true}
	var walk func(parent string) error
	walk = func(parent string) error {
		kids, err := codexChildren(parent)
		if err != nil {
			return err
		}
		untouched, err := codexUntouched(kids)
		if err != nil {
			return err
		}
		for _, c := range kids {
			if seen[c] {
				continue
			}
			seen[c] = true
			if r := codexRollout(c); keep["codex:"+c] || !untouched[c] || (r != "" && rolloutInteractive(r)) {
				continue // protected: leave it and everything below it
			}
			if err := walk(c); err != nil {
				return err
			}
			out = append(out, c) // after its descendants
		}
		return nil
	}
	return out, walk(id)
}

// codexChildren returns hidden helper threads that belong to thread id and
// should be deleted with it: spawn-edge children, plus subagent threads whose
// rollout names id as parent_thread_id (the reviewer `codex review` starts has
// no edge, only that field). Helpers whose parent can't be read are left alone.
func codexChildren(id string) ([]string, error) {
	db, err := sql.Open("sqlite", "file:"+codexDB()+"?mode=ro&_pragma=busy_timeout(2000)")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	ids, err := queryStrings(db, `SELECT child_thread_id FROM thread_spawn_edges WHERE parent_thread_id = ?`, id)
	if err != nil {
		return nil, err
	}
	// Candidates only narrow the search; ownership comes from the rollout below.
	rows, err := db.Query(`SELECT c.id, c.rollout_path FROM threads c JOIN threads p ON p.id = ?
		WHERE c.source LIKE '{"subagent"%' AND c.cwd = p.cwd AND c.created_at_ms >= p.created_at_ms
		  AND c.id NOT IN (SELECT child_thread_id FROM thread_spawn_edges)`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c, rollout string
		if err := rows.Scan(&c, &rollout); err != nil {
			return nil, err
		}
		if rolloutParent(rollout) == id {
			ids = append(ids, c)
		}
	}
	return ids, rows.Err()
}

func queryStrings(db *sql.DB, q string, args ...any) ([]string, error) {
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// rolloutParent reads parent_thread_id from a rollout's session_meta line.
func rolloutParent(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20) // session_meta embeds the instructions
	if !sc.Scan() {
		return ""
	}
	var r struct {
		Type    string `json:"type"`
		Payload struct {
			Parent string `json:"parent_thread_id"`
		} `json:"payload"`
	}
	if json.Unmarshal(sc.Bytes(), &r) != nil || r.Type != "session_meta" {
		return ""
	}
	return r.Payload.Parent
}

var (
	interactiveMu    sync.Mutex
	interactiveCache = map[string]interactiveEntry{}
)

type interactiveEntry struct {
	size  int64
	mtime time.Time
	yes   bool
}

// rolloutInteractive reports whether an exec run was resumed later (see
// scanInteractive). Cached per file size and mtime: List runs every few seconds.
func rolloutInteractive(path string) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	interactiveMu.Lock()
	c, ok := interactiveCache[path]
	interactiveMu.Unlock()
	if ok && c.size == st.Size() && c.mtime.Equal(st.ModTime()) {
		return c.yes
	}
	yes := scanInteractive(path)
	interactiveMu.Lock()
	interactiveCache[path] = interactiveEntry{st.Size(), st.ModTime(), yes}
	interactiveMu.Unlock()
	return yes
}

// scanInteractive reports whether an exec rollout may have been used beyond
// the run itself. Only the untouched shape counts as headless: exactly one
// task_started, then task_complete as the last event, and no non-exec
// originator. A resume (even one that sends nothing) appends events after it.
// A stopped run (turn_aborted, or no marker at all) is kept too: a later
// resume of it can't be told apart. Unreadable to the end also counts as used.
func scanInteractive(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return true
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	started, done := 0, false
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if done {
			return true // activity after the run finished
		}
		switch {
		case bytes.Contains(line, []byte(`"type":"task_started"`)):
			if started++; started > 1 {
				return true
			}
		case bytes.Contains(line, []byte(`"type":"task_complete"`)):
			done = true
		case bytes.Contains(line, []byte(`"type":"turn_aborted"`)):
			return true // stopped, not finished: kept, like a run with no marker
		}
		if i := bytes.Index(line, []byte(`"originator":"`)); i >= 0 {
			rest := line[i+len(`"originator":"`):]
			if j := bytes.IndexByte(rest, '"'); j >= 0 && string(rest[:j]) != "codex_exec" {
				return true
			}
		}
	}
	return sc.Err() != nil || !done
}

// codexRollout returns the existing rollout file of thread id, or "".
func codexRollout(id string) string {
	db, err := sql.Open("sqlite", "file:"+codexDB()+"?mode=ro&_pragma=busy_timeout(2000)")
	if err != nil {
		return ""
	}
	defer db.Close()
	var p string
	if db.QueryRow(`SELECT rollout_path FROM threads WHERE id = ?`, id).Scan(&p) != nil {
		return ""
	}
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// codexThreadsLive reports whether any codex process holds one of the rollout
// paths open or was started as `codex resume <id>` for one of ids. It errors
// when processes can't be inspected, so callers fail closed.
func codexThreadsLive(ids, paths []string) (bool, error) {
	if len(paths) == 0 && len(ids) == 0 {
		return false, nil
	}
	procs, err := codexProcs()
	if err != nil {
		return false, err
	}
	want := map[string]bool{}
	for _, p := range append(paths, ids...) {
		want[p] = true
	}
	for _, p := range procs {
		if p.unknown {
			return false, errors.New("a codex process could not be inspected")
		}
		if p.resumeID != "" && want[p.resumeID] {
			return true, nil
		}
		for _, r := range p.rollouts {
			if want[r] {
				return true, nil
			}
		}
	}
	return false, nil
}

// codexUntouched reports, per thread id, whether its sqlite turn history has
// only the shape a finished non-interactive thread leaves: no turns, one
// completed turn (a reviewer, a plain exec), or a stale inProgress turn then a
// completed one (a review run). Any other shape means someone resumed it.
func codexUntouched(ids []string) (map[string]bool, error) {
	res := map[string]bool{}
	if len(ids) == 0 {
		return res, nil
	}
	shapes := map[string]string{}
	path := filepath.Join(CodexHome(), "thread_history_1.sqlite")
	if _, err := os.Stat(path); err == nil {
		db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(2000)")
		if err != nil {
			return nil, err
		}
		defer db.Close()
		args := make([]any, len(ids))
		for i, id := range ids {
			args[i] = id
		}
		rows, err := db.Query(`SELECT thread_id, status FROM thread_turns
			WHERE thread_id IN (?`+strings.Repeat(",?", len(ids)-1)+`) ORDER BY thread_id, rollout_ordinal`, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var id, status string
			if err := rows.Scan(&id, &status); err != nil {
				return nil, err
			}
			if shapes[id] != "" {
				shapes[id] += ","
			}
			shapes[id] += status
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	for _, id := range ids {
		switch shapes[id] {
		case "", "completed", "inProgress,completed":
			res[id] = true
		}
	}
	return res, nil
}
