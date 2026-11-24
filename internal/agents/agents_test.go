package agents

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

func TestListClaude(t *testing.T) {
	Home = t.TempDir()
	proj := filepath.Join(Home, ".claude", "projects", "-p")
	os.MkdirAll(proj, 0o700)
	jsonl := filepath.Join(proj, "abc.jsonl")
	os.WriteFile(jsonl, []byte(`{"type":"user","cwd":"/p"}`+"\n"), 0o600)

	ss, err := listClaude()
	if err != nil || len(ss) != 1 || ss[0].Cwd != "/p" || len(ss[0].Files) != 1 || ss[0].Files[0] != jsonl {
		t.Fatalf("list: %v %+v", err, ss)
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
	d, err := parseClaudeDetails(p, false)
	if err != nil || d.Model != "m2" || d.Total != 117 || d.HasCost || d.Started.Hour() != 11 {
		t.Fatalf("usage fallback: %+v %v", d, err)
	}
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"type":"cost-state","totalCostUSD":1.5,"startTime":1793136669045,"modelUsage":{"a":{"inputTokens":3,"outputTokens":4,"cacheReadInputTokens":5,"cacheCreationInputTokens":6}}}` + "\n")
	f.Close()
	d, _ = parseClaudeDetails(p, false)
	if !d.HasCost || d.Cost != 1.5 || d.Total != 18 || d.Split.Cache != 11 || d.Started.UnixMilli() != 1793136669045 {
		t.Fatalf("cost-state: %+v", d)
	}
	f, _ = os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"type":"assistant","requestId":"r3","message":{"model":"m3","usage":{"input_tokens":2,"output_tokens":2}}}` + "\n")
	f.Close()
	if d, _ = parseClaudeDetails(p, false); d.Total != 22 || d.Model != "m3" {
		t.Fatalf("usage after snapshot: %+v", d)
	}
	f, _ = os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"type":"system","subtype":"local_command","content":"<local-command-stdout>Set model to ` + "`Opus 5.5`" + ` and saved</local-command-stdout>"}` + "\n")
	f.Close()
	if d, _ = parseClaudeDetails(p, false); d.Model != "Opus 5.5" {
		t.Fatalf("model switch: %q", d.Model)
	}
	f, _ = os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"type":"user","message":{"content":"Set model to ` + "`Fake`" + `"}}` + "\n")
	f.Close()
	if d, _ = parseClaudeDetails(p, false); d.Model != "Opus 5.5" {
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

func TestClaudeDerivedNameNotShown(t *testing.T) {
	Home = t.TempDir()
	proj := filepath.Join(Home, ".claude", "projects", "-p")
	os.MkdirAll(proj, 0o700)
	os.WriteFile(filepath.Join(proj, "empty.jsonl"), []byte(`{"type":"user","cwd":"/p"}`+"\n"), 0o600)
	os.WriteFile(filepath.Join(proj, "prompted.jsonl"), []byte(`{"type":"user","cwd":"/p"}`+"\n"+`{"type":"last-prompt","lastPrompt":"fix the bug"}`+"\n"), 0o600)
	sess := filepath.Join(Home, ".claude", "sessions")
	os.MkdirAll(sess, 0o700)
	live := func(pid int, id, name, src string) {
		rec := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":"/p","name":%q,"nameSource":%q,"startedAt":%d}`, pid, id, name, src, time.Now().UnixMilli())
		os.WriteFile(filepath.Join(sess, fmt.Sprintf("%d.json", pid)), []byte(rec), 0o600)
	}
	pids := make([]int, 4) // live records only count while their process is alive
	for i := range pids {
		c := exec.Command("sleep", "30")
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		defer c.Process.Kill()
		pids[i] = c.Process.Pid
	}
	live(pids[0], "fresh", "p-50", "derived")    // no transcript yet
	live(pids[1], "empty", "p-51", "derived")    // transcript without title or prompt
	live(pids[2], "prompted", "p-52", "derived") // the prompt is a better name
	live(pids[3], "renamed", "my name", "user")  // no transcript, named by /rename
	got := map[string]string{}
	ss, _ := List()
	for _, s := range ss {
		got[s.ID] = s.Name
	}
	want := map[string]string{"fresh": Unnamed, "empty": Unnamed, "prompted": "fix the bug"}
	for id, n := range want {
		if got[id] != n {
			t.Errorf("%s: got %q, want %q", id, got[id], n)
		}
	}
	if got["renamed"] != "my name" {
		t.Errorf("renamed: got %q", got["renamed"])
	}
}
