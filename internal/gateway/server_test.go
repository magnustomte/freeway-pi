package gateway

import (
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"freewaypi/internal/bus"
	"freewaypi/internal/modbus"
	"freewaypi/internal/modbustest"
)

type fixture struct {
	unit   *modbustest.Unit
	bus    *bus.Bus
	server *Server
	addr   string
}

func newFixture(t *testing.T, allow ...string) *fixture {
	t.Helper()

	u := modbustest.NewUnit(1, bus.LastHolding+1, bus.LastCoil+1)
	c := modbus.NewClient(u, 1)
	c.InterFrame = 0
	c.Timeout = 20 * time.Millisecond
	c.Retries = 1

	b := bus.New(c, bus.Options{})
	t.Cleanup(b.Close)

	acl := NewACL()
	if len(allow) == 0 {
		allow = []string{"127.0.0.0/8", "::1/128"}
	}
	if err := acl.Set(true, allow); err != nil {
		t.Fatal(err)
	}

	s := New(b, Options{
		ACL:            acl,
		Breaker:        NewBreaker(BreakerOptions{Threshold: 2, Cooldown: time.Hour}),
		Logger:         slog.New(slog.DiscardHandler),
		RequestTimeout: time.Second,
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve(ln) }()
	t.Cleanup(func() { _ = s.Close() })

	return &fixture{unit: u, bus: b, server: s, addr: ln.Addr().String()}
}

// conn is a minimal Modbus TCP client, so the tests exercise the same path a
// real client takes rather than calling the server's methods directly.
type conn struct {
	t   *testing.T
	c   net.Conn
	txn uint16
}

func (f *fixture) dial(t *testing.T) *conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", f.addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return &conn{t: t, c: c}
}

func (c *conn) request(unit byte, pdu ...byte) *modbus.TCPFrame {
	c.t.Helper()
	c.txn++
	out := (&modbus.TCPFrame{Transaction: c.txn, Unit: unit, PDU: pdu}).Encode()
	if err := c.c.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		c.t.Fatal(err)
	}
	if _, err := c.c.Write(out); err != nil {
		c.t.Fatalf("write: %v", err)
	}
	resp, err := modbus.ReadTCPFrame(c.c)
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	if resp.Transaction != c.txn {
		c.t.Fatalf("transaction %d in the answer, sent %d", resp.Transaction, c.txn)
	}
	return resp
}

func readHolding(addr, count uint16) []byte {
	pdu := []byte{modbus.FCReadHoldingRegisters}
	pdu = binary.BigEndian.AppendUint16(pdu, addr)
	return binary.BigEndian.AppendUint16(pdu, count)
}

func TestGatewayReadsThroughToTheUnit(t *testing.T) {
	f := newFixture(t)
	f.unit.Set(135, 220) // setpoint 22.0 C

	resp := f.dial(t).request(1, readHolding(135, 1)...)

	if resp.PDU[0] != modbus.FCReadHoldingRegisters {
		t.Fatalf("function %d in the answer: % x", resp.PDU[0], resp.PDU)
	}
	if got := binary.BigEndian.Uint16(resp.PDU[2:4]); got != 220 {
		t.Fatalf("HR135 = %d, want 220", got)
	}
}

func TestGatewayWritesThroughToTheUnit(t *testing.T) {
	f := newFixture(t)
	c := f.dial(t)

	// Function 16, the one the Homey app uses, since Freeway swallowed 6.
	pdu := []byte{modbus.FCWriteMultipleRegisters}
	pdu = binary.BigEndian.AppendUint16(pdu, 135)
	pdu = binary.BigEndian.AppendUint16(pdu, 1)
	pdu = append(pdu, 2, 0x00, 0xD2) // 210, i.e. 21.0 C
	c.request(1, pdu...)

	holding, _ := f.unit.Snapshot()
	if holding[135] != 210 {
		t.Fatalf("HR135 on the unit = %d, want 210", holding[135])
	}
}

func TestGatewayForwardsTheUnitsOwnException(t *testing.T) {
	// The fidelity that makes this a drop-in: a client reading a register the
	// unit lacks must see illegal data address, exactly as through Freeway.
	f := newFixture(t)

	resp := f.dial(t).request(1, readHolding(uint16(bus.LastHolding)+1, 1)...)

	if resp.PDU[0] != modbus.FCReadHoldingRegisters|0x80 {
		t.Fatalf("expected an exception, got % x", resp.PDU)
	}
	if resp.PDU[1] != modbus.ExceptionIllegalDataAddress {
		t.Fatalf("exception code %d, want %d", resp.PDU[1], modbus.ExceptionIllegalDataAddress)
	}
}

func TestGatewayAnswersUnsupportedFunctionsInsteadOfHangingUp(t *testing.T) {
	f := newFixture(t)
	// Function 43, read device identification: legal Modbus, not something
	// this gateway frames. Dropping the connection would look like a fault.
	resp := f.dial(t).request(1, 0x2B, 0x0E, 0x01, 0x00)

	if resp.PDU[0] != 0x2B|0x80 || resp.PDU[1] != modbus.ExceptionIllegalFunction {
		t.Fatalf("answer was % x, want an illegal function exception", resp.PDU)
	}
}

