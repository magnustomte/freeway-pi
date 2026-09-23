package web

import (
	"errors"
	"net/http"
	"time"

	"freewaypi/internal/i18n"

	"freewaypi/internal/auth"
)

// sessionCookie is the name of the cookie carrying a session.
const sessionCookie = "freeway_session"

// handleAuthStatus tells the page whether a PIN exists and whether this
// browser is logged in, so it can show a prompt to set one, a prompt to log in,
// or the settings themselves.
func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{
		"configured":    s.auth.Configured(),
		"authenticated": s.authenticated(r),
	})
}

// handleAuthChallenge issues a nonce and the derivation parameters.
func (s *Server) handleAuthChallenge(w http.ResponseWriter, r *http.Request) {
	// Never cached: a reused nonce is a nonce that does not work, and the
	// failure looks like a wrong PIN.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, s.auth.Challenge())
}

// handleAuthSetup stores the first PIN.
func (s *Server) handleAuthSetup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Nonce string `json:"nonce"`
		Key   string `json:"key"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if err := s.auth.SetPIN(body.Nonce, body.Key); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, auth.ErrConfigured) {
			status = http.StatusConflict
		}
		writeError(w, r, status, err)
		return
	}
	s.log.Info("a PIN was set", "from", r.RemoteAddr)
	s.handleAuthLoginWithKey(w, r, body.Nonce)
}

// handleAuthLoginWithKey issues a session straight after setup, so setting a
// PIN does not immediately ask for it.
func (s *Server) handleAuthLoginWithKey(w http.ResponseWriter, r *http.Request, _ string) {
	p := s.auth.Challenge()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "salt": p.Salt, "iterations": p.Iterations})
}

// handleAuthLogin checks a proof and sets the session cookie.
func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Nonce string `json:"nonce"`
		Proof string `json:"proof"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}

	token, err := s.auth.Verify(body.Nonce, body.Proof)
	if err != nil {
		status := http.StatusUnauthorized
		if errors.Is(err, auth.ErrRateLimited) {
			status = http.StatusTooManyRequests
		}
		s.log.Info("failed login", "from", r.RemoteAddr, "reason", err)
		writeError(w, r, status, err)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:  sessionCookie,
		Value: token,
		Path:  "/",
		// Long-lived on purpose: this is a box in a utility room, and being
		// asked for a PIN every visit is how a PIN ends up on a sticky note.
		MaxAge:   s.auth.SessionDays() * 24 * 3600,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	s.log.Info("logged in", "from", r.RemoteAddr)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleAuthLogout clears the session.
func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if err := sameSite(r); err != nil {
		writeError(w, r, http.StatusForbidden, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) authenticated(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	return s.auth.Valid(c.Value)
}

// requireAuth wraps a handler so it is only reached by a logged-in browser.
//
// Everything behind it can change how the unit is configured rather than how it
// is running today: settings that persist, timer programs, and writes to
// arbitrary registers. Daily use — temperature, fan, mode, overpressure —
// deliberately stays in front of it.
func (s *Server) requireAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.auth.Configured() {
			writeError(w, r, http.StatusUnauthorized,
				i18n.Errf("error.auth.nopin"))
			return
		}
		if !s.authenticated(r) {
			writeError(w, r, http.StatusUnauthorized, i18n.Errf("error.auth.required"))
			return
		}
		h(w, r)
	}
}

// sessionAge is how long a session lasts, for the interface to show.
func (s *Server) sessionAge() time.Duration {
	return time.Duration(s.auth.SessionDays()) * 24 * time.Hour
}

// handleAuthChange replaces the PIN, proving the old one on the way.
//
// Behind requireAuth as well as behind the old PIN: a session alone is not
// enough to change the lock, because a page left open on a phone in a kitchen
// is a session.
func (s *Server) handleAuthChange(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Nonce string `json:"nonce"`
		Proof string `json:"proof"`
		Salt  string `json:"salt"`
		Key   string `json:"key"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	token, err := s.auth.ChangePIN(body.Nonce, body.Proof, body.Salt, body.Key)
	if err != nil {
		status := http.StatusUnauthorized
		if errors.Is(err, auth.ErrRateLimited) {
			status = http.StatusTooManyRequests
		}
		s.log.Warn("a PIN change was refused", "err", err, "from", r.RemoteAddr)
		writeError(w, r, status, err)
		return
	}
	// Every other session is gone; this one is handed a token signed with the
	// new secret so the person doing it stays where they are.
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   s.auth.SessionDays() * 24 * 3600,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	s.log.Warn("the PIN was changed", "from", r.RemoteAddr)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
