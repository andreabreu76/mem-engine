package spool

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const ext = ".jsonl"

func Append(dir string, v any) error {
	line, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("spool append: encode: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("spool append: %w", err)
	}
	if err := publish(dir, "", append(line, '\n')); err != nil {
		return fmt.Errorf("spool append: %w", err)
	}
	return nil
}

func Drain(dir string, apply func(json.RawMessage) error) (applied int, err error) {
	names, err := pendingFiles(dir)
	if err != nil {
		return 0, fmt.Errorf("spool drain: %w", err)
	}
	for _, name := range names {
		n, err := drainFile(dir, name, apply)
		applied += n
		if err != nil {
			return applied, err
		}
	}
	return applied, nil
}

func Pending(dir string) (lines int, err error) {
	names, err := pendingFiles(dir)
	if err != nil {
		return 0, fmt.Errorf("spool pending: %w", err)
	}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("spool pending: %w", err)
		}
		lines += len(splitLines(data))
	}
	return lines, nil
}

func drainFile(dir, name string, apply func(json.RawMessage) error) (int, error) {
	claimed := filepath.Join(dir, strings.TrimSuffix(name, ext)+".draining-"+strconv.Itoa(os.Getpid()))
	if err := os.Rename(filepath.Join(dir, name), claimed); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("spool drain: claim %s: %w", name, err)
	}
	data, err := os.ReadFile(claimed)
	if err != nil {
		return 0, fmt.Errorf("spool drain: read %s: %w", name, err)
	}
	lines := splitLines(data)
	for i, line := range lines {
		if applyErr := apply(json.RawMessage(line)); applyErr != nil {
			if err := publish(dir, name, joinLines(lines[i:])); err != nil {
				return i, fmt.Errorf("spool drain: keep remaining lines of %s in %s: %w (apply: %w)", name, claimed, err, applyErr)
			}
			if err := os.Remove(claimed); err != nil {
				return i, fmt.Errorf("spool drain: remove %s: %w (apply: %w)", claimed, err, applyErr)
			}
			return i, fmt.Errorf("spool drain: apply line from %s: %w", name, applyErr)
		}
	}
	if err := os.Remove(claimed); err != nil {
		return len(lines), fmt.Errorf("spool drain: remove %s: %w", claimed, err)
	}
	return len(lines), nil
}

func publish(dir, preferred string, data []byte) error {
	tmp, err := os.CreateTemp(dir, ".spool-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	name := preferred
	for {
		if name == "" {
			name = fmt.Sprintf("%d-%d%s", os.Getpid(), time.Now().UnixNano(), ext)
		}
		err := os.Link(tmpPath, filepath.Join(dir, name))
		if err == nil {
			return nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
		name = ""
	}
}

func pendingFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ext) {
			names = append(names, e.Name())
		}
	}
	slices.SortFunc(names, func(a, b string) int {
		if c := cmp.Compare(stamp(a), stamp(b)); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	return names, nil
}

func stamp(name string) int64 {
	_, nano, _ := strings.Cut(strings.TrimSuffix(name, ext), "-")
	n, _ := strconv.ParseInt(nano, 10, 64)
	return n
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) > 0 {
			lines = append(lines, line)
		}
	}
	return lines
}

func joinLines(lines [][]byte) []byte {
	return append(bytes.Join(lines, []byte("\n")), '\n')
}
