// Package ui is the muxlair Bubble Tea dashboard.
package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/fsnotify/fsnotify"

	"github.com/phr0nak/muxlair/internal/agents"
	"github.com/phr0nak/muxlair/internal/clip"
	"github.com/phr0nak/muxlair/internal/config"
	"github.com/phr0nak/muxlair/internal/mux"
)

var (
	agentColor = map[string]lipgloss.Color{
		"claude": "#D97757", // Anthropic orange
		"codex":  "#3B82F6", // blue
		"agy":    "#8B5CF6", // Antigravity purple
	}
	statusColor = map[agents.Status]lipgloss.Color{
		agents.Busy:   "#22C55E",
		agents.Idle:   "#EAB308",
		agents.Exited: "#6B7280",
	}
	dim       = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	header    = lipgloss.NewStyle().Bold(true)
	cursor    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#F9FAFB"))
	errSty    = lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444"))
	border    = lipgloss.NewStyle().Foreground(lipgloss.Color("#4B5563"))
	banner    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#F9FAFB"))
	colHead   = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280")).Bold(true)
	selBg     = lipgloss.AdaptiveColor{Light: "#E5E7EB", Dark: "#2B303B"} // light row highlight
	selStyle  = lipgloss.NewStyle().Reverse(true)                         // selected text in the details pane
	popBorder = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF"))
)

type loadedMsg struct {
	sessions []agents.Session
	targets  map[string]mux.Target
	err      error
}
type tickMsg struct{}
type fsMsg struct{}
type statusMsg string
type deletedMsg string
type renamedMsg struct{ key, name, err, note string }
type autoDeletedMsg struct {
	deleted []string // "<agent> <session id>"
	failed  []string // keys; not retried this run
	err     string   // last error
}
type detailsMsg struct {
	key     string
	updated time.Time
	d       agents.Details
	err     string
}
type detailEntry struct {
	loaded  time.Time
	updated time.Time // session's Updated when loaded
	d       agents.Details
	err     string
}
type openFailedMsg struct{ key, err string }
type renameDoneMsg struct{ key, err string }

type row struct {
	group  string // set on group header rows
	s      *agents.Session
	spacer bool // blank line between folder groups; never selected
}

type configMsg struct{}

// resizeShownMsg ends the details-width readout, unless a newer resize restarted it.
type resizeShownMsg struct{ at time.Time }

const resizeShown = 1500 * time.Millisecond

type copiedMsg struct {
	n   int // characters copied
	via string
	err string
}

// selection is a mouse selection in the details pane, in pane coordinates:
// x is the cell within the pane, y the details line.
type selection struct{ x1, y1, x2, y2 int }

// ordered returns the selection with its start before its end.
func (s selection) ordered() selection {
	if s.y2 < s.y1 || (s.y2 == s.y1 && s.x2 < s.x1) {
		return selection{s.x2, s.y2, s.x1, s.y1}
	}
	return s
}

// settingsForm is the settings popup: an edited copy of the options.
type settingsForm struct {
	opts Options
	cur  int // index into settingItems, then Save, Cancel
	err  string
}

const (
	itemSave   = 4
	itemCancel = 5
)

// keyHelp is the "?" popup: every key and what it does.
var keyHelp = [][2]string{
	{"↑ ↓  j k", "move"},
	{"enter", "jump to the session, or reopen it"},
	{"double-click", "same as enter"},
	{"w", "open in a new cmux workspace"},
	{"← →  h l", "collapse / expand a folder"},
	{"space  tab", "toggle a folder"},
	{"r", "rename (in the agent too)"},
	{"d", "delete (to ~/.muxlair/trash)"},
	{"/", "filter; esc clears it"},
	{"i", "show / hide details"},
	{"< >", "resize details; dragging works too"},
	{"mouse drag", "select details text; copied on release"},
	{"R", "refresh"},
	{"s", "settings"},
	{"?", "this help"},
	{"q  ctrl+c", "quit"},
}

var settingItems = []string{"Mouse support", "Tint opened tabs", "Details pane at start", "Auto-delete headless"}

// autoDeleteSteps are the choices offered for auto_delete_headless.
var autoDeleteSteps = []time.Duration{0, time.Minute, 5 * time.Minute, 10 * time.Minute, 30 * time.Minute, time.Hour, 24 * time.Hour}

type Model struct {
	cfgPath     string
	cfgEvents   chan struct{}
	settings    *settingsForm
	help        bool       // keys popup open
	sel         *selection // text selected in the details pane
	selecting   bool       // mouse button held while selecting
	selPane     string     // the pane's text when the selection started
	resizedAt   time.Time  // last details resize; its width shows in the bottom bar briefly
	helpTop     int        // first visible help line when it must scroll
	sessions    []agents.Session
	targets     map[string]mux.Target
	names       map[string]string    // last seen name per session key, for rename sync
	rename      map[string]bool      // sessions whose tab still needs the current name
	renaming    map[string]bool      // sessions with a rename command in flight
	pending     map[string]time.Time // sessions muxlair just opened; rename their tab once found
	collapsed   map[string]bool
	rows        []row
	cur         int
	filter      string
	filtering   bool
	confirm     *agents.Session
	editing     *agents.Session // session being renamed
	editBuf     string
	agentRename map[string]bool // sessions with an agent rename in flight
	showDetails bool
	detailsW    int                    // user-chosen details width, 0 = default
	dragging    bool                   // dragging the list/details divider
	autoDel     map[string]bool        // headless sessions auto-delete failed on
	autoBusy    bool                   // an auto-delete batch is running
	autoKeys    map[string]bool        // sessions in the running batch; open refuses them
	details     map[string]detailEntry // per session key; reloaded when the session's Updated changes
	detailsBusy map[string]bool
	status      string
	height      int
	width       int
	events      chan struct{}
	loading     bool              // a load is in flight; results apply in order
	dirty       bool              // a refresh was requested while loading
	labels      map[string]string // group cwd -> header label
	counts      map[string]int    // group cwd -> sessions shown in it
	opts        Options
	lastClick   time.Time
	lastKey     string // session (or group) clicked last, for double-click
	top         int    // first visible row
}

// Options are the user settings from ~/.muxlair/config.json.
type Options = config.Options

// New builds the dashboard; cfgPath is the config file it watches and saves.
func New(opts Options, cfgPath string) Model {
	return Model{
		cfgPath: cfgPath, cfgEvents: make(chan struct{}, 1),
		opts: opts, labels: map[string]string{}, agentRename: map[string]bool{},
		showDetails: opts.Details, details: map[string]detailEntry{}, detailsBusy: map[string]bool{},
		autoDel: map[string]bool{}, autoKeys: map[string]bool{},
		names: map[string]string{}, rename: map[string]bool{}, renaming: map[string]bool{}, pending: map[string]time.Time{}, collapsed: map[string]bool{},
		events: make(chan struct{}, 1), loading: true, // Init starts the first load
	}
}

func (m Model) Init() tea.Cmd {
	go watch(m.events, m.cfgEvents, m.cfgPath)
	return tea.Batch(load, tick(), waitFS(m.events), waitConfig(m.cfgEvents))
}

func load() tea.Msg {
	ss, err := agents.List()
	return loadedMsg{ss, mux.Targets(), err}
}

// reload starts a load unless one is in flight, so a stale result never lands after a newer one.
func (m *Model) reload() tea.Cmd {
	if m.loading {
		m.dirty = true
		return nil
	}
	m.loading = true
	return load
}

