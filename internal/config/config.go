package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

type Group struct {
	Name   string `toml:"name"`
	Prefix string `toml:"prefix"`
}

type Alias struct {
	Path string `toml:"path"`
	Key  string `toml:"key"`
}

type Config struct {
	Home                 string   `toml:"-"`
	RetentionDays        int      `toml:"retention_days"`
	MaxObservationBytes  int      `toml:"max_observation_bytes"`
	UIPort               int      `toml:"ui_port"`
	SkipTools            []string `toml:"skip_tools"`
	RedactExtra          []string `toml:"redact_extra"`
	RedactDisableBuiltin bool     `toml:"redact_disable_builtin"`
	Groups               []Group  `toml:"group"`
	Aliases              []Alias  `toml:"alias"`
}

func Load() (Config, error) {
	home := os.Getenv("MEM_ENGINE_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return Config{}, fmt.Errorf("resolve home: %w", err)
		}
		home = filepath.Join(userHome, ".mem-engine")
	}
	return LoadFrom(home)
}

func LoadFrom(home string) (Config, error) {
	c := Config{
		Home:                home,
		RetentionDays:       90,
		MaxObservationBytes: 4096,
		UIPort:              47600,
		SkipTools:           []string{"*mem-engine*"},
	}
	path := filepath.Join(home, "config.toml")
	if _, err := toml.DecodeFile(path, &c); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return c, nil
		}
		return Config{}, fmt.Errorf("load %s: %w", path, err)
	}
	for i := range c.Groups {
		c.Groups[i].Prefix = cleanPath(c.Groups[i].Prefix)
	}
	for i := range c.Aliases {
		c.Aliases[i].Path = cleanPath(c.Aliases[i].Path)
		c.Aliases[i].Key = cleanPath(c.Aliases[i].Key)
	}
	return c, nil
}

func (c Config) DBPath() string   { return filepath.Join(c.Home, "mem.db") }
func (c Config) WikiDir() string  { return filepath.Join(c.Home, "wiki") }
func (c Config) LogDir() string   { return filepath.Join(c.Home, "logs") }
func (c Config) SpoolDir() string { return filepath.Join(c.Home, "spool") }

func ExpandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(userHome, strings.TrimPrefix(p, "~"))
}

func cleanPath(p string) string {
	return filepath.Clean(ExpandHome(p))
}
