package config

import (
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func writeConfig(t *testing.T, home, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDefaultsWhenFileMissing(t *testing.T) {
	home := t.TempDir()
	c, err := LoadFrom(home)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if c.Home != home {
		t.Errorf("Home = %q, want %q", c.Home, home)
	}
	if c.RetentionDays != 90 {
		t.Errorf("RetentionDays = %d, want 90", c.RetentionDays)
	}
	if c.MaxObservationBytes != 4096 {
		t.Errorf("MaxObservationBytes = %d, want 4096", c.MaxObservationBytes)
	}
	if c.UIPort != 47600 {
		t.Errorf("UIPort = %d, want 47600", c.UIPort)
	}
	if !slices.Equal(c.SkipTools, []string{"*mem-engine*"}) {
		t.Errorf("SkipTools = %v, want [*mem-engine*]", c.SkipTools)
	}
	if got, want := c.DBPath(), filepath.Join(home, "mem.db"); got != want {
		t.Errorf("DBPath = %q, want %q", got, want)
	}
	if got, want := c.WikiDir(), filepath.Join(home, "wiki"); got != want {
		t.Errorf("WikiDir = %q, want %q", got, want)
	}
	if got, want := c.LogDir(), filepath.Join(home, "logs"); got != want {
		t.Errorf("LogDir = %q, want %q", got, want)
	}
	if got, want := c.SpoolDir(), filepath.Join(home, "spool"); got != want {
		t.Errorf("SpoolDir = %q, want %q", got, want)
	}
}

func TestLoadGroupsAndOverrides(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	home := t.TempDir()
	writeConfig(t, home, `retention_days = 30

[[group]]
name = "acme"
prefix = "~/src/acme/"

[[alias]]
path = "~/src/billing-api-old/"
key = "~/src/billing-api"
`)
	c, err := LoadFrom(home)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if c.RetentionDays != 30 {
		t.Errorf("RetentionDays = %d, want 30", c.RetentionDays)
	}
	if c.UIPort != 47600 {
		t.Errorf("UIPort = %d, want default 47600", c.UIPort)
	}
	wantGroups := []Group{{Name: "acme", Prefix: filepath.Join(userHome, "src", "acme")}}
	if !slices.Equal(c.Groups, wantGroups) {
		t.Errorf("Groups = %v, want %v", c.Groups, wantGroups)
	}
	wantAliases := []Alias{{
		Path: filepath.Join(userHome, "src", "billing-api-old"),
		Key:  filepath.Join(userHome, "src", "billing-api"),
	}}
	if !slices.Equal(c.Aliases, wantAliases) {
		t.Errorf("Aliases = %v, want %v", c.Aliases, wantAliases)
	}
}

func TestLoadInvalidTOMLFails(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "retention_days = = 30\n")
	if _, err := LoadFrom(home); err == nil {
		t.Fatal("LoadFrom with invalid TOML returned nil error")
	}
}

func TestLoadUsesEnvHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MEM_ENGINE_HOME", dir)
	writeConfig(t, dir, "ui_port = 47999\n")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Home != dir {
		t.Errorf("Home = %q, want %q", c.Home, dir)
	}
	if c.UIPort != 47999 {
		t.Errorf("UIPort = %d, want 47999", c.UIPort)
	}
}

func TestLoadDefaultsToUserHome(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv("MEM_ENGINE_HOME", "")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := filepath.Join(userHome, ".mem-engine"); c.Home != want {
		t.Errorf("Home = %q, want %q", c.Home, want)
	}
}

func TestExpandHome(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	cases := map[string]string{
		"~":             userHome,
		"~/":            userHome,
		"~/src/acme":    filepath.Join(userHome, "src", "acme"),
		"/home/dev/src": "/home/dev/src",
		"relative/path": "relative/path",
		"~other/src":    "~other/src",
		"":              "",
	}
	for in, want := range cases {
		if got := ExpandHome(in); got != want {
			t.Errorf("ExpandHome(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRetentionClamped(t *testing.T) {
	const day = 24 * time.Hour
	cases := []struct {
		days int
		want time.Duration
	}{
		{0, 0},
		{-5, 0},
		{90, 90 * day},
		{MaxRetentionDays, MaxRetentionDays * day},
		{MaxRetentionDays + 1, MaxRetentionDays * day},
		{math.MaxInt, MaxRetentionDays * day},
	}
	for _, c := range cases {
		if got := (Config{RetentionDays: c.days}).Retention(); got != c.want {
			t.Errorf("Retention(%d) = %v, want %v", c.days, got, c.want)
		}
	}
}
