package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func ToolsPayload(ctx context.Context) ([]byte, error) {
	serverT, clientT := mcp.NewInMemoryTransports()
	ss, err := New(Deps{}).Connect(ctx, serverT, nil)
	if err != nil {
		return nil, fmt.Errorf("tools payload: %w", err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "mem-bench", Version: "v0"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		return nil, fmt.Errorf("tools payload: %w", err)
	}
	defer cs.Close()
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("tools payload: %w", err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(res.Tools); err != nil {
		return nil, fmt.Errorf("tools payload: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
