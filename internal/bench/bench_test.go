package bench

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andreabreu76/mem-engine/internal/handoff"
	"github.com/andreabreu76/mem-engine/internal/mcpserver"
	"github.com/andreabreu76/mem-engine/internal/project"
	"github.com/andreabreu76/mem-engine/internal/store"
)

var billing = project.Project{Key: "/home/dev/src/github.com/acme/billing-api", Name: "billing-api", Dir: "billing-api-abc123"}

func setEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MEM_ENGINE_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
}

func TestNativeMemoryPath(t *testing.T) {
	setEnv(t)
	got := NativeMemoryPath("/home/dev", "/home/dev/src/github.com/acme/billing-api")
	want := filepath.Join("/home/dev", ".claude", "projects", "-home-dev-src-github-com-acme-billing-api", "memory", "MEMORY.md")
	if got != want {
		t.Errorf("NativeMemoryPath = %q, want %q", got, want)
	}
	got = NativeMemoryPath("/home/dev", "/home/dev/src/acme/billing_api v2/ção")
	want = filepath.Join("/home/dev", ".claude", "projects", "-home-dev-src-acme-billing-api-v2---o", "memory", "MEMORY.md")
	if got != want {
		t.Errorf("NativeMemoryPath = %q, want %q", got, want)
	}
}

func TestNativeLoadedBytesCaps(t *testing.T) {
	setEnv(t)
	line := "- retry policy for payments\n"
	if got := NativeLoadedBytes([]byte(strings.Repeat(line, 300))); got != 200*len(line) {
		t.Errorf("300 lines = %d, want %d", got, 200*len(line))
	}
	if got := NativeLoadedBytes([]byte(strings.Repeat("a", 40*1024))); got != 25600 {
		t.Errorf("40 KB line = %d, want 25600", got)
	}
	tail := strings.Repeat(line, 199) + "last line without newline"
	if got := NativeLoadedBytes([]byte(tail)); got != len(tail) {
		t.Errorf("final line without newline = %d, want %d", got, len(tail))
	}
	if got := NativeLoadedBytes([]byte(tail + "\nline 201\n")); got != len(tail)+1 {
		t.Errorf("200 lines then more = %d, want %d", got, len(tail)+1)
	}
	if got := NativeLoadedBytes(nil); got != 0 {
		t.Errorf("empty = %d", got)
	}
	runes := strings.Repeat("a", 25599) + "ção"
	if got := NativeLoadedBytes([]byte(runes)); got != 25599 {
		t.Errorf("rune at the cap = %d, want 25599", got)
	}
}

func TestResultTokens(t *testing.T) {
	setEnv(t)
	r := Result{NativeBytes: 8003, HandoffBytes: 401, SessionOpenBytes: 3999}
	native, hand, open := r.Tokens()
	if native != 2000 || hand != 100 || open != 999 || r.MemEngineTokens() != 1100 {
		t.Errorf("Tokens = %d, %d, %d, total %d", native, hand, open, r.MemEngineTokens())
	}
}

func TestSkillDescriptionMatchesPlugin(t *testing.T) {
	setEnv(t)
	b, err := os.ReadFile(filepath.Join("..", "..", "plugins", "mem-engine", "skills", "mem-engine", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	front := strings.Split(string(b), "---")[1]
	if !strings.Contains(front, "\nname: "+SkillName+"\n") || !strings.Contains(front, "\ndescription: "+SkillDescription+"\n") {
		t.Errorf("SKILL.md frontmatter %q does not match SkillName/SkillDescription", front)
	}
}

func TestSessionOpenBytes(t *testing.T) {
	setEnv(t)
	tools, err := mcpserver.ToolsPayload(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got, err := SessionOpenBytes(context.Background())
	if err != nil || got != len(tools)+len(SkillName)+len(SkillDescription) {
		t.Errorf("SessionOpenBytes = %d, %v", got, err)
	}
	r, err := Run(context.Background(), nil, billing, filepath.Join(t.TempDir(), "MEMORY.md"))
	if err != nil || r.SessionOpenBytes != got {
		t.Errorf("Run SessionOpenBytes = %d, %v, want %d", r.SessionOpenBytes, err, got)
	}
}

func TestRunComparesSizes(t *testing.T) {
	setEnv(t)
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	row := store.ProjectRow{Key: billing.Key, Name: billing.Name, Dir: billing.Dir}
	at := time.Date(2026, 11, 20, 14, 2, 0, 0, time.UTC)
	if err := st.EnsureProject(ctx, row); err != nil {
		t.Fatal(err)
	}
	if err := st.TouchSession(ctx, store.SessionEvent{SessionID: "s1", Project: billing.Key, At: at}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertObservation(ctx, store.Observation{SessionID: "s1", Project: billing.Key, Kind: "prompt", Title: "prompt", Body: "fix the retry loop", CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertMemory(ctx, store.Memory{ID: "m-000000000001", Project: billing.Key, Title: "Invoices use UTC", Tags: []string{}, Updated: at}); err != nil {
		t.Fatal(err)
	}

	nativePath := filepath.Join(t.TempDir(), "MEMORY.md")
	var native bytes.Buffer
	for i := range 250 {
		native.WriteString("- note " + strings.Repeat("x", i%40) + "\n")
	}
	if err := os.WriteFile(nativePath, native.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Run(ctx, st, billing, nativePath)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	in, err := handoff.Collect(ctx, st, row, "", "startup")
	if err != nil {
		t.Fatal(err)
	}
	text := handoff.Build(in)
	if !strings.Contains(text, "fix the retry loop") || !strings.Contains(text, "Invoices use UTC") {
		t.Fatalf("handoff = %q", text)
	}
	open, err := SessionOpenBytes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := Result{NativePath: nativePath, NativeBytes: NativeLoadedBytes(native.Bytes()), HandoffBytes: len(text), SessionOpenBytes: open}
	if got != want {
		t.Errorf("Run = %+v, want %+v", got, want)
	}
	if got.NativeBytes >= native.Len() || got.HandoffBytes >= got.NativeBytes {
		t.Errorf("sizes = %+v, file = %d", got, native.Len())
	}
}

func TestRunWithoutStoreOrNative(t *testing.T) {
	setEnv(t)
	nativePath := filepath.Join(t.TempDir(), "missing", "MEMORY.md")
	got, err := Run(context.Background(), nil, billing, nativePath)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	minimal := handoff.Build(handoff.Input{Project: store.ProjectRow{Key: billing.Key, Name: billing.Name, Dir: billing.Dir}, Source: "startup"})
	open, err := SessionOpenBytes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := Result{NativePath: nativePath, HandoffBytes: len(minimal), SessionOpenBytes: open}
	if got != want {
		t.Errorf("Run = %+v, want %+v", got, want)
	}
}

func TestRunNativeUnreadable(t *testing.T) {
	setEnv(t)
	if _, err := Run(context.Background(), nil, billing, t.TempDir()); err == nil {
		t.Error("Run on a directory: want error")
	}
}
