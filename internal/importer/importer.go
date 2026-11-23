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
	indexFile   = "MEMORY.md"
	importedTag = "imported"
	delimiter   = "---"
)

var linkPattern = regexp.MustCompile(`\[\[([^\[\]\n]+)\]\]`)

type nativeFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Type        string `yaml:"type"`
	Metadata    struct {
		Type string `yaml:"type"`
	} `yaml:"metadata"`
}

type entry struct {
	file, id string
	in       memory.Input
}

func Run(ctx context.Context, svc *memory.Service, st *store.Store, o Options) (Report, error) {
	var rep Report
	files, err := markdownFiles(o.Dir)
	if err != nil {
		return rep, fmt.Errorf("import: %w", err)
	}
	existing, err := importedIDs(ctx, st, o.Target.Key)
	if err != nil {
		return rep, fmt.Errorf("import: %w", err)
	}
	planned := map[string]*entry{}
	var order []*entry
	for _, file := range files {
		b, err := os.ReadFile(filepath.Join(o.Dir, file))
		if err != nil {
			return rep, fmt.Errorf("import: %w", err)
		}
		in, err := parseNative(b)
		if err == nil {
			if prev, dup := planned[in.Title]; dup {
				err = fmt.Errorf("name %q already used by %s", in.Title, prev.file)
			}
		}
		if err != nil {
			rep.Skipped++
			rep.Notes = append(rep.Notes, file+": skipped, "+err.Error())
			continue
		}
		if _, done := existing[in.Title]; done {
			rep.Skipped++
			continue
		}
		e := &entry{file: file, in: in}
		planned[in.Title] = e
		order = append(order, e)
	}
	for _, e := range order {
		if !o.DryRun {
			m, err := svc.Save(ctx, o.Target, e.in)
			if err != nil {
				return rep, fmt.Errorf("import %s: %w", e.file, err)
			}
			e.id = m.ID
		}
		rep.Imported++
		rep.Titles = append(rep.Titles, e.in.Title)
	}
	resolve := func(name string) (string, bool) {
		title := titleOf(name)
		if id, ok := existing[title]; ok {
			return id, true
		}
		if e, ok := planned[title]; ok {
			return e.id, true
		}
		return "", false
	}
	for _, e := range order {
		body, missing := rewriteLinks(e.in.Body, resolve)
		for _, name := range missing {
			rep.Unresolved = append(rep.Unresolved, "[["+name+"]] in "+e.file)
		}
		if o.DryRun || body == e.in.Body {
			continue
		}
		if _, err := svc.Update(ctx, e.id, memory.Patch{Body: &body}); err != nil {
			return rep, fmt.Errorf("import %s: %w", e.file, err)
		}
	}
	return rep, nil
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

func importedIDs(ctx context.Context, st *store.Store, key string) (map[string]string, error) {
	ids := map[string]string{}
	if st == nil {
		return ids, nil
	}
	ms, err := st.ListMemories(ctx, key)
	if err != nil {
		return nil, err
	}
	for _, m := range ms {
		if _, seen := ids[m.Title]; !seen && slices.Contains(m.Tags, importedTag) {
			ids[m.Title] = m.ID
		}
	}
	return ids, nil
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
