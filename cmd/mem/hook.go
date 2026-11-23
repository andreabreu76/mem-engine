package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/andreabreu76/mem-engine/internal/applog"
	"github.com/andreabreu76/mem-engine/internal/capture"
	"github.com/andreabreu76/mem-engine/internal/config"
)

func runHook(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cfg, cfgErr := config.Load()
	logDir := cfg.LogDir()
	if cfgErr != nil {
		logDir = fallbackLogDir()
	}
	log := slog.New(slog.NewJSONHandler(stderr, nil))
	if logDir != "" {
		if l, closer, err := applog.Open(logDir); err == nil {
			defer closer.Close()
			log = l
		}
	}
	event := ""
	if len(args) > 0 {
		event = args[0]
	}
	defer func() {
		if r := recover(); r != nil {
			log.Error("hook panicked", "event", event)
		}
	}()
	switch {
	case cfgErr != nil:
		log.Error("load config failed, event not captured", "event", event)
	case event == "":
		log.Error("mem hook called without an event name")
	default:
		capture.Handle(context.Background(), event, stdin, stdout, capture.Deps{Cfg: cfg, Log: log, Now: time.Now})
	}
	return 0
}

func fallbackLogDir() string {
	if home := os.Getenv("MEM_ENGINE_HOME"); home != "" {
		return filepath.Join(home, "logs")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".mem-engine", "logs")
	}
	return ""
}
