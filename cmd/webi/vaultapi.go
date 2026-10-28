package main

import (
	"errors"
	"net/http"
	"os"
	"strings"
)

// hasKey answers whether a run can reach a model. The environment wins; the
// vault counts too, because that is where the UI moves the key.
func (s *Server) hasKey() bool {
	if strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY")) != "" || strings.TrimSpace(os.Getenv("OPENAI_API_KEY")) != "" {
		return true
	}
	for _, scope := range []string{vaultScopeGlobal} {
		if s.vault.Has(scope, "DEEPSEEK_API_KEY") || s.vault.Has(scope, "OPENAI_API_KEY") {
			return true
		}
	}
	return false
}

// handleVaultState lists scopes and names, never values.
func (s *Server) handleVaultState(w http.ResponseWriter, r *http.Request) {
	names, lastUsed, err := s.vault.Names()
	if err != nil {
		if err == errVaultLocked {
			writeJSON(w, 200, map[string]any{
				"exists": s.vault.Exists(), "locked": true,
				"vars": map[string][]string{}, "last_used": map[string]int64{},
			})
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"exists":    s.vault.Exists(),
		"locked":    false,
		"vars":      names,
		"last_used": lastUsed,
	})
}

// notePINError counts only a real wrong PIN: a locked or missing vault is not
// a guess and must not lock the user out.
func (s *Server) notePINError(ip string, err error) {
	if errors.Is(err, errWrongPIN) {
		s.pins.fail(ip)
	}
}

// handleVaultAction serves the panel's verbs. The UI checks for the exact
// string "wrong PIN", so it must not be wrapped.
func (s *Server) handleVaultAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID      string `json:"id"`
		PIN     string `json:"pin"`
		Confirm string `json:"confirm"`
		Scope   string `json:"scope"`
		Name    string `json:"name"`
		Old     string `json:"old"`
		Value   string `json:"value"`
		Text    string `json:"text"`
		Skip    bool   `json:"skip"`
		MoveKey bool   `json:"move_key"`
	}
	if r.ContentLength != 0 {
		if !readJSON(w, r, &req) {
			return
		}
	}
	action := r.PathValue("action")
	guarded := action == "unlock" || action == "check" || action == "reveal"
	ip := clientIP(r)
	if guarded {
		if wait, ok := s.pins.allow(ip); !ok {
			w.Header().Set("Retry-After", retryAfter(wait))
			writeErr(w, 429, pinWaitError(wait))
			return
		}
	}
	switch action {
	case "init":
		if err := s.vault.Create(req.PIN, req.Confirm); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		// move_key is accepted for the UI's sake; webi never stored the key in
		// a file, so there is nothing to move.
		s.shareVaultKey(false)
		writeJSON(w, 200, map[string]bool{"ok": true})
	case "unlock":
		if err := s.vault.Unlock(req.PIN); err != nil {
			s.notePINError(ip, err)
			writeErr(w, 400, err.Error())
			return
		}
		s.pins.ok(ip)
		s.shareVaultKey(false)
		writeJSON(w, 200, map[string]bool{"ok": true})
	case "lock":
		s.vault.Lock()
		s.shareVaultKey(true)
		writeJSON(w, 200, map[string]bool{"ok": true})
	case "check":
		if err := s.vault.Check(req.PIN); err != nil {
			s.notePINError(ip, err)
			writeErr(w, 400, err.Error())
			return
		}
		s.pins.ok(ip)
		writeJSON(w, 200, map[string]bool{"ok": true})
	case "reveal":
		value, err := s.vault.Reveal(req.Scope, req.Name, req.PIN)
		if err != nil {
			s.notePINError(ip, err)
			writeErr(w, 400, err.Error())
			return
		}
		s.pins.ok(ip)
		writeJSON(w, 200, map[string]string{"value": value})
	case "item":
		if req.Skip {
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
		if err := s.vault.Put(req.Scope, req.Name, req.Value, req.Old); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	case "delete":
		if err := s.vault.Delete(req.Scope, req.Name); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	case "import":
		count, err := s.vault.Import(req.Scope, req.Text)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]int{"imported": count})
	case "secret":
		// A tool asked for this value mid-run: store it and hand it over so the
		// call can retry without restarting the thread.
		request, ok := s.takePendingSecret(req.ID)
		if !ok {
			writeErr(w, 404, "no pending secret request for that id")
			return
		}
		if req.Skip {
			if err := s.skipSecret(request); err != nil {
				writeErr(w, 400, err.Error())
				return
			}
			writeJSON(w, 200, map[string]bool{"ok": true, "skipped": true})
			return
		}
		if strings.TrimSpace(req.Value) == "" {
			writeErr(w, 400, "the value is empty")
			return
		}
		if err := s.answerSecret(request, req.Value); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	default:
		writeErr(w, 400, "unknown vault action")
	}
}
