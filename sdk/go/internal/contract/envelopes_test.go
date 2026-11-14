package contract

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestEnvelopeVectors(t *testing.T) {
	data, err := os.ReadFile("../../../../contracts/fixtures/crypto/v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		KeyHex, KeyID, PlaintextHex, Record, PayloadHex, Cursor, GoRecord, TsRecord string
		Scope                                                                       RecordScope
		BadRecords, BadCursors                                                      []string
		WrongScopes                                                                 []RecordScope
	}
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	key, err := hex.DecodeString(v.KeyHex)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := hex.DecodeString(v.PlaintextHex)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := hex.DecodeString(v.PayloadHex)
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewRecordCipher(v.KeyID, map[string][]byte{v.KeyID: key})
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewCursorSigner(v.KeyID, map[string][]byte{v.KeyID: key})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("golden", func(t *testing.T) {
		for _, record := range []string{v.GoRecord, v.TsRecord} {
			got, err := c.Open(v.Scope, record)
			if err != nil || !bytes.Equal(got, plain) {
				t.Fatalf("cross-writer record failed: %v", err)
			}
		}
		got, err := c.Open(v.Scope, v.Record)
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("record: %v", err)
		}
		signed, err := s.Sign(payload)
		if err != nil || signed != v.Cursor {
			t.Fatalf("signature mismatch: %v", err)
		}
		verified, err := s.Verify(v.Cursor)
		if err != nil || !bytes.Equal(verified, payload) {
			t.Fatalf("cursor: %v", err)
		}
	})
	t.Run("random-roundtrip", func(t *testing.T) {
		a, err := c.Seal(v.Scope, plain)
		if err != nil {
			t.Fatal(err)
		}
		b, err := c.Seal(v.Scope, plain)
		if err != nil {
			t.Fatal(err)
		}
		if a == b {
			t.Fatal("reused nonce")
		}
		for _, value := range []string{a, b} {
			got, err := c.Open(v.Scope, value)
			if err != nil || !bytes.Equal(got, plain) {
				t.Fatal("roundtrip failed", err)
			}
		}
	})
	t.Run("tampering", func(t *testing.T) {
		for _, bad := range v.BadRecords {
			got, err := c.Open(v.Scope, bad)
			if !errors.Is(err, ErrUnauthenticated) || got != nil {
				t.Fatal("accepted record tamper")
			}
		}
		for _, bad := range v.BadCursors {
			got, err := s.Verify(bad)
			if !errors.Is(err, ErrInvalidArgument) || got != nil {
				t.Fatal("accepted cursor tamper")
			}
		}
		for _, scope := range v.WrongScopes {
			if _, err := c.Open(scope, v.Record); !errors.Is(err, ErrUnauthenticated) {
				t.Fatal("accepted wrong context")
			}
		}
	})
	t.Run("key-id-binding", func(t *testing.T) {
		alias, _ := NewRecordCipher(v.KeyID, map[string][]byte{v.KeyID: key, "alias": key})
		if _, err := alias.Open(v.Scope, strings.Replace(v.Record, "."+v.KeyID+".", ".alias.", 1)); !errors.Is(err, ErrUnauthenticated) {
			t.Fatal("accepted changed key ID")
		}
		aliasSigner, _ := NewCursorSigner(v.KeyID, map[string][]byte{v.KeyID: key, "alias": key})
		if _, err := aliasSigner.Verify(strings.Replace(v.Cursor, "."+v.KeyID+".", ".alias.", 1)); !errors.Is(err, ErrInvalidArgument) {
			t.Fatal("accepted changed signing key ID")
		}
	})
	t.Run("rotation-copy", func(t *testing.T) {
		local := append([]byte(nil), key...)
		keys := map[string][]byte{v.KeyID: local, "key-2": bytes.Repeat([]byte{42}, 32)}
		rotated, err := NewRecordCipher("key-2", keys)
		if err != nil {
			t.Fatal(err)
		}
		signed, err := NewCursorSigner("key-2", keys)
		if err != nil {
			t.Fatal(err)
		}
		local[0] ^= 1
		if _, err := rotated.Open(v.Scope, v.Record); err != nil {
			t.Fatal(err)
		}
		if _, err := signed.Verify(v.Cursor); err != nil {
			t.Fatal(err)
		}
		next, err := rotated.Seal(v.Scope, plain)
		if err != nil || !strings.HasPrefix(next, "hr1.key-2.") {
			t.Fatal("wrong active encryption key")
		}
		next, err = signed.Sign(payload)
		if err != nil || !strings.HasPrefix(next, "hc1.key-2.") {
			t.Fatal("wrong active signing key")
		}
		delete(keys, v.KeyID)
		retired, _ := NewRecordCipher("key-2", keys)
		if _, err := retired.Open(v.Scope, v.Record); !errors.Is(err, ErrUnauthenticated) {
			t.Fatal("retired key accepted")
		}
		retiredSigner, _ := NewCursorSigner("key-2", keys)
		if _, err := retiredSigner.Verify(v.Cursor); !errors.Is(err, ErrInvalidArgument) {
			t.Fatal("retired signing key accepted")
		}
	})
	t.Run("limits", func(t *testing.T) {
		for _, size := range []int{0, recordLimit + 1} {
			if _, err := c.Seal(v.Scope, make([]byte, size)); !errors.Is(err, ErrInvalidArgument) {
				t.Fatal("record limit")
			}
		}
		for _, size := range []int{0, cursorLimit + 1} {
			if _, err := s.Sign(make([]byte, size)); !errors.Is(err, ErrInvalidArgument) {
				t.Fatal("cursor limit")
			}
		}
		if _, err := c.Open(v.Scope, strings.Repeat("x", 1400001)); !errors.Is(err, ErrUnauthenticated) {
			t.Fatal("envelope limit")
		}
		if _, err := s.Verify(strings.Repeat("x", 88001)); !errors.Is(err, ErrInvalidArgument) {
			t.Fatal("cursor envelope limit")
		}
	})
}
func TestKeyConfiguration(t *testing.T) {
	for _, size := range []int{0, 16, 31, 33} {
		if _, err := NewRecordCipher("k", map[string][]byte{"k": make([]byte, size)}); !errors.Is(err, ErrInvalidArgument) {
			t.Fatal("invalid key accepted")
		}
		if _, err := NewCursorSigner("k", map[string][]byte{"k": make([]byte, size)}); !errors.Is(err, ErrInvalidArgument) {
			t.Fatal("invalid key accepted")
		}
	}
	if _, err := NewRecordCipher("missing", map[string][]byte{"k": make([]byte, 32)}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatal("missing active key accepted")
	}
	if _, err := NewRecordCipher("k\n", map[string][]byte{"k\n": make([]byte, 32)}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatal("invalid key id accepted")
	}
}
