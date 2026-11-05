package agents

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// exportOutputMax caps one command's output in the export (bytes).
const exportOutputMax = 4000

// ExportCodex renders a trashed Codex session as markdown from the rows that
// Trash saved in history.jsonl. It is for sessions whose rollout was already
// gone when they were deleted ("dead"): the rows are all that is left of them.
// The newest trash entry for the id is used, restored or not. The result is a
// readable, possibly partial export, not a transcript: Codex may have lost the
// messages before the delete, and long command output is cut.
func ExportCodex(id string) (string, error) {
	entries, err := ListTrash() // looked up by id, never joined into a path
	var e *TrashEntry
	for i := range entries {
		if entries[i].Meta.Agent == "codex" && entries[i].Meta.ID == id {
			e = &entries[i] // newest first
			break
		}
	}
	if e == nil {
		if err != nil {
			return "", err
		}
		return "", fmt.Errorf("no codex session %s in the trash (see: muxlair trash)", id)
	}
	thread, items, err := readHistory(filepath.Join(e.Dir, "history.jsonl"))
	if err != nil {
		return "", fmt.Errorf("reading the saved history: %w", err)
	}
	return renderExport(*e, thread, items), nil
}

type histItem struct {
	ordinal float64
	typ     string
	json    string
}

func readHistory(path string) (thread map[string]any, items []histItem, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 256<<20)
	for sc.Scan() {
		line := sc.Bytes()
		isThread := bytes.Contains(line, []byte(`"table":"threads"`))
		if !isThread && !bytes.Contains(line, []byte(`"table":"thread_items"`)) {
			continue
		}
		var r struct {
			Table string         `json:"table"`
			Row   map[string]any `json:"row"`
		}
		if json.Unmarshal(line, &r) != nil {
			continue
		}
		switch r.Table {
		case "threads":
			thread = r.Row
		case "thread_items":
			it := histItem{typ: str(r.Row["item_type"]), json: str(r.Row["item_json"])}
			it.ordinal, _ = r.Row["rollout_ordinal"].(float64)
			items = append(items, it)
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].ordinal < items[j].ordinal })
	return thread, items, sc.Err()
}

// str reads a text column; a non-UTF-8 one was saved as {"base64": ...} and is not shown.
func str(v any) string {
	s, _ := v.(string)
	return s
}

func renderExport(e TrashEntry, thread map[string]any, items []histItem) string {
	var b strings.Builder
	name := firstNonEmpty(str(thread["name"]), str(thread["title"]), e.Meta.Name, "(no name)")
	fmt.Fprintf(&b, "# %s\n\n", safeLine(name))
	fmt.Fprintf(&b, "- Agent: codex\n- ID: %s\n", safeLine(e.Meta.ID))
	if cwd := firstNonEmpty(str(thread["cwd"]), e.Meta.Cwd); cwd != "" {
		fmt.Fprintf(&b, "- Folder: %s\n", safeLine(cwd))
	}
	if m := str(thread["model"]); m != "" {
		fmt.Fprintf(&b, "- Model: %s\n", safeLine(m))
	}
	if t, ok := thread["created_at"].(float64); ok && t > 0 {
		fmt.Fprintf(&b, "- Created: %s\n", time.Unix(int64(t), 0).Local().Format("2006-01-02 15:04"))
	}
	fmt.Fprintf(&b, "- Deleted: %s\n\n", e.Meta.DeletedAt.Local().Format("2006-01-02 15:04"))
	b.WriteString("_Readable export of the history muxlair saved when this session was deleted. It can be partial: Codex may have lost messages, and long command output is cut._\n")
	chat := 0
	for _, it := range items {
		if s := renderItem(it); s != "" {
			b.WriteString("\n" + s)
			if it.typ == "userMessage" || it.typ == "agentMessage" {
				chat++
			}
		}
	}
	if chat == 0 {
		b.WriteString("\nNo messages were saved for this session.\n")
	}
	return b.String()
}

func renderItem(it histItem) string {
	var j map[string]any
	if json.Unmarshal([]byte(it.json), &j) != nil {
		return "_(unreadable " + safeLine(it.typ) + " item)_\n"
	}
	switch it.typ {
	case "userMessage":
		var parts []string
		content, _ := j["content"].([]any)
		for _, c := range content {
			m, _ := c.(map[string]any)
			if t := str(m["text"]); t != "" {
				parts = append(parts, clean(t))
			} else if ty := str(m["type"]); ty != "" {
				parts = append(parts, "_("+safeLine(ty)+")_")
			}
		}
		return "## You\n\n" + strings.Join(parts, "\n\n") + "\n"
	case "agentMessage":
		return "## Codex\n\n" + clean(str(j["text"])) + "\n"
	case "commandExecution":
		cmd := "$ " + clean(str(j["command"]))
		if out := cut(clean(str(j["aggregatedOutput"])), exportOutputMax); out != "" {
			cmd += "\n" + out
		}
		if x, ok := j["exitCode"].(float64); ok && x != 0 {
			cmd += fmt.Sprintf("\n(exit %d)", int(x))
		}
		return fence(cmd) + "\n"
	case "webSearch":
		return "_web search: " + safeLine(str(j["query"])) + "_\n"
	case "reasoning":
		return "_(reasoning)_\n"
	default:
		return "_(" + safeLine(it.typ) + ")_\n"
	}
}

// clean drops control characters (terminal escapes, bell, CR, C1) except newline
// and tab, so saved text can't drive the terminal that prints it.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			return -1
		}
		return r
	}, s)
}

func safeLine(s string) string {
	return strings.Join(strings.Fields(clean(s)), " ")
}

// cut keeps the first max bytes of s on a rune boundary.
func cut(s string, max int) string {
	if len(s) <= max {
		return s
	}
	n := max
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + fmt.Sprintf("\n… %d more bytes cut", len(s)-n)
}

// fence wraps s in a code block whose fence is longer than any backtick run in s.
func fence(s string) string {
	run, longest := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	f := strings.Repeat("`", max(3, longest+1))
	return f + "\n" + s + "\n" + f
}
