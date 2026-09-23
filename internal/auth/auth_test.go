package auth

import (
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

func configured(t *testing.T) (*Authenticator, string) {
	t.Helper()
	a := New(Config{})
	p := a.Challenge()
	key := DeriveKey("1234", p.Salt, p.Iterations)
	if err := a.SetPIN(p.Nonce, base64.StdEncoding.EncodeToString(key)); err != nil {
		t.Fatal(err)
	}
	return a, "1234"
}

func login(t *testing.T, a *Authenticator, pin string) (string, error) {
	t.Helper()
	p := a.Challenge()
	key := DeriveKey(pin, p.Salt, p.Iterations)
	return a.Verify(p.Nonce, Proof(key, p.Nonce))
}

func TestAPINIsNotSetUntilItIs(t *testing.T) {
	a := New(Config{})
	if a.Configured() {
		t.Fatal("reports a PIN before one was set")
	}
	if _, err := a.Verify("nonce", "proof"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("logging in with no PIN gave %v, want ErrNotConfigured", err)
	}
}

func TestSetPINThenLogIn(t *testing.T) {
	a, pin := configured(t)
	if !a.Configured() {
		t.Fatal("a PIN was set but is not reported")
	}
	token, err := login(t, a, pin)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if !a.Valid(token) {
		t.Fatal("the token issued at login is not accepted")
	}
}

func TestSetPINOnlyOnce(t *testing.T) {
	a, _ := configured(t)
	p := a.Challenge()
	key := DeriveKey("9999", p.Salt, p.Iterations)
	if err := a.SetPIN(p.Nonce, base64.StdEncoding.EncodeToString(key)); !errors.Is(err, ErrConfigured) {
		t.Fatalf("a second PIN was accepted: %v", err)
	}
}

func TestWrongPINIsRefused(t *testing.T) {
	a, _ := configured(t)
	if _, err := login(t, a, "0000"); !errors.Is(err, ErrBadProof) {
		t.Fatalf("the wrong PIN gave %v, want ErrBadProof", err)
	}
}

func TestAProofCannotBeReplayed(t *testing.T) {
	// Over plain HTTP a proof can be read off the wire. Single-use nonces are
	// what stop it being worth anything.
	a, pin := configured(t)
	p := a.Challenge()
	proof := Proof(DeriveKey(pin, p.Salt, p.Iterations), p.Nonce)

	if _, err := a.Verify(p.Nonce, proof); err != nil {
		t.Fatalf("first use: %v", err)
	}
	if _, err := a.Verify(p.Nonce, proof); !errors.Is(err, ErrBadNonce) {
		t.Fatalf("the same proof was accepted twice: %v", err)
	}
}

func TestAChallengeExpires(t *testing.T) {
	a, pin := configured(t)
	now := time.Now()
	a.now = func() time.Time { return now }

	p := a.Challenge()
	proof := Proof(DeriveKey(pin, p.Salt, p.Iterations), p.Nonce)

	now = now.Add(NonceTTL + time.Second)
	if _, err := a.Verify(p.Nonce, proof); !errors.Is(err, ErrBadNonce) {
		t.Fatalf("an expired challenge was accepted: %v", err)
	}
}

func TestRepeatedWrongPINsSlowDown(t *testing.T) {
	// Four digits is ten thousand possibilities: nothing to a machine, a lot
	// to a person. Backing off rather than locking out means a mistyped PIN
	// does not shut someone out of their own house for an hour.
	a, pin := configured(t)
	now := time.Now()
	a.now = func() time.Time { return now }

	for i := 0; i < 5; i++ {
		if _, err := login(t, a, "0000"); !errors.Is(err, ErrBadProof) {
			t.Fatalf("attempt %d gave %v", i, err)
		}
	}
	if _, err := login(t, a, pin); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("after five wrong tries the right PIN gave %v, want ErrRateLimited", err)
	}

	now = now.Add(time.Minute)
	if _, err := login(t, a, pin); err != nil {
		t.Fatalf("after waiting, the right PIN gave %v", err)
	}
}

func TestASuccessfulLoginClearsTheBackoff(t *testing.T) {
	a, pin := configured(t)
	for i := 0; i < 3; i++ {
		_, _ = login(t, a, "0000")
	}
	if _, err := login(t, a, pin); err != nil {
		t.Fatalf("login after a few mistakes: %v", err)
	}
	for i := 0; i < 4; i++ {
		if _, err := login(t, a, "0000"); !errors.Is(err, ErrBadProof) {
			t.Fatalf("the counter did not reset: %v", err)
		}
	}
}

