package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/phr0nak/muxlair/internal/agents"
	"github.com/phr0nak/muxlair/internal/config"
)

func TestGroupLabels(t *testing.T) {
	got := groupLabels([]string{"/a/x/null", "/b/y/null", "/c/twinpeaks", "/d/x/null2"})
	want := map[string]string{"/a/x/null": "x/null", "/b/y/null": "y/null", "/c/twinpeaks": "twinpeaks", "/d/x/null2": "null2"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q want %q", k, got[k], v)
		}
	}
}

func TestMouse(t *testing.T) {
	m := New(Options{Mouse: true}, "")
	m.height = 20
	m.sessions = []agents.Session{
		{Agent: "claude", ID: "1", Cwd: "/a", Updated: time.Now()},
		{Agent: "codex", ID: "2", Cwd: "/b", Updated: time.Now().Add(-time.Minute)},
	}
	m.rebuild() // rows: /a header, s1, spacer, /b header, s2
	click := func(y int) tea.Cmd {
		nm, cmd := m.mouse(tea.MouseMsg{X: 3, Y: y + firstRowY, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
		m = nm.(Model)
		return cmd
	}
	if click(1) != nil || m.cur != 1 {
		t.Fatalf("single click: cur=%d", m.cur)
	}
	if click(1) == nil {
		t.Fatal("double click did not open")
	}
	if click(2) != nil || m.cur != 1 {
		t.Fatalf("spacer click moved the cursor: cur=%d", m.cur)
	}
	click(3) // header /b
	if !m.collapsed["/b"] || len(m.rows) != 4 {
		t.Fatalf("header click: collapsed=%v rows=%d", m.collapsed, len(m.rows))
	}
	nm, _ := m.mouse(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	if m = nm.(Model); m.cur != 1 {
		t.Fatalf("wheel up skipped no spacer: cur=%d", m.cur)
	}
}

func TestClickDoesNotScroll(t *testing.T) {
	m := New(Options{Mouse: true}, "")
	m.height = 20 + chromeRows // 20 visible rows
	for i := 0; i < 40; i++ {
		m.sessions = append(m.sessions, agents.Session{Agent: "claude", ID: string(rune('a' + i)), Cwd: "/a", Updated: time.Now()})
	}
	m.rebuild()
	press := tea.MouseMsg{Y: 15 + firstRowY, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}
	nm, _ := m.Update(press)
	m = nm.(Model)
	if m.cur != 15 || m.top != 0 {
		t.Fatalf("after click: cur=%d top=%d", m.cur, m.top)
	}
	if _, cmd := m.Update(press); cmd == nil {
		t.Fatal("second click on same row did not open")
	}
}

func TestFooterAtBottom(t *testing.T) {
	m := New(Options{}, "")
	m.height = 10
	m.sessions = []agents.Session{{Agent: "claude", ID: "1", Cwd: "/a", Updated: time.Now()}}
	m.rebuild()
	lines := strings.Split(m.View(), "\n")
	if len(lines) != 10 || !strings.Contains(lines[9], "quit") {
		t.Fatalf("got %d lines, last %q", len(lines), lines[len(lines)-1])
	}
}

func TestRenameKeys(t *testing.T) {
	m := New(Options{}, "")
	m.height = 20
	m.sessions = []agents.Session{{Agent: "claude", ID: "1", Name: "old", Cwd: "/a", Updated: time.Now()}}
	m.rebuild()
	m.cur = 1
	press := func(k tea.KeyMsg) tea.Cmd {
		nm, cmd := m.Update(k)
		m = nm.(Model)
		return cmd
	}
	press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if m.editing == nil || m.editBuf != "old" {
		t.Fatalf("r did not start rename: %+v", m.editing)
	}
	press(tea.KeyMsg{Type: tea.KeyBackspace})
	press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("D")}) // capital letters are text here, not refresh
	if m.editBuf != "olD" {
		t.Fatalf("buf %q", m.editBuf)
	}
	if press(tea.KeyMsg{Type: tea.KeyEnter}) == nil || m.editing != nil {
		t.Fatal("enter did not submit")
	}
}

func TestRenameEditorScrolls(t *testing.T) {
	m := New(Options{}, "")
	m.height, m.width = 20, 50
	s := agents.Session{Agent: "codex", Name: strings.Repeat("x", 100) + "END"}
	m.editing, m.editBuf = &s, s.Name
	if v := m.View(); !strings.Contains(v, "END█") {
		t.Fatalf("cursor end not visible:\n%s", v)
	}
}

func TestRenameEditorWideChars(t *testing.T) {
	m := New(Options{}, "")
	m.height, m.width = 20, 50
	s := agents.Session{Agent: "codex", Name: strings.Repeat("漢", 30) + "終"}
	m.editing, m.editBuf = &s, s.Name
	for _, l := range strings.Split(m.View(), "\n") {
		if ansi.StringWidth(l) > 50 {
			t.Fatalf("line wider than terminal (50): %q", l)
		}
	}
	if v := m.View(); !strings.Contains(v, "終█") {
		t.Fatalf("cursor end not visible:\n%s", v)
	}
}

func TestFoldAndResize(t *testing.T) {
	m := New(Options{Details: true}, "")
	m.height, m.width = 30, 140
	m.sessions = []agents.Session{
		{Agent: "claude", ID: "1", Cwd: "/a", Updated: time.Now()},
		{Agent: "codex", ID: "2", Cwd: "/b", Updated: time.Now().Add(-time.Minute)},
	}
	m.rebuild()
	m.cur = 1 // session in /a
	key := func(s string) {
		k := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
		switch s {
		case "left":
			k = tea.KeyMsg{Type: tea.KeyLeft}
		case "right":
			k = tea.KeyMsg{Type: tea.KeyRight}
		}
		nm, _ := m.Update(k)
		m = nm.(Model)
	}
	key("left")
	if !m.collapsed["/a"] || m.cur != 0 {
		t.Fatalf("left: collapsed=%v cur=%d", m.collapsed, m.cur)
	}
	key("right")
	if m.collapsed["/a"] || len(m.rows) != 5 {
		t.Fatalf("right: collapsed=%v rows=%d", m.collapsed, len(m.rows))
	}
	_, before := m.split()
	key("<")
	if _, after := m.split(); after != before+4 {
		t.Fatalf("< : %d -> %d", before, after)
	}
	lw, _ := m.split()
	drag := func(a tea.MouseAction, x int) {
		nm, _ := m.Update(tea.MouseMsg{X: x, Y: 5, Action: a, Button: tea.MouseButtonLeft})
		m = nm.(Model)
	}
	drag(tea.MouseActionPress, lw+1)
	drag(tea.MouseActionMotion, 100)
	drag(tea.MouseActionRelease, 100)
	if _, dw := m.split(); dw != 140-100-2 || m.dragging {
		t.Fatalf("drag: dw=%d dragging=%v", dw, m.dragging)
	}
}

func TestRefreshStatusClears(t *testing.T) {
	m := New(Options{}, "")
	m.status = "refreshing…"
	nm, _ := m.Update(loadedMsg{})
	if s := nm.(Model).status; s != "" {
		t.Fatalf("status %q", s)
	}
}

func TestDragClamped(t *testing.T) {
	m := New(Options{Details: true}, "")
	m.height, m.width = 30, 140
	lw, _ := m.split()
	for _, ev := range []tea.MouseMsg{
		{X: lw + 1, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft},
		{X: 139, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft},
	} {
		nm, _ := m.Update(ev)
		m = nm.(Model)
	}
	if _, dw := m.split(); dw != 24 {
		t.Fatalf("dragged to the edge: dw=%d, want minimum 24", dw)
	}
}

func TestSettingsPopup(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	m := New(config.Defaults(), cfg)
	m.height, m.width = 30, 120
	key := func(s string) tea.Cmd {
		k := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
		switch s {
		case "down":
			k = tea.KeyMsg{Type: tea.KeyDown}
		case "right":
			k = tea.KeyMsg{Type: tea.KeyRight}
		case "enter":
			k = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			k = tea.KeyMsg{Type: tea.KeyEsc}
		}
		nm, cmd := m.Update(k)
		m = nm.(Model)
		return cmd
	}
	key("s")
	if m.settings == nil || !strings.Contains(m.View(), "Settings") {
		t.Fatal("s did not open settings")
	}
	key(" ") // mouse off
	key("down")
	key("down")
	key("down")
	key("right") // conversation peek off
	key("esc")
	if m.settings != nil || !m.opts.Mouse {
		t.Fatal("esc applied changes")
	}
	if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Fatal("esc wrote the file")
	}
	key("s")
	key(" ")
	key("down")
	key("down")
	key("down")
	key("right")
	key("down") // Save
	key("enter")
	if m.settings != nil || m.opts.Mouse || m.opts.Peek {
		t.Fatalf("save not applied: %+v", m.opts)
	}
	got, err := config.Load(cfg)
	if err != nil || got != m.opts {
		t.Fatalf("file: %+v %v", got, err)
	}
}

