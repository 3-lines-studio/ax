package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const tokenCookie = "webi_token"

func loadToken(stateDir string) (token, path string, err error) {
	if env := strings.TrimSpace(os.Getenv("WEBI_TOKEN")); env != "" {
		if len(env) < 16 {
			return "", "", fmt.Errorf("WEBI_TOKEN is shorter than 16 characters")
		}
		return env, "", nil
	}
	path = filepath.Join(stateDir, "token")
	if data, err := os.ReadFile(path); err == nil {
		if token := strings.TrimSpace(string(data)); len(token) >= 32 {
			return token, path, nil
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	token = hex.EncodeToString(raw)
	if err := writeFileAtomic(path, []byte(token+"\n"), 0o600); err != nil {
		return "", "", err
	}
	return token, path, nil
}

func sameToken(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func publicPath(p string) bool {
	switch p {
	case "/healthz", "/readyz", "/manifest.webmanifest", "/sw.js":
		return true
	}
	return p == "/static" || strings.HasPrefix(p, "/static/")
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if publicPath(path.Clean(r.URL.Path)) {
			next.ServeHTTP(w, r)
			return
		}
		if given := r.URL.Query().Get("token"); given != "" && sameToken(given, s.token) {
			http.SetCookie(w, &http.Cookie{
				Name:     tokenCookie,
				Value:    s.token,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
				MaxAge:   30 * 24 * 60 * 60,
			})
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				target := *r.URL
				query := target.Query()
				query.Del("token")
				target.RawQuery = query.Encode()
				http.Redirect(w, r, target.String(), http.StatusSeeOther)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		given := ""
		if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
			given = strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		}
		if cookie, err := r.Cookie(tokenCookie); err == nil {
			given = cookie.Value
		}
		if given != "" && sameToken(given, s.token) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="webi"`)
		http.Error(w, "unauthorized: open the UI once with ?token=<token>", http.StatusUnauthorized)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("X-Frame-Options", "DENY")
		header.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https:; font-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}
