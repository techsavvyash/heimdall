package contract

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

const recordLimit = 1048576
const cursorLimit = 65536

type keyRing struct {
	active string
	keys   map[string][]byte
}

func validToken(s string, limit int) bool {
	if len(s) == 0 || len(s) > limit {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
func newKeyRing(active string, keys map[string][]byte) (keyRing, error) {
	r := keyRing{active: active, keys: make(map[string][]byte, len(keys))}
	if !validToken(active, 64) || len(keys) == 0 || len(keys) > 32 {
		return r, ErrInvalidArgument
	}
	for id, key := range keys {
		if !validToken(id, 64) || len(key) != 32 {
			return r, ErrInvalidArgument
		}
		r.keys[id] = append([]byte(nil), key...)
	}
	if _, ok := r.keys[active]; !ok {
		return r, ErrInvalidArgument
	}
	return r, nil
}

type RecordScope struct{ ProductID, Environment, Kind, RecordKey string }

func recordAAD(s RecordScope, id string) ([]byte, error) {
	if !validToken(s.ProductID, 64) || !validToken(s.Environment, 64) || !validToken(s.RecordKey, 128) ||
		(s.Kind != "session" && s.Kind != "login") {
		return nil, ErrInvalidArgument
	}
	return []byte(strings.Join([]string{"heimdall.record.v1", id, s.ProductID, s.Environment, s.Kind, s.RecordKey}, "|")), nil
}
func decodeEnvelope(s string, limit int) ([]byte, error) {
	if len(s) == 0 || len(s) > (limit*4+2)/3 || !validToken(s, (limit*4+2)/3) {
		return nil, ErrInvalidArgument
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	if err != nil || len(b) > limit || base64.RawURLEncoding.EncodeToString(b) != s {
		return nil, ErrInvalidArgument
	}
	return b, nil
}

type RecordCipher struct{ ring keyRing }

func NewRecordCipher(active string, keys map[string][]byte) (*RecordCipher, error) {
	r, err := newKeyRing(active, keys)
	if err != nil {
		return nil, err
	}
	return &RecordCipher{ring: r}, nil
}
func (r *RecordCipher) Seal(scope RecordScope, plaintext []byte) (string, error) {
	aad, err := recordAAD(scope, r.ring.active)
	if err != nil {
		return "", err
	}
	if len(plaintext) == 0 || len(plaintext) > recordLimit {
		return "", ErrInvalidArgument
	}
	block, err := aes.NewCipher(r.ring.keys[r.ring.active])
	if err != nil {
		return "", ErrUnavailable
	}
	gcm, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return "", ErrUnavailable
	}
	sealed := gcm.Seal(nil, nil, plaintext, aad)
	return "hr1." + r.ring.active + "." + base64.RawURLEncoding.EncodeToString(sealed), nil
}
func (r *RecordCipher) Open(scope RecordScope, envelope string) ([]byte, error) {
	if _, err := recordAAD(scope, r.ring.active); err != nil {
		return nil, err
	}
	if len(envelope) > 1400000 {
		return nil, ErrUnauthenticated
	}
	p := strings.Split(envelope, ".")
	if len(p) != 3 || p[0] != "hr1" || !validToken(p[1], 64) {
		return nil, ErrUnauthenticated
	}
	key, ok := r.ring.keys[p[1]]
	if !ok {
		return nil, ErrUnauthenticated
	}
	b, err := decodeEnvelope(p[2], recordLimit+28)
	if err != nil || len(b) < 29 {
		return nil, ErrUnauthenticated
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrUnauthenticated
	}
	gcm, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, ErrUnauthenticated
	}
	aad, _ := recordAAD(scope, p[1])
	plaintext, err := gcm.Open(nil, nil, b, aad)
	if err != nil {
		return nil, ErrUnauthenticated
	}
	return plaintext, nil
}

type CursorSigner struct{ ring keyRing }

func NewCursorSigner(active string, keys map[string][]byte) (*CursorSigner, error) {
	r, err := newKeyRing(active, keys)
	if err != nil {
		return nil, err
	}
	return &CursorSigner{ring: r}, nil
}
func cursorMAC(key []byte, id, payload string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(strings.Join([]string{"heimdall.cursor.v1", id, payload}, "|")))
	return m.Sum(nil)
}
func (s *CursorSigner) Sign(payload []byte) (string, error) {
	if len(payload) == 0 || len(payload) > cursorLimit {
		return "", ErrInvalidArgument
	}
	p := base64.RawURLEncoding.EncodeToString(payload)
	mac := cursorMAC(s.ring.keys[s.ring.active], s.ring.active, p)
	return "hc1." + s.ring.active + "." + p + "." + base64.RawURLEncoding.EncodeToString(mac), nil
}
func (s *CursorSigner) Verify(envelope string) ([]byte, error) {
	if len(envelope) > 88000 {
		return nil, ErrInvalidArgument
	}
	p := strings.Split(envelope, ".")
	if len(p) != 4 || p[0] != "hc1" || !validToken(p[1], 64) {
		return nil, ErrInvalidArgument
	}
	key, ok := s.ring.keys[p[1]]
	if !ok {
		return nil, ErrInvalidArgument
	}
	payload, err := decodeEnvelope(p[2], cursorLimit)
	if err != nil {
		return nil, ErrInvalidArgument
	}
	mac, err := decodeEnvelope(p[3], 32)
	if err != nil || len(mac) != 32 || !hmac.Equal(mac, cursorMAC(key, p[1], p[2])) {
		return nil, ErrInvalidArgument
	}
	return payload, nil
}
