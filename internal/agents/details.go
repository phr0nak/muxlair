package agents

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"time"
)

// Details is the per-session information shown in the details pane.
// Zero values mean "not recorded by the agent".
type Details struct {
	Location string // transcript / rollout file, or the database holding the session
	Started  time.Time
	Model    string
	Total    int64  // tokens, all kinds
	Split    Tokens // breakdown, when HasSplit
	HasSplit bool
	Cost     float64
	HasCost  bool
	// Peek is the latest exchange of the conversation; only read when asked for.
	Peek Peek
}

type Tokens struct{ In, Out, Cache int64 }

// claudeCostState is the cost-state record Claude appends to its transcript.
type claudeCostState struct {
	TotalCostUSD float64 `json:"totalCostUSD"`
	StartTime    int64   `json:"startTime"`
	ModelUsage   map[string]struct {
		In          int64 `json:"inputTokens"`
		Out         int64 `json:"outputTokens"`
		CacheRead   int64 `json:"cacheReadInputTokens"`
		CacheCreate int64 `json:"cacheCreationInputTokens"`
	} `json:"modelUsage"`
}

var (
	detailsMu    sync.Mutex
	detailsCache = map[string]detailsEntry{}
	peekGen      int // bumped when the peek cache is dropped, so a parse already running can't put its text back
)

type detailsEntry struct {
	size  int64
	mtime time.Time
	d     Details
}

// DropPeekCache forgets every cached conversation text (the peek was turned off).
func DropPeekCache() {
	detailsMu.Lock()
	defer detailsMu.Unlock()
	peekGen++
	for k := range detailsCache {
		if strings.HasSuffix(k, "\x00peek") {
			delete(detailsCache, k)
		}
	}
}

// SessionDetails reads the details for s. Claude transcripts are parsed in full,
// so results are cached per file size and mtime. peek also reads the latest
// exchange of the conversation; when it is false no conversation text is read
// or cached.
func SessionDetails(s Session, peek bool) (Details, error) {
	switch s.Agent {
	case "claude":
		if len(s.Files) == 0 {
			return Details{}, errors.New("no transcript yet")
		}
		return claudeDetails(s.Files[0], peek)
	case "codex":
		return codexDetails(s, peek)
	}
	return Details{}, errors.New("details not supported for " + s.Agent)
}

func claudeDetails(path string, peek bool) (Details, error) {
	st, err := os.Stat(path)
	if err != nil {
		return Details{}, err
	}
	key := path
	if peek {
		key += "\x00peek" // a different entry, so turning the peek off drops no cache and leaks no text
	}
	detailsMu.Lock()
	if c, ok := detailsCache[key]; ok && c.size == st.Size() && c.mtime.Equal(st.ModTime()) {
		detailsMu.Unlock()
		return c.d, nil
	}
	gen := peekGen
	detailsMu.Unlock()
	d, err := parseClaudeDetails(path, peek)
	if err != nil {
		return d, err
	}
	detailsMu.Lock()
	if !peek || gen == peekGen {
		detailsCache[key] = detailsEntry{st.Size(), st.ModTime(), d}
	}
	detailsMu.Unlock()
	return d, nil
}

