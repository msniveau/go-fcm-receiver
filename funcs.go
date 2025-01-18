package go_fcm_receiver

import (
	"errors"
)

func StringsSliceContains(s []string, e string) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}

func ReadUint32(buf []byte) (uint32, int, error) {
	value := uint32(4294967295) // Use uint32 instead of int
	pos := 0

	value = (uint32(buf[pos]) & 127) >> 0
	if buf[pos] < 128 {
		return value, pos, nil
	}
	if len(buf) < 2 {
		return uint32(pos), int(value), errors.New("not enough bytes for ReadUint32")
	}

	pos++
	value = (value | (uint32(buf[pos])&127)<<7) >> 0
	if buf[pos] < 128 {
		return value, pos, nil
	}
	if len(buf) < 3 {
		return uint32(pos), int(value), errors.New("not enough bytes for ReadUint32")
	}

	pos++
	value = (value | (uint32(buf[pos])&127)<<14) >> 0
	if buf[pos] < 128 {
		return value, pos, nil
	}
	if len(buf) < 4 {
		return uint32(pos), int(value), errors.New("not enough bytes for ReadUint32")
	}

	pos++
	value = (value | (uint32(buf[pos])&127)<<21) >> 0
	if buf[pos] < 128 {
		return value, pos, nil
	}
	if len(buf) < 5 {
		return uint32(pos), int(value), errors.New("not enough bytes for ReadUint32")
	}

	pos++
	value = (value | (uint32(buf[pos])&15)<<28) >> 0
	if buf[pos] < 128 {
		return value, pos, nil
	}

	return value, pos, nil
}

func ReadInt32(buf []byte) (int, int, error) {
	value, pos, err := ReadUint32(buf)
	return int(value | 0), pos, err
}