func TestConfigReload(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	m := New(config.Defaults(), cfg)
	m.showDetails = false // user pressed i
	os.WriteFile(cfg, []byte(`{"color_tabs": false}`), 0o600)
	nm, _ := m.Update(configMsg{})
	m = nm.(Model)
	if m.opts.ColorTabs || m.showDetails {
		t.Fatalf("reload: opts=%+v showDetails=%v (details setting unchanged, so i stays)", m.opts, m.showDetails)
	}
	os.WriteFile(cfg, []byte(`{broken`), 0o600)
	nm, _ = m.Update(configMsg{})
	m = nm.(Model)
	if m.opts.ColorTabs || !strings.Contains(m.status, "config not applied") {
		t.Fatalf("broken file: opts=%+v status=%q", m.opts, m.status)
	}
}

func TestSettingsClickOverDivider(t *testing.T) {
	m := New(config.Defaults(), filepath.Join(t.TempDir(), "config.json"))
	m.height, m.width = 30, 120
	m.settings = &settingsForm{opts: m.opts}
	lw, _ := m.split()
	nm, _ := m.Update(tea.MouseMsg{X: lw + 1, Y: 10, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if nm.(Model).dragging {
		t.Fatal("click inside the settings popup started a divider drag")
	}
}

func TestHelpPopup(t *testing.T) {
	m := New(Options{}, "")
	m.height, m.width = 30, 120
	m.opts.Mouse = true
	lines := strings.Split(m.View(), "\n")
	if !strings.HasPrefix(strings.TrimLeft(ansi.Strip(lines[len(lines)-1]), " "), "? keys") {
		t.Fatalf("bottom bar does not start with ? keys: %q", lines[len(lines)-1])
	}
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	m = nm.(Model)
	v := m.View()
	if !m.help || !strings.Contains(v, "Keys") || !strings.Contains(v, "resize details") {
		t.Fatalf("? did not show the keys popup:\n%s", v)
	}
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if m = nm.(Model); m.help || m.settings != nil {
		t.Fatal("a key closing help also acted on the list")
	}
}

func TestFooterCentered(t *testing.T) {
	m := New(Options{}, "")
	m.height, m.width = 20, 200
	m.status = "hello"
	lines := strings.Split(m.View(), "\n")
	last := lines[len(lines)-1]
	if pad := len(last) - len(strings.TrimLeft(last, " ")); pad != (200-5)/2 {
		t.Fatalf("footer pad %d: %q", pad, last)
	}
}

func TestHelpReachableEverywhere(t *testing.T) {
	// Every height from tiny to roomy, at a narrow and a wide width: no panic,
	// everything reachable (19 rows once hit body fits but hint doesn't).
	for h := 7; h <= 30; h++ {
		helpReachable(t, h, 44)
		helpReachable(t, h, 120)
	}
	for _, size := range [][2]int{} {
		helpReachable(t, size[0], size[1])
	}
}

// helpReachable scrolls the keys popup to the end and checks every word of
// every description was on screen at some point.
func helpReachable(t *testing.T, h, w int) {
	m := New(Options{}, "")
	m.height, m.width = h, w
	m.help = true
	var screens strings.Builder
	for i := 0; i < 3*len(keyHelp); i++ {
		screens.WriteString(ansi.Strip(m.View()))
		nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = nm.(Model)
		if !m.help {
			t.Fatal("down closed the help")
		}
	}
	for _, k := range keyHelp {
		for _, word := range strings.Fields(k[1]) {
			if !strings.Contains(screens.String(), word) {
				t.Errorf("%dx%d: %q never shown (from %q)", w, h, word, k[1])
			}
		}
	}
	// Clamped: one up after over-scrolling moves the view at once.
	before := m.helpTop
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if before > 0 && nm.(Model).helpTop != before-1 {
		t.Errorf("%dx%d: up after over-scrolling: %d -> %d", w, h, before, nm.(Model).helpTop)
	}
}

func TestHelpScrollClampedOnResize(t *testing.T) {
	m := New(Options{}, "")
	m.height, m.width, m.help = 7, 44, true
	for i := 0; i < 40; i++ {
		nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = nm.(Model)
	}
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 44, Height: 20})
	m = nm.(Model)
	if m.helpTop != m.helpMaxTop() {
		t.Fatalf("helpTop %d not clamped to %d", m.helpTop, m.helpMaxTop())
	}
	if top := m.helpTop; top > 0 {
		nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
		if nm.(Model).helpTop != top-1 {
			t.Fatal("up did not move right after the resize")
		}
	}
}