// parseClaudeDetails prefers Claude's own cost-state record (cost, start time,
// per-model tokens). Without one it sums assistant message usage, deduplicated
// by request id since a streamed reply is written more than once.
func parseClaudeDetails(path string, peek bool) (Details, error) {
	d := Details{Location: path}
	f, err := os.Open(path)
	if err != nil {
		return d, err
	}
	defer f.Close()
	type usage struct {
		In          int64 `json:"input_tokens"`
		Out         int64 `json:"output_tokens"`
		CacheRead   int64 `json:"cache_read_input_tokens"`
		CacheCreate int64 `json:"cache_creation_input_tokens"`
	}
	byRequest := map[string]usage{}
	after := map[string]usage{}    // requests after the last cost-state snapshot
	var costState *claudeCostState // last one wins; its totals are cumulative
	var pk Peek                    // the latest exchange, when peek is set
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		switch {
		case bytes.Contains(line, []byte(`"type":"cost-state"`)):
			var c claudeCostState
			if json.Unmarshal(line, &c) == nil {
				costState = &c
				clear(after)
			}
		case bytes.Contains(line, []byte(`"type":"assistant"`)):
			var r struct {
				RequestID string `json:"requestId"`
				Message   struct {
					Model string `json:"model"`
					Usage usage  `json:"usage"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &r) == nil && r.Message.Model != "" && r.Message.Model != "<synthetic>" {
				d.Model = r.Message.Model
				byRequest[r.RequestID] = r.Message.Usage
				after[r.RequestID] = r.Message.Usage
				if peek { // a second decode, only when the conversation is wanted
					var c struct {
						IsSidechain bool `json:"isSidechain"`
						Message     struct {
							StopReason string          `json:"stop_reason"`
							Content    json.RawMessage `json:"content"`
						} `json:"message"`
					}
					if json.Unmarshal(line, &c) == nil && !c.IsSidechain {
						pk.claudeAssistant(c.Message.Content, c.Message.StopReason)
					}
				}
			}
		case peek && bytes.Contains(line, []byte(`"type":"user"`)):
			// Most user records are tool results, some with huge output: skip them
			// before decoding. (In prompt text the quotes are escaped, so this
			// only matches the block type.)
			if !bytes.Contains(line, []byte(`"type":"tool_result"`)) {
				pk.claudeUser(line)
			}
		}
		// /model logs "Set model to `Opus 5.5`"; until the next reply that is the newest model.
		if i := bytes.Index(line, []byte("Set model to `")); i >= 0 && bytes.Contains(line, []byte(`"subtype":"local_command"`)) {
			rest := line[i+len("Set model to `"):]
			if j := bytes.IndexByte(rest, '`'); j > 0 {
				d.Model = string(rest[:j])
			}
		}
		if d.Started.IsZero() && bytes.Contains(line, []byte(`"timestamp":"`)) {
			var r struct {
				Timestamp time.Time `json:"timestamp"`
			}
			if json.Unmarshal(line, &r) == nil {
				d.Started = r.Timestamp
			}
		}
	}
	if err := sc.Err(); err != nil {
		return d, err
	}
	if costState != nil {
		d.Cost, d.HasCost = costState.TotalCostUSD, true
		if costState.StartTime > 0 {
			d.Started = time.UnixMilli(costState.StartTime)
		}
		for _, u := range costState.ModelUsage {
			d.Split.In += u.In
			d.Split.Out += u.Out
			d.Split.Cache += u.CacheRead + u.CacheCreate
		}
		// A resumed session keeps going after the snapshot; add what came since.
		// The cost stays the snapshot's, since prices aren't known here.
		for _, u := range after {
			d.Split.In += u.In
			d.Split.Out += u.Out
			d.Split.Cache += u.CacheRead + u.CacheCreate
		}
	} else {
		for _, u := range byRequest {
			d.Split.In += u.In
			d.Split.Out += u.Out
			d.Split.Cache += u.CacheRead + u.CacheCreate
		}
	}
	d.HasSplit = true
	d.Total = d.Split.In + d.Split.Out + d.Split.Cache
	d.Peek = pk
	return d, nil
}

// codexDetails reads the threads row; codex records a token total but no cost
// and no in/out split.
func codexDetails(s Session, peek bool) (Details, error) {
	d := Details{Location: codexDB()}
	if len(s.Files) > 0 {
		d.Location = s.Files[0]
	}
	db, err := sql.Open("sqlite", "file:"+codexDB()+"?mode=ro&_pragma=busy_timeout(2000)")
	if err != nil {
		return d, err
	}
	defer db.Close()
	var created, tokens sql.NullInt64
	var model sql.NullString
	err = db.QueryRow(`SELECT created_at_ms, model, tokens_used FROM threads WHERE id = ?`, s.ID).Scan(&created, &model, &tokens)
	if err != nil {
		return d, err
	}
	d.Started = time.UnixMilli(created.Int64)
	d.Model = model.String
	// Paginated threads' rollout may be a stale legacy file; their sqlite model is current.
	if len(s.Files) > 0 && !s.historyDB {
		if m := codexRolloutModel(s.Files[0]); m != "" {
			d.Model = m // the rollout logs /model switches; the sqlite column can lag
		}
	}
	d.Total = tokens.Int64 // codex stores only a total
	// shortcut: a paginated thread's rollout is read as the current conversation
	// (13 of 15 on Codex 0.166 matched the history database, none was behind it);
	// upgrade to the thread_items rows if a stale rollout shows up.
	if peek && len(s.Files) > 0 {
		d.Peek = codexPeek(s.Files[0])
	}
	return d, nil
}

// codexRolloutModel returns the model of the last thread_settings_applied event.
func codexRolloutModel(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	model := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		if !bytes.Contains(sc.Bytes(), []byte(`"thread_settings_applied"`)) {
			continue
		}
		var r struct {
			Payload struct {
				ThreadSettings struct {
					Model string `json:"model"`
				} `json:"thread_settings"`
			} `json:"payload"`
		}
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.Payload.ThreadSettings.Model != "" {
			model = r.Payload.ThreadSettings.Model
		}
	}
	return model
}
