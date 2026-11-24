package capture

import (
	"encoding/json"

	"github.com/andreabreu76/mem-engine/internal/store"
)

type Payload struct {
	SessionID            string          `json:"session_id"`
	TranscriptPath       string          `json:"transcript_path"`
	Cwd                  string          `json:"cwd"`
	HookEventName        string          `json:"hook_event_name"`
	Source               string          `json:"source"`
	Prompt               string          `json:"prompt"`
	ToolName             string          `json:"tool_name"`
	ToolInput            json.RawMessage `json:"tool_input"`
	ToolResponse         json.RawMessage `json:"tool_response"`
	Error                string          `json:"error"`
	DurationMS           int64           `json:"duration_ms"`
	LastAssistantMessage string          `json:"last_assistant_message"`
	Trigger              string          `json:"trigger"`
	CompactSummary       string          `json:"compact_summary"`
	Reason               string          `json:"reason"`
}

type Op struct {
	Project store.ProjectRow   `json:"project"`
	Session store.SessionEvent `json:"session"`
	Obs     *store.Observation `json:"obs"`
}
