package ui

import (
	"regexp"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/phr0nak/muxlair/internal/agents"
	"github.com/phr0nak/muxlair/internal/clip"
)

// peekView is the conversation popup (p): the latest exchange of one session,
// scrollable. It reads the details the pane already loads, so it shows what the
// pane shows, in full.
type peekView struct {
	key string // the session's Key
	top int    // first body line shown

	sel       *selection // mouse selection: x is the cell from the content's left edge, y the body line
	selecting bool       // mouse button held while selecting
	selBody   string     // the body's text when the selection started; a reload that changes it drops the selection
	msg       string     // result of the last copy, shown at the bottom
}

// peekLine is one line of the popup body.
type peekLine struct {
	text   string // as drawn, with styles
	raw    string // the text to copy: no styles, no indent, trailing spaces kept
	indent int    // cells before raw in text
	soft   bool   // wraps on from the previous line, so a copy joins them without a newline
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

// plainLine is a line with no wrapping or indent.
func plainLine(text string) peekLine {
	return peekLine{text: text, raw: ansi.Strip(text)}
}

// wordRe splits a line into words, each with the spaces after it.
var wordRe = regexp.MustCompile(`\S+ *| +`)

// wrapLine wraps one source line to width cells and returns the pieces with
// their trailing spaces kept, so joining them gives the line back exactly. A
// word wider than the width is broken by cells.
func wrapLine(s string, width int) []string {
	if s == "" || width < 1 {
		return []string{s}
	}
	var out []string
	cur := ""
	for _, tok := range wordRe.FindAllString(s, -1) {
		core := strings.TrimRight(tok, " ")
		if cw := ansi.StringWidth(core); cw > width {
			if cur != "" {
				out, cur = append(out, cur), ""
			}
			chunks := strings.Split(ansi.Hardwrap(core, width, true), "\n")
			out = append(out, chunks[:len(chunks)-1]...)
			cur = chunks[len(chunks)-1] + tok[len(core):]
			continue
		}
		if cur != "" && ansi.StringWidth(cur)+ansi.StringWidth(core) > width {
			out, cur = append(out, cur), ""
		}
		cur += tok
	}
	return append(out, cur)
}

// textLines wraps text (paragraphs separated by newlines) into body lines
// indented by indent cells; the pieces of one source line are soft.
func textLines(text string, width, indent int) []peekLine {
	var out []peekLine
	for _, para := range strings.Split(text, "\n") {
		for i, piece := range wrapLine(para, max(8, width-indent)) {
			out = append(out, peekLine{
				text:   strings.Repeat(" ", indent) + strings.TrimRight(piece, " "),
				raw:    piece,
				indent: indent,
				soft:   i > 0,
			})
		}
	}
	return out
}

// peekLines is the popup's body wrapped to in cells: the prompt and the reply in
// full (up to what was stored), or a line saying why there is nothing to show.
func (m Model) peekLines(in int) []peekLine {
	s, ok := m.peekSession()
	if !ok {
		return []peekLine{plainLine(dim.Render("this session is gone"))}
	}
	e, loaded := m.details[s.Key()]
	switch {
	case !loaded:
		return []peekLine{plainLine(dim.Render("loading…"))}
	case e.err != "":
		return []peekLine{plainLine(dim.Render(e.err))}
	case e.d.Peek.Empty():
		return []peekLine{plainLine(dim.Render("no conversation to show yet"))}
	}
	pk := e.d.Peek
	body := append([]peekLine{plainLine(colHead.Render("You"))}, textLines(pk.Prompt, in, 2)...)
	head := colHead.Render("Agent")
	switch {
	case pk.Reply == "" && s.Status == agents.Busy:
		return append(body, plainLine(""), plainLine(head), peekLine{text: "  " + dim.Render("working…"), raw: "working…", indent: 2})
	case pk.Reply == "":
		return append(body, plainLine(""), plainLine(head), peekLine{text: "  " + dim.Render("no reply yet"), raw: "no reply yet", indent: 2})
	case !pk.Done && s.Status != agents.Busy:
		head += dim.Render(" (the reply stopped early)")
	}
	return append(append(body, plainLine(""), plainLine(head)), textLines(pk.Reply, in, 2)...)
}

// peekBody is the popup's body as drawn lines (for sizing and scrolling).
func (m Model) peekBody(in int) []string {
	lines := m.peekLines(in)
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.text
	}
	return out
}

// peekRoom is how many body lines fit in the popup (borders and the position
// line taken off).
func (m Model) peekRoom() int {
	if m.height <= 0 {
		return 1000
	}
	return max(1, m.boxHeight()-3)
}

// peekBodyText is the body as plain text, to notice when it changes under a selection.
func (m Model) peekBodyText() string {
	return ansi.Strip(strings.Join(m.peekBody(m.wideWidth()-4), "\n"))
}

// peekSelection is the selection, ordered, if there is one and the body it was
// made on is still the body shown.
func (m Model) peekSelection() (selection, bool) {
	v := m.peek
	if v.sel == nil || v.selBody != m.peekBodyText() {
		return selection{}, false
	}
	return v.sel.ordered(), true
}

// highlightLine marks the selected cells of body line i.
func highlightLine(text string, i int, s selection) string {
	if i < s.y1 || i > s.y2 {
		return text
	}
	plain := ansi.Strip(text)
	w := ansi.StringWidth(plain)
	from, to := 0, w
	if i == s.y1 {
		from = min(s.x1, w)
	}
	if i == s.y2 {
		to = min(s.x2+1, w)
	}
	if from >= to {
		return text
	}
	return ansi.Cut(plain, 0, from) + selStyle.Render(ansi.Cut(plain, from, to)) + ansi.Cut(plain, to, w)
}

