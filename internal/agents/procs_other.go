//go:build !darwin

package agents

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// ownProcs lists the current user's processes with ps (portable fallback).
func ownProcs() ([]proc, error) {
	out, err := exec.Command("ps", "-x", "-U", strconv.Itoa(os.Getuid()), "-o", "pid=,lstart=,tty=,comm=").Output()
	if err != nil {
		return nil, err
	}
	var procs []proc
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 8 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		tty, started, ok := parsePS(strings.Join(f[1:7], " "))
		if !ok {
			continue
		}
		procs = append(procs, proc{pid: pid, tty: tty, started: started, exe: filepath.Base(strings.Join(f[7:], " "))})
	}
	return procs, nil
}

func procArgs(pid int) []string {
	out, err := exec.Command("ps", "-o", "args=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return nil
	}
	return strings.Fields(string(out))
}
