// Package auth guards the parts of the interface that are more than daily use.
//
// The PIN never crosses the network. The page is served over plain HTTP by
// design — see the TLS discussion in the plan — so a password in a form field
// would travel in the clear, and would also end up in server logs, in any proxy
// along the way and in browser history. Instead the server issues a nonce, the
// browser proves it knows the PIN by returning an HMAC over that nonce, and the
// PIN itself stays on the device it was typed into.
//
// The stored verifier is PBKDF2-SHA256 of the PIN. That is a speed bump rather
// than a wall for a four digit PIN, which is brute-forceable whatever the
// derivation; it is there so that a leaked configuration file or backup does
// not hand over the PIN itself, which people reuse.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/pbkdf2"
)

// DefaultIterations is the PBKDF2 work factor.
//
// Chosen against what a phone can do in hand-written JavaScript rather than
// against a server benchmark: crypto.subtle is unavailable over plain HTTP, so
// the browser derives this in a loop we ship, and a login nobody will wait for
// is a login that gets disabled.
const DefaultIterations = 120000

// NonceTTL is how long a challenge stays valid. Long enough for a slow phone
// to derive the key, short enough that a captured proof is worthless by the
// time anyone could use it.
const NonceTTL = 2 * time.Minute

// MaxLockout bounds how long a run of wrong proofs can shut the door.
//
// The delay is cumulative on purpose — ten thousand guesses take days — but
// without a ceiling it was also a way for anything on the network to keep the
// owner out of their own settings for good.
const MaxLockout = time.Minute

// AttemptDecay is how long the count survives without a new attempt.
const AttemptDecay = 15 * time.Minute

// MaxNonces bounds the outstanding challenges. The endpoint that issues them
// needs no credentials, so the map has to have a floor under it.
const MaxNonces = 256

// Errors returned by Authenticator.
var (
	ErrNotConfigured = errors.New("auth: no PIN has been set")
	ErrConfigured    = errors.New("auth: a PIN is already set")
	ErrBadNonce      = errors.New("auth: the challenge is unknown or has expired")
	ErrBadProof      = errors.New("auth: wrong PIN")
	ErrRateLimited   = errors.New("auth: too many attempts, wait a moment")
)

// Config is the stored half of the credentials.
type Config struct {
	Salt       string `json:"salt"`
	Verifier   string `json:"verifier"`
	Iterations int    `json:"iterations"`
	// SessionSecret signs session cookies. Kept so that sessions survive a
	// restart: being logged out by a software update is a poor reward for
	// keeping the thing up to date.
	SessionSecret string `json:"session_secret"`
	SessionDays   int    `json:"session_days"`
}

// Authenticator issues challenges and checks proofs.
type Authenticator struct {
	mu     sync.Mutex
	cfg    Config
	nonces map[string]time.Time
	// attempts throttles wrong answers. A four digit PIN has ten thousand
	// possibilities, which is nothing to a machine and a lot to a person.
	attempts  int
	lockUntil time.Time
	// lastAttempt is when the count last moved, so a quiet spell can forgive
	// it rather than the count growing for the life of the process.
	lastAttempt time.Time
	now         func() time.Time
	// Save persists a changed configuration, when the PIN is first set.
	Save func(Config) error
}

// New returns an Authenticator over the given configuration.
func New(cfg Config) *Authenticator {
	return &Authenticator{cfg: cfg, nonces: map[string]time.Time{}, now: time.Now}
}

// Configured reports whether a PIN has been set.
func (a *Authenticator) Configured() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.Verifier != ""
}

// Params are what a browser needs to derive the same key.
type Params struct {
	Nonce      string `json:"nonce"`
	Salt       string `json:"salt"`
	Iterations int    `json:"iterations"`
	Configured bool   `json:"configured"`
}

// Challenge issues a nonce and the derivation parameters.
//
// When no PIN is set it still returns parameters, with a fresh salt, so the
// same code path can set the first one.
func (a *Authenticator) Challenge() Params {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.expireNonces()
	// Bounded. This endpoint needs no credentials, and without a cap anything
	// on the network could fill the map faster than the two-minute expiry
	// empties it. The oldest go first, which at worst costs somebody a retry.
	for len(a.nonces) >= MaxNonces {
		oldest, at := "", time.Time{}
		for n, exp := range a.nonces {
			if at.IsZero() || exp.Before(at) {
				oldest, at = n, exp
			}
		}
		delete(a.nonces, oldest)
	}
	nonce := randomHex(16)
	a.nonces[nonce] = a.now().Add(NonceTTL)

	salt := a.cfg.Salt
	iterations := a.cfg.Iterations
	if salt == "" {
		salt = randomHex(16)
		// Held so that a proof arriving for this challenge derives against the
		// same salt it was issued with.
		a.cfg.Salt = salt
	}
	if iterations <= 0 {
		iterations = DefaultIterations
		a.cfg.Iterations = iterations
	}
	return Params{Nonce: nonce, Salt: salt, Iterations: iterations, Configured: a.cfg.Verifier != ""}
}