// peekSelectedText is the selected part of the body. Lines that were wrapped
// from one source line are joined back without a newline, so a paragraph copies
// as it was written.
func peekSelectedText(lines []peekLine, s selection) string {
	var b strings.Builder
	for i := s.y1; i <= s.y2 && i < len(lines); i++ {
		l := lines[i]
		w := ansi.StringWidth(l.raw)
		from, to := 0, w
		if i == s.y1 {
			from = max(0, s.x1-l.indent)
		}
		if i == s.y2 {
			to = min(w, max(0, s.x2-l.indent+1))
		}
		if i > s.y1 && !l.soft {
			b.WriteString("\n")
		}
		if from < to {
			b.WriteString(ansi.Cut(l.raw, from, to))
		}
	}
	return strings.TrimRight(b.String(), " \n")
}

func (m Model) peekPopup() []string {
	w := m.wideWidth()
	lines := m.peekLines(w - 4)
	room := m.peekRoom()
	top := max(0, min(m.peek.top, len(lines)-room))
	end := min(len(lines), top+room)
	sel, hasSel := m.peekSelection()
	var shown []string
	for i := top; i < end; i++ {
		l := lines[i].text
		if hasSel {
			l = highlightLine(l, i, sel)
		}
		shown = append(shown, l)
	}
	var foot []string
	if len(lines) > room {
		foot = append(foot, "↑↓ "+strconv.Itoa(top+1)+"-"+strconv.Itoa(end)+" of "+strconv.Itoa(len(lines)))
	}
	if m.peek.msg != "" {
		foot = append(foot, m.peek.msg)
	}
	// The footer line is always there, even empty, so the popup never changes
	// height when a copy result appears or goes (it is centered, and a move would
	// shift every row under a drag that is about to start).
	line := strings.Join(foot, " · ")
	if m.peek.msg != "" && ansi.StringWidth(line) > w-4 {
		line = m.peek.msg // in a narrow popup the copy result matters more than the position
	}
	shown = append(shown, dim.Render(line))
	return box("Conversation", w, shown)
}

// peekMouse handles the mouse while the popup is open: drag to select (the
// release copies), the wheel scrolls. A drag past the top or bottom edge
// scrolls as it goes. Clicks outside the text only clear the selection.
func (m Model) peekMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	v := m.peek
	w := m.wideWidth()
	lines := m.peekLines(w - 4)
	room := m.peekRoom()
	maxTop := max(0, len(lines)-room)
	v.top = max(0, min(v.top, maxTop))
	pop := m.peekPopup()
	px, py := popupOrigin(m.width, m.boxHeight(), pop)
	rows := min(len(lines), room)
	cx, row := msg.X-(px+2), msg.Y-(py+1) // the content starts after the border and a space
	cols := ansi.StringWidth(pop[0]) - 4
	inside := cx >= 0 && cx < cols && row >= 0 && row < rows
	switch {
	case msg.Button == tea.MouseButtonWheelUp && msg.Action == tea.MouseActionPress:
		v.top = max(0, v.top-3)
	case msg.Button == tea.MouseButtonWheelDown && msg.Action == tea.MouseActionPress:
		v.top = min(maxTop, v.top+3)
	case msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft:
		v.sel, v.selecting, v.msg = nil, false, ""
		if inside {
			v.sel, v.selecting = &selection{cx, v.top + row, cx, v.top + row}, true
			v.selBody = m.peekBodyText()
		}
	case (msg.Action == tea.MouseActionMotion || msg.Action == tea.MouseActionRelease) && v.selecting && v.selBody != m.peekBodyText():
		v.sel, v.selecting = nil, false // the conversation changed under the drag; coordinates no longer mean the same text
	case msg.Action == tea.MouseActionMotion && v.selecting:
		switch {
		case msg.Y < py+1:
			v.top = max(0, v.top-1)
		case msg.Y > py+rows:
			v.top = min(maxTop, v.top+1)
		}
		v.sel.x2 = max(0, min(cx, cols-1))
		v.sel.y2 = v.top + max(0, min(row, rows-1))
	case msg.Action == tea.MouseActionRelease && v.selecting:
		v.selecting = false
		// The release carries the final position; a quick drag may end before any motion arrives.
		v.sel.x2 = max(0, min(cx, cols-1))
		v.sel.y2 = v.top + max(0, min(row, rows-1))
		if *v.sel == (selection{v.sel.x1, v.sel.y1, v.sel.x1, v.sel.y1}) {
			v.sel = nil // a plain click selects nothing
			return m, nil
		}
		text := peekSelectedText(lines, v.sel.ordered())
		if text == "" {
			return m, nil
		}
		return m, func() tea.Msg {
			via, err := clip.Copy(text)
			if err != nil {
				return copiedMsg{err: err.Error()}
			}
			return copiedMsg{n: len([]rune(text)), via: via}
		}
	}
	return m, nil
}

// peekKey handles keys while the conversation popup is open.
func (m Model) peekKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	v := m.peek
	maxTop := max(0, len(m.peekBody(m.wideWidth()-4))-m.peekRoom())
	if ks := k.String(); ks != "up" && ks != "down" {
		v.sel, v.selecting, v.msg = nil, false, ""
	}
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
