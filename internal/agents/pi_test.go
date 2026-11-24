package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const piFixture = `{"type":"session","version":3,"id":"p1","timestamp":"2026-11-24T10:00:00.000Z","cwd":"/work"}
{"type":"message","id":"a","parentId":null,"timestamp":"2026-11-24T10:00:01.000Z","message":{"role":"system","content":"","timestamp":1}}
{"type":"message","id":"b","parentId":"a","timestamp":"2026-11-24T10:00:02.000Z","message":{"role":"user","content":"first question","timestamp":2}}
{"type":"message","id":"c","parentId":"b","timestamp":"2026-11-24T10:00:03.000Z","message":{"role":"assistant","content":[{"type":"text","text":"first answer"}],"provider":"x","model":"m1","usage":{"input":10,"output":5,"cacheRead":2,"cacheWrite":1,"totalTokens":18,"cost":{"total":0.25}},"stopReason":"stop","timestamp":3}}
{"type":"usage","id":"u","parentId":"c","timestamp":"2026-11-24T10:00:04.000Z","kind":"cache_warm","usage":{"input":0,"output":0,"cacheRead":100,"cacheWrite":0,"totalTokens":100,"cost":{"total":0.5}}}
{"type":"message","id":"d","parentId":"c","timestamp":"2026-11-24T10:00:05.000Z","message":{"role":"user","content":[{"type":"text","text":"second"},{"type":"image","data":"x"}],"timestamp":5}}
{"type":"message","id":"e","parentId":"d","timestamp":"2026-11-24T10:00:06.000Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hmm"},{"type":"toolCall","id":"t","name":"bash"}],"provider":"x","model":"m2","usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"totalTokens":2,"cost":{"total":0}},"stopReason":"toolUse","timestamp":6}}
{"type":"session_info","id":"f","parentId":"e","timestamp":"2026-11-24T10:00:07.000Z","name":"old name"}
{"type":"session_info","id":"g","parentId":"f","timestamp":"2026-11-24T10:00:08.000Z","name":"Fix auth"}
`

func TestPiListAndDetails(t *testing.T) {
	Home = t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", "")
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", "")
	dir := filepath.Join(Home, ".pi", "agent", "sessions", "--work--")
	os.MkdirAll(dir, 0o700)
	f := filepath.Join(dir, "2026-11-24_p1.jsonl")
	os.WriteFile(f, []byte(piFixture), 0o600)
	os.WriteFile(filepath.Join(dir, "broken.jsonl"), []byte("not a session\n"), 0o600)

	ss, err := listPi()
	if err != nil || len(ss) != 1 {
		t.Fatalf("%v %+v", err, ss)
	}
	s := ss[0]
	if s.ID != "p1" || s.Cwd != "/work" || s.Name != "Fix auth" || s.Files[0] != f || !strings.Contains(s.Resume, "pi --session '"+f+"'") {
		t.Fatalf("session: %+v", s)
	}
	d, err := (piAgent{}).Details(s, true)
	if err != nil || d.Model != "m2" || d.Split != (Tokens{In: 11, Out: 6, Cache: 103}) || d.Cost != 0.75 || d.Started.IsZero() {
		t.Fatalf("details: %+v %v", d, err)
	}
	// The last prompt; its turn stopped at a tool call, so no reply yet.
	if d.Peek.Prompt != "second\n[image]" || d.Peek.Reply != "" || d.Peek.Done {
		t.Fatalf("peek: %+v", d.Peek)
	}
	if d, _ := (piAgent{}).Details(s, false); !d.Peek.Empty() {
		t.Fatal("peek read with the setting off")
	}
}

func TestPiNameFallsBackToFirstPrompt(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(p, []byte(strings.Join(strings.Split(piFixture, "\n")[:4], "\n")+"\n"), 0o600)
	if m := parsePiMeta(p); m.name != "" || m.firstPrompt != "first question" {
		t.Fatalf("%+v", m)
	}
}

func TestPiRenameRefusesLive(t *testing.T) {
	if err := (piAgent{}).Rename(Session{MaybeLive: true, Files: []string{"x"}}, "n"); err == nil {
		t.Fatal("renamed a session a pi process may have open")
	}
}

// After /tree back to an earlier entry, the file still holds the branch left
// behind; the peek follows the branch pi shows (from the last entry written).
func TestPiPeekFollowsActiveBranch(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(p, []byte(`{"type":"session","version":3,"id":"p","timestamp":"2026-11-24T10:00:00.000Z","cwd":"/w"}
{"type":"message","id":"b","parentId":null,"timestamp":"2026-11-24T10:00:02.000Z","message":{"role":"user","content":"kept question"}}
{"type":"message","id":"c","parentId":"b","timestamp":"2026-11-24T10:00:03.000Z","message":{"role":"assistant","content":[{"type":"text","text":"kept answer"}],"stopReason":"stop"}}
{"type":"message","id":"d","parentId":"c","timestamp":"2026-11-24T10:00:04.000Z","message":{"role":"user","content":"abandoned question"}}
{"type":"message","id":"e","parentId":"d","timestamp":"2026-11-24T10:00:05.000Z","message":{"role":"assistant","content":[{"type":"text","text":"abandoned answer"}],"stopReason":"stop"}}
{"type":"session_info","id":"f","parentId":"c","timestamp":"2026-11-24T10:00:06.000Z","name":"n"}
`), 0o600)
	d, err := (piAgent{}).Details(Session{Files: []string{p}}, true)
	if err != nil || d.Peek.Prompt != "kept question" || d.Peek.Reply != "kept answer" || !d.Peek.Done {
		t.Fatalf("%+v %v", d.Peek, err)
	}
}

func TestPiSessionDirFromSettings(t *testing.T) {
	Home = t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", "")
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", "")
	agent := filepath.Join(Home, ".pi", "agent")
	os.MkdirAll(agent, 0o700)
	if got := piSessionDir(); got != filepath.Join(agent, "sessions") {
		t.Fatalf("default: %s", got)
	}
	os.WriteFile(filepath.Join(agent, "settings.json"), []byte(`{"sessionDir":"~/pi-sessions"}`), 0o600)
	if got := piSessionDir(); got != filepath.Join(Home, "pi-sessions") {
		t.Fatalf("settings: %s", got)
	}
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", "/env")
	if got := piSessionDir(); got != "/env" {
		t.Fatalf("env wins: %s", got)
	}
}
