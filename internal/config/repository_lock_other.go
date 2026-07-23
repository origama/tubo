//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package config

import (
	"context"
	"fmt"
)

type configFileLock struct{}

func acquireConfigFileLock(context.Context, string) (*configFileLock, error) {
	return nil, fmt.Errorf("interprocess config locking is unsupported on this platform")
}

func (*configFileLock) release() {}

func syncConfigDirectory(string) error { return nil }
