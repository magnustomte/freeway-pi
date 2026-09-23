package gateway

import (
	"testing"
	"time"
)

func TestBreakerStaysClosedUntilTheThreshold(t *testing.T) {
	b := NewBreaker(BreakerOptions{Threshold: 3})
	for i := 0; i < 2; i++ {
		b.Failure()
		if !b.Allow() {
			t.Fatalf("opened after %d failures, threshold is 3", i+1)
		}
	}
	b.Failure()
	if b.Allow() {
		t.Fatal("did not open at the threshold")
	}
	if !b.State().Open {
		t.Fatal("state does not report the breaker as open")
	}
}

func TestBreakerRefusesWithoutTouchingTheBus(t *testing.T) {
	// The whole point: while open, nothing reaches the bus at all, so the
	// poller keeps getting turns and can notice the unit returning.
	b := NewBreaker(BreakerOptions{Threshold: 1, Cooldown: time.Hour})
	b.Failure()
	for i := 0; i < 100; i++ {
		if b.Allow() {
			t.Fatalf("let request %d through while open", i)
		}
	}
}

func TestBreakerProbesOncePerCooldown(t *testing.T) {
	now := time.Now()
	b := NewBreaker(BreakerOptions{Threshold: 1, Cooldown: time.Minute, Now: func() time.Time { return now }})
	b.Failure()

	if b.Allow() {
		t.Fatal("probed before the cooldown elapsed")
	}
	now = now.Add(time.Minute)
	if !b.Allow() {
		t.Fatal("did not probe after the cooldown elapsed")
	}
	if b.Allow() {
		t.Fatal("let a second probe through in the same cooldown")
	}
	now = now.Add(time.Minute)
	if !b.Allow() {
		t.Fatal("did not probe again after a further cooldown")
	}
}

func TestBreakerClosesOnASuccessfulProbe(t *testing.T) {
	now := time.Now()
	b := NewBreaker(BreakerOptions{Threshold: 2, Cooldown: time.Minute, Now: func() time.Time { return now }})
	b.Failure()
	b.Failure()
	now = now.Add(time.Minute)

	if !b.Allow() {
		t.Fatal("no probe was allowed")
	}
	b.Success()

	if !b.Allow() || !b.Allow() {
		t.Fatal("stayed open after the unit answered")
	}
	if b.State().Open {
		t.Fatal("state still reports open after recovery")
	}
	if b.State().Consecutive != 0 {
		t.Fatal("failure count survived a success")
	}
}

func TestBreakerCountsTripsOncePerOpening(t *testing.T) {
	// Trips is a health signal. Counting every failure instead would make a
	// single long outage look like hundreds of separate incidents.
	b := NewBreaker(BreakerOptions{Threshold: 2, Cooldown: time.Millisecond})
	for i := 0; i < 10; i++ {
		b.Failure()
	}
	if got := b.State().Trips; got != 1 {
		t.Fatalf("trips = %d after one continuous outage, want 1", got)
	}
	b.Success()
	b.Failure()
	b.Failure()
	if got := b.State().Trips; got != 2 {
		t.Fatalf("trips = %d after a second outage, want 2", got)
	}
}
