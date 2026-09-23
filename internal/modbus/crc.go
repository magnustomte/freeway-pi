package modbus

// CRC16 computes the Modbus RTU checksum: CRC-16/MODBUS, polynomial 0xA001
// (reversed 0x8005) with an initial value of 0xFFFF.
//
// On the wire the result is appended low byte first, which is the opposite of
// every other multi-byte field in Modbus. AppendCRC handles that.
func CRC16(buf []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, b := range buf {
		crc ^= uint16(b)
		for i := 0; i < 8; i++ {
			if crc&1 != 0 {
				crc = (crc >> 1) ^ 0xA001
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}

// AppendCRC returns buf with its CRC appended, low byte first.
func AppendCRC(buf []byte) []byte {
	crc := CRC16(buf)
	return append(buf, byte(crc), byte(crc>>8))
}

// CheckCRC reports whether frame ends in a valid CRC over its own contents.
func CheckCRC(frame []byte) bool {
	if len(frame) < 3 {
		return false
	}
	body, sum := frame[:len(frame)-2], frame[len(frame)-2:]
	crc := CRC16(body)
	return sum[0] == byte(crc) && sum[1] == byte(crc>>8)
}
