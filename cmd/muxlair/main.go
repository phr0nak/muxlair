package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"sort"
	"strings"
	"text/tabwriter"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phr0nak/muxlair/internal/agents"
	"github.com/phr0nak/muxlair/internal/config"
	"github.com/phr0nak/muxlair/internal/ui"
)

// version is set at build time: go build -ldflags "-X main.version=v0.1.0".
var version = ""

const usage = `muxlair: a terminal dashboard for your local AI coding-agent sessions.

Usage:
  muxlair             start the dashboard (run it inside tmux, cmux or Rex)
  muxlair list [agent]
                      print sessions as an aligned table and exit; agent is
                      claude, codex, opencode, pi or agy (default: all)
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
		case "list":
			if len(os.Args) > 3 {
				fmt.Fprintf(os.Stderr, "usage: muxlair list [%s]\n", strings.Join(agents.Names(), "|"))
				os.Exit(2)
			}
			list := agents.List
			if len(os.Args) == 3 {
				a, ok := agents.ByName(os.Args[2])
				if !ok {
					fmt.Fprintf(os.Stderr, "muxlair: unknown agent %q (want %s)\n", os.Args[2], strings.Join(agents.Names(), ", "))
					os.Exit(2)
				}
				// Only that agent is read, so another agent's error can't get in the way.
				list = func() ([]agents.Session, error) { return agents.ListOf(a) }
			}
			listSessions(list)
			return
		case "trash", "restore", "export":
			// Removed in v0.5.0; the trash on disk is left alone, and v0.4.1 can still read it.
			fmt.Fprintf(os.Stderr, "muxlair: %q was removed in v0.5.0. ~/.muxlair/trash is kept as it was;\n"+
				"to restore from it, use v0.4.1: go install github.com/phr0nak/muxlair/cmd/muxlair@v0.4.1\n", os.Args[1])
			os.Exit(2)
		default:
			fmt.Fprintf(os.Stderr, "muxlair: unknown argument %q\n\n%s", os.Args[1], usage)
			os.Exit(2)
		}
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

// listSessions prints the sessions list returns, newest first.
func listSessions(list func() ([]agents.Session, error)) {
	ss, err := list()
	if err != nil {
		fmt.Fprintln(os.Stderr, err) // partial results still listed below
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0) // aligned columns; NAME last so its length can't push the rest around
	fmt.Fprintln(w, "STATUS\tID\tUPDATED\tAGENT\tBRANCH\tTTY\tMAYBELIVE\tCWD\tNAME")
	sort.Slice(ss, func(i, j int) bool { return ss[i].Updated.After(ss[j].Updated) })
	for _, s := range ss {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%v\t%s\t%s\n", s.Status, s.ID, s.Updated.Format("2006-01-02 15:04"), s.Agent, s.BranchLabel(), s.TTY, s.MaybeLive, s.Cwd, firstWords(s.Name, 6))
	}
	w.Flush()
}

// firstWords keeps the first n words of s, with "…" when it cut any.
func firstWords(s string, n int) string {
	f := strings.Fields(s)
	if len(f) <= n {
		return strings.Join(f, " ")
	}
	return strings.Join(f[:n], " ") + "…"
}
