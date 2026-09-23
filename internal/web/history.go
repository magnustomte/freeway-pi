package web

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"freewaypi/internal/i18n"
	"freewaypi/internal/store"
)

// History is the part of the store the interface reads.
type History interface {
	Query(metric store.Metric, from, to time.Time, maxPoints int) (*store.Series, error)
	Stats() (*store.Stats, error)
	Snapshot(path string) error
	Events(since time.Time, limit int) ([]store.Event, error)
	CountEvents(since time.Time) (int, error)
}

// handleMetrics lists what can be graphed, so the page does not have to carry
// its own copy of the list and drift from it.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, store.Metrics)
}

// handleSeries returns one or more metrics over a window.
//
// Several in one request because a chart shows several lines, and issuing four
// requests for four lines means four chances for them to disagree about what
// "now" meant.
func (s *Server) handleSeries(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		writeError(w, r, http.StatusNotFound, fmt.Errorf("history is not being recorded"))
		return
	}
	q := r.URL.Query()

	names := strings.Split(q.Get("metric"), ",")
	if len(names) == 0 || names[0] == "" {
		writeError(w, r, http.StatusBadRequest, fmt.Errorf("give at least one metric"))
		return
	}
	if len(names) > 8 {
		writeError(w, r, http.StatusBadRequest, fmt.Errorf("at most eight metrics at a time"))
		return
	}

	to := time.Now()
	from := to.Add(-6 * time.Hour)
	if v := q.Get("hours"); v != "" {
		hours, err := strconv.ParseFloat(v, 64)
		if err != nil || hours <= 0 || hours > 24*366*10 {
			writeError(w, r, http.StatusBadRequest, fmt.Errorf("hours must be a positive number"))
			return
		}
		from = to.Add(-time.Duration(hours * float64(time.Hour)))
	}

	points := 2000
	if v := q.Get("points"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 2 || n > 20000 {
			writeError(w, r, http.StatusBadRequest, fmt.Errorf("points must be between 2 and 20000"))
			return
		}
		points = n
	}

	out := make([]*store.Series, 0, len(names))
	for _, name := range names {
		def, ok := store.MetricByName(strings.TrimSpace(name))
		if !ok {
			writeError(w, r, http.StatusBadRequest, fmt.Errorf("no metric called %q", name))
			return
		}
		series, err := s.history.Query(def.ID, from, to, points)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, err)
			return
		}
		series.Label = t(r, series.Label)
		out = append(out, series)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSeriesCSV exports the history as a spreadsheet.
//
// Wide rather than long: one row per moment with a column per metric, because
// that is what somebody who asked for a CSV is going to plot. The timestamps
// are taken as a union rather than assumed to line up — in practice every
// metric in one request lands on the same tier and so on the same instants, but
// a file that silently dropped a column when they did not would be worse than
// one with a few gaps in it.
//
// Semicolons and decimal commas by default. This interface is in Norwegian and
// the likely destination is a Norwegian Excel, which reads a comma as a column
// break and a point as nothing at all. ?decimal=point gives the international
// form for anything that would rather have it.
func (s *Server) handleSeriesCSV(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		writeError(w, r, http.StatusNotFound, fmt.Errorf("history is not being recorded"))
		return
	}
	q := r.URL.Query()

	names := strings.Split(q.Get("metric"), ",")
	if len(names) == 0 || names[0] == "" {
		// No metric named means everything there is, which is what a plain
		// "download the data" button should hand over.
		names = nil
		for _, m := range store.Metrics {
			names = append(names, m.Name)
		}
	}

	to := time.Now()
	hours := 24.0
	if v := q.Get("hours"); v != "" {
		parsed, err := strconv.ParseFloat(v, 64)
		if err != nil || parsed <= 0 || parsed > 24*366*10 {
			writeError(w, r, http.StatusBadRequest, fmt.Errorf("hours must be a positive number"))
			return
		}
		hours = parsed
	}
	from := to.Add(-time.Duration(hours * float64(time.Hour)))

	// The cap the query allows, so an export is the data rather than a
	// thinned-out picture of it.
	const points = 20000

	// Deduplicated and capped. Each name costs a query for up to twenty
	// thousand points and a map to hold them; without this, one unauthenticated
	// URL asking for the same metric a thousand times is enough to take the
	// daemon out of memory on a Pi. handleSeries above has always had a cap.
	seen := map[string]bool{}
	unique := names[:0]
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		unique = append(unique, name)
	}
	names = unique
	if len(names) > len(store.Metrics) {
		writeError(w, r, http.StatusBadRequest, i18n.Errf("error.metrics.toomany"))
		return
	}

	series := make([]*store.Series, 0, len(names))
	for _, name := range names {
		def, ok := store.MetricByName(name)
		if !ok {
			writeError(w, r, http.StatusBadRequest, fmt.Errorf("no metric called %q", name))
			return
		}
		got, err := s.history.Query(def.ID, from, to, points)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, err)
			return
		}
		got.Label = t(r, got.Label)
		series = append(series, got)
	}

	sep, comma := ';', true
	if q.Get("decimal") == "point" {
		sep, comma = ',', false
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q",
		fmt.Sprintf("freeway-%s-%.0ft.csv", to.Format("2006-01-02-1504"), hours)))
	writeCSV(w, series, sep, comma)
}

