package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/3-lines-studio/ax/internal/catalog"
	"github.com/3-lines-studio/ax/internal/mcp"
)

const mcpLoginTimeout = 5 * time.Minute

func runMCPCommand(fc fileConfig, bot *botCtx, args []string) {
	if len(args) == 0 {
		mcpUsage()
		return
	}
	switch args[0] {
	case "serve":
		mcpServeCommand(fc, bot, args[1:])
	case "list":
		mcpListCommand(fc, bot, args[1:])
	case "add":
		mcpAddCommand(fc, bot, args[1:])
	case "import":
		mcpImportCommand(fc, bot, args[1:])
	case "login":
		mcpLoginCommand(fc, bot, args[1:])
	case "logout":
		mcpLogoutCommand(fc, bot, args[1:])
	case "doctor":
		mcpDoctorCommand(fc, bot, args[1:])
	case "-h", "--help", "help":
		mcpUsage()
	default:
		fmt.Fprintf(os.Stderr, "error: unknown mcp command %q\n", args[0])
		mcpUsage()
		os.Exit(2)
	}
}

func mcpUsage() {
	fmt.Fprint(os.Stderr, `usage: ax mcp <command> [flags]

  serve [--stdio | --http ADDR]  expose search and execute over MCP
  list [--json]            configured servers and their login state
  add <name|url>           add a server to bot.toml (name from the registry)
  import <path|url|dir>    import an mcp.json, plugin.json or Cursor plugin
  login <server>           authorize a remote server with OAuth
  logout <server>          forget a stored login
  doctor [--refresh]       connect to every server and report what breaks

Run "ax mcp add" without arguments to list the registry.
`)
}

