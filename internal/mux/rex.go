package mux

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// In Rex, a session holds windows (tabs) that hold blocks (panes). A Target for
// Rex uses Workspace for the session ID, Window for the window ID and Pane for
// the block ID.

// rexBlocksScript lists every terminal block on the Rex server with its window
// and the pid of the program Rex started in it, in one call: a CLI call per
// block would cost a round trip each on every refresh.
const rexBlocksScript = `
local out = {}
for _, s in ipairs(rex.call("session.list", {}).sessions or {}) do
  for _, b in ipairs(rex.call("session.list_blocks", {session_id = s.session_id}).blocks or {}) do
    if b.window_id and b.creator_name == "com.superlogical.terminal" then
      -- Terminals with no running program still count when telling a shared tab from a solo one;
      -- other blocks (not terminals) don't, like a browser tab beside a cmux terminal.
      local ok, p = pcall(rex.call, "block.method", {session_id = s.session_id, block_id = b.block_id, method = "process"})
      local pid = (ok and p and p.child and p.child.pid) or 0
      out[#out + 1] = {session = s.session_id, window = b.window_id, block = b.block_id, pid = pid}
    end
  end
end
return out`

type rexBlock struct {
	Session, Window, Block string
	PID                    int
}

// rexTargets maps the tty of each Rex terminal block to its Target. It never
// starts a Rex server: with none running there is nothing to find.
func rexTargets() map[string]Target {
	if _, err := exec.LookPath("rex"); err != nil {
		return nil
	}
	out, err := output("rex", "--autostart=false", "do", "--json", "-e", rexBlocksScript)
	if err != nil {
		return nil
	}
	blocks, err := parseRexBlocks(out)
	if err != nil || len(blocks) == 0 {
		return nil
	}
	var pids []string
	for _, b := range blocks {
		if b.PID > 0 {
			pids = append(pids, strconv.Itoa(b.PID))
		}
	}
	if len(pids) == 0 {
		return nil
	}
	// ps exits 1 when a pid is gone but still lists the others.
	ps, _ := exec.Command("ps", "-o", "pid=,tty=", "-p", strings.Join(pids, ",")).Output()
	return rexTargetMap(blocks, string(ps))
}

// parseRexBlocks reads the script's result: a JSON array, or {} when empty
// (Lua can't tell an empty array from an empty table).
func parseRexBlocks(out string) ([]rexBlock, error) {
	if strings.TrimSpace(out) == "{}" {
		return nil, nil
	}
	var bs []rexBlock
	return bs, json.Unmarshal([]byte(out), &bs)
}

// rexTargetMap joins blocks with `ps -o pid=,tty=` output by pid.
func rexTargetMap(blocks []rexBlock, ps string) map[string]Target {
	ttys := map[int]string{}
	for _, l := range strings.Split(ps, "\n") {
		f := strings.Fields(l)
		if len(f) != 2 || f[1] == "??" {
			continue
		}
		if pid, err := strconv.Atoi(f[0]); err == nil {
			ttys[pid] = strings.TrimPrefix(f[1], "/dev/")
		}
	}
	perWindow := map[string]int{}
	for _, b := range blocks {
		perWindow[b.Window]++
	}
	m := map[string]Target{}
	for _, b := range blocks {
		if tty := ttys[b.PID]; tty != "" {
			m[tty] = Target{Mux: "rex", Pane: b.Block, Window: b.Window, Workspace: b.Session, Solo: perWindow[b.Window] == 1}
		}
	}
	return m
}

// rexFocus focuses the block, then has the Rex app show its tab: the app keeps
// its own selected tab, so focusing on the server alone changes nothing on
// screen.
func rexFocus(t Target) error {
	if err := run("rex", "--autostart=false", "focus", "-s", t.Workspace, t.Pane); err != nil {
		return err
	}
	return rexShow(t.Workspace, t.Window)
}

// rexShow switches a Rex app to the tab (window) in session: an app attached to
// muxlair's own Rex session, or from outside Rex any Rex app. The app only does
// this with Remote Control turned on in its settings.
// shortcut: Rex doesn't say which app is in front, so with several apps attached
// to muxlair's session the first one that accepts switches; upgrade if Rex
// exposes the app in front.
func rexShow(session, window string) error {
	own := os.Getenv("REX_SESSION") // also set when muxlair runs in tmux inside a Rex tab
	out, err := output("rex", "--autostart=false", "client", "ls", "--json")
	if err != nil {
		return err
	}
	var cl struct {
		Clients []struct {
			ID         string   `json:"client_id"`
			SessionIDs []string `json:"session_ids"`
			Info       struct {
				Kind string `json:"kind"`
			} `json:"info"`
		} `json:"clients"`
	}
	if err := json.Unmarshal([]byte(out), &cl); err != nil {
		return err
	}
	var errs []error
	for _, c := range cl.Clients {
		if c.Info.Kind != "app" || (own != "" && !slices.Contains(c.SessionIDs, own)) {
			continue
		}
		// One app failing (closing, another machine) must not stop the others.
		err := run("rex", "--autostart=false", "-C", c.ID, "do", "session.select", "session_id="+session, "window_id="+window)
		if err == nil {
			return nil
		}
		errs = append(errs, err)
	}
	msg := "the Rex app didn't switch to its tab"
	switch {
	case len(errs) == 0:
		msg += ": no Rex app is attached"
	case strings.Contains(errors.Join(errs...).Error(), "Remote Control"):
		msg += ": turn on Remote Control in Rex's Settings, under Rex Server"
	default:
		msg += ": " + errors.Join(errs...).Error()
	}
	return errors.New(msg)
}

