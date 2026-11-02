package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"text/tabwriter"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phr0nak/muxlair/internal/agents"
	"github.com/phr0nak/muxlair/internal/config"
	"github.com/phr0nak/muxlair/internal/ui"
)

// version is set at build time: go build -ldflags "-X main.version=v0.1.0".
var version = ""

const usage = `muxlair: a terminal dashboard for local Claude Code and Codex sessions.

Usage:
  muxlair             start the dashboard (run it inside tmux or cmux)
  muxlair --list      print sessions as an aligned table and exit (debug)
  muxlair --version   print the version and exit
  muxlair --help      show this help

Press ? in the dashboard to see every key.
`

// buildVersion is the -ldflags value, else the module version that go install
// recorded, else "dev" for a plain go build.
func buildVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--help", "-h", "help":
			fmt.Print(usage)
			return
		case "--version", "-v", "version":
			fmt.Println("muxlair", buildVersion())
			return
		case "--list":
		default:
			fmt.Fprintf(os.Stderr, "muxlair: unknown argument %q\n\n%s", os.Args[1], usage)
			os.Exit(2)
		}
	}
	if len(os.Args) > 1 && os.Args[1] == "--list" {
		ss, err := agents.List()
		if err != nil {
			fmt.Fprintln(os.Stderr, err) // partial results still listed below
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0) // aligned columns; NAME last so its length can't push the rest around
		fmt.Fprintln(w, "STATUS\tID\tUPDATED\tAGENT\tTTY\tMAYBELIVE\tCWD\tNAME")
		for _, s := range ss {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%v\t%s\t%s\n", s.Status, s.ID, s.Updated.Format("2006-01-02 15:04"), s.Agent, s.TTY, s.MaybeLive, s.Cwd, s.Name)
		}
		w.Flush()
		return
	}
	path := config.Path(agents.Home)
	opts, cfgErr := config.Load(path) // on error: defaults, and the problem shown in the app
	progOpts := []tea.ProgramOption{tea.WithAltScreen()}
	if opts.Mouse {
		progOpts = append(progOpts, tea.WithMouseCellMotion())
	}
	m := ui.New(opts, path)
	if cfgErr != nil {
		m = m.WithStatus("config not applied, using defaults: " + cfgErr.Error())
	}
	if _, err := tea.NewProgram(m, progOpts...).Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
