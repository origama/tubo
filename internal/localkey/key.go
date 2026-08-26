// Package localkey owns the narrow owner-local Ed25519 private-key file
// lifecycle used by Tubo owner secrets. It is not a generic filesystem or
// secret-manager abstraction: it stores one canonical private-key file with
// fail-closed local mode/symlink/durability checks.
package localkey

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
)

const (
	encodedPrivateKeyLen = 86 // RawURLEncoding length for 64 bytes.
	lockSuffix           = ".lock"
)

var (
	ErrUnsafe  = errors.New("localkey: unsafe key state")
	ErrInvalid = errors.New("localkey: invalid key file")
)

type testHooks struct {
	write                     func(*os.File, []byte) (int, error)
	syncFile                  func(*os.File) error
	syncDir                   func(*os.File) error
	beforeLock                func()
	afterPublishBeforeDirSync func()
}

var hooks atomic.Pointer[testHooks]

func setTestHooks(h *testHooks) { hooks.Store(h) }
func getHooks() *testHooks      { return hooks.Load() }

// Load reads an existing canonical Ed25519 private-key file. It performs no
// filesystem mutation. Missing, unsafe, corrupt, partial, oversized, symlinked,
// non-regular, or non-canonical state fails closed.
func Load(path string) (ed25519.PrivateKey, error) {
	if err := validateFinalPath(path); err != nil {
		return nil, err
	}
	if err := requireSecureDir(filepath.Dir(path), false); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if err := checkFileInfo(info); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("%w: key file is symlink", ErrUnsafe)
		}
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if err := checkFileInfo(opened); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, encodedPrivateKeyLen+1))
	if err != nil {
		return nil, err
	}
	return parseCanonicalPrivate(data)
}

// Provision creates the key once for a trusted local owner workflow, or returns
// the already committed key after serializing behind the same local file lock.
// Success is not reported until data is fsynced, the final name is published,
// and the containing directory is fsynced.
func Provision(path string) (ed25519.PrivateKey, error) {
	return provision(path, rand.Reader)
}

func provision(path string, random io.Reader) (ed25519.PrivateKey, error) {
	if err := validateFinalPath(path); err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	if err := requireSecureDir(dir, true); err != nil {
		return nil, err
	}
	lock, lockPath, err := acquireLock(dir, filepath.Base(path)+lockSuffix)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = os.Remove(lockPath)
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}()

	private, err := Load(path)
	if err == nil {
		return private, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	_, private, err = ed25519.GenerateKey(random)
	if err != nil {
		return nil, err
	}
	if err := writeDurableNoClobber(path, private); err != nil {
		return nil, err
	}
	return private, nil
}

func validateFinalPath(path string) error {
	if path == "" || filepath.Base(path) == "." || filepath.Base(path) == string(filepath.Separator) {
		return fmt.Errorf("%w: invalid key path", ErrInvalid)
	}
	if strings.ContainsRune(filepath.Base(path), os.PathSeparator) {
		return fmt.Errorf("%w: invalid key filename", ErrInvalid)
	}
	return nil
}

func requireSecureDir(dir string, create bool) error {
	if create {
		if err := mkdirAllSecure(dir); err != nil {
			return err
		}
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: key directory is symlink", ErrUnsafe)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: key directory is not directory", ErrUnsafe)
	}
	if unsafeMode(info.Mode(), 0o700) {
		return fmt.Errorf("%w: key directory mode %o", ErrUnsafe, info.Mode().Perm())
	}
	return nil
}

func mkdirAllSecure(dir string) error {
	clean := filepath.Clean(dir)
	if clean == "." {
		return nil
	}
	var missing []string
	for cur := clean; ; cur = filepath.Dir(cur) {
		info, err := os.Lstat(cur)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("%w: key directory is symlink", ErrUnsafe)
			}
			if !info.IsDir() {
				return fmt.Errorf("%w: key directory is not directory", ErrUnsafe)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		missing = append(missing, cur)
		parent := filepath.Dir(cur)
		if parent == cur {
			return err
		}
	}
	for i := len(missing) - 1; i >= 0; i-- {
		path := missing[i]
		if err := os.Mkdir(path, 0o700); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return err
			}
		} else if err := syncDir(filepath.Dir(path)); err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || unsafeMode(info.Mode(), 0o700) {
			return fmt.Errorf("%w: key directory mode", ErrUnsafe)
		}
	}
	return nil
}

