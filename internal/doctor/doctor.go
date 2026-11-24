package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/andreabreu76/mem-engine/internal/config"
	"github.com/andreabreu76/mem-engine/internal/setup"
	"github.com/andreabreu76/mem-engine/internal/spool"
	"github.com/andreabreu76/mem-engine/internal/store"
)

type Level int

const (
	OK Level = iota
	Warn
	Fail
)

func (l Level) String() string {
	switch l {
	case OK:
		return "ok"
	case Warn:
		return "warn"
	default:
		return "FAIL"
	}
}

type Check struct {
	Name   string
	Level  Level
	Detail string
}

type Env struct {
	Cfg        config.Config
	UserHome   string
	LookPath   func(string) (string, error)
	RunCommand func(ctx context.Context, name string, args ...string) ([]byte, error)
	Executable string
	Now        time.Time
}

type checkFunc func(context.Context, Env) Check

var checks = []checkFunc{
	checkBinary,
	checkConfig,
	checkDataDir,
	checkDatabase,
	checkSpool,
	checkCaptureErrors,
	checkHooks,
	checkHooksDuplicated,
	checkMCP,
}

const mcpTimeout = 30 * time.Second

func Run(ctx context.Context, env Env) []Check {
	out := make([]Check, 0, len(checks))
	for _, c := range checks {
		out = append(out, c(ctx, env))
	}
	return out
}

func checkBinary(_ context.Context, env Env) Check {
	c := Check{Name: "binary"}
	found, err := env.LookPath("mem")
	if err != nil {
		c.Level, c.Detail = Warn, "mem not found in PATH, running "+env.Executable
		return c
	}
	if resolve(found) != resolve(env.Executable) {
		c.Level, c.Detail = Warn, fmt.Sprintf("PATH resolves mem to %s, running %s", found, env.Executable)
		return c
	}
	c.Detail = found
	return c
}

func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

func checkConfig(_ context.Context, env Env) Check {
	c := Check{Name: "config"}
	path := filepath.Join(env.Cfg.Home, "config.toml")
	if _, err := config.LoadFrom(env.Cfg.Home); err != nil {
		c.Level, c.Detail = Fail, err.Error()
		return c
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		c.Detail = "defaults (no config.toml)"
		return c
	}
	c.Detail = path
	return c
}

func checkDataDir(_ context.Context, env Env) Check {
	c := Check{Name: "data-dir"}
	home := env.Cfg.Home
	fi, err := os.Stat(home)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		c.Level, c.Detail = Warn, home+" does not exist yet (created on first hook)"
		return c
	case err != nil:
		c.Level, c.Detail = Fail, err.Error()
		return c
	case !fi.IsDir():
		c.Level, c.Detail = Fail, home+" is not a directory"
		return c
	}
	f, err := os.CreateTemp(home, ".doctor-*")
	if err != nil {
		c.Level, c.Detail = Fail, "not writable: "+err.Error()
		return c
	}
	f.Close()
	if err := os.Remove(f.Name()); err != nil {
		c.Level, c.Detail = Warn, "could not remove probe file: "+err.Error()
		return c
	}
	c.Detail = home
	return c
}

func openStore(env Env) (*store.Store, error) {
	if _, err := os.Stat(env.Cfg.DBPath()); err != nil {
		return nil, err
	}
	return store.Open(env.Cfg.DBPath())
}

func checkDatabase(ctx context.Context, env Env) Check {
	c := Check{Name: "database"}
	st, err := openStore(env)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		c.Level, c.Detail = Warn, "no database yet (created on first hook)"
		return c
	case errors.Is(err, store.ErrSchemaTooNew):
		c.Level, c.Detail = Fail, err.Error()+", upgrade mem"
		return c
	case err != nil:
		c.Level, c.Detail = Fail, err.Error()
		return c
	}
	defer st.Close()
	v, err := st.Version(ctx)
	if err != nil {
		c.Level, c.Detail = Fail, err.Error()
		return c
	}
	if err := st.CheckFTS5(ctx); err != nil {
		c.Level, c.Detail = Fail, err.Error()
		return c
	}
	c.Detail = fmt.Sprintf("%s (schema %d, fts5 ok)", env.Cfg.DBPath(), v)
	return c
}

