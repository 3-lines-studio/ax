package mcp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ErrLoginRequired means the server needs OAuth and no usable token is stored.
var ErrLoginRequired = errors.New("login required")

// AuthRequiredError names the server that needs an interactive login, so the
// catalog can show a precise hint instead of a generic failure.
type AuthRequiredError struct {
	Server string
}

func (e *AuthRequiredError) Error() string {
	return fmt.Sprintf("mcp: server %q: authentication required; run ax mcp login %s", e.Server, e.Server)
}

// Unwrap lets errors.Is(err, ErrLoginRequired) match.
func (e *AuthRequiredError) Unwrap() error { return ErrLoginRequired }

// OAuthConfig is the pre-registered client information for a server.
type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	Scopes       []string
	RedirectPort int
}

// StoredToken is the durable OAuth state for one server.
type StoredToken struct {
	AccessToken   string    `json:"access_token"`
	RefreshToken  string    `json:"refresh_token,omitempty"`
	TokenType     string    `json:"token_type,omitempty"`
	Scope         string    `json:"scope,omitempty"`
	Expiry        time.Time `json:"expiry,omitempty"`
	ClientID      string    `json:"client_id,omitempty"`
	ClientSecret  string    `json:"client_secret,omitempty"`
	AuthStyle     string    `json:"auth_style,omitempty"`
	TokenEndpoint string    `json:"token_endpoint,omitempty"`
	Issuer        string    `json:"issuer,omitempty"`
	Resource      string    `json:"resource,omitempty"`
}

func (t StoredToken) expired() bool {
	return !t.Expiry.IsZero() && time.Now().Add(time.Minute).After(t.Expiry)
}

// LoadToken reads a token file. A missing file is ErrLoginRequired.
func LoadToken(path string) (StoredToken, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return StoredToken{}, ErrLoginRequired
		}
		return StoredToken{}, fmt.Errorf("read token %s: %w", path, err)
	}
	var token StoredToken
	if err := json.Unmarshal(data, &token); err != nil {
		return StoredToken{}, fmt.Errorf("parse token %s: %w", path, err)
	}
	if token.AccessToken == "" {
		return StoredToken{}, ErrLoginRequired
	}
	return token, nil
}

