package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"version"}, nil, &out, &errb); code != 0 {
		t.Fatalf("code=%d", code)
	}
	if strings.TrimSpace(out.String()) != "mem dev" {
		t.Fatalf("got %q", out.String())
	}
}

func TestRunUnknown(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"nope"}, nil, &out, &errb); code != 2 {
		t.Fatalf("code=%d", code)
	}
}
