// Package axproto is the wire contract shared by the programs in this
// repository: the JSONL event stream that `ax --events` emits, the outcomes
// that end a run, and the descriptors that tool providers print from
// `<provider> describe`.
//
// The human-readable version of this contract lives in docs/protocol.md and
// docs/tools.md. Keep the constants here in sync with those documents: they
// are what transports (slaxi) and hosts compile against.
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
	TypeReasoningDelta = "reasoning_delta"
	TypeAssistantDone  = "assistant_done"
	TypeToolStart      = "tool_start"
	TypeToolDelta      = "tool_delta"
	TypeToolResult     = "tool_result"
	TypeToolDone       = "tool_done"
	TypeUsage          = "usage"
	TypeCompaction     = "compaction"
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
	// Sequential asks the engine to run this tool alone, in call order, when
	// it appears in a batch. Mutating tools set it so they never race.
	Sequential bool `json:"sequential,omitempty"`
	// Timeout is a per-call limit in seconds. Zero means the engine default.
	Timeout int `json:"timeout,omitempty"`
	// Replay is "safe" when re-running an interrupted call has no side
	// effects. The engine still reports interrupted calls instead of
	// replaying them automatically; the field records intent for hosts.
	Replay string `json:"replay,omitempty"`
	// ReadOnly and Destructive are catalog hints. They do not change
	// execution, but they drive search tags and the default batching policy.
	ReadOnly    bool `json:"read_only,omitempty"`
	Destructive bool `json:"destructive,omitempty"`
	OpenWorld   bool `json:"open_world,omitempty"`
}
