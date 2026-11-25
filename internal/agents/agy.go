package agents

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// agy (Google's Antigravity CLI) indexes conversations in a SQLite database,
// conversation_summaries.db, and logs each one under brain/<id>/. muxlair only
// reads them. A running agy holds presence/<id>.lock open for the conversation
// it shows; that open file is how muxlair ties a process to a conversation.
// agy has no command to rename a conversation from outside, so only a running
// one is renamed, by typing /rename into its terminal.

type agyAgent struct{}

func (agyAgent) Name() string             { return "agy" }
func (agyAgent) Title() string            { return "agy" }
func (agyAgent) List() ([]Session, error) { return listAgy() }
func (agyAgent) WatchPaths() []string {
	return []string{agyHome(), filepath.Join(agyHome(), "presence"), filepath.Join(agyHome(), "annotations")}
}
func (agyAgent) RenameRunning() bool        { return false }
func (agyAgent) RenameLine(n string) string { return "/rename " + n }

func (agyAgent) Rename(Session, string) error {
	return errors.New("agy can only rename an open conversation: open it (enter), then press r")
}

func agyHome() string { return filepath.Join(Home, ".gemini", "antigravity-cli") }

func agyDB() string { return filepath.Join(agyHome(), "conversation_summaries.db") }

// agyQuery runs read on the index, read-only. agy keeps it locked while it
// runs, which makes a plain read-only read fail; with no (or an empty) -wal
// file beside it, every committed row is in the main file, so it is then read
// as immutable.
func agyQuery(read func(*sql.DB) error) error {
	err := agyRead("file:"+agyDB()+"?mode=ro&_pragma=busy_timeout(2000)", read)
	if err == nil {
		return nil
	}
	if st, werr := os.Stat(agyDB() + "-wal"); werr == nil && st.Size() > 0 {
		return err // rows may still be in the WAL: don't read a stale copy
	}
	return agyRead("file:"+agyDB()+"?immutable=1", read)
}

func agyRead(dsn string, read func(*sql.DB) error) error {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	return read(db)
}

