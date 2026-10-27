package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

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
		if summary.Error != "" && !summary.AuthRequired {
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
