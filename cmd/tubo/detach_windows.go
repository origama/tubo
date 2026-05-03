//go:build windows

package main

import "os/exec"

func configureDetachedCommand(cmd *exec.Cmd) {
	// Placeholder for future Windows-specific detached process flags.
	_ = cmd
}
