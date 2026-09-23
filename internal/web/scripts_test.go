// Checks on the hand-written scripts that ship with the interface.
//
// The file is called scripts_test.go and not static_js_test.go on purpose: js
// is a valid GOOS, so a file ending _js_test.go is taken as a build constraint
// and never compiled on Linux. It went green by not existing.
package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path"
	"regexp"
	"sort"
	"strings"
	"testing"

	"freewaypi/internal/i18n"
)

// TestNoCallsToMissingFunctions catches the one mistake this codebase has no
// other guard against: deleting a function and leaving the calls behind.
//
// There is no build step and no bundler, which is deliberate — the page has to
// be fast and the fastest thing to ship is the thing that is not there. The
// price is that nothing checks the scripts before a browser does, and a missing
// function is not a syntax error: the page loads, the call throws, and every
// render after it silently stops. That is exactly how renderLink came to be
// deleted along with the block beside it, leaving the overview stuck on
// "kobler til…" with the exception in a banner.
//
// It is a heuristic, not a parser. It errs towards silence: anything it cannot
// account for goes in the known-globals list rather than being reported.
func TestNoCallsToMissingFunctions(t *testing.T) {
	sources := readStatic(t)
	src := regexp.MustCompile(`<script src="([^"]+)"`)

	// What is in scope is what the page loads, read from the page rather than
	// from a list kept beside it. A list drifts — and it also cannot catch the
	// other half of this: a page that loads one script and not the one it
	// depends on.
	pages := rawPages(t)
	if len(pages) == 0 {
		t.Fatal("no pages were read, so this test proves nothing")
	}
	for page, markup := range pages {
		loaded := []string{}
		for _, m := range src.FindAllStringSubmatch(markup, -1) {
			if name := path.Base(m[1]); sources[name] != "" {
				loaded = append(loaded, name)
			}
		}
		if len(loaded) == 0 {
			t.Errorf("%s loads no scripts this test can read", page)
			continue
		}

		inScope := map[string]bool{}
		for _, name := range loaded {
			for id := range declaredIn(sources[name]) {
				inScope[id] = true
			}
		}

		sort.Strings(loaded)
		for _, name := range loaded {
			var missing []string
			for id := range calledIn(sources[name]) {
				if inScope[id] || jsGlobals[id] {
					continue
				}
				missing = append(missing, id)
			}
			sort.Strings(missing)
			for _, id := range missing {
				t.Errorf("%s: %s calls %s(), which nothing the page loads defines", page, name, id)
			}
		}
	}
}

// readStatic returns every hand-written script, stripped of comments and string
// literals so the patterns below cannot match inside prose or a template.
func readStatic(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := fs.WalkDir(staticFS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(p) != ".js" {
			return err
		}
		// Vendored and minified: not ours to check, and the heuristic would
		// drown in it.
		if path.Base(p) == "uplot.js" {
			return nil
		}
		b, err := fs.ReadFile(staticFS, p)
		if err != nil {
			return err
		}
		out[path.Base(p)] = stripJS(string(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) < 5 {
		t.Fatalf("only found %d scripts; the walk is wrong", len(out))
	}
	return out
}

// stripJS removes comments and the contents of string and template literals.
func stripJS(src string) string {
	var b strings.Builder
	for i := 0; i < len(src); {
		switch {
		case src[i] == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case src[i] == '/' && i+1 < len(src) && src[i+1] == '*':
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			i += 2
		case src[i] == '"' || src[i] == '\'' || src[i] == '`':
			quote := src[i]
			i++
			for i < len(src) && src[i] != quote {
				if src[i] == '\\' {
					i++
				}
				i++
			}
			i++
			b.WriteString(`""`)
		default:
			b.WriteByte(src[i])
			i++
		}
	}
	return b.String()
}

var (
	reDeclKeyword = regexp.MustCompile(`\b(?:function|class|const|let|var)\s+([A-Za-z_$][\w$]*)`)
	reDestructure = regexp.MustCompile(`\b(?:const|let|var)\s*[\[{]([^\]}]*)[\]}]`)
	reParams      = regexp.MustCompile(`\(([^()]*)\)\s*(?:=>|\{)`)
	reArrowOne    = regexp.MustCompile(`\b([A-Za-z_$][\w$]*)\s*=>`)
	reCatch       = regexp.MustCompile(`\bcatch\s*\(\s*([A-Za-z_$][\w$]*)`)
	reCall        = regexp.MustCompile(`(^|[^.\w$])([A-Za-z_$][\w$]*)\s*\(`)
	reIdent       = regexp.MustCompile(`^[A-Za-z_$][\w$]*$`)
)

// declaredIn collects every name the file binds: declarations, destructuring,
// parameters and catch bindings. Parameters matter because a callback passed in
// and then called — save(button, feedback, run) and then run() — is common here.
func declaredIn(src string) map[string]bool {
	out := map[string]bool{}
	add := func(s string) {
		if reIdent.MatchString(s) {
			out[s] = true
		}
	}
	for _, m := range reDeclKeyword.FindAllStringSubmatch(src, -1) {
		add(m[1])
	}
	for _, m := range reDestructure.FindAllStringSubmatch(src, -1) {
		for _, part := range strings.Split(m[1], ",") {
			if i := strings.LastIndex(part, ":"); i >= 0 {
				part = part[i+1:]
			}
			part, _, _ = strings.Cut(part, "=")
			add(strings.TrimSpace(part))
		}
	}
	for _, m := range reParams.FindAllStringSubmatch(src, -1) {
		for _, part := range strings.Split(m[1], ",") {
			part, _, _ = strings.Cut(part, "=")
			add(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(part), "...")))
		}
	}
	for _, m := range reArrowOne.FindAllStringSubmatch(src, -1) {
		add(m[1])
	}
	for _, m := range reCatch.FindAllStringSubmatch(src, -1) {
		add(m[1])
	}
	return out
}

