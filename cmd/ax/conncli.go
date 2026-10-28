package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func runConnCommand(fc fileConfig, bot *botCtx, args []string) {
	if len(args) == 0 {
		connUsage()
		return
	}
	switch args[0] {
	case "list":
		connListCommand(fc, bot, args[1:])
	case "doctor":
		connDoctorCommand(fc, bot, args[1:])
	case "-h", "--help", "help":
		connUsage()
	default:
		fmt.Fprintf(os.Stderr, "error: unknown conn command %q\n", args[0])
		connUsage()
		os.Exit(2)
	}
}

func connUsage() {
	fmt.Fprint(os.Stderr, `usage: ax conn <command> [flags]

  list [--json]        configured connections: kind, trust, capabilities, auth
  doctor [--connection ID]
                       connect every connection and report declared vs enabled

Connections come from [[connection]] tables in bot.toml. tools/AX_TOOLS and
[[mcp]] are legacy spellings and appear here with the same ids.
`)
}

// connTarget is the human form of where a connection lives.
func connTarget(conn connection) string {
	if conn.kind == connMCP {
		if conn.cfg.URL != "" {
			return conn.cfg.URL
		}
		return strings.TrimSpace(conn.cfg.Command + " " + strings.Join(conn.cfg.Args, " "))
	}
	return conn.command
}

func connAuthState(conn connection) string {
	if conn.kind != connMCP {
		return ""
	}
	return serverAuthState(conn.entry.mcpEntry(conn.id))
}

func connCapabilityLabel(conn connection) string {
	if len(conn.caps) == 0 {
		return "all"
	}
	return fmt.Sprintf("%d", len(conn.caps))
}

func connListCommand(fc fileConfig, bot *botCtx, args []string) {
	asJSON := false
	for _, arg := range args {
		switch arg {
		case "--json", "-json":
			asJSON = true
		case "-h", "--help":
			fmt.Fprint(os.Stderr, "usage: ax conn list [--json]\n")
			return
		default:
			fmt.Fprintf(os.Stderr, "error: unknown conn list flag %q\n", arg)
			os.Exit(2)
		}
	}
	conns, err := resolveConnections(fc, bot.Workspace, mcpAuthDir(bot))
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	type listed struct {
		ID           string `json:"id"`
		Kind         string `json:"kind"`
		Trust        string `json:"trust"`
		Target       string `json:"target"`
		Capabilities string `json:"capabilities"`
		Auth         string `json:"auth,omitempty"`
		Status       string `json:"status"`
		Error        string `json:"error,omitempty"`
	}
	out := make([]listed, 0, len(conns))
	for _, conn := range conns {
		item := listed{
			ID:           conn.id,
			Kind:         conn.kind,
			Trust:        conn.trust,
			Target:       connTarget(conn),
			Capabilities: connCapabilityLabel(conn),
			Auth:         connAuthState(conn),
			Status:       "ready",
		}
		if conn.err != nil {
			item.Status = "error"
			item.Error = conn.err.Error()
		}
		out = append(out, item)
	}
	if asJSON {
		data, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		fmt.Println(string(data))
		return
	}
	if len(out) == 0 {
		fmt.Println("No connections configured. Add a [[connection]] to bot.toml.")
		return
	}
	fmt.Printf("%-20s %-9s %-6s %-7s %-12s %s\n", "ID", "KIND", "TRUST", "CAPS", "STATUS", "TARGET")
	for _, item := range out {
		status := item.Status
		if item.Error != "" {
			status += ": " + oneLineText(item.Error, 60)
		}
		fmt.Printf("%-20s %-9s %-6s %-7s %-12s %s\n", item.ID, item.Kind, item.Trust, item.Capabilities, status, oneLineText(item.Target, 60))
	}
}

func connDoctorCommand(fc fileConfig, bot *botCtx, args []string) {
	only := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--refresh", "-refresh":
			// Doctor always connects; kept for symmetry with ax catalog.
		case "--connection", "-connection", "--server", "-server":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --connection needs an id")
				os.Exit(2)
			}
			only = args[i]
		case "-h", "--help":
			fmt.Fprint(os.Stderr, "usage: ax conn doctor [--connection ID]\n")
			return
		default:
			fmt.Fprintf(os.Stderr, "error: unknown conn doctor flag %q\n", args[i])
			os.Exit(2)
		}
	}
	runtimes, err := openConnections(fc, bot, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if len(runtimes) == 0 {
		fmt.Println("No connections configured.")
		return
	}
	failures := 0
	fmt.Printf("%-20s %-9s %-6s %-17s %s\n", "ID", "KIND", "TRUST", "TOOLS", "STATUS")
	for _, runtime := range runtimes {
		if only != "" && !strings.EqualFold(runtime.conn.id, only) {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), catalogLoadTimeout)
		declared, err := runtime.inner.List(ctx)
		cancel()
		if err != nil {
			_ = runtime.source.Close()
			failures++
			fmt.Printf("%-20s %-9s %-6s %-17s %s\n", runtime.conn.id, runtime.conn.kind, runtime.conn.trust, "-", "error: "+oneLineText(err.Error(), 80))
			continue
		}
		ctx, cancel = context.WithTimeout(context.Background(), catalogLoadTimeout)
		enabled, err := runtime.source.List(ctx)
		cancel()
		_ = runtime.source.Close()
		if err != nil {
			failures++
			fmt.Printf("%-20s %-9s %-6s %-17s %s\n", runtime.conn.id, runtime.conn.kind, runtime.conn.trust, "-", "error: "+oneLineText(err.Error(), 80))
			continue
		}
		status := "ready"
		if len(declared) > 0 && len(enabled) == 0 {
			status = "nothing enabled (check trust and capabilities)"
		} else if auth := connAuthState(runtime.conn); auth != "" && auth != "stdio" {
			status = auth
		}
		fmt.Printf("%-20s %-9s %-6s %-17s %s\n", runtime.conn.id, runtime.conn.kind, runtime.conn.trust,
			fmt.Sprintf("%d declared, %d on", len(declared), len(enabled)), status)
	}
	if failures > 0 {
		os.Exit(1)
	}
}
