package modbus

import "testing"

func TestCRC16CheckValue(t *testing.T) {
	// The standard check value for CRC-16/MODBUS over the ASCII digits.
	if got := CRC16([]byte("123456789")); got != 0x4B37 {
		t.Fatalf("CRC16(123456789) = %#04x, want 0x4b37", got)
	}
}

func TestAppendCRCIsLowByteFirst(t *testing.T) {
	frame := AppendCRC([]byte("123456789"))
	if n := len(frame); n != 11 {
		t.Fatalf("frame length %d, want 11", n)
	}
	if frame[9] != 0x37 || frame[10] != 0x4B {
		t.Fatalf("CRC on the wire is %#02x %#02x, want 0x37 0x4b", frame[9], frame[10])
	}
}

func TestCheckCRCRoundTrip(t *testing.T) {
	frame := AppendCRC([]byte{0x01, 0x03, 0x00, 0x28, 0x00, 0x03})
	if !CheckCRC(frame) {
		t.Fatal("CheckCRC rejected a frame produced by AppendCRC")
	}
	frame[3] ^= 0xFF
	if CheckCRC(frame) {
		t.Fatal("CheckCRC accepted a frame with a corrupted body")
	}
}

func TestCheckCRCRejectsRunts(t *testing.T) {
	for _, f := range [][]byte{nil, {}, {0x01}, {0x01, 0x03}} {
		if CheckCRC(f) {
			t.Fatalf("CheckCRC accepted a %d-byte frame", len(f))
		}
	}
}
