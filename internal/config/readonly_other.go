//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package config

import (
	"strings"
)

// IsReadOnlyFilesystem reports whether err indicates a genuine read-only
// filesystem. On platforms without a stable EROFS errno, it falls back to the
// OS error string. It deliberately does not match "permission denied", which
// signals ownership/permission problems that must surface as real failures.
func IsReadOnlyFilesystem(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "read-only file system")
}
