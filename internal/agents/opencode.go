package agents

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// OpenCode (v2) keeps every session in one SQLite database, owned by its
// background server. muxlair only reads it (mode=ro, which also reads the WAL)
// and changes sessions through the server's own API (`opencode api`).

type opencodeAgent struct{}

func (opencodeAgent) Name() string             { return "opencode" }
func (opencodeAgent) Title() string            { return "OpenCode" }
func (opencodeAgent) List() ([]Session, error) { return listOpencode() }
func (opencodeAgent) WatchPaths() []string     { return []string{opencodeDataDir()} }
func (opencodeAgent) RenameRunning() bool      { return true }

// RenameLine is empty: the server owns the session, so Rename reaches a running
// one too and its terminal shows the new title without typing anything.
func (opencodeAgent) RenameLine(string) string { return "" }

func (opencodeAgent) Rename(s Session, name string) error {
	body, err := json.Marshal(map[string]string{"title": name})
	if err != nil {
		return err
	}
	cmd := exec.Command(opencodeBin(), "api", "session.update", "--param", "sessionID="+s.ID, "-d", string(body))
	cmd.Dir = os.TempDir() // the server is shared; the folder it runs from doesn't matter
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		var e struct{ Message string } // the server answers errors as JSON
		if json.Unmarshal(out, &e) == nil && e.Message != "" {
			msg = e.Message
		}
		return errors.New("opencode: " + firstNonEmpty(msg, err.Error()))
	}
	return nil
}

// opencodeDataDir follows OpenCode's XDG layout, on macOS too.
func opencodeDataDir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "opencode")
	}
	return filepath.Join(Home, ".local", "share", "opencode")
}

func opencodeDB() string { return filepath.Join(opencodeDataDir(), "opencode.db") }

// opencodeBin is the opencode CLI: on PATH, else where its installer puts it.
// An absolute path, so a new terminal finds it whatever its PATH.
func opencodeBin() string {
	if p, err := exec.LookPath("opencode"); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return p
	}
	return filepath.Join(Home, ".opencode", "bin", "opencode")
}

func opencodeOpen() (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+opencodeDB()+"?mode=ro&_pragma=busy_timeout(2000)")
}