func TestSessionsExpireAndCannotBeForged(t *testing.T) {
	a, pin := configured(t)
	now := time.Now()
	a.now = func() time.Time { return now }

	token, err := login(t, a, pin)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Valid(token) {
		t.Fatal("a fresh token was rejected")
	}

	for _, bad := range []string{
		"", "nonsense", "9999999999", "9999999999.", "9999999999.AAAA",
		token + "x", "1." + token,
	} {
		if a.Valid(bad) {
			t.Errorf("accepted a forged token %q", bad)
		}
	}

	now = now.Add(time.Duration(a.SessionDays())*24*time.Hour + time.Minute)
	if a.Valid(token) {
		t.Fatal("an expired token is still accepted")
	}
}

func TestSessionsSurviveARestartButNotANewSecret(t *testing.T) {
	// Being logged out by a software update is a poor reward for keeping the
	// thing up to date, so the signing secret is stored. Changing it
	// deliberately must still invalidate everything.
	a, pin := configured(t)
	token, err := login(t, a, pin)
	if err != nil {
		t.Fatal(err)
	}

	restarted := New(a.cfg)
	if !restarted.Valid(token) {
		t.Fatal("a restart invalidated an existing session")
	}

	rotated := a.cfg
	rotated.SessionSecret = randomHex(32)
	if New(rotated).Valid(token) {
		t.Fatal("a session survived the signing secret being replaced")
	}
}

func TestSetPINPersistsThroughSave(t *testing.T) {
	var saved Config
	a := New(Config{})
	a.Save = func(c Config) error { saved = c; return nil }

	p := a.Challenge()
	key := DeriveKey("4321", p.Salt, p.Iterations)
	if err := a.SetPIN(p.Nonce, base64.StdEncoding.EncodeToString(key)); err != nil {
		t.Fatal(err)
	}
	if saved.Verifier == "" || saved.Salt == "" || saved.SessionSecret == "" {
		t.Fatalf("an incomplete configuration was saved: %+v", saved)
	}
	if saved.Iterations != DefaultIterations {
		t.Errorf("iterations = %d, want %d", saved.Iterations, DefaultIterations)
	}
	// And the saved copy alone is enough to log in after a restart.
	if _, err := login(t, New(saved), "4321"); err != nil {
		t.Fatalf("login against the saved configuration: %v", err)
	}
}

func TestChallengeKeepsTheSaltStableBeforeAPINExists(t *testing.T) {
	// Two challenges before setup must derive against the same salt, or the
	// key proved at setup is not the key checked at login.
	a := New(Config{})
	first := a.Challenge()
	second := a.Challenge()
	if first.Salt != second.Salt {
		t.Fatalf("salt changed between challenges: %s then %s", first.Salt, second.Salt)
	}
	if first.Nonce == second.Nonce {
		t.Fatal("the same nonce was issued twice")
	}
}

// TestLockoutIsCappedAndDecays: the delay is cumulative so that guessing is
// hopeless, but without a ceiling it was also a way for anything on the network
// to keep the owner out of their own settings for good.
func TestLockoutIsCappedAndDecays(t *testing.T) {
	a, pin := configured(t)
	now := time.Now()
	a.now = func() time.Time { return now }

	// Three hundred wrong proofs, which is minutes of work for a script. Each
	// waits out the previous lock, so they all land.
	for i := 0; i < 300; i++ {
		now = now.Add(MaxLockout + time.Second)
		_, _ = login(t, a, "0000")
	}

	// However many it took, the door opens again within the cap rather than
	// hours later. Before this was capped the wait here was about a quarter of
	// an hour and growing by five seconds a try.
	now = now.Add(MaxLockout + time.Second)
	if _, err := login(t, a, pin); err != nil {
		t.Fatalf("after the cap the right PIN gave %v", err)
	}
}

// TestAQuietSpellForgivesTheCount: somebody who got it wrong twice last week
// starts from scratch today. An attacker gains nothing by pausing — the wait
// only shrinks as fast as they stop trying.
func TestAQuietSpellForgivesTheCount(t *testing.T) {
	a, pin := configured(t)
	now := time.Now()
	a.now = func() time.Time { return now }

	for i := 0; i < 4; i++ {
		_, _ = login(t, a, "0000")
	}
	now = now.Add(AttemptDecay + time.Minute)

	// The fifth wrong proof would have locked the door; after the quiet spell
	// it is the first again.
	if _, err := login(t, a, "0000"); !errors.Is(err, ErrBadProof) {
		t.Fatalf("first attempt after a quiet spell gave %v", err)
	}
	if _, err := login(t, a, pin); err != nil {
		t.Fatalf("the right PIN after a quiet spell gave %v", err)
	}
}