// rexRename renames the tab; skipped when the tab holds other blocks (tmuxRename's rule).
func rexRename(t Target, name string) error {
	if !t.Solo {
		return nil
	}
	return run("rex", "--autostart=false", "window", "rename", "-s", t.Workspace, t.Window, rexLabel(name))
}

// rexSend types text into the block and presses Enter after enterDelay.
func rexSend(t Target, text string) error {
	if err := run("rex", "--autostart=false", "send", "-s", t.Workspace, "-b", t.Pane, "--", text); err != nil {
		return err
	}
	time.Sleep(enterDelay)
	return run("rex", "--autostart=false", "send-key", "-s", t.Workspace, "-b", t.Pane, "enter")
}

// rexScreen is the block's last screenful of text. styled keeps colors and
// styles as SGR sequences (for Claude's dim placeholder), like tmux -e.
// shortcut: Rex can only capture the whole scrollback, so each prompt check
// reads all of it and keeps the end; upgrade when Rex can capture just the screen.
func rexScreen(t Target, styled bool) (string, error) {
	args := []string{"--autostart=false", "capture", "-s", t.Workspace, "-b", t.Pane, "--trim"}
	if styled {
		args = append(args, "--format=vt")
	}
	out, err := output("rex", args...)
	if err != nil {
		return "", err
	}
	if styled {
		out = rexVT(out)
	}
	lines := strings.Split(out, "\n") // the capture holds the whole scrollback
	return strings.Join(lines[max(0, len(lines)-80):], "\n"), nil
}

var (
	rexOSC = regexp.MustCompile("\x1b\\][^\x07\x1b]*(\x07|\x1b\\\\)")
	rexCSI = regexp.MustCompile("\x1b\\[[0-9;:?]*[ -/]*[@-~]")
)

// rexVT keeps only SGR sequences of Rex's vt capture: cursor-forward moves
// become the spaces they skip, and other controls (OSC titles and links,
// cursor moves, erases) are dropped.
// shortcut: Rex 0.1 captures write rows as lines with only SGR, cursor-forward
// and OSC; a capture positioned with cursor moves would not be rebuilt, and the
// prompt check then fails closed (no rename typed). Upgrade if Rex starts doing that.
func rexVT(s string) string {
	s = rexOSC.ReplaceAllString(s, "")
	return rexCSI.ReplaceAllStringFunc(s, func(m string) string {
		switch m[len(m)-1] {
		case 'm':
			return m
		case 'C':
			n, err := strconv.Atoi(m[2 : len(m)-1])
			if err != nil || n < 1 {
				n = 1
			}
			return strings.Repeat(" ", n)
		}
		return ""
	})
}

// rexOpen starts cmd in cwd in a new tab (a Rex "window") of the Rex session
// muxlair runs in, and has the Rex app show it, like a new tmux window.
func rexOpen(name, cwd, cmd string) error {
	session := os.Getenv("REX_SESSION")
	out, err := output("rex", "window", "new", "-s", session, rexLabel(name), "-c", cwd, "--json", "--", "sh", "-c", cmd)
	if err != nil {
		return err
	}
	var w struct {
		WindowID string `json:"window_id"`
	}
	if json.Unmarshal([]byte(out), &w) != nil || w.WindowID == "" {
		return nil // opened; there is just no tab to show
	}
	if err := rexShow(session, w.WindowID); err != nil {
		return NotShown{err.Error()}
	}
	return nil
}

// NotShown is Open's error when the session was opened but its new tab isn't
// on screen: the open itself succeeded, so callers must not retry it.
type NotShown struct{ Why string }

func (e NotShown) Error() string { return "opened in a new tab, but " + e.Why }

// rexLabel makes name safe as rex's positional tab label: a leading dash would
// be read as a flag.
func rexLabel(name string) string {
	if l := strings.TrimLeft(name, "-"); strings.TrimSpace(l) != "" {
		return l
	}
	return "session"
}
