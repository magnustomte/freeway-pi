package modbus

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// Transport is the byte pipe a Client talks over: a serial port in production,
// a scripted fake in tests.
type Transport interface {
	// Drain discards any bytes already waiting, so a late answer to an earlier
	// request cannot be mistaken for this one's.
	Drain() error
	Write(p []byte) error
	// ReadFull fills p completely or returns an error. It must respect the
	// deadline rather than blocking forever on a silent bus.
	ReadFull(p []byte, deadline time.Time) error
}

// ErrTimeout is returned when the unit says nothing within the timeout. On a
// fresh install the usual cause is Data + and Data - being swapped.
var ErrTimeout = errors.New("modbus: no answer from unit")

// Exchange is one request and its answer, with the measured turnaround. The
// timing is the point: it tells the gateway's queue how fast it may go.
type Exchange struct {
	Request  []byte
	Response []byte
	// Turnaround is measured from the last byte written to the last byte read.
	Turnaround time.Duration
	Err        error
	Attempt    int
}

// Client is a Modbus RTU master. It is not safe for concurrent use: on RS-485
// exactly one request may be in flight, which is the whole reason freewayd
// funnels every caller through a single owner of the port.
type Client struct {
	T     Transport
	Slave byte

	// Timeout bounds one attempt.
	Timeout time.Duration
	// InterFrame is the silence held between one answer and the next request.
	// The standard asks for 3.5 character times, which is 2 ms at 19200 bps,
	// but real units often want considerably more. Measured by fwprobe timing.
	InterFrame time.Duration
	// Retries is the number of extra attempts after a transport-level failure.
	// Exceptions are answers, not failures, and are never retried.
	Retries int

	// OnExchange, if set, is called for every attempt including failures.
	OnExchange func(Exchange)

	lastDone time.Time
}

// NewClient returns a client with defaults that are safe on a 19200 bps bus.
func NewClient(t Transport, slave byte) *Client {
	return &Client{
		T:          t,
		Slave:      slave,
		Timeout:    time.Second,
		InterFrame: 10 * time.Millisecond,
		Retries:    2,
	}
}

func (c *Client) execute(req []byte, fc byte, quantity uint16) ([]byte, error) {
	return c.executeAs(c.Slave, req, fc, quantity)
}

// executeAs is execute for a slave address other than the client's own, which
// the gateway needs: the unit address belongs to the request, not to the port.
func (c *Client) executeAs(slave byte, req []byte, fc byte, quantity uint16) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= c.Retries; attempt++ {
		payload, ex := c.attempt(slave, req, fc, quantity, attempt)
		if c.OnExchange != nil {
			c.OnExchange(ex)
		}
		if ex.Err == nil {
			return payload, nil
		}
		var mbErr *Exception
		if errors.As(ex.Err, &mbErr) {
			// The unit answered. Retrying will not change its mind.
			return nil, ex.Err
		}
		lastErr = ex.Err
	}
	return nil, lastErr
}

func (c *Client) attempt(slave byte, req []byte, fc byte, quantity uint16, attempt int) ([]byte, Exchange) {
	ex := Exchange{Request: req, Attempt: attempt}

	if gap := time.Until(c.lastDone.Add(c.InterFrame)); gap > 0 {
		time.Sleep(gap)
	}
	if err := c.T.Drain(); err != nil {
		ex.Err = fmt.Errorf("drain: %w", err)
		c.lastDone = time.Now()
		return nil, ex
	}
	if err := c.T.Write(req); err != nil {
		ex.Err = fmt.Errorf("write: %w", err)
		c.lastDone = time.Now()
		return nil, ex
	}

	start := time.Now()
	deadline := start.Add(c.Timeout)
	defer func() { c.lastDone = time.Now() }()

	// Read the header first: the second byte says whether this is the answer we
	// sized for or a five-byte exception.
	head := make([]byte, 2)
	if err := c.T.ReadFull(head, deadline); err != nil {
		ex.Turnaround = time.Since(start)
		ex.Err = normalizeReadErr(err)
		return nil, ex
	}

	var total int
	if head[1]&0x80 != 0 {
		total = ExceptionLen
	} else {
		n, err := ResponseLen(fc, quantity)
		if err != nil {
			ex.Turnaround = time.Since(start)
			ex.Err = err
			return nil, ex
		}
		total = n
	}

	frame := make([]byte, total)
	copy(frame, head)
	if err := c.T.ReadFull(frame[2:], deadline); err != nil {
		ex.Turnaround = time.Since(start)
		ex.Response = head
		// A truncated frame is worth distinguishing from total silence: it
		// usually means the response length was mispredicted or the line is
		// noisy, not that the unit is absent.
		ex.Err = fmt.Errorf("after %d of %d bytes: %w", 2, total, normalizeReadErr(err))
		return nil, ex
	}
	ex.Turnaround = time.Since(start)
	ex.Response = frame

	payload, err := ParseResponse(frame, slave, fc)
	ex.Err = err
	return payload, ex
}

