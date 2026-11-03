package agents

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// TrashEntry is one deleted session in ~/.muxlair/trash.
type TrashEntry struct {
	Dir  string
	Meta trashMeta
}

func (e TrashEntry) Restored() bool { return e.Meta.RestoredAt != nil }

// Restorable: a Codex session deleted while its session file was already gone
// (it showed as dead) has only database rows saved, which can't be put back.
// Files listed in meta.json must also be there: a failed delete can leave a
// partial entry.
func (e TrashEntry) Restorable() bool {
	for name := range e.Meta.Files {
		if _, err := os.Lstat(filepath.Join(e.Dir, name)); err != nil {
			return false
		}
	}
	return len(e.Meta.Files) > 0
}

// ListTrash returns the trash entries, newest first. An entry whose meta.json
// can't be read is skipped and reported in the error.
func ListTrash() ([]TrashEntry, error) {
	metas, _ := filepath.Glob(filepath.Join(TrashDir(), "*", "*", "meta.json"))
	var out []TrashEntry
	var errs []error
	for _, f := range metas {
		var m trashMeta
		b, err := os.ReadFile(f)
		if err == nil {
			err = json.Unmarshal(b, &m)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", f, err))
			continue
		}
		out = append(out, TrashEntry{filepath.Dir(f), m})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Meta.DeletedAt.After(out[j].Meta.DeletedAt) })
	return out, errors.Join(errs...)
}

// Restore puts a trashed session back where its agent expects it and returns
// notes worth showing. The newest entry for the session that is not yet
// restored is used. Nothing is overwritten: if any destination exists, nothing
// is touched. A restored session is added to the keep-list, so auto-delete
// never trashes it again.
func Restore(agent, id string) ([]string, error) {
	entries, err := ListTrash()
	var e *TrashEntry
	restored := false
	for i := range entries {
		if entries[i].Meta.Agent != agent || entries[i].Meta.ID != id {
			continue
		}
		if entries[i].Restored() {
			restored = true
		} else if e == nil {
			e = &entries[i]
		}
	}
	if e == nil {
		if restored {
			return nil, fmt.Errorf("%s %s was already restored (its backup stays in the trash)", agent, id)
		}
		if err != nil {
			return nil, err // the entry may be among the unreadable ones
		}
		return nil, fmt.Errorf("no %s session %s in the trash (see: muxlair trash)", agent, id)
	}
	var notes []string
	switch agent {
	case "claude":
		err = restoreClaude(*e)
	case "codex":
		notes, err = restoreCodex(*e)
	default:
		err = errors.New("restoring " + agent + " sessions is not supported")
	}
	if err != nil {
		return nil, err
	}
	return notes, nil
}

// restoreClaude moves the files back. Claude reads its own files, so no
// registration is needed. It holds the session lock and marks the session
// before moving anything: a running muxlair could otherwise auto-delete a
// restored headless run (its old mtime makes it look idle) before the mark.
func restoreClaude(e TrashEntry) error {
	return WithSessionLock(func() error {
		for name, dst := range e.Meta.Files {
			if _, err := os.Lstat(filepath.Join(e.Dir, name)); err != nil {
				return fmt.Errorf("the trash entry is missing %s", name)
			}
			if _, err := os.Lstat(dst); err == nil {
				return fmt.Errorf("%s already exists; not overwriting it", dst)
			}
		}
		// A key left behind by a failed restore only protects; it is harmless.
		if err := markInteractive(e.Meta.Agent + ":" + e.Meta.ID); err != nil {
			return err
		}
		names := make([]string, 0, len(e.Meta.Files))
		for name := range e.Meta.Files {
			names = append(names, name)
		}
		sort.Strings(names)   // a fixed order, so a failure rolls back the same way every time
		var moved [][2]string // src, dst
		for _, name := range names {
			dst := e.Meta.Files[name]
			src := filepath.Join(e.Dir, name)
			err := os.MkdirAll(filepath.Dir(dst), 0o755)
			if err == nil {
				err = moveNoReplace(src, dst)
			}
			if err != nil {
				for i := len(moved) - 1; i >= 0; i-- { // back into the trash, so a retry can work
					os.Rename(moved[i][1], moved[i][0])
				}
				return err
			}
			moved = append(moved, [2]string{src, dst})
		}
		os.Remove(filepath.Join(e.Dir, "meta.json")) // all moved out; the entry is now empty
		os.Remove(e.Dir)
		return nil
	})
}

// moveNoReplace moves src to dst and fails if dst exists, even if it appeared
// after an earlier check: rename would silently replace a file, a hard link
// refuses.
// shortcut: a directory is moved with rename, which would replace an empty
// directory at dst; upgrade to renameat2/renamex_np if that ever matters.
func moveNoReplace(src, dst string) error {
	if st, err := os.Lstat(src); err == nil && st.IsDir() {
		return os.Rename(src, dst)
	}
	if err := os.Link(src, dst); err != nil {
		return err
	}
	return os.Remove(src)
}

// rolloutName is a rollout file name: rollout-<date>T<time>-<thread id>.jsonl.
var rolloutName = regexp.MustCompile(`^rollout-(\d{4})-(\d{2})-(\d{2})T[\d-]+-([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.jsonl$`)

// helperTarget gives where a helper thread's rollout belongs and its thread id,
// from its file name alone: codex files them under sessions/YYYY/MM/DD/.
func helperTarget(base string) (path, id string, ok bool) {
	m := rolloutName.FindStringSubmatch(base)
	if m == nil {
		return "", "", false
	}
	return filepath.Join(CodexHome(), "sessions", m[1], m[2], m[3], base), m[4], true
}

