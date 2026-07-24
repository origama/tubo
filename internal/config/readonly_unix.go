//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package config

import (
	"errors"
	"syscall"
)

// IsReadOnlyFilesystem reports whether err indicates a genuine read-only
// filesystem (EROFS). It deliberately does not match EACCES/EPERM or
// "permission denied", because those signal ownership/permission problems that
// must surface as real failures rather than silent in-memory fallbacks.
//
// Callers that need to keep running with a read-only config volume (for example
// a containerized smoke mount) must opt in by checking this helper explicitly.
func IsReadOnlyFilesystem(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, syscall.EROFS)
}
