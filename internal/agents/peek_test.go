package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPeekText(t *testing.T) {
	got := peekText("  a\tb\x1b[31m red‮ rtl  \r\n\r\n\r\n\r\nnext\x07  ")
	if got != "a b[31m red rtl\n\nnext" {
		t.Fatalf("got %q", got)
	}
	long := peekText(strings.Repeat("é", peekMax+50))
	if r := []rune(long); len(r) != peekMax || r[len(r)-1] != '…' {
		t.Fatalf("not capped: %d runes", len(r))
	}
	// A multi-megabyte text is cut early and still ends in an ellipsis.
	huge := peekText(strings.Repeat("x", 8<<20))
	if r := []rune(huge); len(r) != peekMax || r[len(r)-1] != '…' {
		t.Fatalf("huge text: %d runes", len(r))
	}
}

// jsonl writes records, one per line.
func jsonl(t *testing.T, recs ...map[string]any) string {
	t.Helper()
	var b strings.Builder
	for _, r := range recs {
		j, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(j)
		b.WriteByte('\n')
	}
	p := filepath.Join(t.TempDir(), "x.jsonl")
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func cUser(content any, extra ...string) map[string]any {
	r := map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": content}}
	for _, e := range extra {
		r[e] = true
	}
	return r
}

func cAsst(stop string, blocks ...map[string]any) map[string]any {
	var stopReason any
	if stop != "" {
		stopReason = stop
	}
	return map[string]any{"type": "assistant", "requestId": "r", "message": map[string]any{
		"model": "m", "stop_reason": stopReason, "content": blocks, "usage": map[string]any{}}}
}

func text(s string) map[string]any { return map[string]any{"type": "text", "text": s} }

func TestClaudePeek(t *testing.T) {
	toolUse := map[string]any{"type": "tool_use", "id": "t1", "name": "Bash", "input": map[string]any{}}
	toolResult := []any{map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": strings.Repeat("x", 1<<20)}}
	p := jsonl(t,
		cUser("<system-reminder>injected</system-reminder>", "isMeta"),
		cUser("fix the bug\x1b[2J please"),
		cAsst("", text("Let me look")),
		cAsst("tool_use", toolUse),
		cUser(toolResult),
		cAsst("end_turn", text("Fixed it."), text("All done.")),
		cUser("<command-name>/model</command-name>"),
		cUser("[Request interrupted by user]"),
		cUser("side prompt", "isSidechain"),
		cUser("summary of the past", "isCompactSummary"),
		cUser([]any{text("<system-reminder>context as a block</system-reminder>")}), // wrappers in array form too
	)
	d, err := parseClaudeDetails(p, true)
	if err != nil {
		t.Fatal(err)
	}
	if d.Peek.Prompt != "fix the bug[2J please" || d.Peek.Reply != "Fixed it.\n\nAll done." || !d.Peek.Done {
		t.Fatalf("peek %+v", d.Peek)
	}

	// Off: nothing is read.
	if d, _ = parseClaudeDetails(p, false); !d.Peek.Empty() {
		t.Fatalf("peek read while off: %+v", d.Peek)
	}

	// A wrapper block beside real text is dropped; the text is the prompt.
	p = jsonl(t, cUser([]any{text("<system-reminder>ctx</system-reminder>"), text("the real question")}))
	if d, _ = parseClaudeDetails(p, true); d.Peek.Prompt != "the real question" {
		t.Fatalf("mixed blocks: %+v", d.Peek)
	}

	// A newer prompt replaces the exchange and clears the reply; an image-only prompt shows [image].
	p = jsonl(t,
		cUser("first"), cAsst("end_turn", text("answer")),
		cUser([]any{map[string]any{"type": "image", "source": map[string]any{}}}),
	)
	if d, _ = parseClaudeDetails(p, true); d.Peek.Prompt != "[image]" || d.Peek.Reply != "" || d.Peek.Done {
		t.Fatalf("new prompt: %+v", d.Peek)
	}

	// Stopped partway: text, then a tool call, no final answer. Synthetic replies and sidechains are not replies.
	synth := cAsst("end_turn", text("No response requested."))
	synth["message"].(map[string]any)["model"] = "<synthetic>"
	side := cAsst("end_turn", text("subagent answer"))
	side["isSidechain"] = true
	p = jsonl(t, cUser("do it"), cAsst("tool_use", text("Starting"), toolUse), synth, side)
	if d, _ = parseClaudeDetails(p, true); d.Peek.Prompt != "do it" || d.Peek.Reply != "Starting" || d.Peek.Done {
		t.Fatalf("partial: %+v", d.Peek)
	}

	// A half-written last line is skipped, not fatal.
	p = jsonl(t, cUser("hello"), cAsst("end_turn", text("hi")))
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"type":"user","message":{"content":"half`)
	f.Close()
	if d, _ = parseClaudeDetails(p, true); d.Peek.Prompt != "hello" || d.Peek.Reply != "hi" {
		t.Fatalf("half line: %+v", d.Peek)
	}
}

// The details cache keeps the peek apart, so turning it off never serves text.
func TestClaudeDetailsCacheKeepsPeekApart(t *testing.T) {
	p := jsonl(t, cUser("secret question"), cAsst("end_turn", text("answer")))
	with, _ := claudeDetails(p, true)
	without, _ := claudeDetails(p, false)
	if with.Peek.Prompt == "" || !without.Peek.Empty() {
		t.Fatalf("with %+v without %+v", with.Peek, without.Peek)
	}
}

func cx(payload map[string]any) map[string]any {
	return map[string]any{"type": "event_msg", "payload": payload}
}

func cxItem(kind, phase, txt string) map[string]any {
	item := map[string]any{"type": kind, "content": []any{map[string]any{"type": "Text", "text": txt}}}
	if phase != "" {
		item["phase"] = phase
	}
	return cx(map[string]any{"type": "item_completed", "item": item})
}

func TestCodexPeek(t *testing.T) {
	p := jsonl(t,
		map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "user"}}, // injected context: ignored
		cxItem("UserMessage", "", "first question"),
		cxItem("AgentMessage", "final_answer", "first answer"),
		cxItem("UserMessage", "", "second question"),
		cxItem("AgentMessage", "commentary", "looking"),
		cxItem("CommandExecution", "", "ls"),
	)
	if got := codexPeek(p); got.Prompt != "second question" || got.Reply != "looking" || got.Done {
		t.Fatalf("commentary: %+v", got)
	}
	p = jsonl(t,
		cxItem("UserMessage", "", "q"),
		cxItem("AgentMessage", "final_answer", "the answer"),
		cxItem("AgentMessage", "commentary", "late note"), // never replaces a final answer
	)
	if got := codexPeek(p); got.Reply != "the answer" || !got.Done {
		t.Fatalf("final: %+v", got)
	}
	p = jsonl(t, cxItem("UserMessage", "", "q"), cxItem("AgentMessage", "", "no phase"))
	if got := codexPeek(p); got.Reply != "no phase" || !got.Done {
		t.Fatalf("no phase: %+v", got)
	}
	p = jsonl(t, cxItem("UserMessage", "", "q"), cxItem("AgentMessage", "commentary", "c"),
		cx(map[string]any{"type": "task_complete", "last_agent_message": "final from task_complete"}))
	if got := codexPeek(p); got.Reply != "final from task_complete" || !got.Done {
		t.Fatalf("task_complete: %+v", got)
	}
	// Legacy event names (synthetic: not seen on real data).
	p = jsonl(t, cx(map[string]any{"type": "user_message", "message": "old q"}), cx(map[string]any{"type": "agent_message", "message": "old a"}))
	if got := codexPeek(p); got.Prompt != "old q" || got.Reply != "old a" || !got.Done {
		t.Fatalf("legacy: %+v", got)
	}
	if got := codexPeek(filepath.Join(t.TempDir(), "missing.jsonl")); !got.Empty() {
		t.Fatalf("missing file: %+v", got)
	}
}

// Turning the peek off forgets the cached text, not just hides it.
func TestDropPeekCache(t *testing.T) {
	p := jsonl(t, cUser("q"), cAsst("end_turn", text("a")))
	claudeDetails(p, true)
	detailsMu.Lock()
	_, had := detailsCache[p+"\x00peek"]
	detailsMu.Unlock()
	DropPeekCache()
	detailsMu.Lock()
	_, still := detailsCache[p+"\x00peek"]
	detailsMu.Unlock()
	if !had || still {
		t.Fatalf("had=%v still=%v", had, still)
	}
}

// A peek parse that started before the cache was dropped must not put its text back.
func TestDropPeekCacheInvalidatesRunningLoad(t *testing.T) {
	p := jsonl(t, cUser("q"), cAsst("end_turn", text("a")))
	detailsMu.Lock()
	gen := peekGen
	detailsMu.Unlock()
	DropPeekCache() // the peek is turned off while a load that began at gen is still parsing
	detailsMu.Lock()
	changed := peekGen != gen
	detailsMu.Unlock()
	if !changed {
		t.Fatal("dropping the cache did not change the generation")
	}
	claudeDetails(p, true) // a fresh load after the drop is cached again (the peek is back on)
	detailsMu.Lock()
	_, ok := detailsCache[p+"\x00peek"]
	detailsMu.Unlock()
	if !ok {
		t.Fatal("a load after the drop was not cached")
	}
}
