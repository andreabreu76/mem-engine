package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/andreabreu76/mem-engine/internal/config"
	"github.com/andreabreu76/mem-engine/internal/store"
)

func runGC(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintf(stderr, "mem gc: unexpected arguments\n\n%s", usage)
		return 2
	}
	deleted, err := gc()
	if err != nil {
		fmt.Fprintf(stderr, "mem gc: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "deleted %d observations\n", deleted)
	return 0
}

func gc() (int64, error) {
	cfg, err := config.Load()
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(cfg.Home, 0o700); err != nil {
		return 0, err
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return 0, err
	}
	defer st.Close()
	deleted, _, err := st.GC(context.Background(), time.Now(), time.Duration(cfg.RetentionDays)*24*time.Hour, true)
	return deleted, err
}
