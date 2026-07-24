package grants

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const defaultGrantStoreLockTimeout = 10 * time.Second

type localStoreLock struct {
	semaphore chan struct{}
	users     int
}

var localStoreLocks = struct {
	sync.Mutex
	byPath map[string]*localStoreLock
}{byPath: make(map[string]*localStoreLock)}

func withGrantStoreLock(path string, timeout time.Duration, fn func() error) error {
	if fn == nil {
		return errors.New("grant store transaction is required")
	}
	if path == "" {
		return fn()
	}
	if timeout <= 0 {
		timeout = defaultGrantStoreLockTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	key, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(key), 0o700); err != nil {
		return err
	}
	releaseLocal, err := acquireLocalStoreLock(ctx, key)
	if err != nil {
		return fmt.Errorf("lock grant store %s: %w", path, err)
	}
	defer releaseLocal()

	lock, err := acquireGrantFileLock(ctx, key+".lock")
	if err != nil {
		return fmt.Errorf("lock grant store %s: %w", path, err)
	}
	defer lock.release()
	return fn()
}

func acquireLocalStoreLock(ctx context.Context, key string) (func(), error) {
	localStoreLocks.Lock()
	lock := localStoreLocks.byPath[key]
	if lock == nil {
		lock = &localStoreLock{semaphore: make(chan struct{}, 1)}
		localStoreLocks.byPath[key] = lock
	}
	lock.users++
	localStoreLocks.Unlock()

	select {
	case lock.semaphore <- struct{}{}:
		return func() {
			<-lock.semaphore
			releaseLocalStoreLockReference(key, lock)
		}, nil
	case <-ctx.Done():
		releaseLocalStoreLockReference(key, lock)
		return nil, ctx.Err()
	}
}

func releaseLocalStoreLockReference(key string, lock *localStoreLock) {
	localStoreLocks.Lock()
	defer localStoreLocks.Unlock()
	lock.users--
	if lock.users == 0 && localStoreLocks.byPath[key] == lock {
		delete(localStoreLocks.byPath, key)
	}
}

func atomicWriteGrantStore(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tempPrefix := "." + filepath.Base(path) + ".tmp-"
	if err := removeStaleGrantStoreTemps(dir, tempPrefix); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, tempPrefix)
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}()
	if err := temp.Chmod(0o600); err != nil {
		return err
	}
	for written := 0; written < len(data); {
		n, err := temp.Write(data[written:])
		written += n
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := replaceGrantStoreFile(tempPath, path); err != nil {
		return err
	}
	return syncGrantStoreDirectory(dir)
}

func removeStaleGrantStoreTemps(dir, prefix string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
