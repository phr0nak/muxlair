// Package config loads and saves ~/.muxlair/config.json.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Options are the user settings. A missing file or key means the default.
type Options struct {
	Mouse     bool // default on
	ColorTabs bool // tint tabs/workspaces muxlair opens with the agent's color; default on
	Details   bool // show the details pane at startup (i toggles it); default on
	Peek      bool // show the latest exchange of the conversation in the details pane and with p; default on
}

func Defaults() Options {
	return Options{Mouse: true, ColorTabs: true, Details: true, Peek: true}
}

// file is the on-disk shape; pointers tell "missing" from "false".
type file struct {
	Mouse     *bool `json:"mouse"`
	ColorTabs *bool `json:"color_tabs"`
	Details   *bool `json:"details"`
	Peek      *bool `json:"conversation_peek"`
}

// Load reads path; a missing file gives the defaults.
func Load(path string) (Options, error) {
	opts := Defaults()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return opts, nil
	}
	if err != nil {
		return opts, err
	}
	var c file
	if err := json.Unmarshal(b, &c); err != nil {
		return opts, fmt.Errorf("%s: %w", path, err)
	}
	set := func(dst *bool, p *bool) {
		if p != nil {
			*dst = *p
		}
	}
	set(&opts.Mouse, c.Mouse)
	set(&opts.ColorTabs, c.ColorTabs)
	set(&opts.Details, c.Details)
	set(&opts.Peek, c.Peek)
	return opts, nil
}

// Save writes opts to path, keeping any keys muxlair doesn't know, and
// publishes with a rename so a watcher never reads a half-written file.
func Save(path string, opts Options) error {
	m := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &m) // unreadable: replaced by a clean file
	}
	if m == nil {
		m = map[string]any{} // the file held "null"
	}
	m["mouse"] = opts.Mouse
	m["color_tabs"] = opts.ColorTabs
	m["details"] = opts.Details
	m["conversation_peek"] = opts.Peek
	delete(m, "auto_delete_headless") // removed in v0.5.0 with auto-delete
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "config-*.json")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Path is the config file under home.
func Path(home string) string { return filepath.Join(home, ".muxlair", "config.json") }
