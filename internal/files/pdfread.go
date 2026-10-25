package files

import (
	"bytes"
	"compress/flate"
	"encoding/ascii85"
	"errors"
	"io"
	"strings"
)

// MaxExtractChars caps extracted text: a PDF is read, not pasted whole.
const MaxExtractChars = 20000

// ExtractText returns the text-layer words of a PDF: the strings its content
// streams show with Tj and TJ, through raw, ASCII85 and Flate encodings.
// A scanned PDF has no text layer and reads as "".
func ExtractText(raw []byte) (string, error) {
	if len(raw) < 5 || string(raw[:5]) != "%PDF-" {
		return "", errors.New("not a PDF")
	}
	var out []string
	for _, st := range streams(raw) {
		decoded, ok := decodeStream(st.dict, st.data)
		if !ok {
			continue
		}
		if text := contentText(decoded); text != "" {
			out = append(out, text)
		}
	}
	joined := strings.Join(out, "\n")
	if r := []rune(joined); len(r) > MaxExtractChars {
		joined = string(r[:MaxExtractChars]) + "… [cut: the document is longer]"
	}
	return joined, nil
}

type stream struct {
	dict []byte
	data []byte
}

// streams cuts out every "stream … endstream" body with its dictionary.
func streams(raw []byte) []stream {
	var out []stream
	for i := 0; i < len(raw); {
		s := bytes.Index(raw[i:], []byte("stream"))
		if s < 0 {
			break
		}
		s += i
		// The dictionary is the << … >> just before; without one the
		// body is still tried raw.
		dict := raw[max(0, s-300):s]
		start := s + len("stream")
		for start < len(raw) && (raw[start] == '\r' || raw[start] == '\n') {
			start++
		}
		e := bytes.Index(raw[start:], []byte("endstream"))
		if e < 0 {
			break
		}
		out = append(out, stream{dict: dict, data: raw[start : start+e]})
		i = start + e + len("endstream")
	}
	return out
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// decodeStream undoes ASCII85 and Flate wrapping, outermost first. Unknown
// filters are left alone: the text scan may still find words.
func decodeStream(dict, data []byte) ([]byte, bool) {
	d := bytes.ToUpper(dict)
	// Test the dictionary text for the two filters this reader supports,
	// outermost first.
	cur := data
	if bytes.Contains(d, []byte("ASCII85DECODE")) {
		dec := make([]byte, len(cur)*4/5+10)
		n, _, err := ascii85.Decode(dec, cur, true)
		if err != nil {
			return nil, false
		}
		cur = dec[:n]
	}
	if bytes.Contains(d, []byte("FLATEDECODE")) {
		r := flate.NewReader(bytes.NewReader(cur))
		dec, err := io.ReadAll(io.LimitReader(r, MaxExtractChars*4+1024))
		r.Close()
		if err != nil {
			return nil, false
		}
		cur = dec
	}
	return cur, true
}

// contentText collects the strings a content stream shows: (literal) and
// <hex> operands of Tj and TJ.
func contentText(data []byte) string {
	var words []string
	for i := 0; i < len(data); {
		c := data[i]
		switch {
		case c == '(':
			s, next := literal(data, i)
			i = next
			if strings.TrimSpace(s) != "" {
				words = append(words, s)
			}
		case c == '<' && i+1 < len(data) && data[i+1] != '<':
			s, next := hexstr(data, i)
			i = next
			if strings.TrimSpace(s) != "" {
				words = append(words, s)
			}
		default:
			i++
		}
	}
	return strings.Join(words, " ")
}

// literal reads a (...) string from data[i], honouring \( \) \\ and \ddd.
func literal(data []byte, i int) (string, int) {
	var b strings.Builder
	i++ // (
	depth := 1
	for i < len(data) && depth > 0 {
		c := data[i]
		switch {
		case c == '\\' && i+1 < len(data):
			n := data[i+1]
			switch {
			case n >= '0' && n <= '7':
				v := 0
				j := i + 1
				for j < len(data) && j < i+4 && data[j] >= '0' && data[j] <= '7' {
					v = v*8 + int(data[j]-'0')
					j++
				}
				b.WriteByte(byte(v))
				i = j
			case n == 'n':
				b.WriteByte('\n')
				i += 2
			case n == 'r':
				b.WriteByte('\r')
				i += 2
			case n == 't':
				b.WriteByte('\t')
				i += 2
			default:
				b.WriteByte(n)
				i += 2
			}
		case c == '(':
			depth++
			b.WriteByte(c)
			i++
		case c == ')':
			depth--
			if depth > 0 {
				b.WriteByte(c)
			}
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), i
}

// hexstr reads a <…> string from data[i] as Latin-1.
func hexstr(data []byte, i int) (string, int) {
	var digits []byte
	i++ // <
	for i < len(data) && data[i] != '>' {
		c := data[i]
		if isHex(c) {
			digits = append(digits, c)
		}
		i++
	}
	if i < len(data) {
		i++ // >
	}
	var b strings.Builder
	for j := 0; j < len(digits); j += 2 {
		hi := unhex(digits[j])
		lo := 0
		if j+1 < len(digits) {
			lo = unhex(digits[j+1])
		}
		b.WriteByte(byte(hi*16 + lo))
	}
	return b.String(), i
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func unhex(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	default:
		return int(c-'A') + 10
	}
}
