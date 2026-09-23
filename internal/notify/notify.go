// Package notify sends word when something happens that somebody would want to
// know about.
//
// The hard part is not sending; it is not sending. A dirty filter alarm stays
// on for weeks, and a unit that has lost power is unreachable for as long as it
// takes somebody to notice. Either would produce a message every poll unless
// something remembers what has already been said.
package notify

import (
	"context"
	"freewaypi/internal/config"
	"freewaypi/internal/i18n"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Severity orders events so a channel can be told to keep quiet about the small
// ones.
type Severity int

// The three levels. Critical is reserved for what stops the unit or leaves the
// house without ventilation; everything else that merely wants attention is a
// warning.
const (
	Info Severity = iota
	Warning
	Critical
)

// String is the severity as a stable word for machines — the "severity" field
// a webhook receives, which somebody's automation matches on. It is not
// translated, because a flow that tests for "critical" should not stop working
// when the owner switches the interface to another language.
func (s Severity) String() string {
	switch s {
	case Critical:
		return "critical"
	case Warning:
		return "warning"
	default:
		return "info"
	}
}

// ParseSeverity reads a severity from configuration.
func ParseSeverity(s string) Severity {
	switch s {
	case "critical", "kritisk":
		return Critical
	case "info":
		return Info
	default:
		return Warning
	}
}

// Event is something worth telling somebody about.
type Event struct {
	// Key identifies the condition rather than the moment. The same key is not
	// sent twice while the condition lasts, which is what stops a dirty filter
	// from sending a message every ten seconds for a fortnight.
	Key      string    `json:"key"`
	Kind     string    `json:"kind"`
	Severity Severity  `json:"-"`
	Level    string    `json:"severity"`
	Title    string    `json:"title"`
	Message  string    `json:"message"`
	Time     time.Time `json:"time"`
	// Unit is what this installation is called, so somebody with two of them
	// can tell which sent it.
	Unit string `json:"unit"`
	// Resolved marks the message that says a condition has ended.
	Resolved bool `json:"resolved"`
}

// Channel is somewhere events can be sent.
type Channel interface {
	Name() string
	Send(ctx context.Context, e Event) error
}

// Record is what happened to one event: the event itself, whether anybody was
// actually told, and where it went or why it did not.
type Record struct {
	Event
	Delivered bool
	Detail    string
}

// Journal is somewhere records are written down so they can be read back.
//
// Optional, and deliberately an interface: the notifier has no business
// knowing about a database, and a box with no history file still notifies.
type Journal interface {
	Append(r Record) error
}

// Notifier decides what to send and to whom.
type Notifier struct {
	mu       sync.Mutex
	channels []Channel
	min      Severity
	unit     string
	log      *slog.Logger
	// lang is what messages are written in. See config.Notify.Language.
	lang i18n.Lang

	// active is the set of conditions currently raised. Conditions below the
	// severity threshold are tracked too, so a persistent quiet one is still
	// only considered once, but they remember whether anybody was actually
	// told — an all-clear for something never announced is worse than silence.
	active map[string]tracked
	// journal is where events are written down, or nil.
	journal Journal
	// sent counts for the system page.
	sent, failed uint64
	lastErr      string
	lastSent     time.Time
}

// Options configures a Notifier.
type Options struct {
	Channels    []Channel
	MinSeverity Severity
	UnitName    string
	Logger      *slog.Logger
	// Language is what messages are written in; empty is the default.
	Language i18n.Lang
}

// New returns a Notifier.
func New(o Options) *Notifier {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.UnitName == "" {
		o.UnitName = "Aggregat"
	}
	if o.Language == "" {
		o.Language = i18n.Default
	}
	return &Notifier{
		channels: o.Channels, min: o.MinSeverity, unit: o.UnitName,
		log: o.Logger, active: map[string]tracked{}, lang: o.Language,
	}
}

// Lang is the language messages are written in, for whatever raises them.
func (n *Notifier) Lang() i18n.Lang {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.lang
}

// SetLanguage changes it, from the notification settings being saved.
func (n *Notifier) SetLanguage(l i18n.Lang) {
	if l == "" {
		l = i18n.Default
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.lang = l
}

// Raise reports a condition. The first call for a key sends; later calls for
// the same key while it is still raised do nothing.
func (n *Notifier) Raise(ctx context.Context, e Event) {
	e.Time = time.Now()
	e.Unit = n.unit
	e.Level = e.Severity.String()

	n.mu.Lock()
	if _, already := n.active[e.Key]; already {
		n.mu.Unlock()
		return
	}
	n.active[e.Key] = tracked{Event: e}
	n.mu.Unlock()

	notified := n.dispatch(ctx, e)

	n.mu.Lock()
	if t, still := n.active[e.Key]; still {
		t.notified = notified
		n.active[e.Key] = t
	}
	n.mu.Unlock()
}

// tracked is a raised condition and whether anybody was told about it.
type tracked struct {
	Event
	notified bool
}

// Clear reports that a condition has ended, and sends word only if its start
// was sent. Nobody wants to hear that something they were never told about is
// over.
func (n *Notifier) Clear(ctx context.Context, key string) {
	n.mu.Lock()
	raised, was := n.active[key]
	delete(n.active, key)
	n.mu.Unlock()
	// Nobody wants to hear that something they were never told about is over.
	if !was || !raised.notified {
		return
	}

	n.dispatch(ctx, Event{
		Key: key + ":resolved", Kind: raised.Kind,
		Severity: Info, Level: Info.String(),
		Title:   i18n.T(n.Lang(), "notify.resolved", raised.Title),
		Message: raised.Message,
		Time:    time.Now(), Unit: n.unit, Resolved: true,
	})
}

// Active returns the conditions currently raised, for the system page.
func (n *Notifier) Active() []Event {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]Event, 0, len(n.active))
	for _, t := range n.active {
		out = append(out, t.Event)
	}
	return out
}

