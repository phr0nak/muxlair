package agents

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// pi (earendil-works/pi-coding-agent) keeps each session in its own JSONL file
// under ~/.pi/agent/sessions/--<cwd>--/<timestamp>_<id>.jsonl, written only by
// the pi process that has it open. muxlair reads the files and renames through
// pi itself: `pi --mode rpc` with set_session_name for a closed session, /name
// typed into the terminal for a running one. Never both writers at once.

type piAgent struct{}

func (piAgent) Name() string               { return "pi" }
func (piAgent) Title() string              { return "pi" }
func (piAgent) List() ([]Session, error)   { return listPi() }
func (piAgent) WatchPaths() []string       { return piWatchPaths() }
func (piAgent) RenameRunning() bool        { return false } // a second pi would write the same file
func (piAgent) RenameLine(n string) string { return "/name " + n }

// piSessionDir follows pi's own order: the environment, then sessionDir in
// the agent's settings.json, then the agent directory.
// shortcut: a relative sessionDir (resolved per project) and project-local
// settings are not followed; upgrade if someone stores sessions that way.
func piSessionDir() string {
	if d := os.Getenv("PI_CODING_AGENT_SESSION_DIR"); d != "" {
		return d
	}
	agent := filepath.Join(Home, ".pi", "agent")
	if d := os.Getenv("PI_CODING_AGENT_DIR"); d != "" {
		agent = d
	}
	var st struct{ SessionDir string }
	if b, err := os.ReadFile(filepath.Join(agent, "settings.json")); err == nil && json.Unmarshal(b, &st) == nil {
		d := st.SessionDir
		if rest, ok := strings.CutPrefix(d, "~/"); ok {
			d = filepath.Join(Home, rest)
		}
		if filepath.IsAbs(d) {
			return d
		}
	}
	return filepath.Join(agent, "sessions")
}

func piWatchPaths() []string {
	dirs, _ := filepath.Glob(filepath.Join(piSessionDir(), "*"))
	return append([]string{piSessionDir()}, dirs...)
}

// piMeta is what a session file says about itself.
type piMeta struct {
	id, cwd, name, firstPrompt string
	started                    time.Time
}

type piCacheEntry struct {
	size  int64
	mtime time.Time
	meta  piMeta
}

var (
	piCacheMu sync.Mutex
	piCache   = map[string]piCacheEntry{}
)

func listPi() ([]Session, error) {
	files, _ := filepath.Glob(filepath.Join(piSessionDir(), "*", "*.jsonl"))
	var out []Session
	for _, f := range files {
		st, err := os.Stat(f)
		if err != nil {
			continue
		}
		m := piMetaOf(f, st)
		if m.id == "" {
			continue // no header: not a session file
		}
		s := Session{Agent: "pi", ID: m.id, Cwd: m.cwd, Updated: st.ModTime(), Files: []string{f}}
		// pi's own session picker shows the first message when there is no name.
		s.Name = oneLine(firstNonEmpty(m.name, m.firstPrompt, Unnamed))
		s.Resume = "cd " + shellQuote(s.Cwd) + " && pi --session " + shellQuote(f)
		out = append(out, s)
	}
	attachPiProcs(out)
	return out, nil
}

func piMetaOf(path string, st os.FileInfo) piMeta {
	piCacheMu.Lock()
	defer piCacheMu.Unlock()
	if c, ok := piCache[path]; ok && c.size == st.Size() && c.mtime.Equal(st.ModTime()) {
		return c.meta
	}
	m := parsePiMeta(path)
	piCache[path] = piCacheEntry{st.Size(), st.ModTime(), m}
	return m
}

func parsePiMeta(path string) piMeta {
	var m piMeta
	f, err := os.Open(path)
	if err != nil {
		return m
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for first := true; sc.Scan(); first = false {
		line := sc.Bytes()
		if first {
			var h struct {
				Type, ID, Cwd string
				Timestamp     time.Time
			}
			if json.Unmarshal(line, &h) != nil || h.Type != "session" {
				return piMeta{}
			}
			m.id, m.cwd, m.started = h.ID, h.Cwd, h.Timestamp
			continue
		}
		switch {
		case bytes.Contains(line, []byte(`"type":"session_info"`)):
			var r struct{ Name string }
			if json.Unmarshal(line, &r) == nil {
				m.name = r.Name // the latest one wins, as in pi
			}
		case m.firstPrompt == "" && bytes.Contains(line, []byte(`"role":"user"`)):
			var r struct{ Message piMessage }
			if json.Unmarshal(line, &r) == nil {
				m.firstPrompt = piText(r.Message.Content, true)
			}
		}
	}
	return m
}

type piMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	Provider   string          `json:"provider"`
	Model      string          `json:"model"`
	StopReason string          `json:"stopReason"`
	Usage      *piUsage        `json:"usage"`
}

type piUsage struct {
	Input, Output, CacheRead, CacheWrite int64
	Cost                                 struct{ Total float64 }
}

// piText joins the text blocks of a message (content is a string or blocks);
// images show as [image] in a user message.
func piText(content json.RawMessage, user bool) string {
	var s string
	if json.Unmarshal(content, &s) == nil {
		return s
	}
	var blocks []struct{ Type, Text string }
	if json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		switch {
		case b.Type == "text" && strings.TrimSpace(b.Text) != "":
			parts = append(parts, b.Text)
		case user && b.Type == "image":
			parts = append(parts, "[image]")
		}
	}
	return strings.Join(parts, "\n")
}

