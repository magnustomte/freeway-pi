package web

import (
	"encoding/base64"
	"encoding/json"
	"freewaypi/internal/config"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"freewaypi/internal/auth"
)

func do(t *testing.T, s *Server, method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

// setUpPIN walks the same path the browser does: fetch a challenge, derive the
// key, send it.
func setUpPIN(t *testing.T, s *Server, pin string) {
	t.Helper()
	var p auth.Params
	res := do(t, s, "GET", "/api/auth/challenge", "")
	if err := json.NewDecoder(res.Body).Decode(&p); err != nil {
		t.Fatal(err)
	}
	key := auth.DeriveKey(pin, p.Salt, p.Iterations)
	body, _ := json.Marshal(map[string]string{"nonce": p.Nonce, "key": base64.StdEncoding.EncodeToString(key)})
	if res := do(t, s, "POST", "/api/auth/setup", string(body)); res.Code != http.StatusOK {
		t.Fatalf("setup returned %d: %s", res.Code, res.Body)
	}
}

func logIn(t *testing.T, s *Server, pin string) (*http.Cookie, int) {
	t.Helper()
	var p auth.Params
	res := do(t, s, "GET", "/api/auth/challenge", "")
	if err := json.NewDecoder(res.Body).Decode(&p); err != nil {
		t.Fatal(err)
	}
	proof := auth.Proof(auth.DeriveKey(pin, p.Salt, p.Iterations), p.Nonce)
	body, _ := json.Marshal(map[string]string{"nonce": p.Nonce, "proof": proof})
	res = do(t, s, "POST", "/api/auth/login", string(body))
	for _, c := range res.Result().Cookies() {
		if c.Name == sessionCookie {
			return c, res.Code
		}
	}
	return nil, res.Code
}

func TestDailyUseNeedsNoPIN(t *testing.T) {
	// The whole point of the split: the household can turn up the heat without
	// knowing a code.
	s, _ := newAuthServer(t)
	for _, path := range []string{"/api/state", "/api/auth/status"} {
		if res := do(t, s, "GET", path, ""); res.Code != http.StatusOK {
			t.Errorf("%s returned %d without a session", path, res.Code)
		}
	}
	if res := do(t, s, "POST", "/api/control/setpoint", `{"celsius":21}`); res.Code == http.StatusUnauthorized {
		t.Error("setting the temperature asked for a PIN")
	}
}

func TestSettingAPINThenLoggingIn(t *testing.T) {
	s, a := newAuthServer(t)
	if a.Configured() {
		t.Fatal("configured before setup")
	}
	setUpPIN(t, s, "2468")
	if !a.Configured() {
		t.Fatal("not configured after setup")
	}

	cookie, code := logIn(t, s, "2468")
	if code != http.StatusOK || cookie == nil {
		t.Fatalf("login returned %d with cookie %v", code, cookie)
	}
	if !cookie.HttpOnly {
		t.Error("the session cookie is readable from JavaScript")
	}
	if cookie.SameSite != http.SameSiteStrictMode {
		t.Error("the session cookie is not SameSite=Strict")
	}
}

func TestWrongPINIsRefusedOverHTTP(t *testing.T) {
	s, _ := newAuthServer(t)
	setUpPIN(t, s, "2468")
	if _, code := logIn(t, s, "1111"); code != http.StatusUnauthorized {
		t.Fatalf("the wrong PIN returned %d, want 401", code)
	}
}

func TestSetupCannotBeRepeated(t *testing.T) {
	// Otherwise anyone on the network could replace the PIN at will.
	s, _ := newAuthServer(t)
	setUpPIN(t, s, "2468")

	var p auth.Params
	res := do(t, s, "GET", "/api/auth/challenge", "")
	_ = json.NewDecoder(res.Body).Decode(&p)
	key := auth.DeriveKey("9999", p.Salt, p.Iterations)
	body, _ := json.Marshal(map[string]string{"nonce": p.Nonce, "key": base64.StdEncoding.EncodeToString(key)})

	if res := do(t, s, "POST", "/api/auth/setup", string(body)); res.Code != http.StatusConflict {
		t.Fatalf("a second setup returned %d, want 409", res.Code)
	}
}

func TestTheChallengeIsNeverCached(t *testing.T) {
	// A cached nonce is a nonce that does not work, and the failure looks
	// exactly like a wrong PIN.
	s, _ := newAuthServer(t)
	res := do(t, s, "GET", "/api/auth/challenge", "")
	if got := res.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestLogoutClearsTheSession(t *testing.T) {
	s, _ := newAuthServer(t)
	setUpPIN(t, s, "2468")
	cookie, _ := logIn(t, s, "2468")

	res := do(t, s, "POST", "/api/auth/logout", "", cookie)
	var cleared bool
	for _, c := range res.Result().Cookies() {
		if c.Name == sessionCookie && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("logging out did not clear the cookie")
	}
}

func TestSettingsAreBehindThePIN(t *testing.T) {
	s, _ := newAuthServer(t)

	// Before any PIN exists the answer says so rather than letting anyone in.
	if res := do(t, s, "GET", "/api/settings", ""); res.Code != http.StatusUnauthorized {
		t.Fatalf("settings with no PIN returned %d, want 401", res.Code)
	}

	setUpPIN(t, s, "2468")
	if res := do(t, s, "GET", "/api/settings", ""); res.Code != http.StatusUnauthorized {
		t.Fatalf("settings without a session returned %d, want 401", res.Code)
	}
	if res := do(t, s, "PUT", "/api/settings", `{"key":"setpoint_min","number":18}`); res.Code != http.StatusUnauthorized {
		t.Fatalf("writing a setting without a session returned %d, want 401", res.Code)
	}

	cookie, _ := logIn(t, s, "2468")
	if res := do(t, s, "GET", "/api/settings", "", cookie); res.Code != http.StatusOK {
		t.Fatalf("settings with a session returned %d: %s", res.Code, res.Body)
	}
}

func TestAForgedSessionDoesNotOpenTheSettings(t *testing.T) {
	s, _ := newAuthServer(t)
	setUpPIN(t, s, "2468")
	forged := &http.Cookie{Name: sessionCookie, Value: "99999999999.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}
	if res := do(t, s, "GET", "/api/settings", "", forged); res.Code != http.StatusUnauthorized {
		t.Fatalf("a forged cookie returned %d, want 401", res.Code)
	}
}

func TestStatusReportsWhatThePageNeedsToKnow(t *testing.T) {
	s, _ := newAuthServer(t)

	var st map[string]bool
	res := do(t, s, "GET", "/api/auth/status", "")
	_ = json.NewDecoder(res.Body).Decode(&st)
	if st["configured"] || st["authenticated"] {
		t.Fatalf("status before setup = %+v", st)
	}

	setUpPIN(t, s, "2468")
	cookie, _ := logIn(t, s, "2468")

	res = do(t, s, "GET", "/api/auth/status", "", cookie)
	_ = json.NewDecoder(res.Body).Decode(&st)
	if !st["configured"] || !st["authenticated"] {
		t.Fatalf("status after login = %+v", st)
	}
}

// TestBodylessWritesAreAlsoSameSite: the same-site check lives in decodeBody,
// which a POST with no body never reaches — so scanning, confirming a network
// change, resetting the service counter and the rest went without it.
//
// The session cookie is SameSite=Strict, so a cross-site request arrives with
// no cookie and requireAuth turns it away. That is one layer, and
// api/network/confirm is the last place to rely on one: its whole job is to be
// the thing that holds when a network change has gone wrong.
func TestBodylessWritesAreAlsoSameSite(t *testing.T) {
	s, _, _ := newServerWithClock(t, 0)
	setUpPIN(t, s, "2468")
	cookie, _ := logIn(t, s, "2468")

	for _, path := range []string{
		"/api/network/confirm",
		"/api/wifi/scan",
		"/api/wifi/radio",
		"/api/settings/service-reset",
		"/api/notify/test",
	} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.AddCookie(cookie)
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s from another site gave %d, want %d", path, w.Code, http.StatusForbidden)
		}
	}
}

// TestSavingNotificationSettingsTakesEffectNow: saving a webhook used to write
// it to the configuration and change nothing until somebody restarted the
// service — so "send a test" tested a notifier with no channels, and a webhook
// that was perfectly fine looked broken.
func TestSavingNotificationSettingsTakesEffectNow(t *testing.T) {
	// A real file, because the handler reads the configuration back rather than
	// trusting what it was just handed.
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := config.Default()
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	a := auth.New(auth.Config{})
	s := New(&stubController{}, nil, Options{
		Auth:       a,
		Logger:     discardLogger(),
		ConfigPath: path,
		SaveConfig: func(apply func(*config.Config)) error {
			current, err := config.Load(path)
			if err != nil {
				return err
			}
			apply(&current)
			return current.Save(path)
		},
	})
	setUpPIN(t, s, "2468")
	cookie, _ := logIn(t, s, "2468")

	body := `{"min_severity":"info","webhooks":[{"enabled":true,"name":"x","url":"http://127.0.0.1:1/hook"}],` +
		`"smtp":{"enabled":false,"host":"","port":587,"username":"","password":"","from":"","to":[],"starttls":true}}`
	req := httptest.NewRequest(http.MethodPut, "/api/notify", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("saving gave %d: %s", w.Code, w.Body.String())
	}

	// The channel is in place now, without a restart. Sending to it fails —
	// nothing listens on port 1 — and that failure is the proof it was tried.
	req = httptest.NewRequest(http.MethodPost, "/api/notify/test", nil)
	req.AddCookie(cookie)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Fatal("a test to a dead address reported success")
	}
	if got := w.Body.String(); strings.Contains(got, "ingen varslingskanal") ||
		strings.Contains(got, "no notification channel") {
		t.Errorf("the saved channel was not in use: %s", got)
	}
}