// dispatch sends an event and reports whether anybody was told.
//
// A resolution ignores the severity threshold: it is only ever sent for a
// condition that was announced, so it cannot be noise, and an alert with no
// all-clear leaves somebody believing a problem is still running.
func (n *Notifier) dispatch(ctx context.Context, e Event) bool {
	if e.Severity < n.min && !e.Resolved {
		n.record(e, false, "notify.skipped.severity")
		return false
	}
	n.mu.Lock()
	channels := append([]Channel(nil), n.channels...)
	n.mu.Unlock()

	if len(channels) == 0 {
		// Worth a log line: a condition that nobody was told about looks
		// exactly like one that never happened.
		n.log.Info("no notification channel configured", "event", e.Title)
		n.record(e, false, "notify.skipped.nochannel")
		return false
	}

	// Every message that leaves the box gets a line. Somebody whose phone just
	// buzzed wants to know which condition did it and when, and a webhook at the
	// far end is somebody else's log, not ours.
	n.log.Info("sending notification", "key", e.Key, "kind", e.Kind,
		"severity", e.Level, "resolved", e.Resolved, "message", e.Message)

	var delivered bool
	var detail []string
	for _, c := range channels {
		// Each channel gets its own deadline: one that hangs must not stop
		// the others being told.
		send, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := c.Send(send, e)
		cancel()

		n.mu.Lock()
		if err != nil {
			n.failed++
			n.lastErr = c.Name() + ": " + err.Error()
			n.log.Warn("could not send notification", "channel", c.Name(), "event", e.Title, "err", err)
			detail = append(detail, c.Name()+": "+err.Error())
		} else {
			n.sent++
			n.lastSent = time.Now()
			n.log.Info("notification sent", "channel", c.Name(), "key", e.Key)
			delivered = true
			detail = append(detail, c.Name())
		}
		n.mu.Unlock()
	}
	n.record(e, delivered, strings.Join(detail, "; "))
	return true
}

// record writes an event down, if anywhere is listening.
//
// Every exit from dispatch calls it, including the two that send nothing. Those
// are the rows worth having: a condition raised while no channel was configured,
// or one below the severity somebody asked to hear about, leaves no trace
// anywhere else, and "why was I not told" is a question with no other answer.
//
// A journal that cannot be written to must never stop a notification, so the
// error is logged and dropped.
func (n *Notifier) record(e Event, delivered bool, detail string) {
	n.mu.Lock()
	j := n.journal
	n.mu.Unlock()
	if j == nil {
		return
	}
	if err := j.Append(Record{Event: e, Delivered: delivered, Detail: detail}); err != nil {
		n.log.Warn("could not write the notification down", "key", e.Key, "err", err)
	}
}

