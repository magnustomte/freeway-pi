package web

import (
	"strings"
	"testing"
	"time"

	"freewaypi/internal/store"
)

// TestWriteCSV pins the shape of the export, including the two things a
// spreadsheet quietly gets wrong: the separator and the decimal mark.
func TestWriteCSV(t *testing.T) {
	when := time.Date(2026, 1, 2, 3, 0, 0, 0, time.Local)
	series := []*store.Series{{
		Label: "Tilluft", Unit: "°C",
		Points: []store.Point{
			// No spread: the fine tier stores one reading three times over.
			{Time: when, Avg: 1.5, Min: 1.5, Max: 1.5},
			{Time: when.Add(time.Minute), Avg: 13.8, Min: 13.8, Max: 13.8},
		},
	}, {
		Label: "Vifte", Unit: "%",
		Points: []store.Point{
			// A spread, so this one gets its minimum and maximum too.
			{Time: when, Avg: 62.5, Min: 55, Max: 70},
			{Time: when.Add(time.Minute), Avg: 63, Min: 60, Max: 70},
		},
	}}

	var b strings.Builder
	writeCSV(&b, series, ';', true)
	out := b.String()

	if !strings.HasPrefix(out, bom) {
		t.Error("no byte-order mark, so Excel will mangle the ° and the å")
	}
	lines := strings.Split(strings.TrimSuffix(strings.TrimPrefix(out, bom), "\r\n"), "\r\n")
	if len(lines) != 3 {
		t.Fatalf("%d lines, want a header and two rows: %q", len(lines), lines)
	}
	wantHead := "Tid;Tilluft (°C);Vifte (%);Vifte min (%);Vifte maks (%)"
	if lines[0] != wantHead {
		t.Errorf("header\n got %q\nwant %q", lines[0], wantHead)
	}
	wantRow := "2026-01-02 03:00:00;1,5;62,5;55;70"
	if lines[1] != wantRow {
		t.Errorf("first row\n got %q\nwant %q", lines[1], wantRow)
	}
}

// TestWriteCSVInternational is the other form, for anything that is not Excel.
func TestWriteCSVInternational(t *testing.T) {
	when := time.Date(2026, 1, 2, 3, 0, 0, 0, time.Local)
	series := []*store.Series{{
		Label: "Tilluft", Unit: "°C",
		Points: []store.Point{{Time: when, Avg: 1.5, Min: 1.5, Max: 1.5}},
	}}
	var b strings.Builder
	writeCSV(&b, series, ',', false)
	if !strings.Contains(b.String(), "03:00:00,1.5") {
		t.Errorf("wrong separator or decimal mark: %q", b.String())
	}
}

// TestWriteCSVFillsGaps: the instants are a union across metrics, so a series
// that is missing one leaves an empty cell rather than shifting every value
// after it into the wrong row.
func TestWriteCSVFillsGaps(t *testing.T) {
	t0 := time.Date(2026, 9, 19, 14, 0, 0, 0, time.Local)
	t1 := t0.Add(time.Minute)
	series := []*store.Series{
		{Label: "A", Points: []store.Point{{Time: t0, Avg: 1, Min: 1, Max: 1}}},
		{Label: "B", Points: []store.Point{{Time: t1, Avg: 2, Min: 2, Max: 2}}},
	}
	var b strings.Builder
	writeCSV(&b, series, ';', true)
	lines := strings.Split(strings.TrimSpace(strings.TrimPrefix(b.String(), bom)), "\r\n")
	if len(lines) != 3 {
		t.Fatalf("%d lines, want a header and two rows: %q", len(lines), lines)
	}
	if !strings.HasSuffix(lines[1], "1;") {
		t.Errorf("row for the first instant = %q, want B empty", lines[1])
	}
	if !strings.HasSuffix(lines[2], ";2") {
		t.Errorf("row for the second instant = %q, want A empty", lines[2])
	}
}
