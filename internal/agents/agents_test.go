package agents

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestParseClaudeTranscript(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.jsonl")
	os.WriteFile(p, []byte(`{"type":"user","cwd":"/a/b"}
{"type":"custom-title","customTitle":"old"}
{"type":"last-prompt","lastPrompt":"fix\nthe   bug"}
{"type":"custom-title","customTitle":"new"}
{"type":"ai-title","aiTitle":"Project overview"}
not json
`), 0o600)
	m := parseClaudeTranscript(p)
	if m.cwd != "/a/b" || m.title != "new" || m.aiTitle != "Project overview" || m.lastPrompt != "fix the bug" {
		t.Fatalf("got %+v", m)
	}
}

func TestLastTaskStarted(t *testing.T) {
	for in, want := range map[string][2]bool{
		`"task_started" "task_complete"`: {false, true},
		`"task_complete" "task_started"`: {true, true},
		`"task_started" "turn_aborted"`:  {false, true},
		``:                               {false, false},
	} {
		if busy, found := lastTaskStarted([]byte(in)); busy != want[0] || found != want[1] {
			t.Errorf("%q: got %v %v", in, busy, found)
		}
	}
}

func TestTrashClaude(t *testing.T) {
	Home = t.TempDir()
	proj := filepath.Join(Home, ".claude", "projects", "-p")
	os.MkdirAll(filepath.Join(proj, "abc", "subagents"), 0o700)
	jsonl := filepath.Join(proj, "abc.jsonl")
	os.WriteFile(jsonl, []byte(`{"type":"user","cwd":"/p"}`+"\n"), 0o600)

	ss, err := listClaude()
	if err != nil || len(ss) != 1 || ss[0].Cwd != "/p" || len(ss[0].Files) != 2 {
		t.Fatalf("list: %v %+v", err, ss)
	}
	dir, err := Trash(ss[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(jsonl); !os.IsNotExist(err) {
		t.Fatal("transcript still in place")
	}
	for _, f := range []string{"meta.json", "0-abc.jsonl", "1-abc"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTrashRefusesRunning(t *testing.T) {
	if _, err := Trash(Session{PID: os.Getpid(), Files: []string{"x"}}); err == nil {
		t.Fatal("expected refusal for running session")
	}
}

func TestResumeID(t *testing.T) {
	for in, want := range map[string]string{
		"resume abc":            "abc",
		"--yolo resume --x abc": "abc",
		"exec hi":               "",
		"resume":                "",
	} {
		if got := resumeID(strings.Fields(in)); got != want {
			t.Errorf("%q: got %q", in, got)
		}
	}
}

func TestParsePS(t *testing.T) {
	tty, st, ok := parsePS("Fri Oct  3 12:08:29 2026     ttys029 \n")
	if !ok || tty != "ttys029" || st.Day() != 3 || st.Hour() != 12 {
		t.Fatalf("got %q %v %v", tty, st, ok)
	}
	if tty, _, ok := parsePS("Fri Oct 30 12:08:29 2026 ??"); !ok || tty != "" {
		t.Fatalf("?? tty: %q %v", tty, ok)
	}
}

func TestCleanName(t *testing.T) {
	if n, err := CleanName("  ok name "); err != nil || n != "ok name" {
		t.Fatalf("got %q %v", n, err)
	}
	for _, bad := range []string{"", "  ", "a\nb", `a\nb`} {
		if _, err := CleanName(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestRenameClaudeTranscript(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(p, []byte(`{"type":"custom-title","customTitle":"old"}`+"\n"), 0o600)
	if err := RenameClaudeTranscript(Session{ID: "s", Files: []string{p}}, "new"); err != nil {
		t.Fatal(err)
	}
	if m := parseClaudeTranscript(p); m.title != "new" {
		t.Fatalf("title %q", m.title)
	}
	if err := RenameClaudeTranscript(Session{PID: os.Getpid(), Files: []string{p}}, "x"); err == nil {
		t.Fatal("renamed a running session's transcript")
	}
}

func TestAwaitRPC(t *testing.T) {
	ok := "{\"method\":\"note\"}\n{\"id\":1,\"result\":{}}\n{\"id\":2,\"result\":{}}\n"
	if err := awaitRPC(rpcScanner(strings.NewReader(ok)), 2); err != nil {
		t.Fatal(err)
	}
	bad := "{\"id\":2,\"error\":{\"message\":\"thread not found\"}}\n"
	if err := awaitRPC(rpcScanner(strings.NewReader(bad)), 2); err == nil || !strings.Contains(err.Error(), "thread not found") {
		t.Fatalf("got %v", err)
	}
	if err := awaitRPC(rpcScanner(strings.NewReader("")), 2); err == nil {
		t.Fatal("no error on EOF")
	}
}

func TestParseClaudeDetails(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(p, []byte(`{"type":"user","timestamp":"2026-10-30T11:08:35Z"}
{"type":"assistant","requestId":"r1","message":{"model":"m1","usage":{"input_tokens":10,"output_tokens":5}}}
{"type":"assistant","requestId":"r1","message":{"model":"m1","usage":{"input_tokens":10,"output_tokens":5}}}
{"type":"assistant","requestId":"r2","message":{"model":"m2","usage":{"input_tokens":1,"output_tokens":1,"cache_read_input_tokens":100}}}
`), 0o600)
	d, err := parseClaudeDetails(p)
	if err != nil || d.Model != "m2" || d.Total != 117 || d.HasCost || d.Started.Hour() != 11 {
		t.Fatalf("usage fallback: %+v %v", d, err)
	}
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"type":"cost-state","totalCostUSD":1.5,"startTime":1793136669045,"modelUsage":{"a":{"inputTokens":3,"outputTokens":4,"cacheReadInputTokens":5,"cacheCreationInputTokens":6}}}` + "\n")
	f.Close()
	d, _ = parseClaudeDetails(p)
	if !d.HasCost || d.Cost != 1.5 || d.Total != 18 || d.Split.Cache != 11 || d.Started.UnixMilli() != 1793136669045 {
		t.Fatalf("cost-state: %+v", d)
	}
	f, _ = os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"type":"assistant","requestId":"r3","message":{"model":"m3","usage":{"input_tokens":2,"output_tokens":2}}}` + "\n")
	f.Close()
	if d, _ = parseClaudeDetails(p); d.Total != 22 || d.Model != "m3" {
		t.Fatalf("usage after snapshot: %+v", d)
	}
	f, _ = os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"type":"system","subtype":"local_command","content":"<local-command-stdout>Set model to ` + "`Opus 5.5`" + ` and saved</local-command-stdout>"}` + "\n")
	f.Close()
	if d, _ = parseClaudeDetails(p); d.Model != "Opus 5.5" {
		t.Fatalf("model switch: %q", d.Model)
	}
	f, _ = os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"type":"user","message":{"content":"Set model to ` + "`Fake`" + `"}}` + "\n")
	f.Close()
	if d, _ = parseClaudeDetails(p); d.Model != "Opus 5.5" {
		t.Fatalf("quoted phrase changed model: %q", d.Model)
	}
}

func TestParseLsof(t *testing.T) {
	out := "p10\nfcwd\nn/work\nf5\nn/h/.codex/sessions/a.jsonl\nf6\nn/h/.codex/sessions/b.jsonl\np11\nfcwd\nn/\n"
	m := parseLsof(out, "/h/.codex/sessions/")
	if m[10].cwd != "/work" || len(m[10].rollouts) != 2 || m[11].cwd != "/" || len(m[11].rollouts) != 0 {
		t.Fatalf("got %+v", m)
	}
	if _, ok := m[12]; ok {
		t.Fatal("phantom pid")
	}
}

func TestProcStatsSelf(t *testing.T) {
	st, ok := procStats([]int{os.Getpid()})[os.Getpid()]
	if !ok || st.started.IsZero() || time.Since(st.started) > time.Hour {
		t.Fatalf("got %+v %v", st, ok)
	}
}

func TestOwnProcsSelf(t *testing.T) {
	procs, err := ownProcs()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range procs {
		if p.pid == os.Getpid() {
			if time.Since(p.started) > time.Hour || p.exe == "" {
				t.Fatalf("self: %+v", p)
			}
			if args := procArgs(p.pid); len(args) == 0 || !strings.HasSuffix(args[0], ".test") {
				t.Fatalf("args: %q", args)
			}
			return
		}
	}
	t.Fatal("own pid not listed")
}

func TestCodexChildren(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	meta := func(id, parent string) string {
		p := filepath.Join(home, id+".jsonl")
		line := `{"type":"session_meta","payload":{"id":"` + id + `","parent_thread_id":"` + parent + `"}}`
		os.WriteFile(p, []byte(line+"\n"), 0o600)
		return p
	}
	db, err := sql.Open("sqlite", filepath.Join(home, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	// Overlapping runs A and B: timing would mix their reviewers; the rollouts don't.
	for _, q := range []string{
		`CREATE TABLE threads (id TEXT, source TEXT, archived INT, cwd TEXT, originator TEXT, created_at_ms INT, rollout_path TEXT)`,
		`CREATE TABLE thread_spawn_edges (parent_thread_id TEXT, child_thread_id TEXT, status TEXT)`,
		`INSERT INTO threads VALUES ('A','exec',0,'/w','codex_exec',1000,''), ('B','exec',0,'/w','codex_exec',1010,''),
			('A-rev','{"subagent":"review"}',0,'/w','codex_exec',1030,'` + meta("A-rev", "A") + `'),
			('B-rev','{"subagent":"review"}',0,'/w','codex_exec',1020,'` + meta("B-rev", "B") + `'),
			('unreadable','{"subagent":"review"}',0,'/w','codex_exec',1040,'/nope'),
			('edge-child','{"subagent":"x"}',0,'/w','codex_exec',1050,'')`,
		`INSERT INTO thread_spawn_edges VALUES ('A','edge-child','done')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	for run, want := range map[string]string{"A": "A-rev,edge-child", "B": "B-rev"} {
		got, err := codexChildren(run)
		if err != nil {
			t.Fatal(err)
		}
		sort.Strings(got)
		if strings.Join(got, ",") != want {
			t.Errorf("%s: got %v, want %s", run, got, want)
		}
	}
}

func TestRolloutInteractive(t *testing.T) {
	dir := t.TempDir()
	write := func(name, s string) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(s), 0o600)
		return p
	}
	run := `{"type":"session_meta","payload":{"originator":"codex_exec"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"task_started"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"task_complete"}}` + "\n"
	execOnly := write("a", run)
	// A resume that sends nothing still appends events after the run finished.
	resumed := write("b", run+`{"type":"event_msg","payload":{"type":"thread_settings_applied"}}`+"\n")
	otherOriginator := write("d", `{"type":"session_meta","payload":{"originator":"codex-tui"}}`+"\n")
	// Killed before finishing, then resumed with a new turn: no marker before it.
	interrupted := write("e", `{"type":"session_meta","payload":{"originator":"codex_exec"}}`+"\n"+
		`{"type":"event_msg","payload":{"type":"task_started"}}`+"\n"+
		`{"type":"event_msg","payload":{"type":"thread_settings_applied"}}`+"\n"+
		`{"type":"event_msg","payload":{"type":"task_started"}}`+"\n"+
		`{"type":"event_msg","payload":{"type":"task_complete"}}`+"\n")
	killed := write("f", `{"type":"session_meta","payload":{"originator":"codex_exec"}}`+"\n"+
		`{"type":"event_msg","payload":{"type":"task_started"}}`+"\n")
	aborted := write("g", `{"type":"session_meta","payload":{"originator":"codex_exec"}}`+"\n"+
		`{"type":"event_msg","payload":{"type":"task_started"}}`+"\n"+
		`{"type":"event_msg","payload":{"type":"turn_aborted"}}`+"\n")
	if rolloutInteractive(execOnly) || !rolloutInteractive(resumed) || !rolloutInteractive(otherOriginator) ||
		!rolloutInteractive(interrupted) || !rolloutInteractive(killed) || !rolloutInteractive(aborted) {
		t.Fatal("wrong classification")
	}
	// A line longer than the scanner allows: unreadable to the end, so protected.
	huge := write("c", `{"type":"session_meta","payload":{"originator":"codex_exec"}}`+"\n"+strings.Repeat("x", 65<<20)+"\n")
	if !scanInteractive(huge) {
		t.Fatal("incomplete scan treated as headless")
	}
}

func TestMarkInteractive(t *testing.T) {
	Home = t.TempDir()
	if err := MarkInteractive("codex:x"); err != nil {
		t.Fatal(err)
	}
	keys, err := interactiveKeys()
	if err != nil || !keys["codex:x"] || keys["codex:y"] {
		t.Fatalf("keep-list wrong: %v %v", keys, err)
	}
	// A corrupt keep-list fails closed: nothing is headless.
	os.WriteFile(interactiveFile(), []byte("{broken"), 0o600)
	ss := []Session{{Agent: "codex", ID: "z", Headless: true}}
	if err := applyKeepList(ss); err == nil || ss[0].Headless {
		t.Fatalf("corrupt list: err=%v headless=%v", err, ss[0].Headless)
	}
}

func TestClaudeHeadless(t *testing.T) {
	Home = t.TempDir()
	proj := filepath.Join(Home, ".claude", "projects", "-p")
	os.MkdirAll(proj, 0o700)
	os.WriteFile(filepath.Join(proj, "a.jsonl"), []byte(`{"type":"user","cwd":"/p","entrypoint":"sdk-cli"}`+"\n"), 0o600)
	os.WriteFile(filepath.Join(proj, "b.jsonl"), []byte(`{"type":"user","cwd":"/p","entrypoint":"cli"}`+"\n"), 0o600)
	// c: started headless, later resumed interactively.
	os.WriteFile(filepath.Join(proj, "c.jsonl"), []byte(`{"type":"user","cwd":"/p","entrypoint":"sdk-cli"}`+"\n"+`{"type":"user","cwd":"/p","entrypoint":"cli"}`+"\n"), 0o600)
	ss, _ := listClaude()
	h := map[string]bool{}
	for _, s := range ss {
		h[s.ID] = s.Headless
	}
	// d: a record too long to scan hides what follows, so it must not count as headless.
	os.WriteFile(filepath.Join(proj, "d.jsonl"), []byte(`{"type":"user","cwd":"/p","entrypoint":"sdk-cli"}`+"\n"+strings.Repeat("x", 65<<20)+"\n"), 0o600)
	ss, _ = listClaude()
	for _, s := range ss {
		h[s.ID] = s.Headless
	}
	if !h["a"] || h["b"] || h["c"] || h["d"] {
		t.Fatalf("headless flags %v", h)
	}
}

func TestCodexUntouched(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	db, _ := sql.Open("sqlite", filepath.Join(home, "thread_history_1.sqlite"))
	db.Exec(`CREATE TABLE thread_turns (thread_id TEXT, rollout_ordinal INT, status TEXT)`)
	db.Exec(`INSERT INTO thread_turns VALUES
		('review',1,'inProgress'), ('review',2,'completed'),
		('reviewer',1,'completed'),
		('resumed',1,'inProgress'), ('resumed',2,'completed'), ('resumed',3,'completed'),
		('interrupted',1,'completed'), ('interrupted',2,'inProgress')`)
	db.Close()
	u, err := codexUntouched([]string{"review", "reviewer", "resumed", "interrupted", "none"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"review": true, "reviewer": true, "resumed": false, "interrupted": false, "none": true}
	for id, w := range want {
		if u[id] != w {
			t.Errorf("%s: got %v want %v", id, u[id], w)
		}
	}
}

func TestHelpersNested(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	Home = t.TempDir()
	db, _ := sql.Open("sqlite", filepath.Join(home, "state_5.sqlite"))
	for _, q := range []string{
		`CREATE TABLE threads (id TEXT, source TEXT, archived INT, cwd TEXT, originator TEXT, created_at_ms INT, rollout_path TEXT)`,
		`CREATE TABLE thread_spawn_edges (parent_thread_id TEXT, child_thread_id TEXT, status TEXT)`,
		`INSERT INTO threads VALUES ('root','exec',0,'/w','x',1,''), ('child','{"subagent":"a"}',0,'/w','x',2,''),
			('grandchild','{"subagent":"b"}',0,'/w','x',3,''), ('kept','{"subagent":"c"}',0,'/w','x',4,''), ('below-kept','{"subagent":"d"}',0,'/w','x',5,'')`,
		`INSERT INTO thread_spawn_edges VALUES ('root','child','done'), ('child','grandchild','done'), ('root','kept','done'), ('kept','below-kept','done')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	MarkInteractive("codex:kept")
	got, err := codexHelpersToDelete("root")
	if err != nil || strings.Join(got, ",") != "grandchild,child" {
		t.Fatalf("got %v %v (want deepest first, kept subtree left alone)", got, err)
	}
}

func TestClaudeResumedHeadlessPersists(t *testing.T) {
	Home = t.TempDir()
	proj := filepath.Join(Home, ".claude", "projects", "-p")
	os.MkdirAll(proj, 0o700)
	os.WriteFile(filepath.Join(proj, "h.jsonl"), []byte(`{"type":"user","cwd":"/p","entrypoint":"sdk-cli"}`+"\n"), 0o600)
	sess := filepath.Join(Home, ".claude", "sessions")
	os.MkdirAll(sess, 0o700)
	// This test process plays the interactive claude that resumed it.
	rec := fmt.Sprintf(`{"pid":%d,"sessionId":"h","cwd":"/p","entrypoint":"cli","startedAt":%d}`, os.Getpid(), time.Now().UnixMilli())
	os.WriteFile(filepath.Join(sess, fmt.Sprintf("%d.json", os.Getpid())), []byte(rec), 0o600)
	if _, err := List(); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(sess, fmt.Sprintf("%d.json", os.Getpid()))) // it exits
	ss, _ := List()
	for _, s := range ss {
		if s.ID == "h" && s.Headless {
			t.Fatal("headless again after the interactive resume exited")
		}
	}
}

func TestRecheckUnderLockNoDeadlock(t *testing.T) {
	Home = t.TempDir()
	proj := filepath.Join(Home, ".claude", "projects", "-p")
	os.MkdirAll(proj, 0o700)
	os.WriteFile(filepath.Join(proj, "h.jsonl"), []byte(`{"type":"user","cwd":"/p","entrypoint":"sdk-cli"}`+"\n"), 0o600)
	sess := filepath.Join(Home, ".claude", "sessions")
	os.MkdirAll(sess, 0o700)
	rec := fmt.Sprintf(`{"pid":%d,"sessionId":"h","cwd":"/p","entrypoint":"cli","startedAt":%d}`, os.Getpid(), time.Now().UnixMilli())
	os.WriteFile(filepath.Join(sess, fmt.Sprintf("%d.json", os.Getpid())), []byte(rec), 0o600)
	done := make(chan Session, 1)
	go WithSessionLock(func() error {
		s, _ := Recheck(Session{Agent: "claude", ID: "h"})
		done <- s
		return nil
	})
	select {
	case s := <-done:
		if s.Headless {
			t.Fatal("resumed session still headless under the lock")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: Recheck under the session lock never returned")
	}
}

func TestHeadlessArgs(t *testing.T) {
	for in, want := range map[string]bool{
		"codex review --uncommitted": true,
		"codex exec hi":              true,
		"codex -c x=y e hi":          true,
		"codex resume abc":           false,
		"codex":                      false,
		"":                           false,
	} {
		if got := headlessArgs(strings.Fields(in)); got != want {
			t.Errorf("%q: got %v", in, got)
		}
	}
}

func TestNotedProtectsBeforeFlush(t *testing.T) {
	Home = t.TempDir()
	noteInteractive("claude:q")
	defer func() { notedMu.Lock(); delete(noted, "claude:q"); notedMu.Unlock() }()
	ss := []Session{{Agent: "claude", ID: "q", Headless: true}}
	if err := applyKeepList(ss); err != nil || ss[0].Headless {
		t.Fatalf("queued observation ignored: %v %v", err, ss[0].Headless)
	}
}
