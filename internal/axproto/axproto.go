// Package axproto is the wire contract shared by the programs in this
// repository: the JSONL event stream that `ax --events` emits, the outcomes
// that end a run, and the descriptors that tool providers print from
// `<provider> describe`.
//
// The human-readable version of this contract lives in docs/protocol.md and
// docs/tools.md. Keep the constants here in sync with those documents: they
// are what bridges (acpi, mcpx) and transports (slaxi) compile against.
package axproto

import "encoding/json"

// Version is the protocol version announced by the first event of every run.
// A client must reject an unsupported version and may ignore unknown event
// types within a supported version.
const Version = 1

// Event types on the `ax --events` stream.
const (
	TypeProtocol       = "protocol"
	TypeAssistantDelta = "assistant_delta"
	TypeAssistantDone  = "assistant_done"
	TypeToolStart      = "tool_start"
	TypeToolDelta      = "tool_delta"
	TypeToolResult     = "tool_result"
	TypeToolDone       = "tool_done"
	TypeUsage          = "usage"
	TypeMessage        = "message"
	TypeResult         = "result"
	TypeError          = "error"
	TypeDone           = "done"
)

// Outcomes carried by the terminal `done` event.
const (
	OutcomeDone      = "done"
	OutcomeCancelled = "cancelled"
	OutcomeCompact   = "compact"
	OutcomeMaxTurns  = "max_turns"
	OutcomeFailed    = "failed"
)

// ToolSpec is one line of `<provider> describe`. Providers print zero or
// more of these as JSON lines; ax registers a tool for each one.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Snippet     string          `json:"snippet,omitempty"`
}
