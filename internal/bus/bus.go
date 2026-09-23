// Package bus owns the serial port.
//
// RS-485 allows exactly one request in flight, so every caller — the web
// interface, the Modbus TCP clients, the background poller — is funnelled
// through a single goroutine. What that goroutine picks next is the whole
// design: a person waiting for a button to do something must not sit behind a
// poll that nobody is waiting for.
package bus

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"freewaypi/internal/modbus"
)

// Priority decides who goes first when several callers are waiting.
type Priority int

const (
	// PriorityWrite is a command from the web interface. Someone is looking at
	// the screen waiting for it to take effect.
	PriorityWrite Priority = iota
	// PriorityClient is a Modbus TCP client such as the Homey app. It has its
	// own timeout and must not be starved by background work.
	PriorityClient
	// PriorityPoll is the background refresh. It can always wait, and is
	// dropped outright if it waits too long, because a newer one will follow.
	PriorityPoll

	priorityCount
)

func (p Priority) String() string {
	switch p {
	case PriorityWrite:
		return "write"
	case PriorityClient:
		return "client"
	case PriorityPoll:
		return "poll"
	default:
		return "unknown"
	}
}

// ErrClosed is returned once the bus has been shut down.
var ErrClosed = errors.New("bus: closed")

// ErrDropped is returned when a job waited longer than its caller was prepared
// to wait. It is not a fault: a poll that has been queued longer than the poll
// interval is stale by definition, and running it would waste bus time that a
// fresher one needs.
var ErrDropped = errors.New("bus: request dropped after waiting too long")

type job struct {
	priority Priority
	run      func(*modbus.Client) error
	done     chan error
	ctx      context.Context
	queued   time.Time
}

// Stats is a snapshot of how the bus is behaving, for the system page and for
// the health checks.
type Stats struct {
	Requests  [priorityCount]uint64        `json:"requests"`
	Dropped   [priorityCount]uint64        `json:"dropped"`
	Failures  [priorityCount]uint64        `json:"failures"`
	WaitTotal [priorityCount]time.Duration `json:"-"`

	// Exchanges and ExchangeErrors count individual attempts on the wire,
	// including the ones retries hide. A rising error rate here is the earliest
	// sign of a cabling or adapter problem, long before anything visibly fails.
	Exchanges      uint64 `json:"exchanges"`
	ExchangeErrors uint64 `json:"exchange_errors"`
	// RecentErrorRate is the share of the last few hundred attempts that
	// failed. The lifetime rate is no use as a health signal: a bad hour on
	// the day of installation would poison it for years, and a line that
	// starts failing today would take weeks to show up in it.
	RecentErrorRate float64 `json:"recent_error_rate"`
	// RecentSamples is how many attempts that rate is drawn from. A rate over
	// a handful of attempts is not a rate: one failure in the fifty-odd a
	// freshly started daemon has made reads as two per cent, which is enough
	// to raise an alarm about a line that is fine.
	RecentSamples int `json:"recent_samples"`
}

// EnoughSamples is how full the window has to be before the rate means
// anything.
//
// A quarter of it, which is chosen rather than picked: one failure among 64
// attempts is 1.6 % and stays under the threshold, while two is 3.1 % and does
// not. Below that a single unlucky exchange is enough to call a healthy line
// unstable — a freshly started daemon had made 56 attempts with one failure,
// and that read as 1.8 %.
//
// At roughly six attempts a poll pass and a pass every ten seconds it is about
// two minutes of evidence.
const EnoughSamples = 64

// ErrorRate is the share of single attempts on the wire that failed. Measurement
// measured about 1 in 1000 with the FTDI latency timer at 1 ms, and 3 to 5 in
// 100 with it at the default 16 ms, so this number is worth watching.
func (s Stats) ErrorRate() float64 {
	if s.Exchanges == 0 {
		return 0
	}
	return float64(s.ExchangeErrors) / float64(s.Exchanges)
}

// Bus serialises access to the unit.
type Bus struct {
	client *modbus.Client

	queues [priorityCount]chan *job
	closed chan struct{}
	once   sync.Once
	wg     sync.WaitGroup

	mu    sync.Mutex
	stats Stats
	// recent is a ring of the last attempts, true where one failed.
	recent    [256]bool
	recentPos int
	recentLen int
	recentBad int

	running atomic.Bool

	// consecutive counts failures in a row, and reopen is what to do about a
	// run of them. Only the bus goroutine touches any of these.
	consecutive int
	// reopens counts reopens since the bus last answered, and reopenFailed is
	// whether the last attempt failed. Together they turn a fault into three
	// lines — it broke, the port came back, the bus came back — instead of one
	// line every forty seconds for as long as the fault lasts.
	reopens      int
	reopenFailed bool
	reopen       func() error
	logger       *slog.Logger
}

// log is the bus's only reporting. A nil logger is fine and means silence,
// which the tests want.
func (b *Bus) log(msg string, err error) {
	if b.logger == nil {
		return
	}
	if err != nil {
		b.logger.Warn(msg, "err", err)
		return
	}
	b.logger.Info(msg)
}

// Options configures a Bus.
type Options struct {
	// Depth is how many jobs may wait at each priority. Small on purpose: a
	// long queue on a bus this slow means latency nobody wants, and it is
	// better to refuse than to promise.
	Depth int
	// Reopen brings the serial port back after a run of failures. Optional:
	// without it the bus keeps trying the descriptor it has, which is right
	// for a test and wrong for an adapter somebody unplugged.
	Reopen func() error
	Logger *slog.Logger
}

