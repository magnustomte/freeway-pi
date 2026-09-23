package control

import (
	"errors"

	"freewaypi/internal/i18n"

	"freewaypi/internal/modbus"
)

// explain turns a Modbus exception into something that says what to do about
// it.
//
// "modbus exception 4 (server device failure) on write multiple coils" is
// accurate and useless. It is the unit saying it will not do this, and the
// usual reason is a register that only reports. Saying so would have saved
// working out from two register lists why a switch did not switch.
func explain(err error) error {
	var ex *modbus.Exception
	if !errors.As(err, &ex) {
		return err
	}
	writing := ex.Function == modbus.FCWriteSingleCoil ||
		ex.Function == modbus.FCWriteSingleRegister ||
		ex.Function == modbus.FCWriteMultipleCoils ||
		ex.Function == modbus.FCWriteMultipleRegisters

	switch ex.Code {
	case modbus.ExceptionIllegalDataAddress:
		return i18n.Wrap(err, "error.modbus.address", err)
	case modbus.ExceptionIllegalDataValue:
		return i18n.Wrap(err, "error.modbus.value", err)
	case modbus.ExceptionServerDeviceFailure:
		if writing {
			return i18n.Wrap(err, "error.modbus.readonly", err)
		}
		return i18n.Wrap(err, "error.modbus.failure", err)
	case modbus.ExceptionServerDeviceBusy:
		return i18n.Wrap(err, "error.modbus.busy", err)
	default:
		return err
	}
}
