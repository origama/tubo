//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package grants

import (
	"context"
	"fmt"
)

type grantFileLock struct{}

func acquireGrantFileLock(context.Context, string) (*grantFileLock, error) {
	return nil, fmt.Errorf("interprocess grant store locking is unsupported on this platform")
}

func (*grantFileLock) release() {}

func replaceGrantStoreFile(string, string) error {
	return fmt.Errorf("atomic grant store replacement is unsupported on this platform")
}

func syncGrantStoreDirectory(string) error { return nil }
