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
	if err := SendLine(Target{Mux: "tmux", Pane: strings.TrimSpace(string(pane))}, "/rename hello world"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	out, _ := exec.Command("tmux", "capture-pane", "-p", "-t", "it").Output()
	// cat echoes the typed line, then prints it again after Enter.
	if strings.Count(string(out), "/rename hello world") != 2 {
		t.Fatalf("pane:\n%s", out)
	}
}