// SaveToken writes a token file atomically with mode 0600.
func SaveToken(path string, token StoredToken) error {
	dir := dirOf(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create token dir %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(token, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".token-*")
	if err != nil {
		return fmt.Errorf("create token: %w", err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

// DeleteToken removes a stored token.
func DeleteToken(path string) error {
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func dirOf(path string) string {
	if index := strings.LastIndexByte(path, '/'); index >= 0 {
		return path[:index]
	}
	return "."
}

// OAuthTokenSource loads and refreshes one server's token.
type OAuthTokenSource struct {
	Path     string
	Resource string
	Client   *http.Client
	mu       sync.Mutex
}

// NewOAuthTokenSource builds a token source for a stored OAuth session.
func NewOAuthTokenSource(path, resource string, client *http.Client) *OAuthTokenSource {
	if client == nil {
		client = defaultHTTPClient()
	}
	return &OAuthTokenSource{Path: path, Resource: resource, Client: client}
}

// Token returns a usable access token, refreshing it when needed.
func (s *OAuthTokenSource) Token(ctx context.Context, force bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	token, err := LoadToken(s.Path)
	if err != nil {
		return "", ErrLoginRequired
	}
	if !force && !token.expired() {
		return token.AccessToken, nil
	}
	if token.RefreshToken == "" || token.TokenEndpoint == "" {
		return "", ErrLoginRequired
	}
	refreshed, err := refreshToken(ctx, s.Client, token, s.Resource)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrLoginRequired, err)
	}
	if err := SaveToken(s.Path, refreshed); err != nil {
		return "", err
	}
	return refreshed.AccessToken, nil
}

// ResourceMetadata is the OAuth Protected Resource Metadata document.
type ResourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

// AuthServerMetadata is the OAuth Authorization Server Metadata document.
type AuthServerMetadata struct {
	Issuer                                     string   `json:"issuer"`
	AuthorizationEndpoint                      string   `json:"authorization_endpoint"`
	TokenEndpoint                              string   `json:"token_endpoint"`
	RegistrationEndpoint                       string   `json:"registration_endpoint"`
	ScopesSupported                            []string `json:"scopes_supported"`
	CodeChallengeMethodsSupported              []string `json:"code_challenge_methods_supported"`
	TokenEndpointAuthMethodsSupported          []string `json:"token_endpoint_auth_methods_supported"`
	ClientIDMetadataDocumentSupported          bool     `json:"client_id_metadata_document_supported"`
	AuthorizationResponseIssParameterSupported bool     `json:"authorization_response_iss_parameter_supported"`
}

type clientRegistration struct {
	ClientID                string `json:"client_id"`
	ClientSecret            string `json:"client_secret"`
	TokenEndpointAuthMethod string `json:"token_endpoint_auth_method"`
}

// LoginOptions drives the interactive authorization flow.
type LoginOptions struct {
	ServerName string
	ServerURL  string
	Config     OAuthConfig
	StorePath  string
	Out        io.Writer
	Input      io.Reader
	Manual     bool
	Timeout    time.Duration
	Client     *http.Client
	OpenURL    func(string) error
}

// Login runs discovery, client registration, PKCE and the token exchange.
func Login(ctx context.Context, opts LoginOptions) (StoredToken, error) {
	if opts.Client == nil {
		opts.Client = defaultHTTPClient()
	}
	if opts.Out == nil {
		opts.Out = os.Stderr
	}
	if opts.Input == nil {
		opts.Input = os.Stdin
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Minute
	}
	if opts.OpenURL == nil {
		opts.OpenURL = openBrowser
	}
	resource, err := DiscoverResource(ctx, opts.ServerURL, opts.Client)
	if err != nil {
		return StoredToken{}, err
	}
	if len(resource.AuthorizationServers) == 0 {
		return StoredToken{}, fmt.Errorf("mcp: server %q did not advertise an authorization server", opts.ServerName)
	}
	authServer, err := DiscoverAuthServer(ctx, resource.AuthorizationServers, opts.Client)
	if err != nil {
		return StoredToken{}, err
	}
	if authServer.AuthorizationEndpoint == "" || authServer.TokenEndpoint == "" {
		return StoredToken{}, fmt.Errorf("mcp: authorization server %q has no authorization or token endpoint", authServer.Issuer)
	}

	redirectPort := opts.Config.RedirectPort
	var listener net.Listener
	if opts.Manual {
		port, err := freePort(redirectPort)
		if err != nil {
			return StoredToken{}, err
		}
		redirectPort = port
	} else {
		listener, redirectPort, err = listenLoopback(redirectPort)
		if err != nil {
			return StoredToken{}, err
		}
		defer func() { _ = listener.Close() }()
	}
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", redirectPort)

	client, err := oauthClient(ctx, authServer, opts.Config, redirectURI, opts.Client)
	if err != nil {
		return StoredToken{}, err
	}
	verifier, challenge, err := pkce()
	if err != nil {
		return StoredToken{}, err
	}
	state, err := randomToken(16)
	if err != nil {
		return StoredToken{}, err
	}
	scope := strings.Join(opts.Config.Scopes, " ")
	authURL, err := authorizationURL(authServer, client.ClientID, redirectURI, state, challenge, scope, canonicalResource(resource.Resource, opts.ServerURL))
	if err != nil {
		return StoredToken{}, err
	}

	if listener == nil {
		fmt.Fprintf(opts.Out, "Open this URL to authorize %s:\n\n%s\n\n", opts.ServerName, authURL)
		fmt.Fprint(opts.Out, "Paste the callback URL (or the authorization code): ")
	} else {
		fmt.Fprintf(opts.Out, "Open this URL to authorize %s:\n\n%s\n\nWaiting for the callback on %s ...\n", opts.ServerName, authURL, redirectURI)
		if err := opts.OpenURL(authURL); err != nil {
			fmt.Fprintf(opts.Out, "Could not open a browser: %v\n", err)
		}
	}

	code := ""
	if opts.Manual {
		line, err := bufio.NewReader(opts.Input).ReadString('\n')
		if err != nil && err != io.EOF {
			return StoredToken{}, err
		}
		code, err = callbackCode(strings.TrimSpace(line), state, authServer)
		if err != nil {
			return StoredToken{}, err
		}
	} else {
		result, err := waitCallback(ctx, opts.Timeout, listener)
		if err != nil {
			return StoredToken{}, err
		}
		if result.err != nil {
			return StoredToken{}, result.err
		}
		if result.state != state {
			return StoredToken{}, fmt.Errorf("mcp: authorization callback state mismatch")
		}
		if err := validateIssuer(result.iss, authServer); err != nil {
			return StoredToken{}, err
		}
		code = result.code
	}
	if code == "" {
		return StoredToken{}, fmt.Errorf("mcp: authorization did not return a code")
	}

	stored, err := exchangeCode(ctx, opts.Client, authServer, client, code, redirectURI, verifier, canonicalResource(resource.Resource, opts.ServerURL))
	if err != nil {
		return StoredToken{}, err
	}
	if opts.StorePath != "" {
		if err := SaveToken(opts.StorePath, stored); err != nil {
			return StoredToken{}, err
		}
	}
	return stored, nil
}

type callbackResult struct {
	code  string
	state string
	iss   string
	err   error
}

// waitCallback serves the loopback redirect. It speaks just enough HTTP/1.1 to
// read one request and answer it, which keeps the MCP package out of
// net/http's server (and its HTTP/2 bundle).
func waitCallback(ctx context.Context, timeout time.Duration, listener net.Listener) (callbackResult, error) {
	results := make(chan callbackResult, 1)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go handleCallbackConn(conn, results)
		}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-results:
		if result.err == nil {
			// Give the browser a moment to read the page before the listener
			// closes under it.
			time.Sleep(100 * time.Millisecond)
		}
		return result, nil
	case <-ctx.Done():
		return callbackResult{}, ctx.Err()
	case <-timer.C:
		return callbackResult{}, fmt.Errorf("mcp: timed out waiting for the authorization callback")
	}
}

func handleCallbackConn(conn net.Conn, results chan<- callbackResult) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	request, err := http.ReadRequest(bufio.NewReader(conn))
	if err != nil {
		return
	}
	if request.URL.Path != "/callback" {
		writeHTTP(conn, http.StatusNotFound, "text/plain; charset=utf-8", "not found")
		return
	}
	query := request.URL.Query()
	result := callbackResult{code: query.Get("code"), state: query.Get("state"), iss: query.Get("iss")}
	if message := query.Get("error"); message != "" {
		result.err = fmt.Errorf("mcp: authorization failed: %s %s", message, query.Get("error_description"))
	}
	writeHTTP(conn, http.StatusOK, "text/html; charset=utf-8", "<html><body><p>Authorization received. You can close this window.</p></body></html>")
	select {
	case results <- result:
	default:
	}
}

