package mailbox

import (
	"encoding/base64"
	"errors"
	"strings"
	"unicode/utf16"
)

// IMAP writes folder names in "modified UTF-7": printable ASCII as it is, "&" as "&-", and anything else
// as base64 of its UTF-16, between "&" and "-", with "," where base64 has "/".

var utf7 = base64.NewEncoding("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+,").WithPadding(base64.NoPadding)

func encodeFolder(name string) string {
	var b strings.Builder
	var pending []uint16
	flush := func() {
		if len(pending) == 0 {
			return
		}
		raw := make([]byte, 0, len(pending)*2)
		for _, u := range pending {
			raw = append(raw, byte(u>>8), byte(u))
		}
		b.WriteString("&" + utf7.EncodeToString(raw) + "-")
		pending = pending[:0]
	}
	for _, r := range name {
		switch {
		case r >= 0x20 && r <= 0x7e:
			flush()
			if r == '&' {
				b.WriteString("&-")
			} else {
				b.WriteRune(r)
			}
		default:
			pending = append(pending, utf16.Encode([]rune{r})...)
		}
	}
	flush()
	return b.String()
}

func decodeFolder(name string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		if name[i] != '&' {
			b.WriteByte(name[i])
			continue
		}
		end := strings.IndexByte(name[i:], '-')
		if end < 0 {
			return "", errors.New("unterminated encoded section")
		}
		chunk := name[i+1 : i+end]
		i += end
		if chunk == "" {
			b.WriteByte('&')
			continue
		}
		raw, err := utf7.DecodeString(chunk)
		if err != nil || len(raw)%2 != 0 {
			return "", errors.New("bad encoded section")
		}
		units := make([]uint16, 0, len(raw)/2)
		for j := 0; j < len(raw); j += 2 {
			units = append(units, uint16(raw[j])<<8|uint16(raw[j+1]))
		}
		b.WriteString(string(utf16.Decode(units)))
	}
	return b.String(), nil
}