func checkSpool(_ context.Context, env Env) Check {
	c := Check{Name: "spool"}
	n, err := spool.Pending(env.Cfg.SpoolDir())
	switch {
	case err != nil:
		c.Level, c.Detail = Warn, err.Error()
	case n > 0:
		c.Level, c.Detail = Warn, fmt.Sprintf("%d events pending in %s", n, env.Cfg.SpoolDir())
	default:
		c.Detail = "empty"
	}
	return c
}

func checkCaptureErrors(ctx context.Context, env Env) Check {
	c := Check{Name: "capture-errors"}
	st, err := openStore(env)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		c.Detail = "none (no database yet)"
		return c
	case err != nil:
		c.Level, c.Detail = Warn, "skipped, database unavailable"
		return c
	}
	defer st.Close()
	n, last, err := st.CaptureErrorsSince(ctx, env.Now.Add(-24*time.Hour))
	switch {
	case err != nil:
		c.Level, c.Detail = Warn, err.Error()
	case n > 0:
		c.Level, c.Detail = Warn, fmt.Sprintf("%d in the last 24h, last: %s", n, last)
	default:
		c.Detail = "none in the last 24h"
	}
	return c
}

func checkHooks(_ context.Context, env Env) Check {
	c := Check{Name: "hooks"}
	path, data, err := readUserSettings(env)
	if err != nil {
		c.Level, c.Detail = Warn, err.Error()
		return c
	}
	_, changed, err := setup.MergeHooks(data)
	plugin := enabledPlugin(data)
	switch {
	case err != nil:
		c.Level, c.Detail = Warn, fmt.Sprintf("%s: %v", path, err)
	case !setup.HasMemHooks(data) && plugin != "":
		c.Detail = "provided by plugin " + plugin
	case !setup.HasMemHooks(data):
		c.Level, c.Detail = Warn, "no mem hooks in "+path+", run mem setup"
	case changed:
		c.Level, c.Detail = Warn, "hooks outdated, run mem setup"
	default:
		c.Detail = path
	}
	return c
}

const pluginPrefix = "mem-engine@"

func enabledPlugin(settings []byte) string {
	var s struct {
		EnabledPlugins map[string]any `json:"enabledPlugins"`
	}
	if json.Unmarshal(settings, &s) != nil {
		return ""
	}
	var keys []string
	for name, enabled := range s.EnabledPlugins {
		if enabled == true && strings.HasPrefix(name, pluginPrefix) {
			keys = append(keys, name)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	slices.Sort(keys)
	return keys[0]
}

func readUserSettings(env Env) (string, []byte, error) {
	path := filepath.Join(env.UserHome, ".claude", "settings.json")
	data, _, err := setup.ReadSettings(path)
	return path, data, err
}

func checkHooksDuplicated(_ context.Context, env Env) Check {
	c := Check{Name: "hooks-duplicated", Detail: "none"}
	path, data, err := readUserSettings(env)
	if err != nil || !setup.HasMemHooks(data) {
		return c
	}
	if plugin := enabledPlugin(data); plugin != "" {
		c.Level = Warn
		c.Detail = fmt.Sprintf("mem hooks in %s and plugin %s both run, remove the hooks from settings.json or uninstall the plugin", path, plugin)
	}
	return c
}

func checkMCP(ctx context.Context, env Env) Check {
	c := Check{Name: "mcp"}
	if _, data, err := readUserSettings(env); err == nil {
		if plugin := enabledPlugin(data); plugin != "" {
			c.Detail = "provided by plugin " + plugin
			return c
		}
	}
	claude, err := env.LookPath("claude")
	if err != nil {
		c.Detail = "claude not found, skipped"
		return c
	}
	runCmd := env.RunCommand
	if runCmd == nil {
		runCmd = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		}
	}
	ctx, cancel := context.WithTimeout(ctx, mcpTimeout)
	defer cancel()
	if _, err := runCmd(ctx, claude, setup.MCPGetArgs()...); err != nil {
		c.Level, c.Detail = Warn, "claude "+strings.Join(setup.MCPGetArgs(), " ")+" failed, run mem setup"
		return c
	}
	c.Detail = setup.MCPServerName + " registered with claude"
	return c
}
