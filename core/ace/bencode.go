package ace

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strconv"
)

// Encode bencodes ints, strings, byte slices, lists and string-keyed dicts.
// Dict keys are written sorted by their raw bytes, per the spec.
func Encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := encodeTo(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func encodeTo(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case int:
		buf.WriteString("i" + strconv.Itoa(t) + "e")
	case int64:
		buf.WriteString("i" + strconv.FormatInt(t, 10) + "e")
	case string:
		buf.WriteString(strconv.Itoa(len(t)) + ":" + t)
	case []byte:
		buf.WriteString(strconv.Itoa(len(t)) + ":")
		buf.Write(t)
	case []any:
		buf.WriteByte('l')
		for _, item := range t {
			if err := encodeTo(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte('e')
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('d')
		for _, k := range keys {
			encodeTo(buf, k)
			if err := encodeTo(buf, t[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('e')
	default:
		return fmt.Errorf("bencode: cannot encode %T", v)
	}
	return nil
}

var errTruncated = errors.New("bencode: truncated input")

// maxDepth bounds nesting so hostile input cannot exhaust the stack.
const maxDepth = 64

// Decode decodes exactly one value spanning all of b. It yields int64, []byte,
// []any and map[string]any.
func Decode(b []byte) (any, error) {
	v, n, err := decodeAt(b, 0, 0)
	if err != nil {
		return nil, err
	}
	if n != len(b) {
		return nil, fmt.Errorf("bencode: %d trailing bytes", len(b)-n)
	}
	return v, nil
}

func decodeAt(b []byte, i, depth int) (any, int, error) {
	if i >= len(b) {
		return nil, 0, errTruncated
	}
	if depth > maxDepth {
		return nil, 0, errors.New("bencode: nesting too deep")
	}
	switch c := b[i]; {
	case c == 'i':
		end := bytes.IndexByte(b[i:], 'e')
		if end < 0 {
			return nil, 0, errTruncated
		}
		n, err := strconv.ParseInt(string(b[i+1:i+end]), 10, 64)
		if err != nil {
			return nil, 0, fmt.Errorf("bencode: bad int: %w", err)
		}
		return n, i + end + 1, nil
	case c == 'l':
		out := []any{}
		i++
		for {
			if i >= len(b) {
				return nil, 0, errTruncated
			}
			if b[i] == 'e' {
				return out, i + 1, nil
			}
			v, n, err := decodeAt(b, i, depth+1)
			if err != nil {
				return nil, 0, err
			}
			out = append(out, v)
			i = n
		}
	case c == 'd':
		out := map[string]any{}
		i++
		for {
			if i >= len(b) {
				return nil, 0, errTruncated
			}
			if b[i] == 'e' {
				return out, i + 1, nil
			}
			k, n, err := decodeAt(b, i, depth+1)
			if err != nil {
				return nil, 0, err
			}
			key, ok := k.([]byte)
			if !ok {
				return nil, 0, errors.New("bencode: dict key is not a string")
			}
			v, n, err := decodeAt(b, n, depth+1)
			if err != nil {
				return nil, 0, err
			}
			out[string(key)] = v
			i = n
		}
	case c >= '0' && c <= '9':
		colon := bytes.IndexByte(b[i:], ':')
		if colon < 0 {
			return nil, 0, errTruncated
		}
		n, err := strconv.Atoi(string(b[i : i+colon]))
		if err != nil || n < 0 {
			return nil, 0, fmt.Errorf("bencode: bad string length %q", b[i:i+colon])
		}
		start := i + colon + 1
		if n > len(b)-start {
			return nil, 0, errTruncated
		}
		return b[start : start+n], start + n, nil
	default:
		return nil, 0, fmt.Errorf("bencode: unexpected byte %q at %d", c, i)
	}
}

// dictInt reads an integer field, tolerating its absence.
func dictInt(d map[string]any, key string) (int64, bool) {
	v, ok := d[key].(int64)
	return v, ok
}