func TestGatewayRefusesPeersOutsideTheAccessList(t *testing.T) {
	f := newFixture(t, "192.0.2.0/24")

	c, err := net.DialTimeout("tcp", f.addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write((&modbus.TCPFrame{Transaction: 1, Unit: 1, PDU: readHolding(135, 1)}).Encode()); err != nil {
		return // a refused peer may see the close on write, which is fine
	}
	if _, err := modbus.ReadTCPFrame(c); err == nil {
		t.Fatal("a peer outside the access list was answered")
	}
	if f.server.Stats().Refused != 1 {
		t.Fatalf("refused count = %d, want 1", f.server.Stats().Refused)
	}
}

func TestGatewayReportsASilentUnitAndThenStopsUsingTheBus(t *testing.T) {
	// The behaviour the Homey app's per-register fallback makes necessary:
	// once the unit is plainly gone, refusals must be free.
	f := newFixture(t)
	f.unit.SetSilent(true)
	c := f.dial(t)

	for i := 0; i < 2; i++ {
		resp := c.request(1, readHolding(135, 1)...)
		if resp.PDU[0] != modbus.FCReadHoldingRegisters|0x80 || resp.PDU[1] != modbus.ExceptionGatewayTargetNoResponse {
			t.Fatalf("answer %d was % x, want a gateway-target-no-response exception", i, resp.PDU)
		}
	}
	if !f.server.Stats().Breaker.Open {
		t.Fatal("the breaker did not open after repeated silence")
	}

	before := f.unit.Requests
	start := time.Now()
	for i := 0; i < 20; i++ {
		resp := c.request(1, readHolding(135, 1)...)
		if resp.PDU[1] != modbus.ExceptionGatewayTargetNoResponse {
			t.Fatalf("answer %d was % x", i, resp.PDU)
		}
	}
	if got := f.unit.Requests - before; got != 0 {
		t.Fatalf("%d requests still reached the bus with the breaker open, want 0", got)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("twenty refusals took %v; they are supposed to be immediate", elapsed)
	}
}

func TestGatewayRecoversWhenTheUnitComesBack(t *testing.T) {
	now := time.Now()
	f := newFixture(t)
	f.server.breaker = NewBreaker(BreakerOptions{
		Threshold: 2, Cooldown: time.Minute, Now: func() time.Time { return now },
	})
	f.unit.SetSilent(true)
	c := f.dial(t)

	for i := 0; i < 2; i++ {
		c.request(1, readHolding(135, 1)...)
	}
	if !f.server.Stats().Breaker.Open {
		t.Fatal("the breaker did not open")
	}

	f.unit.SetSilent(false)
	f.unit.Set(135, 220)
	now = now.Add(time.Minute) // the probe becomes due

	resp := c.request(1, readHolding(135, 1)...)
	if resp.PDU[0]&0x80 != 0 {
		t.Fatalf("the probe was refused: % x", resp.PDU)
	}
	if got := binary.BigEndian.Uint16(resp.PDU[2:4]); got != 220 {
		t.Fatalf("HR135 = %d, want 220", got)
	}
	if f.server.Stats().Breaker.Open {
		t.Fatal("the breaker stayed open after the unit answered")
	}
}

func TestGatewayListsItsClients(t *testing.T) {
	// Answers "is anything actually talking to the gateway", which the old
	// interface could not.
	f := newFixture(t)
	c := f.dial(t)
	c.request(1, readHolding(135, 1)...)

	clients := f.server.Clients()
	if len(clients) != 1 {
		t.Fatalf("%d clients listed, want 1", len(clients))
	}
	ci := clients[0]
	if ci.Requests != 1 {
		t.Errorf("requests = %d, want 1", ci.Requests)
	}
	if ci.Errors != 0 {
		t.Errorf("errors = %d, want 0", ci.Errors)
	}
	if ci.LastFunction != modbus.FCReadHoldingRegisters {
		t.Errorf("last function = %d, want %d", ci.LastFunction, modbus.FCReadHoldingRegisters)
	}
	if ci.LastUnit != 1 {
		t.Errorf("last unit = %d, want 1", ci.LastUnit)
	}
	if ci.Addr == "" || ci.Connected.IsZero() || ci.LastRequest.IsZero() {
		t.Errorf("incomplete client record: %+v", ci)
	}
}

func TestGatewayForgetsClientsThatDisconnect(t *testing.T) {
	f := newFixture(t)
	c := f.dial(t)
	c.request(1, readHolding(135, 1)...)
	_ = c.c.Close()

	deadline := time.Now().Add(2 * time.Second)
	for len(f.server.Clients()) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("a disconnected client is still listed")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestGatewayServesSeveralClientsAtOnce(t *testing.T) {
	// Freeway allowed one address. Several clients sharing the bus is the
	// point of replacing it.
	f := newFixture(t)
	f.unit.Set(135, 220)

	a, b := f.dial(t), f.dial(t)
	for i := 0; i < 5; i++ {
		for _, c := range []*conn{a, b} {
			resp := c.request(1, readHolding(135, 1)...)
			if got := binary.BigEndian.Uint16(resp.PDU[2:4]); got != 220 {
				t.Fatalf("HR135 = %d, want 220", got)
			}
		}
	}
	if n := len(f.server.Clients()); n != 2 {
		t.Fatalf("%d clients listed, want 2", n)
	}
}

func TestGatewaySurvivesAGarbageFrame(t *testing.T) {
	f := newFixture(t)
	c, err := net.DialTimeout("tcp", f.addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// A non-zero protocol id is not Modbus TCP; the connection should end
	// without taking the server with it. How it ends is not interesting: the
	// close arrives as a reset rather than a clean EOF whenever unread bytes
	// are still buffered, which is normal and not the server's doing.
	_, _ = c.Write([]byte{0, 1, 0, 9, 0, 6, 1, 3, 0, 40, 0, 3})
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	_, _ = io.ReadAll(c)

	// What matters is that the server still works for everyone else.
	f.unit.Set(135, 220)
	resp := f.dial(t).request(1, readHolding(135, 1)...)
	if got := binary.BigEndian.Uint16(resp.PDU[2:4]); got != 220 {
		t.Fatalf("HR135 = %d, want 220 after a garbage frame", got)
	}
}

func TestCloseHangsUpRatherThanWaitingForever(t *testing.T) {
	// A Modbus client holds its connection open for as long as it runs — the
	// Homey app sets no idle timeout at all. Waiting for the handlers to
	// finish means waiting until somebody turns the client off, and systemd
	// kills the service after its stop timeout instead, which is ninety
	// seconds of gateway outage on every restart.
	f := newFixture(t)
	c := f.dial(t)
	c.request(1, readHolding(135, 1)...)

	done := make(chan struct{})
	go func() {
		_ = f.server.Close()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return with a client still connected")
	}
	if n := len(f.server.Clients()); n != 0 {
		t.Errorf("%d clients still listed after close", n)
	}
}

// TestTheSwitchOpensAndClosesThePort: a box that starts with the gateway off
// has to be able to turn it on from the System page, and off has to mean the
// port is closed — not open and refusing. This used to be impossible: with the
// gateway off at startup no server was built at all, so there was nothing for
// the switch to act on.
func TestTheSwitchOpensAndClosesThePort(t *testing.T) {
	u := modbustest.NewUnit(1, bus.LastHolding+1, bus.LastCoil+1)
	c := modbus.NewClient(u, 1)
	c.InterFrame = 0
	c.Timeout = 20 * time.Millisecond
	b := bus.New(c, bus.Options{})
	t.Cleanup(b.Close)

	s := New(b, Options{Logger: slog.New(slog.DiscardHandler)})
	t.Cleanup(func() { _ = s.Close() })

	// Off at startup, the way a fresh image comes up.
	if err := s.Configure(false, nil, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	if s.Listening() {
		t.Fatal("listening while switched off")
	}

	// On from the page.
	if err := s.Configure(true, []string{"127.0.0.0/8"}, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	if !s.Listening() {
		t.Fatal("switched on but not listening")
	}
	s.mu.Lock()
	addr := s.ln.Addr().String()
	s.mu.Unlock()
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("switched on, and a client could not connect: %v", err)
	}
	conn.Close()
	if got := s.Allow(); len(got) != 1 || got[0] != "127.0.0.0/8" {
		t.Errorf("access list = %v, want the one just applied", got)
	}

	// Off again: the port closes, rather than staying open and refusing.
	if err := s.Configure(false, []string{"127.0.0.0/8"}, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	if s.Listening() {
		t.Fatal("still listening after being switched off")
	}
	if conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond); err == nil {
		conn.Close()
		t.Fatal("switched off, and the port still accepted a connection")
	}
}

// TestSavingTheListKeepsClientsConnected: changing the access list is not a
// reason to drop the client that is already talking to the unit.
func TestSavingTheListKeepsClientsConnected(t *testing.T) {
	u := modbustest.NewUnit(1, bus.LastHolding+1, bus.LastCoil+1)
	c := modbus.NewClient(u, 1)
	c.InterFrame = 0
	c.Timeout = 20 * time.Millisecond
	b := bus.New(c, bus.Options{})
	t.Cleanup(b.Close)
	s := New(b, Options{Logger: slog.New(slog.DiscardHandler)})
	t.Cleanup(func() { _ = s.Close() })

	if err := s.Configure(true, []string{"127.0.0.0/8"}, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	first := s.ln
	s.mu.Unlock()

	if err := s.Configure(true, []string{"127.0.0.0/8", "192.0.2.0/24"}, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	second := s.ln
	s.mu.Unlock()
	if first != second {
		t.Error("the listener was replaced when only the access list changed")
	}
}