func checkFileInfo(info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: key file is symlink", ErrUnsafe)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: key file is not regular", ErrUnsafe)
	}
	if unsafeMode(info.Mode(), 0o600) {
		return fmt.Errorf("%w: key file mode %o", ErrUnsafe, info.Mode().Perm())
	}
	if info.Size() > encodedPrivateKeyLen {
		return fmt.Errorf("%w: key file oversized", ErrInvalid)
	}
	if info.Size() != encodedPrivateKeyLen {
		return fmt.Errorf("%w: key file wrong length", ErrInvalid)
	}
	return nil
}

func unsafeMode(mode os.FileMode, allowed os.FileMode) bool {
	if mode.Perm()&^allowed != 0 {
		return true
	}
	return mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0
}

func parseCanonicalPrivate(data []byte) (ed25519.PrivateKey, error) {
	if len(data) > encodedPrivateKeyLen {
		return nil, fmt.Errorf("%w: key file oversized", ErrInvalid)
	}
	if len(data) != encodedPrivateKeyLen {
		return nil, fmt.Errorf("%w: key file wrong length", ErrInvalid)
	}
	text := string(data)
	if strings.ContainsAny(text, "\r\n\t ") || strings.ContainsRune(text, '=') {
		return nil, fmt.Errorf("%w: non-canonical private key", ErrInvalid)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(text)
	if err != nil || len(decoded) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%w: invalid Ed25519 private key", ErrInvalid)
	}
	if base64.RawURLEncoding.EncodeToString(decoded) != text {
		return nil, fmt.Errorf("%w: non-canonical private key", ErrInvalid)
	}
	// P2 hardening: verify that the public suffix (last 32 bytes) of the
	// loaded 64-byte Ed25519 private key matches the canonical key derived
	// from its 32-byte seed. Go's ed25519.PrivateKey is a 64-byte sequence
	// (seed || public) but does not enforce consistency. Without this check
	// a corrupt or adversarial 64-byte payload with an arbitrary public suffix
	// would be accepted and used, silently defeating identity and signature
	// verification guarantees.
	private := ed25519.PrivateKey(decoded)
	seed := private.Seed()
	expected := ed25519.NewKeyFromSeed(seed)
	if !bytes.Equal([]byte(private), []byte(expected)) {
		return nil, fmt.Errorf("%w: private key public suffix inconsistent with seed", ErrInvalid)
	}
	return private, nil
}

func acquireLock(dir, name string) (*os.File, string, error) {
	path := filepath.Join(dir, name)
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, "", fmt.Errorf("%w: lock file is symlink", ErrUnsafe)
		}
		return nil, "", err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, "", err
	}
	if err := checkLockInfo(info); err != nil {
		file.Close()
		return nil, "", err
	}
	if h := getHooks(); h != nil && h.beforeLock != nil {
		h.beforeLock()
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		file.Close()
		return nil, "", err
	}
	return file, path, nil
}

func checkLockInfo(info os.FileInfo) error {
	if !info.Mode().IsRegular() || unsafeMode(info.Mode(), 0o600) {
		return fmt.Errorf("%w: key lock mode", ErrUnsafe)
	}
	return nil
}

func writeDurableNoClobber(path string, private ed25519.PrivateKey) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	suffix := make([]byte, 12)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+base+".tmp."+base64.RawURLEncoding.EncodeToString(suffix))
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = os.Remove(tmp)
		}
	}()
	encoded := []byte(base64.RawURLEncoding.EncodeToString(private))
	if err := writeAll(file, encoded); err != nil {
		file.Close()
		return err
	}
	if err := syncFile(file); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Link(tmp, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: key appeared during provision", ErrUnsafe)
		}
		return err
	}
	if err := os.Remove(tmp); err != nil {
		_ = os.Remove(path)
		return err
	}
	removeTmp = false
	if h := getHooks(); h != nil && h.afterPublishBeforeDirSync != nil {
		h.afterPublishBeforeDirSync()
	}
	if err := syncDir(dir); err != nil {
		_ = os.Remove(path)
		_ = os.Remove(tmp)
		_ = syncDir(dir)
		return err
	}
	return nil
}

func writeAll(file *os.File, data []byte) error {
	if h := getHooks(); h != nil && h.write != nil {
		n, err := h.write(file, data)
		if err != nil {
			return err
		}
		if n != len(data) {
			return io.ErrShortWrite
		}
		return nil
	}
	for len(data) > 0 {
		n, err := file.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func syncFile(file *os.File) error {
	if h := getHooks(); h != nil && h.syncFile != nil {
		return h.syncFile(file)
	}
	return file.Sync()
}

func syncDir(dir string) error {
	file, err := os.OpenFile(dir, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	if h := getHooks(); h != nil && h.syncDir != nil {
		return h.syncDir(file)
	}
	return file.Sync()
}
