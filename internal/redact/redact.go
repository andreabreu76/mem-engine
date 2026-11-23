package redact

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

type pattern struct {
	re          *regexp.Regexp
	candidate   *regexp.Regexp
	replacement string
	secretGroup int
}

type Redactor struct {
	patterns []pattern
}

var builtin = []struct {
	name string
	expr string
}{
	{"pem", `(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`},
	{"github", `\bgh[pousr]_[A-Za-z0-9]{36,}`},
	{"github", `\bgithub_pat_[A-Za-z0-9_]{22,}`},
	{"slack", `\bxox[abprs]-[A-Za-z0-9-]{10,}`},
	{"aws", `\b(AKIA|ASIA)[0-9A-Z]{16}`},
	{"aws", `(?i)aws_secret_access_key\s*[=:]\s*\S+`},
	{"anthropic", `\bsk-ant-[A-Za-z0-9_-]{20,}`},
	{"openai", `\bsk-(proj-)?[A-Za-z0-9_-]{20,}`},
	{"jwt", `\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`},
	{"conn", `://[^\s:/@]+:(?P<secret>[^\s/@]+)@`},
}

var privateRe = regexp.MustCompile(`(?is)<private>.*?(?:</private>|$)`)

func New(extra []string, disableBuiltin bool) (*Redactor, error) {
	r := &Redactor{}
	if !disableBuiltin {
		for _, b := range builtin {
			p := newPattern(regexp.MustCompile(b.expr), b.name)
			if rest, ok := strings.CutPrefix(b.expr, `\b`); ok {
				p.candidate = regexp.MustCompile(rest)
			}
			r.patterns = append(r.patterns, p)
		}
	}
	for i, expr := range extra {
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, fmt.Errorf("redact: extra pattern %d %q: %w", i, expr, err)
		}
		r.patterns = append(r.patterns, newPattern(re, "extra"))
	}
	return r, nil
}

func newPattern(re *regexp.Regexp, name string) pattern {
	group := re.SubexpIndex("secret")
	if group < 0 {
		group = 0
	}
	return pattern{re: re, replacement: "[REDACTED:" + name + "]", secretGroup: group}
}

func (r *Redactor) Clean(s string) string {
	s = StripPrivate(s)
	for _, p := range r.patterns {
		s = p.apply(s)
	}
	return s
}

func (p pattern) apply(s string) string {
	if p.candidate != nil && !p.candidate.MatchString(s) {
		return s
	}
	matches := p.re.FindAllStringSubmatchIndex(s, -1)
	if matches == nil {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	last := 0
	for _, m := range matches {
		start, end := m[2*p.secretGroup], m[2*p.secretGroup+1]
		if start < 0 {
			continue
		}
		b.WriteString(s[last:start])
		b.WriteString(p.replacement)
		last = end
	}
	b.WriteString(s[last:])
	return b.String()
}

func StripPrivate(s string) string {
	return pattern{re: privateRe}.apply(s)
}

func Truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return fmt.Sprintf("%s\n[truncated %d bytes]", s[:cut], len(s)-cut)
}
