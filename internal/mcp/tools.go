//go:build tools

package mcp

// The official Go SDK is used only by internal/mcp/testdata/fake-server, a
// real MCP server the tests drive our minimal client against. Keeping the
// import behind the tools tag keeps the SDK out of the ax binary while
// pinning the version in go.mod.
import _ "github.com/modelcontextprotocol/go-sdk/mcp"
