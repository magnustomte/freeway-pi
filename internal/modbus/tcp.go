package modbus

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Modbus TCP wraps the same PDU the serial side carries in an MBAP header:
// transaction id, protocol id, length, unit id. The gateway's whole job is to
// move a PDU between the two framings without interpreting it, so that a client
// sees the unit's own answers, exceptions included.

// MBAPHeaderLen is the fixed part of a Modbus TCP frame.
const MBAPHeaderLen = 7

// MaxPDULen is the largest PDU Modbus allows: 253 bytes.
const MaxPDULen = 253

// TCPFrame is one Modbus TCP message.
type TCPFrame struct {
	Transaction uint16
	Unit        byte
	// PDU is the function code followed by its data.
	PDU []byte
}

// ReadTCPFrame reads exactly one frame. It returns io.EOF only when the reader
// ends cleanly between frames, so a caller can tell an orderly disconnect from
// a client that vanished mid-message.
func ReadTCPFrame(r io.Reader) (*TCPFrame, error) {
	var head [MBAPHeaderLen]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return nil, err
	}
	proto := binary.BigEndian.Uint16(head[2:4])
	if proto != 0 {
		return nil, fmt.Errorf("modbus tcp: protocol id %d, expected 0", proto)
	}
	length := binary.BigEndian.Uint16(head[4:6])
	if length < 2 {
		return nil, fmt.Errorf("modbus tcp: length field %d is too small", length)
	}
	// The length counts the unit id plus the PDU.
	pduLen := int(length) - 1
	if pduLen > MaxPDULen {
		return nil, fmt.Errorf("modbus tcp: PDU of %d bytes exceeds the %d byte maximum", pduLen, MaxPDULen)
	}
	pdu := make([]byte, pduLen)
	if _, err := io.ReadFull(r, pdu); err != nil {
		// Having read a header, a truncated body is a broken frame rather than
		// a clean end of stream.
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return &TCPFrame{
		Transaction: binary.BigEndian.Uint16(head[0:2]),
		Unit:        head[6],
		PDU:         pdu,
	}, nil
}

// Encode renders the frame for the wire.
func (f *TCPFrame) Encode() []byte {
	buf := make([]byte, 0, MBAPHeaderLen+len(f.PDU))
	buf = binary.BigEndian.AppendUint16(buf, f.Transaction)
	buf = binary.BigEndian.AppendUint16(buf, 0)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(f.PDU)+1))
	buf = append(buf, f.Unit)
	return append(buf, f.PDU...)
}

// ExceptionPDU builds the response that tells a client its request was refused.
// The gateway needs this for failures of its own, such as a unit that does not
// answer, which have no PDU from the unit to forward.
func ExceptionPDU(fc, code byte) []byte {
	return []byte{fc | 0x80, code}
}

// RequestQuantity extracts the number of items a request asks for, which is
// what decides how long the answer will be. Writes have a fixed-length echo,
// so they report 1.
func RequestQuantity(pdu []byte) (uint16, error) {
	if len(pdu) < 1 {
		return 0, fmt.Errorf("modbus: empty PDU")
	}
	switch pdu[0] {
	case FCReadCoils, FCReadDiscreteInputs, FCReadHoldingRegisters, FCReadInputRegisters,
		FCWriteMultipleCoils, FCWriteMultipleRegisters:
		if len(pdu) < 5 {
			return 0, fmt.Errorf("modbus: %s request is %d bytes, needs at least 5", FunctionName(pdu[0]), len(pdu))
		}
		return binary.BigEndian.Uint16(pdu[3:5]), nil
	case FCWriteSingleCoil, FCWriteSingleRegister:
		if len(pdu) < 5 {
			return 0, fmt.Errorf("modbus: %s request is %d bytes, needs 5", FunctionName(pdu[0]), len(pdu))
		}
		return 1, nil
	default:
		return 0, fmt.Errorf("modbus: unsupported function %d", pdu[0])
	}
}
