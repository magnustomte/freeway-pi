package modbus

import (
	"encoding/binary"
	"fmt"
)

// MaxFrameLen is the largest legal Modbus RTU frame: 256 bytes including the
// slave address and CRC.
const MaxFrameLen = 256

// BuildRead frames a read request. fc must be one of the four read codes.
func BuildRead(slave, fc byte, addr, quantity uint16) []byte {
	buf := make([]byte, 0, 8)
	buf = append(buf, slave, fc)
	buf = binary.BigEndian.AppendUint16(buf, addr)
	buf = binary.BigEndian.AppendUint16(buf, quantity)
	return AppendCRC(buf)
}

// BuildWriteSingleRegister frames function code 6.
func BuildWriteSingleRegister(slave byte, addr, value uint16) []byte {
	buf := make([]byte, 0, 8)
	buf = append(buf, slave, FCWriteSingleRegister)
	buf = binary.BigEndian.AppendUint16(buf, addr)
	buf = binary.BigEndian.AppendUint16(buf, value)
	return AppendCRC(buf)
}

// BuildWriteSingleCoil frames function code 5. The value on the wire is 0xFF00
// for on and 0x0000 for off; anything else is illegal.
func BuildWriteSingleCoil(slave byte, addr uint16, on bool) []byte {
	var v uint16
	if on {
		v = 0xFF00
	}
	buf := make([]byte, 0, 8)
	buf = append(buf, slave, FCWriteSingleCoil)
	buf = binary.BigEndian.AppendUint16(buf, addr)
	buf = binary.BigEndian.AppendUint16(buf, v)
	return AppendCRC(buf)
}

// BuildWriteMultipleRegisters frames function code 16.
func BuildWriteMultipleRegisters(slave byte, addr uint16, values []uint16) []byte {
	buf := make([]byte, 0, 9+2*len(values))
	buf = append(buf, slave, FCWriteMultipleRegisters)
	buf = binary.BigEndian.AppendUint16(buf, addr)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(values)))
	buf = append(buf, byte(2*len(values)))
	for _, v := range values {
		buf = binary.BigEndian.AppendUint16(buf, v)
	}
	return AppendCRC(buf)
}

// BuildWriteMultipleCoils frames function code 15.
func BuildWriteMultipleCoils(slave byte, addr uint16, values []bool) []byte {
	nbytes := (len(values) + 7) / 8
	packed := make([]byte, nbytes)
	for i, v := range values {
		if v {
			packed[i/8] |= 1 << (i % 8)
		}
	}
	buf := make([]byte, 0, 9+nbytes)
	buf = append(buf, slave, FCWriteMultipleCoils)
	buf = binary.BigEndian.AppendUint16(buf, addr)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(values)))
	buf = append(buf, byte(nbytes))
	buf = append(buf, packed...)
	return AppendCRC(buf)
}

// ResponseLen reports how long the response to a request with this function
// code and quantity will be, in bytes, including slave address and CRC.
//
// RTU has no length field, so a master either watches for the inter-frame gap
// or knows what it asked for. Knowing is far more reliable at 19200 bps, where
// the 3.5-character gap is under two milliseconds.
func ResponseLen(fc byte, quantity uint16) (int, error) {
	switch fc {
	case FCReadCoils, FCReadDiscreteInputs:
		return 5 + int((quantity+7)/8), nil
	case FCReadHoldingRegisters, FCReadInputRegisters:
		return 5 + 2*int(quantity), nil
	case FCWriteSingleCoil, FCWriteSingleRegister, FCWriteMultipleCoils, FCWriteMultipleRegisters:
		// Echo of address and value, or of address and quantity.
		return 8, nil
	default:
		return 0, fmt.Errorf("modbus: no response length known for function %d", fc)
	}
}

// ExceptionLen is the length of any exception response.
const ExceptionLen = 5

// ParseResponse validates a complete response frame against the request it
// answers and returns the payload after the function code, CRC stripped.
func ParseResponse(frame []byte, slave, fc byte) ([]byte, error) {
	if len(frame) < ExceptionLen {
		return nil, fmt.Errorf("modbus: short frame, %d bytes", len(frame))
	}
	if !CheckCRC(frame) {
		return nil, fmt.Errorf("modbus: bad CRC in %d-byte frame", len(frame))
	}
	if frame[0] != slave {
		return nil, fmt.Errorf("modbus: answer from slave %d, expected %d", frame[0], slave)
	}
	got := frame[1]
	if got&0x80 != 0 {
		if got&0x7f != fc {
			return nil, fmt.Errorf("modbus: exception for function %d, expected %d", got&0x7f, fc)
		}
		return nil, &Exception{Function: fc, Code: frame[2]}
	}
	if got != fc {
		return nil, fmt.Errorf("modbus: answer for function %d, expected %d", got, fc)
	}
	return frame[2 : len(frame)-2], nil
}

// DecodeRegisters turns a read-registers payload into words. The payload starts
// with its own byte count.
func DecodeRegisters(payload []byte, quantity uint16) ([]uint16, error) {
	if len(payload) < 1 {
		return nil, fmt.Errorf("modbus: empty register payload")
	}
	n := int(payload[0])
	if n != 2*int(quantity) || len(payload) < 1+n {
		return nil, fmt.Errorf("modbus: register payload says %d bytes, wanted %d for %d registers", n, 2*quantity, quantity)
	}
	out := make([]uint16, quantity)
	for i := range out {
		out[i] = binary.BigEndian.Uint16(payload[1+2*i:])
	}
	return out, nil
}

// DecodeBits turns a read-coils payload into booleans.
func DecodeBits(payload []byte, quantity uint16) ([]bool, error) {
	if len(payload) < 1 {
		return nil, fmt.Errorf("modbus: empty coil payload")
	}
	n := int(payload[0])
	want := int((quantity + 7) / 8)
	if n != want || len(payload) < 1+n {
		return nil, fmt.Errorf("modbus: coil payload says %d bytes, wanted %d for %d coils", n, want, quantity)
	}
	out := make([]bool, quantity)
	for i := range out {
		out[i] = payload[1+i/8]&(1<<(i%8)) != 0
	}
	return out, nil
}