// normalizeReadErr turns the transport's deadline error into ErrTimeout, so
// callers can test for "the unit said nothing" without knowing the transport.
func normalizeReadErr(err error) error {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return ErrTimeout
	}
	return err
}

// ReadHolding reads quantity holding registers starting at addr.
func (c *Client) ReadHolding(addr, quantity uint16) ([]uint16, error) {
	payload, err := c.execute(BuildRead(c.Slave, FCReadHoldingRegisters, addr, quantity), FCReadHoldingRegisters, quantity)
	if err != nil {
		return nil, err
	}
	return DecodeRegisters(payload, quantity)
}

// ReadInput reads quantity input registers starting at addr.
func (c *Client) ReadInput(addr, quantity uint16) ([]uint16, error) {
	payload, err := c.execute(BuildRead(c.Slave, FCReadInputRegisters, addr, quantity), FCReadInputRegisters, quantity)
	if err != nil {
		return nil, err
	}
	return DecodeRegisters(payload, quantity)
}

// ReadCoils reads quantity coils starting at addr.
func (c *Client) ReadCoils(addr, quantity uint16) ([]bool, error) {
	payload, err := c.execute(BuildRead(c.Slave, FCReadCoils, addr, quantity), FCReadCoils, quantity)
	if err != nil {
		return nil, err
	}
	return DecodeBits(payload, quantity)
}

// WriteSingleRegister writes one register with function code 6.
func (c *Client) WriteSingleRegister(addr, value uint16) error {
	_, err := c.execute(BuildWriteSingleRegister(c.Slave, addr, value), FCWriteSingleRegister, 1)
	return err
}

// WriteSingleCoil writes one coil with function code 5.
func (c *Client) WriteSingleCoil(addr uint16, on bool) error {
	_, err := c.execute(BuildWriteSingleCoil(c.Slave, addr, on), FCWriteSingleCoil, 1)
	return err
}

// WriteRegisters writes consecutive registers with function code 16.
func (c *Client) WriteRegisters(addr uint16, values []uint16) error {
	_, err := c.execute(BuildWriteMultipleRegisters(c.Slave, addr, values), FCWriteMultipleRegisters, uint16(len(values)))
	return err
}

// WriteCoils writes consecutive coils with function code 15.
func (c *Client) WriteCoils(addr uint16, values []bool) error {
	_, err := c.execute(BuildWriteMultipleCoils(c.Slave, addr, values), FCWriteMultipleCoils, uint16(len(values)))
	return err
}

// ExecutePDU sends a raw PDU to the given unit address and returns the unit's
// raw response PDU. It is the gateway's path: nothing about the request is
// interpreted beyond what is needed to know how long the answer will be, and an
// exception from the unit is returned as a response rather than an error,
// because it is the unit's own answer and the client asked for it.
//
// The address comes from the request rather than from the client, because a
// gateway forwards to whichever unit the caller named. The panel allows
// addresses 1 to 10 on this bus.
//
// err is reserved for failures that produced no answer at all.
func (c *Client) ExecutePDU(unit byte, pdu []byte) ([]byte, error) {
	quantity, err := RequestQuantity(pdu)
	if err != nil {
		return nil, err
	}
	fc := pdu[0]

	frame := make([]byte, 0, 1+len(pdu)+2)
	frame = append(frame, unit)
	frame = append(frame, pdu...)
	frame = AppendCRC(frame)

	payload, err := c.executeAs(unit, frame, fc, quantity)
	if err != nil {
		var ex *Exception
		if errors.As(err, &ex) {
			return ExceptionPDU(ex.Function, ex.Code), nil
		}
		return nil, err
	}
	return append([]byte{fc}, payload...), nil
}