func TestFolderCount(t *testing.T) {
	m := New(Options{}, "")
	m.height, m.width = 20, 100
	m.sessions = []agents.Session{
		{Agent: "claude", ID: "1", Name: "alpha", Cwd: "/a", Updated: time.Now()},
		{Agent: "codex", ID: "2", Name: "beta", Cwd: "/a", Updated: time.Now()},
		{Agent: "codex", ID: "3", Name: "gamma", Cwd: "/b", Updated: time.Now()},
	}
	m.rebuild()
	v := ansi.Strip(m.View())
	if !strings.Contains(v, "a (2)") || !strings.Contains(v, "b (1)") {
		t.Fatalf("counts missing:\n%s", v)
	}
	m.filter = "alpha"
	m.rebuild()
	if v = ansi.Strip(m.View()); !strings.Contains(v, "a (1)") {
		t.Fatalf("count ignores the filter:\n%s", v)
	}
}

func TestDetailsSelectionCopiesWrappedPath(t *testing.T) {
	m := New(Options{Details: true}, "")
	m.height, m.width = 30, 120
	long := "/very/long/folder/" + strings.Repeat("segment/", 10) + "end"
	m.sessions = []agents.Session{{Agent: "claude", ID: "abc-123", Name: "n", Cwd: long, Updated: time.Now()}}
	m.rebuild()
	m.cur = 1
	lw, dw := m.split()
	det := m.detailsPane(dw)
	// Find the Folder line and how many lines its wrapped path takes.
	first, last := -1, -1
	for i, l := range det {
		p := ansi.Strip(l)
		if strings.HasPrefix(strings.TrimSpace(p), "Folder") {
			first = i
		} else if first >= 0 && last < 0 && !strings.HasPrefix(p, strings.Repeat(" ", 11)) {
			last = i - 1
		}
	}
	if first < 0 || last <= first {
		t.Fatalf("expected a wrapped Folder line: first=%d last=%d\n%s", first, last, strings.Join(det, "\n"))
	}
	press := func(a tea.MouseAction, x, y int) {
		nm, _ := m.Update(tea.MouseMsg{X: lw + 2 + x, Y: y + 1, Action: a, Button: tea.MouseButtonLeft})
		m = nm.(Model)
	}
	press(tea.MouseActionPress, 11, first) // start of the value, after the label
	press(tea.MouseActionMotion, dw-1, last)
	if got := m.selectedText(); got != long {
		t.Fatalf("selected %q, want %q", got, long)
	}
	lipgloss.SetColorProfile(termenv.ANSI) // tests have no terminal; force styles on to see the highlight
	defer lipgloss.SetColorProfile(termenv.Ascii)
	if !strings.Contains(m.View(), "\x1b[7m") {
		t.Fatal("selection not highlighted")
	}
	// A plain click selects nothing.
	press(tea.MouseActionRelease, dw-1, last)
	press(tea.MouseActionPress, 3, first)
	press(tea.MouseActionRelease, 3, first)
	if m.sel != nil {
		t.Fatal("plain click left a selection")
	}
}

