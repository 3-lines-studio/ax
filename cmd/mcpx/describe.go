package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/3-lines-studio/ax/internal/axproto"
)

// runDescribe lists tools from every configured server and prints one
// axproto.ToolSpec JSON line per tool, qualified as <server>__<tool>. A server
// that fails to connect or list is skipped with a stderr warning rather than
// failing the whole describe call -- one broken MCP server shouldn't take
// down every other tool ax has available.
//
// Does not handle pagination (ListToolsResult.NextCursor): fine for the
// small, fixed tool sets mcpx targets today (e.g. buzz-cli); revisit if a
// server with a large paginated tool catalog shows up.
func runDescribe(ctx context.Context) error {
	servers, err := loadServers()
	if err != nil {
		return err
	}
	for _, s := range servers {
		session, err := connectServer(ctx, s)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mcpx: describe: skipping server %q: %v\n", s.Name, err)
			continue
		}
		result, err := session.ListTools(ctx, &mcp.ListToolsParams{})
		_ = session.Close()
		if err != nil {
			fmt.Fprintf(os.Stderr, "mcpx: describe: listing tools on %q: %v\n", s.Name, err)
			continue
		}
		for _, t := range result.Tools {
			parameters, err := json.Marshal(t.InputSchema)
			if err != nil {
				return fmt.Errorf("mcpx: encoding parameters for %q: %w", t.Name, err)
			}
			spec := axproto.ToolSpec{
				Name:        s.Name + toolSeparator + t.Name,
				Description: t.Description,
				Parameters:  parameters,
				// MCP tools have unknown side effects, so never run two in
				// parallel within one model batch.
				Sequential: true,
			}
			line, err := json.Marshal(spec)
			if err != nil {
				return fmt.Errorf("mcpx: encoding tool spec for %q: %w", spec.Name, err)
			}
			fmt.Println(string(line))
		}
	}
	return nil
}
