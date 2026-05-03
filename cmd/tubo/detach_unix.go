//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

func configureDetachedCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
