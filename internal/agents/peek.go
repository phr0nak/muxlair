package agents

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"unicode/utf8"
)

// peekMax caps each stored text (runes), so a pasted log can't fill memory. The
// popup shows up to this; the details pane cuts it further to fit.
const peekMax = 20000

// Peek is the latest exchange of a conversation: the last thing the user asked
// and the agent's last answer to it. It is never a summary, and it never holds
// tool calls, tool output or reasoning.
type Peek struct {
	Prompt string
	Reply  string
	// Done is true when Reply is a finished answer; false means the agent is
	// still working, or stopped partway (an interrupt, or a tool call after it).
	Done bool
}

// Empty reports whether there is nothing to show.
func (p Peek) Empty() bool { return p.Prompt == "" }

// peekText makes saved conversation text safe to print: control characters and
// bidirectional controls are dropped, tabs become spaces, runs of blank lines
// collapse, and the result is cut to peekMax runes with an ellipsis.
func peekText(s string) string {
	// Cut a huge text before touching all of it (a pasted log can be megabytes);
	// 4 bytes a rune is the most the kept part can need.
	cut := len(s) > 4*peekMax
	if cut {
		n := 4 * peekMax
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		s = s[:n]
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n':
			return r
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			return -1
		case r == 0x061c || r == 0x200e || r == 0x200f || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069):
			return -1 // direction controls can reorder what the terminal shows
		}
		return r
	}, s)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	s = strings.Join(lines, "\n")
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	s = strings.TrimSpace(s)
	if cut || utf8.RuneCountInString(s) > peekMax {
		r := []rune(s)
		s = string(r[:min(len(r), peekMax-1)]) + "…"
	}
	return s
}

// claudeWrappers start a user record that is Claude's own bookkeeping (slash
// commands and their output, shell passthrough, notifications, interrupts), not
// something the user asked.
var claudeWrappers = []string{
	"<command-", "<local-command-", "<bash-", "<task-notification", "<system-reminder>",
	"[Request interrupted", "Caveat:",
}

func isClaudeWrapper(s string) bool {
	for _, w := range claudeWrappers {
		if strings.HasPrefix(s, w) {
			return true
		}
	}
	return false
}

// claudeUser updates p from a user record line, if it is a human prompt.
func (p *Peek) claudeUser(line []byte) {
	var r struct {
		IsMeta           bool `json:"isMeta"`
		IsSidechain      bool `json:"isSidechain"`
		IsCompactSummary bool `json:"isCompactSummary"`
		Message          struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &r) != nil || r.IsMeta || r.IsSidechain || r.IsCompactSummary {
		return
	}
	text, ok := claudePromptText(r.Message.Content)
	if !ok {
		return
	}
	*p = Peek{Prompt: peekText(text)}
}

// claudePromptText returns the text of a human prompt: a string, or an array of
// text and image blocks. An array with a tool_result is the answer to a tool call.
func claudePromptText(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "", false
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil || strings.TrimSpace(s) == "" {
			return "", false
		}
		if isClaudeWrapper(s) {
			return "", false
		}
		return s, true
	}
	var items []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &items) != nil {
		return "", false
	}
	var parts []string
	for _, it := range items {
		switch it.Type {
		case "tool_result":
			return "", false
		case "text":
			if strings.TrimSpace(it.Text) != "" && !isClaudeWrapper(it.Text) {
				parts = append(parts, it.Text)
			}
		case "image":
			parts = append(parts, "[image]")
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, "\n"), true
}

// claudeAssistant updates p from an assistant record's content blocks: the text
// blocks of the record are the reply so far (a later record with text replaces
// them). A record with no text (a tool call, thinking) leaves it alone.
// shortcut: Claude writes one record per content block, so text split over
// several records is not joined; upgrade if a reply shows up cut in two.
func (p *Peek) claudeAssistant(content json.RawMessage, stopReason string) {
	var items []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &items) != nil || p.Prompt == "" {
		return
	}
	var parts []string
	for _, it := range items {
		if it.Type == "text" && strings.TrimSpace(it.Text) != "" {
			parts = append(parts, it.Text)
		}
	}
	if len(parts) == 0 {
		return
	}
	p.Reply = peekText(strings.Join(parts, "\n\n"))
	p.Done = stopReason == "end_turn" || stopReason == "stop_sequence"
}

// codexPeek reads the latest exchange from a Codex rollout. Codex 0.166 logs
// item_completed events (UserMessage, AgentMessage with a phase) and a
// task_complete with the final message.
// shortcut: the legacy user_message / agent_message events are handled from
// memory of older rollouts and are only tested on synthetic data, and a session
// whose rollout is gone (dead) has no peek; upgrade when real old rollouts or the
// saved thread_items are worth reading.
func codexPeek(path string) Peek {
	f, err := os.Open(path)
	if err != nil {
		return Peek{}
	}
	defer f.Close()
	var p Peek
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.Contains(line, []byte(`"event_msg"`)) {
			continue
		}
		var r struct {
			Payload struct {
				Type             string `json:"type"`
				Message          string `json:"message"`
				LastAgentMessage string `json:"last_agent_message"`
				Item             struct {
					Type    string `json:"type"`
					Phase   string `json:"phase"`
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"item"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &r) != nil {
			continue
		}
		pl := r.Payload
		switch {
		case pl.Type == "item_completed" && pl.Item.Type == "UserMessage":
			if t := codexText(pl.Item.Content); t != "" {
				p = Peek{Prompt: peekText(t)}
			}
		case pl.Type == "item_completed" && pl.Item.Type == "AgentMessage" && p.Prompt != "":
			t := codexText(pl.Item.Content)
			if t == "" {
				continue
			}
			if pl.Item.Phase == "commentary" {
				if !p.Done { // commentary only stands in until the final answer
					p.Reply = peekText(t)
				}
			} else { // final_answer, or no phase
				p.Reply, p.Done = peekText(t), true
			}
		case pl.Type == "task_complete" && pl.LastAgentMessage != "" && p.Prompt != "":
			p.Reply, p.Done = peekText(pl.LastAgentMessage), true
		case pl.Type == "user_message" && strings.TrimSpace(pl.Message) != "": // legacy
			p = Peek{Prompt: peekText(pl.Message)}
		case pl.Type == "agent_message" && strings.TrimSpace(pl.Message) != "" && p.Prompt != "": // legacy
			p.Reply, p.Done = peekText(pl.Message), true
		}
	}
	return p
}

// codexText joins the text blocks of a Codex message; other blocks (images) are
// shown as [image] for a user message and ignored otherwise.
func codexText(content []struct {
	Type string `json:"type"`
	Text string `json:"text"`
}) string {
	var parts []string
	for _, c := range content {
		switch {
		case strings.TrimSpace(c.Text) != "":
			parts = append(parts, c.Text)
		case strings.Contains(strings.ToLower(c.Type), "image"):
			parts = append(parts, "[image]")
		}
	}
	return strings.Join(parts, "\n")
}
