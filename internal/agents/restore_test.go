package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// trashedClaude creates a Claude session "abc", trashes it, and returns its original transcript path.
func trashedClaude(t *testing.T) string {
	t.Helper()
	Home = t.TempDir()
	proj := filepath.Join(Home, ".claude", "projects", "-p")
	os.MkdirAll(filepath.Join(proj, "abc", "subagents"), 0o700)
	jsonl := filepath.Join(proj, "abc.jsonl")
	os.WriteFile(jsonl, []byte(`{"type":"user","cwd":"/p"}`+"\n"), 0o600)
	ss, err := listClaude()
	if err != nil || len(ss) != 1 {
		t.Fatalf("list: %v %+v", err, ss)
	}
	if _, err := Trash(ss[0]); err != nil {
		t.Fatal(err)
	}
	return jsonl
}

func TestRestoreClaude(t *testing.T) {
	jsonl := trashedClaude(t)
	es, err := ListTrash()
	if err != nil || len(es) != 1 || es[0].Meta.ID != "abc" || es[0].Restored() {
		t.Fatalf("trash: %v %+v", err, es)
	}
	if _, err := Restore("claude", "abc"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{jsonl, strings.TrimSuffix(jsonl, ".jsonl") + "/subagents"} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("not restored: %v", err)
		}
	}
	if es, _ := ListTrash(); len(es) != 0 {
		t.Fatalf("entry left in the trash: %+v", es)
	}
	// Back on disk and never auto-deleted, even if it was a headless run.
	if keys, _ := interactiveKeys(); !keys["claude:abc"] {
		t.Fatal("restored session is not on the keep-list")
	}
	if _, err := Restore("claude", "abc"); err == nil || !strings.Contains(err.Error(), "in the trash") {
		t.Fatalf("second restore: %v", err)
	}
}

func TestRestoreClaudeNeverOverwrites(t *testing.T) {
	jsonl := trashedClaude(t)
	os.MkdirAll(filepath.Dir(jsonl), 0o700)
	os.WriteFile(jsonl, []byte("new"), 0o600) // a new session took the same id
	if _, err := Restore("claude", "abc"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("got %v", err)
	}
	if b, _ := os.ReadFile(jsonl); string(b) != "new" {
		t.Fatal("overwrote the existing file")
	}
	if es, _ := ListTrash(); len(es) != 1 {
		t.Fatal("trash entry was consumed by a refused restore")
	}
}

func TestListTrashNewestFirstAndSkipsBadMeta(t *testing.T) {
	Home = t.TempDir()
	put := func(dir, body string) {
		p := filepath.Join(TrashDir(), dir)
		os.MkdirAll(p, 0o700)
		os.WriteFile(filepath.Join(p, "meta.json"), []byte(body), 0o600)
	}
	old, recent := time.Now().Add(-time.Hour), time.Now()
	for dir, at := range map[string]time.Time{"claude/a": old, "codex/b": recent} {
		b, _ := json.Marshal(trashMeta{Agent: strings.Split(dir, "/")[0], ID: dir[len(dir)-1:], DeletedAt: at})
		put(dir, string(b))
	}
	put("claude/bad", "{not json")
	es, err := ListTrash()
	if err == nil {
		t.Fatal("bad meta.json not reported")
	}
	if len(es) != 2 || es[0].Meta.ID != "b" || es[1].Meta.ID != "a" {
		t.Fatalf("order: %+v", es)
	}
}

func TestHelperTarget(t *testing.T) {
	t.Setenv("CODEX_HOME", "/c")
	p, id, ok := helperTarget("rollout-2026-11-03T18-01-20-01a1a25b-b0b4-7961-8617-061b3dd637b8.jsonl")
	if !ok || id != "01a1a25b-b0b4-7961-8617-061b3dd637b8" ||
		p != "/c/sessions/2026/11/03/rollout-2026-11-03T18-01-20-01a1a25b-b0b4-7961-8617-061b3dd637b8.jsonl" {
		t.Fatalf("got %q %q %v", p, id, ok)
	}
	if _, _, ok := helperTarget("history-x.jsonl"); ok {
		t.Fatal("accepted a non-rollout name")
	}
}

