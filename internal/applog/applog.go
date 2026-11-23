package applog

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

const (
	fileName = "mem.log"
	maxSize  = 5 << 20
)

func Open(dir string) (*slog.Logger, io.Closer, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("open log: %w", err)
	}
	path := filepath.Join(dir, fileName)
	if info, err := os.Stat(path); err == nil && info.Size() > maxSize {
		os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open log: %w", err)
	}
	return slog.New(slog.NewJSONHandler(f, nil)), f, nil
}