func tick() tea.Cmd {
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

func waitFS(ch chan struct{}) tea.Cmd {
	return func() tea.Msg { <-ch; return fsMsg{} }
}

func waitConfig(ch chan struct{}) tea.Cmd {
	return func() tea.Msg { <-ch; return configMsg{} }
}

// watch signals ch (coalesced) whenever an agent's session store changes, and
// cfg when the config file changes (an edit by hand, or our own Save).
func watch(ch, cfg chan struct{}, cfgPath string) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return
	}
	for _, p := range agents.WatchPaths() {
		w.Add(p)
	}
	// Watch the directory, not the file: editors and Save replace it by rename.
	if cfgPath != "" && os.MkdirAll(filepath.Dir(cfgPath), 0o700) == nil {
		w.Add(filepath.Dir(cfgPath))
	}
	var debounce, cfgDebounce <-chan time.Time
	for {
		select {
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if ev.Name == cfgPath && cfgDebounce == nil {
				cfgDebounce = time.After(150 * time.Millisecond)
				continue
			}
			if ev.Has(fsnotify.Create) && filepath.Dir(ev.Name) == filepath.Join(agents.Home, ".claude", "projects") {
				w.Add(ev.Name) // new Claude project dir; Add on a non-dir is a harmless error
			}
			if relevant(ev.Name) && debounce == nil {
				debounce = time.After(150 * time.Millisecond)
			}
		case <-w.Errors:
		case <-cfgDebounce:
			cfgDebounce = nil
			select {
			case cfg <- struct{}{}:
			default:
			}
		case <-debounce:
			debounce = nil
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
}

// relevant filters out noisy files (codex writes logs sqlite constantly).
func relevant(path string) bool {
	base := filepath.Base(path)
	switch {
	case strings.HasPrefix(base, "state_5.sqlite"), base == "session_index.jsonl":
		return true
	case strings.HasPrefix(path, agents.CodexHome()+"/"):
		return false
	}
	return strings.HasSuffix(base, ".json") || strings.HasSuffix(base, ".jsonl")
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	nm, cmd := m.update(msg)
	mm := nm.(Model)
	mm.scroll()
	return mm, tea.Batch(cmd, mm.wantDetails())
}

// wantDetails loads details for the selected session when the pane shows and
// they are missing or older than the session's last activity.
func (m Model) wantDetails() tea.Cmd {
	if _, dw := m.split(); dw == 0 {
		return nil
	}
	r, ok := m.row()
	if !ok || r.s == nil {
		return nil
	}
	s := *r.s
	k := s.Key()
	// Failed loads are retried after 5s rather than cached until the session changes.
	if e, ok := m.details[k]; (ok && !e.updated.Before(s.Updated) && (e.err == "" || time.Since(e.loaded) < 5*time.Second)) || m.detailsBusy[k] {
		return nil
	}
	m.detailsBusy[k] = true
	return func() tea.Msg {
		d, err := agents.SessionDetails(s)
		msg := detailsMsg{key: k, updated: s.Updated, d: d}
		if err != nil {
			msg.err = err.Error()
		}
		return msg
	}
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.height, m.width = msg.Height, msg.Width
		m.sel, m.selecting = nil, false            // the pane reflows; old coordinates would copy other text
		m.helpTop = min(m.helpTop, m.helpMaxTop()) // a bigger window needs less scrolling
	case tickMsg:
		return m, tea.Batch(m.reload(), tick())
	case fsMsg:
		return m, tea.Batch(m.reload(), waitFS(m.events))
	case resizeShownMsg:
		if msg.at.Equal(m.resizedAt) {
			m.resizedAt = time.Time{}
		}
	case copiedMsg:
		if msg.err != "" {
			m.status = "copy failed: " + msg.err
		} else {
			m.status = fmt.Sprintf("copied %d characters (%s)", msg.n, msg.via)
		}
	case configMsg:
		opts, err := config.Load(m.cfgPath)
		if err != nil {
			m.status = "config not applied: " + err.Error() // keep the current settings
			return m, waitConfig(m.cfgEvents)
		}
		return m, tea.Batch(m.applyOptions(opts), waitConfig(m.cfgEvents))
	case statusMsg:
		m.status = string(msg)
	case renameDoneMsg:
		delete(m.renaming, msg.key)
		if msg.err != "" {
			m.rename[msg.key] = true
			m.status = "rename: " + msg.err
		}
	case openFailedMsg:
		delete(m.pending, msg.key)
		if msg.err != "" {
			m.status = "open: " + msg.err
		}
	case detailsMsg:
		delete(m.detailsBusy, msg.key)
		// New details reshuffle the pane's lines; a selection made on the old
		// layout would copy a different field.
		m.sel, m.selecting = nil, false
		m.details[msg.key] = detailEntry{time.Now(), msg.updated, msg.d, msg.err}
	case autoDeletedMsg:
		m.autoBusy = false
		clear(m.autoKeys)
		for _, k := range msg.failed {
			m.autoDel[k] = true // failing sessions aren't retried on every refresh
		}
		switch {
		case msg.err != "":
			m.status = "auto-delete " + msg.err
		case len(msg.deleted) == 1:
			m.status = "auto-deleted " + msg.deleted[0] + " (in ~/.muxlair/trash)"
		case len(msg.deleted) > 1:
			m.status = fmt.Sprintf("auto-deleted %d headless sessions (in ~/.muxlair/trash)", len(msg.deleted))
		}
		if len(msg.deleted) > 0 {
			return m, m.reload()
		}
		return m, nil
	case renamedMsg:
		delete(m.agentRename, msg.key)
		if msg.err != "" {
			m.status = "rename: " + msg.err
			return m, nil
		}
		m.status = "renamed to " + msg.name
		if msg.note != "" {
			m.status += "; " + msg.note
		}
		return m, m.reload()
	case deletedMsg:
		m.status = "moved to " + string(msg)
		return m, m.reload()
	case loadedMsg:
		m.loading = false
		if m.status == "refreshing…" {
			m.status = "" // was only cleared by the next key press, so refresh looked hung
		}
		var again tea.Cmd
		if m.dirty {
			m.dirty = false
			again = m.reload()
		}
		if msg.err != nil {
			m.status = "load: " + msg.err.Error() // other agents' sessions still shown
		}
		cmd := m.syncNames(msg.sessions, msg.targets)
		m.sessions, m.targets = msg.sessions, msg.targets
		m.rebuild()
		if m.sel != nil && m.paneText() != m.selPane {
			m.sel, m.selecting = nil, false // the pane changed under the selection
		}
		auto := m.autoDelete()
		return m, tea.Batch(cmd, again, auto)
	case tea.KeyMsg:
		return m.key(msg)
	case tea.MouseMsg:
		return m.mouse(msg)
	}
	return m, nil
}

