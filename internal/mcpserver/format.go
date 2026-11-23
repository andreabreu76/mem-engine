package mcpserver

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/andreabreu76/mem-engine/internal/store"
)

const (
	titleMaxRunes   = 80
	summaryMaxRunes = 100
	ellipsis        = "…"
)

func hitLine(h store.Hit) string {
	typ := h.Type
	if typ == "" {
		typ = "-"
	}
	return fmt.Sprintf("%s %s %s %s | %s", h.ID, h.At.UTC().Format(time.DateOnly), typ, clip(h.Title, titleMaxRunes), clip(h.Summary, summaryMaxRunes))
}

func formatHits(hits []store.Hit) string {
	if len(hits) == 0 {
		return "no results\n"
	}
	var b strings.Builder
	for _, h := range hits {
		b.WriteString(hitLine(h))
		b.WriteByte('\n')
	}
	return b.String()
}

func formatTimeline(hits []store.Hit, anchor string) string {
	var b strings.Builder
	for _, h := range hits {
		if h.ID == anchor {
			b.WriteString("> ")
		} else {
			b.WriteString("  ")
		}
		b.WriteString(hitLine(h))
		b.WriteByte('\n')
	}
	return b.String()
}

func formatItems(items []store.Item, missing []string, maxBytes int) string {
	blocks := make([]string, len(items))
	for i, it := range items {
		blocks[i] = itemBlock(it, maxBytes)
	}
	notFound := ""
	if len(missing) > 0 {
		notFound = fmt.Sprintf("not found: %s\n", strings.Join(missing, ", "))
	}
	for k := len(blocks); k > 0; k-- {
		footer := notFound + truncationNotice(items[k:], "", maxBytes)
		if out := withFooter(strings.Join(blocks[:k], "\n---\n"), footer); len(out) <= maxBytes {
			return out
		}
	}
	if len(blocks) == 0 {
		return notFound
	}
	footer := notFound + truncationNotice(items[1:], items[0].ID, maxBytes)
	room := max(maxBytes-len(footer)-2, 0)
	return withFooter(cutUTF8(blocks[0], room)+"\n", footer)
}

func itemBlock(it store.Item, maxBytes int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s %s %s\n", it.ID, it.At.UTC().Format(time.DateOnly), it.Type, oneLine(it.Title))
	fmt.Fprintf(&b, "summary: %s\n", oneLine(it.Summary))
	if len(it.Tags) > 0 {
		fmt.Fprintf(&b, "tags: %s\n", strings.Join(it.Tags, ", "))
	}
	if it.ToolName != "" {
		fmt.Fprintf(&b, "tool: %s\n", it.ToolName)
	}
	if len(it.Files) > 0 {
		fmt.Fprintf(&b, "files: %s\n", strings.Join(it.Files, ", "))
	}
	if it.Body != "" {
		b.WriteByte('\n')
		b.WriteString(strings.TrimRight(it.Body, "\n"))
		b.WriteByte('\n')
	}
	if it.Truncated {
		fmt.Fprintf(&b, "[body truncated, %d bytes in total per call]\n", maxBytes)
	}
	return b.String()
}

func truncationNotice(hidden []store.Item, cut string, maxBytes int) string {
	if len(hidden) == 0 && cut == "" {
		return ""
	}
	notice := fmt.Sprintf("[output truncated at %d bytes", maxBytes)
	if cut != "" {
		notice += "; " + cut + " cut short"
	}
	if len(hidden) > 0 {
		ids := make([]string, len(hidden))
		for i, it := range hidden {
			ids[i] = it.ID
		}
		notice += "; not shown: " + strings.Join(ids, ", ")
	}
	return notice + "]\n"
}

func withFooter(body, footer string) string {
	if body == "" || footer == "" {
		return body + footer
	}
	return body + "\n" + footer
}

func cutUTF8(s string, n int) string {
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func clip(s string, n int) string {
	s = oneLine(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimRight(string(r[:n]), " ") + ellipsis
}

func parseSince(s string, now time.Time) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err == nil && n >= 0 {
			return now.AddDate(0, 0, -n), nil
		}
	}
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t, nil
	}
	return time.Time{}, userErrorf("invalid since %q: want YYYY-MM-DD or <N>d, e.g. 7d", s)
}