func calledIn(src string) map[string]bool {
	out := map[string]bool{}
	for _, m := range reCall.FindAllStringSubmatch(src, -1) {
		if !jsKeywords[m[2]] {
			out[m[2]] = true
		}
	}
	return out
}

var jsKeywords = words(`if for while switch catch return typeof function await new
	delete void in of do else case throw yield instanceof async super import`)

// jsGlobals is what the browser provides. Generous on purpose: a false alarm
// here would train somebody to ignore this test, which is worse than a miss.
var jsGlobals = words(`window document console localStorage sessionStorage fetch
	setTimeout clearTimeout setInterval clearInterval requestAnimationFrame
	cancelAnimationFrame matchMedia EventSource WebSocket Promise JSON Math Date
	Number String Boolean Array Object Error TypeError RangeError Map Set WeakMap
	WeakSet Symbol Proxy Reflect BigInt parseInt parseFloat isNaN isFinite
	encodeURIComponent decodeURIComponent encodeURI decodeURI alert confirm prompt
	btoa atob CustomEvent Event MouseEvent KeyboardEvent addEventListener
	removeEventListener dispatchEvent navigator location history performance crypto
	TextEncoder TextDecoder Uint8Array Uint16Array Uint32Array Int8Array Int16Array
	Int32Array Float32Array Float64Array ArrayBuffer DataView Intl AbortController
	FormData Headers Request Response URL URLSearchParams structuredClone
	queueMicrotask getComputedStyle IntersectionObserver ResizeObserver
	MutationObserver Blob File FileReader Image uPlot`)

func words(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields(s) {
		out[w] = true
	}
	return out
}

// TestEveryPageLoadsTheCatalogueFirst: the words have to be there before the
// first line of any script that uses them.
//
// api/lang.js sets the catalogue, i18n.js defines t() over it, and everything
// else uses t() — some of it at the top level, where a const holds a label. The
// order is easy to get wrong by adding a page and copying the wrong header, and
// getting it wrong shows message ids on screen rather than failing loudly.
func TestEveryPageLoadsTheCatalogueFirst(t *testing.T) {
	s, _ := newAuthServer(t)
	for _, page := range []string{
		"/", "/data.html", "/settings.html",
		"/programs.html", "/system.html", "/alarms.html",
	} {
		res := httptest.NewRecorder()
		s.ServeHTTP(res, httptest.NewRequest("GET", page, nil))
		if res.Code != http.StatusOK {
			t.Errorf("%s: status %d", page, res.Code)
			continue
		}
		body := res.Body.String()
		head, _, _ := strings.Cut(body, "</head>")

		catalogue := strings.Index(body, `src="api/lang.js"`)
		helper := strings.Index(body, `src="i18n.js"`)
		if catalogue < 0 || helper < 0 {
			t.Errorf("%s: does not load both api/lang.js and i18n.js", page)
			continue
		}
		if catalogue > helper {
			t.Errorf("%s: i18n.js is loaded before the catalogue it reads", page)
		}
		for _, want := range []string{`src="api/lang.js"`, `src="i18n.js"`} {
			if !strings.Contains(head, want) {
				t.Errorf("%s: %s is not in <head>, so the words arrive after the paint", page, want)
			}
		}
		if strings.Contains(head, `src="api/lang.js" defer`) {
			t.Errorf("%s: the catalogue is deferred, which is the race this replaced", page)
		}
		for _, after := range []string{"theme.js", "app.js", "system.js", "settings.js",
			"programs.js", "alarms.js", "data.js"} {
			if at := strings.Index(body, `src="`+after+`"`); at >= 0 && at < helper {
				t.Errorf("%s: %s is loaded before i18n.js", page, after)
			}
		}
	}
}

