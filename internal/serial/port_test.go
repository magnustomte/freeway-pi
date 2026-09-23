package serial

import (
	"errors"
	"testing"
	"time"
)

// TestAClosedPortRefusesEveryOperation: a failed reopen used to leave fd at
// zero, which is standard input — so writes went to stdin and reads came back
// empty. Under systemd stdin is /dev/null, which accepts everything silently,
// and the bus would have looked idle rather than broken.
func TestAClosedPortRefusesEveryOperation(t *testing.T) {
	p := Closed(Config{Path: "/dev/does-not-exist", Baud: 19200})

	if err := p.Write([]byte("x")); !errors.Is(err, ErrClosed) {
		t.Errorf("Write on a closed port gave %v, want ErrClosed", err)
	}
	if err := p.Drain(); !errors.Is(err, ErrClosed) {
		t.Errorf("Drain on a closed port gave %v, want ErrClosed", err)
	}
	if err := p.ReadFull(make([]byte, 4), time.Now().Add(time.Second)); !errors.Is(err, ErrClosed) {
		t.Errorf("ReadFull on a closed port gave %v, want ErrClosed", err)
	}
}

// TestAClosedPortCanStillBeReopened: it is the state a box starts in when the
// adapter is not plugged in yet, and plugging one in has to be enough.
func TestAClosedPortCanStillBeReopened(t *testing.T) {
	p := Closed(Config{Path: "/dev/does-not-exist", Baud: 19200})
	if err := p.Reopen(); err == nil {
		t.Fatal("reopening a device that is not there succeeded")
	}
	// And it is still closed rather than half-open pointing at stdin.
	if err := p.Write([]byte("x")); !errors.Is(err, ErrClosed) {
		t.Errorf("after a failed reopen, Write gave %v, want ErrClosed", err)
	}
}
