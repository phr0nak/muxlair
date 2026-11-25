package agents

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// opencodeFixture writes a minimal OpenCode database under a temp XDG_DATA_HOME.
func opencodeFixture(t *testing.T) {
	t.Helper()
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	os.MkdirAll(filepath.Join(data, "opencode"), 0o700)
	db, err := sql.Open("sqlite", "file:"+filepath.Join(data, "opencode", "opencode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE session_v2 (id TEXT, parent_id TEXT, directory TEXT, title TEXT, model TEXT, cost REAL,
			tokens_input INT, tokens_output INT, tokens_reasoning INT, tokens_cache_read INT, tokens_cache_write INT,
			time_created INT, time_updated INT, time_busy INT, time_idle INT, time_archived INT)`,
		`CREATE TABLE session_message (id TEXT, session_id TEXT, type TEXT, seq INT, data TEXT)`,
		`INSERT INTO session_v2 VALUES ('root', NULL, '/p', 'Fix it', '{"id":"m1","providerID":"x"}', 0.5, 10, 20, 5, 30, 1, 1000, 3000, 2000, 2500, NULL)`,
		`INSERT INTO session_v2 VALUES ('busy', NULL, '/p', '', NULL, 0, 0, 0, 0, 0, 0, 1000, 4000, 4000, 2500, NULL)`,
		`INSERT INTO session_v2 VALUES ('child', 'root', '/p', 'sub', NULL, 0, 0, 0, 0, 0, 0, 1000, 3000, 0, 0, NULL)`,
		`INSERT INTO session_v2 VALUES ('gone', NULL, '/p', 'old', NULL, 0, 0, 0, 0, 0, 0, 1000, 3000, 0, 0, 5000)`,
		`INSERT INTO session_message VALUES ('1', 'root', 'user', 1, '{"text":"first question"}')`,
		`INSERT INTO session_message VALUES ('2', 'root', 'assistant', 2, '{"finish":"stop","content":[{"type":"text","text":"first answer"}]}')`,
		`INSERT INTO session_message VALUES ('3', 'root', 'user', 3, '{"text":"second question"}')`,
		`INSERT INTO session_message VALUES ('4', 'root', 'assistant', 4, '{"finish":"stop","content":[{"type":"reasoning","text":"hmm"},{"type":"text","text":"second answer"}]}')`,
		`INSERT INTO session_message VALUES ('5', 'root', 'idle', 5, '{"outcome":"succeeded"}')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

func TestListOpencode(t *testing.T) {
	opencodeFixture(t)
	defer func(f func() ([]proc, error)) { opencodeProcs = f }(opencodeProcs)
	opencodeProcs = func() ([]proc, error) { return []proc{{pid: 1, exe: "opencode"}}, nil } // the server
	ss, err := listOpencode()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Session{}
	for _, s := range ss {
		got[s.ID] = s
	}
	if len(got) != 2 || got["root"].Name != "Fix it" || got["root"].Cwd != "/p" || got["root"].Status != Exited {
		t.Fatalf("child and archived sessions must be hidden, root listed: %+v", ss)
	}
	if got["busy"].Status != Busy || got["busy"].Name != Unnamed {
		t.Fatalf("busy: %+v", got["busy"])
	}
	opencodeProcs = func() ([]proc, error) { return nil, nil } // OpenCode exited mid-turn
	if ss, _ := listOpencode(); len(ss) != 2 || ss[0].Status != Exited || ss[1].Status != Exited {
		t.Fatalf("a busy row with no opencode running must show exited: %+v", ss)
	}
	d, err := (opencodeAgent{}).Details(got["root"], true)
	if err != nil || d.Model != "m1" || d.Split != (Tokens{In: 10, Out: 25, Cache: 31}) || d.Total != 66 || !d.HasCost || d.Cost != 0.5 {
		t.Fatalf("details: %+v %v", d, err)
	}
	if d.Peek.Prompt != "second question" || d.Peek.Reply != "second answer" || !d.Peek.Done {
		t.Fatalf("peek: %+v", d.Peek)
	}
	// A newer prompt with only an attachment replaces the old exchange.
	db, _ := sql.Open("sqlite", "file:"+opencodeDB())
	db.Exec(`INSERT INTO session_message VALUES ('6', 'root', 'user', 6, '{"text":"","files":[{"name":"a.png"}]}')`)
	db.Close()
	if d, _ := (opencodeAgent{}).Details(got["root"], true); d.Peek.Prompt != "[file]" || d.Peek.Reply != "" {
		t.Fatalf("file-only prompt: %+v", d.Peek)
	}
	if d, _ := (opencodeAgent{}).Details(got["root"], false); !d.Peek.Empty() {
		t.Fatal("peek read with the setting off")
	}
}

func TestOpencodeSessionArg(t *testing.T) {
	for args, want := range map[string]string{
		"opencode -s ses_1":            "ses_1",
		"opencode --session ses_2 dir": "ses_2",
		"opencode --session=ses_3":     "ses_3",
		"opencode serve --service":     "",
		"opencode -s":                  "",
	} {
		if got := opencodeSessionArg(strings.Fields(args)); got != want {
			t.Errorf("%q: got %q want %q", args, got, want)
		}
	}
}
