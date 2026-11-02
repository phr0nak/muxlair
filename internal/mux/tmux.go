package mux

import (
	"fmt"
	"os/exec"
	"strings"
)

func tmuxTargets() map[string]Target {
	out, err := exec.Command("tmux", "list-panes", "-a", "-F",
		"#{pane_tty}\t#{pane_id}\t#{window_id}\t#{session_name}\t#{window_panes}").Output()
	if err != nil {
		return nil // no tmux server
	}
	return parseTmuxPanes(string(out))
}

func parseTmuxPanes(out string) map[string]Target {
	m := map[string]Target{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 5 {
			continue
		}
		m[strings.TrimPrefix(f[0], "/dev/")] = Target{
			Mux: "tmux", Pane: f[1], Window: f[2], Workspace: f[3], Solo: f[4] == "1",
		}
	}
	return m
}

func tmuxFocus(t Target) error {
	switchArgs := []string{"switch-client", "-t", t.Workspace}
	var host *Target
	if Current() != "tmux" {
		// muxlair runs outside tmux: drive an attached client, then show the terminal hosting it.
		out, err := exec.Command("tmux", "list-clients", "-F", "#{client_tty}").Output()
		clients := strings.Fields(string(out))
		if err != nil || len(clients) == 0 {
			return fmt.Errorf("session is in a detached tmux session %q", t.Workspace)
		}
		client := clients[0]
		hosts := cmuxTargets()
		for _, c := range clients { // prefer a client we can bring into view
			if h, ok := hosts[strings.TrimPrefix(c, "/dev/")]; ok {
				client, host = c, &h
				break
			}
		}
		switchArgs = []string{"switch-client", "-c", client, "-t", t.Workspace}
	}
	if err := run("tmux", switchArgs...); err != nil {
		return err
	}
	if host != nil {
		if err := cmuxFocus(*host); err != nil {
			return err
		}
	}
	if err := run("tmux", "select-window", "-t", t.Window); err != nil {
		return err
	}
	return run("tmux", "select-pane", "-t", t.Pane)
}

// tmuxRename renames the window; skipped when the window holds other panes,
// so we never retitle a window the session merely shares.
func tmuxRename(t Target, name string) error {
	if !t.Solo {
		return nil
	}
	return run("tmux", "rename-window", "-t", t.Window, name)
}
