package agents

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// TrashDir is where deleted sessions go.
func TrashDir() string { return filepath.Join(Home, ".muxlair", "trash") }

type trashMeta struct {
	Agent     string            `json:"agent"`
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Cwd       string            `json:"cwd"`
	DeletedAt time.Time         `json:"deletedAt"`
	Files     map[string]string `json:"files"` // trash name -> original path
}

// Trash moves a session's files to ~/.muxlair/trash/<agent>/<ts>-<id>/.
// Running sessions are refused: their agent would keep writing to the moved files.
func Trash(s Session) (string, error) {
	if s.PID != 0 && alive(s.PID) {
		return "", errors.New("session is running; exit it first")
	}
	if s.MaybeLive {
		return "", errors.New("an unidentified " + s.Agent + " process in this folder may be using it; exit it first")
	}
	// Codex can keep history only in sqlite, so it may have no files.
	if len(s.Files) == 0 && s.Agent != "codex" {
		return "", errors.New("no files found for session")
	}
	dir := filepath.Join(TrashDir(), s.Agent, time.Now().Format("20060102-150405")+"-"+s.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	meta := trashMeta{Agent: s.Agent, ID: s.ID, Name: s.Name, Cwd: s.Cwd, DeletedAt: time.Now(), Files: map[string]string{}}
	for i, src := range s.Files {
		name := fmt.Sprintf("%d-%s", i, filepath.Base(src))
		meta.Files[name] = src
	}
	// Write metadata first so a partial move is still recoverable.
	if err := writeMeta(dir, meta); err != nil {
		return "", err
	}
	switch s.Agent {
	case "codex":
		// Copy, then let codex delete its own rollout + DB row, so we never write its sqlite.
		for name, src := range meta.Files {
			if err := copyFile(src, filepath.Join(dir, name)); err != nil {
				return dir, err
			}
		}
		if err := exportCodexThread(filepath.Join(dir, "history.jsonl"), s.ID); err != nil {
			return dir, fmt.Errorf("backup codex history: %w", err)
		}
		// Hidden helpers (e.g. a review's reviewer) go too, or they'd linger unseen.
		children, err := codexHelpersToDelete(s.ID)
		if err != nil {
			return dir, fmt.Errorf("find codex helper threads: %w", err)
		}
		// A helper resumed on its own would have its rollout open; check all before deleting any.
		var helperRollouts []string
		for _, c := range children {
			if r := codexRollout(c); r != "" {
				helperRollouts = append(helperRollouts, r)
			}
		}
		if open, err := codexThreadsLive(children, helperRollouts); err != nil || open {
			if err == nil {
				err = errors.New("a helper thread of this session is still running")
			}
			return dir, err
		}
		for _, c := range children {
			if err := exportCodexThread(filepath.Join(dir, "history-"+c+".jsonl"), c); err != nil {
				return dir, fmt.Errorf("backup codex helper %s: %w", c, err)
			}
			if r := codexRollout(c); r != "" {
				if err := copyFile(r, filepath.Join(dir, "helper-"+filepath.Base(r))); err != nil {
					return dir, fmt.Errorf("backup codex helper %s: %w", c, err)
				}
			}
		}
		// Helpers first: if one fails, the parent stays listed so the delete can be retried.
		for _, id := range append(children, s.ID) {
			if out, err := exec.Command("codex", "delete", "--force", id).CombinedOutput(); err != nil {
				return dir, fmt.Errorf("codex delete %s: %v: %s", id, err, out)
			}
		}
	default:
		for name, src := range meta.Files {
			if err := os.Rename(src, filepath.Join(dir, name)); err != nil {
				return dir, err
			}
		}
	}
	return dir, nil
}

func writeMeta(dir string, m trashMeta) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "meta.json"), b, 0o600)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
