package setup

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type hookCmd struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Async   *bool  `json:"async"`
	Timeout *int   `json:"timeout"`
}

type hookGroup struct {
	Matcher *string   `json:"matcher"`
	Hooks   []hookCmd `json:"hooks"`
}

func decodeHooks(t *testing.T, data []byte) map[string][]hookGroup {
	t.Helper()
	var s struct {
		Hooks map[string][]hookGroup `json:"hooks"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
	return s.Hooks
}

func decodeMap(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
	return m
}

func memCommands(groups []hookGroup) []hookCmd {
	var out []hookCmd
	for _, g := range groups {
		for _, h := range g.Hooks {
			if strings.HasPrefix(h.Command, "mem hook ") {
				out = append(out, h)
			}
		}
	}
	return out
}

func TestMergeIntoEmpty(t *testing.T) {
	updated, changed, err := MergeHooks([]byte("{}"))
	if err != nil {
		t.Fatalf("MergeHooks: %v", err)
	}
	if !changed {
		t.Fatal("changed = false")
	}
	if !strings.HasPrefix(string(updated), "{\n  \"hooks\": {\n    \"") {
		t.Errorf("not indented with 2 spaces:\n%s", updated)
	}
	hooks := decodeHooks(t, updated)
	want := map[string]struct {
		matcher string
		async   bool
		timeout int
	}{
		"SessionStart":       {"", false, 10},
		"UserPromptSubmit":   {"", true, 0},
		"PostToolUse":        {"*", true, 0},
		"PostToolUseFailure": {"*", true, 0},
		"Stop":               {"", true, 0},
		"PreCompact":         {"", true, 0},
		"PostCompact":        {"", true, 0},
		"SessionEnd":         {"", false, 2},
	}
	if len(hooks) != len(want) {
		t.Fatalf("events = %d, want %d: %s", len(hooks), len(want), updated)
	}
	for event, w := range want {
		groups := hooks[event]
		if len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Fatalf("%s: groups = %+v", event, groups)
		}
		g, h := groups[0], groups[0].Hooks[0]
		if w.matcher == "" && g.Matcher != nil {
			t.Errorf("%s: matcher = %q, want omitted", event, *g.Matcher)
		}
		if w.matcher != "" && (g.Matcher == nil || *g.Matcher != w.matcher) {
			t.Errorf("%s: matcher = %v, want %q", event, g.Matcher, w.matcher)
		}
		if h.Type != "command" || h.Command != "mem hook "+event {
			t.Errorf("%s: hook = %+v", event, h)
		}
		if w.async != (h.Async != nil) || (h.Async != nil && !*h.Async) {
			t.Errorf("%s: async = %v, want %v", event, h.Async, w.async)
		}
		if w.timeout == 0 && h.Timeout != nil {
			t.Errorf("%s: timeout = %d, want omitted", event, *h.Timeout)
		}
		if w.timeout != 0 && (h.Timeout == nil || *h.Timeout != w.timeout) {
			t.Errorf("%s: timeout = %v, want %d", event, h.Timeout, w.timeout)
		}
	}
}

func TestMergeEmptyInputIsEmptyObject(t *testing.T) {
	for _, in := range []string{"", "  \n"} {
		updated, changed, err := MergeHooks([]byte(in))
		if err != nil || !changed {
			t.Fatalf("MergeHooks(%q) = changed %v, err %v", in, changed, err)
		}
		if len(decodeHooks(t, updated)) != len(Hooks()) {
			t.Errorf("MergeHooks(%q) = %s", in, updated)
		}
	}
}

func TestMergePreservesOtherHooks(t *testing.T) {
	in := `{
  "model": "opus",
  "permissions": {"allow": ["Bash(go test:*)"]},
  "hooks": {
    "PostToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "lint-hook --fast"}]},
      {"matcher": "*", "hooks": [
        {"type": "command", "command": "mem hook PostToolUse", "async": true},
        {"type": "command", "command": "audit && log <x>", "timeout": 5}
      ]}
    ],
    "Notification": [{"hooks": [{"type": "command", "command": "notify-send done"}]}]
  }
}`
	updated, changed, err := MergeHooks([]byte(in))
	if err != nil {
		t.Fatalf("MergeHooks: %v", err)
	}
	if !changed {
		t.Fatal("changed = false")
	}
	if !strings.Contains(string(updated), `"audit && log <x>"`) {
		t.Errorf("third-party command escaped or lost:\n%s", updated)
	}
	m := decodeMap(t, updated)
	if m["model"] != "opus" {
		t.Errorf("model = %v", m["model"])
	}
	if allow := m["permissions"].(map[string]any)["allow"].([]any); len(allow) != 1 || allow[0] != "Bash(go test:*)" {
		t.Errorf("permissions = %v", m["permissions"])
	}
	hooks := decodeHooks(t, updated)
	post := hooks["PostToolUse"]
	if len(post) != 3 {
		t.Fatalf("PostToolUse groups = %+v", post)
	}
	if *post[0].Matcher != "Bash" || len(post[0].Hooks) != 1 || post[0].Hooks[0].Command != "lint-hook --fast" {
		t.Errorf("third-party group = %+v", post[0])
	}
	if *post[1].Matcher != "*" || len(post[1].Hooks) != 1 || post[1].Hooks[0].Command != "audit && log <x>" || *post[1].Hooks[0].Timeout != 5 {
		t.Errorf("mixed group = %+v", post[1])
	}
	if mem := memCommands(post); len(mem) != 1 || mem[0].Command != "mem hook PostToolUse" {
		t.Errorf("mem entries = %+v", mem)
	}
	if n := hooks["Notification"]; len(n) != 1 || n[0].Hooks[0].Command != "notify-send done" {
		t.Errorf("Notification = %+v", n)
	}
}

func TestMergeIdempotent(t *testing.T) {
	in := `{"model": "opus", "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "say bye"}]}]}}`
	first, changed, err := MergeHooks([]byte(in))
	if err != nil || !changed {
		t.Fatalf("first merge: changed %v, err %v", changed, err)
	}
	second, changed, err := MergeHooks(first)
	if err != nil {
		t.Fatalf("second merge: %v", err)
	}
	if changed {
		t.Error("second merge changed = true")
	}
	if !bytes.Equal(first, second) {
		t.Errorf("bytes differ:\n%s\n---\n%s", first, second)
	}
}

func TestMergeUnchangedKeepsOriginalBytes(t *testing.T) {
	first, _, err := MergeHooks([]byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, first); err != nil {
		t.Fatal(err)
	}
	out, changed, err := MergeHooks(compact.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if changed || !bytes.Equal(out, compact.Bytes()) {
		t.Errorf("changed = %v, out = %s", changed, out)
	}
}

func TestMergeReplacesOldMemHooks(t *testing.T) {
	in := `{"hooks": {
  "Stop": [{"hooks": [{"type": "command", "command": "mem hook Stop", "timeout": 99}]}],
  "SessionStart": [
    {"hooks": [{"type": "command", "command": "mem hook SessionStart"}]},
    {"matcher": "startup", "hooks": [{"type": "command", "command": "mem hook SessionStart --old"}]}
  ],
  "Notification": [{"hooks": [{"type": "command", "command": "mem hook Notification"}]}],
  "SubagentStop": [{"hooks": []}]
}}`
	updated, changed, err := MergeHooks([]byte(in))
	if err != nil || !changed {
		t.Fatalf("changed %v, err %v", changed, err)
	}
	hooks := decodeHooks(t, updated)
	if _, ok := hooks["Notification"]; ok {
		t.Errorf("Notification kept: %+v", hooks["Notification"])
	}
	if g := hooks["SubagentStop"]; len(g) != 1 || len(g[0].Hooks) != 0 {
		t.Errorf("SubagentStop = %+v", g)
	}
	for _, h := range Hooks() {
		groups := hooks[h.Event]
		if len(groups) != 1 {
			t.Errorf("%s: groups = %+v", h.Event, groups)
			continue
		}
		mem := memCommands(groups)
		if len(mem) != 1 || mem[0].Command != "mem hook "+h.Event {
			t.Errorf("%s: mem entries = %+v", h.Event, mem)
		}
	}
	if stop := hooks["Stop"][0].Hooks[0]; stop.Timeout != nil {
		t.Errorf("old Stop timeout kept: %d", *stop.Timeout)
	}
}

func TestMergeRejectsInvalidSettings(t *testing.T) {
	for _, in := range []string{
		`{"model": `,
		`[]`,
		`null`,
		`{} {}`,
		`{"hooks": []}`,
		`{"hooks": {"Stop": {"hooks": []}}}`,
	} {
		if _, _, err := MergeHooks([]byte(in)); err == nil {
			t.Errorf("MergeHooks(%s) err = nil", in)
		}
	}
}

func TestMergeKeepsLargeNumbers(t *testing.T) {
	in := `{"cleanupPeriodDays": 12345678901234567890}`
	updated, _, err := MergeHooks([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), "12345678901234567890") {
		t.Errorf("number rewritten:\n%s", updated)
	}
}

func TestHasMemHooks(t *testing.T) {
	merged, _, err := MergeHooks([]byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		in   string
		want bool
	}{
		{string(merged), true},
		{`{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "mem hook Stop"}]}]}}`, true},
		{`{}`, false},
		{``, false},
		{`{"model": `, false},
		{`{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "memo hook Stop"}]}]}}`, false},
		{`{"hooks": {"Stop": "mem hook Stop"}}`, false},
	}
	for _, c := range cases {
		if got := HasMemHooks([]byte(c.in)); got != c.want {
			t.Errorf("HasMemHooks(%s) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestHooksConfigMatchesMerge(t *testing.T) {
	merged, _, err := MergeHooks([]byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := json.Marshal(HooksConfig())
	if err != nil {
		t.Fatal(err)
	}
	var a, b any
	if err := json.Unmarshal(cfg, &a); err != nil {
		t.Fatal(err)
	}
	b = decodeMap(t, merged)["hooks"]
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	if !bytes.Equal(ab, bb) {
		t.Errorf("HooksConfig = %s\nmerged hooks = %s", ab, bb)
	}
}

func TestSettingsPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	got, err := SettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".claude", "settings.json"); got != want {
		t.Errorf("SettingsPath = %q, want %q", got, want)
	}
}

func TestReadSettingsMissing(t *testing.T) {
	data, exists, err := ReadSettings(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil || exists || data != nil {
		t.Errorf("data %q, exists %v, err %v", data, exists, err)
	}
}

func TestWriteSettingsNewFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".claude")
	path := filepath.Join(dir, "settings.json")
	if err := WriteSettings(path, []byte("{}\n")); err != nil {
		t.Fatalf("WriteSettings: %v", err)
	}
	data, exists, err := ReadSettings(path)
	if err != nil || !exists || string(data) != "{}\n" {
		t.Fatalf("data %q, exists %v, err %v", data, exists, err)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Errorf("backup created for a new file: %v", err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v", fi.Mode().Perm())
	}
}

func TestWriteSettingsBackupAndMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	old := []byte(`{"model": "opus"}`)
	if err := os.WriteFile(path, old, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := WriteSettings(path, []byte("{\n  \"model\": \"sonnet\"\n}\n")); err != nil {
		t.Fatalf("WriteSettings: %v", err)
	}
	bak, err := os.ReadFile(path + ".bak")
	if err != nil || !bytes.Equal(bak, old) {
		t.Errorf("backup = %q, err %v", bak, err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "sonnet") {
		t.Errorf("settings = %q", data)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("leftover files: %v", entries)
	}
	if runtime.GOOS == "windows" {
		return
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o640 {
		t.Errorf("file mode = %v, want 0640", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(path + ".bak"); fi.Mode().Perm() != 0o600 {
		t.Errorf("backup mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestWriteSettingsKeepsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles-settings.json")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "settings.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := WriteSettings(link, []byte("{\"model\": \"opus\"}\n")); err != nil {
		t.Fatalf("WriteSettings: %v", err)
	}
	fi, err := os.Lstat(link)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link replaced: mode %v, err %v", fi.Mode(), err)
	}
	if data, _ := os.ReadFile(target); !strings.Contains(string(data), "opus") {
		t.Errorf("target = %q", data)
	}
	if bak, _ := os.ReadFile(link + ".bak"); string(bak) != "{}" {
		t.Errorf("backup = %q", bak)
	}
}

func TestMCPAddArgs(t *testing.T) {
	want := []string{"mcp", "add", "--scope", "user", "mem-engine", "--", "mem", "mcp"}
	if got := MCPAddArgs(); strings.Join(got, " ") != strings.Join(want, " ") || len(got) != len(want) {
		t.Errorf("MCPAddArgs() = %q, want %q", got, want)
	}
}
