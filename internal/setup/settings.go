package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

func SettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

func ReadSettings(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func WriteSettings(path string, data []byte) error {
	mode := fs.FileMode(0o600)
	fi, err := os.Stat(path)
	switch {
	case err == nil:
		mode = fi.Mode().Perm()
		old, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := writeAtomic(path+".bak", old, 0o600); err != nil {
			return fmt.Errorf("write backup: %w", err)
		}
		if path, err = filepath.EvalSymlinks(path); err != nil {
			return err
		}
		return writeAtomic(path, data, mode)
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		return writeAtomic(path, data, mode)
	default:
		return err
	}
}

func writeAtomic(path string, data []byte, mode fs.FileMode) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".settings-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if err = f.Chmod(mode); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func MergeHooks(settings []byte) ([]byte, bool, error) {
	original, err := decodeObject(settings)
	if err != nil {
		return nil, false, err
	}
	merged, _ := decodeObject(settings)
	hooks := map[string]any{}
	if raw, ok := merged["hooks"]; ok {
		if hooks, ok = raw.(map[string]any); !ok {
			return nil, false, errors.New("\"hooks\" is not a JSON object")
		}
	}
	for event, raw := range hooks {
		groups, ok := raw.([]any)
		if !ok {
			continue
		}
		kept, removed := withoutMemHooks(groups)
		if removed && len(kept) == 0 {
			delete(hooks, event)
			continue
		}
		hooks[event] = kept
	}
	for _, h := range Hooks() {
		var groups []any
		if raw, ok := hooks[h.Event]; ok {
			if groups, ok = raw.([]any); !ok {
				return nil, false, fmt.Errorf("\"hooks.%s\" is not a JSON array", h.Event)
			}
		}
		hooks[h.Event] = append(groups, h.group())
	}
	merged["hooks"] = hooks
	out, err := encode(merged)
	if err != nil {
		return nil, false, err
	}
	result, err := decodeObject(out)
	if err != nil {
		return nil, false, err
	}
	if reflect.DeepEqual(original, result) {
		return settings, false, nil
	}
	return out, true, nil
}

func DisableNativeMemory(settings []byte) ([]byte, bool, error) {
	m, err := decodeObject(settings)
	if err != nil {
		return nil, false, err
	}
	if m[nativeMemoryKey] == false {
		return settings, false, nil
	}
	m[nativeMemoryKey] = false
	out, err := encode(m)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}

func HasMemHooks(settings []byte) bool {
	m, err := decodeObject(settings)
	if err != nil {
		return false
	}
	hooks, _ := m["hooks"].(map[string]any)
	for _, raw := range hooks {
		groups, _ := raw.([]any)
		for _, g := range groups {
			gm, _ := g.(map[string]any)
			entries, _ := gm["hooks"].([]any)
			for _, e := range entries {
				if isMemHook(e) {
					return true
				}
			}
		}
	}
	return false
}

func withoutMemHooks(groups []any) ([]any, bool) {
	kept := make([]any, 0, len(groups))
	removed := false
	for _, g := range groups {
		gm, ok := g.(map[string]any)
		if !ok {
			kept = append(kept, g)
			continue
		}
		entries, ok := gm["hooks"].([]any)
		if !ok {
			kept = append(kept, g)
			continue
		}
		others := make([]any, 0, len(entries))
		for _, e := range entries {
			if !isMemHook(e) {
				others = append(others, e)
			}
		}
		if len(others) == len(entries) {
			kept = append(kept, g)
			continue
		}
		removed = true
		if len(others) > 0 {
			gm["hooks"] = others
			kept = append(kept, gm)
		}
	}
	return kept, removed
}

func isMemHook(entry any) bool {
	m, _ := entry.(map[string]any)
	cmd, _ := m["command"].(string)
	return strings.HasPrefix(cmd, memCommandPrefix)
}

func decodeObject(data []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("invalid JSON: unexpected data after the top-level object")
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("top-level value is not a JSON object")
	}
	return m, nil
}

func encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

const MCPServerName = "mem-engine"

const nativeMemoryKey = "autoMemoryEnabled"

func MCPAddArgs() []string {
	return []string{"mcp", "add", "--scope", "user", MCPServerName, "--", "mem", "mcp"}
}

func MCPGetArgs() []string {
	return []string{"mcp", "get", MCPServerName}
}
