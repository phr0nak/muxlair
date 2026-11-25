package agents

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func agyFixture(t *testing.T) {
	t.Helper()
	Home = t.TempDir()
	home := agyHome()
	for _, d := range []string{"annotations", "brain/c1/.system_generated/logs"} {
		os.MkdirAll(filepath.Join(home, d), 0o700)
	}
	db, err := sql.Open("sqlite", "file:"+agyDB())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE conversation_summaries (conversation_id TEXT, title TEXT, preview TEXT, step_count INT,
			last_modified_time TEXT, workspace_uris TEXT, status TEXT, parent_conversation_id TEXT)`,
		`INSERT INTO conversation_summaries VALUES ('c1', 'Agy title', 'p', 2, '2026-11-24 16:08:34.474586+00:00', '["file:///work/a%20b"]', 'CASCADE_RUN_STATUS_IDLE', '')`,
		`INSERT INTO conversation_summaries VALUES ('c2', '', 'the preview', 4, '2026-11-24 15:00:00+00:00', '["file:///work"]', 'CASCADE_RUN_STATUS_IDLE', NULL)`,
		`INSERT INTO conversation_summaries VALUES ('empty', '', '', 0, '0001-01-01 00:00:00+00:00', '[]', '', '')`,
		`INSERT INTO conversation_summaries VALUES ('sub', 'x', '', 3, '2026-11-24 15:00:00+00:00', '["file:///work"]', '', 'c1')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(home, "annotations", "c1.pbtxt"), []byte("title:\"My \\\"agy\\\" one\"\n"), 0o600)
	os.WriteFile(filepath.Join(home, "brain/c1/.system_generated/logs/transcript.jsonl"), []byte(
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-11-24T16:08:31Z","content":"<USER_REQUEST>\nfix it\n</USER_REQUEST>\n<ADDITIONAL_METADATA>x</ADDITIONAL_METADATA>"}
{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-24T16:08:32Z","model":"gem-1","input_tokens":100,"cache_read_tokens":20,"output_tokens":5,"content":"looking","tool_calls":[{"name":"view_file"}]}
{"step_index":2,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-24T16:08:33Z","model":"gem-2","input_tokens":10,"cache_read_tokens":0,"output_tokens":7,"content":"fixed"}
`), 0o600)
}

func TestListAgy(t *testing.T) {
	agyFixture(t)
	ss, err := listAgy()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Session{}
	for _, s := range ss {
		got[s.ID] = s
	}
	if len(got) != 2 {
		t.Fatalf("empty and subagent conversations must be hidden: %+v", ss)
	}
	c1 := got["c1"]
	if c1.Name != `My "agy" one` || c1.Cwd != "/work/a b" || c1.Updated.IsZero() || len(c1.Files) != 1 || c1.Resume != "cd '/work/a b' && agy --conversation 'c1'" {
		t.Fatalf("c1: %+v", c1)
	}
	if got["c2"].Name != "the preview" || len(got["c2"].Files) != 0 {
		t.Fatalf("c2: %+v", got["c2"])
	}
	d, err := (agyAgent{}).Details(c1, true)
	if err != nil || d.Model != "gem-2" || d.Split != (Tokens{In: 110, Out: 12, Cache: 20}) || d.HasCost || d.Started.IsZero() {
		t.Fatalf("details: %+v %v", d, err)
	}
	if d.Peek.Prompt != "fix it" || d.Peek.Reply != "fixed" || !d.Peek.Done {
		t.Fatalf("peek: %+v", d.Peek)
	}
	if err := (agyAgent{}).Rename(c1, "x"); err == nil {
		t.Fatal("a closed agy conversation can't be renamed from outside")
	}
}

func TestParseAgyLsof(t *testing.T) {
	out := "p10\nfcwd\nn/work\nf143\nn/h/presence/c1.lock\np20\nfcwd\nn/other\nf9\nn/h/presence/notalock\n"
	locks, cwds, ok := parseAgyLsof(out, "/h/presence/")
	if !ok || locks[10] != "c1" || locks[20] != "" || cwds[10] != "/work" || cwds[20] != "/other" {
		t.Fatalf("%v %v %v", locks, cwds, ok)
	}
}
