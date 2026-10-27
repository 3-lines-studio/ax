// Command mcpx bridges MCP (Model Context Protocol) servers into ax's own
// Unix tool convention (describe/run), so an ax-based agent can call tools
// an MCP server exposes. ax has no MCP client of its own.
//
// mcpx is standalone: it has no dependency on acpi or ACP. Point
// MCPX_SERVERS_FILE at a JSON array of stdio server configs (config.go) and
// add mcpx to AX_TOOLS -- that's the whole contract. acpi happens to write
// that file itself when an ACP client hands it MCP servers via session/new,
// but nothing about mcpx requires acpi to be involved.
//
// ax execs a fresh `mcpx describe` at startup and a fresh `mcpx run <name>`
// per tool call -- no persistent process -- so every call here spawns its
// target MCP server, does the handshake, and tears it down again. See the
// README's "Known limitations" section.
package main

import (
	"context"
	"fmt"
	"os"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: mcpx describe | mcpx run <name>")
		os.Exit(2)
	}

	ctx := context.Background()
	var err error
	switch os.Args[1] {
	case "describe":
		err = runDescribe(ctx)
	case "run":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: mcpx run <name>")
			os.Exit(2)
		}
		err = runRun(ctx, os.Args[2])
	default:
		fmt.Fprintf(os.Stderr, "mcpx: unknown command %q\n", os.Args[1])
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
