package agents

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode"
)

// CleanName validates a user-typed session name.
func CleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("name is empty")
	}
	for _, r := range name {
		// Backslash too: cmux send turns \n into Enter, which would submit mid-command.
		if unicode.IsControl(r) || r == '\\' {
			return "", errors.New("name has control characters or a backslash")
		}
	}
	return name, nil
}

// RenameClaudeTranscript renames an exited Claude session by appending the same
// custom-title record /rename writes. A running Claude would overwrite it from
// memory, so live sessions must be renamed through /rename in their terminal.
func RenameClaudeTranscript(s Session, name string) error {
	if s.PID != 0 && alive(s.PID) {
		return errors.New("session is running; rename it with /rename in its terminal")
	}
	if len(s.Files) == 0 {
		return errors.New("no transcript found")
	}
	b, err := json.Marshal(map[string]string{"type": "custom-title", "customTitle": name, "sessionId": s.ID})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.Files[0], os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// RenameCodex sets a thread name through the codex app server's thread/name/set,
// the same call the codex UI uses, so muxlair never writes codex's database.
func RenameCodex(id, name string) error {
	cmd := exec.Command("codex", "app-server")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() { stdin.Close(); cmd.Process.Kill(); cmd.Wait() }()

	reqs := []any{
		map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]string{"name": "muxlair", "version": "0.1"}}},
		map[string]any{"method": "initialized"},
		map[string]any{"id": 2, "method": "thread/name/set", "params": map[string]string{"threadId": id, "name": name}},
	}
	enc := json.NewEncoder(stdin)
	for _, r := range reqs {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	done := make(chan error, 1)
	go func() { done <- awaitRPC(stdout, 2) }()
	select {
	case err := <-done:
		return err
	case <-time.After(15 * time.Second):
		return errors.New("codex app-server: timed out")
	}
}

// awaitRPC reads JSON-RPC lines until the response with the given id.
func awaitRPC(r io.Reader, id int) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var msg struct {
			ID    *int `json:"id"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) != nil || msg.ID == nil || *msg.ID != id {
			continue
		}
		if msg.Error != nil {
			return fmt.Errorf("codex: %s", msg.Error.Message)
		}
		return nil
	}
	return errors.New("codex app-server exited without answering")
}
