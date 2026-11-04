package mux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseTmuxPanes(t *testing.T) {
	m := parseTmuxPanes("/dev/ttys001\t%1\t@1\tmain\t1\n/dev/ttys002\t%2\t@2\tmain\t2\n")
	if got := m["ttys001"]; got.Pane != "%1" || got.Window != "@1" || got.Workspace != "main" || !got.Solo {
		t.Fatalf("got %+v", got)
	}
	if m["ttys002"].Solo {
		t.Fatal("2-pane window marked solo")
	}
}

func TestParseCmuxTree(t *testing.T) {
	m := parseCmuxTree([]byte(`{"windows":[{"ref":"window:1","workspaces":[{"ref":"workspace:2","panes":[
		{"ref":"pane:3","surfaces":[{"ref":"surface:4","tty":"ttys009","type":"terminal"},{"ref":"surface:5","type":"browser"}]}]}]}]}`))
	got := m["ttys009"]
	if got.Mux != "cmux" || got.Pane != "surface:4" || got.Workspace != "workspace:2" || got.Window != "window:1" || len(m) != 1 {
		t.Fatalf("got %+v (%d)", got, len(m))
	}
	if !got.Solo { // the browser tab beside it doesn't count
		t.Fatal("one terminal plus a browser tab should be solo")
	}
	two := parseCmuxTree([]byte(`{"windows":[{"ref":"window:1","workspaces":[{"ref":"workspace:2","panes":[
		{"ref":"pane:3","surfaces":[{"ref":"surface:4","tty":"ttys009","type":"terminal"},{"ref":"surface:5","tty":"ttys010","type":"terminal"}]}]}]}]}`))
	if two["ttys009"].Solo || two["ttys010"].Solo {
		t.Fatal("two terminals share the workspace")
	}
}

