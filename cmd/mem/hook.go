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
	"github.com/andreabreu76/mem-engine/internal/handoff"
	"github.com/andreabreu76/mem-engine/internal/store"
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
		capture.Handle(context.Background(), event, stdin, stdout, capture.Deps{
			Cfg:                cfg,
			Log:                log,
			Now:                time.Now,
			SessionStartOutput: sessionStartOutput(log),
		})
	}
	return 0
}

func sessionStartOutput(log *slog.Logger) func(context.Context, *store.Store, capture.Op, capture.Payload) string {
	return func(ctx context.Context, st *store.Store, op capture.Op, _ capture.Payload) string {
		in, err := handoff.Collect(ctx, st, op.Project, op.Session.SessionID, op.Session.Source)
		if err != nil {
			log.Warn("build handoff failed", "event", "SessionStart", "session_id", op.Session.SessionID, "error", err.Error())
			return handoff.Build(handoff.Input{Project: op.Project})
		}
		return handoff.Build(in)
	}
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