// TestNoNorwegianLeftInTheScripts: the pages are authored in Norwegian and the
// markup keeps it as the fallback, but a script that builds a card has no
// fallback — a literal there is a sentence an English reader will see.
//
// Checked two ways. The letters catch most of it; the catalogue catches the
// rest, because a literal that is word for word a Norwegian message is a
// message that was meant to be looked up. An earlier version of this test only
// had the letters, and "Ingen programmer har en funksjon satt." walked past it.
func TestNoNorwegianLeftInTheScripts(t *testing.T) {
	// Folded, because a message used as a fallback in code is rarely spelled
	// with the catalogue's capital: 'aggregat' against "Aggregat" and
	// 'brukernavn' against "Brukernavn" both reached English readers while
	// this compared exactly.
	//
	// Only messages whose English differs count. "System", "Gateway" and
	// "Alarm" are the same word in both languages, and the same spelling as a
	// dozen class names and keys in the scripts; a leak is text that would be
	// wrong for an English reader, and those would not be.
	english := i18n.Catalogue(i18n.EN)
	norwegian := map[string]bool{}
	for id, text := range i18n.Catalogue(i18n.NO) {
		if len([]rune(text)) > 2 && !strings.EqualFold(text, english[id]) {
			norwegian[strings.ToLower(text)] = true
		}
	}

	for name, src := range rawStatic(t) {
		if name == "i18n.js" {
			continue // its own comments describe the Norwegian case
		}
		for i, line := range strings.Split(src, "\n") {
			if strings.ContainsAny(line, "æøåÆØÅ") {
				t.Errorf("%s:%d a Norwegian literal survived: %s", name, i+1, strings.TrimSpace(line))
			}
		}
		for _, lit := range literals(withoutComments(src)) {
			if norwegian[strings.ToLower(lit)] {
				t.Errorf("%s: %q is a catalogue message and should be looked up", name, lit)
			}
		}
	}
}

