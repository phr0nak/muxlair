package agents

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

const sZomb = 5 // SZOMB in <sys/proc.h>

// ownProcs lists the current user's processes from the kernel (sysctl): about
// 0.3ms, where ps takes ~100ms and made every refresh slow.
func ownProcs() ([]proc, error) {
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.uid", os.Getuid())
	if err != nil {
		return nil, err
	}
	out := make([]proc, 0, len(kps))
	for _, k := range kps {
		if k.Proc.P_stat == sZomb {
			continue // defunct: holds no files and runs nothing, but lsof can't inspect it
		}
		p := proc{
			pid:     int(k.Proc.P_pid),
			started: time.Unix(k.Proc.P_starttime.Sec, int64(k.Proc.P_starttime.Usec)*1000),
			exe:     unix.ByteSliceToString(k.Proc.P_comm[:]),
		}
		if d := k.Eproc.Tdev; d != -1 {
			p.tty = fmt.Sprintf("ttys%03d", d&0xffffff) // minor number of the pty
		}
		out = append(out, p)
	}
	return out, nil
}

// procArgs returns argv of pid, or nil if it can't be read.
func procArgs(pid int) []string {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(b) < 4 {
		return nil
	}
	return parseProcArgs(b)
}

// parseProcArgs decodes kern.procargs2: int32 argc, exec path, NUL padding, then argv.
func parseProcArgs(b []byte) []string {
	argc := int(binary.LittleEndian.Uint32(b[:4]))
	b = b[4:]
	i := bytes.IndexByte(b, 0)
	if i < 0 {
		return nil
	}
	b = bytes.TrimLeft(b[i:], "\x00")
	var args []string
	for len(args) < argc && len(b) > 0 {
		j := bytes.IndexByte(b, 0)
		if j < 0 {
			j = len(b)
		}
		args = append(args, string(b[:j]))
		b = b[min(len(b), j+1):]
	}
	return args
}
