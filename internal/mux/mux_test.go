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
