// Package clip copies text to the system clipboard.
package clip

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Copy puts text on the clipboard and says how. It uses the platform's
// clipboard command when there is one; otherwise it asks the terminal through
// OSC 52, which works over SSH too when the terminal (and tmux, with
// set-clipboard on) allow it.
func Copy(text string) (string, error) {
	for _, c := range commands() {
		if _, err := exec.LookPath(c[0]); err != nil {
			continue
		}
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err == nil {
			return c[0], nil
		}
	}
	return "terminal", osc52(text)
}

// commands are the clipboard tools to try, in order.
func commands() [][]string {
	cmds := [][]string{{"pbcopy"}}
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		cmds = append(cmds, []string{"wl-copy"})
	}
	if os.Getenv("DISPLAY") != "" {
		cmds = append(cmds, []string{"xclip", "-selection", "clipboard"}, []string{"xsel", "--clipboard", "--input"})
	}
	return cmds
}

// osc52 writes the clipboard escape straight to the terminal device, so it
// doesn't interleave with the UI's own output on stdout.
func osc52(text string) error {
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return errors.New("no clipboard tool found and no terminal to ask")
	}
	defer tty.Close()
	_, err = fmt.Fprintf(tty, "\x1b]52;c;%s\a", base64.StdEncoding.EncodeToString([]byte(text)))
	return err
}
