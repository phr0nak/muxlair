package mux

import "testing"

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
