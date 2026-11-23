package handoff

import (
	"context"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/andreabreu76/mem-engine/internal/store"
)

const (
	MaxChars = 1200

	sourceCompact  = "compact"
	maxMemories    = 5
	nameRunes      = 60
	promptRunes    = 120
	assistantRunes = 160
	compactRunes   = 480
	fileRunes      = 40
	idRunes        = 24
	titleRunes     = 64
	dateLayout     = "2006-01-02 15:04"
	ellipsis       = "…"
	lookupLine     = "Lookup: search -> timeline -> get (memory tools of this project)."
)

type Input struct {
	Project  store.ProjectRow
	Source   string
	Last     *store.SessionSummary
	Compact  string
	Memories []store.Memory
}

func Collect(ctx context.Context, st *store.Store, p store.ProjectRow, sessionID, source string) (Input, error) {
	in := Input{Project: p, Source: source}
	if source == sourceCompact {
		summary, ok, err := st.LatestCompactSummary(ctx, sessionID)
		if err != nil {
			return in, err
		}
		if ok && strings.TrimSpace(summary) != "" {
			in.Compact = summary
		}
	}
	if in.Compact == "" {
		last, ok, err := st.LastSession(ctx, p.Key, sessionID)
		if err != nil {
			return in, err
		}
		if ok {
			in.Last = &last
		}
	}
	ms, err := st.RecentMemories(ctx, store.CurrentScope(p.Key), maxMemories)
	if err != nil {
		return in, err
	}
	in.Memories = ms
	return in, nil
}

func Build(in Input) string {
	lines := []string{"mem-engine | project " + clip(in.Project.Name, nameRunes)}
	if line := sessionLine(in); line != "" {
		lines = append(lines, line)
	}
	if line := memoriesLine(in.Memories); line != "" {
		lines = append(lines, line)
	}
	return fit(strings.Join(lines, "\n"))
}

func sessionLine(in Input) string {
	if in.Source == sourceCompact {
		if summary := clip(in.Compact, compactRunes); summary != "" {
			return "Compact summary: " + summary
		}
	}
	if in.Last == nil {
		return ""
	}
	var parts []string
	if prompt := clip(in.Last.LastPrompt, promptRunes); prompt != "" {
		parts = append(parts, `asked "`+prompt+`"`)
	}
	var files []string
	for _, f := range in.Last.Files {
		if name := clipLeft(displayPath(in.Project.Key, f), fileRunes); name != "" {
			files = append(files, name)
		}
	}
	if len(files) > 0 {
		parts = append(parts, "edited "+strings.Join(files, ", "))
	}
	if answer := clip(in.Last.LastAssistant, assistantRunes); answer != "" {
		parts = append(parts, `stopped at "`+answer+`"`)
	}
	line := "Last session " + in.Last.StartedAt.UTC().Format(dateLayout)
	if len(parts) > 0 {
		line += ": " + strings.Join(parts, "; ")
	}
	return line
}

func memoriesLine(ms []store.Memory) string {
	var items []string
	for _, m := range ms {
		if len(items) == maxMemories {
			break
		}
		item := strings.TrimSpace(clip(m.ID, idRunes) + " " + clip(m.Title, titleRunes))
		if item != "" {
			items = append(items, item)
		}
	}
	if len(items) == 0 {
		return ""
	}
	return "Memories: " + strings.Join(items, "; ")
}

func displayPath(projectKey, file string) string {
	rel, err := filepath.Rel(projectKey, file)
	if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return rel
	}
	return filepath.Base(file)
}

func fit(body string) string {
	budget := MaxChars - len(lookupLine) - 1
	if len(body) > budget {
		body = cutBytes(body, budget-len(ellipsis)) + ellipsis
	}
	return body + "\n" + lookupLine
}

func normalize(s string) string {
	return strings.Join(strings.Fields(strings.ToValidUTF8(s, "�")), " ")
}

func clip(s string, n int) string {
	s = normalize(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n-1]) + ellipsis
}

func clipLeft(s string, n int) string {
	s = normalize(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return ellipsis + string(runes[len(runes)-n+1:])
}

func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
