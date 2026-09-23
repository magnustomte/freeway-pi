// Package modbustest provides an in-memory Modbus RTU slave, so the gateway
// can be exercised end to end without a ventilation unit on the other end of
// a cable.
//
// It answers real frames with real CRCs, refuses addresses it does not have,
// and can be told to fail on demand, because the interesting behaviour in a
// gateway is what happens when the unit does not answer.
package modbustest

import (
	"encoding/binary"
	"os"
	"sync"
	"time"

	"freewaypi/internal/modbus"
)

// Unit is a simulated EDA board.
type Unit struct {
	mu sync.Mutex

	Slave   byte
	Holding []uint16
	Coils   []bool

	// Silent makes the unit stop answering, as a unit with a pulled cable
	// would. The client should then time out and retry.
	Silent bool
	// CorruptNext mangles the next answer's CRC once, to exercise retries.
	CorruptNext bool
	// Delay is how long the unit takes to answer.
	Delay time.Duration

	// Requests counts frames received, including those answered with an
	// exception.
	Requests int
	// Writes records every write applied, in order, for tests that care about
	// what reached the unit rather than what was asked.
	Writes []Write

	pending []byte
}

// Write is one applied write.
type Write struct {
	Function byte
	Address  uint16
	Values   []uint16
}

// NewUnit returns a unit with the given address space, all zero.
func NewUnit(slave byte, holding, coils int) *Unit {
	return &Unit{
		Slave:   slave,
		Holding: make([]uint16, holding),
		Coils:   make([]bool, coils),
	}
}

// Drain discards any unread answer.
func (u *Unit) Drain() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.pending = nil
	return nil
}

// Write receives a request frame and prepares the answer.
func (u *Unit) Write(frame []byte) error {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.Requests++
	u.pending = nil

	if u.Silent {
		return nil
	}
	if len(frame) < 4 || !modbus.CheckCRC(frame) || frame[0] != u.Slave {
		return nil // a unit ignores frames that are not for it, or are damaged
	}

	resp := u.answer(frame[1 : len(frame)-2])
	out := modbus.AppendCRC(append([]byte{u.Slave}, resp...))
	if u.CorruptNext {
		u.CorruptNext = false
		out[len(out)-1] ^= 0xFF
	}
	u.pending = out
	return nil
}

// answer builds the response PDU for a request PDU.
func (u *Unit) answer(pdu []byte) []byte {
	if len(pdu) < 5 {
		return modbus.ExceptionPDU(pdu[0], modbus.ExceptionIllegalDataValue)
	}
	fc := pdu[0]
	addr := binary.BigEndian.Uint16(pdu[1:3])
	arg := binary.BigEndian.Uint16(pdu[3:5])

	switch fc {
	case modbus.FCReadHoldingRegisters, modbus.FCReadInputRegisters:
		if int(addr)+int(arg) > len(u.Holding) {
			return modbus.ExceptionPDU(fc, modbus.ExceptionIllegalDataAddress)
		}
		out := []byte{fc, byte(2 * arg)}
		for _, v := range u.Holding[addr : addr+arg] {
			out = binary.BigEndian.AppendUint16(out, v)
		}
		return out

	case modbus.FCReadCoils, modbus.FCReadDiscreteInputs:
		if int(addr)+int(arg) > len(u.Coils) {
			return modbus.ExceptionPDU(fc, modbus.ExceptionIllegalDataAddress)
		}
		n := (int(arg) + 7) / 8
		out := make([]byte, 2+n)
		out[0], out[1] = fc, byte(n)
		for i := 0; i < int(arg); i++ {
			if u.Coils[int(addr)+i] {
				out[2+i/8] |= 1 << (i % 8)
			}
		}
		return out

	case modbus.FCWriteSingleRegister:
		if int(addr) >= len(u.Holding) {
			return modbus.ExceptionPDU(fc, modbus.ExceptionIllegalDataAddress)
		}
		u.Holding[addr] = arg
		u.Writes = append(u.Writes, Write{fc, addr, []uint16{arg}})
		return pdu // the echo

	case modbus.FCWriteSingleCoil:
		if int(addr) >= len(u.Coils) {
			return modbus.ExceptionPDU(fc, modbus.ExceptionIllegalDataAddress)
		}
		u.Coils[addr] = arg == 0xFF00
		u.Writes = append(u.Writes, Write{fc, addr, []uint16{arg}})
		return pdu

	case modbus.FCWriteMultipleRegisters:
		if len(pdu) < 6+2*int(arg) || int(addr)+int(arg) > len(u.Holding) {
			return modbus.ExceptionPDU(fc, modbus.ExceptionIllegalDataAddress)
		}
		vals := make([]uint16, arg)
		for i := range vals {
			vals[i] = binary.BigEndian.Uint16(pdu[6+2*i:])
			u.Holding[int(addr)+i] = vals[i]
		}
		u.Writes = append(u.Writes, Write{fc, addr, vals})
		return pdu[:5]

	case modbus.FCWriteMultipleCoils:
		if int(addr)+int(arg) > len(u.Coils) {
			return modbus.ExceptionPDU(fc, modbus.ExceptionIllegalDataAddress)
		}
		vals := make([]uint16, arg)
		for i := 0; i < int(arg); i++ {
			on := len(pdu) > 6+i/8 && pdu[6+i/8]&(1<<(i%8)) != 0
			u.Coils[int(addr)+i] = on
			if on {
				vals[i] = 1
			}
		}
		u.Writes = append(u.Writes, Write{fc, addr, vals})
		return pdu[:5]

	default:
		return modbus.ExceptionPDU(fc, modbus.ExceptionIllegalFunction)
	}
}

// ReadFull hands back the prepared answer.
func (u *Unit) ReadFull(p []byte, deadline time.Time) error {
	u.mu.Lock()
	delay := u.Delay
	u.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.pending) < len(p) {
		u.pending = nil
		return os.ErrDeadlineExceeded
	}
	copy(p, u.pending[:len(p)])
	u.pending = u.pending[len(p):]
	return nil
}

// Snapshot returns copies of the unit's memory, for assertions.
func (u *Unit) Snapshot() ([]uint16, []bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	h := append([]uint16(nil), u.Holding...)
	c := append([]bool(nil), u.Coils...)
	return h, c
}

// Set writes a holding register directly, bypassing the protocol.
func (u *Unit) Set(addr uint16, v uint16) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.Holding[addr] = v
}

// SetCoil writes a coil directly, bypassing the protocol.
func (u *Unit) SetCoil(addr uint16, v bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.Coils[addr] = v
}

// SetSilent starts or stops the unit answering.
func (u *Unit) SetSilent(silent bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.Silent = silent
}
