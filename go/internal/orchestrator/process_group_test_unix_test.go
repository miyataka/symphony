//go:build unix

package orchestrator

import (
	"errors"
	"syscall"
)

func supportsProcessGroupTests() bool {
	return true
}

func cleanupProcess(pid int) {
	if pid > 0 {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

func processExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
