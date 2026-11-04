package identity

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// normalizeStrings compensates for gqlparser's older Unicode escape and block
// string handling before tokenization. Values follow GraphQL's string rules.
func normalizeStrings(query string) (string, error) {
	if !utf8.ValidString(query) {
		return "", fmt.Errorf("invalid UTF-8")
	}
	var out strings.Builder
	for i := 0; i < len(query); {
		if query[i] == '#' {
			end := i
			for end < len(query) && query[end] != '\n' && query[end] != '\r' {
				end++
			}
			out.WriteString(query[i:end])
			i = end
			continue
		}
		if query[i] != '"' {
			out.WriteByte(query[i])
			i++
			continue
		}
		block := strings.HasPrefix(query[i:], `"""`)
		start := i + 1
		if block {
			start = i + 3
		}
		end := start
		for end < len(query) {
			if block {
				if strings.HasPrefix(query[end:], `\"""`) {
					end += 4
					continue
				}
				if strings.HasPrefix(query[end:], `"""`) {
					break
				}
			} else {
				if query[end] == '\\' {
					end += 2
					continue
				}
				if query[end] == '"' {
					break
				}
			}
			end++
		}
		if end >= len(query) {
			return "", fmt.Errorf("unterminated string")
		}
		var value string
		var err error
		if block {
			value = blockValue(query[start:end])
			for _, r := range value {
				if r < 32 && r != '\n' && r != '\t' {
					return "", fmt.Errorf("invalid block string character")
				}
			}
			i = end + 3
		} else {
			value, err = quotedValue(query[start:end])
			if err != nil {
				return "", err
			}
			i = end + 1
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return "", err
		}
		out.Write(encoded)
	}
	return out.String(), nil
}

func quotedValue(raw string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(raw); {
		if raw[i] != '\\' {
			if raw[i] < 32 && raw[i] != '\t' {
				return "", fmt.Errorf("invalid string character")
			}
			out.WriteByte(raw[i])
			i++
			continue
		}
		i++
		if i >= len(raw) {
			return "", fmt.Errorf("invalid escape")
		}
		if raw[i] != 'u' {
			escapes := map[byte]byte{'"': '"', '\\': '\\', '/': '/', 'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t'}
			value, ok := escapes[raw[i]]
			if !ok {
				return "", fmt.Errorf("invalid escape")
			}
			out.WriteByte(value)
			i++
			continue
		}
		i++
		end := i + 4
		variable := i < len(raw) && raw[i] == '{'
		if variable {
			i++
			offset := strings.IndexByte(raw[i:], '}')
			if offset < 1 || offset > 8 {
				return "", fmt.Errorf("invalid Unicode escape")
			}
			end = i + offset
		}
		if end > len(raw) {
			return "", fmt.Errorf("invalid Unicode escape")
		}
		for _, digit := range raw[i:end] {
			if !strings.ContainsRune("0123456789abcdefABCDEF", digit) {
				return "", fmt.Errorf("invalid Unicode escape")
			}
		}
		point, err := strconv.ParseUint(raw[i:end], 16, 32)
		if err != nil || point > utf8.MaxRune {
			return "", fmt.Errorf("invalid Unicode escape")
		}
		i = end
		if variable {
			i++
		}
		r := rune(point)
		if !variable && r >= 0xd800 && r <= 0xdbff && i+6 <= len(raw) && raw[i:i+2] == `\u` {
			low, err := strconv.ParseUint(raw[i+2:i+6], 16, 16)
			if err == nil && low >= 0xdc00 && low <= 0xdfff {
				r = utf16.DecodeRune(r, rune(low))
				i += 6
			}
		}
		if utf16.IsSurrogate(r) {
			return "", fmt.Errorf("invalid Unicode surrogate")
		}
		out.WriteRune(r)
	}
	return out.String(), nil
}

func blockValue(raw string) string {
	raw = strings.ReplaceAll(raw, `\"""`, `"""`)
	raw = strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(raw, "\n")
	indent := -1
	for _, line := range lines[1:] {
		width := len(line) - len(strings.TrimLeft(line, " \t"))
		if width < len(line) && (indent < 0 || width < indent) {
			indent = width
		}
	}
	if indent > 0 {
		for i := 1; i < len(lines); i++ {
			lines[i] = lines[i][min(indent, len(lines[i])):]
		}
	}
	for len(lines) > 0 && strings.Trim(lines[0], " \t") == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.Trim(lines[len(lines)-1], " \t") == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}
