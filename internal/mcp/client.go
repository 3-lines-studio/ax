package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Version is reported to servers as the client version.
const Version = "1.0.0"

const defaultTimeout = 30 * time.Second

type transport interface {
	RoundTrip(ctx context.Context, request []byte) ([]byte, error)
	Notify(ctx context.Context, notification []byte) error
	Close() error
	SetProtocol(version string)
	SetSessionID(id string)
}

// Client is one connected MCP server.
type Client struct {
	cfg     ServerConfig
	session transport

	mu       sync.Mutex
	nextID   int64
	protocol string
	info     ServerInfo
	prompt   string
	ttl      time.Duration
	closed   bool
}

// New validates a server configuration. Call Connect to start it.
func New(cfg ServerConfig) (*Client, error) {
	if strings.TrimSpace(cfg.Name) == "" {
		return nil, errors.New("mcp: server name is required")
	}
	if (cfg.Command == "") == (cfg.URL == "") {
		return nil, fmt.Errorf("mcp: server %q needs exactly one of command or url", cfg.Name)
	}
	if cfg.URL != "" {
		u, err := url.Parse(cfg.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return nil, fmt.Errorf("mcp: server %q: invalid url %q", cfg.Name, cfg.URL)
		}
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.Timeout < time.Second {
		cfg.Timeout = time.Second
	}
	return &Client{cfg: cfg}, nil
}

// Connect starts the server and negotiates a protocol version.
func (c *Client) Connect(ctx context.Context) error {
	if c.session != nil {
		return nil
	}
	var (
		s   transport
		err error
	)
	if c.cfg.URL != "" {
		s = newHTTPTransport(c.cfg)
	} else {
		s, err = newStdioTransport(c.cfg)
		if err != nil {
			return err
		}
	}
	c.session = s
	if err := c.negotiate(ctx); err != nil {
		_ = s.Close()
		c.session = nil
		return err
	}
	return nil
}

// Close terminates the server process or connection.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	s := c.session
	c.mu.Unlock()
	if s == nil {
		return nil
	}
	return s.Close()
}

// Protocol returns the negotiated protocol version.
func (c *Client) Protocol() string { return c.protocol }

// Info returns the server identity.
func (c *Client) Info() ServerInfo { return c.info }

// Instructions returns the server's usage instructions, if any.
func (c *Client) Instructions() string { return c.prompt }

// TTL returns the server's suggested catalog freshness hint.
func (c *Client) TTL() time.Duration { return c.ttl }

type discoverResult struct {
	SupportedVersions []string   `json:"supportedVersions"`
	Capabilities      any        `json:"capabilities"`
	ServerInfo        ServerInfo `json:"serverInfo"`
	Instructions      string     `json:"instructions"`
	TTLMs             int64      `json:"ttlMs"`
}

type initializeResult struct {
	ProtocolVersion string     `json:"protocolVersion"`
	Capabilities    any        `json:"capabilities"`
	ServerInfo      ServerInfo `json:"serverInfo"`
	Instructions    string     `json:"instructions"`
}

func (c *Client) negotiate(ctx context.Context) error {
	raw, discoverErr := c.call(ctx, "server/discover", nil, true)
	if discoverErr == nil {
		var res discoverResult
		if err := json.Unmarshal(raw, &res); err != nil {
			return fmt.Errorf("mcp: server %q: decode discover: %w", c.cfg.Name, err)
		}
		version := pickVersion(res.SupportedVersions)
		if version == "" {
			return fmt.Errorf("mcp: server %q supports no known protocol (offered %v)", c.cfg.Name, res.SupportedVersions)
		}
		c.protocol = version
		c.info = res.ServerInfo
		c.prompt = res.Instructions
		if res.TTLMs > 0 {
			c.ttl = time.Duration(res.TTLMs) * time.Millisecond
		}
		c.session.SetProtocol(version)
		return nil
	}

	// Older servers do not know server/discover. Fall back to the initialize
	// handshake; the error from discover is only reported if initialize also
	// fails.
	raw, initErr := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": LegacyProtocol,
		"capabilities":    map[string]any{},
		"clientInfo":      clientInfo(),
	}, false)
	if initErr != nil {
		return fmt.Errorf("mcp: server %q: initialize: %w (discover: %v)", c.cfg.Name, initErr, discoverErr)
	}
	var res initializeResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return fmt.Errorf("mcp: server %q: decode initialize: %w", c.cfg.Name, err)
	}
	version := res.ProtocolVersion
	if version == "" {
		version = LegacyProtocol
	}
	if !supportedLegacy(version) && version != LatestProtocol {
		return fmt.Errorf("mcp: server %q negotiated unsupported protocol %q", c.cfg.Name, version)
	}
	c.protocol = version
	c.info = res.ServerInfo
	c.prompt = res.Instructions
	c.session.SetProtocol(version)
	if err := c.notify(ctx, "notifications/initialized", nil); err != nil {
		return err
	}
	return nil
}