func TestWorkspaceRef(t *testing.T) {
	if got := workspaceRef("OK workspace:1000080021"); got != "workspace:1000080021" {
		t.Fatalf("got %q", got)
	}
	if got := workspaceRef("OK"); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestPromptEmpty(t *testing.T) {
	rule := "────────────"
	for screen, want := range map[string]bool{
		"out\n" + rule + "\n❯ \n" + rule + "\nstatus":        true,
		"out\n" + rule + "\n❯ draft text\n" + rule:           false,
		"out\n" + rule + "\n❯ \nsecond draft line\n" + rule:  false,
		"no prompt here\n$ ":                                 false,
		"❯ in output, not prompt\n" + rule + "\n❯ \n" + rule: true,
	} {
		if got := promptEmpty(screen); got != want {
			t.Errorf("%q: got %v", screen, got)
		}
	}
}

// tmux capture -e: a fresh Claude session's example prompt is dim, a draft is plain.
func TestPromptEmptyStyled(t *testing.T) {
	rule := "\x1b[38;5;244m────────────\x1b[0m"
	screen := func(prompt string) string { return "out\n" + rule + "\n" + prompt + "\n" + rule + "\nstatus" }
	for name, c := range map[string]struct {
		prompt string
		want   bool
	}{
		"dim placeholder":            {"\x1b[39m❯ \x1b[2mTry \"how do I log an error?\"\x1b[0m", true},
		"dim, combined params":       {"\x1b[0m❯ \x1b[2;39mTry it\x1b[0m", true},
		"dim, 256-color number is 2": {"\x1b[2m\x1b[38;5;2m❯ \x1b[0m\x1b[2m\x1b[38;5;2mTry\x1b[0m", true}, // 38;5;2 is a color, not dim
		"no text":                    {"\x1b[39m❯ \x1b[0m", true},
		"plain draft":                {"\x1b[39m❯ hello", false},
		"plain then dim":             {"\x1b[39m❯ hello \x1b[2mmore\x1b[0m", false},
		"dim then plain":             {"\x1b[39m❯ \x1b[2mTry\x1b[0m x", false},
		"dim reset by 22":            {"\x1b[39m❯ \x1b[2mTry\x1b[22m x", false},
		"reset by bare m":            {"\x1b[39m❯ \x1b[2mTry\x1b[m x", false},
		"gray color is not dim":      {"\x1b[39m❯ \x1b[90mTry\x1b[0m", false},
		"unknown escape":             {"\x1b[39m❯ \x1b[2m\x1b[?25hTry\x1b[0m", false},
		"unstyled text":              {"❯ Try \"how do I log an error?\"", false},
	} {
		if got := promptEmpty(screen(c.prompt)); got != c.want {
			t.Errorf("%s: got %v", name, got)
		}
	}
	// A dim example prompt that wraps in a narrow pane; a draft that wraps does not count.
	wrapped := "out\n" + rule + "\n\x1b[39m❯ \x1b[2mTry \"how do I\x1b[0m\n\x1b[2m  log an error?\"\x1b[0m\n" + rule
	if !promptEmpty(wrapped) {
		t.Error("a wrapped dim placeholder counts as a draft")
	}
	for name, s := range map[string]string{
		"wrapped, plain tail": "out\n" + rule + "\n\x1b[39m❯ \x1b[2mTry\x1b[0m\n  typed text\n" + rule,
		"wrapped, blank line": "out\n" + rule + "\n\x1b[39m❯ \x1b[2mTry\x1b[0m\n\n" + rule,
		"wrapped, no rule":    "out\n" + rule + "\n\x1b[39m❯ \x1b[2mTry\x1b[0m\n\x1b[2m  more\x1b[0m",
	} {
		if promptEmpty(s) {
			t.Errorf("%s counted as empty", name)
		}
	}
	if !promptEmpty("out\n" + rule + "\n\x1b[39m❯ \x1b[2mTry\x1b[0m\n" + rule) {
		t.Error("rule styling broke the structure check")
	}
	if promptEmpty("out\n" + rule + "\n\x1b[39m❯ \x1b[2mTry\x1b[0m\nsecond line\n" + rule) {
		t.Error("a second line of text counts as a draft")
	}
}

// A fake cmux on PATH records the calls cmuxRename makes.
func TestCmuxRenameWorkspaceOnlyWhenAlone(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	os.WriteFile(filepath.Join(dir, "cmux"), []byte("#!/bin/sh\necho \"$@\" >> "+log+"\n"), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	calls := func() []string {
		b, _ := os.ReadFile(log)
		os.Remove(log)
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
	tg := Target{Mux: "cmux", Pane: "surface:1", Window: "window:1", Workspace: "workspace:2", Solo: true}
	if err := cmuxRename(tg, "My name"); err != nil {
		t.Fatal(err)
	}
	got := calls()
	if len(got) != 2 || !strings.HasPrefix(got[0], "tab-action") ||
		got[1] != "rename-workspace --workspace workspace:2 --window window:1 -- My name" {
		t.Fatalf("alone: %q", got)
	}
	tg.Solo = false
	if err := cmuxRename(tg, "My name"); err != nil {
		t.Fatal(err)
	}
	if got := calls(); len(got) != 1 || !strings.HasPrefix(got[0], "tab-action") {
		t.Fatalf("shared workspace must only get the tab renamed: %q", got)
	}
}

func TestCodexPromptEmpty(t *testing.T) {
	for screen, want := range map[string]bool{
		"out\n› Ask Codex to do anything\n\n  gpt · ~/p\n  ? for shortcuts":     true,
		"› earlier message\n• ok\n› Ask Codex to do anything\n  status":         true, // an echoed message above the box
		"out\n› half-written draft\n  status":                                   false,
		"› Ask Codex to do anything\n• reply\n› typed after an old placeholder": false, // the bottom-most one is the box
		"no prompt here\n$ ":               false,
		"› 1. gpt-5 (current)\n  2. other": false, // a picker, not the box
		"› Ask a follow-up question":       false, // another placeholder: not ours to guess
	} {
		if got := codexPromptEmpty(screen); got != want {
			t.Errorf("%q: got %v", screen, got)
		}
	}
}
