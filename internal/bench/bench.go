package bench

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/andreabreu76/mem-engine/internal/handoff"
	"github.com/andreabreu76/mem-engine/internal/project"
	"github.com/andreabreu76/mem-engine/internal/store"
)

const (
	nativeMaxLines = 200
	nativeMaxBytes = 25600
	bytesPerToken  = 4
	startupSource  = "startup"
)

type Result struct {
	NativePath                string
	NativeBytes, HandoffBytes int
}

func NativeMemoryPath(userHome, projectRoot string) string {
	enc := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return '-'
	}, projectRoot)
	return filepath.Join(userHome, ".claude", "projects", enc, "memory", "MEMORY.md")
}

func NativeLoadedBytes(content []byte) int {
	n := 0
	for range nativeMaxLines {
		i := bytes.IndexByte(content[n:], '\n')
		if i < 0 {
			n = len(content)
			break
		}
		n += i + 1
	}
	if n <= nativeMaxBytes {
		return n
	}
	n = nativeMaxBytes
	for n > 0 && !utf8.RuneStart(content[n]) {
		n--
	}
	return n
}

func (r Result) Tokens() (native, handoff int) {
	return r.NativeBytes / bytesPerToken, r.HandoffBytes / bytesPerToken
}

func Run(ctx context.Context, st *store.Store, p project.Project, nativePath string) (Result, error) {
	r := Result{NativePath: nativePath}
	content, err := os.ReadFile(nativePath)
	switch {
	case err == nil:
		r.NativeBytes = NativeLoadedBytes(content)
	case !errors.Is(err, fs.ErrNotExist):
		return r, err
	}
	row := store.ProjectRow{Key: p.Key, Name: p.Name, Dir: p.Dir}
	in := handoff.Input{Project: row, Source: startupSource}
	if st != nil {
		if in, err = handoff.Collect(ctx, st, row, "", startupSource); err != nil {
			return r, err
		}
	}
	r.HandoffBytes = len(handoff.Build(in))
	return r, nil
}
