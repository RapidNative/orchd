//go:build !unix

package runtime

import (
	"os"
	"os/exec"
	"syscall"
)

// Non-unix fallback: no process groups, so a workload's grandchildren are not
// reachable. The local driver is a macOS/Linux dev convenience; the docker
// driver is the supported path elsewhere.

func setProcAttr(*exec.Cmd) {}

func signalTree(pid int, _ syscall.Signal) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

func processAlive(pid int) bool {
	_, err := os.FindProcess(pid)
	return pid > 0 && err == nil
}
