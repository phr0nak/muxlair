package mux

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Runs against a private tmux server so the user's tmux is untouched.
func TestTmuxOpenFindRename(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("no tmux")
	}
	sock := t.TempDir() + "/sock"
	if out, err := exec.Command("tmux", "-S", sock, "new-session", "-d", "-s", "it").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	defer exec.Command("tmux", "-S", sock, "kill-server").Run()
	t.Setenv("TMUX", sock+",0,0")
	t.Setenv("CMUX_WORKSPACE_ID", "")

	if err := Open("first", "/tmp", "sleep 30", "#D97757", false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	out, _ := exec.Command("tmux", "list-panes", "-a", "-F", "#{pane_tty} #{window_name}").Output()
	var tty string
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasSuffix(l, " first") {
			tty = strings.TrimPrefix(strings.Fields(l)[0], "/dev/")
		}
	}
	tg, ok := tmuxTargets()[tty]
	if tty == "" || !ok || !tg.Solo {
		t.Fatalf("window not found: tty=%q %+v\n%s", tty, tg, out)
	}
	if err := Rename(tg, "renamed"); err != nil {
		t.Fatal(err)
	}
	name, _ := exec.Command("tmux", "display", "-p", "-t", tg.Window, "#{window_name}").Output()
	if strings.TrimSpace(string(name)) != "renamed" {
		t.Fatalf("window name %q", name)
	}
	style, _ := exec.Command("tmux", "show-options", "-w", "-v", "-t", tg.Window, "window-status-style").Output()
	if !strings.Contains(strings.ToLower(string(style)), "#d97757") {
		t.Fatalf("window-status-style %q", style)
	}
}

func TestTmuxSendLine(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("no tmux")
	}
	sock := t.TempDir() + "/sock"
	// A fake Claude screen with an empty prompt, then cat to echo what gets typed.
	fake := `printf '────\n❯ \n────\n'; cat`
	if out, err := exec.Command("tmux", "-S", sock, "new-session", "-d", "-s", "it", fake).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	defer exec.Command("tmux", "-S", sock, "kill-server").Run()
	t.Setenv("TMUX", sock+",0,0")
	time.Sleep(200 * time.Millisecond) // let the fake prompt draw
	pane, _ := exec.Command("tmux", "display", "-p", "-t", "it", "#{pane_id}").Output()
	if err := SendLine(Target{Mux: "tmux", Pane: strings.TrimSpace(string(pane))}, "claude", "/rename hello world"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	out, _ := exec.Command("tmux", "capture-pane", "-p", "-t", "it").Output()
	// cat echoes the typed line, then prints it again after Enter.
	if strings.Count(string(out), "/rename hello world") != 2 {
		t.Fatalf("pane:\n%s", out)
	}
}

// Codex's empty prompt gets the line; a draft in its prompt is left alone.
func TestTmuxSendLineCodex(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("no tmux")
	}
	sock := t.TempDir() + "/sock"
	t.Setenv("TMUX", sock+",0,0")
	defer exec.Command("tmux", "-S", sock, "kill-server").Run()
	try := func(name, screen string) (string, error) {
		fake := "printf '" + screen + "'; cat"
		if out, err := exec.Command("tmux", "-S", sock, "new-session", "-d", "-s", name, fake).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
		time.Sleep(200 * time.Millisecond)
		pane, _ := exec.Command("tmux", "display", "-p", "-t", name, "#{pane_id}").Output()
		err := SendLine(Target{Mux: "tmux", Pane: strings.TrimSpace(string(pane))}, "codex", "/rename hello")
		time.Sleep(200 * time.Millisecond)
		out, _ := exec.Command("tmux", "capture-pane", "-p", "-t", name).Output()
		return string(out), err
	}
	out, err := try("empty", "› earlier message\n\n› Ask Codex to do anything\n  status\n")
	if err != nil || strings.Count(out, "/rename hello") != 2 {
		t.Fatalf("empty prompt: %v\n%s", err, out)
	}
	out, err = try("draft", "› Ask Codex to do anything\n\n› half-written draft\n  status\n")
	if err == nil || strings.Contains(out, "/rename hello") {
		t.Fatalf("typed over a draft: %v\n%s", err, out)
	}
}
