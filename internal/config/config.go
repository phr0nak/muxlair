// Package config loads and saves ~/.muxlair/config.json.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Options are the user settings. A missing file or key means the default.
type Options struct {
	Mouse     bool // default on
	ColorTabs bool // tint tabs/workspaces muxlair opens with the agent's color; default on
	Details   bool // show the details pane at startup (i toggles it); default on
	// AutoDelete trashes headless sessions (claude -p, codex exec/review) once
	// exited and idle this long; 0 disables. Default 10m.
	AutoDelete time.Duration
}

func Defaults() Options {
	return Options{Mouse: true, ColorTabs: true, Details: true, AutoDelete: 10 * time.Minute}
}

// file is the on-disk shape; pointers tell "missing" from "false".
type file struct {
	Mouse      *bool   `json:"mouse"`
	ColorTabs  *bool   `json:"color_tabs"`
	Details    *bool   `json:"details"`
	AutoDelete *string `json:"auto_delete_headless"`
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
	if c.AutoDelete != nil {
		d, err := ParseDuration(*c.AutoDelete)
		if err != nil {
			return Defaults(), fmt.Errorf("%s: auto_delete_headless: %w", path, err)
		}
		opts.AutoDelete = d
	}
	return opts, nil
}

// ParseDuration accepts "off" (0) or a Go duration such as "30s", "10m", "2h".
func ParseDuration(v string) (time.Duration, error) {
	switch v = strings.TrimSpace(v); v {
	case "off", "0", "":
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 0, fmt.Errorf(`want a duration like "10m" or "off", got %q`, v)
	}
	return d, nil
}

// FormatDuration is ParseDuration's inverse: "off", "10m", "1h30m", "45s".
func FormatDuration(d time.Duration) string {
	if d <= 0 {
		return "off"
	}
	s := d.String() // "10m0s", "1h0m0s", "1m30s"
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
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
	m["auto_delete_headless"] = FormatDuration(opts.AutoDelete)
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
