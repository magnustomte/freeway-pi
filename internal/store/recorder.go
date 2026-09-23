package store

import (
	"log/slog"
	"sync"
	"time"

	"freewaypi/internal/eda"
)

// Recorder writes each new snapshot into the store, and keeps the coarser
// tiers and the retention up to date.
type Recorder struct {
	store *Store
	state func() (*eda.State, error)
	// host reports Freeway Pi's own measurements, which have nothing to do
	// with whether the unit is answering.
	host func() []Sample
	log  *slog.Logger

	interval time.Duration
	// housekeeping is how often to roll up and prune. Every minute: the
	// one-minute tier then fills as data arrives rather than in bursts, and a
	// roll-up over a minute of rows is too small to notice.
	housekeeping time.Duration

	stop chan struct{}
	done chan struct{}
	once sync.Once

	mu       sync.Mutex
	written  uint64
	skipped  uint64
	lastErr  error
	lastSeen time.Time
	lastHost time.Time
}

// RecorderOptions configures a Recorder.
type RecorderOptions struct {
	Interval     time.Duration
	Housekeeping time.Duration
	Logger       *slog.Logger
	// Host is Freeway Pi's own health, recorded alongside the unit's readings
	// and, when the unit has gone quiet, instead of them.
	Host func() []Sample
}

// NewRecorder starts recording. Call Close to stop.
func NewRecorder(s *Store, state func() (*eda.State, error), o RecorderOptions) *Recorder {
	if o.Interval <= 0 {
		o.Interval = 5 * time.Second
	}
	if o.Housekeeping <= 0 {
		o.Housekeeping = time.Minute
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	r := &Recorder{
		store: s, state: state, host: o.Host, log: o.Logger,
		interval: o.Interval, housekeeping: o.Housekeeping,
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	go r.loop()
	return r
}

func (r *Recorder) loop() {
	defer close(r.done)
	sample := time.NewTicker(r.interval)
	defer sample.Stop()
	keep := time.NewTicker(r.housekeeping)
	defer keep.Stop()

	for {
		select {
		case <-r.stop:
			return
		case <-sample.C:
			r.record()
		case <-keep.C:
			r.housekeep()
		}
	}
}

// record stores the current state, unless it is one already stored.
//
// The poller and this ticker run at their own rates, so the same snapshot is
// usually seen more than once. Writing it again would be harmless but wasteful
// on a card that wears out.
func (r *Recorder) record() {
	st, err := r.state()
	fresh := err == nil
	seen := true
	if fresh {
		r.mu.Lock()
		seen = st.Taken.Equal(r.lastSeen)
		if !seen {
			r.lastSeen = st.Taken
		}
		r.mu.Unlock()
	}
	if !fresh || seen {
		// The unit had nothing new to say, which is exactly when Freeway Pi's
		// own health is worth having: a supply that sags hard enough takes the
		// unit's answers with it, and a gap in the record of why is a gap over
		// the moment that explains everything else.
		r.recordHost()
		return
	}

	samples := SamplesFrom(st)
	if len(samples) == 0 {
		r.mu.Lock()
		r.skipped++
		r.mu.Unlock()
		return
	}
	if r.host != nil {
		samples = append(samples, r.host()...)
		r.mu.Lock()
		r.lastHost = time.Now()
		r.mu.Unlock()
	}
	if err := r.store.Record(st.Taken, samples); err != nil {
		r.mu.Lock()
		r.lastErr = err
		r.mu.Unlock()
		r.log.Warn("could not store readings", "err", err)
		return
	}
	r.mu.Lock()
	r.written++
	r.mu.Unlock()
}

// recordHost writes Freeway Pi's own measurements on their own.
//
// Rate limited to the finest tier's bucket: the ticker runs faster than the
// unit is polled, and two writes into one ten second bucket is two commits
// where one would do.
func (r *Recorder) recordHost() {
	if r.host == nil {
		return
	}
	now := time.Now()
	r.mu.Lock()
	due := now.Sub(r.lastHost) >= Tiers[0].Bucket
	if due {
		r.lastHost = now
	}
	r.mu.Unlock()
	if !due {
		return
	}
	samples := r.host()
	if len(samples) == 0 {
		return
	}
	if err := r.store.Record(now, samples); err != nil {
		r.mu.Lock()
		r.lastErr = err
		r.mu.Unlock()
		r.log.Warn("could not store Freeway Pi's own readings", "err", err)
	}
}

func (r *Recorder) housekeep() {
	now := time.Now()
	if err := r.store.Rollup(now); err != nil {
		r.log.Warn("rolling up history failed", "err", err)
		r.mu.Lock()
		r.lastErr = err
		r.mu.Unlock()
		return
	}
	if err := r.store.Prune(now); err != nil {
		r.log.Warn("pruning history failed", "err", err)
		r.mu.Lock()
		r.lastErr = err
		r.mu.Unlock()
	}
}

// RecorderStats reports how recording is going, for the system page.
type RecorderStats struct {
	Written uint64 `json:"written"`
	// Skipped counts states that were not worth storing, which is almost
	// always a stale one during an outage.
	Skipped uint64 `json:"skipped"`
	LastErr string `json:"last_error,omitempty"`
}

// Stats returns the recorder's counters.
func (r *Recorder) Stats() RecorderStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := RecorderStats{Written: r.written, Skipped: r.skipped}
	if r.lastErr != nil {
		s.LastErr = r.lastErr.Error()
	}
	return s
}

// Close stops recording.
func (r *Recorder) Close() {
	r.once.Do(func() { close(r.stop) })
	<-r.done
}
