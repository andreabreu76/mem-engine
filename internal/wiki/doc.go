package wiki

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	"go.yaml.in/yaml/v3"
)

type Doc struct {
	ID, Type, Title, Summary, Project string
	Tags                              []string
	Created, Updated                  time.Time
	SourceSession                     string
	Body                              string
}

type frontmatter struct {
	ID            string    `yaml:"id"`
	Type          string    `yaml:"type"`
	Title         string    `yaml:"title"`
	Summary       string    `yaml:"summary"`
	Project       string    `yaml:"project"`
	Tags          []string  `yaml:"tags"`
	Created       time.Time `yaml:"created"`
	Updated       time.Time `yaml:"updated"`
	SourceSession string    `yaml:"source_session"`
}

const delimiter = "---"

func Marshal(d Doc) []byte {
	tags := d.Tags
	if tags == nil {
		tags = []string{}
	}
	fm := frontmatter{
		ID:            d.ID,
		Type:          d.Type,
		Title:         d.Title,
		Summary:       d.Summary,
		Project:       d.Project,
		Tags:          tags,
		Created:       d.Created.UTC().Truncate(time.Second),
		Updated:       d.Updated.UTC().Truncate(time.Second),
		SourceSession: d.SourceSession,
	}
	var buf bytes.Buffer
	buf.WriteString(delimiter + "\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(fm); err != nil {
		panic(fmt.Sprintf("wiki: encode frontmatter: %v", err))
	}
	if err := enc.Close(); err != nil {
		panic(fmt.Sprintf("wiki: encode frontmatter: %v", err))
	}
	buf.WriteString(delimiter + "\n\n")
	buf.WriteString(d.Body)
	return buf.Bytes()
}

func Parse(b []byte) (Doc, error) {
	first, rest, ok := cutLine(b)
	if !ok || string(first) != delimiter {
		return Doc{}, errors.New("wiki: missing frontmatter")
	}
	var head []byte
	body := rest
	closed := false
	for len(body) > 0 {
		line, next, _ := cutLine(body)
		if string(line) == delimiter {
			head = rest[:len(rest)-len(body)]
			body = next
			closed = true
			break
		}
		body = next
	}
	if !closed {
		return Doc{}, errors.New("wiki: unclosed frontmatter")
	}
	if line, after, ok := cutLine(body); ok && len(line) == 0 {
		body = after
	}

	var fm frontmatter
	if err := yaml.Unmarshal(head, &fm); err != nil {
		return Doc{}, fmt.Errorf("wiki: frontmatter: %w", err)
	}
	switch {
	case fm.ID == "":
		return Doc{}, errors.New("wiki: frontmatter without id")
	case fm.Type == "":
		return Doc{}, errors.New("wiki: frontmatter without type")
	case fm.Title == "":
		return Doc{}, errors.New("wiki: frontmatter without title")
	}
	tags := fm.Tags
	if tags == nil {
		tags = []string{}
	}
	return Doc{
		ID:            fm.ID,
		Type:          fm.Type,
		Title:         fm.Title,
		Summary:       fm.Summary,
		Project:       fm.Project,
		Tags:          tags,
		Created:       fm.Created.UTC(),
		Updated:       fm.Updated.UTC(),
		SourceSession: fm.SourceSession,
		Body:          string(body),
	}, nil
}

func cutLine(b []byte) (line, rest []byte, ok bool) {
	line, rest, ok = bytes.Cut(b, []byte("\n"))
	return bytes.TrimSuffix(line, []byte("\r")), rest, ok
}
