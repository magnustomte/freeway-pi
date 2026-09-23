package notify

import (
	"context"
	"encoding/json"
	"errors"
	"freewaypi/internal/i18n"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type recorder struct {
	mu     sync.Mutex
	events []Event
	err    error
}

func (r *recorder) Name() string { return "test" }
func (r *recorder) Send(_ context.Context, e Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return r.err
}
func (r *recorder) all() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

func newNotifier(t *testing.T, min Severity) (*Notifier, *recorder) {
	t.Helper()
	r := &recorder{}
	return New(Options{Channels: []Channel{r}, MinSeverity: min, UnitName: "Loft",
		Logger: slog.New(slog.DiscardHandler)}), r
}

func TestAPersistingConditionIsSentOnce(t *testing.T) {
	// A dirty filter alarm stays on for weeks. Without this it would send a
	// message every poll for a fortnight.
	n, r := newNotifier(t, Warning)
	e := Event{Key: "alarm:16", Kind: "alarm", Severity: Warning, Title: "Skittent filter"}

	for i := 0; i < 50; i++ {
		n.Raise(context.Background(), e)
	}
	if got := len(r.all()); got != 1 {
		t.Fatalf("%d messages for one condition, want 1", got)
	}
}

func TestAnAllClearIgnoresTheSeverityThreshold(t *testing.T) {
	// It is only ever sent for a condition that was announced, so it cannot be
	// noise — and an alert with no all-clear leaves somebody believing the
	// problem is still running.
	n, r := newNotifier(t, Critical)
	n.Raise(context.Background(), Event{Key: "link", Severity: Critical, Title: "Aggregatet svarer ikke"})
	n.Clear(context.Background(), "link")

	all := r.all()
	if len(all) != 2 {
		t.Fatalf("%d messages, want the alert and the all-clear", len(all))
	}
	if !all[1].Resolved || all[1].Severity != Info {
		t.Fatalf("the all-clear arrived as %+v", all[1])
	}
}

func TestClearingSendsWordThatItIsOver(t *testing.T) {
	n, r := newNotifier(t, Info)
	n.Raise(context.Background(), Event{Key: "link", Severity: Critical, Title: "Aggregatet svarer ikke"})
	n.Clear(context.Background(), "link")

	all := r.all()
	if len(all) != 2 {
		t.Fatalf("%d messages, want the alert and the all-clear", len(all))
	}
	if !all[1].Resolved {
		t.Error("the second message is not marked as a resolution")
	}
	if all[1].Severity != Info {
		t.Error("an all-clear should not arrive as an alert")
	}
}

func TestClearingSomethingNobodyWasToldAboutSaysNothing(t *testing.T) {
	// Being told that a problem you never heard of is over is worse than
	// silence.
	n, r := newNotifier(t, Warning)
	n.Clear(context.Background(), "never-raised")
	if got := len(r.all()); got != 0 {
		t.Fatalf("%d messages for a condition that was never raised", got)
	}
}

func TestRaisingAgainAfterClearingSendsAgain(t *testing.T) {
	n, r := newNotifier(t, Warning)
	e := Event{Key: "alarm:16", Severity: Warning, Title: "Skittent filter"}

	n.Raise(context.Background(), e)
	n.Clear(context.Background(), "alarm:16")
	n.Raise(context.Background(), e)

	if got := len(r.all()); got != 3 {
		t.Fatalf("%d messages, want alert, all-clear, alert", got)
	}
}

func TestQuietEventsAreDroppedBelowTheThreshold(t *testing.T) {
	n, r := newNotifier(t, Critical)
	n.Raise(context.Background(), Event{Key: "a", Severity: Warning, Title: "småtteri"})
	n.Raise(context.Background(), Event{Key: "b", Severity: Critical, Title: "alvor"})

	all := r.all()
	if len(all) != 1 || all[0].Title != "alvor" {
		t.Fatalf("messages = %+v, want only the critical one", all)
	}
	// The quiet one is still tracked, so a second raise is not a second
	// message, but nobody was told — so its ending is not announced either.
	if len(n.Active()) != 2 {
		t.Errorf("%d conditions tracked, want both", len(n.Active()))
	}
	n.Clear(context.Background(), "a")
	if len(r.all()) != 1 {
		t.Error("an all-clear was sent for a condition nobody was told about")
	}
	n.Clear(context.Background(), "b")
	if len(r.all()) != 2 {
		t.Error("no all-clear for the condition that was announced")
	}
}

func TestOneFailingChannelDoesNotStopTheOthers(t *testing.T) {
	bad := &recorder{err: errors.New("nettet er nede")}
	good := &recorder{}
	n := New(Options{Channels: []Channel{bad, good}, MinSeverity: Info, Logger: slog.New(slog.DiscardHandler)})

	n.Raise(context.Background(), Event{Key: "x", Severity: Critical, Title: "noe skjedde"})

	if len(good.all()) != 1 {
		t.Fatal("a working channel was skipped because another failed")
	}
	st := n.Stats()
	if st.Sent != 1 || st.Failed != 1 {
		t.Fatalf("stats = %+v, want one of each", st)
	}
	if st.LastErr == "" {
		t.Error("the failure was not recorded for the system page")
	}
}

func TestTheUnitIsNamedInEveryMessage(t *testing.T) {
	// Somebody with two of these has to be able to tell which one sent it.
	n, r := newNotifier(t, Info)
	n.Raise(context.Background(), Event{Key: "x", Severity: Warning, Title: "noe"})
	if r.all()[0].Unit != "Loft" {
		t.Fatalf("unit = %q, want the configured name", r.all()[0].Unit)
	}
}

func TestWebhookPostsTheEventAsJSON(t *testing.T) {
	var got Event
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("X-Token")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	h := NewWebhook("Homey", srv.URL, map[string]string{"X-Token": "hemmelig"})
	err := h.Send(context.Background(), Event{
		Key: "alarm:3", Kind: "alarm", Level: "kritisk",
		Title: "Brannfare", Message: "Het tilluft", Unit: "Loft",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Brannfare" || got.Unit != "Loft" || got.Level != "kritisk" {
		t.Fatalf("received %+v", got)
	}
	if auth != "hemmelig" {
		t.Errorf("custom header = %q, want it passed through", auth)
	}
}

func TestWebhookReportsWhatTheServerSaid(t *testing.T) {
	// "the webhook failed" is no use when the server explained why.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("ugyldig token"))
	}))
	defer srv.Close()

	err := NewWebhook("", srv.URL, nil).Send(context.Background(), Event{Title: "x"})
	if err == nil {
		t.Fatal("a 403 was treated as success")
	}
	if !contains(err.Error(), "ugyldig token") {
		t.Errorf("error does not quote the server: %v", err)
	}
}

