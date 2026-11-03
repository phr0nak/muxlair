// Package mux finds, focuses, opens and renames agent sessions in tmux and cmux.
package mux

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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

// enterDelay separates the typed text from Enter. Agents treat fast typing as a
// paste, and Enter right after a paste adds a newline instead of submitting
// (Codex does, with no pause at all; 50ms was enough in testing).
const enterDelay = 200 * time.Millisecond

// SendLine types text into an agent's prompt ("claude" or "codex") and presses
// Enter. It refuses unless the prompt is empty, so a half-written draft is never
// submitted. cmux guards Claude's prompt itself, but not Codex's (typing over a
// Codex draft just appends to it), and tmux guards nothing: for those muxlair
// reads the screen and looks for the agent's empty prompt. Where it can read the
// screen it also checks that the prompt is empty again afterwards, so a line the
// agent did not take is reported.
func SendLine(t Target, agent, text string) error {
	empty := promptEmpty
	if agent == "codex" {
		empty = codexPromptEmpty
	}
	readable := t.Mux == "tmux" || agent == "codex"
	if readable {
		screen, err := screenOf(t)
		if err != nil {
			return err
		}
		if !empty(screen) {
			return fmt.Errorf("its prompt is not empty (or not visible); clear it and retry, or type %s there", text)
		}
	}
	var err error
	if t.Mux == "tmux" {
		if err = run("tmux", "send-keys", "-t", t.Pane, "-l", text); err == nil {
			time.Sleep(enterDelay)
			err = run("tmux", "send-keys", "-t", t.Pane, "Enter")
		}
	} else {
		ctx := []string{"--surface", t.Pane, "--workspace", t.Workspace, "--window", t.Window}
		if err = run(cmuxBin(), append(append([]string{"send"}, ctx...), "--", text)...); err == nil {
			time.Sleep(enterDelay)
			err = run(cmuxBin(), append(append([]string{"send-key"}, ctx...), "enter")...)
		}
	}
	if err != nil || !readable {
		return err
	}
	for i := 0; i < 10; i++ { // up to ~2s for the agent to take the line
		time.Sleep(200 * time.Millisecond)
		if screen, err := screenOf(t); err == nil && empty(screen) {
			return nil
		}
	}
	return fmt.Errorf("typed %s but its prompt did not clear; check the agent's prompt", text)
}

// screenOf returns the visible text of the terminal.
func screenOf(t Target) (string, error) {
	if t.Mux == "tmux" {
		return output("tmux", "capture-pane", "-p", "-t", t.Pane)
	}
	return output(cmuxBin(), "read-screen", "--surface", t.Pane, "--workspace", t.Workspace, "--window", t.Window)
}

// codexPlaceholder is what Codex's empty input box shows (as of 0.166).
const codexPlaceholder = "Ask Codex to do anything"

// codexPromptEmpty reports whether Codex's input box, the bottom-most "›" line
// (earlier ones echo past messages), shows only its placeholder. Anything else,
// including no box on screen or a different placeholder, counts as not empty.
func codexPromptEmpty(screen string) bool {
	lines := strings.Split(screen, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); strings.HasPrefix(l, "›") {
			return strings.TrimSpace(strings.TrimPrefix(l, "›")) == codexPlaceholder
		}
	}
	return false
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
