package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthToken(t *testing.T) {
	s := &Server{token: "0123456789abcdef"}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	handler := s.auth(next)
	do := func(method, target string, headers map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, target, nil)
		for key, value := range headers {
			request.Header.Set(key, value)
		}
		for _, cookie := range cookies {
			request.AddCookie(cookie)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}

	if got := do("GET", "/", nil).Code; got != http.StatusUnauthorized {
		t.Fatalf("no token = %d", got)
	}
	if got := do("GET", "/healthz", nil).Code; got != http.StatusOK {
		t.Fatalf("healthz = %d", got)
	}
	if got := do("GET", "/", nil, &http.Cookie{Name: tokenCookie, Value: "wrong"}).Code; got != http.StatusUnauthorized {
		t.Fatalf("wrong cookie = %d", got)
	}
	if got := do("GET", "/", map[string]string{"Authorization": "Bearer 0123456789abcdef"}).Code; got != http.StatusOK {
		t.Fatalf("bearer = %d", got)
	}
	if got := do("GET", "/", nil, &http.Cookie{Name: tokenCookie, Value: "0123456789abcdef"}).Code; got != http.StatusOK {
		t.Fatalf("cookie = %d", got)
	}

	recorder := do("GET", "/?x=1&token=0123456789abcdef", nil)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("token link = %d", recorder.Code)
	}
	if location := recorder.Header().Get("Location"); location != "/?x=1" {
		t.Fatalf("location = %q", location)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != tokenCookie || cookies[0].Value != s.token {
		t.Fatalf("cookies = %v", cookies)
	}
	if !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie flags = %+v", cookies[0])
	}

	post := do("POST", "/api/vault/unlock?token=0123456789abcdef", nil)
	if post.Code != http.StatusOK || len(post.Result().Cookies()) != 1 {
		t.Fatalf("post with token = %d cookies=%v", post.Code, post.Result().Cookies())
	}
}

func TestLoadToken(t *testing.T) {
	t.Setenv("WEBI_TOKEN", "")
	dir := t.TempDir()
	token, path, err := loadToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 64 || path != filepath.Join(dir, "token") {
		t.Fatalf("token=%q path=%q", token, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	again, _, err := loadToken(dir)
	if err != nil || again != token {
		t.Fatalf("second load = %q, %v", again, err)
	}

	t.Setenv("WEBI_TOKEN", "short")
	if _, _, err := loadToken(t.TempDir()); err == nil {
		t.Fatal("a short WEBI_TOKEN must be rejected")
	}
	t.Setenv("WEBI_TOKEN", "a-long-enough-token")
	token, path, err = loadToken(t.TempDir())
	if err != nil || token != "a-long-enough-token" || path != "" {
		t.Fatalf("env token = %q path=%q err=%v", token, path, err)
	}
}

func TestSecurityHeaders(t *testing.T) {
	recorder := httptest.NewRecorder()
	securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP(recorder, httptest.NewRequest("GET", "/", nil))
	headers := recorder.Header()
	if got := headers.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("nosniff = %q", got)
	}
	if got := headers.Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("frame = %q", got)
	}
	if got := headers.Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("referrer = %q", got)
	}
	csp := headers.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("csp = %q", csp)
	}
}
