package mux

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"sync/atomic"
)

type cmuxTree struct {
	Windows []struct {
		Ref        string `json:"ref"`
		Workspaces []struct {
			Ref   string `json:"ref"`
			Panes []struct {
				Ref      string `json:"ref"`
				Surfaces []struct {
					Ref  string `json:"ref"`
					TTY  string `json:"tty"`
					Type string `json:"type"`
				} `json:"surfaces"`
			} `json:"panes"`
		} `json:"workspaces"`
	} `json:"windows"`
}

var cmuxDenied atomic.Bool

// CmuxDenied reports whether cmux refused the last query because muxlair was not
// started inside cmux (cmux's default "cmuxOnly" socket control mode).
func CmuxDenied() bool { return cmuxDenied.Load() }

func cmuxTargets() map[string]Target {
	cmd := exec.Command(cmuxBin(), "--json", "tree", "--all")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	cmuxDenied.Store(err != nil && strings.Contains(stderr.String()+string(out), "Access denied"))
	if err != nil {
		return nil // cmux not running, or access denied
	}
	return parseCmuxTree(out)
}

func parseCmuxTree(b []byte) map[string]Target {
	var tree cmuxTree
	if json.Unmarshal(b, &tree) != nil {
		return nil
	}
	m := map[string]Target{}
	for _, w := range tree.Windows {
		for _, ws := range w.Workspaces {
			n := 0 // terminals only: a browser tab beside the session doesn't make the workspace shared
			for _, p := range ws.Panes {
				for _, s := range p.Surfaces {
					if s.Type == "terminal" {
						n++
					}
				}
			}
			for _, p := range ws.Panes {
				for _, s := range p.Surfaces {
					if s.Type != "terminal" || s.TTY == "" {
						continue
					}
					m[s.TTY] = Target{Mux: "cmux", Pane: s.Ref, Window: w.Ref, Workspace: ws.Ref, CmuxPane: p.Ref, Solo: n == 1}
				}
			}
		}
	}
	return m
}

func cmuxFocus(t Target) error {
	if err := run(cmuxBin(), "focus-window", "--window", t.Window); err != nil {
		return err
	}
	if err := run(cmuxBin(), "select-workspace", "--workspace", t.Workspace, "--window", t.Window); err != nil {
		return err
	}
	return run(cmuxBin(), "focus-panel", "--panel", t.Pane, "--workspace", t.Workspace, "--window", t.Window)
}

// cmuxRename renames the tab, and the workspace too when the session is alone in
// it (tmuxRename's rule). A workspace shared with other terminals may carry a
// name the user chose, so it is left alone.
func cmuxRename(t Target, name string) error {
	if err := run(cmuxBin(), "tab-action", "--action", "rename", "--tab", t.Pane,
		"--workspace", t.Workspace, "--window", t.Window, "--title", name); err != nil {
		return err
	}
	if !t.Solo {
		return nil
	}
	return run(cmuxBin(), "rename-workspace", "--workspace", t.Workspace, "--window", t.Window, "--", name)
}
