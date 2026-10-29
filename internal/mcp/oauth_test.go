package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeAuthServer struct {
	srv *httptest.Server

	mu           sync.Mutex
	clientID     string
	clientSecret string
	codes        map[string]pendingCode
	access       string
	refresh      string
	tokenCalls   int
	refreshCalls int
	registerHits int
	lastScope    string
	lastResource string
	expiresIn    int
}

type pendingCode struct {
	challenge   string
	redirectURI string
	resource    string
}

func newFakeAuthServer(t *testing.T) *fakeAuthServer {
	t.Helper()
	a := &fakeAuthServer{
		clientID:     "client-1",
		clientSecret: "secret-1",
		codes:        map[string]pendingCode{},
		expiresIn:    3600,
	}
	a.srv = httptest.NewServer(http.HandlerFunc(a.handle))
	t.Cleanup(a.srv.Close)
	return a
}

func (a *fakeAuthServer) mcpURL() string { return a.srv.URL + "/mcp" }

func (a *fakeAuthServer) handle(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/.well-known/oauth-protected-resource"):
		a.writeJSON(w, map[string]any{
			"resource":              a.mcpURL(),
			"authorization_servers": []string{a.srv.URL},
			"scopes_supported":      []string{"mcp.read"},
		})
	case r.URL.Path == "/.well-known/oauth-authorization-server":
		a.writeJSON(w, map[string]any{
			"issuer":                                a.srv.URL,
			"authorization_endpoint":                a.srv.URL + "/authorize",
			"token_endpoint":                        a.srv.URL + "/token",
			"registration_endpoint":                 a.srv.URL + "/register",
			"code_challenge_methods_supported":      []string{"S256"},
			"token_endpoint_auth_methods_supported": []string{"client_secret_post"},
			"scopes_supported":                      []string{"mcp.read", "mcp.write"},
		})
	case r.URL.Path == "/register":
		a.mu.Lock()
		a.registerHits++
		a.mu.Unlock()
		a.writeJSON(w, map[string]any{
			"client_id":                  a.clientID,
			"client_secret":              a.clientSecret,
			"token_endpoint_auth_method": "client_secret_post",
		})
	case r.URL.Path == "/authorize":
		a.authorize(w, r)
	case r.URL.Path == "/token":
		a.token(w, r)
	case r.URL.Path == "/mcp":
		a.mcp(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (a *fakeAuthServer) writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func (a *fakeAuthServer) authorize(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if query.Get("response_type") != "code" || query.Get("client_id") == "" {
		http.Error(w, "bad authorization request", http.StatusBadRequest)
		return
	}
	if query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") == "" {
		http.Error(w, "PKCE required", http.StatusBadRequest)
		return
	}
	code := "code-" + query.Get("state")
	a.mu.Lock()
	a.codes[code] = pendingCode{
		challenge:   query.Get("code_challenge"),
		redirectURI: query.Get("redirect_uri"),
		resource:    query.Get("resource"),
	}
	a.lastScope = query.Get("scope")
	a.lastResource = query.Get("resource")
	a.mu.Unlock()
	location := query.Get("redirect_uri") + "?code=" + code + "&state=" + query.Get("state") + "&iss=" + a.srv.URL
	http.Redirect(w, r, location, http.StatusFound)
}

func (a *fakeAuthServer) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if r.PostForm.Get("client_id") != a.clientID || r.PostForm.Get("client_secret") != a.clientSecret {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"invalid_client"}`)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		a.tokenCalls++
		pending, ok := a.codes[r.PostForm.Get("code")]
		if !ok || pending.redirectURI != r.PostForm.Get("redirect_uri") || pending.resource != r.PostForm.Get("resource") {
			http.Error(w, "invalid_grant", http.StatusBadRequest)
			return
		}
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != pending.challenge {
			http.Error(w, "invalid PKCE verifier", http.StatusBadRequest)
			return
		}
		delete(a.codes, r.PostForm.Get("code"))
		a.access = fmt.Sprintf("access-%d", a.tokenCalls)
		a.refresh = fmt.Sprintf("refresh-%d", a.tokenCalls)
		a.writeJSON(w, map[string]any{
			"access_token":  a.access,
			"refresh_token": a.refresh,
			"token_type":    "Bearer",
			"expires_in":    a.expiresIn,
			"scope":         "mcp.read",
		})
	case "refresh_token":
		a.refreshCalls++
		if r.PostForm.Get("refresh_token") != a.refresh {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		}
		a.access = fmt.Sprintf("access-r%d", a.refreshCalls)
		a.refresh = fmt.Sprintf("refresh-r%d", a.refreshCalls)
		a.writeJSON(w, map[string]any{
			"access_token":  a.access,
			"refresh_token": a.refresh,
			"token_type":    "Bearer",
			"expires_in":    a.expiresIn,
		})
	default:
		http.Error(w, "unsupported grant", http.StatusBadRequest)
	}
}

func (a *fakeAuthServer) mcp(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	access := a.access
	a.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+access || access == "" {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+a.srv.URL+`/.well-known/oauth-protected-resource/mcp"`)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var request struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var result any
	switch request.Method {
	case "server/discover":
		result = map[string]any{
			"supportedVersions": []string{LatestProtocol},
			"capabilities":      map[string]any{"tools": map[string]any{}},
			"serverInfo":        map[string]any{"name": "fake", "version": "1"},
		}
	case "initialize":
		result = map[string]any{
			"protocolVersion": LegacyProtocol,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "fake", "version": "1"},
		}
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
		return
	case "tools/list":
		result = map[string]any{"tools": []map[string]any{{
			"name":        "echo",
			"description": "echo text",
			"inputSchema": map[string]any{"type": "object"},
		}}}
	case "tools/call":
		result = map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}}
	default:
		http.Error(w, "unknown method", http.StatusNotFound)
		return
	}
	if len(request.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	a.writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
}

