package privhelper

import (
	"bufio"
	"context"
	"freewaypi/internal/i18n"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// serve stands in for the helper: it reads one request and answers with
// whatever reply is given, after recording what it was asked.
func serve(t *testing.T, reply string) (path string, got *string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	var request string
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		s := bufio.NewScanner(conn)
		var b strings.Builder
		for s.Scan() {
			if s.Text() == "." {
				break
			}
			b.WriteString(s.Text())
			b.WriteString("\n")
		}
		request = b.String()
		conn.Write([]byte(reply))
	}()
	t.Cleanup(func() { <-done })
	return path, &request
}

func TestCallSendsFieldsInOrder(t *testing.T) {
	path, got := serve(t, "changed; reverts in 90s unless confirmed\n")
	defer withSocket(t, path)()

	out, err := Call(context.Background(), "apply",
		Field{Key: "connection", Value: "Wired connection 1"},
		Field{Key: "dhcp", Value: "0"},
		Field{Key: "address", Value: "192.0.2.10/24"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !strings.HasPrefix(out, "changed") {
		t.Errorf("answer = %q", out)
	}

	want := "op=apply\nconnection=Wired connection 1\ndhcp=0\naddress=192.0.2.10/24\n"
	if *got != want {
		t.Errorf("request\n got %q\nwant %q", *got, want)
	}
}

// TestCallReportsFailure: the helper says "feil: " and the reason, and that has
// to come back as an error rather than as a successful answer nobody reads.
func TestCallReportsFailure(t *testing.T) {
	path, _ := serve(t, "feil: ugyldig adresse; forventet 192.0.2.10/24\n")
	defer withSocket(t, path)()

	_, err := Call(context.Background(), "apply")
	if err == nil {
		t.Fatal("a refusal came back as success")
	}
	if !strings.Contains(err.Error(), "ugyldig adresse") {
		t.Errorf("error = %v, want the helper's reason", err)
	}
	if strings.Contains(err.Error(), "feil: ") {
		t.Errorf("error = %v, still carrying the marker", err)
	}
}

// TestCallRefusesNewlines guards the one way a caller could smuggle an extra
// field past the helper.
func TestCallRefusesNewlines(t *testing.T) {
	path, _ := serve(t, "ok")
	defer withSocket(t, path)()

	_, err := Call(context.Background(), "apply",
		Field{Key: "connection", Value: "eth0\nop=poweroff"})
	if err == nil {
		t.Fatal("a value with a newline in it was sent")
	}
}

func TestAvailableSaysWhyNot(t *testing.T) {
	defer withSocket(t, filepath.Join(t.TempDir(), "absent.sock"))()
	ok, why := Available(context.Background())
	if ok {
		t.Fatal("reported available with no helper listening")
	}
	if why == "" {
		t.Error("said unavailable without saying why")
	}
}

// withSocket points the package at a test socket and puts it back afterwards.
func withSocket(t *testing.T, path string) func() {
	t.Helper()
	old := socketPath
	socketPath = path
	return func() { socketPath = old }
}

// TestRefusalsSpeakTheReadersLanguage: the helper's no used to be a Norwegian
// sentence, passed straight to the page, so an English reader got Norwegian
// wherever the helper refused. It now sends a code; the code is what is shown.
func TestRefusalsSpeakTheReadersLanguage(t *testing.T) {
	err := refusal("wifi.country\tinvalid country code")
	if got := i18n.Translate(i18n.EN, err); got != i18n.T(i18n.EN, "helper.wifi.country") {
		t.Errorf("in English: %q", got)
	}
	if got := i18n.Translate(i18n.NO, err); got != i18n.T(i18n.NO, "helper.wifi.country") {
		t.Errorf("in Norwegian: %q", got)
	}

	// A detail the helper alone knows travels with it.
	err = refusal("wifi.join\tcould not join the network\tNo network with SSID 'x' found.")
	if got := i18n.Translate(i18n.EN, err); !strings.Contains(got, "No network with SSID") {
		t.Errorf("the detail was lost: %q", got)
	}

	// A code the catalogue has never heard of says the English, not the code.
	if got := refusal("brand.new\tsomething new went wrong").Error(); got != "something new went wrong" {
		t.Errorf("an unknown code gave %q", got)
	}
	// And the older form, a sentence with no tabs, is passed through as it is.
	if got := refusal("ugyldig adresse").Error(); got != "ugyldig adresse" {
		t.Errorf("the older form gave %q", got)
	}
}

// TestEveryRefusalHasWords reads the helper itself: a code it can send that the
// catalogue has no words for would reach the page as the English fallback, in
// Norwegian as well — the same leak this replaced, only the other way round.
func TestEveryRefusalHasWords(t *testing.T) {
	src, err := os.ReadFile("../../packaging/freeway-helper")
	if err != nil {
		t.Fatal(err)
	}
	codes := regexp.MustCompile(`\bfail ([a-z][a-z0-9.-]*) "`).FindAllStringSubmatch(string(src), -1)
	if len(codes) < 40 {
		t.Fatalf("found %d refusals in the helper; the pattern no longer matches how it says no", len(codes))
	}
	for _, m := range codes {
		for _, l := range i18n.Languages() {
			if !i18n.Has(l, "helper."+m[1]) {
				t.Errorf("the helper can refuse with %q, and %s has no words for it", m[1], l)
			}
		}
	}
}