func TestTestSendingNeedsAChannel(t *testing.T) {
	n := New(Options{Logger: slog.New(slog.DiscardHandler)})
	if err := n.Test(context.Background()); err == nil {
		t.Fatal("a test with no channels reported success")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// journal collects records, standing in for the history file.
type journal struct {
	mu      sync.Mutex
	records []Record
}

func (j *journal) Append(r Record) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.records = append(j.records, r)
	return nil
}
func (j *journal) all() []Record {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]Record(nil), j.records...)
}

// TestTheJournalKeepsWhatWasNotSent: a condition below the chosen severity, or
// one raised with nowhere to send it, used to leave no trace at all — and
// "why was I not told" had no answer anywhere on the box.
func TestTheJournalKeepsWhatWasNotSent(t *testing.T) {
	for _, c := range []struct {
		why      string
		channels []Channel
		min      Severity
		want     string
	}{
		{"below the threshold", []Channel{&recorder{}}, Critical, "notify.skipped.severity"},
		{"nowhere to send it", nil, Info, "notify.skipped.nochannel"},
	} {
		n := New(Options{Channels: c.channels, MinSeverity: c.min, UnitName: "Loft",
			Logger: slog.New(slog.DiscardHandler)})
		j := &journal{}
		n.SetJournal(j)

		n.Raise(context.Background(), Event{
			Key: "alarm:16", Kind: "alarm", Severity: Warning, Title: "Skittent filter"})

		got := j.all()
		if len(got) != 1 {
			t.Fatalf("%s: %d records, want 1", c.why, len(got))
		}
		if got[0].Delivered {
			t.Errorf("%s: recorded as delivered", c.why)
		}
		if got[0].Detail != c.want {
			t.Errorf("%s: detail = %q, want %q", c.why, got[0].Detail, c.want)
		}
	}
}