func (a *fakeAuthServer) redirect(authURL string) (string, error) {
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(authURL)
	if err != nil {
		return "", err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		return "", fmt.Errorf("authorize returned %d", resp.StatusCode)
	}
	return resp.Header.Get("Location"), nil
}

func (a *fakeAuthServer) visit(authURL string) (string, error) {
	location, err := a.redirect(authURL)
	if err != nil {
		return "", err
	}
	callback, err := http.Get(location)
	if err != nil {
		return "", err
	}
	defer func() { _ = callback.Body.Close() }()
	if callback.StatusCode != http.StatusOK {
		return "", fmt.Errorf("callback returned %d", callback.StatusCode)
	}
	return location, nil
}

func (a *fakeAuthServer) snapshot() (access, refresh string, refreshes int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.access, a.refresh, a.refreshCalls
}

func (a *fakeAuthServer) setAccess(token string) {
	a.mu.Lock()
	a.access = token
	a.mu.Unlock()
}

func loginFake(t *testing.T, a *fakeAuthServer, store string) StoredToken {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opts := LoginOptions{
		ServerName: "fake",
		ServerURL:  a.mcpURL(),
		StorePath:  store,
		Out:        io.Discard,
		Timeout:    10 * time.Second,
		OpenURL: func(rawURL string) error {
			go func() {
				if _, err := a.visit(rawURL); err != nil {
					t.Errorf("browser simulation: %v", err)
					cancel()
				}
			}()
			return nil
		},
	}
	token, err := Login(ctx, opts)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	return token
}

func TestOAuthLoginLoopback(t *testing.T) {
	a := newFakeAuthServer(t)
	store := filepath.Join(t.TempDir(), "auth", "fake.json")
	token := loginFake(t, a, store)

	if token.AccessToken == "" || token.RefreshToken == "" {
		t.Fatalf("token = %+v", token)
	}
	if token.ClientID != "client-1" || token.ClientSecret != "secret-1" {
		t.Errorf("client = %q/%q", token.ClientID, token.ClientSecret)
	}
	if token.TokenEndpoint != a.srv.URL+"/token" || token.Issuer != a.srv.URL {
		t.Errorf("metadata = %q %q", token.TokenEndpoint, token.Issuer)
	}
	if token.Resource != a.mcpURL() {
		t.Errorf("resource = %q", token.Resource)
	}
	if token.Expiry.Before(time.Now().Add(30 * time.Minute)) {
		t.Errorf("expiry = %v", token.Expiry)
	}
	info, err := os.Stat(store)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("token mode = %v", mode)
	}
	saved, err := LoadToken(store)
	if err != nil || saved.AccessToken != token.AccessToken {
		t.Fatalf("saved token = %+v, %v", saved, err)
	}
	if a.registerHits != 1 {
		t.Errorf("client registrations = %d", a.registerHits)
	}
	a.mu.Lock()
	scope := a.lastScope
	resource := a.lastResource
	a.mu.Unlock()
	if scope != "" {
		t.Errorf("scope = %q, want empty (server defaults)", scope)
	}
	if resource != a.mcpURL() {
		t.Errorf("authorization resource = %q", resource)
	}
}