// mouse: wheel moves the cursor, click selects, click on a header toggles it,
// a second click on the same session within 400ms opens it.
func (m Model) mouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.help {
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
			m.help = false // a click anywhere closes it
		}
		return m, nil
	}
	// The settings popup is modal: it gets every click, before divider hit-testing.
	if m.settings != nil {
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
			return m.settingsClick(msg.X, msg.Y)
		}
		return m, nil
	}
	lw, dw := m.split()
	// Details pane text selection: drag to select, release to copy.
	paneX, paneY := msg.X-(lw+2), msg.Y-1
	inPane := dw > 0 && paneX >= 0 && paneX < dw && paneY >= 0 && paneY < m.avail()+3
	switch {
	case msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft && inPane && !m.tooSmall() &&
		m.confirm == nil && m.editing == nil && !m.filtering: // popups are modal
		m.sel, m.selecting = &selection{paneX, paneY, paneX, paneY}, true
		m.selPane = m.paneText()
		return m, nil
	case msg.Action == tea.MouseActionMotion && m.selecting:
		m.sel.x2 = max(0, min(paneX, dw-1))
		m.sel.y2 = max(0, min(paneY, m.avail()+2))
		return m, nil
	case msg.Action == tea.MouseActionRelease && m.selecting:
		m.selecting = false
		if *m.sel == (selection{m.sel.x1, m.sel.y1, m.sel.x1, m.sel.y1}) {
			m.sel = nil // a plain click selects nothing
			return m, nil
		}
		text := m.selectedText()
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
	if msg.Action == tea.MouseActionPress {
		m.sel, m.selecting = nil, false // a click anywhere else clears the selection
	}
	switch {
	case msg.Action == tea.MouseActionRelease:
		m.dragging = false
		return m, nil
	case msg.Action == tea.MouseActionMotion:
		if m.dragging {
			// Divider follows the pointer, held within split's limits (0 would mean "default").
			m.detailsW = max(24, min(m.width-msg.X-2, m.width-50))
			return m, m.showResize()
		}
		return m, nil
	case msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft && dw > 0 && msg.X == lw+1:
		m.dragging = true // pressed on the divider
		return m, nil
	}
	if msg.Action != tea.MouseActionPress || m.tooSmall() || m.confirm != nil || m.filtering || m.editing != nil {
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.step(-1)
	case tea.MouseButtonWheelDown:
		m.step(+1)
	case tea.MouseButtonLeft:
		start, end := m.window()
		i := start + msg.Y - firstRowY
		if msg.Y < firstRowY || i >= end || msg.X > lw { // X past the list: details pane
			return m, nil
		}
		r := m.rows[i]
		if r.spacer {
			return m, nil
		}
		key := "group:" + r.group
		if r.s != nil {
			key = r.s.Key()
		}
		// Keyed by session, so a refresh reordering rows between clicks can't open another one.
		double := key == m.lastKey && time.Since(m.lastClick) < 400*time.Millisecond
		m.cur, m.lastKey, m.lastClick = i, key, time.Now()
		if r.s == nil {
			m.collapsed[r.group] = !m.collapsed[r.group]
			m.rebuild()
			m.cur = m.indexOfGroup(r.group)
			m.lastClick = time.Time{} // a header click never starts a double-click
		} else if double {
			m.lastClick = time.Time{}
			return m, m.open(false)
		}
	}
	return m, nil
}

// settingsClick: a click on an option selects and changes it; on the buttons
// line the left half is Save, the right half Cancel.
func (m Model) settingsClick(x, y int) (tea.Model, tea.Cmd) {
	pop := m.popup()
	px, py := popupOrigin(m.width, m.boxHeight(), pop)
	line := y - py - 1 // body line: the popup's first line is its top border
	if x < px || x >= px+ansi.StringWidth(pop[0]) {
		return m, nil
	}
	switch {
	case line >= 0 && line < len(settingItems):
		m.settings.cur = line
		m.settings.change(+1)
	case line == len(settingItems)+1:
		if x < px+ansi.StringWidth(pop[0])/2 {
			m.settings.cur = itemSave
			return m.saveSettings()
		}
		m.settings = nil
	}
	return m, nil
}

// scroll moves the viewport only as far as needed to keep the cursor visible,
// so clicking a visible row never shifts the rows under the mouse.
func (m *Model) scroll() {
	avail := m.avail()
	if m.cur < m.top {
		m.top = m.cur
	}
	if m.cur >= m.top+avail {
		m.top = m.cur - avail + 1
	}
	m.top = max(0, min(m.top, len(m.rows)-avail))
}

// tooSmall: the box needs chromeRows plus one list row and some width; below
// that rows wrap and mouse rows would not line up with what is drawn.
func (m Model) tooSmall() bool {
	// 44 columns: the fixed columns take ~30, leaving the session name room to show.
	return (m.height > 0 && m.height <= chromeRows) || (m.width > 0 && m.width < 44)
}

// avail is the number of list rows that fit inside the box.
func (m Model) avail() int { return max(1, m.height-chromeRows) }

// window returns the [start,end) slice of rows on screen.
func (m Model) window() (int, int) {
	avail := m.avail()
	return m.top, min(len(m.rows), m.top+avail)
}

// syncNames renames the tab of any open session whose name changed (or that muxlair just opened).
func (m Model) syncNames(ss []agents.Session, targets map[string]mux.Target) tea.Cmd {
	var cmds []tea.Cmd
	first := len(m.names) == 0
	for _, s := range ss {
		k := s.Key()
		prev, seen := m.names[k]
		m.names[k] = s.Name
		if !first && seen && prev != s.Name {
			m.rename[k] = true // kept until the tab is reachable and the rename succeeds
		}
		t, open := targets[s.TTY]
		if !open || s.TTY == "" {
			continue
		}
		if _, opened := m.pending[k]; opened {
			delete(m.pending, k)
			m.rename[k] = true
		}
		// One rename in flight per session; a newer name waits for the next refresh,
		// so an older command can never land last.
		if m.rename[k] && !m.renaming[k] {
			delete(m.rename, k) // renameDoneMsg puts it back on failure, so failures retry
			m.renaming[k] = true
			t, name := t, tabName(s.Name)
			cmds = append(cmds, func() tea.Msg {
				if err := mux.Rename(t, name); err != nil {
					return renameDoneMsg{k, err.Error()}
				}
				return renameDoneMsg{k, ""}
			})
		}
	}
	return tea.Batch(cmds...)
}

// WithStatus returns m showing msg in the bottom bar (e.g. a startup problem).
func (m Model) WithStatus(msg string) Model {
	m.status = msg
	return m
}

// showResize starts (or restarts) the brief details-width readout.
func (m *Model) showResize() tea.Cmd {
	m.resizedAt = time.Now()
	at := m.resizedAt
	return tea.Tick(resizeShown, func(time.Time) tea.Msg { return resizeShownMsg{at} })
}

// applyOptions switches to opts at runtime. The details pane only follows the
// setting when the setting itself changed, so an unrelated save doesn't undo i.
func (m *Model) applyOptions(opts Options) tea.Cmd {
	var cmd tea.Cmd
	if opts.Mouse != m.opts.Mouse {
		cmd = tea.DisableMouse
		if opts.Mouse {
			cmd = tea.EnableMouseCellMotion
		}
	}
	if opts.Details != m.opts.Details {
		m.showDetails = opts.Details
	}
	m.opts = opts
	return cmd
}

// settingsKey handles keys while the settings popup is open.
func (m Model) settingsKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := m.settings
	switch k.String() {
	case "esc", "q":
		m.settings = nil
	case "up", "k", "shift+tab":
		f.cur = (f.cur + itemCancel) % (itemCancel + 1)
	case "down", "j", "tab":
		f.cur = (f.cur + 1) % (itemCancel + 1)
	case "left", "h":
		f.change(-1)
	case "right", "l", " ":
		f.change(+1)
	case "ctrl+s":
		return m.saveSettings()
	case "enter":
		switch f.cur {
		case itemSave:
			return m.saveSettings()
		case itemCancel:
			m.settings = nil
		default:
			f.change(+1)
		}
	}
	return m, nil
}

