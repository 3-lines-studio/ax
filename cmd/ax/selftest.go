package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/3-lines-studio/ax/internal/axproto"
	"github.com/3-lines-studio/ax/internal/catalog"
)

const selftestLoadTimeout = 20 * time.Second

type selftestReport struct {
	failures int
	warnings int
	lines    []string
}

func (r *selftestReport) ok(format string, args ...any) {
	r.lines = append(r.lines, "ok:   "+fmt.Sprintf(format, args...))
}

func (r *selftestReport) warn(format string, args ...any) {
	r.warnings++
	r.lines = append(r.lines, "warn: "+fmt.Sprintf(format, args...))
}

func (r *selftestReport) fail(format string, args ...any) {
	r.failures++
	r.lines = append(r.lines, "fail: "+fmt.Sprintf(format, args...))
}

func (s *selftestReport) print() {
	for _, line := range s.lines {
		fmt.Println(line)
	}
}

func runSelftest(cfg cliConfig, fc fileConfig, bot *botCtx) {
	report := &selftestReport{}
	report.ok("ax protocol v%d", axproto.Version)
	report.ok("bot %s", bot.Root)
	if bot.Workspace != bot.Root {
		report.ok("workspace %s", bot.Workspace)
	}

	if fc.computeErr != nil {
		report.fail("compute: %v", fc.computeErr)
	} else if len(fc.computeIDs) > 0 {
		report.ok("compute connections: %v", fc.computeIDs)
	}

	conns, err := resolveConnections(fc, bot.Root, bot.Workspace, mcpAuthDir(bot))
	if err != nil {
		report.fail("connections: %v", err)
	} else {
		report.ok("%d connection(s)", len(conns))
	}

	if key := apiKey(fc, bot); key == "" {
		report.warn("no API key: set DEEPSEEK_API_KEY, api_key in ~/.config/ax/config or secrets/api-key")
	} else {
		report.ok("api key present")
	}

	if !strings.EqualFold(firstNonEmpty(cfg.toolMode, fc.toolMode, "catalog"), "direct") {
		sources, err := buildSources(fc, bot, true)
		if err != nil {
			report.fail("catalog: %v", err)
		} else {
			cat := catalog.New(sources)
			ctx, cancel := context.WithTimeout(context.Background(), selftestLoadTimeout)
			cat.Load(ctx)
			cancel()
			tools := cat.Tools()
			servers := cat.Servers()
			report.ok("catalog: %d server(s), %d tool(s)", len(servers), len(tools))
			for _, server := range servers {
				switch {
				case server.Error != "":
					report.warn("%s: %s", server.Name, server.Error)
				case server.AuthRequired:
					report.warn("%s: login required (ax mcp login %s)", server.Name, server.Name)
				case server.Tools == 0:
					report.warn("%s: no tools", server.Name)
				}
			}
			_ = cat.Close()
		}
	}

	report.print()
	if report.failures > 0 {
		fmt.Fprintf(os.Stderr, "\nselftest failed: %d problem(s), %d warning(s)\n", report.failures, report.warnings)
		os.Exit(1)
	}
	if report.warnings > 0 {
		fmt.Printf("\nselftest passed with %d warning(s)\n", report.warnings)
		return
	}
	fmt.Println("\nselftest passed")
}