// restoreCodex copies the rollout files back, then has Codex itself pick them
// up: reading a thread through the app server makes Codex register a rollout
// it has no row for. muxlair never writes Codex's database.
func restoreCodex(e TrashEntry) ([]string, error) {
	type item struct{ src, dst, id string }
	var items []item
	for name, dst := range e.Meta.Files {
		items = append(items, item{filepath.Join(e.Dir, name), dst, e.Meta.ID})
	}
	if len(items) == 0 {
		return nil, errors.New("this entry has no session file: it was already gone when the session was deleted (it showed as dead), so Codex couldn't reopen it then either. Only its readable history (history.jsonl) was saved")
	}
	helpers, _ := filepath.Glob(filepath.Join(e.Dir, "helper-rollout-*.jsonl"))
	for _, h := range helpers {
		dst, id, ok := helperTarget(strings.TrimPrefix(filepath.Base(h), "helper-"))
		if !ok {
			return nil, fmt.Errorf("can't tell where %s belongs", filepath.Base(h))
		}
		items = append(items, item{h, dst, id})
	}
	var notes []string
	err := WithSessionLock(func() error {
		for _, it := range items {
			if _, err := os.Stat(it.src); err != nil {
				return fmt.Errorf("the trash entry is missing %s", filepath.Base(it.src))
			}
			if _, err := os.Lstat(it.dst); err == nil {
				return fmt.Errorf("%s already exists; not overwriting it", it.dst)
			}
		}
		// Mark before any file appears (see restoreClaude). Only the session: a
		// kept helper would be skipped when the session is deleted later, and
		// hidden helpers are never auto-deleted anyway. A key left behind by a
		// failed restore only protects; it is harmless.
		if err := markInteractive("codex:" + e.Meta.ID); err != nil {
			return err
		}
		var copied []string
		undo := func() {
			for _, f := range copied {
				os.Remove(f)
			}
		}
		for _, it := range items {
			if err := os.MkdirAll(filepath.Dir(it.dst), 0o755); err != nil {
				undo()
				return err
			}
			if err := copyFile(it.src, it.dst); err != nil {
				undo()
				return err
			}
			copied = append(copied, it.dst)
		}
		// Registering the session itself is the one step that must work. Once it
		// has, its file must stay, so everything after it only adds notes.
		if err := codexRPC(rpcCall{"thread/read", map[string]any{"threadId": e.Meta.ID}}); err != nil {
			undo() // nothing was registered
			return err
		}
		if codexRollout(e.Meta.ID) == "" {
			undo()
			return errors.New("Codex did not register the restored session")
		}
		var rest []rpcCall
		name, turns := historyInfo(e.Dir)
		if name != "" { // the name lives in Codex's database, not the rollout
			rest = append(rest, rpcCall{"thread/name/set", map[string]string{"threadId": e.Meta.ID, "name": name}})
		}
		for _, it := range items[1:] { // helpers, which listing hides anyway
			rest = append(rest, rpcCall{"thread/read", map[string]any{"threadId": it.id}})
		}
		if len(rest) > 0 {
			if err := codexRPC(rest...); err != nil {
				notes = append(notes, "restored the session, but not all of its name and helper sessions: "+err.Error())
			}
		}
		// Codex keeps the turns of newer sessions in its history database too. Warn
		// if some finished turns recorded there are not in the rollout we put back.
		if miss := missingTurns(items[0].dst, turns); miss > 0 {
			notes = append(notes, fmt.Sprintf("%d of %d finished turns in Codex's history database are not in the session file, so the restored conversation may be missing them. The full history is in %s.", miss, len(turns), filepath.Join(e.Dir, "history.jsonl")))
		}
		now := time.Now()
		e.Meta.RestoredAt = &now
		if err := writeMeta(e.Dir, e.Meta); err != nil {
			notes = append(notes, "restored, but could not mark the trash entry: "+err.Error())
		}
		return nil
	})
	return notes, err
}

// historyInfo reads the thread's name and the ids of its finished turns from
// the history.jsonl that Trash exported: it holds the rows of Codex's threads
// and thread_turns tables.
func historyInfo(dir string) (name string, turns []string) {
	f, err := os.Open(filepath.Join(dir, "history.jsonl"))
	if err != nil {
		return "", nil
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.Contains(line, []byte(`"table":"threads"`)) && !bytes.Contains(line, []byte(`"table":"thread_turns"`)) {
			continue
		}
		var r struct {
			Table string `json:"table"`
			Row   struct {
				Name   string `json:"name"`
				Status string `json:"status"`
				TurnID string `json:"turn_id"`
			} `json:"row"`
		}
		if json.Unmarshal(line, &r) != nil {
			continue
		}
		switch {
		case r.Table == "threads":
			name = r.Row.Name
		case r.Table == "thread_turns" && r.Row.Status == "completed" && r.Row.TurnID != "":
			turns = append(turns, r.Row.TurnID)
		}
	}
	return name, turns
}

// missingTurns counts the turn ids whose completion the rollout does not record.
// shortcut: only completed turns are checked, not failed or interrupted ones, and a
// rollout with a turn's completion but not all its messages goes unnoticed; upgrade
// to comparing the saved items if an incomplete restore shows up.
func missingTurns(path string, ids []string) int {
	if len(ids) == 0 {
		return 0
	}
	f, err := os.Open(path)
	if err != nil {
		return len(ids)
	}
	defer f.Close()
	left := map[string]bool{}
	for _, id := range ids {
		left[id] = true
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() && len(left) > 0 {
		if !bytes.Contains(sc.Bytes(), []byte(`"type":"task_complete"`)) {
			continue // a turn only counts once its completion is recorded
		}
		for id := range left {
			if bytes.Contains(sc.Bytes(), []byte(id)) {
				delete(left, id)
			}
		}
	}
	return len(left)
}