// change flips a toggle or steps the auto-delete choice by dir.
func (f *settingsForm) change(dir int) {
	switch f.cur {
	case 0:
		f.opts.Mouse = !f.opts.Mouse
	case 1:
		f.opts.ColorTabs = !f.opts.ColorTabs
	case 2:
		f.opts.Details = !f.opts.Details
	case 3:
		steps := autoDeleteChoices(f.opts.AutoDelete)
		for i, d := range steps {
			if d == f.opts.AutoDelete {
				f.opts.AutoDelete = steps[(i+dir+len(steps))%len(steps)]
				return
			}
		}
	}
}

// autoDeleteChoices is autoDeleteSteps plus the current value if it is custom
// (set by hand in the file), so opening and saving settings never loses it.
func autoDeleteChoices(cur time.Duration) []time.Duration {
	steps := append([]time.Duration(nil), autoDeleteSteps...)
	for _, d := range steps {
		if d == cur {
			return steps
		}
	}
	steps = append(steps, cur)
	sort.Slice(steps, func(i, j int) bool { return steps[i] < steps[j] })
	return steps
}

func (m Model) saveSettings() (tea.Model, tea.Cmd) {
	opts := m.settings.opts
	if err := config.Save(m.cfgPath, opts); err != nil {
		m.settings.err = err.Error()
		return m, nil
	}
	m.settings = nil
	m.status = "settings saved to " + tildify(m.cfgPath)
	return m, m.applyOptions(opts)
}

func (m Model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.sel, m.selecting = nil, false // any key clears a details selection
	if m.help {
		switch k.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			m.helpTop = max(0, min(m.helpTop, m.helpMaxTop())-1)
		case "down", "j":
			m.helpTop = min(m.helpTop+1, m.helpMaxTop())
		default:
			m.help, m.helpTop = false, 0 // any other key closes it
		}
		return m, nil
	}
	if m.settings != nil {
		return m.settingsKey(k)
	}
	if m.confirm != nil {
		s := *m.confirm
		m.confirm = nil
		if k.String() != "y" {
			m.status = "delete cancelled"
			return m, nil
		}
		return m, func() tea.Msg {
			// Re-read state: the session may have been resumed since the snapshot.
			s, err := fresh(s)
			if err != nil {
				return statusMsg("delete: " + err.Error())
			}
			dir, err := agents.Trash(s)
			if err != nil {
				return statusMsg("delete: " + err.Error())
			}
			return deletedMsg(dir)
		}
	}
	if m.editing != nil {
		switch k.Type {
		case tea.KeyEsc:
			m.editing = nil
		case tea.KeyEnter:
			s := *m.editing
			m.editing = nil
			name, err := agents.CleanName(m.editBuf)
			if err != nil {
				m.status = "rename: " + err.Error()
				return m, nil
			}
			m.status = "renaming…"
			m.agentRename[s.Key()] = true
			return m, renameCmd(s, name)
		case tea.KeyBackspace:
			if r := []rune(m.editBuf); len(r) > 0 {
				m.editBuf = string(r[:len(r)-1])
			}
		case tea.KeyRunes, tea.KeySpace:
			m.editBuf += string(k.Runes)
		}
		return m, nil
	}
	if m.filtering {
		switch k.Type {
		case tea.KeyEnter, tea.KeyEsc:
			m.filtering = false
			if k.Type == tea.KeyEsc {
				m.filter = ""
			}
		case tea.KeyBackspace:
			if r := []rune(m.filter); len(r) > 0 {
				m.filter = string(r[:len(r)-1])
			}
		case tea.KeyRunes, tea.KeySpace:
			m.filter += string(k.Runes)
		}
		m.rebuild()
		return m, nil
	}
	m.status = ""
	switch k.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "left", "h":
		// Collapse the folder of the current row and land on its header.
		if r, ok := m.row(); ok {
			g := r.group
			if r.s != nil {
				g = r.s.Cwd
			}
			m.collapsed[g] = true
			m.rebuild()
			m.cur = m.indexOfGroup(g)
		}
	case "right", "l":
		if r, ok := m.row(); ok && r.s == nil && m.collapsed[r.group] {
			delete(m.collapsed, r.group)
			m.rebuild()
			m.cur = m.indexOfGroup(r.group)
		}
	case "<", ">":
		if _, dw := m.split(); dw > 0 {
			if k.String() == "<" {
				m.detailsW = dw + 4 // divider moves left: details wider
			} else {
				m.detailsW = dw - 4
			}
			return m, m.showResize()
		}
	case "up", "k":
		m.step(-1)
	case "down", "j":
		m.step(+1)
	case " ", "tab":
		if r, ok := m.row(); ok {
			g := r.group
			if r.s != nil {
				g = r.s.Cwd
			}
			m.collapsed[g] = !m.collapsed[g]
			m.rebuild()
			m.cur = m.indexOfGroup(g)
		}
	case "/":
		m.filtering = true
	case "i":
		m.showDetails = !m.showDetails
	case "s":
		m.settings = &settingsForm{opts: m.opts}
	case "?":
		m.help = true
	case "R":
		m.status = "refreshing…"
		clear(m.details) // R also reloads details
		return m, m.reload()
	case "r":
		if r, ok := m.row(); ok && r.s != nil {
			// One agent rename at a time per session, so two can't land out of order.
			if m.agentRename[r.s.Key()] {
				m.status = "a rename of this session is still running"
				return m, nil
			}
			s := *r.s
			m.editing, m.editBuf = &s, s.Name
		}
	case "enter":
		return m, m.open(false)
	case "w":
		return m, m.open(true)
	case "d":
		if r, ok := m.row(); ok && r.s != nil {
			s := *r.s
			m.confirm = &s
		}
	}
	return m, nil
}

// step moves the cursor by dir rows, skipping spacer rows.
func (m *Model) step(dir int) {
	for i := m.cur + dir; i >= 0 && i < len(m.rows); i += dir {
		if !m.rows[i].spacer {
			m.cur = i
			return
		}
	}
}

func (m Model) row() (row, bool) {
	if m.cur < 0 || m.cur >= len(m.rows) {
		return row{}, false
	}
	return m.rows[m.cur], true
}

