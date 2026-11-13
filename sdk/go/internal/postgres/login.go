package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

const loginLifetimeMs = 600_000

type LoginTarget struct {
	ProductID, Environment, Issuer, ClientID, RedirectURI, ReturnToKey string
	TenantPolicy                                                       *contract.LoginPolicy
}

func (t LoginTarget) scope(browserBinding string) contract.LoginScope {
	digest := sha256.Sum256([]byte(browserBinding))
	return contract.LoginScope{
		ProductID: t.ProductID, Environment: t.Environment, Issuer: t.Issuer, ClientID: t.ClientID,
		RedirectURI: t.RedirectURI, ReturnToKey: t.ReturnToKey, TenantPolicy: t.TenantPolicy,
		BrowserBindingDigest: hex.EncodeToString(digest[:]),
	}
}

type browserLoginPayload struct {
	contract.LoginRecord
	SchemaVersion int    `json:"schemaVersion"`
	Nonce         string `json:"nonce"`
	PKCEVerifier  string `json:"pkceVerifier"`
}

type StartedLogin struct {
	State, BrowserBinding, Nonce, PKCEChallenge string
	ExpiresAt                                   int64
}

type ClaimedLogin struct {
	Nonce, PKCEVerifier string
}

type loginStore interface {
	MatchesScope(string, string) bool
	Create(context.Context, string, string, []byte, int64) (*Record, error)
	Get(context.Context, string, string) (*Record, error)
	CompareAndSwap(context.Context, string, string, string, []byte, int64) (*Record, error)
}

type BrowserLoginRecords struct {
	store loginStore
	now   func() int64
}

func NewBrowserLoginRecords(store loginStore, now func() int64) *BrowserLoginRecords {
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	return &BrowserLoginRecords{store, now}
}

var verifierPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func loginText(value string, max int) bool {
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= max
}

func validLoginScope(s contract.LoginScope) bool {
	if !valid(s.ProductID, 64) || !valid(s.Environment, 64) || !loginText(s.Issuer, 2048) ||
		!contract.ValidClientID(s.ClientID) || !loginText(s.RedirectURI, 2048) || !valid(s.ReturnToKey, 64) {
		return false
	}
	p := s.TenantPolicy
	if p == nil {
		return true
	}
	return valid(p.TenantID, 128) && valid(p.OrganizationID, 128) && valid(p.PolicyVersion, 128) &&
		((!p.StrictSSO && p.ProviderID == "" && p.Protocol == "") ||
			(loginText(p.ProviderID, 128) && (p.Protocol == "saml" || p.Protocol == "oidc")))
}

// JSON maps check exact property names and presence before Go's typed decoding,
// which otherwise permits case-insensitive keys and silently maps null to zero.
func loginFields(raw []byte, required, optional []string) map[string]json.RawMessage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	for _, key := range required {
		if fields[key] == nil {
			return nil
		}
	}
	if len(fields) != len(required) {
		count := len(required)
		for _, key := range optional {
			if fields[key] != nil {
				count++
			}
		}
		if count != len(fields) {
			return nil
		}
	}
	for key, value := range fields {
		if key != "tenantPolicy" && string(value) == "null" {
			return nil
		}
	}
	return fields
}

func decodeBrowserLogin(stored *Record) (browserLoginPayload, error) {
	var record browserLoginPayload
	if !contract.ValidJSONUnicode(stored.Value) {
		return record, contract.ErrUnavailable
	}
	fields := loginFields(stored.Value, []string{
		"schemaVersion", "productId", "environment", "issuer", "clientId", "redirectUri",
		"returnToKey", "browserBindingDigest", "tenantPolicy", "createdAt", "expiresAt", "nonce", "pkceVerifier", "state",
	}, []string{"claimedAt"})
	if fields == nil || json.Unmarshal(stored.Value, &record) != nil || record.SchemaVersion != 1 ||
		!validLoginScope(record.LoginScope) || record.CreatedAt < 0 || record.CreatedAt > maxSafe ||
		record.ExpiresAt < 0 || record.ExpiresAt > maxSafe || record.ExpiresAt != stored.ExpiresAt ||
		!digestPattern.MatchString(record.BrowserBindingDigest) || len(record.Nonce) < 43 || !valid(record.Nonce, 128) ||
		!verifierPattern.MatchString(record.PKCEVerifier) {
		return browserLoginPayload{}, contract.ErrUnavailable
	}
	if record.TenantPolicy != nil {
		policy := loginFields(fields["tenantPolicy"], []string{"tenantId", "organizationId", "policyVersion", "strictSSO"}, []string{"providerId", "protocol"})
		if policy == nil || (policy["providerId"] != nil) != (record.TenantPolicy.ProviderID != "") ||
			(policy["protocol"] != nil) != (record.TenantPolicy.Protocol != "") {
			return browserLoginPayload{}, contract.ErrUnavailable
		}
	}
	if (record.State != "pending" || record.ClaimedAt != nil) &&
		(record.State != "claimed" || record.ClaimedAt == nil || *record.ClaimedAt < 0 || *record.ClaimedAt > maxSafe) {
		return browserLoginPayload{}, contract.ErrUnavailable
	}
	return record, nil
}

