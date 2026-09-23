package modbus

import (
	"errors"
	"os"
	"testing"
	"time"
)

// fakeTransport replays a script of answers, one per request written. A nil
// answer means the unit stayed silent.
type fakeTransport struct {
	answers [][]byte
	idx     int
	pending []byte
	writes  [][]byte
	drains  int
}

func (f *fakeTransport) Drain() error { f.drains++; f.pending = nil; return nil }

func (f *fakeTransport) Write(p []byte) error {
	f.writes = append(f.writes, append([]byte(nil), p...))
	if f.idx < len(f.answers) {
		f.pending = append([]byte(nil), f.answers[f.idx]...)
		f.idx++
	} else {
		f.pending = nil
	}
	return nil
}

func (f *fakeTransport) ReadFull(p []byte, deadline time.Time) error {
	if len(f.pending) < len(p) {
		f.pending = nil
		return os.ErrDeadlineExceeded
	}
	copy(p, f.pending[:len(p)])
	f.pending = f.pending[len(p):]
	return nil
}

func newTestClient(answers ...[]byte) (*Client, *fakeTransport) {
	f := &fakeTransport{answers: answers}
	c := NewClient(f, 1)
	c.InterFrame = 0
	c.Timeout = 50 * time.Millisecond
	return c, f
}

func TestClientReadHolding(t *testing.T) {
	answer := AppendCRC([]byte{0x01, 0x03, 0x06, 0x00, 0x0C, 0x00, 0x09, 0x00, 0x1A})
	c, f := newTestClient(answer)

	regs, err := c.ReadHolding(40, 3)
	if err != nil {
		t.Fatalf("ReadHolding: %v", err)
	}
	if len(regs) != 3 || regs[0] != 12 || regs[1] != 9 || regs[2] != 26 {
		t.Fatalf("registers = %v, want [12 9 26]", regs)
	}
	if len(f.writes) != 1 {
		t.Fatalf("%d requests written, want 1", len(f.writes))
	}
	if f.drains != 1 {
		t.Fatalf("%d drains, want 1 before the request", f.drains)
	}
}

func TestClientReturnsExceptionWithoutRetrying(t *testing.T) {
	// The unit answering "illegal data address" is an answer. Asking again
	// wastes bus time we need for the Homey app.
	answer := AppendCRC([]byte{0x01, 0x83, 0x02})
	c, f := newTestClient(answer, answer, answer)

	_, err := c.ReadHolding(710, 1)
	if !IsIllegalDataAddress(err) {
		t.Fatalf("error = %v, want illegal data address", err)
	}
	if len(f.writes) != 1 {
		t.Fatalf("%d requests written, want exactly 1 with no retry", len(f.writes))
	}
}

func TestClientRetriesSilenceThenGivesUp(t *testing.T) {
	c, f := newTestClient(nil, nil, nil)
	c.Retries = 2

	_, err := c.ReadHolding(40, 3)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("error = %v, want ErrTimeout", err)
	}
	if len(f.writes) != 3 {
		t.Fatalf("%d attempts, want 3 (one plus two retries)", len(f.writes))
	}
}

func TestClientRecoversOnRetry(t *testing.T) {
	good := AppendCRC([]byte{0x01, 0x03, 0x02, 0x00, 0x2A})
	c, f := newTestClient(nil, good)

	regs, err := c.ReadHolding(135, 1)
	if err != nil {
		t.Fatalf("ReadHolding: %v", err)
	}
	if regs[0] != 42 {
		t.Fatalf("register = %d, want 42", regs[0])
	}
	if len(f.writes) != 2 {
		t.Fatalf("%d attempts, want 2", len(f.writes))
	}
}

func TestClientReportsEveryExchange(t *testing.T) {
	good := AppendCRC([]byte{0x01, 0x03, 0x02, 0x00, 0x2A})
	c, _ := newTestClient(nil, good)

	var seen []Exchange
	c.OnExchange = func(ex Exchange) { seen = append(seen, ex) }

	if _, err := c.ReadHolding(135, 1); err != nil {
		t.Fatalf("ReadHolding: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("%d exchanges reported, want 2", len(seen))
	}
	if seen[0].Err == nil {
		t.Error("first exchange should have recorded the timeout")
	}
	if seen[0].Attempt != 0 || seen[1].Attempt != 1 {
		t.Errorf("attempt numbers = %d, %d, want 0, 1", seen[0].Attempt, seen[1].Attempt)
	}
	if seen[1].Err != nil {
		t.Errorf("second exchange reported %v, want success", seen[1].Err)
	}
	if seen[1].Turnaround <= 0 {
		t.Error("turnaround was not measured")
	}
}

func TestClientWriteSingleRegisterEchoIsAccepted(t *testing.T) {
	// Function code 6 echoes the address and the value written. Freeway WEB
	// produces exactly this echo without passing the write on, which is why
	// a clean echo here proves nothing about the unit and fwprobe reads back.
	echo := AppendCRC([]byte{0x01, 0x06, 0x00, 0x39, 0x00, 0x15})
	c, f := newTestClient(echo)

	if err := c.WriteSingleRegister(57, 21); err != nil {
		t.Fatalf("WriteSingleRegister: %v", err)
	}
	want := BuildWriteSingleRegister(1, 57, 21)
	if string(f.writes[0]) != string(want) {
		t.Fatalf("wrote % x, want % x", f.writes[0], want)
	}
}

func TestClientWriteRegisters(t *testing.T) {
	echo := AppendCRC([]byte{0x01, 0x10, 0x00, 0x38, 0x00, 0x02})
	c, f := newTestClient(echo)

	if err := c.WriteRegisters(56, []uint16{20, 20}); err != nil {
		t.Fatalf("WriteRegisters: %v", err)
	}
	want := BuildWriteMultipleRegisters(1, 56, []uint16{20, 20})
	if string(f.writes[0]) != string(want) {
		t.Fatalf("wrote % x, want % x", f.writes[0], want)
	}
}

func TestClientReadCoils(t *testing.T) {
	answer := AppendCRC([]byte{0x01, 0x01, 0x01, 0b00000001})
	c, _ := newTestClient(answer)

	bits, err := c.ReadCoils(16, 1)
	if err != nil {
		t.Fatalf("ReadCoils: %v", err)
	}
	if len(bits) != 1 || !bits[0] {
		t.Fatalf("coils = %v, want [true] (EC fans)", bits)
	}
}
