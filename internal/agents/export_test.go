package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func putCodexHistory(t *testing.T, id, meta, rows string) {
	t.Helper()
	dir := filepath.Join(TrashDir(), "codex", "2026-11-04-"+id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o600)
	os.WriteFile(filepath.Join(dir, "history.jsonl"), []byte(rows), 0o600)
}

func TestExportCodex(t *testing.T) {
	Home = t.TempDir()
	// Rows are saved out of order, with a terminal escape, backticks and a long output.
	rows := `{"db":"state_5.sqlite","table":"threads","row":{"name":null,"title":"The title","cwd":"/work/x","model":"gpt-x","created_at":1700000000}}` + "\n" +
		`{"db":"h","table":"thread_items","row":{"rollout_ordinal":3,"item_type":"commandExecution","item_json":"{\"command\":\"echo ` + "```" + `\",\"aggregatedOutput\":\"` + strings.Repeat("x", 5000) + `\",\"exitCode\":2}"}}` + "\n" +
		`{"db":"h","table":"thread_items","row":{"rollout_ordinal":2,"item_type":"agentMessage","item_json":"{\"text\":\"done\\u001b[31m red\"}"}}` + "\n" +
		`{"db":"h","table":"thread_items","row":{"rollout_ordinal":1,"item_type":"userMessage","item_json":"{\"content\":[{\"type\":\"text\",\"text\":\"hello\"}]}"}}` + "\n" +
		`{"db":"h","table":"thread_items","row":{"rollout_ordinal":4,"item_type":"mystery","item_json":"not json"}}` + "\n"
	putCodexHistory(t, "abc", `{"agent":"codex","id":"abc","name":"meta name","cwd":"/work/y","deletedAt":"2026-11-04T10:00:00Z"}`, rows)
	out, err := ExportCodex("abc")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# The title", "- Folder: /work/x", "- Model: gpt-x", "## You\n\nhello", "## Codex\n\ndone[31m red", "(exit 2)", "more bytes cut"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.ContainsRune(out, 0x1b) {
		t.Error("terminal escape survived")
	}
	if strings.Index(out, "## You") > strings.Index(out, "## Codex") || strings.Index(out, "## Codex") > strings.Index(out, "$ echo") {
		t.Error("items not in rollout order")
	}
	if !strings.Contains(out, "````\n$ echo ```") { // fence longer than the backtick run inside
		t.Errorf("fence not widened:\n%s", out)
	}
	if strings.Contains(out, "No messages were saved") {
		t.Error("claimed no messages")
	}
	if !strings.Contains(out, "unreadable mystery") {
		t.Error("bad item not flagged")
	}
}

func TestExportCodexWithoutMessagesAndMissing(t *testing.T) {
	Home = t.TempDir()
	putCodexHistory(t, "empty", `{"agent":"codex","id":"empty","name":"from meta","deletedAt":"2026-11-04T10:00:00Z"}`,
		`{"db":"state_5.sqlite","table":"threads","row":{"name":"","title":""}}`+"\n")
	out, err := ExportCodex("empty")
	if err != nil || !strings.Contains(out, "# from meta") || !strings.Contains(out, "No messages were saved") {
		t.Fatalf("got %v:\n%s", err, out)
	}
	for _, id := range []string{"nope", "../empty", ""} {
		if _, err := ExportCodex(id); err == nil {
			t.Errorf("%q: no error", id)
		}
	}
}

func TestExportCodexNewestEntryWins(t *testing.T) {
	Home = t.TempDir()
	mk := func(dir, at, name string) {
		p := filepath.Join(TrashDir(), "codex", dir)
		os.MkdirAll(p, 0o700)
		os.WriteFile(filepath.Join(p, "meta.json"), []byte(`{"agent":"codex","id":"dup","deletedAt":"`+at+`"}`), 0o600)
		os.WriteFile(filepath.Join(p, "history.jsonl"), []byte(`{"table":"threads","row":{"name":"`+name+`"}}`+"\n"), 0o600)
	}
	mk("a-dup", "2026-11-01T10:00:00Z", "old")
	mk("b-dup", "2026-11-03T10:00:00Z", "new")
	if out, _ := ExportCodex("dup"); !strings.HasPrefix(out, "# new") {
		t.Fatalf("got:\n%s", out)
	}
}
