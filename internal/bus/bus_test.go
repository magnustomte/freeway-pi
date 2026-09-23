package bus

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"freewaypi/internal/modbus"
)

// stubTransport satisfies modbus.Transport without touching hardware. The bus
// tests drive the jobs directly, so the client is never actually used.
type stubTransport struct{}

func (stubTransport) Drain() error                     { return nil }
func (stubTransport) Write([]byte) error               { return nil }
func (stubTransport) ReadFull([]byte, time.Time) error { return nil }

func newTestBus(t *testing.T) *Bus {
	t.Helper()
	b := New(modbus.NewClient(stubTransport{}, 1), Options{Depth: 8})
	t.Cleanup(b.Close)
	return b
}

// occupy blocks the bus loop and returns a function that releases it, so a test
// can fill the queues while nothing is being served.
func occupy(t *testing.T, b *Bus) func() {
	t.Helper()
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = b.Do(context.Background(), PriorityClient, func(*modbus.Client) error {
			close(started)
			<-release
			return nil
		})
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("the bus never started the blocking job")
	}
	return func() {
		close(release)
		<-done
	}
}

// waitForQueues waits until each queue holds the expected number of jobs, so
// ordering assertions do not race with goroutine scheduling.
func waitForQueues(t *testing.T, b *Bus, want [priorityCount]int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		ok := true
		for i := range want {
			if len(b.queues[i]) != want[i] {
				ok = false
			}
		}
		if ok {
			return
		}
		if time.Now().After(deadline) {
			var got [priorityCount]int
			for i := range got {
				got[i] = len(b.queues[i])
			}
			t.Fatalf("queues settled at %v, want %v", got, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestBusRunsJobsAndReturnsTheirError(t *testing.T) {
	b := newTestBus(t)
	sentinel := errors.New("the unit said no")

	if err := b.Do(context.Background(), PriorityWrite, func(*modbus.Client) error { return nil }); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if err := b.Do(context.Background(), PriorityWrite, func(*modbus.Client) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("Do returned %v, want the job's own error", err)
	}
}

func TestBusServesHighestPriorityFirst(t *testing.T) {
	b := newTestBus(t)
	release := occupy(t, b)

	var mu sync.Mutex
	var order []string
	record := func(name string) func(*modbus.Client) error {
		return func(*modbus.Client) error {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return nil
		}
	}

	var wg sync.WaitGroup
	// Queued deliberately in the wrong order: poll first, write last.
	for _, c := range []struct {
		p    Priority
		name string
	}{
		{PriorityPoll, "poll"},
		{PriorityClient, "client"},
		{PriorityWrite, "write"},
	} {
		wg.Add(1)
		go func(p Priority, name string) {
			defer wg.Done()
			_ = b.Do(context.Background(), p, record(name))
		}(c.p, c.name)
	}
	waitForQueues(t, b, [priorityCount]int{1, 1, 1})

	release()
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	want := []string{"write", "client", "poll"}
	if len(order) != len(want) {
		t.Fatalf("ran %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("ran %v, want %v", order, want)
		}
	}
}

func TestBusDropsJobsWhoseCallerGaveUp(t *testing.T) {
	b := newTestBus(t)
	release := occupy(t, b)

	ctx, cancel := context.WithCancel(context.Background())
	var ran bool
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = b.Do(ctx, PriorityPoll, func(*modbus.Client) error {
			mu.Lock()
			ran = true
			mu.Unlock()
			return nil
		})
	}()
	waitForQueues(t, b, [priorityCount]int{0, 0, 1})

	// The caller loses patience before the bus ever gets to the job.
	cancel()
	release()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if ran {
		t.Fatal("a job whose caller had given up still used bus time")
	}
	if got := b.Stats().Dropped[PriorityPoll]; got != 1 {
		t.Fatalf("dropped count = %d, want 1", got)
	}
}

func TestBusCountsRequestsFailuresAndExchanges(t *testing.T) {
	b := newTestBus(t)

	_ = b.Do(context.Background(), PriorityWrite, func(*modbus.Client) error { return nil })
	_ = b.Do(context.Background(), PriorityWrite, func(*modbus.Client) error { return errors.New("nope") })
	_ = b.Do(context.Background(), PriorityClient, func(*modbus.Client) error { return nil })

	s := b.Stats()
	if s.Requests[PriorityWrite] != 2 {
		t.Errorf("write requests = %d, want 2", s.Requests[PriorityWrite])
	}
	if s.Failures[PriorityWrite] != 1 {
		t.Errorf("write failures = %d, want 1", s.Failures[PriorityWrite])
	}
	if s.Requests[PriorityClient] != 1 {
		t.Errorf("client requests = %d, want 1", s.Requests[PriorityClient])
	}
	if s.ErrorRate() != 0 {
		t.Errorf("error rate = %v with no exchanges, want 0", s.ErrorRate())
	}
}

func TestBusCountsWireAttemptsThroughTheClientHook(t *testing.T) {
	// Retries hide failures from callers. The bus counts every attempt so a
	// line that is degrading shows up before anything visibly breaks.
	client := modbus.NewClient(stubTransport{}, 1)
	b := New(client, Options{})
	t.Cleanup(b.Close)

	client.OnExchange(modbus.Exchange{})
	client.OnExchange(modbus.Exchange{Err: errors.New("crc")})

	s := b.Stats()
	if s.Exchanges != 2 || s.ExchangeErrors != 1 {
		t.Fatalf("exchanges = %d with %d errors, want 2 and 1", s.Exchanges, s.ExchangeErrors)
	}
	if got := s.ErrorRate(); got != 0.5 {
		t.Fatalf("error rate = %v, want 0.5", got)
	}
}

func TestBusPreservesAnExistingExchangeHook(t *testing.T) {
	client := modbus.NewClient(stubTransport{}, 1)
	var seen int
	client.OnExchange = func(modbus.Exchange) { seen++ }

	b := New(client, Options{})
	t.Cleanup(b.Close)
	client.OnExchange(modbus.Exchange{})

	if seen != 1 {
		t.Fatal("wrapping the hook lost the caller's own")
	}
	if b.Stats().Exchanges != 1 {
		t.Fatal("wrapping the hook lost the bus's counting")
	}
}

func TestBusRefusesWorkOnceClosed(t *testing.T) {
	b := New(modbus.NewClient(stubTransport{}, 1), Options{})
	b.Close()

	if err := b.Do(context.Background(), PriorityWrite, func(*modbus.Client) error { return nil }); !errors.Is(err, ErrClosed) {
		t.Fatalf("Do after Close returned %v, want ErrClosed", err)
	}
	b.Close() // must be safe twice
}

func TestRecentErrorRateForgetsOldTrouble(t *testing.T) {
	// The lifetime rate is no use as a health signal: a bad hour on the day of
	// installation would poison it for years. The recent rate has to fall back
	// to zero once the line behaves again.
	client := modbus.NewClient(stubTransport{}, 1)
	b := New(client, Options{})
	t.Cleanup(b.Close)

	for i := 0; i < 300; i++ {
		client.OnExchange(modbus.Exchange{Err: errors.New("crc")})
	}
	if got := b.Stats().RecentErrorRate; got != 1 {
		t.Fatalf("recent error rate = %v after nothing but failures, want 1", got)
	}

	for i := 0; i < 300; i++ {
		client.OnExchange(modbus.Exchange{})
	}
	if got := b.Stats().RecentErrorRate; got != 0 {
		t.Fatalf("recent error rate = %v after the line recovered, want 0", got)
	}
	if b.Stats().ExchangeErrors != 300 {
		t.Fatalf("lifetime error count = %d, want 300; it must still be there", b.Stats().ExchangeErrors)
	}
}

func TestRecentErrorRateIsAShareOfTheWindow(t *testing.T) {
	client := modbus.NewClient(stubTransport{}, 1)
	b := New(client, Options{})
	t.Cleanup(b.Close)

	// Fill the window, a quarter of it failing.
	for i := 0; i < 256; i++ {
		if i%4 == 0 {
			client.OnExchange(modbus.Exchange{Err: errors.New("timeout")})
		} else {
			client.OnExchange(modbus.Exchange{})
		}
	}
	if got := b.Stats().RecentErrorRate; got < 0.24 || got > 0.26 {
		t.Fatalf("recent error rate = %v, want about 0.25", got)
	}
}

func TestRecentErrorRateIsZeroBeforeAnythingHappens(t *testing.T) {
	b := New(modbus.NewClient(stubTransport{}, 1), Options{})
	t.Cleanup(b.Close)
	if got := b.Stats().RecentErrorRate; got != 0 {
		t.Fatalf("recent error rate = %v with no traffic, want 0", got)
	}
}

// TestAPortIsReopenedAfterARunOfFailures.
//
// The port is opened once at start. Unplug a USB adapter and put it back, and
// the descriptor it left behind never recovers — every read fails for as long
// as the process lives. On a box meant to run for years without anybody
// logging in, that is a cable somebody brushed against turning into a callout.
func TestAPortIsReopenedAfterARunOfFailures(t *testing.T) {
	var reopened atomic.Int64
	b := New(modbus.NewClient(stubTransport{}, 1), Options{Reopen: func() error {
		reopened.Add(1)
		return nil
	}})
	defer b.Close()

	for i := 0; i < ReopenAfter; i++ {
		if got := reopened.Load(); got != 0 {
			t.Fatalf("reopened after %d failures, want it to wait for %d", i, ReopenAfter)
		}
		_ = b.Do(context.Background(), PriorityPoll, func(*modbus.Client) error {
			return errors.New("the adapter is not there")
		})
	}
	if got := reopened.Load(); got != 1 {
		t.Fatalf("reopened %d times after %d failures, want once", got, ReopenAfter)
	}
}

// TestSuccessResetsTheFailureRun: a line that fails now and then is not an
// adapter that has gone away, and reopening the port on every hiccup would
// drop whatever was in flight for no reason.
func TestSuccessResetsTheFailureRun(t *testing.T) {
	var reopened atomic.Int64
	b := New(modbus.NewClient(stubTransport{}, 1), Options{Reopen: func() error {
		reopened.Add(1)
		return nil
	}})
	defer b.Close()

	for i := 0; i < ReopenAfter*3; i++ {
		fail := i%4 != 3 // three bad, one good, over and over
		_ = b.Do(context.Background(), PriorityPoll, func(*modbus.Client) error {
			if fail {
				return errors.New("noise")
			}
			return nil
		})
	}
	if got := reopened.Load(); got != 0 {
		t.Errorf("reopened %d times on a merely noisy line", got)
	}
}

// countingHandler records what the bus chose to say, which is the whole point
// of the test below: the messages, not the reopening.
type countingHandler struct {
	slog.Handler
	mu   sync.Mutex
	msgs []string
}

func (h *countingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *countingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.msgs = append(h.msgs, r.Message)
	return nil
}
func (h *countingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *countingHandler) WithGroup(string) slog.Handler      { return h }

func (h *countingHandler) count(substr string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, m := range h.msgs {
		if strings.Contains(m, substr) {
			n++
		}
	}
	return n
}

func fail(b *Bus, times int) {
	for i := 0; i < times; i++ {
		_ = b.Do(context.Background(), PriorityPoll, func(*modbus.Client) error {
			return errors.New("no answer from the unit")
		})
	}
}

// TestAFaultThatReopeningCannotFixIsSaidOnce: pull the RS-485 wires out of the
// adapter and the port is perfectly healthy — every reopen succeeds and fixes
// nothing, because the port was never the problem. Measured, that
// produced a line every forty-three seconds claiming the port had been
// recovered. Left over a weekend it is thousands of lines, all of them saying
// something was put right when nothing was.
func TestAFaultThatReopeningCannotFixIsSaidOnce(t *testing.T) {
	h := &countingHandler{}
	b := New(modbus.NewClient(stubTransport{}, 1), Options{
		Logger: slog.New(h),
		Reopen: func() error { return nil }, // the port opens fine; the wires are out
	})
	defer b.Close()

	fail(b, ReopenAfter*6)

	if got := h.count("reopened the serial port"); got != 1 {
		t.Errorf("said it reopened %d times over six rounds, want once", got)
	}
	// And it must not claim the bus recovered, because it did not.
	if got := h.count("answering again"); got != 0 {
		t.Errorf("claimed the bus recovered %d times while it was still down", got)
	}
}

// TestAMissingAdapterIsAlsoSaidOnce: the same argument for the other fault.
// The adapter will still be missing in twelve failures' time.
func TestAMissingAdapterIsAlsoSaidOnce(t *testing.T) {
	h := &countingHandler{}
	gone := true
	b := New(modbus.NewClient(stubTransport{}, 1), Options{
		Logger: slog.New(h),
		Reopen: func() error {
			if gone {
				return errors.New("no such file or directory")
			}
			return nil
		},
	})
	defer b.Close()

	fail(b, ReopenAfter*4)
	if got := h.count("could not reopen"); got != 1 {
		t.Errorf("complained %d times about a missing adapter, want once", got)
	}

	// Plugged back in: the port coming back is news even though the earlier
	// failures were not repeated.
	gone = false
	fail(b, ReopenAfter)
	if got := h.count("reopened the serial port"); got != 1 {
		t.Errorf("said the port came back %d times, want once", got)
	}
}

// TestTheBusComingBackIsReported: the line that actually matters. Reopening a
// port says nothing about whether the unit is answering, so recovery is
// reported when a request succeeds — and only when something was said about it
// breaking, or every successful poll would announce itself.
func TestTheBusComingBackIsReported(t *testing.T) {
	h := &countingHandler{}
	b := New(modbus.NewClient(stubTransport{}, 1), Options{
		Logger: slog.New(h),
		Reopen: func() error { return nil },
	})
	defer b.Close()

	// A healthy bus says nothing at all.
	for i := 0; i < 5; i++ {
		_ = b.Do(context.Background(), PriorityPoll, func(*modbus.Client) error { return nil })
	}
	if got := h.count("answering again"); got != 0 {
		t.Fatalf("a bus that never broke announced its recovery %d times", got)
	}

	fail(b, ReopenAfter)
	_ = b.Do(context.Background(), PriorityPoll, func(*modbus.Client) error { return nil })
	if got := h.count("answering again"); got != 1 {
		t.Errorf("reported recovery %d times, want once", got)
	}

	// And a second fault later is reported again — said once per fault, not
	// once per lifetime.
	fail(b, ReopenAfter)
	_ = b.Do(context.Background(), PriorityPoll, func(*modbus.Client) error { return nil })
	if got := h.count("answering again"); got != 2 {
		t.Errorf("a later fault reported recovery %d times in total, want 2", got)
	}
}