func listAgy() ([]Session, error) {
	if _, err := os.Stat(agyDB()); err != nil {
		return nil, nil // agy not installed
	}
	var out []Session
	status := map[string]string{}
	err := agyQuery(func(db *sql.DB) error {
		out, status = nil, map[string]string{} // a failed first try starts over
		// Empty conversations (no steps yet) and subagent conversations are left out.
		rows, err := db.Query(`SELECT conversation_id, COALESCE(title, ''), COALESCE(preview, ''),
			COALESCE(last_modified_time, ''), COALESCE(workspace_uris, ''), COALESCE(status, '')
			FROM conversation_summaries
			WHERE step_count > 0 AND COALESCE(parent_conversation_id, '') = ''`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			s := Session{Agent: "agy"}
			var preview, modified, uris, st string
			if err := rows.Scan(&s.ID, &s.Name, &preview, &modified, &uris, &st); err != nil {
				return err
			}
			// /rename writes the annotation; the index keeps agy's own title.
			s.Name = oneLine(firstNonEmpty(agyAnnotationTitle(s.ID), s.Name, preview, Unnamed))
			s.Updated = agyTime(modified)
			s.Cwd = agyCwd(uris)
			if t := agyTranscript(s.ID); t != "" {
				s.Files = []string{t}
			}
			s.Resume = "cd " + shellQuote(s.Cwd) + " && agy --conversation " + shellQuote(s.ID)
			status[s.ID] = st
			out = append(out, s)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	attachAgyProcs(out, status)
	return out, nil
}

var agyTitle = regexp.MustCompile(`(?m)^title:\s*("(?:[^"\\]|\\.)*")`)

// agyAnnotationTitle reads title:"..." from annotations/<id>.pbtxt, where
// agy's /rename puts the name; "" if there is none.
func agyAnnotationTitle(id string) string {
	b, err := os.ReadFile(filepath.Join(agyHome(), "annotations", id+".pbtxt"))
	if err != nil {
		return ""
	}
	m := agyTitle.FindSubmatch(b)
	if m == nil {
		return ""
	}
	t, err := strconv.Unquote(string(m[1])) // text-proto strings use C-style escapes
	if err != nil {
		return ""
	}
	return t
}

// agyTime parses the index's text timestamps, e.g. "2026-11-24 16:07:40.124301+00:00".
func agyTime(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04:05.999999999-07:00", s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// agyCwd is the first folder of the conversation's workspace, a JSON list of file URIs.
func agyCwd(uris string) string {
	var us []string
	if json.Unmarshal([]byte(uris), &us) != nil {
		return ""
	}
	for _, u := range us {
		if p, err := url.Parse(u); err == nil && p.Scheme == "file" {
			return p.Path
		}
	}
	return ""
}

func agyTranscript(id string) string {
	p := filepath.Join(agyHome(), "brain", id, ".system_generated", "logs", "transcript.jsonl")
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// attachAgyProcs marks conversations shown by a running agy, from the
// presence lock it holds open. An agy holding none (starting, or in its
// picker) may open any conversation in its folder, so those count as maybe
// live; if processes or their files can't be read, every one does.
func attachAgyProcs(ss []Session, status map[string]string) {
	procs, err := ownProcs()
	if err != nil {
		for i := range ss {
			ss[i].MaybeLive = true
		}
		return
	}
	var agys []proc
	var pids []string
	for _, p := range procs {
		if p.exe == "agy" {
			agys = append(agys, p)
			pids = append(pids, strconv.Itoa(p.pid))
		}
	}
	if len(agys) == 0 {
		return
	}
	locks, cwds, ok := lsofAgy(strings.Join(pids, ","))
	byID := map[string]int{}
	for i, s := range ss {
		byID[s.ID] = i
	}
	for _, p := range agys {
		if i, held := byID[locks[p.pid]]; held && ok {
			s := &ss[i]
			s.PID, s.TTY, s.MaybeLive, s.Status = p.pid, p.tty, false, Idle
			if strings.Contains(status[s.ID], "RUNNING") {
				s.Status = Busy
			}
			continue
		}
		for i := range ss {
			if ss[i].PID == 0 && (!ok || cwds[p.pid] == "" || ss[i].Cwd == cwds[p.pid]) {
				ss[i].MaybeLive = true
			}
		}
	}
}

// lsofAgy returns, per pid, the conversation whose presence lock it holds and
// its working directory. ok is false when lsof could not be run.
func lsofAgy(pids string) (locks, cwds map[int]string, ok bool) {
	// lsof exits non-zero when a pid has gone, yet still prints the rest (as for codex).
	out, _ := exec.Command("lsof", "-b", "-n", "-P", "-w", "-p", pids, "-Fpfn").Output()
	if len(out) == 0 {
		return nil, nil, false
	}
	return parseAgyLsof(string(out), filepath.Join(agyHome(), "presence")+"/")
}

func parseAgyLsof(out, presence string) (locks, cwds map[int]string, ok bool) {
	locks, cwds = map[int]string{}, map[int]string{}
	pid, fd := 0, ""
	for _, l := range strings.Split(out, "\n") {
		if l == "" {
			continue
		}
		switch l[0] {
		case 'p':
			pid, _ = strconv.Atoi(l[1:])
		case 'f':
			fd = l[1:]
		case 'n':
			name := l[1:]
			if fd == "cwd" {
				cwds[pid] = name
			} else if rest, found := strings.CutPrefix(name, presence); found && strings.HasSuffix(rest, ".lock") {
				locks[pid] = strings.TrimSuffix(rest, ".lock")
			}
		}
	}
	return locks, cwds, true
}

var agyRequest = regexp.MustCompile(`(?s)<USER_REQUEST>\s*(.*?)\s*</USER_REQUEST>`)

func (agyAgent) Details(s Session, peek bool) (Details, error) {
	d := Details{Location: agyDB()}
	if len(s.Files) == 0 {
		return d, nil // indexed, but no log on this machine: nothing more to show
	}
	d.Location = s.Files[0]
	f, err := os.Open(s.Files[0])
	if err != nil {
		return d, err
	}
	defer f.Close()
	var p Peek
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		// Metadata only: with the peek off, no conversation text is decoded.
		var r struct {
			Type, Status, Model string
			CreatedAt           time.Time `json:"created_at"`
			In                  int64     `json:"input_tokens"`
			CacheRead           int64     `json:"cache_read_tokens"`
			Out                 int64     `json:"output_tokens"`
		}
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue
		}
		if d.Started.IsZero() {
			d.Started = r.CreatedAt
		}
		if r.Model != "" {
			d.Model = r.Model
		}
		d.Split.In += r.In
		d.Split.Out += r.Out
		d.Split.Cache += r.CacheRead
		if !peek || (r.Type != "USER_INPUT" && r.Type != "PLANNER_RESPONSE") {
			continue
		}
		var c struct {
			Content   string
			ToolCalls json.RawMessage `json:"tool_calls"`
		}
		if json.Unmarshal(sc.Bytes(), &c) != nil {
			continue
		}
		switch {
		case r.Type == "USER_INPUT":
			text := c.Content
			if m := agyRequest.FindStringSubmatch(text); m != nil {
				text = m[1] // the rest is agy's own metadata
			}
			if strings.TrimSpace(text) != "" {
				p = Peek{Prompt: peekText(text)}
			}
		case r.Type == "PLANNER_RESPONSE" && p.Prompt != "":
			if strings.TrimSpace(c.Content) != "" {
				p.Reply = peekText(c.Content)
			}
			// A response that calls tools is followed by more steps.
			p.Done = r.Status == "DONE" && (len(c.ToolCalls) == 0 || string(c.ToolCalls) == "null")
		}
	}
	d.Total = d.Split.In + d.Split.Out + d.Split.Cache
	d.HasSplit = d.Total > 0
	d.Peek = p
	return d, sc.Err()
}
