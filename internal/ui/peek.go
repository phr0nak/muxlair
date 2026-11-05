package ui

import (
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/phr0nak/muxlair/internal/agents"
)

// peekView is the conversation popup (p): the latest exchange of one session,
// scrollable. It reads the details the pane already loads, so it shows what the
// pane shows, in full.
type peekView struct {
	key string // the session's Key
	top int    // first body line shown
}

// clipText flattens text to one paragraph, wraps it to width cells and keeps at
// most n lines, ending the last with an ellipsis when it cut something.
func clipText(text string, width, n int) []string {
	if width < 1 || n < 1 {
		return nil
	}
	lines := strings.Split(ansi.Wrap(strings.Join(strings.Fields(text), " "), width, ""), "\n")
	if len(lines) > n {
		lines = lines[:n]
		lines[n-1] = ansi.Truncate(lines[n-1], width-1, "") + "…"
	}
	return lines
}

// peekPane is the "Latest exchange" block for the details pane: a blank line, a
// heading, the prompt (up to 2 lines) and the reply (up to 4), within room lines.
// It returns nil when there is not room for at least one line of each.
func peekPane(label func(k, v string) string, pk agents.Peek, busy bool, w, room int) []string {
	const labelW = 11
	if room < 4 || w <= labelW+3 {
		return nil
	}
	avail := room - 2 // minus the blank line and the heading
	nPrompt := min(2, avail-1)
	nReply := min(4, avail-nPrompt)
	heading := " " + colHead.Render("Latest exchange")
	reply := clipText(pk.Reply, w-labelW-1, nReply) // one cell short of the border, like the metrics
	switch {
	case pk.Reply == "" && busy:
		reply = []string{dim.Render("working…")}
	case pk.Reply == "":
		reply = []string{dim.Render("no reply yet")}
	case !pk.Done && !busy:
		heading += dim.Render(" · stopped early")
	}
	out := []string{"", ansi.Truncate(heading, w-1, "…")} // never past the pane border
	for i, l := range clipText(pk.Prompt, w-labelW-1, nPrompt) {
		k := ""
		if i == 0 {
			k = "You"
		}
		out = append(out, label(k, l))
	}
	for i, l := range reply {
		k := ""
		if i == 0 {
			k = "Agent"
		}
		out = append(out, label(k, l))
	}
	return out
}

// peekSession finds the session the popup is about.
func (m Model) peekSession() (agents.Session, bool) {
	for _, s := range m.sessions {
		if s.Key() == m.peek.key {
			return s, true
		}
	}
	return agents.Session{}, false
}

// peekBody is the popup's body wrapped to in cells: the prompt and the reply in
// full (up to what was stored), or a line saying why there is nothing to show.
func (m Model) peekBody(in int) []string {
	s, ok := m.peekSession()
	if !ok {
		return []string{dim.Render("this session is gone")}
	}
	e, loaded := m.details[s.Key()]
	switch {
	case !loaded:
		return []string{dim.Render("loading…")}
	case e.err != "":
		return []string{dim.Render(e.err)}
	case e.d.Peek.Empty():
		return []string{dim.Render("no conversation to show yet")}
	}
	pk := e.d.Peek
	wrap := func(text string) []string {
		var out []string
		for _, l := range strings.Split(ansi.Wrap(text, max(8, in-2), ""), "\n") {
			out = append(out, "  "+l)
		}
		return out
	}
	body := append([]string{colHead.Render("You")}, wrap(pk.Prompt)...)
	head := colHead.Render("Agent")
	switch {
	case pk.Reply == "" && s.Status == agents.Busy:
		return append(body, "", head, "  "+dim.Render("working…"))
	case pk.Reply == "":
		return append(body, "", head, "  "+dim.Render("no reply yet"))
	case !pk.Done && s.Status != agents.Busy:
		head += dim.Render(" (the reply stopped early)")
	}
	return append(append(body, "", head), wrap(pk.Reply)...)
}

// peekRoom is how many body lines fit in the popup (borders and the position
// line taken off).
func (m Model) peekRoom() int {
	if m.height <= 0 {
		return 1000
	}
	return max(1, m.boxHeight()-3)
}

func (m Model) peekPopup() []string {
	w := m.wideWidth()
	body := m.peekBody(w - 4)
	room := m.peekRoom()
	top := max(0, min(m.peek.top, len(body)-room))
	if len(body) <= room {
		return box("Conversation", w, body)
	}
	shown := append([]string(nil), body[top:min(len(body), top+room)]...)
	pos := dim.Render("↑↓ " + strconv.Itoa(top+1) + "-" + strconv.Itoa(min(len(body), top+room)) + " of " + strconv.Itoa(len(body)))
	return box("Conversation", w, append(shown, pos))
}

// peekKey handles keys while the conversation popup is open.
func (m Model) peekKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	v := m.peek
	maxTop := max(0, len(m.peekBody(m.wideWidth()-4))-m.peekRoom())
	switch k.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q", "p":
		m.peek = nil
	case "up":
		v.top = max(0, v.top-1)
	case "down":
		v.top = min(maxTop, v.top+1)
	}
	return m, nil
}
