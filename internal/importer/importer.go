package importer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/andreabreu76/mem-engine/internal/memory"
	"github.com/andreabreu76/mem-engine/internal/project"
	"github.com/andreabreu76/mem-engine/internal/store"
)

type Options struct {
	Dir    string
	Target project.Project
	DryRun bool
}

type Report struct {
	Imported, Skipped int
	Titles            []string
	Unresolved        []string
	Notes             []string
}

const (
	indexFile       = "MEMORY.md"
	importedTag     = "imported"
	nativeTagPrefix = "native:"
	delimiter       = "---"
)

var saveMemory = (*memory.Service).Save

var (
	linkPattern = regexp.MustCompile(`\[\[([^\[\]\n]+)\]\]`)
	idPattern   = regexp.MustCompile(`^m-[0-9a-f]{12}$`)
)

type nativeFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Type        string `yaml:"type"`
	Metadata    struct {
		Type string `yaml:"type"`
	} `yaml:"metadata"`
}

type source struct {
	file, id, title, body string
	fresh                 bool
}

func Run(ctx context.Context, svc *memory.Service, st *store.Store, o Options) (Report, error) {
	var rep Report
	files, err := markdownFiles(o.Dir)
	if err != nil {
		return rep, fmt.Errorf("import: %w", err)
	}
	sources, err := importedSources(ctx, st, o.Target.Key)
	if err != nil {
		return rep, fmt.Errorf("import: %w", err)
	}
	done := map[string]bool{}
	for _, s := range sources {
		done[s.file] = true
	}
	for _, file := range files {
		if done[file] {
			rep.Skipped++
			rep.Notes = append(rep.Notes, file+": skipped, already imported")
			continue
		}
		b, err := os.ReadFile(filepath.Join(o.Dir, file))
		if err != nil {
			return rep, fmt.Errorf("import: %w", err)
		}
		in, err := parseNative(b)
		if err != nil {
			rep.Skipped++
			rep.Notes = append(rep.Notes, file+": skipped, "+err.Error())
			continue
		}
		in.Tags = append(in.Tags, nativeTagPrefix+file)
		s := &source{file: file, title: in.Title, body: in.Body, fresh: true}
		if !o.DryRun {
			m, err := saveMemory(svc, ctx, o.Target, in)
			if err != nil {
				return rep, fmt.Errorf("import %s: %w", file, err)
			}
			s.id = m.ID
		}
		rep.Imported++
		rep.Titles = append(rep.Titles, in.Title)
		sources = append(sources, s)
	}
	slices.SortStableFunc(sources, func(a, b *source) int { return strings.Compare(a.file, b.file) })
	names, notes := nameIndex(sources)
	rep.Notes = append(rep.Notes, notes...)
	resolve := func(name string) (string, bool) {
		s, ok := names[titleOf(name)]
		if !ok {
			return "", false
		}
		return s.id, true
	}
	for _, s := range sources {
		body, missing := rewriteLinks(s.body, resolve)
		for _, name := range missing {
			rep.Unresolved = append(rep.Unresolved, "[["+name+"]] in "+s.file)
		}
		if o.DryRun || body == s.body {
			continue
		}
		if _, err := svc.Update(ctx, s.id, memory.Patch{Body: &body}); err != nil {
			return rep, fmt.Errorf("import %s: %w", s.file, err)
		}
	}
	return rep, nil
}

func nameIndex(sources []*source) (map[string]*source, []string) {
	names := map[string]*source{}
	var notes []string
	for _, s := range sources {
		for _, key := range []string{titleOf(strings.TrimSuffix(s.file, ".md")), s.title} {
			first, taken := names[key]
			if !taken {
				names[key] = s
				continue
			}
			if first != s && s.fresh && key == s.title {
				notes = append(notes, fmt.Sprintf("%s: name %q is also used by %s, links to it resolve to %s", s.file, s.title, first.file, first.file))
			}
		}
	}
	return names, notes
}

func markdownFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		name := e.Name()
		if e.Type().IsRegular() && filepath.Ext(name) == ".md" && name != indexFile {
			files = append(files, name)
		}
	}
	return files, nil
}

func importedSources(ctx context.Context, st *store.Store, key string) ([]*source, error) {
	if st == nil {
		return nil, nil
	}
	ms, err := st.ListMemories(ctx, key)
	if err != nil {
		return nil, err
	}
	var sources []*source
	for _, m := range ms {
		if !slices.Contains(m.Tags, importedTag) {
			continue
		}
		file := m.ID
		for _, tag := range m.Tags {
			if f, ok := strings.CutPrefix(tag, nativeTagPrefix); ok {
				file = f
				break
			}
		}
		sources = append(sources, &source{file: file, id: m.ID, title: m.Title, body: m.Body})
	}
	return sources, nil
}

func parseNative(b []byte) (memory.Input, error) {
	head, body, ok := splitFrontmatter(b)
	if !ok {
		return memory.Input{}, errors.New("no frontmatter")
	}
	var fm nativeFrontmatter
	if err := yaml.Unmarshal(head, &fm); err != nil {
		return memory.Input{}, fmt.Errorf("invalid frontmatter: %v", err)
	}
	if strings.TrimSpace(fm.Name) == "" {
		return memory.Input{}, errors.New("frontmatter without name")
	}
	typ := fm.Metadata.Type
	if typ == "" {
		typ = fm.Type
	}
	in := memory.Input{
		Type:    typ,
		Title:   titleOf(fm.Name),
		Summary: fm.Description,
		Body:    body,
		Tags:    []string{importedTag},
	}
	if err := memory.Validate(in); err != nil {
		return memory.Input{}, err
	}
	return in, nil
}

func titleOf(name string) string {
	return strings.ReplaceAll(strings.TrimSpace(name), "-", " ")
}

func splitFrontmatter(b []byte) ([]byte, string, bool) {
	first, rest, ok := cutLine(b)
	if !ok || string(first) != delimiter {
		return nil, "", false
	}
	for r := rest; len(r) > 0; {
		line, next, _ := cutLine(r)
		if string(line) == delimiter {
			if blank, after, ok := cutLine(next); ok && len(blank) == 0 {
				next = after
			}
			return rest[:len(rest)-len(r)], string(next), true
		}
		r = next
	}
	return nil, "", false
}

func cutLine(b []byte) ([]byte, []byte, bool) {
	line, rest, ok := bytes.Cut(b, []byte("\n"))
	return bytes.TrimSuffix(line, []byte("\r")), rest, ok
}

func rewriteLinks(body string, resolve func(string) (string, bool)) (string, []string) {
	var b strings.Builder
	var missing []string
	fence := ""
	for _, line := range strings.SplitAfter(body, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		switch {
		case fence != "":
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
			}
		case strings.HasPrefix(trimmed, "```"):
			fence = "```"
		case strings.HasPrefix(trimmed, "~~~"):
			fence = "~~~"
		default:
			line = linkPattern.ReplaceAllStringFunc(line, func(link string) string {
				name := link[2 : len(link)-2]
				if idPattern.MatchString(name) {
					return link
				}
				if id, ok := resolve(name); ok {
					return "[[" + id + "]]"
				}
				missing = append(missing, name)
				return link
			})
		}
		b.WriteString(line)
	}
	return b.String(), missing
}
