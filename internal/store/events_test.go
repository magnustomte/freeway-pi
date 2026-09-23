package store

import (
	"testing"
	"time"
)

func TestEventsComeBackNewestFirst(t *testing.T) {
	s := open(t)
	base := time.Now().Add(-time.Hour).Truncate(time.Second)

	for i, title := range []string{"eldst", "midterst", "nyest"} {
		e := Event{
			Time: base.Add(time.Duration(i) * time.Minute),
			Key:  "k", Kind: "test", Severity: "advarsel",
			Title: title, Message: "m", Delivered: true, Detail: "Homey",
		}
		if err := s.AppendEvent(e); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.Events(base.Add(-time.Minute), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("%d events, want 3", len(got))
	}
	// The question is almost always what just happened.
	if got[0].Title != "nyest" || got[2].Title != "eldst" {
		t.Fatalf("order = %q .. %q, want nyest .. eldst", got[0].Title, got[2].Title)
	}
	if !got[0].Delivered || got[0].Detail != "Homey" {
		t.Fatalf("delivery not kept: %+v", got[0])
	}
}

// TestUndeliveredEventsSurvive: a condition nobody was told about is the one
// worth being able to look up. If the row only recorded what went out, the
// table would answer every question except the one that gets asked.
func TestUndeliveredEventsSurvive(t *testing.T) {
	s := open(t)
	now := time.Now().Truncate(time.Second)
	if err := s.AppendEvent(Event{
		Time: now, Key: "link:down", Kind: "link_down", Severity: "advarsel",
		Title: "Borte", Message: "", Delivered: false,
		Detail: "notify.skipped.nochannel",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Events(now.Add(-time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("%d events, want 1", len(got))
	}
	if got[0].Delivered {
		t.Fatal("recorded as delivered")
	}
	if got[0].Detail != "notify.skipped.nochannel" {
		t.Fatalf("detail = %q, want the reason it was not sent", got[0].Detail)
	}
}

func TestOldEventsArePruned(t *testing.T) {
	s := open(t)
	now := time.Now().Truncate(time.Second)
	old := now.Add(-EventRetention - 24*time.Hour)

	for _, at := range []time.Time{old, now} {
		if err := s.AppendEvent(Event{Time: at, Key: "k", Kind: "test",
			Severity: "info", Title: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Prune(now); err != nil {
		t.Fatal(err)
	}
	got, err := s.Events(old.Add(-time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("%d events after pruning, want 1", len(got))
	}
	if !got[0].Time.Equal(now) {
		t.Fatalf("kept the wrong one: %s", got[0].Time)
	}
}

// TestEventsRespectTheLimit: the page asks for a screenful, not a year.
func TestEventsRespectTheLimit(t *testing.T) {
	s := open(t)
	now := time.Now().Truncate(time.Second)
	for i := 0; i < 10; i++ {
		if err := s.AppendEvent(Event{Time: now.Add(time.Duration(i) * time.Second),
			Key: "k", Kind: "test", Severity: "info", Title: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Events(now.Add(-time.Minute), 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("%d events, want 4", len(got))
	}
}