func TestSelectionKeepsPathSpacesAndKeyCancelsDrag(t *testing.T) {
	m := New(Options{Details: true}, "")
	m.height, m.width = 30, 120
	_, dw := m.split()
	// A path with spaces exactly where it will wrap.
	room := dw - 12
	long := "/x/" + strings.Repeat("a", room-4) + " b" + strings.Repeat(" c", room) + "/end"
	m.sessions = []agents.Session{{Agent: "claude", ID: "1", Name: "n", Cwd: long, Updated: time.Now()}}
	m.rebuild()
	m.cur = 1
	det := m.detailsPane(dw)
	first, last := -1, -1
	for i, l := range det {
		p := ansi.Strip(l)
		if strings.HasPrefix(strings.TrimSpace(p), "Folder") {
			first = i
		} else if first >= 0 && last < 0 && !strings.HasPrefix(p, strings.Repeat(" ", 11)) {
			last = i - 1
		}
	}
	m.sel = &selection{11, first, dw - 1, last}
	if got := m.selectedText(); got != long {
		t.Fatalf("spaces lost:\n got %q\nwant %q", got, long)
	}
	// A key during a drag ends it; the next motion must not crash.
	m.selecting = true
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = nm.(Model)
	nm, _ = m.Update(tea.MouseMsg{X: 100, Y: 5, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft})
	nm, _ = nm.(Model).Update(tea.MouseMsg{X: 100, Y: 5, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
}

func TestDetailsNeverTruncateAndSelectionCopiesFullID(t *testing.T) {
	m := New(Options{Details: true}, "")
	m.height, m.width = 30, 100 // details pane ~40 wide: a UUID line doesn't fit
	id := "00000000-0000-4000-8000-000000000001"
	m.sessions = []agents.Session{{Agent: "codex", ID: id, Name: "n", Cwd: "/a", Updated: time.Now()}}
	m.rebuild()
	m.cur = 1
	_, dw := m.split()
	det := m.detailsPane(dw)
	row := -1
	for i, l := range det {
		if ansi.StringWidth(l) > dw {
			t.Fatalf("line %d wider than the pane: %q", i, ansi.Strip(l))
		}
		if strings.Contains(l, "…") && !strings.Contains(l, "loading…") {
			t.Fatalf("line %d truncated: %q", i, ansi.Strip(l))
		}
		if strings.HasPrefix(strings.TrimSpace(ansi.Strip(l)), "Session") {
			row = i
		}
	}
	m.sel = &selection{11, row, dw - 1, row + 1}
	if got := strings.ReplaceAll(m.selectedText(), "\n", ""); !strings.HasPrefix(got, id) {
		t.Fatalf("copied %q, want the full id %q", got, id)
	}
	// Details arriving mid-drag cancel the selection.
	m.selecting = true
	nm, _ := m.Update(detailsMsg{key: "codex:" + id})
	if nm.(Model).sel != nil || nm.(Model).selecting {
		t.Fatal("selection survived a details update")
	}
}

func TestLongNameCappedAndResizeClearsSelection(t *testing.T) {
	m := New(Options{Details: true}, "")
	m.height, m.width = 30, 120
	m.sessions = []agents.Session{{Agent: "claude", ID: "id-1", Name: strings.Repeat("word ", 300), Cwd: "/a", Updated: time.Now()}}
	m.rebuild()
	m.cur = 1
	m.details["claude:id-1"] = detailEntry{loaded: time.Now(), updated: m.sessions[0].Updated}
	_, dw := m.split()
	v := ansi.Strip(strings.Join(m.detailsPane(dw), "\n"))
	for _, want := range []string{"Session", "Status", "Cost", "Folder"} {
		if !strings.Contains(v, want) {
			t.Fatalf("%s pushed out by a long name:\n%s", want, v)
		}
	}
	m.sel = &selection{11, 2, 20, 2}
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if nm.(Model).sel != nil {
		t.Fatal("selection survived a resize")
	}
	if strings.Contains(ansi.Strip(m.View()), "d  x") {
		t.Fatal("x still listed")
	}
}

func TestSelectionGuards(t *testing.T) {
	m := New(Options{Details: true}, "")
	m.height, m.width = 30, 120
	m.sessions = []agents.Session{{Agent: "claude", ID: "1", Name: "short", Cwd: "/a", Updated: time.Now()}}
	m.rebuild()
	m.cur = 1
	lw, _ := m.split()
	press := tea.MouseMsg{X: lw + 15, Y: 3, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}
	// A popup is modal: no selection underneath it.
	s := m.sessions[0]
	m.editing = &s
	nm, _ := m.Update(press)
	if nm.(Model).sel != nil {
		t.Fatal("selection started behind the rename popup")
	}
	m.editing = nil
	nm, _ = m.Update(press)
	m = nm.(Model)
	if m.sel == nil {
		t.Fatal("no selection")
	}
	// A refresh that changes what the pane shows cancels it.
	renamed := []agents.Session{{Agent: "claude", ID: "1", Name: strings.Repeat("long name ", 20), Cwd: "/a", Updated: s.Updated}}
	nm, _ = m.Update(loadedMsg{sessions: renamed})
	if nm.(Model).sel != nil {
		t.Fatal("selection survived a refresh that reflowed the pane")
	}
	// One that changes nothing keeps it.
	nm, _ = m.Update(loadedMsg{sessions: m.sessions})
	if nm.(Model).sel == nil {
		t.Fatal("an unchanged refresh dropped the selection")
	}
}

func TestBottomBarTrimmed(t *testing.T) {
	m := New(Options{Mouse: true}, "")
	m.height, m.width = 20, 300
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	last := lines[len(lines)-1]
	for _, gone := range []string{"resize", "fold", "double-click"} {
		if strings.Contains(last, gone) {
			t.Errorf("bottom bar still has %q: %q", gone, last)
		}
	}
	if !strings.Contains(last, "? keys") {
		t.Errorf("bottom bar lost ? keys: %q", last)
	}
}

func TestResizeReadout(t *testing.T) {
	m := New(Options{Details: true}, "")
	m.height, m.width = 30, 200
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("<")})
	m = nm.(Model)
	_, dw := m.split()
	want := fmt.Sprintf("details %d%% · %d columns", dw*100/200, dw)
	if !strings.Contains(ansi.Strip(m.View()), want) || cmd == nil {
		t.Fatalf("no readout %q after <", want)
	}
	// A stale timer (from an earlier resize) doesn't hide a newer readout.
	nm, _ = m.Update(resizeShownMsg{at: m.resizedAt.Add(-time.Second)})
	if nm.(Model).resizedAt.IsZero() {
		t.Fatal("stale timer cleared the readout")
	}
	nm, _ = m.Update(resizeShownMsg{at: m.resizedAt})
	if !nm.(Model).resizedAt.IsZero() || strings.Contains(ansi.Strip(nm.(Model).View()), "columns") {
		t.Fatal("readout didn't go away")
	}
}

