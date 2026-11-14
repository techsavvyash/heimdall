// Package contract implements internal portable encodings, not authentication.
package contract

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// EncodeIdentity preserves the exact issuer/subject pair without verifying it.
func EncodeIdentity(issuer, subject string) (string, error) {
	if !validIdentityPart(issuer, 2048) || !validIdentityPart(subject, 1024) {
		return "", ErrInvalidArgument
	}
	canonical := "[" + quoteIdentity(issuer) + "," + quoteIdentity(subject) + "]"
	return "user:" + base64.RawURLEncoding.EncodeToString([]byte(canonical)), nil
}

func validIdentityPart(value string, limit int) bool {
	return len(value) > 0 && len(value) <= limit && utf8.ValidString(value)
}

func quoteIdentity(value string) string {
	var out strings.Builder
	out.WriteByte('"')
	for _, unit := range utf16.Encode([]rune(value)) {
		switch unit {
		case '"', '\\':
			out.WriteByte('\\')
			out.WriteByte(byte(unit))
		case '\b':
			out.WriteString("\\b")
		case '\f':
			out.WriteString("\\f")
		case '\n':
			out.WriteString("\\n")
		case '\r':
			out.WriteString("\\r")
		case '\t':
			out.WriteString("\\t")
		default:
			if unit < 0x20 || unit >= 0x7f {
				fmt.Fprintf(&out, "\\u%04x", unit)
			} else {
				out.WriteByte(byte(unit))
			}
		}
	}
	out.WriteByte('"')
	return out.String()
}

// DecodeIdentity accepts only the canonical encoding and does not authenticate it.
func DecodeIdentity(principal string) (Identity, error) {
	if !strings.HasPrefix(principal, "user:") || len(principal) > 32768 {
		return Identity{}, ErrUnavailable
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(strings.TrimPrefix(principal, "user:"))
	var pair []string
	if err != nil || !utf8.Valid(raw) || json.Unmarshal(raw, &pair) != nil || len(pair) != 2 {
		return Identity{}, ErrUnavailable
	}
	encoded, err := EncodeIdentity(pair[0], pair[1])
	if err != nil || encoded != principal {
		return Identity{}, ErrUnavailable
	}
	return Identity{Issuer: pair[0], Subject: pair[1]}, nil
}
