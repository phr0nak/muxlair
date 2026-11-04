package agents

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// transcriptMeta is what we extract from one Claude .jsonl transcript.
type transcriptMeta struct {
	cwd, title, aiTitle, lastPrompt string
	entrypoint                      string // first record's: "cli" interactive, "sdk-cli" for claude -p
	interactive                     bool   // some record came from an interactive run (e.g. resumed later)
}

type cacheEntry struct {
	size  int64
	mtime time.Time
	meta  transcriptMeta
}

var (
	claudeCacheMu sync.Mutex
	claudeCache   = map[string]cacheEntry{}
)

// claudeLive is ~/.claude/sessions/<pid>.json, written by running Claude processes.
type claudeLive struct {
	PID        int    `json:"pid"`
	SessionID  string `json:"sessionId"`
	Cwd        string `json:"cwd"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	UpdatedAt  int64  `json:"updatedAt"`
	StartedAt  int64  `json:"startedAt"`
	NameSource string `json:"nameSource"` // "user" after /rename, "derived" when auto-named
	Entrypoint string `json:"entrypoint"` // "sdk-cli" for claude -p
	tty        string
}

// Unnamed is shown for a session (Claude or Codex) with no name, title or prompt.
const Unnamed = "(unnamed)"

func listClaude() ([]Session, error) {
	live, liveErr := claudeLiveSessions()
	files, _ := filepath.Glob(filepath.Join(Home, ".claude", "projects", "*", "*.jsonl"))
	byID := map[string]int{} // indices, not pointers: out grows below
	var out []Session
	for _, f := range files {
		st, err := os.Stat(f)
		if err != nil {
			continue
		}
		m := claudeMeta(f, st)
		id := strings.TrimSuffix(filepath.Base(f), ".jsonl")
		s := Session{Agent: "claude", ID: id, Cwd: m.cwd, Updated: st.ModTime(), Files: claudeFiles(f, id)}
		s.Name = firstNonEmpty(m.title, m.aiTitle, m.lastPrompt, Unnamed)
		s.Headless = m.entrypoint == "sdk-cli" && !m.interactive
		if s.Cwd == "" {
			s.Cwd = filepath.Base(filepath.Dir(f))
		}
		out = append(out, s)
	}
	for i := range out {
		byID[out[i].ID] = i
	}
	for _, l := range live {
		i, ok := byID[l.SessionID]
		if !ok {
			// Live but no transcript yet (fresh session).
			out = append(out, Session{Agent: "claude", ID: l.SessionID, Cwd: l.Cwd, Name: Unnamed})
			i = len(out) - 1
			byID[l.SessionID] = i
		}
		s := &out[i]
		s.PID, s.TTY = l.PID, l.tty
		if l.Entrypoint == "sdk-cli" {
			s.Headless = true
		} else if l.Entrypoint != "" && s.Headless {
			// A headless transcript resumed interactively. Remember it: if they
			// exit without a new record, the transcript alone would look headless.
			noteInteractive(s.Key())
			s.Headless = false
		}
		s.Status = Idle
		if l.Status == "busy" {
			s.Status = Busy
		}
		// A user-set live name reflects /rename instantly. A derived one ("twinpeaks-64")
		// is Claude making up a name from the folder, not a name: it is never shown.
		if l.Name != "" && l.NameSource == "user" {
			s.Name = l.Name
		}
		if l.Cwd != "" {
			s.Cwd = l.Cwd
		}
		if t := time.UnixMilli(l.UpdatedAt); t.After(s.Updated) {
			s.Updated = t
		}
	}
	for i := range out {
		out[i].Resume = "cd " + shellQuote(out[i].Cwd) + " && claude --resume " + shellQuote(out[i].ID)
	}
	return out, liveErr
}

// claudeLiveSessions errors if a live process's record can't be read: its session
// would otherwise look exited, and a delete could move files Claude is writing.
func claudeLiveSessions() ([]claudeLive, error) {
	files, _ := filepath.Glob(filepath.Join(Home, ".claude", "sessions", "*.json"))
	var out []claudeLive
	var errs []error
	for _, f := range files {
		pid, _ := strconv.Atoi(strings.TrimSuffix(filepath.Base(f), ".json"))
		if !alive(pid) {
			continue // stale record of an exited process
		}
		b, err := os.ReadFile(f)
		var l claudeLive
		if err == nil {
			err = json.Unmarshal(b, &l)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("claude live session %s: %w", filepath.Base(f), err))
			continue
		}
		if l.SessionID == "" || !alive(l.PID) {
			continue
		}
		out = append(out, l)
	}
	pids := make([]int, len(out))
	for i, l := range out {
		pids[i] = l.PID
	}
	stats := procStats(pids) // one ps for all live sessions
	kept := out[:0]
	for _, l := range out {
		// A process that started after the record was written is a reused PID, not Claude.
		// If ps fails, keep the record: treating a live session as exited risks trashing it.
		st, ok := stats[l.PID]
		if ok && st.started.Unix() > l.StartedAt/1000+1 {
			continue
		}
		l.tty = st.tty
		kept = append(kept, l)
	}
	return kept, errors.Join(errs...)
}

func claudeMeta(path string, st os.FileInfo) transcriptMeta {
	claudeCacheMu.Lock()
	defer claudeCacheMu.Unlock()
	if c, ok := claudeCache[path]; ok && c.size == st.Size() && c.mtime.Equal(st.ModTime()) {
		return c.meta
	}
	m := parseClaudeTranscript(path)
	claudeCache[path] = cacheEntry{st.Size(), st.ModTime(), m}
	return m
}

func parseClaudeTranscript(path string) transcriptMeta {
	var m transcriptMeta
	f, err := os.Open(path)
	if err != nil {
		return m
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		// Cheap prefilter: only decode lines we care about.
		isTitle := bytes.Contains(line, []byte(`"custom-title"`)) || bytes.Contains(line, []byte(`"ai-title"`))
		isPrompt := bytes.Contains(line, []byte(`"last-prompt"`))
		needCwd := m.cwd == "" && bytes.Contains(line, []byte(`"cwd"`))
		if bytes.Contains(line, []byte(`"entrypoint":"cli"`)) {
			m.interactive = true
		}
		if !isTitle && !isPrompt && !needCwd {
			continue
		}
		var r struct {
			Type        string `json:"type"`
			Cwd         string `json:"cwd"`
			Entrypoint  string `json:"entrypoint"`
			CustomTitle string `json:"customTitle"`
			AITitle     string `json:"aiTitle"`
			LastPrompt  string `json:"lastPrompt"`
		}
		if json.Unmarshal(line, &r) != nil {
			continue
		}
		if m.cwd == "" {
			m.cwd, m.entrypoint = r.Cwd, r.Entrypoint // same record carries both
		}
		switch r.Type {
		case "custom-title":
			m.title = r.CustomTitle
		case "ai-title":
			m.aiTitle = r.AITitle
		case "last-prompt":
			m.lastPrompt = oneLine(r.LastPrompt) // full text; the UI truncates for display
		}
	}
	if sc.Err() != nil {
		m.interactive = true // unread records might be an interactive resume: never auto-delete
	}
	return m
}

// claudeFiles lists everything belonging to a transcript that Trash should move.
func claudeFiles(jsonl, id string) []string {
	files := []string{jsonl}
	for _, p := range []string{
		strings.TrimSuffix(jsonl, ".jsonl"), // subagent transcripts, tool results
		filepath.Join(Home, ".claude", "file-history", id),
	} {
		if _, err := os.Stat(p); err == nil {
			files = append(files, p)
		}
	}
	return files
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
