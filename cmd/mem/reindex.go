package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/andreabreu76/mem-engine/internal/config"
	"github.com/andreabreu76/mem-engine/internal/memory"
	"github.com/andreabreu76/mem-engine/internal/store"
)

func runReindex(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintf(stderr, "mem reindex: unexpected arguments\n\n%s", usage)
		return 2
	}
	rep, err := reindex()
	if err != nil {
		fmt.Fprintf(stderr, "mem reindex: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "indexed %d, skipped %d\n", rep.Indexed, len(rep.Skipped))
	for _, s := range rep.Skipped {
		fmt.Fprintln(stdout, s)
	}
	return 0
}

func reindex() (memory.ReindexReport, error) {
	cfg, err := config.Load()
	if err != nil {
		return memory.ReindexReport{}, err
	}
	if err := os.MkdirAll(cfg.Home, 0o700); err != nil {
		return memory.ReindexReport{}, err
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return memory.ReindexReport{}, err
	}
	defer st.Close()
	svc := &memory.Service{Store: st, WikiRoot: cfg.WikiDir(), Now: time.Now}
	return svc.Reindex(context.Background())
}