// A rename that could not reach the open Codex still succeeded in the database:
// the bar says so, with the reason.
func TestRenamedNoteShownInStatus(t *testing.T) {
	m := New(Options{}, "")
	nm, _ := m.Update(renamedMsg{key: "codex:x", name: "New", note: "the open Codex keeps the old name: it is busy"})
	st := nm.(Model).status
	if !strings.Contains(st, "renamed to New") || !strings.Contains(st, "it is busy") {
		t.Fatalf("status %q", st)
	}
	nm, _ = m.Update(renamedMsg{key: "codex:x", name: "New"})
	if st := nm.(Model).status; st != "renamed to New" {
		t.Fatalf("status %q", st)
	}
}

func press(m tea.Model, k string) (Model, tea.Cmd) {
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
	return nm.(Model), cmd
}

func pressType(m tea.Model, ty tea.KeyType) (Model, tea.Cmd) {
	nm, cmd := m.Update(tea.KeyMsg{Type: ty})
	return nm.(Model), cmd
}

func TestRenameUnnamedStartsEmpty(t *testing.T) {
	m := New(Options{}, "")
	m.height = 20
	m.sessions = []agents.Session{{Agent: "claude", ID: "1", Name: agents.Unnamed, Cwd: "/a", Updated: time.Now()}}
	m.rebuild()
	m.cur = 1
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	m = nm.(Model)
	if m.editing == nil || m.editBuf != "" {
		t.Fatalf("editing %v, buf %q", m.editing != nil, m.editBuf)
	}
}

// peekFixture is a model with one Claude session whose details hold an exchange.
func peekFixture(opts Options, pk agents.Peek, status agents.Status) Model {
	m := New(opts, "")
	m.height, m.width = 36, 120
	s := agents.Session{Agent: "claude", ID: "id-1", Name: "n", Cwd: "/a", Updated: time.Now(), Status: status}
	m.sessions = []agents.Session{s}
	m.rebuild()
	m.cur = 1
	m.details[s.Key()] = detailEntry{loaded: time.Now(), updated: s.Updated, d: agents.Details{Location: "/a/t.jsonl", Peek: pk}}
	return m
}

func paneText(m Model) string {
	_, dw := m.split()
	return ansi.Strip(strings.Join(m.detailsPane(dw), "\n"))
}

func TestPeekPane(t *testing.T) {
	pk := agents.Peek{Prompt: "why is the build\nfailing on main?", Reply: "A flaky test. " + strings.Repeat("Details follow. ", 30), Done: true}
	m := peekFixture(Options{Details: true, Peek: true}, pk, agents.Idle)
	got := paneText(m)
	for _, want := range []string{"Latest exchange", "You", "why is the build failing", "main?", "Agent", "A flaky test."} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "…") {
		t.Errorf("a long reply should be cut with an ellipsis:\n%s", got)
	}
	_, dw := m.split()
	for i, l := range m.detailsPane(dw) {
		if ansi.StringWidth(l) > dw {
			t.Errorf("line %d is wider than the pane: %q", i, ansi.Strip(l))
		}
	}

	// Off: nothing about the conversation is drawn.
	if got := paneText(peekFixture(Options{Details: true, Peek: false}, pk, agents.Idle)); strings.Contains(got, "Latest exchange") || strings.Contains(got, "flaky") {
		t.Errorf("peek shown while off:\n%s", got)
	}
	// No room: the metrics win, and no half block is drawn.
	short := peekFixture(Options{Details: true, Peek: true}, pk, agents.Idle)
	short.height = 16
	if got := paneText(short); strings.Contains(got, "Latest exchange") && !strings.Contains(got, "Agent  ") {
		t.Errorf("half a block drawn:\n%s", got)
	}
	// States: working, stopped early.
	if got := paneText(peekFixture(Options{Details: true, Peek: true}, agents.Peek{Prompt: "q"}, agents.Busy)); !strings.Contains(got, "working…") {
		t.Errorf("busy without reply should say working:\n%s", got)
	}
	stopped := peekFixture(Options{Details: true, Peek: true}, agents.Peek{Prompt: "q", Reply: "partial"}, agents.Idle)
	if got := paneText(stopped); !strings.Contains(got, "stopped early") {
		t.Errorf("an unfinished reply on an idle session should say it stopped early:\n%s", got)
	}
	// ... and the heading stays inside a narrow pane.
	stopped.width = 90
	_, ndw := stopped.split()
	for i, l := range stopped.detailsPane(ndw) {
		if ansi.StringWidth(l) > ndw {
			t.Errorf("line %d is wider than the %d-cell pane: %q", i, ndw, ansi.Strip(l))
		}
	}
}

