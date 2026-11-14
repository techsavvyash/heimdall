package contract

import (
	"crypto/sha256"
	"encoding/hex"
)

const MaxRecordKeyEncryptions int64 = 4294967295

func RecordKeyFingerprint(key []byte) (string, error) {
	if len(key) != 32 {
		return "", ErrInvalidArgument
	}
	h := sha256.New()
	h.Write([]byte("heimdall.record-key-usage.v1\x00"))
	h.Write(key)
	return hex.EncodeToString(h.Sum(nil)), nil
}
func ValidateKeyUsage(fingerprint string, limit int64) error {
	if len(fingerprint) != 64 || limit < 1 || limit > MaxRecordKeyEncryptions {
		return ErrInvalidArgument
	}
	for _, c := range fingerprint {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return ErrInvalidArgument
		}
	}
	return nil
}