// listOpencode lists top-level sessions (child sessions are subagents' work,
// like Codex's helper threads) that are not archived.
func listOpencode() ([]Session, error) {
	if _, err := os.Stat(opencodeDB()); err != nil {
		return nil, nil // opencode not installed
	}
	db, err := opencodeOpen()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id, COALESCE(title, ''), COALESCE(directory, ''), time_updated,
		COALESCE(time_busy, 0), COALESCE(time_idle, 0)
		FROM session_v2 WHERE parent_id IS NULL AND time_archived IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bin := opencodeBin()
	var out []Session
	for rows.Next() {
		s := Session{Agent: "opencode"}
		var updated, busy, idle int64
		if err := rows.Scan(&s.ID, &s.Name, &s.Cwd, &updated, &busy, &idle); err != nil {
			return nil, err
		}
		s.Name = oneLine(firstNonEmpty(s.Name, Unnamed))
		s.Updated = time.UnixMilli(updated)
		if busy > idle {
			// The server is running a turn (checked against running processes below);
			// opening another view of it is harmless.
			s.Status = Busy
		}
		s.Resume = "cd " + shellQuote(s.Cwd) + " && " + shellQuote(bin) + " -s " + shellQuote(s.ID)
		if d := os.Getenv("XDG_DATA_HOME"); d != "" {
			// New tmux windows inherit the server's env, not ours; pin the data we listed from.
			s.Resume = "cd " + shellQuote(s.Cwd) + " && XDG_DATA_HOME=" + shellQuote(d) + " " + shellQuote(bin) + " -s " + shellQuote(s.ID)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	attachOpencodeProcs(out)
	return out, nil
}

// attachOpencodeProcs marks sessions shown in a terminal by `opencode -s <id>`,
// so Enter jumps there. A TUI started without -s can show any session and
// isn't matched; since the server owns the session, a second view is harmless.
// shortcut: argv can be stale after switching sessions inside the TUI; upgrade
// if the server exposes which session each client shows.
// A turn left "busy" in the database by an OpenCode that exited or crashed
// mid-turn is shown as exited once no opencode process runs.
// shortcut: with some opencode process running, a stale busy row still shows busy;
// upgrade by asking the server which sessions are active (session.active).
func attachOpencodeProcs(ss []Session) {
	procs, err := opencodeProcs()
	if err != nil {
		return
	}
	running := false
	for _, p := range procs {
		running = running || p.exe == "opencode"
	}
	if !running {
		for i := range ss {
			ss[i].Status = Exited
		}
		return
	}
	byID := map[string]int{}
	for i, s := range ss {
		byID[s.ID] = i
	}
	for _, p := range procs {
		if p.exe != "opencode" || p.tty == "" {
			continue
		}
		if i, ok := byID[opencodeSessionArg(procArgs(p.pid))]; ok {
			ss[i].PID, ss[i].TTY = p.pid, p.tty
			if ss[i].Status != Busy {
				ss[i].Status = Idle
			}
		}
	}
}

// opencodeProcs lists the user's processes (a variable for tests).
var opencodeProcs = ownProcs

// opencodeSessionArg returns the id of `opencode [...] -s|--session <id>`.
func opencodeSessionArg(args []string) string {
	for i, a := range args {
		if v, ok := strings.CutPrefix(a, "--session="); ok {
			return v
		}
		if (a == "-s" || a == "--session") && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func (opencodeAgent) Details(s Session, peek bool) (Details, error) {
	d := Details{Location: opencodeDB()}
	db, err := opencodeOpen()
	if err != nil {
		return d, err
	}
	defer db.Close()
	var created int64
	var model sql.NullString
	var cost float64
	var in, out, reasoning, cacheRead, cacheWrite int64
	err = db.QueryRow(`SELECT time_created, model, COALESCE(cost, 0), COALESCE(tokens_input, 0),
		COALESCE(tokens_output, 0), COALESCE(tokens_reasoning, 0), COALESCE(tokens_cache_read, 0),
		COALESCE(tokens_cache_write, 0) FROM session_v2 WHERE id = ?`, s.ID).
		Scan(&created, &model, &cost, &in, &out, &reasoning, &cacheRead, &cacheWrite)
	if err != nil {
		return d, err
	}
	d.Started = time.UnixMilli(created)
	var m struct {
		ID, ProviderID string
	}
	if json.Unmarshal([]byte(model.String), &m) == nil && m.ID != "" {
		d.Model = m.ID
	}
	d.Split = Tokens{In: in, Out: out + reasoning, Cache: cacheRead + cacheWrite}
	d.HasSplit = true
	d.Total = d.Split.In + d.Split.Out + d.Split.Cache
	d.Cost, d.HasCost = cost, true // OpenCode prices every turn; free models cost 0
	if peek {
		d.Peek, err = opencodePeek(db, s.ID)
	}
	return d, err
}

// opencodePeek reads the last user message and the last assistant text after
// it, like Claude's peek: in a turn with tool calls the earlier texts are
// progress notes and the last one is the answer. Reasoning and tool items are
// left out.
func opencodePeek(db *sql.DB, id string) (Peek, error) {
	// Start at the newest prompt, so a long session isn't read in full on every refresh.
	var start sql.NullInt64
	err := db.QueryRow(`SELECT MAX(seq) FROM session_message WHERE session_id = ? AND type = 'user'
		AND (trim(COALESCE(json_extract(data, '$.text'), '')) <> ''
			OR json_array_length(COALESCE(json_extract(data, '$.files'), '[]')) > 0)`, id).Scan(&start)
	if err != nil || !start.Valid {
		return Peek{}, err
	}
	rows, err := db.Query(`SELECT type, data FROM session_message
		WHERE session_id = ? AND seq >= ? AND type IN ('user', 'assistant') ORDER BY seq`, id, start.Int64)
	if err != nil {
		return Peek{}, err
	}
	defer rows.Close()
	var p Peek
	for rows.Next() {
		var typ, data string
		if err := rows.Scan(&typ, &data); err != nil {
			return Peek{}, err
		}
		var m struct {
			Text    string            `json:"text"`
			Files   []json.RawMessage `json:"files"` // attachments; shown as [file]
			Finish  string            `json:"finish"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal([]byte(data), &m) != nil {
			continue
		}
		switch {
		case typ == "user" && (strings.TrimSpace(m.Text) != "" || len(m.Files) > 0):
			prompt := m.Text
			for range m.Files {
				prompt += "\n[file]"
			}
			p = Peek{Prompt: peekText(prompt)}
		case typ == "assistant" && p.Prompt != "":
			var parts []string
			for _, c := range m.Content {
				if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
					parts = append(parts, c.Text)
				}
			}
			if len(parts) > 0 {
				p.Reply = peekText(strings.Join(parts, "\n\n"))
			}
			p.Done = m.Finish == "stop"
		}
	}
	return p, rows.Err()
}
