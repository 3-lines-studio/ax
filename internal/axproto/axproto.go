package axproto

import "encoding/json"

const Version = 1

const (
	TypeProtocol       = "protocol"
	TypeAssistantDelta = "assistant_delta"
	TypeReasoningDelta = "reasoning_delta"
	TypeAssistantDone  = "assistant_done"
	TypeToolStart      = "tool_start"
	TypeToolDelta      = "tool_delta"
	TypeToolResult     = "tool_result"
	TypeToolDone       = "tool_done"
	TypeUsage          = "usage"
	TypeCompaction     = "compaction"
	TypeSteer          = "steer"
	TypeResume         = "resume"
	TypeSecretRequest  = "secret_request"
	TypeMessage        = "message"
	TypeResult         = "result"
	TypeError          = "error"
	TypeDone           = "done"
)

const (
	OutcomeDone      = "done"
	OutcomeCancelled = "cancelled"
	OutcomeCompact   = "compact"
	OutcomeMaxTurns  = "max_turns"
	OutcomeFailed    = "failed"
)

type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Snippet     string          `json:"snippet,omitempty"`
	Sequential  bool            `json:"sequential,omitempty"`
	Timeout     int             `json:"timeout,omitempty"`
	Replay      string          `json:"replay,omitempty"`
	ReadOnly    bool            `json:"read_only,omitempty"`
	Destructive bool            `json:"destructive,omitempty"`
	OpenWorld   bool            `json:"open_world,omitempty"`
}