func TestHistoryInfo(t *testing.T) {
	dir := t.TempDir()
	rows := `{"db":"state_5.sqlite","table":"thread_spawn_edges","row":{"name":"no"}}` + "\n" +
		`{"db":"state_5.sqlite","table":"threads","row":{"name":"My thread"}}` + "\n" +
		`{"db":"thread_history_1.sqlite","table":"thread_turns","row":{"status":"inProgress","turn_id":"stale"}}` + "\n" + // a stale turn doesn't count
		`{"db":"thread_history_1.sqlite","table":"thread_turns","row":{"status":"completed","turn_id":"t1"}}` + "\n" +
		`{"db":"thread_history_1.sqlite","table":"thread_turns","row":{"status":"completed","turn_id":"t2"}}` + "\n"
	os.WriteFile(filepath.Join(dir, "history.jsonl"), []byte(rows), 0o600)
	if n, ids := historyInfo(dir); n != "My thread" || strings.Join(ids, ",") != "t1,t2" {
		t.Fatalf("got %q %v", n, ids)
	}
	if n, ids := historyInfo(t.TempDir()); n != "" || ids != nil {
		t.Fatal("missing file should give nothing")
	}
}

// Equal counts are not enough: the rollout must hold the same turns.
func TestMissingTurns(t *testing.T) {
	f := filepath.Join(t.TempDir(), "r.jsonl")
	os.WriteFile(f, []byte(`{"payload":{"type":"task_started","turn_id":"t2"}}`+"\n"+`{"payload":{"type":"task_complete","turn_id":"t3"}}`+"\n"+
		`{"payload":{"type":"task_started","turn_id":"t4"}}`+"\n"), 0o600) // t2, t4: started, never completed
	if n := missingTurns(f, []string{"t3"}); n != 0 {
		t.Fatalf("completed turn reported missing: %d", n)
	}
	if n := missingTurns(f, []string{"t2", "t3", "t4"}); n != 2 {
		t.Fatalf("started-only turns must count as missing, got %d", n)
	}
	if n := missingTurns(f, []string{"t1", "t3"}); n != 1 { // a different turn than the rollout's
		t.Fatalf("got %d missing, want 1", n)
	}
	if n := missingTurns(filepath.Join(t.TempDir(), "none"), []string{"t1"}); n != 1 {
		t.Fatal("a missing file has every turn missing")
	}
	if n := missingTurns(f, nil); n != 0 {
		t.Fatal("nothing to find")
	}
}

// codexEntry writes a trash entry for codex thread id with a rollout file, and
// returns where the rollout must go back to.
func codexEntry(t *testing.T, withFile bool) (dst string) {
	t.Helper()
	Home = t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(Home, ".codex"))
	id := "01a1a25b-b0b4-7961-8617-061b3dd637b8"
	dst = filepath.Join(CodexHome(), "sessions", "2026", "11", "03", "rollout-2026-11-03T18-01-20-"+id+".jsonl")
	dir := filepath.Join(TrashDir(), "codex", "20261103-180000-"+id)
	os.MkdirAll(dir, 0o700)
	m := trashMeta{Agent: "codex", ID: id, DeletedAt: time.Now(), Files: map[string]string{}}
	if withFile {
		m.Files["0-"+filepath.Base(dst)] = dst
		os.WriteFile(filepath.Join(dir, "0-"+filepath.Base(dst)), []byte(`{"type":"session_meta"}`+"\n"), 0o600)
	}
	writeMeta(dir, m)
	return dst
}

func TestRestoreCodexWithoutFile(t *testing.T) {
	codexEntry(t, false)
	if _, err := Restore("codex", "01a1a25b-b0b4-7961-8617-061b3dd637b8"); err == nil || !strings.Contains(err.Error(), "no session file") {
		t.Fatalf("got %v", err)
	}
}

func TestRestoreCodexRefusesExisting(t *testing.T) {
	dst := codexEntry(t, true)
	os.MkdirAll(filepath.Dir(dst), 0o755)
	os.WriteFile(dst, []byte("keep"), 0o600)
	if _, err := Restore("codex", "01a1a25b-b0b4-7961-8617-061b3dd637b8"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("got %v", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "keep" {
		t.Fatal("overwrote the existing rollout")
	}
}

// A failing codex must leave nothing behind: the copied rollout is removed again.
func TestRestoreCodexUndoesWhenCodexFails(t *testing.T) {
	dst := codexEntry(t, true)
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nexit 1\n"), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := Restore("codex", "01a1a25b-b0b4-7961-8617-061b3dd637b8"); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("rollout left in place after a failed restore")
	}
	if es, _ := ListTrash(); len(es) != 1 || es[0].Restored() {
		t.Fatalf("trash entry changed: %+v", es)
	}
}

func TestRestoreClaudeRollsBackOnFailure(t *testing.T) {
	Home = t.TempDir()
	dir := filepath.Join(TrashDir(), "claude", "20261103-180000-x")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "0-a"), []byte("a"), 0o600)
	os.WriteFile(filepath.Join(dir, "1-b"), []byte("b"), 0o600)
	good := filepath.Join(Home, "out", "a")
	blocker := filepath.Join(Home, "blocker") // a file, so a path below it can't be created
	os.WriteFile(blocker, nil, 0o600)
	writeMeta(dir, trashMeta{Agent: "claude", ID: "x", DeletedAt: time.Now(),
		Files: map[string]string{"0-a": good, "1-b": filepath.Join(blocker, "b")}})

	if _, err := Restore("claude", "x"); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := os.Stat(good); !os.IsNotExist(err) {
		t.Fatal("first file left outside the trash")
	}
	for _, f := range []string{"0-a", "1-b", "meta.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("%s not back in the trash entry: %v", f, err)
		}
	}
	// Once the problem is fixed, a retry works.
	os.Remove(blocker)
	if _, err := Restore("claude", "x"); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

