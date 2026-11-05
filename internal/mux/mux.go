// Package mux finds, focuses, opens and renames agent sessions in tmux and cmux.
package mux

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
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
	styled := t.Mux == "tmux" && agent == "claude" // to tell its dim placeholder from a draft
	if readable {
		screen, err := screenOf(t, styled)
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
		if screen, err := screenOf(t, styled); err == nil && empty(screen) {
			return nil
		}
	}
	return fmt.Errorf("typed %s but its prompt did not clear; check the agent's prompt", text)
}

// screenOf returns the visible text of the terminal; styled keeps tmux's SGR
// escapes (cmux's read-screen is plain text only).
func screenOf(t Target, styled bool) (string, error) {
	if t.Mux == "tmux" {
		args := []string{"capture-pane", "-p", "-t", t.Pane}
		if styled {
			args = append(args, "-e")
		}
		return output("tmux", args...)
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
// found, counts as not empty. The screen may carry SGR styling (tmux capture -e):
// a fresh session shows an example prompt in dim text, which counts as empty,
// but only if all of it is dim (a draft is plain text).
func promptEmpty(screen string) bool {
	raw := strings.Split(screen, "\n")
	lines := make([]string, len(raw))
	for i, l := range raw {
		lines[i] = sgrRe.ReplaceAllString(l, "")
	}
	for i := len(lines) - 1; i > 0; i-- {
		l := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(l, "❯") {
			continue
		}
		if !strings.HasPrefix(strings.TrimSpace(lines[i-1]), "─") {
			return false
		}
		// The draft continues on following lines until the closing rule.
		j := i + 1
		if strings.TrimSpace(strings.TrimPrefix(l, "❯")) != "" {
			if !dimText(raw[i], false) {
				return false
			}
			// A dim example prompt may wrap in a narrow pane; its other lines are dim too.
			for ; j < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[j]), "─"); j++ {
				if strings.TrimSpace(lines[j]) == "" || !dimText(raw[j], true) {
					return false
				}
			}
		}
		return j < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[j]), "─")
	}
	return false
}

var sgrRe = regexp.MustCompile("\x1b\\[[0-9;:]*m")

// dimText reports whether all text of a styled screen line is dim (SGR 2, until
// a reset). On the prompt line (inPrompt false) the text is what follows the "❯".
// Anything else, including an escape it does not understand, is not dim: this
// must never take a draft for a placeholder.
func dimText(line string, inPrompt bool) bool {
	dim, seen := false, inPrompt
	for i := 0; i < len(line); {
		if loc := sgrRe.FindStringIndex(line[i:]); loc != nil && loc[0] == 0 {
			dim = applySGR(dim, line[i+2:i+loc[1]-1])
			i += loc[1]
			continue
		}
		r, n := utf8.DecodeRuneInString(line[i:])
		i += n
		switch {
		case r == 0x1b || r == utf8.RuneError:
			return false // other escapes, or bytes that are not text
		case !seen:
			seen = r == '❯'
		case !unicode.IsSpace(r) && !dim:
			return false
		}
	}
	return seen
}

// applySGR returns the dim state after one SGR sequence's parameters. Colors
// (38/48 with 5;n or 2;r;g;b) are skipped so their numbers are not misread.
func applySGR(dim bool, params string) bool {
	if params == "" {
		return false // ESC[m is a reset
	}
	ps := strings.Split(params, ";")
	for i := 0; i < len(ps); i++ {
		switch ps[i] {
		case "", "0", "22":
			dim = false
		case "2":
			dim = true
		case "38", "48", "58":
			if i+1 < len(ps) && ps[i+1] == "5" {
				i += 2
			} else if i+1 < len(ps) && ps[i+1] == "2" {
				i += 4
			}
		}
	}
	return dim
}
