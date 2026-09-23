package modbus

import (
	"errors"
	"testing"
)

func TestBuildReadFrame(t *testing.T) {
	// Read three holding registers from address 40: the clock block, day,
	// month and year, which is the first thing fwprobe asks for.
	got := BuildRead(1, FCReadHoldingRegisters, 40, 3)
	want := AppendCRC([]byte{0x01, 0x03, 0x00, 0x28, 0x00, 0x03})
	if string(got) != string(want) {
		t.Fatalf("frame = % x, want % x", got, want)
	}
}

func TestBuildWriteSingleCoilUsesFF00(t *testing.T) {
	on := BuildWriteSingleCoil(1, 3, true)
	if on[4] != 0xFF || on[5] != 0x00 {
		t.Fatalf("on value = % x, want ff 00", on[4:6])
	}
	off := BuildWriteSingleCoil(1, 3, false)
	if off[4] != 0x00 || off[5] != 0x00 {
		t.Fatalf("off value = % x, want 00 00", off[4:6])
	}
}

func TestBuildWriteMultipleRegisters(t *testing.T) {
	got := BuildWriteMultipleRegisters(1, 56, []uint16{20, 20})
	want := AppendCRC([]byte{0x01, 0x10, 0x00, 0x38, 0x00, 0x02, 0x04, 0x00, 0x14, 0x00, 0x14})
	if string(got) != string(want) {
		t.Fatalf("frame = % x, want % x", got, want)
	}
}

func TestBuildWriteMultipleCoilsPacksLSBFirst(t *testing.T) {
	got := BuildWriteMultipleCoils(1, 0, []bool{true, false, false, true})
	if got[6] != 1 {
		t.Fatalf("byte count = %d, want 1", got[6])
	}
	if got[7] != 0b1001 {
		t.Fatalf("packed coils = %#b, want 0b1001", got[7])
	}
}

func TestResponseLen(t *testing.T) {
	cases := []struct {
		fc       byte
		quantity uint16
		want     int
	}{
		{FCReadHoldingRegisters, 3, 11},
		{FCReadHoldingRegisters, 1, 7},
		{FCReadCoils, 8, 6},
		{FCReadCoils, 9, 7},
		{FCReadCoils, 1, 6},
		{FCWriteSingleRegister, 1, 8},
		{FCWriteMultipleRegisters, 2, 8},
	}
	for _, c := range cases {
		got, err := ResponseLen(c.fc, c.quantity)
		if err != nil {
			t.Fatalf("ResponseLen(%d, %d): %v", c.fc, c.quantity, err)
		}
		if got != c.want {
			t.Errorf("ResponseLen(%d, %d) = %d, want %d", c.fc, c.quantity, got, c.want)
		}
	}
	if _, err := ResponseLen(99, 1); err == nil {
		t.Error("ResponseLen accepted an unknown function code")
	}
}

func TestParseResponseExtractsPayload(t *testing.T) {
	frame := AppendCRC([]byte{0x01, 0x03, 0x06, 0x00, 0x0C, 0x00, 0x09, 0x00, 0x1A})
	payload, err := ParseResponse(frame, 1, FCReadHoldingRegisters)
	if err != nil {
		t.Fatalf("ParseResponse: %v", err)
	}
	regs, err := DecodeRegisters(payload, 3)
	if err != nil {
		t.Fatalf("DecodeRegisters: %v", err)
	}
	want := []uint16{12, 9, 26} // 12 September 2026
	for i := range want {
		if regs[i] != want[i] {
			t.Fatalf("registers = %v, want %v", regs, want)
		}
	}
}

func TestParseResponseReportsException(t *testing.T) {
	// Holding register 710 does not exist on the unit measured; it answers with
	// illegal data address, which is how the sweep detects absent registers.
	frame := AppendCRC([]byte{0x01, 0x83, 0x02})
	_, err := ParseResponse(frame, 1, FCReadHoldingRegisters)
	var ex *Exception
	if !errors.As(err, &ex) {
		t.Fatalf("error is %v, want an *Exception", err)
	}
	if ex.Code != ExceptionIllegalDataAddress {
		t.Fatalf("exception code %d, want %d", ex.Code, ExceptionIllegalDataAddress)
	}
	if !IsIllegalDataAddress(err) {
		t.Error("IsIllegalDataAddress did not recognise its own exception")
	}
}

func TestParseResponseRejectsWrongSlaveAndFunction(t *testing.T) {
	wrongSlave := AppendCRC([]byte{0x02, 0x03, 0x02, 0x00, 0x01})
	if _, err := ParseResponse(wrongSlave, 1, FCReadHoldingRegisters); err == nil {
		t.Error("accepted an answer from the wrong slave")
	}
	wrongFC := AppendCRC([]byte{0x01, 0x04, 0x02, 0x00, 0x01})
	if _, err := ParseResponse(wrongFC, 1, FCReadHoldingRegisters); err == nil {
		t.Error("accepted an answer for the wrong function")
	}
	bad := AppendCRC([]byte{0x01, 0x03, 0x02, 0x00, 0x01})
	bad[2] ^= 0xFF
	if _, err := ParseResponse(bad, 1, FCReadHoldingRegisters); err == nil {
		t.Error("accepted a frame with a bad CRC")
	}
}

func TestDecodeBits(t *testing.T) {
	payload := []byte{0x01, 0b00001001}
	bits, err := DecodeBits(payload, 4)
	if err != nil {
		t.Fatalf("DecodeBits: %v", err)
	}
	want := []bool{true, false, false, true}
	for i := range want {
		if bits[i] != want[i] {
			t.Fatalf("bits = %v, want %v", bits, want)
		}
	}
}

func TestDecodeRegistersRejectsMismatchedCount(t *testing.T) {
	if _, err := DecodeRegisters([]byte{0x04, 0, 1, 0, 2}, 3); err == nil {
		t.Error("accepted a payload whose byte count did not match the quantity")
	}
}
