package identity

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"github.com/golang-jwt/jwt/v5"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type Config struct{ Issuer, Audience, JWKSURI, Profile string }

// VerifiedToken proves a signature, not authoritative identity state or tenant access.
type VerifiedToken struct {
	issuer, subject string
	expiresAt       int64
	owner           *TokenVerifier
}

func (t *VerifiedToken) Issuer() string   { return t.issuer }
func (t *VerifiedToken) Subject() string  { return t.subject }
func (t *VerifiedToken) ExpiresAt() int64 { return t.expiresAt }

type TokenVerifier struct {
	config                 Config
	now                    func() time.Time
	client                 *http.Client
	mu                     sync.Mutex
	keys                   map[string]*rsa.PublicKey
	fetchedAt, attemptedAt time.Time
	failed                 bool
	pending                chan struct{}
}

func NewTokenVerifier(config Config, now func() time.Time) (*TokenVerifier, error) {
	if contract.ValidateEndpoint(config.Issuer, config.Profile) != nil || contract.ValidateEndpoint(config.JWKSURI, config.Profile) != nil || len(config.Audience) == 0 || !utf8.ValidString(config.Audience) || utf8.RuneCountInString(config.Audience) > 2048 {
		return nil, contract.ErrInvalidArgument
	}
	issuer, _ := url.Parse(config.Issuer)
	jwks, _ := url.Parse(config.JWKSURI)
	// URL default ports are normalized to match the TypeScript origin comparison.
	port := func(u *url.URL) string {
		if u.Port() != "" {
			return u.Port()
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	if issuer.Scheme != jwks.Scheme || issuer.Hostname() != jwks.Hostname() || port(issuer) != port(jwks) {
		return nil, contract.ErrInvalidArgument
	}
	if now == nil {
		now = time.Now
	}
	return &TokenVerifier{config: config, now: now, client: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (v *TokenVerifier) Owns(token *VerifiedToken) bool { return token != nil && token.owner == v }

var compact = regexp.MustCompile(`^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)

func decodeInteger(value string) ([]byte, error) {
	b, e := base64.RawURLEncoding.Strict().DecodeString(value)
	if e != nil || len(b) == 0 || b[0] == 0 || base64.RawURLEncoding.EncodeToString(b) != value {
		return nil, contract.ErrUnavailable
	}
	return b, nil
}
func parseKeys(raw []byte) (map[string]*rsa.PublicKey, error) {
	var set struct{ Keys []map[string]json.RawMessage }
	if json.Unmarshal(raw, &set) != nil || len(set.Keys) == 0 || len(set.Keys) > 16 {
		return nil, contract.ErrUnavailable
	}
	result := map[string]*rsa.PublicKey{}
	for _, key := range set.Keys {
		str := func(name string) string { var value string; _ = json.Unmarshal(key[name], &value); return value }
		kid := str("kid")
		if str("kty") != "RSA" || !validKid(kid) || result[kid] != nil {
			return nil, contract.ErrUnavailable
		}
		for name, want := range map[string]string{"alg": "RS256", "use": "sig"} {
			if _, ok := key[name]; ok && str(name) != want {
				return nil, contract.ErrUnavailable
			}
		}
		if ops, ok := key["key_ops"]; ok {
			var values []string
			if json.Unmarshal(ops, &values) != nil || len(values) != 1 || values[0] != "verify" {
				return nil, contract.ErrUnavailable
			}
		}
		for _, name := range []string{"d", "p", "q", "dp", "dq", "qi", "oth"} {
			if _, ok := key[name]; ok {
				return nil, contract.ErrUnavailable
			}
		}
		n, e := decodeInteger(str("n"))
		if e != nil {
			return nil, e
		}
		exp, e := decodeInteger(str("e"))
		if e != nil || len(exp) > 4 {
			return nil, contract.ErrUnavailable
		}
		modulus := new(big.Int).SetBytes(n)
		exponent := new(big.Int).SetBytes(exp).Int64()
		if modulus.BitLen() < 2048 || modulus.BitLen() > 8192 || modulus.Bit(0) != 1 || exponent < 3 || exponent > 2147483647 || exponent%2 != 1 {
			return nil, contract.ErrUnavailable
		}
		result[kid] = &rsa.PublicKey{N: modulus, E: int(exponent)}
	}
	return result, nil
}
func (v *TokenVerifier) refresh(start time.Time, done chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var keys map[string]*rsa.PublicKey
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, v.config.JWKSURI, nil)
	req.Header.Set("Accept", "application/json")
	response, err := v.client.Do(req)
	if err == nil {
		defer response.Body.Close()
		if response.StatusCode == 200 {
			raw, e := io.ReadAll(io.LimitReader(response.Body, 262145))
			if e == nil && len(raw) <= 262144 {
				keys, err = parseKeys(raw)
			} else {
				err = contract.ErrUnavailable
			}
		} else {
			err = contract.ErrUnavailable
		}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.failed = err != nil || keys == nil
	if !v.failed {
		v.keys = keys
		v.fetchedAt = start
	}
	v.pending = nil
	close(done)
}
func (v *TokenVerifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	if !v.now().Before(v.fetchedAt) && v.now().Sub(v.fetchedAt) < 5*time.Minute && v.keys[kid] != nil {
		key := v.keys[kid]
		v.mu.Unlock()
		return key, nil
	}
	if v.pending == nil && (v.attemptedAt.IsZero() || v.now().Sub(v.attemptedAt) >= 30*time.Second) {
		v.attemptedAt = v.now()
		v.pending = make(chan struct{})
		go v.refresh(v.attemptedAt, v.pending)
	}
	pending := v.pending
	v.mu.Unlock()
	if pending != nil {
		select {
		case <-ctx.Done():
			return nil, contract.CheckCancellation(ctx)
		case <-pending:
		}
	}
	if err := contract.CheckCancellation(ctx); err != nil {
		return nil, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.now().Before(v.fetchedAt) || v.now().Sub(v.fetchedAt) >= 5*time.Minute || v.failed {
		return nil, contract.ErrUnavailable
	}
	if v.keys[kid] == nil {
		return nil, contract.ErrUnauthenticated
	}
	return v.keys[kid], nil
}
func (v *TokenVerifier) Verify(ctx context.Context, raw string) (*VerifiedToken, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := contract.CheckCancellation(ctx); err != nil {
		return nil, err
	}
	if len(raw) > 16384 || !compact.MatchString(raw) {
		return nil, contract.ErrUnauthenticated
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(strings.SplitN(raw, ".", 2)[0])
	if err != nil {
		return nil, contract.ErrUnauthenticated
	}
	var header map[string]json.RawMessage
	if json.Unmarshal(headerBytes, &header) != nil {
		return nil, contract.ErrUnauthenticated
	}
	var alg, kid string
	_ = json.Unmarshal(header["alg"], &alg)
	_ = json.Unmarshal(header["kid"], &kid)
	if alg != "RS256" || !validKid(kid) {
		return nil, contract.ErrUnauthenticated
	}
	if _, ok := header["crit"]; ok {
		return nil, contract.ErrUnauthenticated
	}
	if _, ok := header["b64"]; ok {
		return nil, contract.ErrUnauthenticated
	}
	key, err := v.key(ctx, kid)
	if err != nil {
		return nil, err
	}
	token, err := jwt.Parse(raw, func(*jwt.Token) (any, error) { return key, nil }, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(v.config.Issuer), jwt.WithAudience(v.config.Audience), jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithJSONNumber(), jwt.WithTimeFunc(v.now))
	if cancellation := contract.CheckCancellation(ctx); cancellation != nil {
		return nil, cancellation
	}
	if err != nil || !token.Valid {
		return nil, contract.ErrUnauthenticated
	}
	claims := token.Claims.(jwt.MapClaims)
	numeric := func(name string) (int64, bool) {
		n, ok := claims[name].(json.Number)
		if !ok {
			return 0, false
		}
		f, e := n.Float64()
		return int64(f), e == nil && f >= 0 && f <= 9007199254740 && math.Trunc(f) == f
	}
	exp, validExp := numeric("exp")
	iat, validIat := numeric("iat")
	sub, ok := claims["sub"].(string)
	if !ok || !validExp || !validIat || iat > v.now().Unix() || iat >= exp || exp <= v.now().Unix() {
		return nil, contract.ErrUnauthenticated
	}
	if _, exists := claims["nbf"]; exists {
		if _, valid := numeric("nbf"); !valid {
			return nil, contract.ErrUnauthenticated
		}
	}
	payloadBytes, _ := base64.RawURLEncoding.DecodeString(strings.Split(raw, ".")[1])
	var original map[string]json.RawMessage
	if json.Unmarshal(payloadBytes, &original) != nil || !validSubjectJSON(original["sub"]) {
		return nil, contract.ErrUnauthenticated
	}
	if _, err = contract.EncodeIdentity(v.config.Issuer, sub); err != nil {
		return nil, contract.ErrUnauthenticated
	}
	return &VerifiedToken{v.config.Issuer, sub, exp * 1000, v}, nil
}

func validKid(kid string) bool {
	if len(kid) == 0 || len(kid) > 128 {
		return false
	}
	for _, c := range []byte(kid) {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

// encoding/json replaces unpaired surrogate escapes; reject instead of changing identity.
func validSubjectJSON(raw []byte) bool { return contract.ValidJSONUnicode(raw) }
