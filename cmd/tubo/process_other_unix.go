//go:build !windows && !linux && !darwin

package main

import (
	"errors"
	"syscall"
)

func pidRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func terminatePID(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

func killPID(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}

func processCommandLine(pid int) ([]string, bool) {
	return nil, false
}
