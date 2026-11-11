package identity

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Identity struct{ Issuer, Subject string }
type Negative struct {
	Identity
	Context, TokenDigest, Reason string
}
type Gate struct {
	Check      func(context.Context, Identity) (bool, error)
	Invalidate func(context.Context, Negative) error
}
type StatusConfig struct {
	Config
	ProductID, Environment, PolicyVersion, ClientID string
	FreshnessMs                                     *int64
}
type Principal struct {
	identity                     Identity
	product, environment, policy string
	validatedAt, expiresAt       int64
	owner                        *IdentityVerifier
}

func (p *Principal) Identity() Identity { return p.identity }
func (p *Principal) Kind() string       { return "human" }
func (p *Principal) ValidatedAt() int64 { return p.validatedAt }
func (p *Principal) ExpiresAt() int64   { return p.expiresAt }

type cacheEntry struct {
	principal *Principal
	identity  string
	digest    string
	until     int64
}
type statusObservation struct{ identity, digest, reason string }
type IdentityVerifier struct {
	config    StatusConfig
	tokens    *TokenVerifier
	secret    func(context.Context) (string, error)
	gate      Gate
	now       func() time.Time
	freshness int64
	mu        sync.Mutex
	cache     map[string]cacheEntry
	inflight  map[*statusObservation]struct{}
}

var configToken = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var statusContext = regexp.MustCompile(`^[A-Za-z0-9:_-]{1,256}$`)

