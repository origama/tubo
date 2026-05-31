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

func processCommandLine(pid int) ([]string, bool) {
	if pid <= 0 {
		return nil, false
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || len(b) == 0 {
		return nil, false
	}
	parts := strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
	if len(parts) == 0 || (len(parts) == 1 && parts[0] == "") {
		return nil, false
	}
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}
