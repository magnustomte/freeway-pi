package control

import (
	"errors"
	"strings"
	"testing"

	"freewaypi/internal/modbus"
)

func TestRefusedWritesSayWhatIsProbablyWrong(t *testing.T) {
	// The message that started this: a write to coil 43 answered with server
	// device failure, because the coil reports whether a timer program is
	// running rather than controlling whether one may. "modbus exception 4"
	// is accurate and useless.
	err := explain(&modbus.Exception{Function: modbus.FCWriteMultipleCoils,
		Code: modbus.ExceptionServerDeviceFailure})
	if !strings.Contains(err.Error(), "skrivebeskyttet") {
		t.Fatalf("a refused write does not mention the likely cause: %v", err)
	}
	// And the original is still in there for anyone debugging.
	var ex *modbus.Exception
	if !errors.As(err, &ex) {
		t.Fatal("the underlying exception was lost")
	}
}

func TestTheSameCodeReadsDifferentlyOnARead(t *testing.T) {
	err := explain(&modbus.Exception{Function: modbus.FCReadCoils,
		Code: modbus.ExceptionServerDeviceFailure})
	if strings.Contains(err.Error(), "skrivebeskyttet") {
		t.Fatalf("a failed read was explained as a write problem: %v", err)
	}
}

func TestAMissingRegisterSaysSo(t *testing.T) {
	err := explain(&modbus.Exception{Function: modbus.FCReadHoldingRegisters,
		Code: modbus.ExceptionIllegalDataAddress})
	if !strings.Contains(err.Error(), "har ikke dette registeret") {
		t.Fatalf("an absent register was not explained: %v", err)
	}
}

func TestOrdinaryErrorsAreLeftAlone(t *testing.T) {
	plain := errors.New("no answer from unit")
	if got := explain(plain); got != plain {
		t.Fatalf("a transport error was rewritten: %v", got)
	}
}