func NewIdentityVerifier(config StatusConfig, secret func(context.Context) (string, error), gate Gate, now func() time.Time) (*IdentityVerifier, error) {
	for _, v := range []string{config.ProductID, config.Environment, config.PolicyVersion, config.ClientID} {
		if !configToken.MatchString(v) {
			return nil, contract.ErrInvalidArgument
		}
	}
	if secret == nil || gate.Check == nil || gate.Invalidate == nil {
		return nil, contract.ErrInvalidArgument
	}
	freshness := int64(60000)
	if config.FreshnessMs != nil {
		freshness = *config.FreshnessMs
	}
	if freshness < 0 || freshness > 60000 {
		return nil, contract.ErrInvalidArgument
	}
	config.FreshnessMs = nil
	if now == nil {
		now = time.Now
	}
	tokens, err := NewTokenVerifier(config.Config, now)
	if err != nil {
		return nil, err
	}
	return &IdentityVerifier{config: config, tokens: tokens, secret: secret, gate: gate, now: now, freshness: freshness, cache: map[string]cacheEntry{}, inflight: map[*statusObservation]struct{}{}}, nil
}
func (v *IdentityVerifier) Owns(p *Principal) bool { return p != nil && p.owner == v }
func boundedCall[T any](ctx context.Context, action func() (T, error), mutation bool) (T, error) {
	var zero T
	if err := contract.CheckCancellation(ctx); err != nil {
		return zero, err
	}
	type result struct {
		value T
		err   error
	}
	done := make(chan result, 1)
	go func() { value, err := action(); done <- result{value, err} }()
	select {
	case <-ctx.Done():
		if mutation {
			return zero, contract.ErrOutcomeUnknown
		}
		return zero, contract.CheckCancellation(ctx)
	case r := <-done:
		if r.err != nil {
			var known *contract.Error
			if errors.As(r.err, &known) {
				return zero, known
			}
			if e := contract.CheckCancellation(ctx); e != nil {
				if mutation {
					return zero, contract.ErrOutcomeUnknown
				}
				return zero, e
			}
			return zero, contract.ErrUnavailable
		}
		return r.value, nil
	}
}
func (v *IdentityVerifier) request(ctx context.Context, path, method string, headers http.Header, body string) (int, map[string]any, error) {
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(v.config.Issuer, "/")+path, strings.NewReader(body))
	if err != nil {
		return 0, nil, contract.ErrUnavailable
	}
	request.Header = headers
	response, err := v.tokens.client.Do(request)
	if err != nil {
		if e := contract.CheckCancellation(ctx); e != nil {
			return 0, nil, e
		}
		return 0, nil, contract.ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return response.StatusCode, nil, nil
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil {
		if e := contract.CheckCancellation(ctx); e != nil {
			return 0, nil, e
		}
		return 0, nil, contract.ErrUnavailable
	}
	if len(raw) > 65536 || !validSubjectJSON(raw) {
		return 0, nil, contract.ErrUnavailable
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var data map[string]any
	if decoder.Decode(&data) != nil || data == nil {
		return 0, nil, contract.ErrUnavailable
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return 0, nil, contract.ErrUnavailable
	}
	return 200, data, nil
}
func (v *IdentityVerifier) Verify(ctx context.Context, raw, contextKey string) (*Principal, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := contract.CheckCancellation(ctx); err != nil {
		return nil, err
	}
	if !statusContext.MatchString(contextKey) {
		return nil, contract.ErrInvalidArgument
	}
	token, err := v.tokens.Verify(ctx, raw)
	if err != nil {
		return nil, err
	}
	identity := Identity{token.Issuer(), token.Subject()}
	encoded, _ := contract.EncodeIdentity(identity.Issuer, identity.Subject)
	digestBytes := sha256.Sum256([]byte(raw))
	digest := hex.EncodeToString(digestBytes[:])
	keyBytes, _ := json.Marshal([]string{v.config.ProductID, v.config.Environment, v.config.PolicyVersion, encoded, digest, contextKey})
	hash := sha256.Sum256(keyBytes)
	key := hex.EncodeToString(hash[:])
	denied, err := boundedCall(ctx, func() (bool, error) { return v.gate.Check(ctx, identity) }, false)
	if err != nil {
		return nil, err
	}
	if denied {
		return nil, contract.ErrUnauthenticated
	}
	v.mu.Lock()
	cached, ok := v.cache[key]
	if ok && v.now().UnixMilli() >= cached.principal.validatedAt && v.now().UnixMilli() < cached.until {
		v.mu.Unlock()
		return cached.principal, nil
	}
	delete(v.cache, key)
	observation := &statusObservation{identity: encoded, digest: digest}
	v.inflight[observation] = struct{}{}
	defer func() { v.mu.Lock(); delete(v.inflight, observation); v.mu.Unlock() }()
	v.mu.Unlock()
	started := v.now().UnixMilli()
	deny := func(reason string) (*Principal, error) {
		v.mu.Lock()
		for active := range v.inflight {
			if active.digest == digest || (reason != "token_inactive" && active.identity == encoded) {
				active.reason = reason
			}
		}
		for k, entry := range v.cache {
			if entry.digest == digest || (reason != "token_inactive" && entry.identity == encoded) {
				delete(v.cache, k)
			}
		}
		v.mu.Unlock()
		_, e := boundedCall(ctx, func() (bool, error) {
			return true, v.gate.Invalidate(ctx, Negative{identity, contextKey, digest, reason})
		}, true)
		if e != nil {
			return nil, e
		}
		return nil, contract.ErrUnauthenticated
	}
	secret, err := boundedCall(ctx, func() (string, error) { return v.secret(ctx) }, false)
	if err != nil {
		return nil, err
	}
	if len(secret) == 0 || len(secret) > 4096 {
		return nil, contract.ErrUnavailable
	}
	basic := base64.StdEncoding.EncodeToString([]byte(url.QueryEscape(v.config.ClientID) + ":" + url.QueryEscape(secret)))
	status, data, err := v.request(ctx, "/oauth/v2/introspect", "POST", http.Header{"Content-Type": {"application/x-www-form-urlencoded"}, "Authorization": {"Basic " + basic}}, url.Values{"token": {raw}, "token_type_hint": {"access_token"}}.Encode())
	if err != nil {
		return nil, err
	}
	active, ok := data["active"].(bool)
	if status != 200 || !ok {
		return nil, contract.ErrUnavailable
	}
	if !active {
		return deny("token_inactive")
	}
	audOK := false
	switch aud := data["aud"].(type) {
	case string:
		audOK = aud == v.config.Audience
	case []any:
		for _, entry := range aud {
			a, ok := entry.(string)
			if !ok {
				return nil, contract.ErrUnavailable
			}
			audOK = audOK || a == v.config.Audience
		}
	}
	exp, ok := data["exp"].(json.Number)
	if !ok {
		return nil, contract.ErrUnavailable
	}
	seconds, e := exp.Float64()
	if e != nil || math.Trunc(seconds) != seconds || seconds < 0 || seconds > 9007199254740 || data["iss"] != identity.Issuer || data["sub"] != identity.Subject || !audOK {
		return nil, contract.ErrUnavailable
	}
	expires := int64(seconds) * 1000
	if expires > token.ExpiresAt() {
		return nil, contract.ErrUnavailable
	}
	if expires <= v.now().UnixMilli() {
		return deny("token_inactive")
	}
	status, data, err = v.request(ctx, "/v2/users/"+url.PathEscape(identity.Subject), "GET", http.Header{"Authorization": {"Bearer " + raw}}, "")
	if err != nil {
		return nil, err
	}
	if status == 401 || status == 404 {
		return deny("token_inactive")
	}
	if status != 200 {
		return nil, contract.ErrUnavailable
	}
	user, ok := data["user"].(map[string]any)
	if !ok || user["userId"] != identity.Subject {
		return nil, contract.ErrUnavailable
	}
	if human, ok := user["human"].(map[string]any); !ok || human == nil {
		return deny("token_inactive")
	}
	if _, exists := user["machine"]; exists {
		return deny("token_inactive")
	}
	if user["state"] == "USER_STATE_DELETED" {
		return deny("identity_deleted")
	}
	if user["state"] == "USER_STATE_INACTIVE" || user["state"] == "USER_STATE_LOCKED" {
		return deny("identity_inactive")
	}
	if user["state"] != "USER_STATE_ACTIVE" {
		return nil, contract.ErrUnavailable
	}
	denied, err = boundedCall(ctx, func() (bool, error) { return v.gate.Check(ctx, identity) }, false)
	if err != nil {
		return nil, err
	}
	if denied {
		return nil, contract.ErrUnauthenticated
	}
	if e := contract.CheckCancellation(ctx); e != nil {
		return nil, e
	}
	if v.now().UnixMilli() < started {
		return nil, contract.ErrUnavailable
	}
	if v.now().UnixMilli() >= expires {
		return deny("token_inactive")
	}
	v.mu.Lock()
	reason := observation.reason
	v.mu.Unlock()
	if reason != "" {
		return deny(reason)
	}
	principal := &Principal{identity, v.config.ProductID, v.config.Environment, v.config.PolicyVersion, started, expires, v}
	until := min(started+v.freshness, expires)
	v.mu.Lock()
	if observation.reason == "" && v.now().UnixMilli() < until {
		if len(v.cache) >= 1024 {
			for existing := range v.cache {
				delete(v.cache, existing)
				break
			}
		}
		v.cache[key] = cacheEntry{principal, encoded, digest, until}
	}
	reason = observation.reason
	v.mu.Unlock()
	if reason != "" {
		return deny(reason)
	}
	return principal, nil
}
