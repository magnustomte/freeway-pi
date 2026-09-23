package web

import (
	"os/exec"
	"testing"
)

// TestBrowserCryptoMatchesGo runs the browser implementation's own checks.
//
// It is skipped where node is not installed rather than failing, because the
// numbers it verifies are also pinned in internal/auth/vectors_test.go, which
// needs nothing but Go. Between the two, neither side can drift alone.
func TestBrowserCryptoMatchesGo(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; the Go half of these vectors is checked in internal/auth")
	}
	out, err := exec.Command(node, "crypto_js_test.js").CombinedOutput()
	if err != nil {
		t.Fatalf("the browser implementation disagrees:\n%s", out)
	}
	t.Logf("\n%s", out)
}
