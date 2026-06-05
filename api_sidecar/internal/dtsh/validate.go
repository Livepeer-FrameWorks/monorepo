package dtsh

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
)

var ErrNoTracks = errors.New("dtsh has no track metadata")

func ValidateFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return Validate(data)
}

func Validate(data []byte) error {
	if len(data) < 9 {
		return fmt.Errorf("dtsh too small: %d bytes", len(data))
	}
	if string(data[:4]) != "DTSC" {
		return errors.New("dtsh missing DTSC header")
	}
	payloadLen := int(binary.BigEndian.Uint32(data[4:8]))
	if payloadLen <= 0 {
		return fmt.Errorf("dtsh invalid payload length: %d", payloadLen)
	}
	if 8+payloadLen > len(data) {
		return fmt.Errorf("dtsh truncated payload: have %d bytes, need %d", len(data)-8, payloadLen)
	}
	hasTracks, err := objectMemberIsNonEmptyObject(data[8:8+payloadLen], "tracks")
	if err != nil {
		return err
	}
	if !hasTracks {
		return ErrNoTracks
	}
	return nil
}

func objectMemberIsNonEmptyObject(data []byte, member string) (bool, error) {
	if len(data) == 0 || !isObjectMarker(data[0]) {
		return false, errors.New("dtsh payload is not a packed object")
	}
	i := 1
	for {
		if i+3 > len(data) {
			return false, errors.New("dtsh object missing terminator")
		}
		if isObjectTerminator(data, i) {
			return false, nil
		}
		if i+2 > len(data) {
			return false, errors.New("dtsh object member key truncated")
		}
		keyLen := int(binary.BigEndian.Uint16(data[i : i+2]))
		if keyLen == 0 {
			return false, errors.New("dtsh object member has empty key")
		}
		i += 2
		if i+keyLen > len(data) {
			return false, errors.New("dtsh object member key overflows payload")
		}
		key := string(data[i : i+keyLen])
		i += keyLen
		if key == member {
			return packedValueIsNonEmptyObject(data, i)
		}
		next, err := skipPackedValue(data, i)
		if err != nil {
			return false, err
		}
		i = next
	}
}

func packedValueIsNonEmptyObject(data []byte, i int) (bool, error) {
	if i >= len(data) {
		return false, errors.New("dtsh object member value missing")
	}
	if !isObjectMarker(data[i]) {
		return false, nil
	}
	if i+4 > len(data) {
		return false, errors.New("dtsh nested object truncated")
	}
	if isObjectTerminator(data, i+1) {
		return false, nil
	}
	if _, err := skipPackedValue(data, i); err != nil {
		return false, err
	}
	return true, nil
}

func skipPackedValue(data []byte, i int) (int, error) {
	if i >= len(data) {
		return 0, errors.New("dtsh packed value missing")
	}
	switch data[i] {
	case 0x01, 0x04:
		if i+9 > len(data) {
			return 0, errors.New("dtsh numeric value truncated")
		}
		return i + 9, nil
	case 0x02:
		if i+5 > len(data) {
			return 0, errors.New("dtsh string header truncated")
		}
		size := int(binary.BigEndian.Uint32(data[i+1 : i+5]))
		if size < 0 || i+5+size > len(data) {
			return 0, errors.New("dtsh string value overflows payload")
		}
		return i + 5 + size, nil
	case 0x03:
		return i + 1, nil
	case 0x0a:
		return skipPackedArray(data, i)
	case 0xe0, 0xff:
		return skipPackedObject(data, i)
	default:
		return 0, fmt.Errorf("dtsh unsupported packed value type 0x%02x", data[i])
	}
}

func skipPackedObject(data []byte, i int) (int, error) {
	if i >= len(data) || !isObjectMarker(data[i]) {
		return 0, errors.New("dtsh packed object missing")
	}
	i++
	for {
		if i+3 > len(data) {
			return 0, errors.New("dtsh object missing terminator")
		}
		if isObjectTerminator(data, i) {
			return i + 3, nil
		}
		keyLen := int(binary.BigEndian.Uint16(data[i : i+2]))
		if keyLen == 0 {
			return 0, errors.New("dtsh object member has empty key")
		}
		i += 2
		if i+keyLen > len(data) {
			return 0, errors.New("dtsh object member key overflows payload")
		}
		i += keyLen
		next, err := skipPackedValue(data, i)
		if err != nil {
			return 0, err
		}
		i = next
	}
}

func skipPackedArray(data []byte, i int) (int, error) {
	if i >= len(data) || data[i] != 0x0a {
		return 0, errors.New("dtsh packed array missing")
	}
	i++
	for {
		if i+3 > len(data) {
			return 0, errors.New("dtsh array missing terminator")
		}
		if isObjectTerminator(data, i) {
			return i + 3, nil
		}
		next, err := skipPackedValue(data, i)
		if err != nil {
			return 0, err
		}
		i = next
	}
}

func isObjectMarker(b byte) bool {
	return b == 0xe0 || b == 0xff
}

func isObjectTerminator(data []byte, i int) bool {
	return i+2 < len(data) && data[i] == 0x00 && data[i+1] == 0x00 && data[i+2] == 0xee
}