// TestNonceMapIsBounded: Challenge needs no credentials, so nothing stops a
// caller asking for them faster than the two-minute expiry clears them.
func TestNonceMapIsBounded(t *testing.T) {
	a := New(Config{})
	for i := 0; i < MaxNonces*3; i++ {
		a.Challenge()
	}
	a.mu.Lock()
	n := len(a.nonces)
	a.mu.Unlock()
	if n > MaxNonces {
		t.Errorf("%d outstanding nonces, want at most %d", n, MaxNonces)
	}
}

func changePIN(t *testing.T, a *Authenticator, old, fresh string) (string, error) {
	t.Helper()
	p := a.Challenge()
	oldKey := DeriveKey(old, p.Salt, p.Iterations)
	newSalt := randomHex(16)
	newKey := DeriveKey(fresh, newSalt, DefaultIterations)
	return a.ChangePIN(p.Nonce, Proof(oldKey, p.Nonce),
		newSalt, base64.StdEncoding.EncodeToString(newKey))
}

// TestChangingThePINNeedsTheOldOne: otherwise anybody who got as far as a
// session — or a page left open on a phone in a kitchen — could lock the owner
// out of their own unit.
func TestChangingThePINNeedsTheOldOne(t *testing.T) {
	a, pin := configured(t)
	if _, err := changePIN(t, a, "9999", "5555"); !errors.Is(err, ErrBadProof) {
		t.Fatalf("the wrong old PIN was accepted: %v", err)
	}
	// And the old PIN still works, so a failed attempt changed nothing.
	if _, err := login(t, a, pin); err != nil {
		t.Fatalf("a failed change broke the existing PIN: %v", err)
	}
}

func TestChangingThePINSwapsWhichOneWorks(t *testing.T) {
	a, pin := configured(t)
	if _, err := changePIN(t, a, pin, "5555"); err != nil {
		t.Fatalf("change: %v", err)
	}
	if _, err := login(t, a, "5555"); err != nil {
		t.Fatalf("the new PIN does not work: %v", err)
	}
	if _, err := login(t, a, pin); !errors.Is(err, ErrBadProof) {
		t.Fatalf("the old PIN still works: %v", err)
	}
}

// TestChangingThePINEndsOtherSessions: the reason to change a PIN is usually
// that somebody else knows the old one. Leaving their browser logged in would
// make the change ceremonial.
func TestChangingThePINEndsOtherSessions(t *testing.T) {
	a, pin := configured(t)
	elsewhere, err := login(t, a, pin)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Valid(elsewhere) {
		t.Fatal("a fresh token was rejected")
	}

	mine, err := changePIN(t, a, pin, "5555")
	if err != nil {
		t.Fatalf("change: %v", err)
	}
	if a.Valid(elsewhere) {
		t.Error("a session opened with the old PIN survived the change")
	}
	// But not the browser doing it: being thrown out of the page you are
	// standing on is a poor reward for good housekeeping.
	if !a.Valid(mine) {
		t.Error("the token handed back by the change was not accepted")
	}
}

// TestANewSaltIsStoredWithTheNewPIN: the salt goes out with the challenge, so
// a change that kept the old one would derive the new key against it — and
// then a restart, reading salt and verifier back, would still work. It would
// only be wrong in the way that matters: two PINs sharing a salt.
func TestANewSaltIsStoredWithTheNewPIN(t *testing.T) {
	var saved Config
	a, pin := configured(t)
	a.Save = func(c Config) error { saved = c; return nil }
	before := a.Challenge().Salt

	if _, err := changePIN(t, a, pin, "5555"); err != nil {
		t.Fatal(err)
	}
	if saved.Salt == before {
		t.Error("the new PIN was stored against the old salt")
	}
	// And the saved copy alone is enough after a restart.
	if _, err := login(t, New(saved), "5555"); err != nil {
		t.Fatalf("login against the saved configuration: %v", err)
	}
}

func TestChangingThePINIsRateLimitedToo(t *testing.T) {
	// Or it would be a way around the brake on the login path.
	a, pin := configured(t)
	now := time.Now()
	a.now = func() time.Time { return now }
	for i := 0; i < 5; i++ {
		_, _ = changePIN(t, a, "0000", "5555")
	}
	if _, err := changePIN(t, a, pin, "5555"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("after five wrong tries the change gave %v, want ErrRateLimited", err)
	}
}
