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

// Target is a terminal (tmux pane, cmux surface or Rex block) hosting a session.
type Target struct {
	Mux       string // "tmux", "cmux" or "rex"
	Pane      string // tmux pane id (%3), cmux surface ref or Rex block id
	Window    string // tmux window id, cmux window ref or Rex window (tab) id
	Workspace string // cmux workspace ref; tmux session name; Rex session id
	CmuxPane  string // cmux pane ref
	Solo      bool   // only terminal in its tmux window / cmux workspace
}

// Current reports which multiplexer muxlair itself runs in: "tmux", "rex", "cmux"
// or "". tmux wins when nested, since that is the client the user is looking at.
// Rex comes before cmux: a Rex app started from a cmux shell inherits cmux's
// variables, while cmux panes never get REX_SESSION.
func Current() string {
	switch {
	case os.Getenv("TMUX") != "" && (os.Getenv("REX_SESSION") == "" || inTmuxPane()):
		return "tmux"
	case os.Getenv("REX_SESSION") != "":
		return "rex"
	case os.Getenv("CMUX_WORKSPACE_ID") != "":
		return "cmux"
	}
	return ""
}

// inTmuxPane reports whether muxlair's own terminal is the tmux pane named by
// TMUX_PANE. With both TMUX and REX_SESSION set, either tmux runs inside a Rex tab
// (yes) or the Rex server was started from a tmux shell and every Rex tab
// inherited TMUX (no). If it can't tell, it says yes: the old behavior.
func inTmuxPane() bool {
	pane, err := output("tmux", "display-message", "-p", "-t", os.Getenv("TMUX_PANE"), "#{pane_tty}")
	if err != nil {
		return true
	}
	own, err := ownTTY()
	if err != nil {
		return true
	}
	return own == pane
}

// ownTTY is the terminal muxlair runs in, e.g. "/dev/ttys006" (a variable for tests).
var ownTTY = func() (string, error) {
	cmd := exec.Command("tty")
	cmd.Stdin = os.Stdin
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// Targets maps tty name ("ttys023") to the terminal that owns it, across tmux,
// cmux and Rex. tmux comes last: a tmux client inside a Rex tab or cmux surface
// owns its panes' ttys, not the outer terminal's.
func Targets() map[string]Target {
	m := map[string]Target{}
	for tty, t := range rexTargets() {
		m[tty] = t
	}
	for tty, t := range cmuxTargets() {
		m[tty] = t
	}
	for tty, t := range tmuxTargets() {
		m[tty] = t
	}
	return m
}

func Focus(t Target) error {
	switch t.Mux {
	case "tmux":
		return tmuxFocus(t)
	case "rex":
		return rexFocus(t)
	}
	return cmuxFocus(t)
}

func Rename(t Target, name string) error {
	switch t.Mux {
	case "tmux":
		return tmuxRename(t, name)
	case "rex":
		return rexRename(t, name)
	}
	return cmuxRename(t, name)
}

// Open starts cmd (a shell command) in cwd, in whatever muxlair runs in: a new
// tmux window, cmux workspace or Rex tab, named name. color ("#RRGGBB", or "" for
// none) tints the tmux window's tab or the cmux workspace; Rex tabs have no color.
func Open(name, cwd, cmd, color string) error {
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
	case "rex":
		return rexOpen(name, cwd, cmd)
	case "cmux":
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
	return fmt.Errorf("not inside tmux, cmux or Rex")
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
// Codex draft just appends to it), and tmux and Rex guard nothing: for those muxlair
// reads the screen and looks for the agent's empty prompt. Where it can read the
// screen it also checks that the prompt is empty again afterwards, so a line the
// agent did not take is reported.
func SendLine(t Target, agent, text string) error {
	empty := promptEmpty
	if agent == "codex" {
		empty = codexPromptEmpty
	}
	readable := t.Mux != "cmux" || agent == "codex"
	styled := t.Mux != "cmux" && agent == "claude" // to tell its dim placeholder from a draft
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
	if t.Mux == "rex" {
		err = rexSend(t, text)
	} else if t.Mux == "tmux" {
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
	if t.Mux == "rex" {
		return rexScreen(t, styled)
	}
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
