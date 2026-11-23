package spool

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type entry struct {
	N int `json:"n"`
}

func decode(t *testing.T, raw json.RawMessage) int {
	t.Helper()
	var e entry
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Errorf("unmarshal %s: %v", raw, err)
	}
	return e.N
}

func TestAppendDrain(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spool")
	for i := 1; i <= 3; i++ {
		if err := Append(dir, entry{N: i}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir perm = %o, want 700", perm)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("files = %d, want 3", len(entries))
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			t.Errorf("unexpected file %s", e.Name())
		}
		fi, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s perm = %o, want 600", e.Name(), perm)
		}
	}
	if n, err := Pending(dir); err != nil || n != 3 {
		t.Errorf("Pending = %d, %v, want 3", n, err)
	}

	var got []int
	applied, err := Drain(dir, func(raw json.RawMessage) error {
		got = append(got, decode(t, raw))
		return nil
	})
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if applied != 3 {
		t.Errorf("applied = %d, want 3", applied)
	}
	if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Errorf("applied order = %v, want [1 2 3]", got)
	}
	left, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("files left after drain: %d", len(left))
	}
}

func TestDrainMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "absent")
	applied, err := Drain(dir, func(json.RawMessage) error { return nil })
	if err != nil || applied != 0 {
		t.Errorf("Drain = %d, %v, want 0, nil", applied, err)
	}
	if n, err := Pending(dir); err != nil || n != 0 {
		t.Errorf("Pending = %d, %v, want 0, nil", n, err)
	}
}

func TestDrainPartialFailure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spool")
	for i := 1; i <= 3; i++ {
		if err := Append(dir, entry{N: i}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	errApply := errors.New("database is locked")
	calls := 0
	applied, err := Drain(dir, func(json.RawMessage) error {
		calls++
		if calls == 2 {
			return errApply
		}
		return nil
	})
	if !errors.Is(err, errApply) {
		t.Fatalf("Drain err = %v, want %v", err, errApply)
	}
	if applied != 1 {
		t.Errorf("applied = %d, want 1", applied)
	}
	if n, err := Pending(dir); err != nil || n != 2 {
		t.Errorf("Pending = %d, %v, want 2", n, err)
	}

	var got []int
	applied, err = Drain(dir, func(raw json.RawMessage) error {
		got = append(got, decode(t, raw))
		return nil
	})
	if err != nil || applied != 2 {
		t.Fatalf("second Drain = %d, %v, want 2, nil", applied, err)
	}
	if len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Errorf("second drain order = %v, want [2 3]", got)
	}
}

func TestDrainPartialFailureKeepsRestOfFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "1-1.jsonl"), []byte("{\"n\":1}\n{\"n\":2}\n{\"n\":3}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	applied, err := Drain(dir, func(raw json.RawMessage) error {
		if decode(t, raw) == 2 {
			return errors.New("fail")
		}
		return nil
	})
	if err == nil || applied != 1 {
		t.Fatalf("Drain = %d, %v, want 1 and an error", applied, err)
	}
	if n, err := Pending(dir); err != nil || n != 2 {
		t.Errorf("Pending = %d, %v, want 2", n, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".jsonl") {
		t.Errorf("files after failure = %v, want one .jsonl", entries)
	}
}

func writeAged(t *testing.T, dir, name, content string, age time.Duration) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

func collect(t *testing.T, dir string) ([]int, error) {
	t.Helper()
	var got []int
	_, err := Drain(dir, func(raw json.RawMessage) error {
		got = append(got, decode(t, raw))
		return nil
	})
	return got, err
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestDrainLeavesFreshDrainingAlone(t *testing.T) {
	dir := t.TempDir()
	writeAged(t, dir, "1-1.draining-99999", "{\"n\":1}\n", 0)
	got, err := collect(t, dir)
	if err != nil || len(got) != 0 {
		t.Errorf("Drain applied %v, %v, want nothing", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "1-1.draining-99999")); err != nil {
		t.Errorf("fresh draining file touched: %v", err)
	}
}

func TestDrainRecoversOrphanDraining(t *testing.T) {
	dir := t.TempDir()
	writeAged(t, dir, "1-1.draining-99999", "{\"n\":1}\n{\"n\":2}\n", 2*time.Minute)
	got, err := collect(t, dir)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Errorf("applied %v, want [1 2]", got)
	}
	if left := names(t, dir); len(left) != 0 {
		t.Errorf("files left: %v", left)
	}
}

