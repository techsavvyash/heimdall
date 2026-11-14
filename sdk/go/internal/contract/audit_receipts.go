package contract

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"
	"sort"
	"unicode/utf8"
)

// AuditDigest follows schema validation and accepts JSON-decoded values.
func AuditDigest(event map[string]any) (string, error) {
	h := sha256.New()
	h.Write([]byte("heimdall.audit.v1\x00"))
	size := 0
	write := func(b []byte) error {
		size += len(b)
		if size > 1048576 {
			return ErrInvalidArgument
		}
		h.Write(b)
		return nil
	}
	count := func(n int) error {
		if n > 1048576 {
			return ErrInvalidArgument
		}
		return write(binary.BigEndian.AppendUint32(nil, uint32(n)))
	}
	var visit func(any, int) error
	visit = func(value any, depth int) error {
		if depth > 16 {
			return ErrInvalidArgument
		}
		switch v := value.(type) {
		case nil:
			return write([]byte("n"))
		case bool:
			if v {
				return write([]byte("t"))
			}
			return write([]byte("f"))
		case float64:
			if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > 9007199254740991 {
				return ErrInvalidArgument
			}
			if v == 0 {
				v = 0
			}
			if err := write([]byte("d")); err != nil {
				return err
			}
			return write(binary.BigEndian.AppendUint64(nil, math.Float64bits(v)))
		case string:
			if !utf8.ValidString(v) {
				return ErrInvalidArgument
			}
			if err := write([]byte("s")); err != nil {
				return err
			}
			if err := count(len(v)); err != nil {
				return err
			}
			return write([]byte(v))
		case []any:
			if err := write([]byte("a")); err != nil {
				return err
			}
			if err := count(len(v)); err != nil {
				return err
			}
			for _, item := range v {
				if err := visit(item, depth+1); err != nil {
					return err
				}
			}
			return nil
		case map[string]any:
			if err := write([]byte("o")); err != nil {
				return err
			}
			if err := count(len(v)); err != nil {
				return err
			}
			keys := make([]string, 0, len(v))
			for key := range v {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				if !validToken(key, 64) {
					return ErrInvalidArgument
				}
				if err := visit(key, depth+1); err != nil {
					return err
				}
				if err := visit(v[key], depth+1); err != nil {
					return err
				}
			}
			return nil
		default:
			return ErrInvalidArgument
		}
	}
	if err := visit(event, 0); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type AuditReceipt struct {
	ProductID, Environment, EventID, PayloadDigest string
	RecordedAt                                     int64
}
type AuditDelivery struct {
	ProductID, Environment, SinkID string
	EventIDs                       []string
}

// ValidateAuditReceipt follows structural validation; it cannot prove a commit.
func ValidateAuditReceipt(expected, received AuditReceipt) error {
	if expected != received {
		return ErrAuditUnavailable
	}
	return nil
}

// ValidateDeliveryReceipt accepts only a nonempty subset of the submitted batch.
func ValidateDeliveryReceipt(batch, receipt AuditDelivery) error {
	if batch.ProductID != receipt.ProductID || batch.Environment != receipt.Environment || batch.SinkID != receipt.SinkID || len(batch.EventIDs) == 0 || len(receipt.EventIDs) == 0 {
		return ErrUnavailable
	}
	sent := map[string]bool{}
	for _, id := range batch.EventIDs {
		if sent[id] {
			return ErrUnavailable
		}
		sent[id] = true
	}
	seen := map[string]bool{}
	for _, id := range receipt.EventIDs {
		if !sent[id] || seen[id] {
			return ErrUnavailable
		}
		seen[id] = true
	}
	return nil
}