// SetJournal names where records are written. Nil turns it off.
func (n *Notifier) SetJournal(j Journal) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.journal = j
}

// Stats summarises delivery, for the system page.
type Stats struct {
	Sent     uint64    `json:"sent"`
	Failed   uint64    `json:"failed"`
	LastSent time.Time `json:"last_sent"`
	LastErr  string    `json:"last_error,omitempty"`
	Channels []string  `json:"channels"`
	Active   int       `json:"active_conditions"`
}

// Stats returns the counters.
func (n *Notifier) Stats() Stats {
	n.mu.Lock()
	defer n.mu.Unlock()
	names := make([]string, 0, len(n.channels))
	for _, c := range n.channels {
		names = append(names, c.Name())
	}
	return Stats{
		Sent: n.sent, Failed: n.failed, LastSent: n.lastSent,
		LastErr: n.lastErr, Channels: names, Active: len(n.active),
	}
}

// Test sends one event through every channel, so somebody configuring a webhook
// can find out whether it works without waiting for something to go wrong.
func (n *Notifier) Test(ctx context.Context) error {
	e := Event{
		Key: "test", Kind: "test", Severity: Critical, Level: Critical.String(),
		Title:   i18n.T(n.Lang(), "notify.test.title"),
		Message: i18n.T(n.Lang(), "notify.test.message"),
		Time:    time.Now(), Unit: n.unit,
	}
	n.mu.Lock()
	channels := append([]Channel(nil), n.channels...)
	n.mu.Unlock()

	if len(channels) == 0 {
		return errNoChannels
	}
	var firstErr error
	var delivered bool
	var detail []string
	for _, c := range channels {
		send, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := c.Send(send, e)
		cancel()
		if err != nil && firstErr == nil {
			firstErr = err
		}
		// A test counts like any other message. Otherwise the page still says
		// nothing has ever been sent immediately after a test that arrived,
		// which reads as a channel that does not work.
		n.mu.Lock()
		if err != nil {
			n.failed++
			n.lastErr = c.Name() + ": " + err.Error()
			n.log.Warn("could not send test notification", "channel", c.Name(), "err", err)
			detail = append(detail, c.Name()+": "+err.Error())
		} else {
			n.sent++
			n.lastSent = time.Now()
			n.log.Info("notification sent", "channel", c.Name(), "key", e.Key)
			delivered = true
			detail = append(detail, c.Name())
		}
		n.mu.Unlock()
	}
	n.record(e, delivered, strings.Join(detail, "; "))
	return firstErr
}

// SetChannels replaces where events go, while the daemon runs.
//
// Without this, saving a webhook stored it in the configuration and changed
// nothing until somebody restarted the service — and "Send test" then tested a
// notifier with no channels, which looks exactly like a webhook that does not
// work. Every other setting in this interface takes effect when it is saved;
// this one has no reason to be different.
//
// The severity threshold comes with them, because it is part of the same form
// and would otherwise need its own restart.
func (n *Notifier) SetChannels(channels []Channel, min Severity) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.channels = channels
	n.min = min
}

// ChannelsFor builds the channels a configuration describes.
//
// Here rather than in main, because saving the notification settings has to
// build the same list from the same rules — two places deciding what "enabled"
// means is how a webhook ends up saved and silent.
func ChannelsFor(c config.Notify) []Channel {
	var channels []Channel
	for _, w := range c.Webhooks {
		if !w.Enabled || w.URL == "" {
			continue
		}
		channels = append(channels, NewWebhook(w.Name, w.URL, w.Headers))
	}
	if s := c.SMTP; s.Enabled && s.Host != "" && len(s.To) > 0 {
		channels = append(channels, &SMTP{
			Host: s.Host, Port: s.Port, Username: s.Username, Password: s.Password,
			From: s.From, To: s.To, STARTTLS: s.STARTTLS,
		})
	}
	return channels
}
