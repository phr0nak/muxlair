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
	branch                          string // the last gitBranch Claude recorded
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
	tty        string
}

func claudeWatchPaths() []string {
	paths := []string{
		filepath.Join(Home, ".claude", "sessions"),
		filepath.Join(Home, ".claude", "projects"), // new project dirs get watched as they appear
	}
	dirs, _ := filepath.Glob(filepath.Join(Home, ".claude", "projects", "*"))
	return append(paths, dirs...)
}

// Unnamed is shown for a Claude session that has no title and no prompt yet.
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
		s := Session{Agent: "claude", ID: id, Cwd: m.cwd, Updated: st.ModTime(), Files: []string{f}, Branch: m.branch}
		s.Name = firstNonEmpty(m.title, m.aiTitle, m.lastPrompt, Unnamed)
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
// would otherwise look exited, and muxlair could open it a second time.
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
		// If ps fails, keep the record: treating a live session as exited risks opening it twice.
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
		if b, ok := jsonStringField(line, `"gitBranch":`); ok {
			m.branch = b // on most records; the last one (even "") is where the session is now
		}
		// Cheap prefilter: only decode lines we care about.
		isTitle := bytes.Contains(line, []byte(`"custom-title"`)) || bytes.Contains(line, []byte(`"ai-title"`))
		isPrompt := bytes.Contains(line, []byte(`"last-prompt"`))
		needCwd := m.cwd == "" && bytes.Contains(line, []byte(`"cwd"`))
		if !isTitle && !isPrompt && !needCwd {
			continue
		}
		var r struct {
			Type        string `json:"type"`
			Cwd         string `json:"cwd"`
			CustomTitle string `json:"customTitle"`
			AITitle     string `json:"aiTitle"`
			LastPrompt  string `json:"lastPrompt"`
		}
		if json.Unmarshal(line, &r) != nil {
			continue
		}
		if m.cwd == "" {
			m.cwd = r.Cwd
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
	return m
}

// jsonStringField returns the string value of the first `key` (e.g.
// `"gitBranch":`) in a compact JSON line, without decoding the whole record.
func jsonStringField(line []byte, key string) (string, bool) {
	i := bytes.Index(line, []byte(key))
	if i < 0 {
		return "", false
	}
	rest := line[i+len(key):]
	if len(rest) == 0 || rest[0] != '"' {
		return "", false
	}
	for j := 1; j < len(rest); j++ {
		switch rest[j] {
		case '\\':
			j++ // skip the escaped character
		case '"':
			var v string
			if json.Unmarshal(rest[:j+1], &v) != nil {
				return "", false
			}
			return v, true
		}
	}
	return "", false
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
