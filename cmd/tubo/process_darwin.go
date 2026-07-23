//go:build darwin

package main

import (
	"encoding/binary"
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

const darwinZombieState = 5 // SZOMB from <sys/proc.h>.

func pidRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err == nil {
		return info.Proc.P_stat != darwinZombieState
	}
	// kern.proc.pid may be denied for processes owned by another user. Preserve
	// kill(2)'s EPERM-as-alive semantics without treating ESRCH as live.
	err = syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func terminatePID(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

func killPID(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}

func processCommandLine(pid int) ([]string, bool) {
	return readDarwinCommandLine(pid, unix.SysctlRaw)
}

func readDarwinCommandLine(pid int, read func(string, ...int) ([]byte, error)) ([]string, bool) {
	if pid <= 0 {
		return nil, false
	}
	raw, err := read("kern.procargs2", pid)
	if err != nil {
		return nil, false
	}
	return parseDarwinProcArgs(raw)
}

func parseDarwinProcArgs(raw []byte) ([]string, bool) {
	if len(raw) < 4 {
		return nil, false
	}
	argc := int(binary.LittleEndian.Uint32(raw[:4]))
	if argc <= 0 {
		return nil, false
	}
	pos := 4
	// First string is executable path supplied separately by KERN_PROCARGS2.
	for pos < len(raw) && raw[pos] != 0 {
		pos++
	}
	for pos < len(raw) && raw[pos] == 0 {
		pos++
	}
	args := make([]string, 0, argc)
	for len(args) < argc && pos < len(raw) {
		end := pos
		for end < len(raw) && raw[end] != 0 {
			end++
		}
		if end == pos {
			return nil, false
		}
		args = append(args, string(raw[pos:end]))
		pos = end + 1
	}
	if len(args) != argc {
		return nil, false
	}
	return args, true
}
