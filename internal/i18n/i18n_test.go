package i18n

import (
	"strings"
	"testing"
)

// TestCataloguesAgree is the whole point of having a test here: a message that
// exists in one language and not the other is a screen with a key on it, or a
// screen that quietly falls back and looks half-finished.
func TestCataloguesAgree(t *testing.T) {
	base := Catalogue(Default)
	if len(base) == 0 {
		t.Fatal("the default catalogue is empty")
	}
	for _, l := range Languages() {
		if l == Default {
			continue
		}
		other := Catalogue(l)
		for id := range base {
			if _, ok := other[id]; !ok {
				t.Errorf("%s is missing %q", l, id)
			}
		}
		for id := range other {
			if _, ok := base[id]; !ok {
				t.Errorf("%s has %q, which %s does not", l, id, Default)
			}
		}
	}
}

// TestFormatsAgree: a message with arguments must take the same ones in every
// language, or Sprintf writes %!s(MISSING) onto somebody's screen.
func TestFormatsAgree(t *testing.T) {
	base := Catalogue(Default)
	for _, l := range Languages() {
		if l == Default {
			continue
		}
		for id, text := range Catalogue(l) {
			if got, want := verbs(text), verbs(base[id]); got != want {
				t.Errorf("%q: %s has %q, %s has %q", id, l, got, Default, want)
			}
		}
	}
}

// verbs collects the format verbs in order, so %d and %s cannot swap places
// between two languages without the test noticing.
//
// Only real verbs count. These messages are full of per cent signs that mean
// per cent — "drops the fans to 30 % without saving energy" — and an earlier
// version of this took the u of "uten" for a verb and reported a mismatch
// against the w of "without".
func verbs(s string) string {
	const letters = "vTtbcdoOqxXUeEfFgGsp"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			continue
		}
		// An escaped per cent is two characters and no argument. Skipping
		// only the first left the second to start a scan of its own, which
		// found a verb in the word after it.
		if i+1 < len(s) && s[i+1] == '%' {
			i++
			continue
		}
		j := i + 1
		for j < len(s) && strings.ContainsRune("+-# 0123456789.", rune(s[j])) {
			j++
		}
		if j >= len(s) || !strings.ContainsRune(letters, rune(s[j])) {
			continue
		}
		b.WriteByte('%')
		b.WriteByte(s[j])
		i = j
	}
	return b.String()
}

func TestParsePrefersAnExplicitChoice(t *testing.T) {
	if got := Parse("en", "nb-NO,nb;q=0.9"); got != EN {
		t.Errorf("explicit en with a Norwegian browser = %q", got)
	}
	// Bokmål and Nynorsk are Norwegian; accepting only "no" would leave a
	// Bokmål browser reading English.
	for _, tag := range []string{"nb-NO,en;q=0.5", "nn,en;q=0.5", "no"} {
		if got := Parse("", tag); got != NO {
			t.Errorf("Accept-Language %q = %q, want no", tag, got)
		}
	}
	if got := Parse("", "en-GB,en;q=0.9"); got != EN {
		t.Errorf("an English browser = %q", got)
	}
	// Nothing recognisable falls to the default rather than to whatever came
	// first in the header.
	if got := Parse("", "de-DE,fr;q=0.8"); got != Default {
		t.Errorf("an unsupported browser = %q, want the default", got)
	}
	if got := Parse("klingon", ""); got != Default {
		t.Errorf("an unknown explicit choice = %q, want the default", got)
	}
}

func TestMissingMessageShowsItsOwnID(t *testing.T) {
	if got := T(EN, "no.such.message"); got != "no.such.message" {
		t.Errorf("missing message = %q, want the id so it can be found", got)
	}
}

// TestMsgArgumentsAreTranslatedToo: "%s has to be between %g and %g %s" takes
// the name of a setting and its unit, and both are catalogue entries. Passing
// them as plain strings printed the ids onto the screen.
func TestMsgArgumentsAreTranslatedToo(t *testing.T) {
	got := T(EN, "error.setting.range", Msg("setting.service_interval_days.label"), 1.0, 999.0, Msg("ui.unit.days"))
	if strings.Contains(got, "setting.") || strings.Contains(got, "ui.unit") {
		t.Errorf("message ids leaked into the sentence: %q", got)
	}
	if !strings.Contains(got, "days") {
		t.Errorf("the unit was not translated: %q", got)
	}

	// And the same sentence in the other language uses the other language's
	// words for its arguments.
	nb := T(NO, "error.setting.range", Msg("ui.unit.days"), 1.0, 999.0, Msg("ui.unit.days"))
	if !strings.Contains(nb, "dager") {
		t.Errorf("Norwegian rendering used the wrong language for an argument: %q", nb)
	}
}

// TestEnglishHasNoNorwegianLetters: a Norwegian word that reaches the English
// catalogue is invisible to every check that reads the pages, because it is
// looked up correctly — it is simply the wrong language once it arrives. The
// wifi example once told English readers to type "wifi-psk=hemmelig". That one
// has no æ, ø or å in it; most Norwegian does.
func TestEnglishHasNoNorwegianLetters(t *testing.T) {
	for id, text := range Catalogue(EN) {
		if strings.ContainsAny(text, "æøåÆØÅ") {
			t.Errorf("%s: %q has Norwegian letters in the English catalogue", id, text)
		}
	}
}
