package setup

const memCommandPrefix = "mem hook "

type HookEntry struct {
	Event, Matcher string
	Async          bool
	Timeout        int
}

func Hooks() []HookEntry {
	return []HookEntry{
		{Event: "SessionStart", Timeout: 10},
		{Event: "UserPromptSubmit", Async: true},
		{Event: "PostToolUse", Matcher: "*", Async: true},
		{Event: "PostToolUseFailure", Matcher: "*", Async: true},
		{Event: "Stop", Async: true},
		{Event: "PreCompact", Async: true},
		{Event: "PostCompact", Async: true},
		{Event: "SessionEnd", Timeout: 2},
	}
}

func (h HookEntry) Command() string {
	return memCommandPrefix + h.Event
}

func (h HookEntry) group() map[string]any {
	cmd := map[string]any{"type": "command", "command": h.Command()}
	if h.Async {
		cmd["async"] = true
	}
	if h.Timeout != 0 {
		cmd["timeout"] = h.Timeout
	}
	g := map[string]any{"hooks": []any{cmd}}
	if h.Matcher != "" {
		g["matcher"] = h.Matcher
	}
	return g
}

func HooksConfig() map[string]any {
	cfg := map[string]any{}
	for _, h := range Hooks() {
		groups, _ := cfg[h.Event].([]any)
		cfg[h.Event] = append(groups, h.group())
	}
	return cfg
}