func writeHTTP(conn net.Conn, status int, contentType, body string) {
	fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\n", status, http.StatusText(status))
	fmt.Fprintf(conn, "Content-Type: %s\r\n", contentType)
	fmt.Fprintf(conn, "Content-Length: %d\r\n", len(body))
	fmt.Fprint(conn, "Connection: close\r\n\r\n")
	fmt.Fprint(conn, body)
}

func oauthClient(ctx context.Context, meta AuthServerMetadata, cfg OAuthConfig, redirectURI string, client *http.Client) (clientRegistration, error) {
	if cfg.ClientID != "" {
		return clientRegistration{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret}, nil
	}
	if meta.RegistrationEndpoint == "" {
		return clientRegistration{}, fmt.Errorf("mcp: server requires OAuth but supports neither client registration nor a configured client_id")
	}
	body := map[string]any{
		"client_name":                "ax",
		"client_uri":                 "https://github.com/3-lines-studio/ax",
		"redirect_uris":              []string{redirectURI, strings.Replace(redirectURI, "127.0.0.1", "localhost", 1)},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
		"application_type":           "native",
	}
	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, meta.RegistrationEndpoint, bytes.NewReader(data))
	if err != nil {
		return clientRegistration{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return clientRegistration{}, fmt.Errorf("mcp: register OAuth client: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return clientRegistration{}, fmt.Errorf("mcp: register OAuth client: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	var registration clientRegistration
	if err := json.Unmarshal(payload, &registration); err != nil {
		return clientRegistration{}, fmt.Errorf("mcp: decode client registration: %w", err)
	}
	if registration.ClientID == "" {
		return clientRegistration{}, fmt.Errorf("mcp: client registration returned no client_id")
	}
	if registration.TokenEndpointAuthMethod == "" {
		registration.TokenEndpointAuthMethod = "none"
	}
	return registration, nil
}

func authorizationURL(meta AuthServerMetadata, clientID, redirectURI, state, challenge, scope, resource string) (string, error) {
	parsed, err := url.Parse(meta.AuthorizationEndpoint)
	if err != nil {
		return "", fmt.Errorf("mcp: invalid authorization endpoint: %w", err)
	}
	query := parsed.Query()
	query.Set("response_type", "code")
	query.Set("client_id", clientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("state", state)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	if scope != "" {
		query.Set("scope", scope)
	}
	query.Set("resource", resource)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func exchangeCode(ctx context.Context, client *http.Client, meta AuthServerMetadata, registration clientRegistration, code, redirectURI, verifier, resource string) (StoredToken, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", registration.ClientID)
	form.Set("code_verifier", verifier)
	form.Set("resource", resource)
	token, style, err := postToken(ctx, client, meta, registration, form)
	if err != nil {
		return StoredToken{}, err
	}
	token.ClientID = registration.ClientID
	token.ClientSecret = registration.ClientSecret
	token.AuthStyle = style
	token.TokenEndpoint = meta.TokenEndpoint
	token.Issuer = meta.Issuer
	token.Resource = resource
	return token, nil
}

func refreshToken(ctx context.Context, client *http.Client, stored StoredToken, resource string) (StoredToken, error) {
	meta := AuthServerMetadata{Issuer: stored.Issuer, TokenEndpoint: stored.TokenEndpoint}
	switch stored.AuthStyle {
	case "post":
		meta.TokenEndpointAuthMethodsSupported = []string{"client_secret_post"}
	case "basic":
		meta.TokenEndpointAuthMethodsSupported = []string{"client_secret_basic"}
	}
	registration := clientRegistration{ClientID: stored.ClientID, ClientSecret: stored.ClientSecret}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", stored.RefreshToken)
	form.Set("client_id", stored.ClientID)
	if resource == "" {
		resource = stored.Resource
	}
	if resource != "" {
		form.Set("resource", resource)
	}
	refreshed, style, err := postToken(ctx, client, meta, registration, form)
	if err != nil {
		return StoredToken{}, err
	}
	if style == "" {
		style = stored.AuthStyle
	}
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = stored.RefreshToken
	}
	refreshed.ClientID = stored.ClientID
	refreshed.ClientSecret = stored.ClientSecret
	refreshed.AuthStyle = style
	refreshed.TokenEndpoint = stored.TokenEndpoint
	refreshed.Issuer = stored.Issuer
	refreshed.Resource = stored.Resource
	return refreshed, nil
}

func postToken(ctx context.Context, client *http.Client, meta AuthServerMetadata, registration clientRegistration, form url.Values) (StoredToken, string, error) {
	posts := []bool{false}
	if registration.ClientSecret != "" && !hasString(meta.TokenEndpointAuthMethodsSupported, "client_secret_basic") {
		posts = []bool{true, false}
	}
	if registration.ClientSecret != "" && hasString(meta.TokenEndpointAuthMethodsSupported, "client_secret_post") {
		posts = []bool{true}
	}
	var last error
	for _, inBody := range posts {
		token, err := postTokenOnce(ctx, client, meta.TokenEndpoint, registration, form, inBody)
		if err == nil {
			style := ""
			switch {
			case registration.ClientSecret == "":
			case inBody:
				style = "post"
			default:
				style = "basic"
			}
			return token, style, nil
		}
		last = err
	}
	return StoredToken{}, "", last
}

func postTokenOnce(ctx context.Context, client *http.Client, endpoint string, registration clientRegistration, form url.Values, secretInBody bool) (StoredToken, error) {
	body := url.Values{}
	for key, values := range form {
		for _, value := range values {
			body.Add(key, value)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body.Encode()))
	if err != nil {
		return StoredToken{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if registration.ClientSecret != "" {
		if secretInBody {
			body.Set("client_secret", registration.ClientSecret)
			req.Body = io.NopCloser(strings.NewReader(body.Encode()))
			req.ContentLength = int64(len(body.Encode()))
		} else {
			req.SetBasicAuth(registration.ClientID, registration.ClientSecret)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return StoredToken{}, fmt.Errorf("mcp: token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var response struct {
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		TokenType        string `json:"token_type"`
		ExpiresIn        int64  `json:"expires_in"`
		Scope            string `json:"scope"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return StoredToken{}, fmt.Errorf("mcp: decode token response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || response.Error != "" {
		message := response.Error
		if response.ErrorDescription != "" {
			message += ": " + response.ErrorDescription
		}
		if message == "" {
			message = strings.TrimSpace(string(payload))
		}
		return StoredToken{}, fmt.Errorf("mcp: token endpoint: %s", message)
	}
	if response.AccessToken == "" {
		return StoredToken{}, fmt.Errorf("mcp: token endpoint returned no access_token")
	}
	token := StoredToken{
		AccessToken:  response.AccessToken,
		RefreshToken: response.RefreshToken,
		TokenType:    response.TokenType,
		Scope:        response.Scope,
	}
	if response.ExpiresIn > 0 {
		token.Expiry = time.Now().Add(time.Duration(response.ExpiresIn) * time.Second)
	}
	return token, nil
}

// defaultHTTPClient is the fallback for callers that do not share a client.
func defaultHTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

// DiscoverResource finds the protected resource metadata for a server.
func DiscoverResource(ctx context.Context, serverURL string, client *http.Client) (ResourceMetadata, error) {
	if client == nil {
		client = defaultHTTPClient()
	}
	parsed, err := url.Parse(serverURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ResourceMetadata{}, fmt.Errorf("mcp: invalid server url %q", serverURL)
	}
	var candidates []string
	if metadataURL, err := probeResourceMetadata(ctx, serverURL, client); err == nil && metadataURL != "" {
		candidates = append(candidates, metadataURL)
	}
	origin := parsed.Scheme + "://" + parsed.Host
	if parsed.Path != "" && parsed.Path != "/" {
		candidates = append(candidates, origin+"/.well-known/oauth-protected-resource/"+strings.TrimPrefix(parsed.Path, "/"))
	}
	candidates = append(candidates, origin+"/.well-known/oauth-protected-resource")
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if seen[candidate] {
			continue
		}
		seen[candidate] = true
		var metadata ResourceMetadata
		if err := getJSON(ctx, client, candidate, &metadata); err != nil {
			continue
		}
		if len(metadata.AuthorizationServers) > 0 {
			if metadata.Resource == "" {
				metadata.Resource = serverURL
			}
			return metadata, nil
		}
	}
	return ResourceMetadata{}, fmt.Errorf("mcp: server %q does not expose OAuth protected resource metadata", serverURL)
}

func probeResourceMetadata(ctx context.Context, serverURL string, client *http.Client) (string, error) {
	body := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":%q,"io.modelcontextprotocol/clientInfo":{"name":"ax","version":%q},"io.modelcontextprotocol/clientCapabilities":{}}}}`, LatestProtocol, Version))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		return "", fmt.Errorf("not unauthorized")
	}
	header := resp.Header.Get("WWW-Authenticate")
	values := parseWWWAuthenticate(header)
	return values["resource_metadata"], nil
}

func parseWWWAuthenticate(header string) map[string]string {
	// Drop the auth scheme ("Bearer") so the first parameter is parsed alone.
	if scheme, rest, ok := strings.Cut(strings.TrimSpace(header), " "); ok && !strings.Contains(scheme, "=") {
		header = rest
	}
	values := map[string]string{}
	for _, part := range splitHeader(header) {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"`)
		if key != "" {
			values[strings.ToLower(key)] = value
		}
	}
	return values
}

func splitHeader(header string) []string {
	var parts []string
	var current strings.Builder
	inQuotes := false
	for _, r := range header {
		switch r {
		case '"':
			inQuotes = !inQuotes
			current.WriteRune(r)
		case ',':
			if inQuotes {
				current.WriteRune(r)
			} else {
				parts = append(parts, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}

// DiscoverAuthServer tries OAuth and OIDC metadata endpoints for the issuer.
func DiscoverAuthServer(ctx context.Context, issuers []string, client *http.Client) (AuthServerMetadata, error) {
	if client == nil {
		client = defaultHTTPClient()
	}
	var last error
	for _, issuer := range issuers {
		metadata, err := discoverAuthServer(ctx, issuer, client)
		if err == nil {
			return metadata, nil
		}
		last = err
	}
	if last == nil {
		last = errors.New("no authorization server")
	}
	return AuthServerMetadata{}, last
}

func discoverAuthServer(ctx context.Context, issuer string, client *http.Client) (AuthServerMetadata, error) {
	parsed, err := url.Parse(issuer)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return AuthServerMetadata{}, fmt.Errorf("mcp: invalid authorization server %q", issuer)
	}
	origin := parsed.Scheme + "://" + parsed.Host
	path := strings.Trim(parsed.Path, "/")
	var candidates []string
	if path != "" {
		candidates = []string{
			origin + "/.well-known/oauth-authorization-server/" + path,
			origin + "/.well-known/openid-configuration/" + path,
			origin + "/" + path + "/.well-known/openid-configuration",
		}
	} else {
		candidates = []string{
			origin + "/.well-known/oauth-authorization-server",
			origin + "/.well-known/openid-configuration",
		}
	}
	var last error
	for _, candidate := range candidates {
		var metadata AuthServerMetadata
		if err := getJSON(ctx, client, candidate, &metadata); err != nil {
			last = err
			continue
		}
		if metadata.Issuer != issuer {
			last = fmt.Errorf("mcp: authorization server metadata issuer %q does not match %q", metadata.Issuer, issuer)
			continue
		}
		return metadata, nil
	}
	if last == nil {
		last = fmt.Errorf("mcp: no metadata for authorization server %q", issuer)
	}
	return AuthServerMetadata{}, last
}

func getJSON(ctx context.Context, client *http.Client, endpoint string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", endpoint, resp.StatusCode)
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, target)
}

func callbackCode(line, expectedState string, metadata AuthServerMetadata) (string, error) {
	if line == "" {
		return "", fmt.Errorf("mcp: no callback URL or code provided")
	}
	if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
		parsed, err := url.Parse(line)
		if err != nil {
			return "", err
		}
		query := parsed.Query()
		if message := query.Get("error"); message != "" {
			return "", fmt.Errorf("mcp: authorization failed: %s %s", message, query.Get("error_description"))
		}
		if expectedState != "" && query.Get("state") != expectedState {
			return "", fmt.Errorf("mcp: authorization callback state mismatch")
		}
		if err := validateIssuer(query.Get("iss"), metadata); err != nil {
			return "", err
		}
		return query.Get("code"), nil
	}
	return line, nil
}

func validateIssuer(iss string, metadata AuthServerMetadata) error {
	if iss == "" {
		if metadata.AuthorizationResponseIssParameterSupported {
			return fmt.Errorf("mcp: authorization response is missing the required iss parameter")
		}
		return nil
	}
	if metadata.Issuer != "" && iss != metadata.Issuer {
		return fmt.Errorf("mcp: authorization response issuer %q does not match %q", iss, metadata.Issuer)
	}
	return nil
}

func pkce() (string, string, error) {
	verifier, err := randomToken(32)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

func randomToken(bytesCount int) (string, error) {
	data := make([]byte, bytesCount)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func canonicalResource(resource, fallback string) string {
	if resource == "" {
		resource = fallback
	}
	parsed, err := url.Parse(resource)
	if err != nil {
		return resource
	}
	parsed.Fragment = ""
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	if parsed.Path == "/" {
		parsed.Path = ""
	}
	return parsed.String()
}

func freePort(preferred int) (int, error) {
	ports := []int{preferred, 43721, 43722, 43723, 43724, 43725}
	for _, port := range ports {
		if port <= 0 {
			continue
		}
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			_ = listener.Close()
			return port, nil
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port, nil
}

func listenLoopback(preferred int) (net.Listener, int, error) {
	ports := []int{preferred, 43721, 43722, 43723, 43724, 43725}
	for _, port := range ports {
		if port <= 0 {
			continue
		}
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			return listener, port, nil
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, 0, err
	}
	return listener, listener.Addr().(*net.TCPAddr).Port, nil
}

func openBrowser(rawURL string) error {
	if browser := os.Getenv("BROWSER"); browser != "" {
		parts := strings.Fields(browser)
		if len(parts) > 0 {
			args := append([]string{}, parts[1:]...)
			for _, arg := range args {
				if strings.Contains(arg, "%s") {
					return exec.Command(parts[0], strings.ReplaceAll(arg, "%s", rawURL)).Start()
				}
			}
			return exec.Command(parts[0], append(args, rawURL)...).Start()
		}
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", rawURL)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	default:
		cmd = exec.Command("xdg-open", rawURL)
	}
	return cmd.Start()
}

func hasString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
