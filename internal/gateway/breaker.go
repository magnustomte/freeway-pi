package gateway

import (
	"sync"
	"time"
)

// Breaker stops the gateway from spending bus time on a unit that is not
// answering.
//
// This is not a refinement. The Homey app falls back to reading register by
// register when a block read fails, so a unit that has gone quiet turns one
// poll into roughly forty doomed requests, each costing three retries of a
// second. That is over two minutes of bus time a minute, every minute, and the
// background poller — the thing that would notice the unit coming back — never
// gets a turn.
//
// Open, the gateway answers at once with "gateway target device failed to
// respond". A client learns in a millisecond instead of waiting out its own
// five second timeout, and the bus stays free.
type Breaker struct {
	mu sync.Mutex

	threshold int
	cooldown  time.Duration
	now       func() time.Time

	consecutive int
	retryAt     time.Time
	opened      time.Time
	trips       uint64
}

// BreakerOptions configures a Breaker.
type BreakerOptions struct {
	// Threshold is how many consecutive failures open the breaker. Low, because
	// the client layer has already retried three times underneath each one.
	Threshold int
	// Cooldown is how long to wait before letting a single request through to
	// see whether the unit is back.
	Cooldown time.Duration
	// Now is for tests.
	Now func() time.Time
}

// NewBreaker returns a closed breaker.
func NewBreaker(o BreakerOptions) *Breaker {
	if o.Threshold <= 0 {
		o.Threshold = 3
	}
	if o.Cooldown <= 0 {
		o.Cooldown = 5 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Breaker{threshold: o.Threshold, cooldown: o.Cooldown, now: o.Now}
}

// Allow reports whether a request may go to the bus. When the breaker is open
// it lets exactly one request through per cooldown, to find out whether the
// unit has come back.
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.consecutive < b.threshold {
		return true
	}
	if b.now().Before(b.retryAt) {
		return false
	}
	// A probe is allowed through, and the next one is not due until another
	// cooldown has passed, whether this one succeeds or fails.
	b.retryAt = b.now().Add(b.cooldown)
	return true
}

// Success records that the unit answered.
func (b *Breaker) Success() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.consecutive = 0
	b.opened = time.Time{}
}

// Failure records that the unit did not answer.
func (b *Breaker) Failure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.consecutive++
	if b.consecutive == b.threshold {
		b.opened = b.now()
		b.retryAt = b.now().Add(b.cooldown)
		b.trips++
	}
}

// BreakerState describes the breaker for the system page.
type BreakerState struct {
	Open        bool      `json:"open"`
	Consecutive int       `json:"consecutive"`
	Since       time.Time `json:"since"`
	Trips       uint64    `json:"trips"`
}

// State returns the current state.
func (b *Breaker) State() BreakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return BreakerState{
		Open:        b.consecutive >= b.threshold,
		Consecutive: b.consecutive,
		Since:       b.opened,
		Trips:       b.trips,
	}
}
