package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/andreabreu76/mem-engine/internal/store"
)

func TestGCCommand(t *testing.T) {
	home := setHome(t)
	st, err := store.Open(filepath.Join(home, "mem.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	ctx := context.Background()
	now := time.Now()
	for _, at := range []time.Time{now.Add(-100 * 24 * time.Hour), now.Add(-200 * 24 * time.Hour), now.Add(-time.Hour)} {
		if _, err := st.InsertObservation(ctx, store.Observation{SessionID: "s1", Project: "billing-api", Kind: "prompt", Title: "t", Body: "b", CreatedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := st.GC(ctx, now, 90*24*time.Hour, false); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{now.Add(-100 * 24 * time.Hour), now.Add(-200 * 24 * time.Hour)} {
		if _, err := st.InsertObservation(ctx, store.Observation{SessionID: "s1", Project: "billing-api", Kind: "prompt", Title: "t", Body: "b", CreatedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()

	var out, errb bytes.Buffer
	if code := run([]string{"gc"}, nil, &out, &errb); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errb.String())
	}
	if out.String() != "deleted 2 observations\n" {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestGCCommandEmptyHome(t *testing.T) {
	home := setHome(t)
	if err := os.Remove(home); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"gc"}, nil, &out, &errb); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errb.String())
	}
	if out.String() != "deleted 0 observations\n" {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestGCCommandBrokenConfig(t *testing.T) {
	home := setHome(t)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("retention_days = ["), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"gc"}, nil, &out, &errb); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if out.Len() != 0 || errb.Len() == 0 {
		t.Errorf("stdout = %q, stderr = %q", out.String(), errb.String())
	}
}

func TestGCCommandRejectsArgs(t *testing.T) {
	setHome(t)
	var out, errb bytes.Buffer
	if code := run([]string{"gc", "now"}, nil, &out, &errb); code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
}