// rawStatic reads the scripts as written. readStatic strips the contents of
// every string literal, which is right for finding calls and useless for
// finding sentences — the first version of the check above ran against it and
// passed by having nothing to look at.
func rawStatic(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := fs.WalkDir(staticFS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(p) != ".js" || path.Base(p) == "uplot.js" {
			return err
		}
		b, err := fs.ReadFile(staticFS, p)
		if err != nil {
			return err
		}
		out[path.Base(p)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// withoutComments drops the comments and keeps everything else.
//
// stripJS does the opposite of what is wanted here: it keeps the comments and
// empties the strings. A prose comment about a message — "the difference
// between teller… resolving by itself and sitting there" — is not a literal
// anybody will read on screen.
func withoutComments(src string) string {
	var b strings.Builder
	for i := 0; i < len(src); i++ {
		switch {
		case src[i] == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			b.WriteByte('\n')
		case src[i] == '/' && i+1 < len(src) && src[i+1] == '*':
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			i++
		case src[i] == '\'' || src[i] == '"' || src[i] == '`':
			q := src[i]
			b.WriteByte(src[i])
			i++
			for i < len(src) && src[i] != q {
				if src[i] == '\\' {
					b.WriteByte(src[i])
					i++
				}
				if i < len(src) {
					b.WriteByte(src[i])
					i++
				}
			}
			if i < len(src) {
				b.WriteByte(src[i])
			}
		default:
			b.WriteByte(src[i])
		}
	}
	return b.String()
}

// literals pulls the string literals out of a script.
func literals(src string) []string {
	var out []string
	for i := 0; i < len(src); i++ {
		q := src[i]
		if q != '\'' && q != '"' {
			continue
		}
		j := i + 1
		for j < len(src) && src[j] != q {
			if src[j] == '\\' {
				j++
			}
			j++
		}
		if j < len(src) {
			out = append(out, src[i+1:j])
		}
		i = j
	}
	return out
}

// TestNoPageRepeatsAnElementId: ids are how every script on this page finds
// anything, and getElementById returns the first match in document order. A
// second element with the same id does not fail, it shadows — so a card can
// quietly render itself into a login field and show a button with no form
// above it, which is exactly what happened when the PIN card reused "pin".
func TestNoPageRepeatsAnElementId(t *testing.T) {
	id := regexp.MustCompile(`\bid="([^"]+)"`)
	pages := rawPages(t)
	if len(pages) == 0 {
		t.Fatal("no pages were read, so this test proves nothing")
	}
	for name, body := range pages {
		seen := map[string]bool{}
		for _, m := range id.FindAllStringSubmatch(body, -1) {
			if seen[m[1]] {
				t.Errorf("%s uses id %q more than once; whichever script looks it up gets the first one", name, m[1])
			}
			seen[m[1]] = true
		}
	}
}

// rawPages reads the markup. rawStatic above deliberately reads only .js, so a
// test that asked it for pages got an empty map and passed by checking nothing
// — which is how the first version of the duplicate-id test above proved
// nothing at all. Verified by reintroducing the clash it was written for and
// watching it stay green.
func rawPages(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := fs.WalkDir(staticFS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(p) != ".html" {
			return err
		}
		b, err := fs.ReadFile(staticFS, p)
		if err != nil {
			return err
		}
		out[path.Base(p)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestEveryMessageTheScriptsAskForExists: t() falls back to printing the id, so
// a message that is not in the catalogue renders as "ui.port" on the page and
// nothing anywhere says a word. Deleting one is a one-line mistake — this file
// has already made it — and without this the first person to notice is
// whoever is looking at the page.
func TestEveryMessageTheScriptsAskForExists(t *testing.T) {
	call := regexp.MustCompile(`\bt\(\s*'([a-z][a-z0-9_.]*)'`)
	scripts := rawStatic(t)
	if len(scripts) == 0 {
		t.Fatal("no scripts were read, so this test proves nothing")
	}
	for name, body := range scripts {
		for _, m := range call.FindAllStringSubmatch(withoutComments(body), -1) {
			if !i18n.Has(i18n.Default, m[1]) {
				t.Errorf("%s asks for message %q, which no catalogue has", name, m[1])
			}
		}
	}
}

// TestEveryButtonIsStyled: a button made in script without a class is drawn by
// the browser in its own grey, among the page's own buttons. Six of them got
// that far on the system page — each one a secondary action beside a primary,
// each one missed because it looked fine in the code.
func TestEveryButtonIsStyled(t *testing.T) {
	made := regexp.MustCompile(`const (\w+) = document\.createElement\('button'\)`)
	for name, src := range rawStatic(t) {
		lines := strings.Split(src, "\n")
		for i, line := range lines {
			m := made.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			end := i + 8
			if end > len(lines) {
				end = len(lines)
			}
			near := strings.Join(lines[i:end], "\n")
			if !strings.Contains(near, m[1]+".className") && !strings.Contains(near, m[1]+".classList") {
				t.Errorf("%s:%d button %q is given no class, so the browser styles it", name, i+1, m[1])
			}
		}
	}
}

// TestStaticTextIsTranslated: markup is authored in Norwegian and translated
// by data-i18n before it is painted. Text without the attribute is shown in
// Norwegian to everybody — "kobler til…" and "aggregat" inside the overview's
// drawing did exactly that, on every page load, because the script checks
// above only ever read the scripts.
func TestStaticTextIsTranslated(t *testing.T) {
	// Text that is the same in every language.
	same := map[string]bool{"Freeway&nbsp;Pi": true, "Freeway Pi": true, "°C": true}
	// An element with text and no markup inside it, which is what data-i18n
	// replaces. Elements with children are left alone; their text is in the
	// children.
	element := regexp.MustCompile(`<(\w[\w-]*)([^>]*)>([^<>]+)</\w[\w-]*>`)
	onlyMarks := regexp.MustCompile(`^[\s\d.,:;·—–\-+%&;()/]*$`)
	err := fs.WalkDir(staticFS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(p) != ".html" {
			return err
		}
		b, err := fs.ReadFile(staticFS, p)
		if err != nil {
			return err
		}
		for _, m := range element.FindAllStringSubmatch(string(b), -1) {
			tag, attrs, text := m[1], m[2], strings.TrimSpace(m[3])
			if tag == "script" || tag == "style" || text == "" || same[text] || onlyMarks.MatchString(text) {
				continue
			}
			if !strings.Contains(attrs, "data-i18n") {
				t.Errorf("%s: <%s> %q has no data-i18n, so English readers see it in Norwegian", path.Base(p), tag, text)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
