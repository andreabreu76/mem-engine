package mcpserver

import (
	"errors"
	"fmt"
	"strings"

	"github.com/andreabreu76/mem-engine/internal/memory"
)

type userError string

func (e userError) Error() string { return string(e) }

func userErrorf(format string, args ...any) error {
	return userError(fmt.Sprintf(format, args...))
}

func toolError(tool string, err error) error {
	var ue userError
	if errors.As(err, &ue) {
		return ue
	}
	msg := err.Error()
	if errors.Is(err, memory.ErrInvalid) ||
		strings.Contains(msg, ": file holds memory ") && strings.HasSuffix(msg, "(run mem reindex)") {
		return err
	}
	return fmt.Errorf("%s failed, see mem.log", tool)
}