// attachPiProcs marks sessions a running pi may have open. pi (a node
// program) replaces its argv with "pi", so the session it shows can't be read
// from it, and it keeps no lock file: every session in a running pi's folder
// counts as maybe live, and is neither opened nor renamed (two pi processes
// would write the same file). /name in that pi renames it.
// shortcut: no jump to a running pi and no busy/idle; upgrade if pi records
// which session a process has open.
func attachPiProcs(ss []Session) {
	procs, err := ownProcs()
	if err != nil {
		for i := range ss {
			ss[i].MaybeLive = true
		}
		return
	}
	for _, p := range procs {
		if p.exe != "pi" && !(p.exe == "node" || p.exe == "bun") {
			continue
		}
		if args := procArgs(p.pid); p.exe != "pi" && (len(args) == 0 || filepath.Base(args[0]) != "pi") {
			continue
		}
		cwd := lsofCwd(p.pid)
		for i := range ss {
			if cwd == "" || ss[i].Cwd == cwd {
				ss[i].MaybeLive = true // cwd unknown: fail closed for every session
			}
		}
	}
}

// lsofCwd returns the working directory of pid, or "" if it can't be read.
func lsofCwd(pid int) string {
	out, err := exec.Command("lsof", "-a", "-p", fmt.Sprint(pid), "-d", "cwd", "-Fn").Output()
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(l, "n"); ok {
			return v
		}
	}
	return ""
}

// Rename renames a closed session through pi itself: a short-lived
// `pi --mode rpc --session <file>` gets set_session_name, which appends the
// session_info record pi reads its name from. Nothing is sent to a model.
func (piAgent) Rename(s Session, name string) error {
	if s.MaybeLive || len(s.Files) == 0 {
		return errors.New("a pi process in this folder may have this session open; rename it with /name there")
	}
	cmd := exec.Command("pi", "--mode", "rpc", "--offline", "--no-approve", "--no-extensions", "--no-mcp", "--session", s.Files[0])
	cmd.Dir = s.Cwd
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() { stdin.Close(); cmd.Process.Kill(); cmd.Wait() }()
	req, _ := json.Marshal(map[string]string{"id": "muxlair", "type": "set_session_name", "name": name})
	done := make(chan error, 1)
	go func() {
		if _, err := stdin.Write(append(req, '\n')); err != nil {
			done <- err
			return
		}
		sc := rpcScanner(stdout)
		for sc.Scan() {
			var r struct {
				Type, Command, Error string
				Success              bool
			}
			if json.Unmarshal(sc.Bytes(), &r) != nil || r.Type != "response" || r.Command != "set_session_name" {
				continue
			}
			if !r.Success {
				done <- errors.New("pi: " + firstNonEmpty(r.Error, "set_session_name failed"))
				return
			}
			stdin.Close() // orderly shutdown, so pi finishes writing
			done <- cmd.Wait()
			return
		}
		done <- errors.New("pi exited without answering")
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(20 * time.Second):
		return errors.New("pi --mode rpc: timed out")
	}
}

func (piAgent) Details(s Session, peek bool) (Details, error) {
	if len(s.Files) == 0 {
		return Details{}, errNoTranscript
	}
	f, err := os.Open(s.Files[0])
	if err != nil {
		return Details{}, err
	}
	defer f.Close()
	d := Details{Location: s.Files[0], HasSplit: true, HasCost: true}
	var msgs []piEntry // message entries, in file order
	parent := map[string]string{}
	leaf := "" // pi's current position: the last entry written
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var e struct {
			Type      string
			ID        string
			ParentID  *string `json:"parentId"`
			Timestamp time.Time
			Message   piMessage
			Usage     *piUsage
			ModelID   string `json:"modelId"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if e.ID != "" && e.Type != "session" {
			leaf = e.ID
			if e.ParentID != nil {
				parent[e.ID] = *e.ParentID
			}
		}
		u := e.Usage
		switch e.Type {
		case "session":
			d.Started = e.Timestamp
		case "model_change":
			if e.ModelID != "" {
				d.Model = e.ModelID // the selected model, until an answer names the one that replied
			}
		case "message":
			if e.Message.Role == "assistant" {
				u = e.Message.Usage
				if e.Message.Model != "" {
					d.Model = e.Message.Model
				}
			}
			if peek {
				msgs = append(msgs, piEntry{e.ID, e.Message})
			}
		}
		if u != nil { // every branch's turns, usage and compaction entries count, as in pi's totals
			d.Split.In += u.Input
			d.Split.Out += u.Output
			d.Split.Cache += u.CacheRead + u.CacheWrite
			d.Cost += u.Cost.Total
		}
	}
	d.Total = d.Split.In + d.Split.Out + d.Split.Cache
	if peek {
		d.Peek = piPeek(msgs, parent, leaf)
	}
	return d, sc.Err()
}

type piEntry struct {
	id  string
	msg piMessage
}

// piPeek reads the latest exchange on the branch pi shows: the chain of
// parents from the last entry written. After /tree, the file also holds
// messages of branches left behind.
func piPeek(msgs []piEntry, parent map[string]string, leaf string) Peek {
	onBranch := map[string]bool{}
	for id, n := leaf, 0; id != "" && !onBranch[id] && n <= len(parent); id, n = parent[id], n+1 {
		onBranch[id] = true
	}
	var p Peek
	for _, e := range msgs {
		if !onBranch[e.id] {
			continue
		}
		m := e.msg
		switch {
		case m.Role == "user":
			if t := piText(m.Content, true); strings.TrimSpace(t) != "" {
				p = Peek{Prompt: peekText(t)}
			}
		case m.Role == "assistant" && p.Prompt != "":
			if t := piText(m.Content, false); strings.TrimSpace(t) != "" {
				p.Reply = peekText(t)
			}
			p.Done = m.StopReason == "stop"
		}
	}
	return p
}