func (m *Model) open(workspace bool) tea.Cmd {
	r, ok := m.row()
	if !ok || r.s == nil {
		return nil
	}
	k := r.s.Key()
	if m.autoKeys[k] {
		m.status = r.s.Name + " is being auto-deleted (to ~/.muxlair/trash); try again in a moment"
		return nil
	}
	if t, ok := m.pending[k]; ok && time.Since(t) < 15*time.Second {
		m.status = "already opening " + r.s.Name
		return nil
	}
	m.pending[k] = time.Now()
	s := *r.s
	color := ""
	if m.opts.ColorTabs {
		color = string(agentColor[s.Agent])
	}
	return func() tea.Msg {
		// Re-read state: the session may have been started elsewhere since the last refresh.
		s, err := fresh(s)
		if err != nil {
			return openFailedMsg{k, err.Error()}
		}
		if t, ok := mux.Targets()[s.TTY]; ok && s.TTY != "" {
			if err := mux.Focus(t); err != nil {
				return openFailedMsg{k, "focus: " + err.Error()}
			}
			return openFailedMsg{k, ""} // focused, nothing to rename
		}
		switch {
		case s.Dead:
			return openFailedMsg{k, "codex can't resume this session: its rollout file is gone (d deletes it)"}
		case s.PID != 0:
			// Running, but not in a terminal we can reach; a second resume would fork the session.
			where := "without a terminal"
			if s.TTY != "" {
				where = "on " + s.TTY
			}
			if mux.CmuxDenied() {
				return openFailedMsg{k, "session is running " + where + ", likely in cmux, which only lets processes started inside cmux " +
					"control it; set cmux's socket control mode to password (Settings > Automation) or run muxlair inside cmux"}
			}
			return openFailedMsg{k, "session is running " + where + ", which is not a tmux pane or cmux tab"}
		case s.MaybeLive:
			return openFailedMsg{k, "an unidentified " + s.Agent + " process in this folder may already run this session"}
		}
		if s.Headless {
			// Resumed by the user from now on: never auto-delete it.
			if err := agents.ClaimForResume(s); err != nil {
				return openFailedMsg{k, err.Error()}
			}
		}
		if err := mux.Open(tabName(s.Name), s.Cwd, s.Resume, color, workspace); err != nil {
			return openFailedMsg{k, err.Error()}
		}
		return statusMsg("opened " + s.Name)
	}
}

// autoDelete trashes headless sessions that have exited and been idle longer
// than opts.AutoDelete. Interactive sessions are never touched. One batch runs
// at a time: a single re-read of all sessions, then up to 20 deletes in a row.
func (m *Model) autoDelete() tea.Cmd {
	if m.opts.AutoDelete <= 0 || m.autoBusy {
		return nil
	}
	idle := m.opts.AutoDelete
	eligible := func(s agents.Session) bool {
		return s.Headless && s.PID == 0 && !s.MaybeLive && time.Since(s.Updated) >= idle
	}
	var keys []string
	for _, s := range m.sessions {
		_, opening := m.pending[s.Key()]
		if eligible(s) && !m.autoDel[s.Key()] && !opening && len(keys) < 20 {
			keys = append(keys, s.Key())
		}
	}
	if len(keys) == 0 {
		return nil
	}
	m.autoBusy = true
	clear(m.autoKeys)
	for _, k := range keys {
		m.autoKeys[k] = true // open() refuses these until the batch ends
	}
	return func() tea.Msg {
		ss, err := agents.List() // re-read once: anything restarted or touched since is skipped
		if err != nil {
			return autoDeletedMsg{err: err.Error()}
		}
		byKey := map[string]agents.Session{}
		for _, s := range ss {
			byKey[s.Key()] = s
		}
		var res autoDeletedMsg
		for _, k := range keys {
			s, ok := byKey[k]
			if !ok || !eligible(s) {
				continue
			}
			// Re-check right before deleting, under the lock a resume from any
			// muxlair takes, so nothing can resume it between check and delete.
			deleted := false
			err := agents.WithSessionLock(func() error {
				s, err := agents.Recheck(s)
				if err != nil || !eligible(s) {
					return nil // gone, resumed or touched: skip silently
				}
				if _, err := agents.Trash(s); err != nil {
					return err
				}
				deleted = true
				return nil
			})
			if err != nil {
				res.failed = append(res.failed, k)
				res.err = s.Agent + " " + s.ID + ": " + err.Error()
				continue
			}
			if deleted {
				res.deleted = append(res.deleted, s.Agent+" "+s.ID)
			}
		}
		return res
	}
}

// renameCmd renames s in the agent itself, so the agent and muxlair agree:
// codex through its app server, an exited Claude through its transcript, and a
// live Claude by typing /rename into its terminal (only while idle).
func renameCmd(s agents.Session, name string) tea.Cmd {
	k := s.Key()
	return func() tea.Msg {
		s, err := fresh(s)
		if err != nil {
			return renamedMsg{key: k, name: name, err: err.Error()}
		}
		note := ""
		switch {
		case s.Agent == "codex":
			note, err = renameCodex(s, name)
		case s.Agent == "claude" && s.PID == 0:
			err = agents.RenameClaudeTranscript(s, name)
		case s.Agent == "claude":
			t, ok := mux.Targets()[s.TTY]
			switch {
			case !ok || s.TTY == "":
				err = fmt.Errorf("can't reach its terminal; run /rename %s there", name)
			case s.Status == agents.Busy:
				err = fmt.Errorf("claude is busy; rename it once it is idle")
			default:
				err = mux.SendLine(t, "claude", "/rename "+name)
			}
		default:
			err = fmt.Errorf("renaming %s sessions is not supported", s.Agent)
		}
		if err != nil {
			return renamedMsg{key: k, name: name, err: err.Error()}
		}
		return renamedMsg{key: k, name: name, note: note}
	}
}

// renameCodex renames a Codex session. A running one keeps its name in memory
// and only learns a new one in its own terminal, so type /rename there when
// that is safe (the session then saves the name itself). Otherwise the name is
// set through the app server, and the open session shows it after a reopen.
func renameCodex(s agents.Session, name string) (note string, err error) {
	if s.PID != 0 {
		var why string
		t, ok := mux.Targets()[s.TTY]
		switch {
		case !ok || s.TTY == "":
			why = "its terminal isn't a tmux pane or cmux tab muxlair can reach"
		case s.Status == agents.Busy:
			why = "it is busy"
		default:
			if err := mux.SendLine(t, "codex", "/rename "+name); err == nil {
				return "", nil
			} else {
				why = err.Error()
			}
		}
		note = "the open Codex keeps the old name until you run /rename there or reopen it: " + why
	}
	return note, agents.RenameCodex(s.ID, name)
}

// tabName shortens a session name for a tmux window or cmux tab title.
func tabName(name string) string {
	if r := []rune(name); len(r) > 40 {
		return string(r[:39]) + "…"
	}
	return name
}

// fresh re-reads s from disk. It fails if state can't be read completely,
// since callers use it to guard against acting on a live session.
func fresh(s agents.Session) (agents.Session, error) {
	ss, err := agents.List()
	if err != nil {
		return s, err
	}
	for _, f := range ss {
		if f.Key() == s.Key() {
			return f, nil
		}
	}
	return s, fmt.Errorf("session %s no longer exists", s.Name)
}

