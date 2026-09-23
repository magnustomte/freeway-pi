package auth

import (
	"encoding/hex"
	"testing"
)

// crossVectors are shared verbatim with the browser implementation in
// internal/web/crypto_js_test.js. Both sides check the same numbers, so the
// two cannot drift apart without a test failing here or there, and neither
// needs the other present in order to run.
//
// A PIN that derives differently in the two would lock the owner out of their
// own box, and would do it silently at the moment the browser was updated.
var crossVectors = []struct {
	PIN, Salt  string
	Iterations int
	Nonce      string
	Key, Proof string
}{
	{
		PIN: "1234", Salt: "0123456789abcdef", Iterations: 1000, Nonce: "cafebabe",
		Key:   "96d86bc387622a132a8ec3fa3dbf14b7c25a903339de2db067430a08647c827e",
		Proof: "cDXyo8FCyFEZOGX8wzJeYaKfsqhUPi1pFPDFnycvzco=",
	},
	{
		// Non-ASCII and spaces: the interface accepts a passphrase, not only
		// digits, and UTF-8 encoding has to agree on both sides.
		PIN: "et lengre passord med mellomrom", Salt: "deadbeefdeadbeef", Iterations: 2000, Nonce: "0f0f0f0f",
		Key:   "4e61e6fad4d9bc49c55570a33e486caac275bf3791e36fcf37efc85ab78fa523",
		Proof: "3nUHQfCZOzBmixBENBZ4ZUN0JJLErMdQzR+xMXhJaIw=",
	},
}

func TestDerivationMatchesTheBrowser(t *testing.T) {
	for _, v := range crossVectors {
		key := DeriveKey(v.PIN, v.Salt, v.Iterations)
		if got := hex.EncodeToString(key); got != v.Key {
			t.Errorf("key for %q:\n  got  %s\n  want %s", v.PIN, got, v.Key)
			continue
		}
		if got := Proof(key, v.Nonce); got != v.Proof {
			t.Errorf("proof for %q:\n  got  %s\n  want %s", v.PIN, got, v.Proof)
		}
	}
}
