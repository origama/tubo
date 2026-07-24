package config

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const defaultConfigLockTimeout = 10 * time.Second

// ConfigRepository serializes read-modify-write config transactions across
// processes and commits complete files atomically.
type ConfigRepository struct {
	Path        string
	LockTimeout time.Duration
	hooks       atomicWriteHooks
}

// ConfigMutation changes config loaded while repository lock is held.
type ConfigMutation func(*Config) error

type atomicWriteHooks struct {
	afterCreate  func(string) error
	write        func(*os.File, []byte) (int, error)
	beforeSync   func(string) error
	beforeRename func(string, string) error
	rename       func(string, string) error
}

func NewConfigRepository(path string) *ConfigRepository {
	return &ConfigRepository{Path: path, LockTimeout: defaultConfigLockTimeout}
}

// Load reads active config. Atomic replacement guarantees reader sees old or
// new complete file without taking writer lock.
func (r *ConfigRepository) Load() (Config, error) {
	return LoadFile(r.Path)
}

// Update reloads config while holding interprocess lock, applies mutation, and
// commits atomically. Unknown YAML mapping keys are retained; comments, anchors,
// and original key ordering are not guaranteed after mutation.
func (r *ConfigRepository) Update(ctx context.Context, mutate ConfigMutation) (Config, error) {
	if mutate == nil {
		return Config{}, fmt.Errorf("config mutation is required")
	}
	var updated Config
	err := r.withLock(ctx, func() error {
		raw, err := os.ReadFile(r.Path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		existed := err == nil
		var current Config
		if existed {
			current, err = decodeConfig(raw)
			if err != nil {
				return err
			}
		}
		before := cloneConfig(current)
		if err := mutate(&current); err != nil {
			return err
		}
		normalizeConfig(&current)
		updated = current
		if existed && reflect.DeepEqual(before, current) {
			return nil
		}
		encoded, err := mergeConfigYAML(raw, before, current)
		if err != nil {
			return err
		}
		return atomicWriteFile(r.Path, encoded, 0o600, r.hooks)
	})
	if err != nil {
		return Config{}, err
	}
	return updated, nil
}

// Write atomically replaces config under lock. force=false preserves create-only
// semantics.
func (r *ConfigRepository) Write(ctx context.Context, cfg Config, force bool) error {
	return r.withLock(ctx, func() error {
		if !force {
			if _, err := os.Stat(r.Path); err == nil {
				return fmt.Errorf("%s exists (use --force)", r.Path)
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		normalizeConfig(&cfg)
		encoded, err := yaml.Marshal(cfg)
		if err != nil {
			return err
		}
		return atomicWriteFile(r.Path, encoded, 0o600, r.hooks)
	})
}

func (r *ConfigRepository) withLock(ctx context.Context, fn func() error) error {
	if r == nil || r.Path == "" {
		return fmt.Errorf("config path is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	dir := filepath.Dir(r.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	timeout := r.LockTimeout
	if timeout <= 0 {
		timeout = defaultConfigLockTimeout
	}
	lockCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	lock, err := acquireConfigFileLock(lockCtx, r.Path+".lock")
	if err != nil {
		return fmt.Errorf("lock config %s: %w", r.Path, err)
	}
	defer lock.release()
	return fn()
}

func decodeConfig(raw []byte) (Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, err
	}
	normalizeConfig(&cfg)
	return cfg, nil
}

func mergeConfigYAML(raw []byte, before, after Config) ([]byte, error) {
	if len(raw) == 0 {
		return yaml.Marshal(after)
	}
	var rawMap map[string]any
	if err := yaml.Unmarshal(raw, &rawMap); err != nil {
		return nil, err
	}
	if rawMap == nil {
		rawMap = make(map[string]any)
	}
	beforeMap, err := configAsMap(before)
	if err != nil {
		return nil, err
	}
	afterMap, err := configAsMap(after)
	if err != nil {
		return nil, err
	}
	applyConfigMapDiff(rawMap, beforeMap, afterMap)
	return yaml.Marshal(rawMap)
}

func configAsMap(cfg Config) (map[string]any, error) {
	encoded, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := yaml.Unmarshal(encoded, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func applyConfigMapDiff(raw, before, after map[string]any) {
	keys := make(map[string]struct{}, len(before)+len(after))
	for key := range before {
		keys[key] = struct{}{}
	}
	for key := range after {
		keys[key] = struct{}{}
	}
	for key := range keys {
		beforeValue, existedBefore := before[key]
		afterValue, existsAfter := after[key]
		switch {
		case !existsAfter && existedBefore:
			delete(raw, key)
		case existsAfter && !existedBefore:
			raw[key] = afterValue
		case reflect.DeepEqual(beforeValue, afterValue):
			continue
		default:
			rawMap, rawOK := stringMap(raw[key])
			beforeMap, beforeOK := stringMap(beforeValue)
			afterMap, afterOK := stringMap(afterValue)
			if rawOK && beforeOK && afterOK {
				applyConfigMapDiff(rawMap, beforeMap, afterMap)
				raw[key] = rawMap
			} else {
				raw[key] = afterValue
			}
		}
	}
}

func stringMap(value any) (map[string]any, bool) {
	mapped, ok := value.(map[string]any)
	return mapped, ok
}

func atomicWriteFile(path string, data []byte, mode os.FileMode, hooks atomicWriteHooks) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := removeStaleConfigTemps(dir, filepath.Base(path)); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}()
	if err := temp.Chmod(mode); err != nil {
		return err
	}
	if hooks.afterCreate != nil {
		if err := hooks.afterCreate(tempPath); err != nil {
			return err
		}
	}
	write := hooks.write
	if write == nil {
		write = func(file *os.File, remaining []byte) (int, error) { return file.Write(remaining) }
	}
	for written := 0; written < len(data); {
		n, err := write(temp, data[written:])
		written += n
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	if hooks.beforeSync != nil {
		if err := hooks.beforeSync(tempPath); err != nil {
			return err
		}
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if hooks.beforeRename != nil {
		if err := hooks.beforeRename(tempPath, path); err != nil {
			return err
		}
	}
	rename := hooks.rename
	if rename == nil {
		rename = os.Rename
	}
	if err := rename(tempPath, path); err != nil {
		return err
	}
	if err := syncConfigDirectory(dir); err != nil {
		return err
	}
	return nil
}

func removeStaleConfigTemps(dir, base string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	prefix := "." + base + ".tmp-"
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