// rebuild regroups sessions by cwd, groups ordered by most recent activity.
// The selected row is kept by identity, since a refresh can reorder rows.
func (m *Model) rebuild() {
	var selKey, selGroup string
	if r, ok := m.row(); ok {
		if r.s != nil {
			selKey = r.s.Key()
		} else {
			selGroup = r.group
		}
	}
	groups := map[string][]*agents.Session{}
	var order []string
	f := strings.ToLower(m.filter)
	for i := range m.sessions {
		s := &m.sessions[i]
		if f != "" && !strings.Contains(strings.ToLower(s.Name+" "+s.Agent+" "+s.Cwd), f) {
			continue
		}
		if _, ok := groups[s.Cwd]; !ok {
			order = append(order, s.Cwd) // sessions are newest-first, so this is recency order
		}
		groups[s.Cwd] = append(groups[s.Cwd], s)
	}
	m.labels = groupLabels(order)
	m.counts = map[string]int{}
	for g, ss := range groups {
		m.counts[g] = len(ss) // after the filter, matching what the folder holds on screen
	}
	m.rows = m.rows[:0]
	for i, g := range order {
		if i > 0 {
			m.rows = append(m.rows, row{spacer: true})
		}
		m.rows = append(m.rows, row{group: g})
		if m.collapsed[g] {
			continue
		}
		ss := groups[g]
		sort.SliceStable(ss, func(i, j int) bool { return ss[i].Updated.After(ss[j].Updated) })
		for _, s := range ss {
			m.rows = append(m.rows, row{s: s})
		}
	}
	for i, r := range m.rows {
		if (r.s != nil && r.s.Key() == selKey) || (r.s == nil && selGroup != "" && r.group == selGroup) {
			m.cur = i
			break
		}
	}
	m.cur = max(0, min(m.cur, len(m.rows)-1))
	if m.cur < len(m.rows) && m.rows[m.cur].spacer {
		m.cur++ // a spacer is always followed by a folder header
	}
}

// groupLabels names each group by its own folder; only when two folders share a
// name does the label grow parent components until they differ.
func groupLabels(dirs []string) map[string]string {
	depth := map[string]int{}
	labels := map[string]string{}
	for {
		count := map[string]int{}
		for _, d := range dirs {
			labels[d] = lastN(tildify(d), max(1, depth[d]))
			count[labels[d]]++
		}
		grew := false
		for _, d := range dirs {
			if count[labels[d]] > 1 && labels[d] != tildify(d) {
				depth[d] = max(1, depth[d]) + 1
				grew = true
			}
		}
		if !grew {
			return labels
		}
	}
}

// lastN returns the last n path components of p.
func lastN(p string, n int) string {
	parts := strings.Split(strings.TrimSuffix(p, "/"), "/")
	if n >= len(parts) {
		return p
	}
	return strings.Join(parts[len(parts)-n:], "/")
}

func (m Model) indexOfGroup(g string) int {
	for i, r := range m.rows {
		if r.group == g {
			return i
		}
	}
	return 0
}

// Layout: a rounded box titled "muxlair" holding a column header, a rule and the
// rows, optionally split with a details pane on the right; the help line sits
// below it on the terminal's last row. Rename/delete prompts are popups on top.
const chromeRows = 6 // top border, blank, column header, rule, bottom border, help line
const firstRowY = 4  // screen row of the first list row

// statusCol is the STATUS cell: a colored dot plus a word, 8 cells wide.
const statusCol = 8

// split returns the inner widths of the list and details panes (details 0 = off).
func (m Model) split() (list, details int) {
	w := m.width
	if w <= 0 {
		w = 80
	}
	if !m.showDetails || w < 90 {
		return max(10, w-2), 0
	}
	details = w * 36 / 100 // default share; < > or dragging change it
	if m.detailsW > 0 {
		details = m.detailsW // set with < > or by dragging the divider
	}
	details = max(24, min(details, w-50)) // keep the list usable
	return w - details - 3, details
}

func (m Model) View() string {
	if m.tooSmall() {
		return m.fit("muxlair: window too small")
	}
	lw, dw := m.split()
	side := border.Render("│")
	hline := func(n int) string { return border.Render(strings.Repeat("─", n)) }
	titled := func(t string, n int) string {
		t = " " + banner.Render(t) + " "
		return border.Render("─") + t + hline(max(0, n-1-ansi.StringWidth(t)))
	}
	det := m.detailsPane(dw)
	right := func(i int) string {
		if dw == 0 {
			return ""
		}
		return m.selected(i, padTo(det[i], dw, lipgloss.NewStyle())) + side
	}

	var lines []string
	top := border.Render("╭") + titled("muxlair", lw)
	if dw > 0 {
		top += border.Render("┬") + titled("details", dw)
	}
	lines = append(lines, top+border.Render("╮"))
	lines = append(lines, side+strings.Repeat(" ", lw)+side+right(0)) // space under the titles
	head := fmt.Sprintf("    %-*s %5s  %-6s  %s", statusCol, "STATUS", "LAST", "AGENT", "SESSION")
	lines = append(lines, side+padTo(colHead.Render(head), lw, lipgloss.NewStyle())+side+right(1))
	rule := border.Render("├") + hline(lw)
	if dw > 0 {
		rule += border.Render("┤") + right(2)
	} else {
		rule += border.Render("┤")
	}
	lines = append(lines, rule)
	start, end := m.window()
	for i := 0; i < m.avail(); i++ {
		row := strings.Repeat(" ", lw)
		if start+i < end {
			row = m.renderRow(start+i, lw)
		}
		lines = append(lines, side+row+side+right(i+3))
	}
	bottom := border.Render("╰") + hline(lw)
	if dw > 0 {
		bottom += border.Render("┴") + hline(dw)
	}
	lines = append(lines, bottom+border.Render("╯"))

	if pop := m.popup(); pop != nil {
		x, y := popupOrigin(m.width, len(lines), pop)
		lines = overlayAt(lines, pop, x, y)
	}

	var foot string
	switch {
	// Repeat the popup's actions here: in a short terminal the popup can be clipped.
	case m.help:
		foot = dim.Render("↑↓ scroll · any other key closes")
	case m.settings != nil:
		foot = dim.Render("↑↓ move · space/←→ change · enter on Save · ctrl+s save · esc cancel")
	case m.confirm != nil:
		foot = dim.Render("y delete · n / esc cancel")
	case m.editing != nil:
		foot = dim.Render("enter save · esc cancel")
	case m.filtering:
		foot = "/" + m.filter + "█"
	case !m.resizedAt.IsZero():
		if _, dw := m.split(); dw > 0 && m.width > 0 {
			foot = fmt.Sprintf("details %d%% · %d columns", dw*100/m.width, dw)
		}
	case m.status != "":
		foot = m.status
	default:
		// Resize, fold and mouse keys are left to the ? popup, to keep this line short.
		foot = "enter open/focus · w cmux workspace · r rename · R refresh · i details · / filter · d delete · s settings · q quit"
		if m.filter != "" {
			foot = "filter: " + m.filter + " · " + foot
		}
		foot = "? keys · " + foot // first, so it survives truncation in a narrow window
		foot = dim.Render(foot)
	}
	return strings.Join(lines, "\n") + "\n" + m.center(m.fit(foot))
}

// center pads s on the left so it sits in the middle of the window.
func (m Model) center(s string) string {
	if pad := (m.width - ansi.StringWidth(s)) / 2; pad > 0 {
		return strings.Repeat(" ", pad) + s
	}
	return s
}

func (m Model) fit(s string) string {
	if m.width <= 0 {
		return s
	}
	return ansi.Truncate(s, m.width, "…")
}