func loginSecret() string {
	var value [32]byte
	rand.Read(value[:])
	return base64.RawURLEncoding.EncodeToString(value[:])
}

func validLoginSecret(value string) bool {
	if len(value) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return err == nil && len(decoded) == 32
}

func (l *BrowserLoginRecords) Start(ctx context.Context, target LoginTarget) (StartedLogin, error) {
	if err := contract.CheckCancellation(ctx); err != nil {
		return StartedLogin{}, err
	}
	created := l.now()
	state, binding := loginSecret(), loginSecret()
	scope := target.scope(binding)
	if created < 0 || created > maxSafe-loginLifetimeMs || !validLoginScope(scope) || !l.store.MatchesScope(target.ProductID, target.Environment) {
		return StartedLogin{}, contract.ErrInvalidArgument
	}
	expires := created + loginLifetimeMs
	nonce, verifier := loginSecret(), loginSecret()
	payload := browserLoginPayload{
		LoginRecord:   contract.LoginRecord{LoginScope: scope, CreatedAt: created, ExpiresAt: expires, State: "pending"},
		SchemaVersion: 1, Nonce: nonce, PKCEVerifier: verifier,
	}
	value, err := json.Marshal(payload)
	if err != nil {
		return StartedLogin{}, contract.ErrInvalidArgument
	}
	stored, err := l.store.Create(ctx, "login", state, value, expires)
	if err != nil {
		return StartedLogin{}, err
	}
	if stored == nil {
		return StartedLogin{}, contract.ErrUnauthenticated
	}
	if err := contract.CheckCancellation(ctx); err != nil {
		return StartedLogin{}, err
	}
	challenge := sha256.Sum256([]byte(verifier))
	return StartedLogin{state, binding, nonce, base64.RawURLEncoding.EncodeToString(challenge[:]), expires}, nil
}

// Claim must return successfully before the caller attempts code exchange.
// Target comes from trusted routing and current tenant policy.
func (l *BrowserLoginRecords) Claim(ctx context.Context, target LoginTarget, state, binding string) (ClaimedLogin, error) {
	if err := contract.CheckCancellation(ctx); err != nil {
		return ClaimedLogin{}, err
	}
	if !validLoginSecret(state) || !validLoginSecret(binding) {
		return ClaimedLogin{}, contract.ErrUnauthenticated
	}
	scope := target.scope(binding)
	if !validLoginScope(scope) {
		return ClaimedLogin{}, contract.ErrInvalidArgument
	}
	if !l.store.MatchesScope(target.ProductID, target.Environment) {
		return ClaimedLogin{}, contract.ErrUnauthenticated
	}
	stored, err := l.store.Get(ctx, "login", state)
	if err != nil {
		return ClaimedLogin{}, err
	}
	if stored == nil {
		return ClaimedLogin{}, contract.ErrUnauthenticated
	}
	payload, err := decodeBrowserLogin(stored)
	if err != nil {
		return ClaimedLogin{}, err
	}
	now := l.now()
	if err := contract.ValidateLoginClaim(payload.LoginRecord, scope, now); err != nil {
		return ClaimedLogin{}, err
	}
	payload.State, payload.ClaimedAt = "claimed", &now
	value, err := json.Marshal(payload)
	if err != nil {
		return ClaimedLogin{}, contract.ErrUnavailable
	}
	updated, err := l.store.CompareAndSwap(ctx, "login", state, stored.Revision, value, payload.ExpiresAt)
	if err != nil {
		return ClaimedLogin{}, err
	}
	if updated == nil {
		return ClaimedLogin{}, contract.ErrUnauthenticated
	}
	if err := contract.CheckCancellation(ctx); err != nil {
		return ClaimedLogin{}, err
	}
	return ClaimedLogin{payload.Nonce, payload.PKCEVerifier}, nil
}