func TestDrainSkipsOwnPidOrphan(t *testing.T) {
	dir := t.TempDir()
	name := "1-1.draining-" + strconv.Itoa(os.Getpid())
	writeAged(t, dir, name, "{\"n\":1}\n", 2*time.Minute)
	got, err := collect(t, dir)
	if err != nil || len(got) != 0 {
		t.Errorf("Drain applied %v, %v, want nothing", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
		t.Errorf("own-pid orphan touched: %v", err)
	}
}

func TestDrainRecoversOrphanTmp(t *testing.T) {
	dir := t.TempDir()
	writeAged(t, dir, ".spool-111.tmp", "{\"n\":7}\n", 2*time.Minute)
	writeAged(t, dir, ".spool-222.tmp", "", 2*time.Minute)
	writeAged(t, dir, ".spool-333.tmp", "{\"n\":8}\n", 0)
	got, err := collect(t, dir)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(got) != 1 || got[0] != 7 {
		t.Errorf("applied %v, want [7]", got)
	}
	if left := names(t, dir); len(left) != 1 || left[0] != ".spool-333.tmp" {
		t.Errorf("files left = %v, want only the fresh tmp", left)
	}
}

func TestPendingCountsOrphans(t *testing.T) {
	dir := t.TempDir()
	writeAged(t, dir, "1-1.jsonl", "{\"n\":1}\n", 0)
	writeAged(t, dir, "1-2.draining-99999", "{\"n\":2}\n{\"n\":3}\n", 2*time.Minute)
	writeAged(t, dir, ".spool-111.tmp", "{\"n\":4}\n", 2*time.Minute)
	writeAged(t, dir, "1-3.draining-99999", "{\"n\":5}\n", 0)
	writeAged(t, dir, ".spool-222.tmp", "{\"n\":6}\n", 0)
	if n, err := Pending(dir); err != nil || n != 4 {
		t.Errorf("Pending = %d, %v, want 4", n, err)
	}
}

func TestDrainRefreshesClaimAge(t *testing.T) {
	dir := t.TempDir()
	writeAged(t, dir, "1-1.jsonl", "{\"n\":1}\n", 2*time.Minute)
	var claimedAge time.Duration
	_, err := Drain(dir, func(json.RawMessage) error {
		for _, name := range names(t, dir) {
			if strings.Contains(name, ".draining-") {
				info, err := os.Stat(filepath.Join(dir, name))
				if err != nil {
					t.Fatal(err)
				}
				claimedAge = time.Since(info.ModTime())
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if claimedAge > orphanAge/2 {
		t.Errorf("claimed file age = %v, want refreshed", claimedAge)
	}
}

func TestDrainConcurrent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spool")
	const total = 200

	var mu sync.Mutex
	seen := make(map[int]int)
	apply := func(raw json.RawMessage) error {
		n := decode(t, raw)
		mu.Lock()
		seen[n]++
		mu.Unlock()
		return nil
	}

	for i := range total / 2 {
		if err := Append(dir, entry{N: i}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 8)
	wg.Go(func() {
		<-start
		for i := total / 2; i < total; i++ {
			if err := Append(dir, entry{N: i}); err != nil {
				errs <- err
				return
			}
		}
	})
	for range 2 {
		wg.Go(func() {
			<-start
			for range 5 {
				if _, err := Drain(dir, apply); err != nil {
					errs <- err
					return
				}
			}
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if _, err := Drain(dir, apply); err != nil {
		t.Fatalf("final Drain: %v", err)
	}

	if len(seen) != total {
		t.Errorf("distinct lines applied = %d, want %d", len(seen), total)
	}
	for n, c := range seen {
		if c != 1 {
			t.Errorf("line %d applied %d times", n, c)
		}
	}
	if n, err := Pending(dir); err != nil || n != 0 {
		t.Errorf("Pending = %d, %v, want 0", n, err)
	}
}

func TestDrainHelperProcess(t *testing.T) {
	dir := os.Getenv("SPOOL_DRAIN_HELPER_DIR")
	if dir == "" {
		t.Skip("helper for TestDrainConcurrentProcesses")
	}
	for range 3 {
		_, err := Drain(dir, func(raw json.RawMessage) error {
			fmt.Printf("applied %d\n", decode(t, raw))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestDrainConcurrentProcesses(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spool")
	const published, orphans = 150, 10
	for i := range published {
		if err := Append(dir, entry{N: i}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range orphans {
		n := published + 2*i
		writeAged(t, dir, fmt.Sprintf("1-%d.draining-99999", i), fmt.Sprintf("{\"n\":%d}\n", n), 2*time.Minute)
		writeAged(t, dir, fmt.Sprintf(".spool-orphan%d.tmp", i), fmt.Sprintf("{\"n\":%d}\n", n+1), 2*time.Minute)
	}
	total := published + 2*orphans

	const procs = 4
	outs := make([][]byte, procs)
	var wg sync.WaitGroup
	for p := range procs {
		wg.Go(func() {
			cmd := exec.Command(os.Args[0], "-test.run=^TestDrainHelperProcess$", "-test.count=1")
			cmd.Env = append(os.Environ(), "SPOOL_DRAIN_HELPER_DIR="+dir)
			out, err := cmd.Output()
			if err != nil {
				t.Errorf("helper %d: %v\n%s", p, err, out)
			}
			outs[p] = out
		})
	}
	wg.Wait()

	seen := make(map[int]int)
	for _, out := range outs {
		for line := range strings.SplitSeq(string(out), "\n") {
			if v, ok := strings.CutPrefix(line, "applied "); ok {
				n, err := strconv.Atoi(v)
				if err != nil {
					t.Fatal(err)
				}
				seen[n]++
			}
		}
	}
	if len(seen) != total {
		t.Errorf("distinct lines applied = %d, want %d", len(seen), total)
	}
	for n, c := range seen {
		if c != 1 {
			t.Errorf("line %d applied %d times", n, c)
		}
	}
	if left := names(t, dir); len(left) != 0 {
		t.Errorf("files left: %v", left)
	}
}
