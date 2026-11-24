package capture

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/andreabreu76/mem-engine/internal/config"
	"github.com/andreabreu76/mem-engine/internal/project"
	"github.com/andreabreu76/mem-engine/internal/redact"
	"github.com/andreabreu76/mem-engine/internal/store"
)

const (
	eventSessionStart       = "SessionStart"
	eventUserPromptSubmit   = "UserPromptSubmit"
	eventPostToolUse        = "PostToolUse"
	eventPostToolUseFailure = "PostToolUseFailure"
	eventStop               = "Stop"
	eventPreCompact         = "PreCompact"
	eventPostCompact        = "PostCompact"
	eventSessionEnd         = "SessionEnd"

	titleMaxRunes = 120
)

var (
	kinds            = map[string]bool{"session_start": true, "prompt": true, "tool": true, "tool_error": true, "stop": true, "compact_start": true, "compact": true}
	descriptorFields = []string{"file_path", "notebook_path", "command", "pattern", "url", "description", "query"}
	fileFields       = []string{"file_path", "notebook_path"}
	fileTools        = map[string]bool{"Edit": true, "Write": true, "MultiEdit": true, "NotebookEdit": true}
)

func Build(event string, p Payload, proj project.Project, cfg config.Config, r *redact.Redactor, now time.Time) (Op, bool, error) {
	op := Op{
		Project: store.ProjectRow{Key: proj.Key, Name: proj.Name, Dir: proj.Dir},
		Session: store.SessionEvent{SessionID: p.SessionID, Project: proj.Key, Cwd: p.Cwd, At: now},
	}
	clean := cleaner(r)
	obs := store.Observation{SessionID: p.SessionID, Project: proj.Key, CreatedAt: now}
	var title, body string
	switch event {
	case eventSessionStart:
		op.Session.Source = p.Source
		obs.Kind, title = "session_start", "session start ("+p.Source+")"
	case eventUserPromptSubmit:
		body = clean(p.Prompt)
		obs.Kind, title = "prompt", firstLine(body)
	case eventPostToolUse, eventPostToolUseFailure:
		if skipTool(p.ToolName, cfg.SkipTools) {
			return Op{}, false, nil
		}
		fields := stringFields(p.ToolInput)
		obs.Kind, obs.ToolName, title = "tool", p.ToolName, p.ToolName
		if desc := firstOf(fields, descriptorFields); desc != "" {
			title += ": " + firstLine(clean(desc))
		}
		result := "output: " + responseText(p.ToolResponse)
		if event == eventPostToolUseFailure {
			obs.Kind, result = "tool_error", "error: "+p.Error
		}
		body = clean("input: " + compactJSON(p.ToolInput) + "\n" + result)
		if fileTools[p.ToolName] {
			if file := firstOf(fields, fileFields); file != "" {
				obs.Files = []string{file}
			}
		}
	case eventStop:
		body = clean(p.LastAssistantMessage)
		obs.Kind, title = "stop", "assistant: "+firstLine(body)
	case eventPreCompact:
		obs.Kind, title = "compact_start", "compact ("+p.Trigger+")"
	case eventPostCompact:
		body = clean(p.CompactSummary)
		obs.Kind, title = "compact", "compact summary"
	case eventSessionEnd:
		op.Session.End, op.Session.EndReason = true, p.Reason
		return scrub(r, op), true, nil
	default:
		return Op{}, false, fmt.Errorf("unknown hook event %q", event)
	}
	obs.Title = title
	obs.Body = redact.Truncate(body, cfg.MaxObservationBytes)
	op.Obs = &obs
	return scrub(r, op), true, nil
}

func scrub(r *redact.Redactor, op Op) Op {
	clean := cleaner(r)
	op.Session.Cwd = clean(op.Session.Cwd)
	op.Session.Source = clean(op.Session.Source)
	op.Session.EndReason = clean(op.Session.EndReason)
	if op.Obs == nil {
		return op
	}
	obs := *op.Obs
	obs.Title = clean(obs.Title)
	obs.Body = clean(obs.Body)
	obs.ToolName = clean(obs.ToolName)
	if obs.Files != nil {
		files := make([]string, len(obs.Files))
		for i, f := range obs.Files {
			files[i] = clean(f)
		}
		obs.Files = files
	}
	op.Obs = &obs
	return op
}

func cleaner(r *redact.Redactor) func(string) string {
	return func(s string) string { return r.Clean(strings.ToValidUTF8(s, "\uFFFD")) }
}

func skipTool(name string, patterns []string) bool {
	for _, pattern := range patterns {
		if ok, err := path.Match(pattern, name); ok && err == nil {
			return true
		}
	}
	return false
}

func stringFields(raw json.RawMessage) map[string]string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	out := make(map[string]string, len(fields))
	for k, v := range fields {
		var s string
		if json.Unmarshal(v, &s) == nil {
			out[k] = s
		}
	}
	return out
}

func firstOf(fields map[string]string, keys []string) string {
	for _, k := range keys {
		if v := fields[k]; v != "" {
			return v
		}
	}
	return ""
}

func compactJSON(raw json.RawMessage) string {
	if !json.Valid(raw) {
		return string(raw)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	var open []json.Delim
	var counts []int
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return buf.String()
		}
		if err != nil {
			return string(raw)
		}
		if d, ok := tok.(json.Delim); ok && (d == '}' || d == ']') {
			buf.WriteByte(byte(d))
			open, counts = open[:len(open)-1], counts[:len(counts)-1]
			continue
		}
		if top := len(open) - 1; top >= 0 {
			switch n := counts[top]; {
			case n > 0 && open[top] == '{' && n%2 == 1:
				buf.WriteByte(':')
			case n > 0:
				buf.WriteByte(',')
			}
			counts[top]++
		}
		switch v := tok.(type) {
		case json.Delim:
			buf.WriteByte(byte(v))
			open, counts = append(open, v), append(counts, 0)
		case string:
			if err := enc.Encode(v); err != nil {
				return string(raw)
			}
			buf.Truncate(buf.Len() - 1)
		case json.Number:
			buf.WriteString(v.String())
		case bool:
			buf.WriteString(strconv.FormatBool(v))
		case nil:
			buf.WriteString("null")
		}
	}
}

func responseText(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return ""
	}
	var s string
	if trimmed[0] == '"' && json.Unmarshal(trimmed, &s) == nil {
		return s
	}
	return compactJSON(trimmed)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if utf8.RuneCountInString(s) <= titleMaxRunes {
		return s
	}
	n := 0
	for i := range s {
		if n == titleMaxRunes {
			return s[:i]
		}
		n++
	}
	return s
}
