package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"text/tabwriter"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/sys/unix"

	"github.com/phr0nak/muxlair/internal/agents"
	"github.com/phr0nak/muxlair/internal/config"
	"github.com/phr0nak/muxlair/internal/ui"
)

// version is set at build time: go build -ldflags "-X main.version=v0.1.0".
var version = ""

const usage = `muxlair: a terminal dashboard for local Claude Code and Codex sessions.

Usage:
  muxlair             start the dashboard (run it inside tmux or cmux)
  muxlair list        print sessions as an aligned table and exit
  muxlair trash       list sessions in ~/.muxlair/trash
  muxlair restore <agent> <id>
                      put a trashed session back (agent: claude or codex)
  muxlair export codex <id>
                      print a deleted Codex session's saved history as markdown
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
		case "list", "--list": // --list is what v0.2 called it; kept so existing scripts keep working
			listSessions()
			return
		case "trash":
			listTrash()
			return
		case "export":
			if len(os.Args) != 4 || os.Args[2] != "codex" {
				fmt.Fprint(os.Stderr, "usage: muxlair export codex <session id>\n")
				os.Exit(2)
			}
			md, err := agents.ExportCodex(os.Args[3])
			if err != nil {
				fmt.Fprintln(os.Stderr, "muxlair: export:", err)
				os.Exit(1)
			}
			fmt.Print(md)
			return
		case "restore":
			if len(os.Args) != 4 {
				fmt.Fprint(os.Stderr, "usage: muxlair restore <claude|codex> <session id>\n")
				os.Exit(2)
			}
			notes, err := agents.Restore(os.Args[2], os.Args[3])
			if err != nil {
				fmt.Fprintln(os.Stderr, "muxlair: restore:", err)
				os.Exit(1)
			}
			fmt.Printf("restored %s %s; it shows up in muxlair within a few seconds\n", os.Args[2], os.Args[3])
			for _, n := range notes {
				fmt.Println("note:", n)
			}
			return
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

func listSessions() {
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
}

func listTrash() {
	es, err := agents.ListTrash()
	if err != nil {
		fmt.Fprintln(os.Stderr, err) // partial results still listed below
	}
	rows := [][]string{{"AGENT", "ID", "DELETED", "RESTORABLE", "RESTORED"}}
	for _, e := range es {
		restored := "-"
		if e.Restored() {
			restored = e.Meta.RestoredAt.Local().Format("2006-01-02 15:04")
		}
		rows = append(rows, []string{e.Meta.Agent, e.Meta.ID, e.Meta.DeletedAt.Local().Format("2006-01-02 15:04"), e.State(), restored})
	}
	// NAME gets what the terminal leaves after the other columns (and their 2-cell
	// gaps), between 10 and 100 cells; 100 when stdout is not a terminal.
	fixed := 2 * len(rows[0])
	for c := range rows[0] {
		w := 0
		for _, r := range rows {
			w = max(w, len(r[c]))
		}
		fixed += w
	}
	nameW := 100
	if ws, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ); err == nil && ws.Col > 0 {
		nameW = min(100, max(10, int(ws.Col)-fixed))
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, strings.Join(rows[0], "\t")+"\tNAME")
	for i, e := range es {
		fmt.Fprintln(w, strings.Join(rows[i+1], "\t")+"\t"+e.ShortName(nameW))
	}
	w.Flush()
}