func mcpServeCommand(fc fileConfig, bot *botCtx, args []string) {
	httpAddr := ""
	for i := 0; i < len(args); i++ {
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
			fmt.Fprint(os.Stderr, "usage: ax mcp serve [--stdio | --http ADDR]\n")
			return
		default:
			fmt.Fprintf(os.Stderr, "error: unknown mcp serve flag %q\n", args[i])
			os.Exit(2)
		}
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
	tools := catalogTools(cat)
	if httpAddr != "" {
		if err := serveMCPHTTP(httpAddr, tools); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	if err := serveMCP(os.Stdin, os.Stdout, tools); err != nil && !strings.Contains(err.Error(), "EOF") {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// resolveAllMCP lists the MCP servers among the bot's connections, in either
// spelling: [[connection]] with an MCP kind, or the legacy [[mcp]] table.
func resolveAllMCP(fc fileConfig, bot *botCtx) ([]resolvedMCP, error) {
	conns, err := resolveConnections(fc, bot.Root, bot.Workspace, mcpAuthDir(bot))
	if err != nil {
		return nil, err
	}
	out := make([]resolvedMCP, 0, len(conns))
	for _, conn := range conns {
		if conn.kind != connMCP {
			continue
		}
		out = append(out, resolvedMCP{name: conn.id, cfg: conn.cfg, err: conn.err})
	}
	return out, nil
}

// serverAuthState describes how a server authenticates, in one phrase.
func serverAuthState(entry mcpEntry) string {
	if entry.URL == "" {
		return "stdio"
	}
	if entry.TokenEnv != "" {
		return "token from " + entry.TokenEnv
	}
	for key := range entry.Headers {
		if strings.EqualFold(key, "Authorization") {
			return "token header"
		}
	}
	oauth, err := wantsOAuth(entry)
	if err != nil || !oauth {
		return "no auth"
	}
	return "oauth"
}

func mcpListCommand(fc fileConfig, bot *botCtx, args []string) {
	asJSON := false
	for _, arg := range args {
		switch arg {
		case "--json", "-json":
			asJSON = true
		case "-h", "--help":
			fmt.Fprint(os.Stderr, "usage: ax mcp list [--json]\n")
			return
		default:
			fmt.Fprintf(os.Stderr, "error: unknown mcp list flag %q\n", arg)
			os.Exit(2)
		}
	}
	resolved, err := resolveAllMCP(fc, bot)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	type listedServer struct {
		Name    string `json:"name"`
		Target  string `json:"target"`
		Auth    string `json:"auth"`
		Status  string `json:"status"`
		Error   string `json:"error,omitempty"`
		Expires string `json:"expires,omitempty"`
	}
	out := make([]listedServer, 0, len(resolved))
	authDir := mcpAuthDir(bot)
	for _, entry := range resolved {
		item := listedServer{Name: entry.name}
		if entry.err != nil {
			item.Status = "error"
			item.Error = entry.err.Error()
			out = append(out, item)
			continue
		}
		if entry.cfg.URL != "" {
			item.Target = entry.cfg.URL
		} else {
			item.Target = strings.TrimSpace(entry.cfg.Command + " " + strings.Join(entry.cfg.Args, " "))
		}
		source := mcpEntryForName(fc, entry.name)
		item.Auth = serverAuthState(source)
		if entry.cfg.Auth != nil {
			token, err := mcp.LoadToken(oauthTokenPath(authDir, entry.name))
			switch {
			case errors.Is(err, mcp.ErrLoginRequired):
				item.Status = "login required"
			case err != nil:
				item.Status = "token unreadable"
				item.Error = err.Error()
			default:
				item.Status = "logged in"
				if !token.Expiry.IsZero() {
					item.Expires = token.Expiry.Format(time.RFC3339)
				}
			}
		} else {
			item.Status = "ready"
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
		fmt.Println("No MCP servers configured. Add one with `ax mcp add <name>`.")
		return
	}
	writer := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "NAME\tAUTH\tSTATUS\tTARGET")
	for _, item := range out {
		status := item.Status
		if item.Expires != "" {
			status += " until " + item.Expires
		}
		if item.Error != "" {
			status = item.Error
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", item.Name, oneLine(item.Auth), oneLine(status), oneLine(item.Target))
	}
	_ = writer.Flush()
}

// mcpEntryForName returns the table as the bot wrote it, which is what the auth
// helpers inspect. Connections win over the legacy table with the same id.
func mcpEntryForName(fc fileConfig, name string) mcpEntry {
	for _, entry := range fc.connections {
		if strings.EqualFold(strings.TrimSpace(entry.ID), name) {
			if kind, err := connectionKindNormal(entry.Kind); err == nil && kind == connMCP {
				return entry.mcpEntry(strings.ToLower(strings.TrimSpace(entry.ID)))
			}
		}
	}
	for _, entry := range fc.mcp {
		if strings.EqualFold(strings.TrimSpace(entry.Name), name) {
			return entry
		}
	}
	return mcpEntry{}
}

func mcpAddCommand(fc fileConfig, bot *botCtx, args []string) {
	registry, err := loadRegistry()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	name := ""
	printOnly := false
	var extraArgs, headers, env, scopes []string
	var target string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--name", "-name":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --name needs a value")
				os.Exit(2)
			}
			name = args[i]
		case "--arg", "-arg":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --arg needs a value")
				os.Exit(2)
			}
			extraArgs = append(extraArgs, args[i])
		case "--header", "-header":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --header needs K=V")
				os.Exit(2)
			}
			headers = append(headers, args[i])
		case "--env", "-env":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --env needs K=V")
				os.Exit(2)
			}
			env = append(env, args[i])
		case "--scope", "-scope":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --scope needs a value")
				os.Exit(2)
			}
			scopes = append(scopes, args[i])
		case "--print", "-print":
			printOnly = true
		case "-h", "--help":
			fmt.Fprint(os.Stderr, "usage: ax mcp add [<name|url>] [--name NAME] [--arg VALUE]... [--header K=V]... [--env K=V]... [--scope SCOPE]... [--print]\n")
			return
		default:
			if strings.HasPrefix(args[i], "-") {
				fmt.Fprintf(os.Stderr, "error: unknown mcp add flag %q\n", args[i])
				os.Exit(2)
			}
			if target != "" {
				fmt.Fprintf(os.Stderr, "error: unexpected argument %q\n", args[i])
				os.Exit(2)
			}
			target = args[i]
		}
	}
	if target == "" {
		printRegistry(registry)
		return
	}
	var entry mcpEntry
	var needs string
	if known, ok := registry.find(target); ok {
		entry = known.entry(extraArgs)
		needs = known.Needs
		name = firstNonEmpty(name, known.Name)
	} else if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		entry = mcpEntry{URL: target}
		name = firstNonEmpty(name, nameFromURL(target))
	} else {
		fmt.Fprintf(os.Stderr, "error: unknown server %q\n\n", target)
		printRegistry(registry)
		os.Exit(2)
	}
	if name != "" {
		entry.Name = normalizeServerName(name)
	}
	if entry.Name == "" {
		fmt.Fprintln(os.Stderr, "error: could not derive a server name; pass --name")
		os.Exit(2)
	}
	if err := validServerName(entry.Name); err != nil {
		fmt.Fprintf(os.Stderr, "error: mcp: server %q: %v\n", entry.Name, err)
		os.Exit(2)
	}
	if entry.Headers == nil {
		entry.Headers = map[string]string{}
	}
	for _, pair := range headers {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || strings.TrimSpace(key) == "" {
			fmt.Fprintf(os.Stderr, "error: --header %q needs K=V\n", pair)
			os.Exit(2)
		}
		entry.Headers[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	if len(entry.Headers) == 0 {
		entry.Headers = nil
	}
	if entry.Env == nil {
		entry.Env = map[string]string{}
	}
	for _, pair := range env {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || !validEnvName(strings.TrimSpace(key)) {
			fmt.Fprintf(os.Stderr, "error: --env %q needs NAME=VALUE\n", pair)
			os.Exit(2)
		}
		entry.Env[strings.TrimSpace(key)] = value
	}
	if len(entry.Env) == 0 {
		entry.Env = nil
	}
	entry.Scopes = append(entry.Scopes, scopes...)
	if printOnly {
		fmt.Print(renderMCPEntry(entry))
		return
	}
	if err := addMCPEntries(bot, []mcpEntry{entry}, false); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Printf("Added %s to %s\n", entry.Name, botTomlPath(bot))
	if needs != "" {
		fmt.Printf("Next: %s\n", needs)
	}
	if entry.URL != "" && entry.Auth != "none" && len(entry.Headers) == 0 && entry.TokenEnv == "" {
		fmt.Printf("Next: ax mcp login %s\n", entry.Name)
	}
}

func printRegistry(registry registryFile) {
	fmt.Println("Known MCP servers (ax mcp add <name>):")
	writer := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, entry := range registry.Servers {
		fmt.Fprintf(writer, "  %s\t%s\n", entry.Name, entry.Description)
	}
	_ = writer.Flush()
	fmt.Println("\nOr add any endpoint: ax mcp add https://example.com/mcp --name example")
}

// nameFromURL derives a short server name: api.githubcopilot.com/mcp/ becomes
// githubcopilot, mcp.sentry.dev becomes sentry.
func nameFromURL(rawURL string) string {
	value := rawURL
	for _, prefix := range []string{"https://", "http://"} {
		value = strings.TrimPrefix(value, prefix)
	}
	host, _, _ := strings.Cut(value, "/")
	if cut, _, ok := strings.Cut(host, ":"); ok {
		host = cut
	}
	label := host
	if parts := strings.Split(host, "."); len(parts) > 1 {
		label = parts[0]
	}
	switch label {
	case "www", "mcp", "api", "ai", "app", "server":
		if parts := strings.Split(host, "."); len(parts) > 1 {
			label = parts[1]
		}
	}
	return normalizeServerName(label)
}

func botTomlPath(bot *botCtx) string {
	return filepath.Join(bot.Root, "bot.toml")
}

// addMCPEntries appends entries to bot.toml, refusing duplicate names unless
// forced.
func addMCPEntries(bot *botCtx, entries []mcpEntry, force bool) error {
	existing, err := existingMCPNames(bot.Root)
	if err != nil {
		return err
	}
	fresh := make([]mcpEntry, 0, len(entries))
	var conflicts []string
	for _, entry := range entries {
		if existing[entry.Name] && !force {
			conflicts = append(conflicts, entry.Name)
			continue
		}
		fresh = append(fresh, entry)
	}
	if len(conflicts) > 0 {
		return fmt.Errorf("already in bot.toml: %s (pass --force to add anyway)", strings.Join(conflicts, ", "))
	}
	if err := appendMCPEntries(botTomlPath(bot), fresh); err != nil {
		return err
	}
	return nil
}

func mcpImportCommand(fc fileConfig, bot *botCtx, args []string) {
	force := false
	printOnly := false
	target := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--force", "-force":
			force = true
		case "--print", "-print", "--dry-run", "-dry-run":
			printOnly = true
		case "-h", "--help":
			fmt.Fprint(os.Stderr, "usage: ax mcp import <path|url|cursor-plugin-dir> [--force] [--print]\n")
			return
		default:
			if strings.HasPrefix(args[i], "-") {
				fmt.Fprintf(os.Stderr, "error: unknown mcp import flag %q\n", args[i])
				os.Exit(2)
			}
			if target != "" {
				fmt.Fprintf(os.Stderr, "error: unexpected argument %q\n", args[i])
				os.Exit(2)
			}
			target = args[i]
		}
	}
	if target == "" {
		fmt.Fprint(os.Stderr, "usage: ax mcp import <path|url|cursor-plugin-dir> [--force] [--print]\n")
		os.Exit(2)
	}
	imported, err := loadMCPImport(target, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	entries, skipped := mcpEntriesFromSpecs(imported.Specs)
	if label := strings.TrimSpace(imported.Source); label != "" && len(entries) > 0 {
		fmt.Fprintf(os.Stderr, "%s: %d server(s)\n", label, len(entries))
	}
	for _, reason := range skipped {
		fmt.Fprintf(os.Stderr, "skipped %s\n", reason)
	}
	if len(imported.Required) > 0 {
		fmt.Fprintf(os.Stderr, "required variables: %s\n", strings.Join(imported.Required, ", "))
	}
	if printOnly {
		for _, entry := range entries {
			fmt.Print(renderMCPEntry(entry))
		}
		return
	}
	if len(entries) == 0 {
		fmt.Fprintln(os.Stderr, "nothing to import")
		return
	}
	existing, err := existingMCPNames(bot.Root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fresh := make([]mcpEntry, 0, len(entries))
	for _, entry := range entries {
		if existing[entry.Name] && !force {
			fmt.Fprintf(os.Stderr, "kept existing %s\n", entry.Name)
			continue
		}
		fresh = append(fresh, entry)
	}
	if len(fresh) == 0 {
		fmt.Fprintln(os.Stderr, "every server was already configured (pass --force to add anyway)")
		return
	}
	if err := appendMCPEntries(botTomlPath(bot), fresh); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	names := make([]string, 0, len(fresh))
	for _, entry := range fresh {
		names = append(names, entry.Name)
	}
	fmt.Printf("Added %d server(s) to %s: %s\n", len(fresh), botTomlPath(bot), strings.Join(names, ", "))
	fmt.Println("Servers that use OAuth need `ax mcp login <server>` before their tools appear.")
}

func mcpLoginCommand(fc fileConfig, bot *botCtx, args []string) {
	manual := false
	name := ""
	for _, arg := range args {
		switch arg {
		case "--manual", "-manual", "--no-browser":
			manual = true
		case "-h", "--help":
			fmt.Fprint(os.Stderr, "usage: ax mcp login <server> [--manual]\n")
			return
		default:
			if strings.HasPrefix(arg, "-") {
				fmt.Fprintf(os.Stderr, "error: unknown mcp login flag %q\n", arg)
				os.Exit(2)
			}
			name = arg
		}
	}
	if name == "" {
		fmt.Fprint(os.Stderr, "usage: ax mcp login <server>\n")
		os.Exit(2)
	}
	entry := mcpEntryForName(fc, name)
	if strings.TrimSpace(entry.Name) == "" {
		fmt.Fprintf(os.Stderr, "error: no MCP server named %q in bot.toml ([[connection]] or [[mcp]])\n", name)
		os.Exit(1)
	}
	if entry.URL == "" {
		fmt.Fprintf(os.Stderr, "error: %s is a stdio server; it does not use OAuth\n", name)
		os.Exit(1)
	}
	oauth, err := wantsOAuth(entry)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if !oauth {
		fmt.Fprintf(os.Stderr, "error: %s authenticates with a token (token_env or a header), not OAuth\n", name)
		os.Exit(1)
	}
	config, err := oauthConfigFor(entry)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	serverURL, err := expandEnvValue(entry.URL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: mcp: server %q: url: %v\n", entry.Name, err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), mcpLoginTimeout+time.Minute)
	defer cancel()
	token, err := mcp.Login(ctx, mcp.LoginOptions{
		ServerName: entry.Name,
		ServerURL:  serverURL,
		Config:     config,
		StorePath:  oauthTokenPath(mcpAuthDir(bot), entry.Name),
		Out:        os.Stderr,
		Manual:     manual || !stdinIsTerminal(),
		Timeout:    mcpLoginTimeout,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if token.Expiry.IsZero() {
		fmt.Printf("Logged in to %s\n", entry.Name)
		return
	}
	fmt.Printf("Logged in to %s (token expires %s)\n", entry.Name, token.Expiry.Format(time.RFC3339))
}

func mcpLogoutCommand(fc fileConfig, bot *botCtx, args []string) {
	name := ""
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			fmt.Fprint(os.Stderr, "usage: ax mcp logout <server>\n")
			return
		}
		if strings.HasPrefix(arg, "-") {
			fmt.Fprintf(os.Stderr, "error: unknown mcp logout flag %q\n", arg)
			os.Exit(2)
		}
		name = arg
	}
	if name == "" {
		fmt.Fprint(os.Stderr, "usage: ax mcp logout <server>\n")
		os.Exit(2)
	}
	if strings.TrimSpace(mcpEntryForName(fc, name).Name) == "" {
		fmt.Fprintf(os.Stderr, "error: no MCP server named %q in bot.toml ([[connection]] or [[mcp]])\n", name)
		os.Exit(1)
	}
	path := oauthTokenPath(mcpAuthDir(bot), strings.ToLower(name))
	if _, err := os.Stat(path); os.IsNotExist(err) {
		fmt.Printf("No stored login for %s\n", name)
		return
	}
	if err := mcp.DeleteToken(path); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Printf("Logged out of %s\n", name)
}

func mcpDoctorCommand(fc fileConfig, bot *botCtx, args []string) {
	// Doctor always talks to the server: a cached tool list would hide the
	// expired login or the dead process it is supposed to find.
	refresh := true
	only := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--refresh", "-refresh":
			// Kept for symmetry with `ax catalog --refresh`; doctor always
			// connects.
		case "--server", "-server":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --server needs a name")
				os.Exit(2)
			}
			only = args[i]
		case "-h", "--help":
			fmt.Fprint(os.Stderr, "usage: ax mcp doctor [--server NAME]\n")
			return
		default:
			fmt.Fprintf(os.Stderr, "error: unknown mcp doctor flag %q\n", args[i])
			os.Exit(2)
		}
	}
	resolved, err := resolveAllMCP(fc, bot)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if len(resolved) == 0 {
		fmt.Println("No MCP servers configured.")
		return
	}
	cacheDir := filepath.Join(bot.DataRoot, "state", "ax", "mcp")
	failures := 0
	for _, item := range resolved {
		if only != "" && !strings.EqualFold(item.name, only) {
			continue
		}
		if item.err != nil {
			failures++
			fmt.Printf("%-20s error    %v\n", item.name, item.err)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), catalogLoadTimeout)
		source := newMCPSource(item.cfg, cacheDir, refresh)
		tools, err := source.List(ctx)
		cancel()
		_ = source.Close()
		if err != nil {
			failures++
			status := "error    "
			if errors.Is(err, mcp.ErrLoginRequired) {
				status = "login    "
				err = fmt.Errorf("run ax mcp login %s", item.name)
			}
			fmt.Printf("%-20s %s %v\n", item.name, status, err)
			continue
		}
		if failure := source.Failure(); failure != nil {
			failures++
			status := "stale    "
			message := failure.Error()
			if source.RequiresAuth() {
				status = "login    "
				message = "run ax mcp login " + item.name
			}
			fmt.Printf("%-20s %s %s\n", item.name, status, message)
			continue
		}
		fmt.Printf("%-20s ok       %d %s\n", item.name, len(tools), plural(len(tools), "tool"))
	}
	if failures > 0 {
		os.Exit(1)
	}
}

func plural(count int, singular string) string {
	if count == 1 {
		return singular
	}
	return singular + "s"
}

func oneLine(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len([]rune(value)) > 60 {
		value = string([]rune(value)[:60]) + "..."
	}
	return value
}
