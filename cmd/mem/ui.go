package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/andreabreu76/mem-engine/internal/applog"
	"github.com/andreabreu76/mem-engine/internal/config"
	"github.com/andreabreu76/mem-engine/internal/memory"
	"github.com/andreabreu76/mem-engine/internal/store"
	"github.com/andreabreu76/mem-engine/internal/ui"
)

func runUI(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mem ui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	port := fs.Int("port", 0, "port to listen on 127.0.0.1 (default ui_port from config.toml, 47600)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "mem ui: unexpected arguments\n\n%s", usage)
		return 2
	}
	portSet := false
	fs.Visit(func(f *flag.Flag) { portSet = portSet || f.Name == "port" })
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "mem ui: %v\n", err)
		return 1
	}
	if !portSet {
		*port = cfg.UIPort
	}
	if *port < 1 || *port > 65535 {
		if !portSet {
			fmt.Fprintf(stderr, "mem ui: ui_port in config.toml must be between 1 and 65535, got %d\n", *port)
			return 1
		}
		fmt.Fprintf(stderr, "mem ui: --port must be between 1 and 65535, got %d\n", *port)
		return 2
	}
	if err := os.MkdirAll(cfg.Home, 0o700); err != nil {
		fmt.Fprintf(stderr, "mem ui: %v\n", err)
		return 1
	}
	log := slog.New(slog.DiscardHandler)
	if l, closer, err := applog.Open(cfg.LogDir()); err == nil {
		defer closer.Close()
		log = l
	}
	st, err := store.Open(cfg.DBPath())
	if errors.Is(err, store.ErrSchemaTooNew) {
		fmt.Fprintf(stderr, "mem ui: %v, upgrade mem\n", err)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "mem ui: open database: %v\n", err)
		return 1
	}
	defer st.Close()

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(*port))
	ln, err := net.Listen("tcp4", addr)
	if isAddrInUse(err) {
		fmt.Fprintf(stderr, "mem ui: port %d is already in use on 127.0.0.1, choose another with --port or ui_port in config.toml\n", *port)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "mem ui: listen on %s: %v\n", addr, err)
		return 1
	}
	srv := &ui.Server{
		Store:  st,
		Memory: &memory.Service{Store: st, WikiRoot: cfg.WikiDir(), Now: time.Now},
		Cfg:    cfg,
		Token:  ui.NewToken(),
		Port:   *port,
		Log:    log,
	}
	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	served := make(chan error, 1)
	go func() { served <- httpSrv.Serve(ln) }()
	fmt.Fprintf(stdout, "mem ui: http://%s/?token=%s\n", addr, srv.Token)

	select {
	case err := <-served:
		log.Error("ui server stopped", "err", err)
		fmt.Fprintf(stderr, "mem ui: %v\n", err)
		return 1
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("ui shutdown", "err", err)
		fmt.Fprintf(stderr, "mem ui: shutdown: %v\n", err)
		return 1
	}
	return 0
}
