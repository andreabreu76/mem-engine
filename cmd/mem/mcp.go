package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andreabreu76/mem-engine/internal/applog"
	"github.com/andreabreu76/mem-engine/internal/config"
	"github.com/andreabreu76/mem-engine/internal/mcpserver"
	"github.com/andreabreu76/mem-engine/internal/memory"
	"github.com/andreabreu76/mem-engine/internal/project"
	"github.com/andreabreu76/mem-engine/internal/store"
)

func runMCP(args []string, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintf(stderr, "mem mcp: unexpected arguments\n\n%s", usage)
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "mem mcp: %v\n", err)
		return 1
	}
	if err := os.MkdirAll(cfg.Home, 0o700); err != nil {
		fmt.Fprintf(stderr, "mem mcp: %v\n", err)
		return 1
	}
	log := slog.New(slog.DiscardHandler)
	if l, closer, err := applog.Open(cfg.LogDir()); err == nil {
		defer closer.Close()
		log = l
	}
	st, err := store.Open(cfg.DBPath())
	if errors.Is(err, store.ErrSchemaTooNew) {
		fmt.Fprintf(stderr, "mem mcp: %v, upgrade mem\n", err)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "mem mcp: open database: %v\n", err)
		return 1
	}
	defer st.Close()
	current, err := currentProject(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "mem mcp: %v\n", err)
		return 1
	}
	srv := mcpserver.New(mcpserver.Deps{
		Store:   st,
		Memory:  &memory.Service{Store: st, WikiRoot: cfg.WikiDir(), Now: time.Now},
		Current: current,
		Now:     time.Now,
		Log:     log,
		Version: version,
	})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		log.Error("mcp server stopped", "err", err)
		fmt.Fprintf(stderr, "mem mcp: %v\n", err)
		return 1
	}
	return 0
}

func currentProject(cfg config.Config) (project.Project, error) {
	dir := os.Getenv("CLAUDE_PROJECT_DIR")
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return project.Project{}, err
		}
		dir = wd
	}
	return project.Resolve(dir, cfg)
}
