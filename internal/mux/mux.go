// Package mux finds, focuses, opens and renames agent sessions in tmux and cmux.
package mux

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Target is a terminal (tmux pane or cmux surface) hosting a session.
type Target struct {
	Mux       string // "tmux" or "cmux"
	Pane      string // tmux pane id (%3) or cmux surface ref
	Window    string // tmux window id or cmux window ref
	Workspace string // cmux workspace ref; tmux session name
	CmuxPane  string // cmux pane ref
	Solo      bool   // only terminal in its tmux window / cmux workspace
}

// Current reports which multiplexer muxlair itself runs in: "cmux", "tmux" or "".
// tmux wins when nested, since that is the client the user is looking at.
func Current() string {
	switch {
	case os.Getenv("TMUX") != "":
		return "tmux"
	case os.Getenv("CMUX_WORKSPACE_ID") != "":
		return "cmux"
	}
	return ""
}

// Targets maps tty name ("ttys023") to the terminal that owns it, across tmux and cmux.
func Targets() map[string]Target {
	m := map[string]Target{}
	for tty, t := range cmuxTargets() {
		m[tty] = t
	}
	for tty, t := range tmuxTargets() {
		m[tty] = t
	}
	return m
}

func Focus(t Target) error {
	if t.Mux == "tmux" {
		return tmuxFocus(t)
	}
	return cmuxFocus(t)
}

func Rename(t Target, name string) error {
	if t.Mux == "tmux" {
		return tmuxRename(t, name)
	}
	return cmuxRename(t, name)
}

// Open starts cmd (a shell command) in cwd. workspace=true forces a new cmux workspace.
// color ("#RRGGBB", or "" for none) tints the new tmux window tab or cmux workspace;
// a cmux pane opened inside the current workspace has no tab color to set.
func Open(name, cwd, cmd, color string, workspace bool) error {
	if workspace {
		out, err := output(cmuxBin(), "new-workspace", "--name", name, "--cwd", cwd, "--command", cmd, "--focus", "true")
		if err != nil || color == "" {
			return err
		}
		ws := workspaceRef(out)
		if ws == "" {
			return fmt.Errorf("cmux new-workspace: no workspace ref in %q", out)
		}
		return run(cmuxBin(), "workspace-action", "--action", "set-color", "--workspace", ws, "--color", color)
	}
	switch Current() {
	case "tmux":
		win, err := output("tmux", "new-window", "-P", "-F", "#{window_id}", "-n", name, "-c", cwd, cmd)
		if err != nil || color == "" {
			return err
		}
		if err := run("tmux", "set-option", "-w", "-t", win, "window-status-style", "fg="+color); err != nil {
			return err
		}
		return run("tmux", "set-option", "-w", "-t", win, "window-status-current-style", "fg="+color+",bold")
	case "cmux":
		return run(cmuxBin(), "new-pane", "--direction", "right", "--command", cmd, "--focus", "true")
	}
	return fmt.Errorf("not inside tmux or cmux")
}

// workspaceRef extracts "workspace:N" from cmux output like "OK workspace:12".
func workspaceRef(out string) string {
	for _, f := range strings.Fields(out) {
		if strings.HasPrefix(f, "workspace:") {
			return f
		}
	}
	return ""
}

// cmuxBin finds the cmux CLI. Outside cmux (e.g. tmux in Ghostty) it is usually
// not on PATH, which would make every cmux-hosted session look unreachable.
func cmuxBin() string {
	if p, err := exec.LookPath("cmux"); err == nil {
		return p
	}
	return "/Applications/cmux.app/Contents/Resources/bin/cmux"
}

func run(name string, args ...string) error {
	_, err := output(name, args...)
	return err
}

func output(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "CMUX_QUIET=1") // silence cmux deprecation notices
	out, err := cmd.Output()
	if err != nil {
		msg := ""
		if ee, ok := err.(*exec.ExitError); ok {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		return "", fmt.Errorf("%s %s: %v: %s", filepath.Base(name), args[0], err, msg)
	}
	return strings.TrimSpace(string(out)), nil
}

// SendLine types text into an agent's prompt and presses Enter. It refuses unless
// the prompt is empty, so a half-written draft is never submitted: cmux checks that
// itself; for tmux muxlair reads the pane and looks for Claude's empty prompt.
func SendLine(t Target, text string) error {
	if t.Mux == "tmux" {
		screen, err := output("tmux", "capture-pane", "-p", "-t", t.Pane)
		if err != nil {
			return err
		}
		if !promptEmpty(screen) {
			return fmt.Errorf("its prompt is not empty (or not visible); clear it and retry, or type /rename there")
		}
		if err := run("tmux", "send-keys", "-t", t.Pane, "-l", text); err != nil {
			return err
		}
		return run("tmux", "send-keys", "-t", t.Pane, "Enter")
	}
	ctx := []string{"--surface", t.Pane, "--workspace", t.Workspace, "--window", t.Window}
	if err := run(cmuxBin(), append(append([]string{"send"}, ctx...), "--", text)...); err != nil {
		return err
	}
	return run(cmuxBin(), append(append([]string{"send-key"}, ctx...), "enter")...)
}

// promptEmpty reports whether the screen shows Claude's input prompt with nothing
// typed: a "❯" line right below a "─" rule. Anything else, including no prompt
// found, counts as not empty.
func promptEmpty(screen string) bool {
	lines := strings.Split(screen, "\n")
	for i := len(lines) - 1; i > 0; i-- {
		l := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(l, "❯") {
			continue
		}
		if !strings.HasPrefix(strings.TrimSpace(lines[i-1]), "─") {
			return false
		}
		// The draft continues on following lines until the closing rule.
		if strings.TrimSpace(strings.TrimPrefix(l, "❯")) != "" {
			return false
		}
		return i+1 < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i+1]), "─")
	}
	return false
}