// popup returns the rename or delete dialog, or nil.
func (m Model) popup() []string {
	w := m.popupWidth()
	in := w - 4
	switch {
	case m.help:
		body, room := m.helpBody(w), m.helpRoom()
		if len(body)+2 <= room {
			return box("Keys", w, append(body, "", dim.Render("any key closes")))
		}
		if len(body) <= room {
			return box("Keys", w, body) // fits, just not with the closing hint
		}
		// Too tall for the window: show the part ↑↓ have scrolled to.
		top := max(0, min(m.helpTop, len(body)-room))
		return box("Keys", w, body[top:top+room])
	case m.settings != nil:
		f := m.settings
		onOff := func(b bool) string {
			if b {
				return "[x] on"
			}
			return "[ ] off"
		}
		values := []string{onOff(f.opts.Mouse), onOff(f.opts.ColorTabs), onOff(f.opts.Details), "‹ " + config.FormatDuration(f.opts.AutoDelete) + " ›"}
		var body []string
		for i, name := range settingItems {
			l := fmt.Sprintf("%-24s %s", name, values[i])
			if i == f.cur {
				l = lipgloss.NewStyle().Background(selBg).Render(padTo("› "+l, in, lipgloss.NewStyle().Background(selBg)))
			} else {
				l = "  " + l
			}
			body = append(body, l)
		}
		button := func(label string, i int) string {
			if i == f.cur {
				return lipgloss.NewStyle().Background(selBg).Bold(true).Render("[ " + label + " ]")
			}
			return "[ " + label + " ]"
		}
		half := in / 2
		body = append(body, "", padTo("  "+button("Save", itemSave), half, lipgloss.NewStyle())+button("Cancel", itemCancel))
		if f.err != "" {
			body = append(body, "", errSty.Render(ansi.Truncate(f.err, in, "…")))
		}
		body = append(body, "", dim.Render("saved to "+tildify(m.cfgPath)))
		return box("Settings", w, body)
	case m.confirm != nil:
		s := m.confirm
		return box("Delete session", w, []string{
			"Move " + agentLabel(s.Agent) + " session",
			lipgloss.NewStyle().Bold(true).Render(ansi.Truncate(s.Name, in, "…")),
			"to ~/.muxlair/trash?",
			"",
			dim.Render("y delete · n / esc cancel"),
		})
	case m.editing != nil:
		// The field scrolls by terminal cells so the cursor, always at the end, stays visible.
		text := m.editBuf
		// room: field minus " ", "…", "█" and one spare cell, since a wide character can't be split.
		if room := in - 4; ansi.StringWidth(text) > room {
			text = ansi.TruncateLeft(text, ansi.StringWidth(text)-room, "…")
		}
		field := lipgloss.NewStyle().Background(selBg)
		return box("Rename session", w, []string{
			agentLabel(m.editing.Agent) + " session, renamed in the agent too",
			"",
			padTo(field.Render(" "+text+"█"), in, field),
			"",
			dim.Render("enter save · esc cancel"),
		})
	}
	return nil
}

func agentLabel(a string) string {
	return lipgloss.NewStyle().Foreground(agentColor[a]).Render(a)
}

// box draws a rounded, titled box w cells wide around body lines.
func box(title string, w int, body []string) []string {
	in := w - 4
	t := " " + banner.Render(title) + " "
	out := []string{popBorder.Render("╭─") + t + popBorder.Render(strings.Repeat("─", max(0, w-3-ansi.StringWidth(t)))+"╮")}
	for _, l := range body {
		out = append(out, popBorder.Render("│ ")+padTo(l, in, lipgloss.NewStyle())+popBorder.Render(" │"))
	}
	return append(out, popBorder.Render("╰"+strings.Repeat("─", w-2)+"╯"))
}

// helpBody is the keys list for a popup w cells wide; descriptions wrap under
// themselves so nothing is cut off in a narrow window.
func (m Model) helpBody(w int) []string {
	const keyCol = 13
	descW := max(8, w-4-keyCol-1)
	var body []string
	for _, k := range keyHelp {
		for i, l := range strings.Split(ansi.Wordwrap(k[1], descW, " "), "\n") {
			key := ""
			if i == 0 {
				key = k[0]
			}
			body = append(body, colHead.Render(fmt.Sprintf("%-*s", keyCol, key))+" "+l)
		}
	}
	return body
}

// helpMaxTop is the last valid help scroll position for the current size.
func (m Model) helpMaxTop() int {
	return max(0, len(m.helpBody(m.popupWidth()))-m.helpRoom())
}

// helpRoom is how many body lines the keys popup can show.
func (m Model) helpRoom() int {
	if m.height <= 0 {
		return len(keyHelp) * 3 // no size yet: no scrolling
	}
	return max(1, m.boxHeight()-2) // minus the popup's borders
}

// popupWidth is the width every popup uses.
func (m Model) popupWidth() int {
	if m.width <= 0 {
		return 60
	}
	return max(16, min(60, m.width-8)) // never wider than the terminal (tooSmall keeps width >= 44)
}

// popupOrigin is where a popup sits: centered over a bgLines-tall screen.
func popupOrigin(width, bgLines int, pop []string) (x, y int) {
	if width <= 0 {
		width = 80
	}
	return max(0, (width-ansi.StringWidth(pop[0]))/2), max(0, (bgLines-len(pop))/2)
}

// boxHeight is the number of lines View draws above the help line.
func (m Model) boxHeight() int { return m.avail() + chromeRows - 1 }

// overlayAt draws pop over bg at (x, y), keeping the background around it.
func overlayAt(bg, pop []string, x, y int) []string {
	pw := ansi.StringWidth(pop[0])
	out := append([]string(nil), bg...)
	for i, p := range pop {
		if y+i >= len(out) {
			break
		}
		l := out[y+i]
		out[y+i] = ansi.Truncate(l, x, "") + p + ansi.TruncateLeft(l, x+pw, "")
	}
	return out
}

// detailLines renders the details pane: n lines w cells wide for the selected row.
// detailsPane is the details pane's lines; the leading "" leaves a blank line
// under the "details" title. Line i is drawn on screen row i+1.
func (m Model) detailsPane(dw int) []string {
	return append([]string{""}, m.detailLines(dw, m.avail()+2)...)
}

// paneText is the details pane as plain text, to notice when it changes.
func (m Model) paneText() string {
	_, dw := m.split()
	return ansi.Strip(strings.Join(m.detailsPane(dw), "\n"))
}

// selected highlights the selected part of details line i (styles on that
// line are dropped while it is selected, to keep the highlight exact).
func (m Model) selected(i int, line string) string {
	if m.sel == nil {
		return line
	}
	s := m.sel.ordered()
	if i < s.y1 || i > s.y2 {
		return line
	}
	plain := ansi.Strip(line)
	w := ansi.StringWidth(plain)
	from, to := 0, w
	if i == s.y1 {
		from = min(s.x1, w)
	}
	if i == s.y2 {
		to = min(s.x2+1, w)
	}
	if from >= to {
		return plain
	}
	return ansi.Cut(plain, 0, from) + selStyle.Render(ansi.Cut(plain, from, to)) + ansi.Cut(plain, to, w)
}

// selectedText is the selection as plain text. A wrapped value (a long path)
// is joined back into one line: only the fixed continuation indent is dropped,
// and lines are cut at their real content, not the pane's padding, so spaces
// that belong to the value survive.
func (m Model) selectedText() string {
	_, dw := m.split()
	if m.sel == nil || dw == 0 {
		return ""
	}
	const labelW = 11 // " " + 9-cell label + " "
	s := m.sel.ordered()
	det := m.detailsPane(dw)
	var b strings.Builder
	for i := s.y1; i <= s.y2 && i < len(det); i++ {
		plain := ansi.Strip(det[i]) // as drawn (detailLines wraps, never cuts), without the padding
		w := ansi.StringWidth(plain)
		from, to := 0, w
		if i == s.y1 {
			from = min(s.x1, w)
		}
		if i == s.y2 {
			to = min(s.x2+1, w)
		}
		continuation := i > s.y1 && w > labelW && strings.TrimSpace(ansi.Cut(plain, 0, labelW)) == ""
		switch {
		case continuation:
			b.WriteString(ansi.Cut(plain, max(from, labelW), to))
		case i > s.y1:
			b.WriteString("\n" + ansi.Cut(plain, from, to))
		default:
			b.WriteString(ansi.Cut(plain, from, to))
		}
	}
	return strings.Trim(b.String(), "\n")
}

