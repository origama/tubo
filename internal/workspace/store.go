package workspace

import (
	"os"

	cfgpkg "github.com/origama/tubo/internal/config"
)

type Store interface {
	Load(configPath string) (cfgpkg.Config, error)
	Save(configPath string, cfg cfgpkg.Config) error
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte, mode os.FileMode) error
	MkdirAll(path string, mode os.FileMode) error
	Stat(path string) (os.FileInfo, error)
}

type FSStore struct{}

func (FSStore) Load(configPath string) (cfgpkg.Config, error) {
	return cfgpkg.LoadFile(configPath)
}

func (FSStore) Save(configPath string, cfg cfgpkg.Config) error {
	return cfgpkg.WriteFile(configPath, cfg, true)
}

func (FSStore) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (FSStore) WriteFile(path string, data []byte, mode os.FileMode) error {
	return os.WriteFile(path, data, mode)
}

func (FSStore) MkdirAll(path string, mode os.FileMode) error {
	return os.MkdirAll(path, mode)
}

func (FSStore) Stat(path string) (os.FileInfo, error) {
	return os.Stat(path)
}
