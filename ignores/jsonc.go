package ignores

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// parseJSONC decodes JSON that may carry a leading BOM, line and block
// comments and trailing commas in objects and arrays.
func parseJSONC(data []byte) (any, error) {
	clean, err := stripJSONC(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")))
	if err != nil {
		return nil, err
	}
	var value any
	if err := json.Unmarshal(clean, &value); err != nil {
		return nil, err
	}
	return value, nil
}

// stripJSONC blanks comments and trailing commas, leaving every other byte
// where it was so a decode error still points at the original position.
func stripJSONC(src []byte) ([]byte, error) {
	out := make([]byte, 0, len(src))
	pendingComma := -1
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case c == '"':
			end, err := stringEnd(src, i)
			if err != nil {
				return nil, err
			}
			out = append(out, src[i:end]...)
			i = end - 1
			pendingComma = -1
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				out = append(out, ' ')
				i++
			}
			if i < len(src) {
				out = append(out, '\n')
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := bytes.Index(src[i+2:], []byte("*/"))
			if end < 0 {
				return nil, fmt.Errorf("unterminated block comment at offset %d", i)
			}
			for _, b := range src[i : i+end+4] {
				if b == '\n' {
					out = append(out, '\n')
				} else {
					out = append(out, ' ')
				}
			}
			i += end + 3
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			out = append(out, c)
		default:
			if (c == '}' || c == ']') && pendingComma >= 0 {
				out[pendingComma] = ' '
			}
			pendingComma = -1
			if c == ',' {
				pendingComma = len(out)
			}
			out = append(out, c)
		}
	}
	return out, nil
}

// stringEnd returns the index just past the string that opens at src[start].
func stringEnd(src []byte, start int) (int, error) {
	for i := start + 1; i < len(src); i++ {
		switch src[i] {
		case '\\':
			i++
		case '"':
			return i + 1, nil
		case '\n':
			return 0, errors.New("unterminated string at offset " + fmt.Sprint(start))
		}
	}
	return 0, errors.New("unterminated string at offset " + fmt.Sprint(start))
}
