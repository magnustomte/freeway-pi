package modbus

import (
	"bytes"
	"io"
	"testing"
)

func TestReadTCPFrame(t *testing.T) {
	// Transaction 0x1234, unit 1, read three holding registers from 40.
	raw := []byte{0x12, 0x34, 0x00, 0x00, 0x00, 0x06, 0x01, 0x03, 0x00, 0x28, 0x00, 0x03}
	f, err := ReadTCPFrame(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ReadTCPFrame: %v", err)
	}
	if f.Transaction != 0x1234 {
		t.Errorf("transaction = %#04x, want 0x1234", f.Transaction)
	}
	if f.Unit != 1 {
		t.Errorf("unit = %d, want 1", f.Unit)
	}
	if want := []byte{0x03, 0x00, 0x28, 0x00, 0x03}; !bytes.Equal(f.PDU, want) {
		t.Errorf("PDU = % x, want % x", f.PDU, want)
	}
}

func TestTCPFrameRoundTrip(t *testing.T) {
	in := &TCPFrame{Transaction: 7, Unit: 1, PDU: []byte{0x03, 0x02, 0x00, 0x2A}}
	out, err := ReadTCPFrame(bytes.NewReader(in.Encode()))
	if err != nil {
		t.Fatalf("ReadTCPFrame: %v", err)
	}
	if out.Transaction != in.Transaction || out.Unit != in.Unit || !bytes.Equal(out.PDU, in.PDU) {
		t.Fatalf("round trip gave %+v, want %+v", out, in)
	}
}

func TestReadTCPFrameRejectsBadHeaders(t *testing.T) {
	cases := map[string][]byte{
		"non-zero protocol id": {0, 1, 0, 9, 0, 6, 1, 3, 0, 40, 0, 3},
		"length below two":     {0, 1, 0, 0, 0, 1, 1},
	}
	for name, raw := range cases {
		if _, err := ReadTCPFrame(bytes.NewReader(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestReadTCPFrameDistinguishesCleanEndFromTruncation(t *testing.T) {
	// Nothing at all: the client hung up between frames, which is normal.
	if _, err := ReadTCPFrame(bytes.NewReader(nil)); err != io.EOF {
		t.Errorf("empty stream gave %v, want io.EOF", err)
	}
	// A header promising a body that never arrives is a broken frame.
	raw := []byte{0, 1, 0, 0, 0, 6, 1, 3}
	if _, err := ReadTCPFrame(bytes.NewReader(raw)); err != io.ErrUnexpectedEOF {
		t.Errorf("truncated body gave %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestRequestQuantity(t *testing.T) {
	cases := []struct {
		name string
		pdu  []byte
		want uint16
	}{
		{"read holding", []byte{0x03, 0x00, 0x28, 0x00, 0x03}, 3},
		{"read coils", []byte{0x01, 0x00, 0x00, 0x00, 0x50}, 80},
		{"write single register", []byte{0x06, 0x00, 0x39, 0x00, 0x15}, 1},
		{"write single coil", []byte{0x05, 0x00, 0x03, 0xFF, 0x00}, 1},
		{"write multiple registers", []byte{0x10, 0x00, 0x38, 0x00, 0x02, 0x04, 0, 20, 0, 20}, 2},
	}
	for _, c := range cases {
		got, err := RequestQuantity(c.pdu)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: quantity = %d, want %d", c.name, got, c.want)
		}
	}
	if _, err := RequestQuantity([]byte{0x2B, 0x0E, 0x01, 0x00}); err == nil {
		t.Error("accepted an unsupported function code")
	}
	if _, err := RequestQuantity([]byte{0x03, 0x00}); err == nil {
		t.Error("accepted a truncated request")
	}
}

func TestExecutePDUPassesResponsesThrough(t *testing.T) {
	answer := AppendCRC([]byte{0x01, 0x03, 0x06, 0x00, 0x0C, 0x00, 0x09, 0x00, 0x1A})
	c, f := newTestClient(answer)

	got, err := c.ExecutePDU(1, []byte{0x03, 0x00, 0x28, 0x00, 0x03})
	if err != nil {
		t.Fatalf("ExecutePDU: %v", err)
	}
	want := []byte{0x03, 0x06, 0x00, 0x0C, 0x00, 0x09, 0x00, 0x1A}
	if !bytes.Equal(got, want) {
		t.Fatalf("response PDU = % x, want % x", got, want)
	}
	if wantReq := BuildRead(1, 3, 40, 3); !bytes.Equal(f.writes[0], wantReq) {
		t.Fatalf("serial request = % x, want % x", f.writes[0], wantReq)
	}
}

func TestExecutePDUForwardsExceptionsAsAnswers(t *testing.T) {
	// A client asking for a register the unit lacks must see the unit's own
	// exception, not a gateway error. That fidelity is what makes this a
	// drop-in replacement.
	answer := AppendCRC([]byte{0x01, 0x83, 0x02})
	c, _ := newTestClient(answer)

	got, err := c.ExecutePDU(1, []byte{0x03, 0x02, 0xC6, 0x00, 0x01})
	if err != nil {
		t.Fatalf("ExecutePDU returned an error for an exception: %v", err)
	}
	if want := []byte{0x83, 0x02}; !bytes.Equal(got, want) {
		t.Fatalf("response PDU = % x, want % x", got, want)
	}
}

func TestExecutePDUReportsSilence(t *testing.T) {
	c, _ := newTestClient(nil, nil, nil)
	if _, err := c.ExecutePDU(1, []byte{0x03, 0x00, 0x28, 0x00, 0x03}); err == nil {
		t.Fatal("silence from the unit was not reported as an error")
	}
}
