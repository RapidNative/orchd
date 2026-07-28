//go:build unix

package runtime

import (
	"os/exec"
	"syscall"
)

// Local workloads spawn children of their own — tinbase starts an embedded
// postgres, Metro starts jest workers. Signalling only the direct child leaves
// those behind, holding ports and the postgres lock file, which then breaks the
// next boot. Putting each workload in its own process group lets us take the
// whole tree down at once.

func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// signalTree sends sig to the process group led by pid, falling back to the
// single process if it leads no group (already reaped, or never grouped).
func signalTree(pid int, sig syscall.Signal) error {
	if pgid, err := syscall.Getpgid(pid); err == nil && pgid == pid {
		return syscall.Kill(-pgid, sig)
	}
	return syscall.Kill(pid, sig)
}

// processAlive reports whether pid exists (signal 0 probes without delivering).
func processAlive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}
