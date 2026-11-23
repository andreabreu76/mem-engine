package redact

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func mustNew(t *testing.T, extra []string, disableBuiltin bool) *Redactor {
	t.Helper()
	r, err := New(extra, disableBuiltin)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func TestCleanBuiltin(t *testing.T) {
	pemBody := strings.Repeat("Q", 64)
	cases := []struct {
		name   string
		label  string
		secret string
	}{
		{"github classic", "github", "ghp_" + strings.Repeat("a", 36)},
		{"github fine grained", "github", "github_pat_" + strings.Repeat("b", 22)},
		{"slack", "slack", "xoxb-" + strings.Repeat("1", 12)},
		{"aws access key", "aws", "AKIA" + strings.Repeat("Z", 16)},
		{"aws secret", "aws", "AWS_SECRET_ACCESS_KEY=" + strings.Repeat("s", 40)},
		{"anthropic", "anthropic", "sk-ant-" + strings.Repeat("c", 30)},
		{"openai project", "openai", "sk-proj-" + strings.Repeat("d", 30)},
		{"openai legacy", "openai", "sk-" + strings.Repeat("e", 30)},
		{"jwt", "jwt", "eyJ" + strings.Repeat("f", 10) + "." + strings.Repeat("g", 10) + "." + strings.Repeat("h", 10)},
		{"pem", "pem", "-----BEGIN " + "RSA PRIVATE KEY-----\n" + pemBody + "\n-----END RSA PRIVATE KEY-----"},
	}
	r := mustNew(t, nil, false)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := r.Clean("before " + tc.secret + " after")
			if !strings.Contains(got, "[REDACTED:"+tc.label+"]") {
				t.Errorf("Clean = %q, want label %q", got, tc.label)
			}
			if strings.Contains(got, tc.secret) {
				t.Errorf("Clean = %q, still contains the secret", got)
			}
			if !strings.HasPrefix(got, "before ") || !strings.HasSuffix(got, " after") {
				t.Errorf("Clean = %q, surrounding text lost", got)
			}
		})
	}
}

func TestCleanPEMWithoutEnd(t *testing.T) {
	r := mustNew(t, nil, false)
	body := strings.Repeat("K", 64)
	got := r.Clean("key:\n-----BEGIN " + "PRIVATE KEY-----\n" + body + "\nmore lines")
	if got != "key:\n[REDACTED:pem]" {
		t.Errorf("Clean = %q", got)
	}
}

func TestCleanConnStringKeepsHost(t *testing.T) {
	r := mustNew(t, nil, false)
	got := r.Clean("dsn=postgres://app:" + "s3cr3t" + "@db.example.com:5432/x")
	if !strings.Contains(got, "db.example.com") {
		t.Errorf("Clean = %q, host lost", got)
	}
	if !strings.Contains(got, "postgres://app:") {
		t.Errorf("Clean = %q, scheme or user lost", got)
	}
	if !strings.Contains(got, "[REDACTED:conn]") {
		t.Errorf("Clean = %q, want conn label", got)
	}
	if strings.Contains(got, "s3cr3t") {
		t.Errorf("Clean = %q, password kept", got)
	}
}

func TestCleanLeavesPlainURL(t *testing.T) {
	r := mustNew(t, nil, false)
	in := "see https://example.com:8080/docs?a=b and http://localhost:3000/"
	if got := r.Clean(in); got != in {
		t.Errorf("Clean = %q, want unchanged", got)
	}
}

func TestCleanExtra(t *testing.T) {
	r := mustNew(t, []string{`acme-[0-9]{6}`}, false)
	got := r.Clean("id acme-123456 and ghp_" + strings.Repeat("a", 36))
	if !strings.Contains(got, "[REDACTED:extra]") || strings.Contains(got, "acme-123456") {
		t.Errorf("Clean = %q, extra not applied", got)
	}
	if !strings.Contains(got, "[REDACTED:github]") {
		t.Errorf("Clean = %q, builtin not applied", got)
	}
}

func TestNewRejectsInvalidExtra(t *testing.T) {
	if _, err := New([]string{`ok`, `(unclosed`}, false); err == nil {
		t.Fatal("New accepted an invalid pattern")
	}
}

func TestDisableBuiltin(t *testing.T) {
	r := mustNew(t, []string{`acme-[0-9]{6}`}, true)
	token := "ghp_" + strings.Repeat("a", 36)
	got := r.Clean("acme-123456 " + token + " <private>hidden</private>")
	if !strings.Contains(got, token) {
		t.Errorf("Clean = %q, builtin applied while disabled", got)
	}
	if !strings.Contains(got, "[REDACTED:extra]") {
		t.Errorf("Clean = %q, extra not applied", got)
	}
	if strings.Contains(got, "hidden") {
		t.Errorf("Clean = %q, private not stripped", got)
	}
}

func TestStripPrivate(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"inline", "a <private>x</private> b <PRIVATE>y</Private> c", "a  b  c"},
		{"multiline", "start\n<private>\nline1\nline2\n</private>\nend", "start\n\nend"},
		{"non greedy", "<private>1</private>keep<private>2</private>", "keep"},
		{"unclosed", "visible <private>secret\nrest", "visible "},
		{"none", "nothing here", "nothing here"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StripPrivate(tc.in); got != tc.want {
				t.Errorf("StripPrivate(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestTruncateUTF8(t *testing.T) {
	s := strings.Repeat("ã", 3000)
	for _, max := range []int{4096, 4095} {
		got := Truncate(s, max)
		if !utf8.ValidString(got) {
			t.Fatalf("max %d: invalid UTF-8", max)
		}
		head, suffix, ok := strings.Cut(got, "\n[truncated ")
		if !ok {
			t.Fatalf("max %d: no truncation suffix in %q", max, got[len(got)-40:])
		}
		if len(head) > max {
			t.Errorf("max %d: kept %d bytes", max, len(head))
		}
		want := len(s) - len(head)
		if suffix != strconv.Itoa(want)+" bytes]" {
			t.Errorf("max %d: suffix %q, want %d bytes", max, suffix, want)
		}
	}
}

func TestTruncateShort(t *testing.T) {
	if got := Truncate("short", 4096); got != "short" {
		t.Errorf("Truncate = %q", got)
	}
	if got := Truncate("exact", 5); got != "exact" {
		t.Errorf("Truncate = %q", got)
	}
}
