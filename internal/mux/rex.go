package mux

import (
	"os"
	"strings"
)

// rexOpen starts cmd in cwd in a new tab (a Rex "window") of the Rex session
// muxlair runs in. The new tab takes focus, like a new tmux window.
// shortcut: open only; a session already running in a Rex tab can't be found or
// focused (no pid-to-block match yet), upgrade when jumping to Rex tabs is wanted.
func rexOpen(name, cwd, cmd string) error {
	return run("rex", "window", "new", "-s", os.Getenv("REX_SESSION"), rexLabel(name),
		"-c", cwd, "--", "sh", "-c", cmd)
}

// rexLabel makes name safe as rex's positional tab label: a leading dash would
// be read as a flag.
func rexLabel(name string) string {
	if l := strings.TrimLeft(name, "-"); strings.TrimSpace(l) != "" {
		return l
	}
	return "session"
}