func pickVersion(offered []string) string {
	for _, version := range offered {
		if version == LatestProtocol {
			return version
		}
	}
	for _, accepted := range legacyProtocols {
		for _, version := range offered {
			if version == accepted {
				return version
			}
		}
	}
	return ""
}

func supportedLegacy(version string) bool {
	for _, accepted := range legacyProtocols {
		if version == accepted {
			return true
		}
	}
	return version == LatestProtocol
}

func clientInfo() map[string]string {
	return map[string]string{"name": "ax", "version": Version}
}

func (c *Client) meta(protocol string) map[string]any {
	version := protocol
	if version == "" {
		version = LatestProtocol
	}
	return map[string]any{
		"io.modelcontextprotocol/protocolVersion":    version,
		"io.modelcontextprotocol/clientInfo":         clientInfo(),
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}
}

// call sends one JSON-RPC request and returns its result. Requests are
// serialized per server; MCP servers are small and the engine's catalog work
// is not latency-sensitive at that granularity.
func (c *Client) call(ctx context.Context, method string, params map[string]any, forceMeta bool) (json.RawMessage, error) {
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	protocol := c.protocol
	session := c.session
	c.mu.Unlock()
	if session == nil {
		return nil, errors.New("mcp: client is not connected")
	}

	payload := map[string]any{}
	for key, value := range params {
		payload[key] = value
	}
	if forceMeta || protocol == LatestProtocol {
		payload["_meta"] = c.meta(protocol)
	}
	request := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": payload}
	data, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("mcp: encode %s: %w", method, err)
	}

	callCtx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	response, err := session.RoundTrip(callCtx, data)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("mcp: %s timed out after %s; side effects may be unknown", method, c.cfg.Timeout)
		}
		return nil, err
	}

	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *RPCError       `json:"error"`
	}
	if err := json.Unmarshal(response, &envelope); err != nil {
		return nil, fmt.Errorf("mcp: decode %s response: %w", method, err)
	}
	if envelope.Error != nil {
		return nil, envelope.Error
	}
	if len(envelope.Result) == 0 {
		return nil, fmt.Errorf("mcp: %s returned no result", method)
	}
	return envelope.Result, nil
}

func (c *Client) notify(ctx context.Context, method string, params map[string]any) error {
	c.mu.Lock()
	session := c.session
	protocol := c.protocol
	c.mu.Unlock()
	if session == nil {
		return errors.New("mcp: client is not connected")
	}
	payload := map[string]any{}
	for key, value := range params {
		payload[key] = value
	}
	if protocol == LatestProtocol {
		payload["_meta"] = c.meta(protocol)
	}
	data, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": payload})
	if err != nil {
		return fmt.Errorf("mcp: encode %s: %w", method, err)
	}
	callCtx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	return session.Notify(callCtx, data)
}

// ListTools reads every page of tools/list.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var tools []Tool
	cursor := ""
	for page := 0; page < 100; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.call(ctx, "tools/list", params, false)
		if err != nil {
			return nil, err
		}
		var result struct {
			Tools      []Tool `json:"tools"`
			NextCursor string `json:"nextCursor"`
			TTLMs      int64  `json:"ttlMs"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, fmt.Errorf("mcp: server %q: decode tools/list: %w", c.cfg.Name, err)
		}
		for _, tool := range result.Tools {
			if strings.TrimSpace(tool.Name) == "" {
				continue
			}
			if len(tool.InputSchema) == 0 || string(tool.InputSchema) == "null" {
				tool.InputSchema = json.RawMessage(`{"type":"object"}`)
			}
			tools = append(tools, tool)
		}
		if result.TTLMs > 0 {
			c.ttl = time.Duration(result.TTLMs) * time.Millisecond
		}
		if result.NextCursor == "" || result.NextCursor == cursor {
			break
		}
		cursor = result.NextCursor
	}
	return tools, nil
}

// CallTool runs one tool. A tool-level failure is a Result with IsError set,
// not a Go error: the model needs to see the output to recover.
func (c *Client) CallTool(ctx context.Context, name string, arguments json.RawMessage) (Result, error) {
	var args any = map[string]any{}
	if len(arguments) > 0 && string(arguments) != "null" {
		if err := json.Unmarshal(arguments, &args); err != nil {
			return Result{}, fmt.Errorf("mcp: invalid arguments for %s: %w", name, err)
		}
	}
	raw, err := c.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, false)
	if err != nil {
		return Result{}, err
	}
	var result Result
	if err := json.Unmarshal(raw, &result); err != nil {
		return Result{}, fmt.Errorf("mcp: server %q: decode tools/call: %w", c.cfg.Name, err)
	}
	return result, nil
}