// SetPIN stores the first PIN, proved the same way a login is.
//
// Only possible while none is set. On a box reached only from the local
// network that is the usual appliance bargain: whoever gets there first
// configures it, and the interface says loudly that nothing is set yet.
func (a *Authenticator) SetPIN(nonce, proof string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.cfg.Verifier != "" {
		return ErrConfigured
	}
	if !a.consumeNonce(nonce) {
		return ErrBadNonce
	}
	// The proof is HMAC(key, nonce) and the key is what we store. We cannot
	// recover the key from the proof, so the browser sends the key itself here,
	// once, at setup — over a connection that is as trusted as the network is.
	key, err := base64.StdEncoding.DecodeString(proof)
	if err != nil || len(key) != sha256.Size {
		return fmt.Errorf("auth: malformed key")
	}
	a.cfg.Verifier = base64.StdEncoding.EncodeToString(key)
	if a.cfg.SessionSecret == "" {
		a.cfg.SessionSecret = randomHex(32)
	}
	if a.cfg.SessionDays <= 0 {
		a.cfg.SessionDays = 90
	}
	if a.Save != nil {
		return a.Save(a.cfg)
	}
	return nil
}

// Verify checks a proof against a challenge and returns a session token.
func (a *Authenticator) Verify(nonce, proof string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.cfg.Verifier == "" {
		return "", ErrNotConfigured
	}
	if a.now().Before(a.lockUntil) {
		return "", ErrRateLimited
	}
	// A quiet spell forgives. Somebody who got it wrong twice last week starts
	// from scratch today, and an attacker gains nothing by pausing: the wait
	// only shrinks as fast as they stop trying.
	if !a.lastAttempt.IsZero() && a.now().Sub(a.lastAttempt) > AttemptDecay {
		a.attempts = 0
	}
	a.lastAttempt = a.now()
	if !a.consumeNonce(nonce) {
		return "", ErrBadNonce
	}

	key, err := base64.StdEncoding.DecodeString(a.cfg.Verifier)
	if err != nil {
		return "", fmt.Errorf("auth: stored verifier is unreadable: %w", err)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(nonce))
	want := mac.Sum(nil)

	got, err := base64.StdEncoding.DecodeString(proof)
	if err != nil || subtle.ConstantTimeCompare(got, want) != 1 {
		a.attempts++
		// Backs off rather than locking out: a person who mistyped should not
		// be shut out for an hour, and a machine should not get thousands of
		// tries a minute.
		//
		// Capped, and the count decays. Without a cap the delay grew without
		// limit and nothing but a successful login reset it — so anything on
		// the network could push the lock past an hour with a few hundred
		// wrong proofs and then keep the owner out of their own settings
		// indefinitely with one guess an hour. The cap is what makes this a
		// brake rather than a lock: ten thousand guesses still take days.
		if a.attempts >= 5 {
			wait := time.Duration(a.attempts-4) * 5 * time.Second
			if wait > MaxLockout {
				wait = MaxLockout
			}
			a.lockUntil = a.now().Add(wait)
		}
		return "", ErrBadProof
	}
	a.attempts = 0
	return a.issue(a.now().Add(time.Duration(a.cfg.SessionDays) * 24 * time.Hour)), nil
}

// Valid reports whether a session token is genuine and unexpired.
func (a *Authenticator) Valid(token string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cfg.SessionSecret == "" {
		return false
	}
	expiry, sig, ok := strings.Cut(token, ".")
	if !ok {
		return false
	}
	unix, err := strconv.ParseInt(expiry, 10, 64)
	if err != nil || a.now().After(time.Unix(unix, 0)) {
		return false
	}
	return hmac.Equal([]byte(sig), []byte(a.sign(expiry)))
}

// DeriveKey performs the same derivation the browser does, for tests and for
// the command line.
func DeriveKey(pin, salt string, iterations int) []byte {
	return pbkdf2.Key([]byte(pin), []byte(salt), iterations, sha256.Size, sha256.New)
}

// Proof computes the answer to a challenge from a derived key.
func Proof(key []byte, nonce string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(nonce))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// SessionDays is how long a session lasts.
func (a *Authenticator) SessionDays() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cfg.SessionDays <= 0 {
		return 90
	}
	return a.cfg.SessionDays
}

func (a *Authenticator) issue(expiry time.Time) string {
	e := strconv.FormatInt(expiry.Unix(), 10)
	return e + "." + a.sign(e)
}

