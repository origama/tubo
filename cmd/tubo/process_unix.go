//go:build !windows

package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

func pidRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err != nil && err != syscall.EPERM {
		return false
	}
	if b, readErr := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); readErr == nil {
		parts := strings.Split(string(b), " ")
		if len(parts) > 2 && parts[2] == "Z" {
			return false
		}
	}
	return true
}

func terminatePID(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

func killPID(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}
