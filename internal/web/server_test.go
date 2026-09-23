package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestStaticFilesCarryAValidator: an embedded file has a zero modification
// time, so the file server sends no Last-Modified and a browser is free to
// guess how long to keep it. After a deploy that means new markup against an
// old stylesheet, which looks like a layout bug and is not one.
func TestStaticFilesCarryAValidator(t *testing.T) {
	s, _ := newAuthServer(t)

	res := httptest.NewRecorder()
	s.ServeHTTP(res, httptest.NewRequest("GET", "/style.css", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("status %d for the stylesheet", res.Code)
	}
	tag := res.Header().Get("ETag")
	if tag == "" {
		t.Fatal("no ETag, so nothing tells the browser when the file has changed")
	}
	if cc := res.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", cc)
	}

	// And the validator works: the same tag comes back as 304 with no body.
	req := httptest.NewRequest("GET", "/style.css", nil)
	req.Header.Set("If-None-Match", tag)
	again := httptest.NewRecorder()
	s.ServeHTTP(again, req)
	if again.Code != http.StatusNotModified {
		t.Errorf("status %d for a matching ETag, want 304", again.Code)
	}
	if again.Body.Len() != 0 {
		t.Errorf("304 came with %d bytes of body", again.Body.Len())
	}
}

// TestMissingStaticFileIsStillMissing guards the short cut that was not taken.
// Answering 304 from the wrapper, rather than letting the file server compare
// the tag, would have made every mistyped path look unchanged.
func TestMissingStaticFileIsStillMissing(t *testing.T) {
	s, _ := newAuthServer(t)
	req := httptest.NewRequest("GET", "/ingen-slik-fil.css", nil)
	req.Header.Set("If-None-Match", `"0.1-test"`)
	res := httptest.NewRecorder()
	s.ServeHTTP(res, req)
	if res.Code != http.StatusNotFound {
		t.Errorf("status %d for a file that does not exist, want 404", res.Code)
	}
}

// TestEveryPageLoadsTheThemeScriptEarly: the theme attribute has to be on
// <html> before the first paint, or the page renders in the system's colours
// and then flips. That means the script is in <head> and not deferred, on every
// page — a new page that forgets it flashes.
func TestEveryPageLoadsTheThemeScriptEarly(t *testing.T) {
	s, _ := newAuthServer(t)
	// "/" rather than "/index.html": the file server redirects the latter, and
	// "/" is what anybody actually opens.
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
		head, _, found := strings.Cut(body, "</head>")
		if !found {
			t.Errorf("%s: no </head>", page)
			continue
		}
		if !strings.Contains(head, `<script src="theme.js"></script>`) {
			t.Errorf("%s: theme.js is not loaded in <head>", page)
		}
		if strings.Contains(head, `src="theme.js" defer`) || strings.Contains(head, `defer src="theme.js"`) {
			t.Errorf("%s: theme.js is deferred, which is a flash of the wrong theme", page)
		}
	}
}

// TestTheStaticTagFollowsTheContent: the validator used to be the version and
// the commit, which name a build without identifying it. Two builds from the
// same dirty tree shared a tag, so the browser revalidated, was told 304, and
// kept serving an interface that had already been replaced on disk — a deploy
// that could not be seen and could not be refreshed away.
func TestTheStaticTagFollowsTheContent(t *testing.T) {
	a := New(nil, nil, Options{Version: "0.1", Commit: "abc123+endret"})
	b := New(nil, nil, Options{Version: "0.1", Commit: "abc123+endret"})
	if a.staticTag() != b.staticTag() {
		t.Fatal("the same embedded files produced two tags; every restart would re-download the interface")
	}

	// What the old scheme could not do: tell two different builds apart when
	// the version and the commit are identical. The files here cannot differ
	// within one test binary, so this checks the property that made it wrong —
	// that the tag is not merely a restatement of the build identifiers.
	c := New(nil, nil, Options{Version: "9.9", Commit: "totally-different"})
	if c.staticTag() != a.staticTag() {
		t.Error("the tag moved with the version rather than with the files")
	}
	if got := a.staticTag(); len(got) < 6 || got[0] != '"' || got[len(got)-1] != '"' {
		t.Errorf("tag %s is not a quoted entity-tag", got)
	}
}

// TestTheCatalogueTagFollowsItsContent: the same mistake as the static files,
// in the one place it is hardest to see. A stale validator there does not break
// the page — it leaves the words as they were, so the deploy looks like it
// silently did nothing.
func TestTheCatalogueTagFollowsItsContent(t *testing.T) {
	s := New(nil, nil, Options{Version: "0.1", Commit: "abc+endret"})

	tag := func(lang string) string {
		req := httptest.NewRequest(http.MethodGet, "/api/lang.js", nil)
		req.AddCookie(&http.Cookie{Name: langCookie, Value: lang})
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s gave %d", lang, w.Code)
		}
		return w.Header().Get("ETag")
	}

	no, en := tag("no"), tag("en")
	if no == "" || en == "" {
		t.Fatal("the catalogue is served without a validator")
	}
	if no == en {
		t.Error("two languages share a validator; one would be served for the other")
	}
	if no != tag("no") {
		t.Error("the same catalogue produced two tags; every reload would re-download it")
	}
	// And the tag has to be answered, or it is decoration: a browser that asks
	// "still this one?" should be told yes in one line rather than sent the
	// whole catalogue again.
	req304 := httptest.NewRequest(http.MethodGet, "/api/lang.js", nil)
	req304.AddCookie(&http.Cookie{Name: langCookie, Value: "no"})
	req304.Header.Set("If-None-Match", no)
	w304 := httptest.NewRecorder()
	s.ServeHTTP(w304, req304)
	if w304.Code != http.StatusNotModified {
		t.Errorf("an unchanged catalogue was re-sent with %d, want 304", w304.Code)
	}

	// The property that was wrong: the tag must not be a restatement of the
	// build identifiers, which do not change when the messages do.
	other := New(nil, nil, Options{Version: "9.9", Commit: "totally-different"})
	req := httptest.NewRequest(http.MethodGet, "/api/lang.js", nil)
	req.AddCookie(&http.Cookie{Name: langCookie, Value: "no"})
	w := httptest.NewRecorder()
	other.ServeHTTP(w, req)
	if got := w.Header().Get("ETag"); got != no {
		t.Errorf("the tag moved with the version rather than with the messages: %s vs %s", got, no)
	}
}