func (a *Authenticator) sign(expiry string) string {
	mac := hmac.New(sha256.New, []byte(a.cfg.SessionSecret))
	mac.Write([]byte(expiry))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// consumeNonce checks and removes a nonce. Single use, so a proof captured off
// the wire cannot be replayed.
func (a *Authenticator) consumeNonce(nonce string) bool {
	expiry, ok := a.nonces[nonce]
	if !ok || a.now().After(expiry) {
		delete(a.nonces, nonce)
		return false
	}
	delete(a.nonces, nonce)
	return true
}

func (a *Authenticator) expireNonces() {
	now := a.now()
	for n, expiry := range a.nonces {
		if now.After(expiry) {
			delete(a.nonces, n)
		}
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("auth: no randomness available: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// ChangePIN replaces the PIN, and requires the old one to do it.
//
// The old PIN is proved exactly as a login proves it: HMAC(old key, nonce),
// against the same nonce that carried the salt. So the old PIN never crosses
// the network, and neither does the new one — the browser derives the new key
// against a fresh salt and sends the key.
//
// It rotates the signing secret, which ends every session the old PIN opened.
// That is the point: the reason to change a PIN is usually that somebody else
// knows the old one, and leaving their browser logged in would make the change
// ceremonial. The caller is handed a new token so the person doing it is not
// thrown out of the page they are standing on.
func (a *Authenticator) ChangePIN(nonce, proof, newSalt, newKey string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.cfg.Verifier == "" {
		return "", ErrNotConfigured
	}
	if a.now().Before(a.lockUntil) {
		return "", ErrRateLimited
	}
	if !a.lastAttempt.IsZero() && a.now().Sub(a.lastAttempt) > AttemptDecay {
		a.attempts = 0
	}
	a.lastAttempt = a.now()
	if !a.consumeNonce(nonce) {
		return "", ErrBadNonce
	}

	// The old PIN, proved the same way a login proves it. Guessing here is
	// rate-limited by the same counter, so this is not a way around the brake.
	old, err := base64.StdEncoding.DecodeString(a.cfg.Verifier)
	if err != nil {
		return "", fmt.Errorf("auth: stored verifier is unreadable: %w", err)
	}
	mac := hmac.New(sha256.New, old)
	mac.Write([]byte(nonce))
	got, err := base64.StdEncoding.DecodeString(proof)
	if err != nil || subtle.ConstantTimeCompare(got, mac.Sum(nil)) != 1 {
		a.attempts++
		if a.attempts >= 5 {
			wait := time.Duration(a.attempts-4) * 5 * time.Second
			if wait > MaxLockout {
				wait = MaxLockout
			}
			a.lockUntil = a.now().Add(wait)
		}
		return "", ErrBadProof
	}

	key, err := base64.StdEncoding.DecodeString(newKey)
	if err != nil || len(key) != sha256.Size {
		return "", fmt.Errorf("auth: malformed key")
	}
	if newSalt == "" {
		return "", fmt.Errorf("auth: the new PIN needs a salt of its own")
	}

	a.attempts = 0
	a.lockUntil = time.Time{}
	a.cfg.Salt = newSalt
	a.cfg.Verifier = base64.StdEncoding.EncodeToString(key)
	a.cfg.Iterations = DefaultIterations
	// Every session opened with the old PIN ends here.
	a.cfg.SessionSecret = randomHex(32)

	if a.Save != nil {
		if err := a.Save(a.cfg); err != nil {
			return "", err
		}
	}
	return a.issue(a.now().Add(time.Duration(a.cfg.SessionDays) * 24 * time.Hour)), nil
}

// ForcePIN sets the PIN without proving the old one.
//
// For the break glass and nothing else: somebody who has taken the card out of
// the box and written on it has physical possession, which is a stronger claim
// than any PIN. It is never reachable over the network — see
// packaging/freeway-card and the file it hands to the daemon at startup.
//
// The derivation happens here rather than in the shell script that reads the
// card, because PBKDF2 at a hundred and twenty thousand iterations is not a
// thing to reimplement in /bin/sh. The script's job is to carry the PIN the few
// metres from the card to this function and then forget it.
func (a *Authenticator) ForcePIN(pin string) error {
	if len(pin) < 4 || len(pin) > 12 {
		return fmt.Errorf("auth: a PIN is between 4 and 12 characters")
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	salt := randomHex(16)
	key := DeriveKey(pin, salt, DefaultIterations)
	a.cfg.Salt = salt
	a.cfg.Verifier = base64.StdEncoding.EncodeToString(key)
	a.cfg.Iterations = DefaultIterations
	a.cfg.SessionSecret = randomHex(32)
	if a.cfg.SessionDays <= 0 {
		a.cfg.SessionDays = 90
	}
	a.attempts = 0
	a.lockUntil = time.Time{}
	if a.Save != nil {
		return a.Save(a.cfg)
	}
	return nil
}
