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
	"sync"
	"sync/atomic"
	"time"
)

const (
	ext         = ".jsonl"
	drainingTag = ".draining-"
	tmpPrefix   = ".spool-"
	tmpSuffix   = ".tmp"
	orphanAge   = time.Minute
)

var (
	drainMu    sync.Mutex
	lastStamp  atomic.Int64
	setTimes   = os.Chtimes
	renameFile = os.Rename
)

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
	drainMu.Lock()
	defer drainMu.Unlock()
	names, err := listFiles(dir)
	if err != nil {
		return 0, fmt.Errorf("spool drain: %w", err)
	}
	own := drainingTag + strconv.Itoa(os.Getpid())
	for _, name := range names {
		if strings.HasSuffix(name, own) {
			continue
		}
		n, err := drainFile(dir, name, apply)
		applied += n
		if err != nil {
			return applied, err
		}
	}
	return applied, nil
}

func Pending(dir string) (lines int, err error) {
	names, err := listFiles(dir)
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
	src := filepath.Join(dir, name)
	claimed := filepath.Join(dir, claimName(name))
	if _, err := os.Lstat(claimed); err == nil {
		return 0, nil
	}
	if touch(src) != nil || renameFile(src, claimed) != nil {
		return 0, nil
	}
	data, err := os.ReadFile(claimed)
	if err != nil {
		return 0, fmt.Errorf("spool drain: read %s: %w", name, err)
	}
	lines := splitLines(data)
	for i, line := range lines {
		if err := touch(claimed); err != nil {
			return i, fmt.Errorf("spool drain: keep claim on %s: %w", name, err)
		}
		if applyErr := apply(json.RawMessage(line)); applyErr != nil {
			preferred := ""
			if strings.HasSuffix(name, ext) {
				preferred = name
			}
			if err := publish(dir, preferred, joinLines(lines[i:])); err != nil {
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

func claimName(name string) string {
	base, _, found := strings.Cut(name, drainingTag)
	if !found {
		base = strings.TrimSuffix(strings.TrimSuffix(name, ext), tmpSuffix)
	}
	return base + drainingTag + strconv.Itoa(os.Getpid())
}

func touch(path string) error {
	now := time.Now()
	return setTimes(path, now, now)
}

func publish(dir, preferred string, data []byte) error {
	tmp, err := os.CreateTemp(dir, tmpPrefix+"*"+tmpSuffix)
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	name := preferred
	if name == "" || exists(filepath.Join(dir, name)) {
		name = fmt.Sprintf("%d-%d%s", os.Getpid(), nextStamp(), ext)
	}
	if err := os.Rename(tmpPath, filepath.Join(dir, name)); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return !errors.Is(err, fs.ErrNotExist)
}

func nextStamp() int64 {
	for {
		last := lastStamp.Load()
		next := max(time.Now().UnixNano(), last+1)
		if lastStamp.CompareAndSwap(last, next) {
			return next
		}
	}
}

func listFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().Add(-orphanAge)
	var names []string
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() {
			continue
		}
		if strings.HasSuffix(name, ext) {
			names = append(names, name)
			continue
		}
		if !strings.Contains(name, drainingTag) && !(strings.HasPrefix(name, tmpPrefix) && strings.HasSuffix(name, tmpSuffix)) {
			continue
		}
		info, err := e.Info()
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.ModTime().Before(cutoff) {
			names = append(names, name)
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
	base, _, _ := strings.Cut(name, ".")
	_, nano, _ := strings.Cut(base, "-")
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
