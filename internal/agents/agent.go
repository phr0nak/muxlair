package agents

// Agent is one coding agent muxlair supports. Adding an agent means writing one
// of these and listing it in registry; the UI only talks to agents through it.
type Agent interface {
	Name() string  // Session.Agent value, e.g. "claude"
	Title() string // for messages, e.g. "Claude"
	List() ([]Session, error)
	// WatchPaths are the files and folders whose changes should refresh the list.
	WatchPaths() []string
	// Details reads the details pane data; peek also reads the latest exchange.
	Details(s Session, peek bool) (Details, error)
	// Rename renames s through the agent's own supported interface, without its
	// terminal. RenameRunning reports whether that also works while s runs; the
	// running agent may then keep showing the old name until told in its terminal.
	Rename(s Session, name string) error
	RenameRunning() bool
	// RenameLine is what to type in a running session's terminal to rename it;
	// "" means Rename alone also renames a running session, nothing to type.
	RenameLine(name string) string
}

var registry = []Agent{claudeAgent{}, codexAgent{}, opencodeAgent{}}

// ByName returns the agent whose Name is name.
func ByName(name string) (Agent, bool) {
	for _, a := range registry {
		if a.Name() == name {
			return a, true
		}
	}
	return nil, false
}

type claudeAgent struct{}

func (claudeAgent) Name() string               { return "claude" }
func (claudeAgent) Title() string              { return "Claude" }
func (claudeAgent) List() ([]Session, error)   { return listClaude() }
func (claudeAgent) RenameRunning() bool        { return false } // a running Claude overwrites it from memory
func (claudeAgent) RenameLine(n string) string { return "/rename " + n }
func (claudeAgent) Rename(s Session, n string) error {
	return RenameClaudeTranscript(s, n)
}
func (claudeAgent) Details(s Session, peek bool) (Details, error) {
	if len(s.Files) == 0 {
		return Details{}, errNoTranscript
	}
	return claudeDetails(s.Files[0], peek)
}
func (claudeAgent) WatchPaths() []string { return claudeWatchPaths() }

type codexAgent struct{}

func (codexAgent) Name() string                                  { return "codex" }
func (codexAgent) Title() string                                 { return "Codex" }
func (codexAgent) List() ([]Session, error)                      { return listCodex() }
func (codexAgent) RenameRunning() bool                           { return true } // the app server renames it either way
func (codexAgent) RenameLine(n string) string                    { return "/rename " + n }
func (codexAgent) Rename(s Session, n string) error              { return RenameCodex(s.ID, n) }
func (codexAgent) Details(s Session, peek bool) (Details, error) { return codexDetails(s, peek) }
func (codexAgent) WatchPaths() []string                          { return []string{CodexHome()} }
