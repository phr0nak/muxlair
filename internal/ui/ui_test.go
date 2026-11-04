package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

func TestAutoDeleteSelection(t *testing.T) {
	m := New(Options{AutoDelete: 10 * time.Minute}, "")
	old := time.Now().Add(-time.Hour)
	m.sessions = []agents.Session{
		{Agent: "codex", ID: "headless-old", Headless: true, Updated: old},
		{Agent: "claude", ID: "headless-running", Headless: true, PID: 1, Updated: old},
		{Agent: "codex", ID: "headless-maybe", Headless: true, MaybeLive: true, Updated: old},
		{Agent: "claude", ID: "headless-recent", Headless: true, Updated: time.Now()},
		{Agent: "claude", ID: "interactive-old", Updated: old},
	}
	if m.autoDelete() == nil || !m.autoBusy {
		t.Fatal("no batch for an old exited headless session")
	}
	m.rebuild()
	for i, r := range m.rows {
		if r.s != nil && r.s.ID == "headless-old" {
			m.cur = i
		}
	}
	if m.open(false) != nil {
		t.Fatal("opened a session that is being auto-deleted")
	}
	if m.autoDelete() != nil {
		t.Fatal("started a second batch while one runs")
	}
	m.autoBusy = false
	m.autoDel["codex:headless-old"] = true // failed before
	if m.autoDelete() != nil {
		t.Fatal("retried a failed session, or picked an ineligible one")
	}
	off := New(Options{}, "")
	if off.autoDelete() != nil {
		t.Fatal("ran with auto-delete off")
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
	key("right") // auto-delete 10m -> 30m
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
	if m.settings != nil || m.opts.Mouse || m.opts.AutoDelete != 30*time.Minute {
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

func TestAutoDeleteChoicesKeepCustom(t *testing.T) {
	c := autoDeleteChoices(90 * time.Second)
	found := false
	for i, d := range c {
		if d == 90*time.Second {
			found = true
		}
		if i > 0 && c[i-1] >= d {
			t.Fatal("not sorted")
		}
	}
	if !found {
		t.Fatal("custom value dropped")
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

func TestAutoDeleteMessagesUseIDs(t *testing.T) {
	m := New(Options{}, "")
	nm, _ := m.Update(autoDeletedMsg{deleted: []string{"codex 01a18e76-b797"}})
	if s := nm.(Model).status; s != "auto-deleted codex 01a18e76-b797 (in ~/.muxlair/trash)" {
		t.Fatalf("one: %q", s)
	}
	nm, _ = m.Update(autoDeletedMsg{failed: []string{"codex:x"}, err: "codex 01a18e76-b797: boom"})
	if s := nm.(Model).status; s != "auto-delete codex 01a18e76-b797: boom" {
		t.Fatalf("failed: %q", s)
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
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if m = nm.(Model); m.help || m.confirm != nil {
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

func TestXDoesNotDelete(t *testing.T) {
	m := New(Options{}, "")
	m.sessions = []agents.Session{{Agent: "claude", ID: "1", Cwd: "/a", Updated: time.Now()}}
	m.rebuild()
	m.cur = 1
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if nm.(Model).confirm != nil {
		t.Fatal("x opened the delete prompt")
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
	m.confirm = &s
	nm, _ := m.Update(press)
	if nm.(Model).sel != nil {
		t.Fatal("selection started behind the delete popup")
	}
	m.confirm = nil
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

// putTrash writes a trash entry the way agents.Trash does; files maps a name in
// the entry to the original path it came from.
func putTrash(t *testing.T, agent, id, name string, at time.Time, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(agents.TrashDir(), agent, at.Format("20060102-150405")+"-"+id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for f := range files {
		os.WriteFile(filepath.Join(dir, f), []byte("x\n"), 0o600)
	}
	b, _ := json.Marshal(map[string]any{"agent": agent, "id": id, "name": name, "cwd": "/p", "deletedAt": at, "files": files})
	os.WriteFile(filepath.Join(dir, "meta.json"), b, 0o600)
	return dir
}

func press(m tea.Model, k string) (Model, tea.Cmd) {
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
	return nm.(Model), cmd
}

func pressType(m tea.Model, ty tea.KeyType) (Model, tea.Cmd) {
	nm, cmd := m.Update(tea.KeyMsg{Type: ty})
	return nm.(Model), cmd
}

// "t" opens the trash, enter restores the selected entry, and an entry with
// nothing to put back says so instead of trying.
func TestTrashPopupRestoresAndExplains(t *testing.T) {
	agents.Home = t.TempDir()
	orig := filepath.Join(agents.Home, ".claude", "projects", "-p", "aaa.jsonl")
	now := time.Now()
	putTrash(t, "claude", "aaa", "Fix the bug", now, map[string]string{"0-aaa.jsonl": orig})
	putTrash(t, "codex", "bbb", "Dead one", now.Add(-time.Hour), nil) // no session file

	m := New(Options{}, "")
	m.height, m.width = 30, 100
	m, _ = press(m, "t")
	if m.trash == nil || len(m.trash.rows) != 2 {
		t.Fatalf("popup not open with 2 entries: %+v", m.trash)
	}
	v := m.View()
	for _, want := range []string{"Trash", "RESTORABLE", "RESTORED", "Fix the bug", "Dead one", "enter restore"} {
		if !strings.Contains(ansi.Strip(v), want) {
			t.Fatalf("view lacks %q:\n%s", want, ansi.Strip(v))
		}
	}

	// The dead codex entry (second line) can't be restored.
	m, _ = press(m, "j")
	m, cmd := pressType(m, tea.KeyEnter)
	if cmd != nil || !m.trash.isErr || !strings.Contains(m.trash.msg, "can't be restored") {
		t.Fatalf("dead entry: cmd=%v msg=%q", cmd != nil, m.trash.msg)
	}

	// The Claude entry (first line) is restored for real.
	m, _ = press(m, "k")
	m, cmd = pressType(m, tea.KeyEnter)
	if cmd == nil || !m.restoring {
		t.Fatal("restore did not start")
	}
	if _, again := pressType(m, tea.KeyEnter); again != nil {
		t.Fatal("a second restore started while one was running")
	}
	nm, _ := m.Update(cmd())
	m = nm.(Model)
	if _, err := os.Stat(orig); err != nil {
		t.Fatalf("transcript not back: %v", err)
	}
	if m.trash == nil || len(m.trash.rows) != 1 || m.trash.isErr || !strings.Contains(m.trash.msg, "restored Fix the bug") {
		t.Fatalf("after restore: %+v", m.trash)
	}

	m, _ = pressType(m, tea.KeyEsc)
	if m.trash != nil {
		t.Fatal("esc did not close the popup")
	}
}

// Restoring the last entry closes the popup and says so in the bottom bar.
func TestTrashPopupClosesWhenEmpty(t *testing.T) {
	agents.Home = t.TempDir()
	orig := filepath.Join(agents.Home, ".claude", "projects", "-p", "aaa.jsonl")
	putTrash(t, "claude", "aaa", "Only one", time.Now(), map[string]string{"0-aaa.jsonl": orig})
	m := New(Options{}, "")
	m.height, m.width = 30, 100
	m, _ = press(m, "t")
	m, cmd := pressType(m, tea.KeyEnter)
	nm, _ := m.Update(cmd())
	m = nm.(Model)
	if m.trash != nil || !strings.Contains(m.status, "restored Only one") {
		t.Fatalf("trash=%v status=%q", m.trash, m.status)
	}
}

func TestTrashEmptyAndFailure(t *testing.T) {
	agents.Home = t.TempDir()
	m := New(Options{}, "")
	m.height, m.width = 30, 100
	m, _ = press(m, "t")
	if m.trash != nil || m.status != "the trash is empty" {
		t.Fatalf("empty trash: trash=%v status=%q", m.trash, m.status)
	}

	// A restore that fails (the destination already exists) keeps the popup and shows why.
	orig := filepath.Join(agents.Home, ".claude", "projects", "-p", "aaa.jsonl")
	putTrash(t, "claude", "aaa", "Taken", time.Now(), map[string]string{"0-aaa.jsonl": orig})
	os.MkdirAll(filepath.Dir(orig), 0o700)
	os.WriteFile(orig, []byte("new"), 0o600)
	m, _ = press(m, "t")
	m, cmd := pressType(m, tea.KeyEnter)
	nm, _ := m.Update(cmd())
	m = nm.(Model)
	if m.trash == nil || !m.trash.isErr || !strings.Contains(m.trash.msg, "already exists") {
		t.Fatalf("failed restore: %+v", m.trash)
	}
	if b, _ := os.ReadFile(orig); string(b) != "new" {
		t.Fatal("overwrote the existing file")
	}
}

// A restore outlives the popup: reopening it must not allow a second restore
// to start, and the first one's result still lands.
func TestTrashRestoreInFlightSurvivesReopen(t *testing.T) {
	agents.Home = t.TempDir()
	proj := filepath.Join(agents.Home, ".claude", "projects", "-p")
	putTrash(t, "claude", "aaa", "First", time.Now(), map[string]string{"0-aaa.jsonl": filepath.Join(proj, "aaa.jsonl")})
	putTrash(t, "claude", "bbb", "Second", time.Now().Add(-time.Hour), map[string]string{"0-bbb.jsonl": filepath.Join(proj, "bbb.jsonl")})
	m := New(Options{}, "")
	m.height, m.width = 30, 100
	m, _ = press(m, "t")
	m, first := pressType(m, tea.KeyEnter) // starts restoring "First"
	if first == nil || !m.restoring {
		t.Fatal("restore did not start")
	}
	m, _ = pressType(m, tea.KeyEsc)
	m, _ = press(m, "t") // reopen while it is still running
	if !strings.Contains(m.trash.msg, "still running") {
		t.Fatalf("reopened popup does not say a restore is running: %q", m.trash.msg)
	}
	m, _ = press(m, "j") // select "Second" and try to restore it as well
	m, second := pressType(m, tea.KeyEnter)
	if second != nil {
		t.Fatal("a second restore started while the first was running")
	}
	nm, _ := m.Update(first()) // the first result arrives
	m = nm.(Model)
	if m.restoring || m.trash == nil || !strings.Contains(m.trash.msg, "restored First") {
		t.Fatalf("restoring=%v trash=%+v", m.restoring, m.trash)
	}
}

// At the narrowest supported window the rows must stay inside the popup.
func TestTrashPopupFitsNarrowWindow(t *testing.T) {
	agents.Home = t.TempDir()
	proj := filepath.Join(agents.Home, ".claude", "projects", "-p")
	putTrash(t, "claude", "aaaaaaaa-1111", "A fairly long session name for a narrow window", time.Now(), map[string]string{"0-a.jsonl": filepath.Join(proj, "a.jsonl")})
	for _, width := range []int{44, 50, 60, 100} {
		m := New(Options{}, "")
		m.height, m.width = 20, width
		m, _ = press(m, "t")
		for i, line := range strings.Split(m.View(), "\n") {
			if w := ansi.StringWidth(line); w > width {
				t.Fatalf("width %d: line %d is %d cells wide:\n%s", width, i, w, ansi.Strip(line))
			}
		}
		if !strings.Contains(ansi.Strip(m.View()), "A fa") { // at 44 columns only the start of the name fits
			t.Fatalf("width %d: the name is not visible:\n%s", width, ansi.Strip(m.View()))
		}
	}
}

// The columns are the ones `muxlair trash` prints, in the same order, with each
// value centered under its header. Long names are cut, and an empty one says so.
func TestTrashPopupColumnsAlignedAndNamesCut(t *testing.T) {
	agents.Home = t.TempDir()
	proj := filepath.Join(agents.Home, ".claude", "projects", "-p")
	now := time.Now()
	long := strings.Repeat("very long name ", 8)
	putTrash(t, "claude", "aaaaaaaa-1", long, now, map[string]string{"0-a.jsonl": filepath.Join(proj, "a.jsonl")})
	putTrash(t, "claude", "bbbbbbbb-2", "", now.Add(-time.Hour), map[string]string{"0-b.jsonl": filepath.Join(proj, "b.jsonl")})
	putTrash(t, "codex", "cccccccc-3", "Dead one", now.Add(-2*time.Hour), nil)
	// An entry that was restored: its meta.json carries restoredAt.
	dir := putTrash(t, "claude", "dddddddd-4", "Was restored", now.Add(-3*time.Hour), map[string]string{"0-d.jsonl": filepath.Join(proj, "d.jsonl")})
	b, _ := os.ReadFile(filepath.Join(dir, "meta.json"))
	var meta map[string]any
	json.Unmarshal(b, &meta)
	meta["restoredAt"] = now
	b, _ = json.Marshal(meta)
	os.WriteFile(filepath.Join(dir, "meta.json"), b, 0o600)

	m := New(Options{}, "")
	m.height, m.width = 30, 140
	m, _ = press(m, "t")
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	find := func(sub string) (string, bool) {
		for _, l := range lines {
			if strings.Contains(l, sub) {
				return l, true
			}
		}
		return "", false
	}
	// span returns the display columns [from, to) of the idx-th word after `after` in line.
	words := regexp.MustCompile(`\S+`)
	span := func(line, after string, idx int) (int, int) {
		at := strings.Index(line, after) + len(after)
		ws := words.FindAllStringIndex(line[at:], -1)
		if len(ws) <= idx {
			t.Fatalf("no word %d after %q in %q", idx, after, line)
		}
		return ansi.StringWidth(line[:at+ws[idx][0]]), ansi.StringWidth(line[:at+ws[idx][1]])
	}
	mid := func(a, b int) int { return (a + b) / 2 }

	head, ok := find("RESTORABLE")
	if !ok {
		t.Fatalf("no header:\n%s", strings.Join(lines, "\n"))
	}
	hmid := map[string]int{}
	last := -1
	for _, h := range []string{"AGENT", "ID", "DELETED", "RESTORABLE", "RESTORED", "NAME"} {
		c := strings.Index(head, h)
		if c <= last {
			t.Fatalf("header column %s is out of order: %q", h, head)
		}
		last = c
		hmid[h] = mid(ansi.StringWidth(head[:c]), ansi.StringWidth(head[:c+len(h)]))
	}
	near := func(what, id string, got, want int) {
		if d := got - want; d < -1 || d > 1 {
			t.Errorf("%s of %s is centered at %d, its header at %d (centered columns may differ by 1 cell)", what, id, got, want)
		}
	}
	for _, id := range []string{"aaaaaaaa", "bbbbbbbb", "cccccccc", "dddddddd"} {
		row, ok := find(id)
		if !ok {
			t.Fatalf("no row for %s", id)
		}
		ia := ansi.StringWidth(row[:strings.Index(row, id)])
		near("ID", id, mid(ia, ia+len(id)), hmid["ID"])
		// words after the id: date (2 words), RESTORABLE, then RESTORED (1 or 2 words), then the name
		d1, _ := span(row, id, 0)
		_, d2 := span(row, id, 1)
		near("DELETED", id, mid(d1, d2), hmid["DELETED"])
		r1, r2 := span(row, id, 2)
		want := "yes"
		switch id {
		case "cccccccc":
			want = "no" // no session file
		case "dddddddd":
			want = "done" // already restored
		}
		if got := row[strings.Index(row, id):]; !strings.Contains(got, " "+want+" ") {
			t.Fatalf("%s: expected RESTORABLE %q in %q", id, want, row)
		}
		near("RESTORABLE", id, mid(r1, r2), hmid["RESTORABLE"])
	}
	// The agent value is centered under AGENT, and the name under NAME.
	{
		row, _ := find("bbbbbbbb")
		a := strings.Index(row, "claude")
		near("AGENT", "bbbbbbbb", mid(ansi.StringWidth(row[:a]), ansi.StringWidth(row[:a])+len("claude")), hmid["AGENT"])
		n := strings.Index(row, "(no name)")
		near("NAME", "bbbbbbbb", mid(ansi.StringWidth(row[:n]), ansi.StringWidth(row[:n])+len("(no name)")), hmid["NAME"])
	}
	if row, _ := find("dddddddd"); strings.Contains(row, " - ") {
		t.Errorf("the restored entry should show its restore date, not -: %q", row)
	}
	if row, _ := find("aaaaaaaa"); !strings.Contains(row, "very long name") || !strings.Contains(row, "…") || strings.Contains(row, strings.Repeat("very long name ", 4)) {
		t.Errorf("long name not cut: %q", row)
	}
	// The name is cut at 40 cells at most.
	row, _ := find("aaaaaaaa")
	n := strings.Index(row, "very long name")
	cut := strings.TrimSpace(row[n:])
	cut = strings.TrimSpace(strings.TrimSuffix(cut, "│"))
	cut = strings.TrimSpace(strings.SplitN(cut, "│", 2)[0])
	if got := ansi.StringWidth(cut); got > 40 {
		t.Errorf("name is %d cells wide, want at most 40: %q", got, cut)
	}
}

// "h" inside the trash popup hides the entries that can't be restored, keeps the
// selected entry selected, and the list index still maps to the right entry.
func TestTrashHideNonRestorable(t *testing.T) {
	agents.Home = t.TempDir()
	proj := filepath.Join(agents.Home, ".claude", "projects", "-p")
	now := time.Now()
	putTrash(t, "claude", "aaa", "Newest", now, map[string]string{"0-aaa.jsonl": filepath.Join(proj, "aaa.jsonl")})
	putTrash(t, "codex", "bbb", "Dead one", now.Add(-time.Hour), nil) // not restorable
	putTrash(t, "claude", "ccc", "Oldest", now.Add(-2*time.Hour), map[string]string{"0-ccc.jsonl": filepath.Join(proj, "ccc.jsonl")})
	// An entry that was already restored still has its files, but enter refuses it.
	dir := putTrash(t, "codex", "ddd", "Was restored", now.Add(-3*time.Hour), map[string]string{"0-ddd.jsonl": filepath.Join(proj, "ddd.jsonl")})
	b, _ := os.ReadFile(filepath.Join(dir, "meta.json"))
	var meta map[string]any
	json.Unmarshal(b, &meta)
	meta["restoredAt"] = now
	b, _ = json.Marshal(meta)
	os.WriteFile(filepath.Join(dir, "meta.json"), b, 0o600)

	m := New(Options{}, "")
	m.height, m.width = 30, 120
	m, _ = press(m, "t")
	if got := len(m.trash.visible()); got != 4 {
		t.Fatalf("all 4 entries should be listed, got %d", got)
	}
	if !strings.Contains(ansi.Strip(m.View()), "h hide non-restorable") {
		t.Fatal("the bottom bar does not mention the h key")
	}

	m, _ = press(m, "h")
	if got := len(m.trash.visible()); got != 2 {
		t.Fatalf("after h, the dead and the restored entries should be hidden: %d visible", got)
	}
	v := ansi.Strip(m.View())
	if strings.Contains(v, "Dead one") || strings.Contains(v, "Was restored") || !strings.Contains(v, "Oldest") || !strings.Contains(v, "2 that can't be restored are hidden") || !strings.Contains(v, "h show all") {
		t.Fatalf("hidden view is wrong:\n%s", v)
	}

	// Select "Oldest", then toggle: it stays selected whether or not the dead one is shown.
	m, _ = press(m, "j")
	sel := func() string { return m.trash.visible()[m.trash.cur].Meta.ID }
	if sel() != "ccc" {
		t.Fatalf("selected %s, want ccc", sel())
	}
	m, _ = press(m, "h")
	if len(m.trash.visible()) != 4 || sel() != "ccc" {
		t.Fatalf("showing all again: %d visible, selected %s", len(m.trash.visible()), sel())
	}
	m, _ = press(m, "h")
	if len(m.trash.visible()) != 2 || sel() != "ccc" {
		t.Fatalf("hiding again: %d visible, selected %s", len(m.trash.visible()), sel())
	}

	// With the dead entry hidden, enter restores the entry under the cursor ("Oldest"), not entries[cur].
	m, cmd := pressType(m, tea.KeyEnter)
	if cmd == nil {
		t.Fatal("restore did not start")
	}
	nm, _ := m.Update(cmd())
	m = nm.(Model)
	if _, err := os.Stat(filepath.Join(proj, "ccc.jsonl")); err != nil {
		t.Fatalf("the selected entry was not restored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(proj, "aaa.jsonl")); err == nil {
		t.Fatal("restored the wrong entry")
	}
	if m.trash == nil || !m.trash.hide {
		t.Fatalf("the popup should stay open and keep hiding: %+v", m.trash)
	}
}

// Everything hidden: the popup says so, and the keys don't crash.
func TestTrashHideEverything(t *testing.T) {
	agents.Home = t.TempDir()
	putTrash(t, "codex", "bbb", "Dead one", time.Now(), nil)
	m := New(Options{}, "")
	m.height, m.width = 30, 120
	m, _ = press(m, "t")
	m, _ = press(m, "h")
	if len(m.trash.visible()) != 0 || !strings.Contains(ansi.Strip(m.View()), "nothing here can be restored") {
		t.Fatalf("empty hidden view:\n%s", ansi.Strip(m.View()))
	}
	m, cmd := pressType(m, tea.KeyEnter)
	m, _ = press(m, "j")
	m, _ = press(m, "k")
	if cmd != nil {
		t.Fatal("enter did something with nothing listed")
	}
	m, _ = press(m, "h")
	if len(m.trash.visible()) != 1 {
		t.Fatal("h should show the entry again")
	}
}

// Opening the popup tells the user about "h" when there is something it would hide.
func TestTrashPopupHintsAtHOnOpen(t *testing.T) {
	agents.Home = t.TempDir()
	proj := filepath.Join(agents.Home, ".claude", "projects", "-p")
	putTrash(t, "claude", "aaa", "Fine", time.Now(), map[string]string{"0-aaa.jsonl": filepath.Join(proj, "aaa.jsonl")})
	m := New(Options{}, "")
	m.height, m.width = 30, 120
	m, _ = press(m, "t")
	if strings.Contains(ansi.Strip(m.View()), "press h") {
		t.Fatal("hinted at h with nothing to hide")
	}
	m, _ = pressType(m, tea.KeyEsc)

	putTrash(t, "codex", "bbb", "Dead one", time.Now().Add(-time.Hour), nil)
	m, _ = press(m, "t")
	v := ansi.Strip(m.View())
	if !strings.Contains(v, "1 can't be restored · press h to hide them") {
		t.Fatalf("no hint on open:\n%s", v)
	}
	m, _ = press(m, "h")
	if v := ansi.Strip(m.View()); !strings.Contains(v, "1 that can't be restored are hidden · press h to show them") {
		t.Fatalf("no hint after hiding:\n%s", v)
	}
	// At the narrowest supported window the note must stay inside the popup.
	for _, hide := range []bool{true, false} {
		n := New(Options{}, "")
		n.height, n.width = 20, 44
		n, _ = press(n, "t")
		if hide {
			n, _ = press(n, "h")
		}
		for i, line := range strings.Split(n.View(), "\n") {
			if w := ansi.StringWidth(line); w > 44 {
				t.Fatalf("hide=%v: line %d is %d cells wide:\n%s", hide, i, w, ansi.Strip(line))
			}
		}
	}
}

// The rows are read once: a saved file that disappears while the popup is open
// does not shift the list under the cursor, so enter still acts on the row the
// user picked. The restore re-checks the files and says what is missing.
func TestTrashRowsDoNotShiftUnderTheCursor(t *testing.T) {
	agents.Home = t.TempDir()
	proj := filepath.Join(agents.Home, ".claude", "projects", "-p")
	now := time.Now()
	dirA := putTrash(t, "claude", "aaa", "First", now, map[string]string{"0-aaa.jsonl": filepath.Join(proj, "aaa.jsonl")})
	putTrash(t, "claude", "bbb", "Second", now.Add(-time.Hour), map[string]string{"0-bbb.jsonl": filepath.Join(proj, "bbb.jsonl")})
	m := New(Options{}, "")
	m.height, m.width = 30, 120
	m, _ = press(m, "t")
	m, _ = press(m, "h")
	m, _ = press(m, "j")                          // on "Second"
	os.Remove(filepath.Join(dirA, "0-aaa.jsonl")) // an EARLIER row stops being restorable behind the user's back
	if got := m.trash.visible()[m.trash.cur].Meta.ID; got != "bbb" {
		t.Fatalf("the cursor moved to %s", got)
	}
	m, cmd := pressType(m, tea.KeyEnter)
	if cmd == nil {
		t.Fatal("enter did not start the restore")
	}
	nm, _ := m.Update(cmd())
	m = nm.(Model)
	if _, err := os.Stat(filepath.Join(proj, "bbb.jsonl")); err != nil {
		t.Fatalf("the selected entry (Second) was not restored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(proj, "aaa.jsonl")); err == nil {
		t.Fatal("restored the wrong entry")
	}

	// And an entry whose file vanished after the list was built is refused with a reason.
	m2 := New(Options{}, "")
	m2.height, m2.width = 30, 120
	agents.Home = t.TempDir()
	dir := putTrash(t, "claude", "ccc", "Gone", now, map[string]string{"0-ccc.jsonl": filepath.Join(agents.Home, "x", "ccc.jsonl")})
	m2, _ = press(m2, "t")
	os.Remove(filepath.Join(dir, "0-ccc.jsonl"))
	m2, cmd2 := pressType(m2, tea.KeyEnter)
	nm2, _ := m2.Update(cmd2())
	m2 = nm2.(Model)
	if m2.trash == nil || !m2.trash.isErr || !strings.Contains(m2.trash.msg, "missing") {
		t.Fatalf("a vanished file should be reported: %+v", m2.trash)
	}
}

// Enter is the only restore key in the trash popup; u is not bound.
func TestTrashPopupUIsNotARestoreKey(t *testing.T) {
	agents.Home = t.TempDir()
	proj := filepath.Join(agents.Home, ".claude", "projects", "-p")
	putTrash(t, "claude", "aaa", "Fine", time.Now(), map[string]string{"0-aaa.jsonl": filepath.Join(proj, "aaa.jsonl")})
	m := New(Options{}, "")
	m.height, m.width = 30, 120
	m, _ = press(m, "t")
	m, cmd := press(m, "u")
	if cmd != nil || m.restoring || m.trash == nil || m.trash.msg != "" {
		t.Fatalf("u did something in the popup: cmd=%v restoring=%v trash=%+v", cmd != nil, m.restoring, m.trash)
	}
	if strings.Contains(ansi.Strip(m.View()), "enter/u") {
		t.Fatal("the bottom bar still mentions u")
	}
	if _, cmd := pressType(m, tea.KeyEnter); cmd == nil {
		t.Fatal("enter should still restore")
	}
}

// The popup's bottom bar spells out what enter does, and falls back to a shorter
// line when the window is too narrow for it.
func TestTrashBottomBarWording(t *testing.T) {
	agents.Home = t.TempDir()
	proj := filepath.Join(agents.Home, ".claude", "projects", "-p")
	putTrash(t, "claude", "aaa", "Fine", time.Now(), map[string]string{"0-aaa.jsonl": filepath.Join(proj, "aaa.jsonl")})
	last := func(m Model) string {
		lines := strings.Split(ansi.Strip(m.View()), "\n")
		return strings.TrimSpace(lines[len(lines)-1])
	}
	wide := New(Options{}, "")
	wide.height, wide.width = 30, 120
	wide, _ = press(wide, "t")
	if got := last(wide); !strings.Contains(got, "enter restore the selected session") || !strings.Contains(got, "h hide non-restorable") || !strings.Contains(got, "esc close") {
		t.Fatalf("wide bar: %q", got)
	}
	wide, _ = press(wide, "h")
	if got := last(wide); !strings.Contains(got, "enter restore the selected session") || !strings.Contains(got, "h show all") {
		t.Fatalf("wide bar after h: %q", got)
	}
	narrow := New(Options{}, "")
	narrow.height, narrow.width = 30, 60
	narrow, _ = press(narrow, "t")
	got := last(narrow)
	if strings.Contains(got, "the selected session") || !strings.Contains(got, "enter restore") || !strings.Contains(got, "esc close") || ansi.StringWidth(got) > 60 {
		t.Fatalf("narrow bar: %q (%d cells)", got, ansi.StringWidth(got))
	}
}