func TestOAuthLoginHonoursConfiguredClient(t *testing.T) {
	a := newFakeAuthServer(t)
	store := filepath.Join(t.TempDir(), "fake.json")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opts := LoginOptions{
		ServerName: "fake",
		ServerURL:  a.mcpURL(),
		Config:     OAuthConfig{ClientID: a.clientID, ClientSecret: a.clientSecret, Scopes: []string{"mcp.read"}},
		StorePath:  store,
		Out:        io.Discard,
		Timeout:    10 * time.Second,
		OpenURL: func(rawURL string) error {
			go func() {
				if _, err := a.visit(rawURL); err != nil {
					t.Errorf("browser simulation: %v", err)
					cancel()
				}
			}()
			return nil
		},
	}
	if _, err := Login(ctx, opts); err != nil {
		t.Fatalf("login: %v", err)
	}
	if a.registerHits != 0 {
		t.Errorf("client registrations = %d, want 0 with a configured client", a.registerHits)
	}
	a.mu.Lock()
	scope := a.lastScope
	a.mu.Unlock()
	if scope != "mcp.read" {
		t.Errorf("scope = %q", scope)
	}
}

func TestOAuthLoginManualPaste(t *testing.T) {
	a := newFakeAuthServer(t)
	store := filepath.Join(t.TempDir(), "fake.json")
	input, writer := io.Pipe()
	out := &captureWriter{marker: "Paste the callback URL", ready: make(chan string, 1)}
	go func() {
		authURL := <-out.ready
		location, err := a.redirect(authURL)
		if err != nil {
			_ = writer.CloseWithError(err)
			return
		}
		_, _ = io.WriteString(writer, location+"\n")
		_ = writer.Close()
	}()
	token, err := Login(context.Background(), LoginOptions{
		ServerName: "fake",
		ServerURL:  a.mcpURL(),
		StorePath:  store,
		Out:        out,
		Input:      input,
		Manual:     true,
		Timeout:    10 * time.Second,
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if token.AccessToken == "" {
		t.Fatalf("token = %+v", token)
	}
}

func TestOAuthManualPasteRejectsForeignState(t *testing.T) {
	a := newFakeAuthServer(t)
	input, writer := io.Pipe()
	out := &captureWriter{marker: "Paste the callback URL", ready: make(chan string, 1)}
	go func() {
		authURL := <-out.ready
		location, err := a.redirect(authURL)
		if err != nil {
			_ = writer.CloseWithError(err)
			return
		}
		forged := strings.Replace(location, "&state=", "&state=forged", 1)
		_, _ = io.WriteString(writer, forged+"\n")
		_ = writer.Close()
	}()
	_, err := Login(context.Background(), LoginOptions{
		ServerName: "fake",
		ServerURL:  a.mcpURL(),
		Out:        out,
		Input:      input,
		Manual:     true,
		Timeout:    10 * time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "state mismatch") {
		t.Fatalf("err = %v", err)
	}
}

func TestOAuthTokenSourceRefreshesExpiredToken(t *testing.T) {
	a := newFakeAuthServer(t)
	store := filepath.Join(t.TempDir(), "fake.json")
	token := loginFake(t, a, store)

	token.Expiry = time.Now().Add(-time.Minute)
	if err := SaveToken(store, token); err != nil {
		t.Fatal(err)
	}
	access, _, _ := a.snapshot()
	source := NewOAuthTokenSource(store, a.mcpURL(), nil)
	refreshed, err := source.Token(context.Background(), false)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if refreshed == access {
		t.Errorf("token was not refreshed")
	}
	if _, _, refreshes := a.snapshot(); refreshes != 1 {
		t.Errorf("refreshes = %d", refreshes)
	}
	saved, err := LoadToken(store)
	if err != nil {
		t.Fatal(err)
	}
	if saved.AccessToken != refreshed || saved.RefreshToken == "" {
		t.Errorf("saved = %+v", saved)
	}
	if saved.ClientID != "client-1" || saved.TokenEndpoint != a.srv.URL+"/token" {
		t.Errorf("saved metadata = %+v", saved)
	}
}

func TestOAuthTokenSourceWithoutTokenReportsLoginRequired(t *testing.T) {
	source := NewOAuthTokenSource(filepath.Join(t.TempDir(), "missing.json"), "https://example.test/mcp", nil)
	_, err := source.Token(context.Background(), false)
	if err == nil || !strings.Contains(err.Error(), "login required") {
		t.Fatalf("err = %v", err)
	}
}

func TestHTTPTransportRefreshesAfter401(t *testing.T) {
	a := newFakeAuthServer(t)
	store := filepath.Join(t.TempDir(), "fake.json")
	loginFake(t, a, store)
	a.setAccess("revoked")

	client, err := New(ServerConfig{
		Name: "fake",
		URL:  a.mcpURL(),
		Auth: NewOAuthTokenSource(store, a.mcpURL(), nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = client.Close() }()
	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("tools = %+v", tools)
	}
	if _, _, refreshes := a.snapshot(); refreshes != 1 {
		t.Errorf("refreshes = %d", refreshes)
	}
}

func TestHTTPTransportWithoutTokenAsksForLogin(t *testing.T) {
	a := newFakeAuthServer(t)
	client, err := New(ServerConfig{
		Name: "fake",
		URL:  a.mcpURL(),
		Auth: NewOAuthTokenSource(filepath.Join(t.TempDir(), "missing.json"), a.mcpURL(), nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = client.Connect(context.Background())
	if err == nil {
		t.Fatal("connect succeeded")
	}
	var authErr *AuthRequiredError
	if !strings.Contains(err.Error(), "ax mcp login fake") || !errors.As(err, &authErr) {
		t.Fatalf("err = %v", err)
	}
}

func TestDiscoverResourceErrorsWithoutMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()
	if _, err := DiscoverResource(context.Background(), srv.URL+"/mcp", nil); err == nil {
		t.Fatal("discovery succeeded")
	}
}

func TestParseWWWAuthenticate(t *testing.T) {
	values := parseWWWAuthenticate(`Bearer realm="mcp", resource_metadata="https://example.test/.well-known/oauth-protected-resource", error="invalid_token"`)
	if values["resource_metadata"] != "https://example.test/.well-known/oauth-protected-resource" {
		t.Errorf("resource_metadata = %q", values["resource_metadata"])
	}
	if values["realm"] != "mcp" || values["error"] != "invalid_token" {
		t.Errorf("values = %v", values)
	}
}

func TestCanonicalResource(t *testing.T) {
	cases := map[string]string{
		"HTTPS://Example.Test/mcp/":         "https://example.test/mcp/",
		"https://example.test/":             "https://example.test",
		"https://example.test/mcp#fragment": "https://example.test/mcp",
	}
	for input, want := range cases {
		if got := canonicalResource(input, "https://fallback.test"); got != want {
			t.Errorf("canonicalResource(%q) = %q, want %q", input, got, want)
		}
	}
	if got := canonicalResource("", "https://fallback.test/mcp"); got != "https://fallback.test/mcp" {
		t.Errorf("fallback = %q", got)
	}
}

type captureWriter struct {
	marker string
	ready  chan string

	mu   sync.Mutex
	buf  strings.Builder
	sent bool
}

func (w *captureWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	if w.sent {
		return len(p), nil
	}
	text := w.buf.String()
	if !strings.Contains(text, w.marker) {
		return len(p), nil
	}
	start := strings.Index(text, "http://")
	if start < 0 {
		return len(p), nil
	}
	rest := text[start:]
	end := strings.IndexByte(rest, '\n')
	if end < 0 {
		return len(p), nil
	}
	w.sent = true
	w.ready <- strings.TrimSpace(rest[:end])
	return len(p), nil
}
