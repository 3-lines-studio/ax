package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/3-lines-studio/ax/internal/catalog"
)

func runCatalogCommand(fc fileConfig, bot *botCtx, args []string) {
	asJSON := false
	refresh := false
	server := ""
	query := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json", "-json":
			asJSON = true
		case "--refresh", "-refresh":
			refresh = true
		case "--server", "-server":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --server needs a name")
				os.Exit(2)
			}
			server = args[i]
		case "--query", "-query":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --query needs text")
				os.Exit(2)
			}
			query = args[i]
		case "-h", "--help":
			fmt.Fprint(os.Stderr, "usage: ax catalog [--json] [--refresh] [--server NAME] [--query TEXT]\n")
			return
		default:
			fmt.Fprintf(os.Stderr, "error: unknown catalog flag %q\n", args[i])
			os.Exit(2)
		}
	}
	sources, err := buildSources(fc, bot, refresh)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	cat := catalog.New(sources)
	ctx, cancel := context.WithTimeout(context.Background(), catalogLoadTimeout)
	cat.Load(ctx)
	cancel()
	defer func() { _ = cat.Close() }()

	for _, summary := range cat.Servers() {
		if summary.Error != "" {
			fmt.Fprintf(os.Stderr, "warning: server %s unavailable: %s\n", summary.Name, summary.Error)
		}
	}
	if asJSON {
		payload := struct {
			Servers []catalog.ServerSummary `json:"servers"`
			Tools   []catalog.Tool          `json:"tools"`
		}{Servers: cat.Servers(), Tools: cat.Tools()}
		data, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		fmt.Println(string(data))
		return
	}
	if server != "" || query != "" {
		fmt.Print(cat.Search(catalog.SearchRequest{Server: server, Query: query}))
		return
	}
	fmt.Print(cat.Menu())
}

func runMCPCommand(fc fileConfig, bot *botCtx, args []string) {
	if len(args) == 0 || args[0] != "serve" {
		fmt.Fprintln(os.Stderr, "usage: ax mcp serve [--stdio]")
		os.Exit(2)
	}
	httpAddr := ""
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--stdio":
		case "--http":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --http needs an address")
				os.Exit(2)
			}
			httpAddr = args[i]
		case "-h", "--help":
			fmt.Fprint(os.Stderr, "usage: ax mcp serve [--stdio] [--http ADDR]\n")
			return
		default:
			fmt.Fprintf(os.Stderr, "error: unknown mcp serve flag %q\n", args[i])
			os.Exit(2)
		}
	}
	if httpAddr != "" {
		fmt.Fprintln(os.Stderr, "error: ax mcp serve --http is not implemented yet; use --stdio")
		os.Exit(2)
	}
	sources, err := buildSources(fc, bot, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	cat := catalog.New(sources)
	ctx, cancel := context.WithTimeout(context.Background(), catalogLoadTimeout)
	cat.Load(ctx)
	cancel()
	defer func() { _ = cat.Close() }()
	if err := serveMCP(os.Stdin, os.Stdout, catalogTools(cat)); err != nil && !strings.Contains(err.Error(), "EOF") {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