func (m Model) detailLines(w, n int) []string {
	lines := make([]string, n)
	if w == 0 {
		return lines
	}
	label := func(k, v string) string {
		return " " + colHead.Render(fmt.Sprintf("%-9s", k)) + " " + v
	}
	// pathLines shows an absolute path in full, wrapped under its label.
	pathLines := func(k, p string) []string {
		parts := strings.Split(ansi.Hardwrap(p, max(8, w-12), true), "\n")
		out := []string{label(k, parts[0])}
		for _, l := range parts[1:] {
			out = append(out, label("", l))
		}
		return out
	}
	var body []string
	r, ok := m.row()
	switch {
	case !ok:
	case r.s == nil:
		n := 0
		for _, s := range m.sessions {
			if s.Cwd == r.group {
				n++
			}
		}
		body = append(pathLines("Folder", r.group), label("Sessions", fmt.Sprint(n)))
	default:
		s := r.s
		body = []string{
			label("Agent", agentLabel(s.Agent)),
			// At most two lines: a Claude name can be a whole prompt, and an
			// unbounded one would push every other field out of the pane.
			label("Name", ansi.Truncate(s.Name, max(8, 2*(w-11)), "…")), // 11: the label column
			label("Session", s.ID),
			label("Status", statusWord(*s)+procInfo(*s)),
		}
		// Wrapped paths go last, so in a short pane they are what gets cut, not the metrics.
		d, ok := m.details[s.Key()]
		switch {
		case !ok:
			body = append(body, label("", dim.Render("loading…")))
			body = append(body, pathLines("Folder", s.Cwd)...)
		case d.err != "":
			body = append(body, label("", dim.Render(d.err)))
			body = append(body, pathLines("Folder", s.Cwd)...)
		default:
			na := dim.Render("not recorded")
			started, model, cost := na, na, na
			if !d.d.Started.IsZero() {
				started = d.d.Started.Local().Format("2006-01-02 15:04") + dim.Render(" ("+ago(d.d.Started)+" ago)")
			}
			if d.d.Model != "" {
				model = d.d.Model
			}
			if d.d.HasCost {
				cost = fmt.Sprintf("$%.2f", d.d.Cost)
			}
			tokens := humanCount(d.d.Total)
			if d.d.HasSplit {
				tokens += dim.Render(fmt.Sprintf(" (in %s · out %s · cache %s)",
					humanCount(d.d.Split.In), humanCount(d.d.Split.Out), humanCount(d.d.Split.Cache)))
			}
			body = append(body,
				label("Started", started),
				label("Activity", ago(s.Updated)+" ago"),
				label("Model", model),
				label("Tokens", tokens),
				label("Cost", cost),
			)
			body = append(body, pathLines("Folder", s.Cwd)...)
			body = append(body, pathLines("Location", d.d.Location)...)
		}
	}
	// Wrap any other line too wide for the pane (a long name, a session ID in a
	// narrow pane) under its label instead of cutting it, so what is shown, and
	// what a selection copies, is always the whole value.
	var wrapped []string
	for _, l := range body {
		if ansi.StringWidth(l) <= w {
			wrapped = append(wrapped, l)
			continue
		}
		// First w cells stay on the label's line; the rest wraps under the value.
		wrapped = append(wrapped, ansi.Truncate(l, w, ""))
		rest := ansi.Cut(l, w, ansi.StringWidth(l))
		for _, r := range strings.Split(ansi.Hardwrap(rest, max(8, w-11), true), "\n") {
			wrapped = append(wrapped, strings.Repeat(" ", 11)+r)
		}
	}
	for i := range lines {
		if i < len(wrapped) {
			lines[i] = wrapped[i]
		}
	}
	return lines
}

func procInfo(s agents.Session) string {
	switch {
	case s.PID != 0 && s.TTY != "":
		return dim.Render(fmt.Sprintf(" (pid %d, %s)", s.PID, s.TTY))
	case s.PID != 0:
		return dim.Render(fmt.Sprintf(" (pid %d)", s.PID))
	}
	return ""
}

// humanCount formats 23_700_000 as "23.7M".
func humanCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

// statusWord is the STATUS column text; it also explains what the dot colors mean.
func statusWord(s agents.Session) string {
	switch {
	case s.Dead:
		return "dead"
	case s.MaybeLive:
		return "in use?"
	}
	return map[agents.Status]string{agents.Busy: "busy", agents.Idle: "idle", agents.Exited: "exited"}[s.Status]
}

// renderRow draws row i exactly w cells wide. Every segment carries the selection
// background itself, since an inner ANSI reset would end an outer one early.
func (m Model) renderRow(i, w int) string {
	r := m.rows[i]
	if r.spacer {
		return strings.Repeat(" ", w)
	}
	sel := i == m.cur
	st := func(s lipgloss.Style) lipgloss.Style {
		if sel {
			return s.Background(selBg)
		}
		return s
	}
	plain := lipgloss.NewStyle()
	gutter := "  "
	if sel {
		gutter = "› "
	}
	parts := []string{st(cursor).Render(gutter)}
	if r.s == nil {
		arrow := "▾"
		if m.collapsed[r.group] {
			arrow = "▸"
		}
		parts = append(parts, st(header).Render(arrow+" "+m.labels[r.group]),
			st(dim).Render(fmt.Sprintf(" (%d)", m.counts[r.group])))
	} else {
		s := r.s
		dot, dotColor := "●", statusColor[s.Status]
		switch {
		case s.Dead:
			dot = "✕" // agent can't resume it any more
		case s.MaybeLive:
			dot, dotColor = "○", statusColor[agents.Idle] // open/delete blocked
		}
		parts = append(parts,
			st(plain).Render("  "),
			st(plain.Foreground(dotColor)).Render(dot),
			st(plain.Foreground(dotColor)).Render(fmt.Sprintf(" %-*s", statusCol-2, statusWord(*s))),
			st(plain).Render(fmt.Sprintf(" %5s  ", ago(s.Updated))),
			// Not bold: bold orange "claude" read as bigger than "codex".
			st(plain.Foreground(agentColor[s.Agent])).Render(fmt.Sprintf("%-6s", s.Agent)),
			st(plain).Render("  "+s.Name),
		)
	}
	return padTo(strings.Join(parts, ""), w, st(plain))
}

// padTo truncates or pads s to exactly w cells, padding with style pad.
func padTo(s string, w int, pad lipgloss.Style) string {
	s = ansi.Truncate(s, w, "…")
	if n := w - ansi.StringWidth(s); n > 0 {
		s += pad.Render(strings.Repeat(" ", n))
	}
	return s
}

// ago formats time since t in minutes, switching to h/d once minutes get unwieldy.
func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func tildify(p string) string {
	if h := agents.Home; h != "" && strings.HasPrefix(p, h) {
		return "~" + p[len(h):]
	}
	return p
}