// bom is the UTF-8 byte-order mark, written as an escape rather than as the
// character itself: a literal one in the source is a compile error, and in a
// string literal it is invisible to whoever reads it next.
const bom = "\ufeff"

// writeCSV lays the series out as rows.
//
// A point is an average with the minimum and maximum it was averaged from. At
// the fine tier all three are the same number; at the coarse ones the spread is
// most of what the data says, and an export that quietly dropped it would be
// smoother than the house ever was. So the extra two columns appear for a
// series that has a spread, and not for one that does not.
func writeCSV(w io.Writer, series []*store.Series, sep rune, comma bool) {
	// A byte-order mark, because Excel otherwise reads a UTF-8 file as the
	// local code page and every Norwegian vowel comes out wrong.
	io.WriteString(w, bom)

	spread := make([]bool, len(series))
	byTime := make([]map[int64]store.Point, len(series))
	seen := map[int64]bool{}
	var stamps []int64
	for i, s := range series {
		byTime[i] = make(map[int64]store.Point, len(s.Points))
		for _, p := range s.Points {
			t := p.Time.Unix()
			byTime[i][t] = p
			if p.Min != p.Max {
				spread[i] = true
			}
			if !seen[t] {
				seen[t] = true
				stamps = append(stamps, t)
			}
		}
	}
	sort.Slice(stamps, func(i, j int) bool { return stamps[i] < stamps[j] })

	var b strings.Builder
	field := func(text string) {
		if b.Len() > 0 {
			b.WriteRune(sep)
		}
		// Quote anything that could be read as structure. The labels are ours
		// and contain neither, but one added later might.
		if strings.ContainsAny(text, string(sep)+"\"\n") {
			b.WriteString(`"` + strings.ReplaceAll(text, `"`, `""`) + `"`)
		} else {
			b.WriteString(text)
		}
	}
	flush := func() {
		b.WriteString("\r\n") // what a spreadsheet expects
		io.WriteString(w, b.String())
		b.Reset()
	}
	number := func(v float64) string {
		text := strconv.FormatFloat(v, 'f', -1, 64)
		if comma {
			text = strings.Replace(text, ".", ",", 1)
		}
		return text
	}

	field("Tid")
	for i, s := range series {
		unit := ""
		if s.Unit != "" {
			unit = " (" + s.Unit + ")"
		}
		field(s.Label + unit)
		if spread[i] {
			field(s.Label + " min" + unit)
			field(s.Label + " maks" + unit)
		}
	}
	flush()

	for _, t := range stamps {
		// Local time without a zone, which is what a spreadsheet parses. The
		// file name carries the day it was taken.
		field(time.Unix(t, 0).Local().Format("2006-01-02 15:04:05"))
		for i := range series {
			p, ok := byTime[i][t]
			if !ok {
				field("")
				if spread[i] {
					field("")
					field("")
				}
				continue
			}
			field(number(p.Avg))
			if spread[i] {
				field(number(p.Min))
				field(number(p.Max))
			}
		}
		flush()
	}
}

// logDays is how far back the notification log reaches by default: long enough
// to cover "was there anything while we were away", short enough that the
// answer is a page rather than a year. The count on the collapsed card counts
// the same window, so the number and the list agree.
const logDays = 30

// handleNotificationLog returns what the box has reported and where it went.
//
// Behind the PIN with the rest of the system page: it says which conditions the
// box has reported and where it sent them, which is more than a passer-by in
// the kitchen needs to know.
func (s *Server) handleNotificationLog(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		writeError(w, r, http.StatusNotFound, i18n.Errf("error.history.off"))
		return
	}
	q := r.URL.Query()

	days := logDays
	if v, err := strconv.Atoi(q.Get("days")); err == nil && v > 0 && v <= 366 {
		days = v
	}
	limit := 100
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 && v <= 1000 {
		limit = v
	}

	events, err := s.history.Events(time.Now().AddDate(0, 0, -days), limit)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if events == nil {
		events = []store.Event{}
	}
	// Rows written before the severity became a stable machine word carry the
	// Norwegian it used to be. Put right here, so the page and anything else
	// reading this sees one vocabulary.
	for i := range events {
		switch events[i].Severity {
		case "kritisk":
			events[i].Severity = "critical"
		case "advarsel":
			events[i].Severity = "warning"
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"events":         events,
		"days":           days,
		"retention_days": int(store.EventRetention.Hours() / 24),
	})
}
