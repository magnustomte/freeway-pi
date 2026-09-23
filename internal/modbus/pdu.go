package modbus

import "fmt"

// Function codes. Freeway WEB answers WriteSingleCoil and WriteSingleRegister
// with a well-formed echo but never passes the write to the unit, which is why
// the Homey driver writes with the "multiple" variants. Whether that is Freeway
// or the EDA board is what fwprobe's fc6 test answers.
const (
	FCReadCoils              = 1
	FCReadDiscreteInputs     = 2
	FCReadHoldingRegisters   = 3
	FCReadInputRegisters     = 4
	FCWriteSingleCoil        = 5
	FCWriteSingleRegister    = 6
	FCWriteMultipleCoils     = 15
	FCWriteMultipleRegisters = 16
)

// FunctionName renders a function code for humans.
func FunctionName(fc byte) string {
	switch fc {
	case FCReadCoils:
		return "read coils"
	case FCReadDiscreteInputs:
		return "read discrete inputs"
	case FCReadHoldingRegisters:
		return "read holding registers"
	case FCReadInputRegisters:
		return "read input registers"
	case FCWriteSingleCoil:
		return "write single coil"
	case FCWriteSingleRegister:
		return "write single register"
	case FCWriteMultipleCoils:
		return "write multiple coils"
	case FCWriteMultipleRegisters:
		return "write multiple registers"
	default:
		return fmt.Sprintf("function %d", fc)
	}
}

// Exception is a Modbus exception response from the slave. It is a normal
// answer, not a transport failure: the unit understood the request and is
// declining it. Reading a register this unit does not have gives
// ExceptionIllegalDataAddress, which is how fwprobe's sweep tells present
// registers from absent ones.
type Exception struct {
	Function byte
	Code     byte
}

const (
	ExceptionIllegalFunction         = 1
	ExceptionIllegalDataAddress      = 2
	ExceptionIllegalDataValue        = 3
	ExceptionServerDeviceFailure     = 4
	ExceptionAcknowledge             = 5
	ExceptionServerDeviceBusy        = 6
	ExceptionMemoryParityError       = 8
	ExceptionGatewayPathUnavailable  = 10
	ExceptionGatewayTargetNoResponse = 11
)

func (e *Exception) Error() string {
	return fmt.Sprintf("modbus exception %d (%s) on %s", e.Code, exceptionText(e.Code), FunctionName(e.Function))
}

func exceptionText(code byte) string {
	switch code {
	case ExceptionIllegalFunction:
		return "illegal function"
	case ExceptionIllegalDataAddress:
		return "illegal data address"
	case ExceptionIllegalDataValue:
		return "illegal data value"
	case ExceptionServerDeviceFailure:
		return "server device failure"
	case ExceptionAcknowledge:
		return "acknowledge"
	case ExceptionServerDeviceBusy:
		return "server device busy"
	case ExceptionMemoryParityError:
		return "memory parity error"
	case ExceptionGatewayPathUnavailable:
		return "gateway path unavailable"
	case ExceptionGatewayTargetNoResponse:
		return "gateway target device failed to respond"
	default:
		return "unknown"
	}
}

// IsIllegalDataAddress reports whether err is the unit saying the register does
// not exist.
func IsIllegalDataAddress(err error) bool {
	e, ok := err.(*Exception)
	return ok && e.Code == ExceptionIllegalDataAddress
}