// New starts the bus. Call Close to stop it.
func New(client *modbus.Client, opts Options) *Bus {
	if opts.Depth <= 0 {
		opts.Depth = 16
	}
	b := &Bus{client: client, closed: make(chan struct{}),
		reopen: opts.Reopen, logger: opts.Logger}
	for i := range b.queues {
		b.queues[i] = make(chan *job, opts.Depth)
	}

	// Count every attempt, so retries do not hide a degrading line.
	prev := client.OnExchange
	client.OnExchange = func(ex modbus.Exchange) {
		b.mu.Lock()
		b.stats.Exchanges++
		failed := ex.Err != nil
		if failed {
			b.stats.ExchangeErrors++
		}
		if b.recentLen == len(b.recent) && b.recent[b.recentPos] {
			b.recentBad--
		}
		if failed {
			b.recentBad++
		}
		b.recent[b.recentPos] = failed
		b.recentPos = (b.recentPos + 1) % len(b.recent)
		if b.recentLen < len(b.recent) {
			b.recentLen++
		}
		b.mu.Unlock()
		if prev != nil {
			prev(ex)
		}
	}

	b.wg.Add(1)
	go b.loop()
	return b
}

// Do runs fn with exclusive use of the port and returns its error.
//
// fn must not block on anything but the port: it holds the only thing every
// other caller is waiting for.
func (b *Bus) Do(ctx context.Context, p Priority, fn func(*modbus.Client) error) error {
	if p < 0 || p >= priorityCount {
		p = PriorityClient
	}
	j := &job{
		priority: p,
		run:      fn,
		done:     make(chan error, 1),
		ctx:      ctx,
		queued:   time.Now(),
	}

	select {
	case <-b.closed:
		return ErrClosed
	default:
	}

	select {
	case b.queues[p] <- j:
	case <-ctx.Done():
		b.record(p, 0, false, true)
		return ctx.Err()
	case <-b.closed:
		return ErrClosed
	}

	select {
	case err := <-j.done:
		return err
	case <-ctx.Done():
		// The job may still run; its result is discarded. Cancelling the
		// caller cannot cancel a frame already on the wire.
		return ctx.Err()
	}
}

func (b *Bus) loop() {
	defer b.wg.Done()
	b.running.Store(true)
	defer b.running.Store(false)

	for {
		j := b.next()
		if j == nil {
			return
		}
		b.serve(j)
	}
}

// next returns the highest-priority job available, blocking if none is. The
// nested selects are how Go expresses a priority order: a select with several
// ready cases picks one at random, so the only way to prefer a channel is to
// try it alone first.
func (b *Bus) next() *job {
	select {
	case j := <-b.queues[PriorityWrite]:
		return j
	case <-b.closed:
		return nil
	default:
	}
	select {
	case j := <-b.queues[PriorityWrite]:
		return j
	case j := <-b.queues[PriorityClient]:
		return j
	case <-b.closed:
		return nil
	default:
	}
	select {
	case j := <-b.queues[PriorityWrite]:
		return j
	case j := <-b.queues[PriorityClient]:
		return j
	case j := <-b.queues[PriorityPoll]:
		return j
	case <-b.closed:
		return nil
	}
}

func (b *Bus) serve(j *job) {
	wait := time.Since(j.queued)

	// A caller that has already given up gets no bus time. This is what keeps
	// a burst of stale polls from delaying live requests.
	if err := j.ctx.Err(); err != nil {
		b.record(j.priority, wait, false, true)
		j.done <- ErrDropped
		return
	}

	err := j.run(b.client)
	b.record(j.priority, wait, err != nil, false)
	j.done <- err

	if err == nil {
		// Only worth saying if something was said about it breaking.
		if b.reopens > 0 {
			b.log("the bus is answering again", nil)
		}
		b.consecutive, b.reopens, b.reopenFailed = 0, 0, false
		return
	}
	b.consecutive++
	if b.reopen == nil || b.consecutive < ReopenAfter {
		return
	}
	// Long past the point where this is a noisy line. Either the unit is off,
	// in which case reopening costs one syscall and changes nothing, or the
	// adapter was unplugged and put back, in which case this is the only thing
	// that brings it back without somebody logging in.
	b.consecutive = 0
	b.reopens++

	if err := b.reopen(); err != nil {
		// The adapter is gone. Said once: it will still be gone in twelve
		// failures' time, and a journal that repeats itself every forty
		// seconds for a weekend buries the line that mattered.
		if !b.reopenFailed {
			b.log("could not reopen the serial port", err)
			b.reopenFailed = true
		}
		return
	}
	// Reopening worked, which is not the same as the bus working — with the
	// wires out of the adapter this succeeds every time and fixes nothing.
	// So this claims only what happened, and the recovery line above is what
	// reports the bus actually answering.
	if b.reopenFailed || b.reopens == 1 {
		b.log("reopened the serial port", nil)
		b.reopenFailed = false
	}
}

// ReopenAfter is how many failures in a row bring the port back.
//
// Enough that an unhappy line does not churn: at three attempts per request
// and a second of timeout each, this is the better part of a minute of nothing
// working at all.
const ReopenAfter = 12

func (b *Bus) record(p Priority, wait time.Duration, failed, dropped bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if dropped {
		b.stats.Dropped[p]++
		return
	}
	b.stats.Requests[p]++
	b.stats.WaitTotal[p] += wait
	if failed {
		b.stats.Failures[p]++
	}
}

// Stats returns a copy of the current counters.
func (b *Bus) Stats() Stats {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.stats
	s.RecentSamples = b.recentLen
	if b.recentLen > 0 {
		s.RecentErrorRate = float64(b.recentBad) / float64(b.recentLen)
	}
	return s
}

// Close stops the bus. Jobs already queued are abandoned; a job on the wire is
// allowed to finish, because a half-written frame would leave the unit waiting.
func (b *Bus) Close() {
	b.once.Do(func() { close(b.closed) })
	b.wg.Wait()
}