// TestTheJournalNamesWhereItWent: the row has to say which channel accepted it,
// because "sent" with two channels configured and one broken is not an answer.
func TestTheJournalNamesWhereItWent(t *testing.T) {
	n, _ := newNotifier(t, Warning)
	j := &journal{}
	n.SetJournal(j)

	n.Raise(context.Background(), Event{
		Key: "alarm:16", Kind: "alarm", Severity: Warning, Title: "Skittent filter"})

	got := j.all()
	if len(got) != 1 {
		t.Fatalf("%d records, want 1", len(got))
	}
	if !got[0].Delivered {
		t.Fatal("recorded as not delivered")
	}
	if got[0].Detail != "test" {
		t.Errorf("detail = %q, want the channel name", got[0].Detail)
	}
	if got[0].Key != "alarm:16" || got[0].Title != "Skittent filter" {
		t.Errorf("the event did not survive: %+v", got[0].Event)
	}
}

// TestAFailingJournalDoesNotStopANotification: the point of the box is the
// message, not the bookkeeping.
func TestAFailingJournalDoesNotStopANotification(t *testing.T) {
	n, r := newNotifier(t, Warning)
	n.SetJournal(brokenJournal{})

	n.Raise(context.Background(), Event{
		Key: "alarm:16", Kind: "alarm", Severity: Warning, Title: "Skittent filter"})

	if len(r.all()) != 1 {
		t.Fatalf("%d sent, want 1", len(r.all()))
	}
}

type brokenJournal struct{}

func (brokenJournal) Append(Record) error { return errors.New("disk full") }

// TestMessagesAreWrittenInTheChosenLanguage: a webhook has no reader with a
// browser to ask, so messages take the language the settings were saved in.
// They were Norwegian whatever the interface was set to.
func TestMessagesAreWrittenInTheChosenLanguage(t *testing.T) {
	r := &recorder{}
	n := New(Options{Channels: []Channel{r}, MinSeverity: Info, Language: i18n.EN,
		Logger: slog.New(slog.DiscardHandler)})

	if err := n.Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	n.Raise(context.Background(), Event{Key: "k", Kind: "link", Severity: Critical, Title: "The unit is not answering"})
	n.Clear(context.Background(), "k")

	got := r.all()
	if len(got) != 3 {
		t.Fatalf("%d messages, want 3", len(got))
	}
	if got[0].Title != "Test notification from Freeway Pi" {
		t.Errorf("test title = %q", got[0].Title)
	}
	if !strings.HasPrefix(got[2].Title, "Resolved: ") {
		t.Errorf("resolution title = %q", got[2].Title)
	}

	// Switching language takes effect for the next message, without a restart.
	n.SetLanguage(i18n.NO)
	_ = n.Test(context.Background())
	if last := r.all()[3]; last.Title != "Testvarsel fra Freeway Pi" {
		t.Errorf("after switching to Norwegian: %q", last.Title)
	}
}

// TestSeverityIsAStableWord: the severity field is what somebody's automation
// matches on, so it must not change with the language.
func TestSeverityIsAStableWord(t *testing.T) {
	for s, want := range map[Severity]string{Critical: "critical", Warning: "warning", Info: "info"} {
		if got := s.String(); got != want {
			t.Errorf("%d renders as %q, want %q", s, got, want)
		}
	}
	// Configuration written in Norwegian is still read.
	if ParseSeverity("kritisk") != Critical {
		t.Error("the Norwegian word for critical is no longer understood")
	}
}
