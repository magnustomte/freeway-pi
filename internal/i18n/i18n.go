// Package i18n holds every string a person reads, in every language the
// interface speaks.
//
// One catalogue, two readers. The daemon looks strings up here; the browser
// fetches the same file and looks them up itself. A message that exists in only
// one of the two is the failure this is meant to make impossible, so the
// catalogues are checked against each other by a test rather than by hoping.
//
// The language is chosen per browser, not per box: a household has one
// ventilation unit and several phones, and the one that belongs to a guest
// should not have to be Norwegian. It arrives as a request header. The
// exception is anything the box sends by itself — an alarm by mail or webhook
// has no browser to ask, so it uses the configured default.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

//go:embed locales/*.json
var locales embed.FS

// Lang is a language the interface speaks.
type Lang string

// The languages. Norwegian is the default because the unit, its manual and its
// owner are.
const (
	NO Lang = "no"
	EN Lang = "en"
)

// Default is what is used when nothing says otherwise.
const Default = NO

// Languages lists what is available, in the order a chooser should offer them.
func Languages() []Lang { return []Lang{NO, EN} }

// Name is what a language calls itself, which is what belongs in a chooser:
// somebody looking for English does not read Norwegian to find it.
func Name(l Lang) string {
	switch l {
	case EN:
		return "English"
	default:
		return "Norsk"
	}
}

var (
	once     sync.Once
	loaded   map[Lang]map[string]string
	loadErr  error
	fallback = Default
)

func load() {
	loaded = make(map[Lang]map[string]string)
	for _, l := range Languages() {
		b, err := locales.ReadFile("locales/" + string(l) + ".json")
		if err != nil {
			loadErr = fmt.Errorf("i18n: %w", err)
			return
		}
		var m map[string]string
		if err := json.Unmarshal(b, &m); err != nil {
			loadErr = fmt.Errorf("i18n: %s.json: %w", l, err)
			return
		}
		loaded[l] = m
	}
}

// Catalogue returns every string in one language, for serving to a browser.
func Catalogue(l Lang) map[string]string {
	once.Do(load)
	if m, ok := loaded[l]; ok {
		return m
	}
	return loaded[fallback]
}

// T looks a message up and fills in its arguments.
//
// A missing message returns its own id rather than an empty string or a panic:
// on a screen that is ugly and obvious, which is what finding one requires.
func T(l Lang, id string, args ...any) string {
	once.Do(load)
	m, ok := loaded[l]
	if !ok {
		m = loaded[fallback]
	}
	s, ok := m[id]
	if !ok {
		// Fall back to the default language before giving up, so a message
		// added in Norwegian and not yet translated reads as Norwegian rather
		// than as a key.
		if s, ok = loaded[fallback][id]; !ok {
			return id
		}
	}
	if len(args) == 0 {
		return s
	}
	return fmt.Sprintf(s, resolve(l, args)...)
}

// Parse works out which language to use.
//
// An explicit choice wins; otherwise the browser's own preference, in the order
// it gave them. Anything unrecognised falls through to the default rather than
// to whatever happened to be first.
func Parse(explicit, acceptLanguage string) Lang {
	if l, ok := match(explicit); ok {
		return l
	}
	// "nb-NO,nb;q=0.9,no;q=0.8,en;q=0.7" — taken in order, which is near
	// enough: the q values are already a ranking and this list is short.
	for _, part := range strings.Split(acceptLanguage, ",") {
		tag, _, _ := strings.Cut(strings.TrimSpace(part), ";")
		if l, ok := match(tag); ok {
			return l
		}
	}
	return Default
}

// match recognises a language tag.
//
// Norwegian has three that mean this: no, nb and nn. Accepting only "no" would
// leave a Bokmål browser reading English.
func match(tag string) (Lang, bool) {
	tag = strings.ToLower(strings.TrimSpace(tag))
	base, _, _ := strings.Cut(tag, "-")
	switch base {
	case "no", "nb", "nn":
		return NO, true
	case "en":
		return EN, true
	}
	return "", false
}

// IDs lists every message id, sorted. Used by the test that holds the
// catalogues to each other.
func IDs(l Lang) []string {
	c := Catalogue(l)
	out := make([]string, 0, len(c))
	for k := range c {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Has reports whether a message exists, in the given language or the default.
//
// For the places that have a family of ids and a general one to fall back on —
// an alarm code the manual never named, for instance — where asking is better
// than rendering the id and calling it a name.
func Has(l Lang, id string) bool {
	once.Do(load)
	if m, ok := loaded[l]; ok {
		if _, ok := m[id]; ok {
			return true
		}
	}
	_, ok := loaded[fallback][id]
	return ok
}
