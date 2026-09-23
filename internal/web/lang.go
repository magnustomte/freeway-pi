package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"freewaypi/internal/i18n"
)

// langCookie is how a browser says which language it wants.
//
// A cookie rather than a header, because it rides along on everything without
// the page having to remember: the spreadsheet download and the backup are
// plain links, and their file names and column headings have to be in the same
// language as the page that offered them.
const langCookie = "freeway-lang"

// langOf works out what language to answer a request in.
//
// The explicit choice first, then what the browser asked for. The box's own
// configured language does not come into it: that one is for the messages the
// box sends by itself, where there is nobody to ask.
func langOf(r *http.Request) i18n.Lang {
	if r == nil {
		return i18n.Default
	}
	explicit := ""
	if c, err := r.Cookie(langCookie); err == nil {
		explicit = c.Value
	}
	return i18n.Parse(explicit, r.Header.Get("Accept-Language"))
}

// t looks a message up in the language of the request being answered.
func t(r *http.Request, id string, args ...any) string {
	return i18n.T(langOf(r), id, args...)
}

// handleCatalogueScript serves the catalogue as a script rather than as data.
//
// A blocking script in <head>, so the words are there before the first line of
// any page script runs. Fetching it instead left a window in which t() could
// only return message ids, and anything drawn in that window — a row of mode
// buttons, a range selector, a chart legend — kept them for the rest of the
// visit. That was not a race worth winning carefully; it was a race worth not
// having.
func (s *Server) handleCatalogueScript(w http.ResponseWriter, r *http.Request) {
	lang := langOf(r)
	body, err := json.Marshal(map[string]any{
		"lang":      lang,
		"messages":  i18n.Catalogue(lang),
		"available": languageList(),
	})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	// The answer depends on who is asking, so a shared cache must not hand one
	// reader's language to another.
	w.Header().Set("Vary", "Cookie, Accept-Language")
	w.Header().Set("Cache-Control", "no-cache")
	// A digest of what is being sent, not the version and the commit.
	//
	// Those name a build without identifying it — a working tree that has been
	// changed reports the same commit however many times it is built, so every
	// deploy produced the same validator and the browser was told 304 while the
	// server had new text sitting right there. The static files were fixed for
	// this; the catalogue was the one place left, and it is the place where the
	// symptom is hardest to read: the page works, and only the words are old.
	sum := sha256.Sum256(body)
	tag := fmt.Sprintf("%q", hex.EncodeToString(sum[:])[:16])
	w.Header().Set("ETag", tag)
	// And answer the question the tag invites. Without this the validator was
	// decorative: every page load re-sent the whole catalogue, and a browser
	// that politely asked "still this one?" was told the long way round.
	if match := r.Header.Get("If-None-Match"); match != "" {
		for _, candidate := range strings.Split(match, ",") {
			if strings.TrimSpace(candidate) == tag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
	}
	fmt.Fprintf(w, "window.__i18n = %s;\n", body)
}

// handleLanguages serves a catalogue as data, for a chooser that wants to know
// what is on offer without reloading.
func (s *Server) handleLanguages(w http.ResponseWriter, r *http.Request) {
	// "auto" means: work it out the same way the daemon does for every other
	// request. One implementation of the rules, so the page and the API cannot
	// disagree about which language they are in.
	want := r.PathValue("lang")
	lang := langOf(r)
	if want != "" && want != "auto" {
		lang = i18n.Parse(want, "")
	}
	// Cached like the rest of the interface: it changes only when the binary
	// does, and it is fetched before the page can render.
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", s.staticTag())
	writeJSON(w, http.StatusOK, map[string]any{
		"lang":      lang,
		"messages":  i18n.Catalogue(lang),
		"available": languageList(),
	})
}

// languageList is what a chooser offers: each language named in itself, since
// somebody looking for English does not read Norwegian to find it.
func languageList() []map[string]string {
	out := make([]map[string]string, 0, len(i18n.Languages()))
	for _, l := range i18n.Languages() {
		out = append(out, map[string]string{"code": string(l), "name": i18n.Name(l)})
	}
	return out
}

// localiseState fills in every id the deeper packages left for this layer.
//
// They hold no sentences on purpose: a sentence has a language, and which one
// depends on who is asking. This is the one place that knows.
func (s *Server) localiseState(r *http.Request, out *stateResponse) {
	lang := langOf(r)
	out.Link.Label = i18n.T(lang, out.Link.Label)
	if out.Link.Detail != "" {
		if out.Link.DetailArg != 0 {
			out.Link.Detail = i18n.T(lang, out.Link.Detail, out.Link.DetailArg)
		} else {
			out.Link.Detail = i18n.T(lang, out.Link.Detail)
		}
	}
	for i := range out.Flags {
		out.Flags[i].Label = i18n.T(lang, out.Flags[i].Label)
	}
}