func TestPeekPopup(t *testing.T) {
	reply := strings.Repeat("A long paragraph of the reply that wraps over several lines. ", 150)
	m := peekFixture(Options{Details: true, Peek: true}, agents.Peek{Prompt: "the question", Reply: reply, Done: true}, agents.Idle)
	m, _ = press(m, "p")
	if m.peek == nil {
		t.Fatal("p did not open the conversation")
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "Conversation") || !strings.Contains(view, "the question") || !strings.Contains(view, "↑↓ scroll · drag to copy · esc close") {
		t.Fatalf("popup:\n%s", view)
	}
	if !strings.Contains(view, " of ") {
		t.Errorf("a long reply should show a position line:\n%s", view)
	}
	// Only the arrow keys scroll; nothing else does.
	for _, k := range []string{"j", "k", "G", "g", " "} {
		if m, _ = press(m, k); m.peek == nil || m.peek.top != 0 {
			t.Fatalf("%q scrolled the popup", k)
		}
	}
	for _, ty := range []tea.KeyType{tea.KeyPgDown, tea.KeyEnd, tea.KeyCtrlD} {
		if m, _ = pressType(m, ty); m.peek == nil || m.peek.top != 0 {
			t.Fatalf("key %v scrolled the popup", ty)
		}
	}
	m, _ = pressType(m, tea.KeyDown)
	if m.peek.top != 1 {
		t.Errorf("down did not scroll: top=%d", m.peek.top)
	}
	m, _ = pressType(m, tea.KeyUp)
	if m.peek.top != 0 {
		t.Errorf("up did not scroll back: top=%d", m.peek.top)
	}
	m, _ = pressType(m, tea.KeyUp)
	if m.peek.top != 0 {
		t.Errorf("up scrolled past the top: top=%d", m.peek.top)
	}
	for i := 0; i < 500; i++ {
		m, _ = pressType(m, tea.KeyDown)
	}
	if m.peek.top == 0 || m.peek.top >= 500 {
		t.Errorf("down should stop at the end: top=%d", m.peek.top)
	}
	// The popup is modal: a mouse click does nothing.
	nm, _ := m.Update(tea.MouseMsg{X: 5, Y: 5, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if nm.(Model).peek == nil {
		t.Error("a click closed the popup")
	}
	m, _ = press(m, "esc")
	if m.peek != nil {
		t.Error("esc did not close it")
	}
	// Off: p says so and opens nothing.
	m = peekFixture(Options{Details: true, Peek: false}, agents.Peek{Prompt: "q"}, agents.Idle)
	m, _ = press(m, "p")
	if m.peek != nil || !strings.Contains(m.status, "off") {
		t.Errorf("p while off: peek=%v status=%q", m.peek != nil, m.status)
	}
	// Nothing to show yet.
	m = peekFixture(Options{Details: true, Peek: true}, agents.Peek{}, agents.Idle)
	m, _ = press(m, "p")
	if m.peek == nil || !strings.Contains(ansi.Strip(m.View()), "no conversation to show yet") {
		t.Errorf("empty peek:\n%s", ansi.Strip(m.View()))
	}
}

// Turning the setting off drops the text that was read.
func TestPeekSettingOffDropsText(t *testing.T) {
	m := peekFixture(Options{Details: true, Peek: true}, agents.Peek{Prompt: "q", Reply: "a", Done: true}, agents.Idle)
	m.applyOptions(Options{Details: true, Peek: false})
	if len(m.details) != 0 {
		t.Fatalf("details kept: %+v", m.details)
	}
	// A load that was already running when it was turned off must not bring it back.
	nm, _ := m.Update(detailsMsg{key: "claude:id-1", updated: time.Now(), peek: true, d: agents.Details{Peek: agents.Peek{Prompt: "late"}}})
	if len(nm.(Model).details) != 0 {
		t.Fatalf("a load started with the peek on was kept: %+v", nm.(Model).details)
	}
	// ... and the other way: a load started while it was off must not leave it blank once on.
	m.applyOptions(Options{Details: true, Peek: true})
	nm, _ = m.Update(detailsMsg{key: "claude:id-1", updated: time.Now(), peek: false})
	if len(nm.(Model).details) != 0 {
		t.Fatalf("a load started with the peek off was kept: %+v", nm.(Model).details)
	}
}

func TestSettingsHasPeekToggle(t *testing.T) {
	m := New(Options{Peek: true}, "")
	m.height, m.width = 30, 100
	m, _ = press(m, "s")
	if !strings.Contains(ansi.Strip(m.View()), "Conversation peek") {
		t.Fatalf("settings:\n%s", ansi.Strip(m.View()))
	}
}

// With the details pane hidden, the open popup's session still reloads.
func TestPeekPopupKeepsLoadingWithoutPane(t *testing.T) {
	m := peekFixture(Options{Details: false, Peek: true}, agents.Peek{Prompt: "q"}, agents.Idle)
	m.showDetails = false
	if m.wantDetails() != nil {
		t.Fatal("loaded details with the pane hidden and no popup")
	}
	delete(m.details, "claude:id-1") // force a load
	m.peek = &peekView{key: "claude:id-1"}
	if m.wantDetails() == nil {
		t.Fatal("the open popup's session was not loaded")
	}
}

func TestWrapLineKeepsEverything(t *testing.T) {
	for _, in := range []string{
		"", "short", "two words", "trailing  spaces   ", "  leading spaces",
		strings.Repeat("a long sentence that wraps over lines ", 12),
		strings.Repeat("x", 100), "ab " + strings.Repeat("y", 60) + " cd",
		strings.Repeat("漢字 ", 40),
	} {
		for _, w := range []int{8, 20, 33} {
			pieces := wrapLine(in, w)
			if strings.Join(pieces, "") != in {
				t.Errorf("width %d: pieces do not join back to %q: %q", w, in, pieces)
			}
			for _, p := range pieces {
				if got := ansi.StringWidth(strings.TrimRight(p, " ")); got > w {
					t.Errorf("width %d: piece is %d cells: %q", w, got, p)
				}
			}
		}
	}
}

// peekGeom is where the popup's text starts on screen.
func peekGeom(m Model) (x0, y0 int) {
	px, py := popupOrigin(m.width, m.boxHeight(), m.peekPopup())
	return px + 2, py + 1
}

func mouseAt(m Model, act tea.MouseAction, btn tea.MouseButton, x, y int) (Model, tea.Cmd) {
	nm, cmd := m.Update(tea.MouseMsg{X: x, Y: y, Action: act, Button: btn})
	return nm.(Model), cmd
}

func TestPeekDragSelectsAndCopiesParagraph(t *testing.T) {
	reply := "First paragraph " + strings.Repeat("word ", 60) + "end one.\nSecond line."
	m := peekFixture(Options{Details: true, Peek: true}, agents.Peek{Prompt: "the question", Reply: reply, Done: true}, agents.Idle)
	m, _ = press(m, "p")
	lines := m.peekLines(m.wideWidth() - 4)
	var first, last int // body lines of the reply: from "Agent" + 1 to the end
	for i, l := range lines {
		if strings.HasPrefix(l.raw, "First paragraph") {
			first = i
		}
		last = i
	}
	if first == 0 || last <= first+1 {
		t.Fatalf("expected a wrapped reply, got %d lines from %d", len(lines), first)
	}
	x0, y0 := peekGeom(m)
	// Press at the start of the paragraph, drag to the end of the last line, release.
	m, _ = mouseAt(m, tea.MouseActionPress, tea.MouseButtonLeft, x0+2, y0+first)
	m, _ = mouseAt(m, tea.MouseActionMotion, tea.MouseButtonLeft, x0+60, y0+last)
	m, cmd := mouseAt(m, tea.MouseActionRelease, tea.MouseButtonLeft, x0+60, y0+last)
	if cmd == nil || m.peek.sel == nil {
		t.Fatal("releasing a drag did not start a copy")
	}
	sel, ok := m.peekSelection()
	if !ok {
		t.Fatal("the selection was dropped")
	}
	got := peekSelectedText(lines, sel)
	if !strings.HasPrefix(got, "First paragraph word word") || !strings.Contains(got, "end one.\nSecond line.") {
		t.Fatalf("copied text:\n%q", got)
	}
	if strings.Contains(got, "\n") && strings.Count(got, "\n") != 1 {
		t.Errorf("the wrapped paragraph was split by newlines: %q", got)
	}
	// A plain click selects nothing; a key other than the arrows clears a selection.
	m, _ = mouseAt(m, tea.MouseActionPress, tea.MouseButtonLeft, x0+2, y0+first)
	m, cmd = mouseAt(m, tea.MouseActionRelease, tea.MouseButtonLeft, x0+2, y0+first)
	if cmd != nil || m.peek.sel != nil {
		t.Error("a click selected something")
	}
	m, _ = mouseAt(m, tea.MouseActionPress, tea.MouseButtonLeft, x0+2, y0+first)
	m, _ = mouseAt(m, tea.MouseActionMotion, tea.MouseButtonLeft, x0+9, y0+first)
	m, _ = mouseAt(m, tea.MouseActionRelease, tea.MouseButtonLeft, x0+9, y0+first)
	if m.peek.sel == nil {
		t.Fatal("no selection")
	}
	m, _ = pressType(m, tea.KeyDown)
	if m.peek.sel == nil {
		t.Error("an arrow key cleared the selection")
	}
	m, _ = press(m, "x")
	if m.peek.sel != nil {
		t.Error("another key kept the selection")
	}
	// Clicking outside the text clears a selection and does not close the popup.
	m, _ = mouseAt(m, tea.MouseActionPress, tea.MouseButtonLeft, x0+2, y0+first)
	m, _ = mouseAt(m, tea.MouseActionMotion, tea.MouseButtonLeft, x0+9, y0+first)
	m, _ = mouseAt(m, tea.MouseActionRelease, tea.MouseButtonLeft, x0+9, y0+first)
	m, _ = mouseAt(m, tea.MouseActionPress, tea.MouseButtonLeft, 0, 0)
	if m.peek == nil || m.peek.sel != nil {
		t.Errorf("outside click: popup=%v sel=%v", m.peek != nil, m.peek != nil && m.peek.sel != nil)
	}
}

func TestPeekWheelScrollsAndSelectionSurvivesReload(t *testing.T) {
	reply := strings.Repeat("A long paragraph of the reply that wraps over several lines. ", 150)
	m := peekFixture(Options{Details: true, Peek: true}, agents.Peek{Prompt: "q", Reply: reply, Done: true}, agents.Idle)
	m, _ = press(m, "p")
	m, _ = mouseAt(m, tea.MouseActionPress, tea.MouseButtonWheelDown, 10, 10)
	if m.peek.top != 3 {
		t.Fatalf("wheel down: top=%d", m.peek.top)
	}
	m, _ = mouseAt(m, tea.MouseActionPress, tea.MouseButtonWheelUp, 10, 10)
	m, _ = mouseAt(m, tea.MouseActionPress, tea.MouseButtonWheelUp, 10, 10)
	if m.peek.top != 0 {
		t.Fatalf("wheel up stops at the top: top=%d", m.peek.top)
	}
	x0, y0 := peekGeom(m)
	m, _ = mouseAt(m, tea.MouseActionPress, tea.MouseButtonLeft, x0+2, y0+3)
	m, _ = mouseAt(m, tea.MouseActionMotion, tea.MouseButtonLeft, x0+20, y0+4)
	m, _ = mouseAt(m, tea.MouseActionRelease, tea.MouseButtonLeft, x0+20, y0+4)
	if _, ok := m.peekSelection(); !ok {
		t.Fatal("no selection")
	}
	// The conversation changes under the selection (a reload): the selection is dropped.
	k := "claude:id-1"
	e := m.details[k]
	e.d.Peek.Reply += " and a new sentence"
	m.details[k] = e
	if _, ok := m.peekSelection(); ok {
		t.Error("a selection made on other text survived a reload")
	}
	// Dragging past the bottom edge scrolls.
	m, _ = mouseAt(m, tea.MouseActionPress, tea.MouseButtonLeft, x0+2, y0+3)
	rows := min(len(m.peekLines(m.wideWidth()-4)), m.peekRoom())
	m, _ = mouseAt(m, tea.MouseActionMotion, tea.MouseButtonLeft, x0+10, y0+rows+2)
	if m.peek.top == 0 {
		t.Error("dragging below the text did not scroll")
	}
}

// A quick drag can release before any motion event arrives: the release position counts.
// A reload during the drag cancels it instead of copying other text.
func TestPeekDragReleasePositionAndReload(t *testing.T) {
	reply := strings.Repeat("A long paragraph of the reply that wraps over several lines. ", 20)
	m := peekFixture(Options{Details: true, Peek: true}, agents.Peek{Prompt: "q", Reply: reply, Done: true}, agents.Idle)
	m, _ = press(m, "p")
	x0, y0 := peekGeom(m)
	m, _ = mouseAt(m, tea.MouseActionPress, tea.MouseButtonLeft, x0+2, y0+4)
	m, cmd := mouseAt(m, tea.MouseActionRelease, tea.MouseButtonLeft, x0+30, y0+5) // no motion in between
	if cmd == nil || m.peek.sel == nil || m.peek.sel.y2 != m.peek.top+5 || m.peek.sel.x2 != 30 {
		t.Fatalf("release position ignored: cmd=%v sel=%+v", cmd != nil, m.peek.sel)
	}
	m, _ = mouseAt(m, tea.MouseActionPress, tea.MouseButtonLeft, x0+2, y0+4)
	e := m.details["claude:id-1"]
	e.d.Peek.Reply += " changed"
	m.details["claude:id-1"] = e
	m, cmd = mouseAt(m, tea.MouseActionRelease, tea.MouseButtonLeft, x0+30, y0+5)
	if cmd != nil || m.peek.sel != nil || m.peek.selecting {
		t.Errorf("a drag over a reload still copied: cmd=%v sel=%v", cmd != nil, m.peek.sel)
	}
}

// In a narrow popup the copy result wins over the scroll position, so a failure keeps its reason.
func TestPeekCopyResultWinsInNarrowPopup(t *testing.T) {
	reply := strings.Repeat("A long paragraph of the reply that wraps over several lines. ", 150)
	m := peekFixture(Options{Details: true, Peek: true}, agents.Peek{Prompt: "q", Reply: reply, Done: true}, agents.Idle)
	m.width = 50
	m, _ = press(m, "p")
	nm, _ := m.Update(copiedMsg{err: "no clipboard"})
	m = nm.(Model)
	if v := ansi.Strip(m.View()); !strings.Contains(v, "copy failed: no clipboard") {
		t.Errorf("the reason was cut:\n%s", v)
	}
}

// The popup keeps its place when a copy result appears or goes, or the next drag would start a row off.
func TestPeekPopupDoesNotMoveWithCopyResult(t *testing.T) {
	m := peekFixture(Options{Details: true, Peek: true}, agents.Peek{Prompt: "q", Reply: "short", Done: true}, agents.Idle)
	m, _ = press(m, "p")
	x0, y0 := peekGeom(m)
	nm, _ := m.Update(copiedMsg{n: 5, via: "test"})
	m = nm.(Model)
	if x1, y1 := peekGeom(m); x1 != x0 || y1 != y0 {
		t.Fatalf("popup moved from (%d,%d) to (%d,%d) when the copy result appeared", x0, y0, x1, y1)
	}
	if !strings.Contains(ansi.Strip(m.View()), "copied 5 characters") {
		t.Error("the copy result is not shown")
	}
}

// fakeAgent records offline renames; live says whether it renames while running.
type fakeAgent struct {
	live    bool
	renamed *string
}

func (fakeAgent) Name() string                                         { return "fake" }
func (fakeAgent) Title() string                                        { return "Fake" }
func (fakeAgent) List() ([]agents.Session, error)                      { return nil, nil }
func (fakeAgent) WatchPaths() []string                                 { return nil }
func (fakeAgent) Details(agents.Session, bool) (agents.Details, error) { return agents.Details{}, nil }
func (f fakeAgent) RenameRunning() bool                                { return f.live }
func (fakeAgent) RenameLine(n string) string                           { return "/name " + n }
func (f fakeAgent) Rename(_ agents.Session, n string) error            { *f.renamed = n; return nil }

// A running session muxlair can't type into: an agent that renames while running
// is renamed anyway with a note; any other is left alone with an error.
func TestRenameRunningUnreachable(t *testing.T) {
	s := agents.Session{Agent: "fake", ID: "1", PID: 1}
	var got string
	note, err := renameRunning(fakeAgent{live: true, renamed: &got}, s, "new")
	if err != nil || got != "new" || !strings.Contains(note, "the open Fake keeps the old name until you run /name there") {
		t.Fatalf("live: note %q err %v renamed %q", note, err, got)
	}
	got = ""
	note, err = renameRunning(fakeAgent{renamed: &got}, s, "new")
	if err == nil || err.Error() != "can't reach its terminal; run /name new there" || got != "" || note != "" {
		t.Fatalf("not live: note %q err %v renamed %q", note, err, got)
	}
	s.TTY, s.Status = "ttys999", agents.Busy // not a terminal muxlair knows: still unreachable
	if _, err = renameRunning(fakeAgent{renamed: &got}, s, "new"); err == nil || got != "" {
		t.Fatalf("unknown tty: err %v renamed %q", err, got)
	}
}

func TestRelevantWatchEvents(t *testing.T) {
	for path, want := range map[string]bool{
		"/h/.local/share/opencode/opencode.db":     true,
		"/h/.local/share/opencode/opencode.db-wal": true,
		"/h/.codex/state_5.sqlite-wal":             true,
		"/h/.claude/sessions/12.json":              true,
		"/h/.local/share/opencode/notes.txt":       false,
	} {
		if got := relevant(path); got != want {
			t.Errorf("%s: got %v", path, got)
		}
	}
}