func TestCopyFileRemovesPartialOutput(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "out")
	if err := copyFile(t.TempDir(), dst); err == nil { // reading a directory fails after dst is created
		t.Fatal("expected an error")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("partial copy left behind")
	}
	// An existing destination must survive an O_EXCL failure.
	os.WriteFile(dst, []byte("keep"), 0o600)
	src := filepath.Join(t.TempDir(), "s")
	os.WriteFile(src, []byte("x"), 0o600)
	if err := copyFile(src, dst); err == nil {
		t.Fatal("expected an error")
	}
	if b, _ := os.ReadFile(dst); string(b) != "keep" {
		t.Fatal("removed a pre-existing file")
	}
}

func TestMoveNoReplace(t *testing.T) {
	d := t.TempDir()
	src, dst := filepath.Join(d, "src"), filepath.Join(d, "dst")
	os.WriteFile(src, []byte("new"), 0o600)
	os.WriteFile(dst, []byte("keep"), 0o600)
	if err := moveNoReplace(src, dst); err == nil {
		t.Fatal("replaced an existing file")
	}
	if b, _ := os.ReadFile(dst); string(b) != "keep" {
		t.Fatal("existing file changed")
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatal("source lost after a refused move")
	}
	os.Remove(dst)
	if err := moveNoReplace(src, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("source still there")
	}
	// A directory moves too.
	sd := filepath.Join(d, "sd")
	os.MkdirAll(filepath.Join(sd, "x"), 0o700)
	if err := moveNoReplace(sd, filepath.Join(d, "dd")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d, "dd", "x")); err != nil {
		t.Fatal(err)
	}
}

func TestRestorable(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "0-a"), []byte("x"), 0o600)
	full := TrashEntry{dir, trashMeta{Files: map[string]string{"0-a": "/o/a"}}}
	partial := TrashEntry{dir, trashMeta{Files: map[string]string{"0-a": "/o/a", "1-b": "/o/b"}}} // 1-b never arrived
	none := TrashEntry{dir, trashMeta{}}                                                          // dead codex session: rows only
	if !full.Restorable() || partial.Restorable() || none.Restorable() {
		t.Fatalf("full=%v partial=%v none=%v", full.Restorable(), partial.Restorable(), none.Restorable())
	}
}

// RestoreEntry restores the entry it is given, not the newest one for the session.
func TestRestoreEntryPicksTheGivenEntry(t *testing.T) {
	Home = t.TempDir()
	dst := filepath.Join(Home, ".claude", "projects", "-p", "abc.jsonl")
	put := func(stamp, body string, at time.Time) {
		dir := filepath.Join(TrashDir(), "claude", stamp+"-abc")
		os.MkdirAll(dir, 0o700)
		os.WriteFile(filepath.Join(dir, "0-abc.jsonl"), []byte(body), 0o600)
		writeMeta(dir, trashMeta{Agent: "claude", ID: "abc", DeletedAt: at, Files: map[string]string{"0-abc.jsonl": dst}})
	}
	put("20261101-100000", "old", time.Now().Add(-48*time.Hour))
	put("20261103-100000", "new", time.Now())
	es, err := ListTrash()
	if err != nil || len(es) != 2 || es[1].Meta.DeletedAt.After(es[0].Meta.DeletedAt) {
		t.Fatalf("list: %v %+v", err, es)
	}
	if _, err := RestoreEntry(es[1]); err != nil { // the older one
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "old" {
		t.Fatalf("restored %q, want the selected (older) entry's file", b)
	}
	if left, _ := ListTrash(); len(left) != 1 || left[0].Meta.DeletedAt.Before(es[0].Meta.DeletedAt) {
		t.Fatalf("the newer entry should remain in the trash: %+v", left)
	}
	done := es[1]
	now := time.Now()
	done.Meta.RestoredAt = &now
	if _, err := RestoreEntry(done); err == nil || !strings.Contains(err.Error(), "already restored") {
		t.Fatalf("restoring a restored entry: %v", err)
	}
}
