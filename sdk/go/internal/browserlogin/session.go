package browserlogin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/identity"
	"github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

type sessionAuth interface {
	Verify(context.Context, string, string) (*identity.Principal, error)
}

type sessionStore interface {
	GetSession(context.Context, string) (*postgres.Record, error)
	CompareAndSwapSession(context.Context, string, string, []byte, int64) (*postgres.Record, error)
	DeleteSession(context.Context, string) error
	CreateSession(context.Context, string, []byte, int64) (*postgres.Record, error)
}

type Sessions struct {
	login              *HTTP
	auth               sessionAuth
	store              sessionStore
	returnTo, cookie   string
	absoluteMs, idleMs int64
	now                func() int64
}

var returnPath = regexp.MustCompile(`^/(?:[A-Za-z0-9_-]+/)*[A-Za-z0-9_-]*$`)

func NewSessions(login *HTTP, auth sessionAuth, store sessionStore, returnTo string, absolute, idle time.Duration, now func() int64) (*Sessions, error) {
	if absolute == 0 {
		absolute = 8 * time.Hour
	}
	if idle == 0 {
		idle = 30 * time.Minute
	}
	if login == nil || auth == nil || store == nil || !returnPath.MatchString(returnTo) ||
		absolute < time.Second || idle < time.Second || idle > absolute ||
		absolute%time.Millisecond != 0 || idle%time.Millisecond != 0 {
		return nil, contract.ErrInvalidArgument
	}
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	return &Sessions{login, auth, store, returnTo, login.prefix[:len(login.prefix)-len("login-")] + "session", absolute.Milliseconds(), idle.Milliseconds(), now}, nil
}

func (s *Sessions) Start(w http.ResponseWriter, r *http.Request) error {
	return s.login.Start(w, r)
}

func (s *Sessions) Callback(w http.ResponseWriter, r *http.Request) error {
	result, err := s.login.Callback(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	tokens := result.Tokens
	actor, err := s.auth.Verify(ctx, tokens.AccessToken, "bearer")
	if err != nil {
		return err
	}
	id := actor.Identity()
	product, environment, policy := actor.Scope()
	if id.Issuer != tokens.Identity.Issuer() || id.Subject != tokens.Identity.Subject() ||
		product != s.login.target.ProductID || environment != s.login.target.Environment {
		return contract.ErrUnauthenticated
	}
	if err = contract.CheckCancellation(ctx); err != nil {
		return err
	}
	issued := s.now()
	if issued > 9007199254740991-s.absoluteMs {
		return contract.ErrInvalidArgument
	}
	accessExpires := min(actor.ExpiresAt(), tokens.AccessExpiresAt)
	if issued >= accessExpires || issued < actor.ValidatedAt() {
		return contract.ErrUnauthenticated
	}
	var raw [32]byte
	rand.Read(raw[:])
	opaque := base64.RawURLEncoding.EncodeToString(raw[:])
	key := sessionKey(opaque)
	expires := issued + s.idleMs
	payload := map[string]any{
		"schemaVersion": 1, "productId": product, "environment": environment, "issuer": id.Issuer, "subject": id.Subject,
		"policyVersion": policy, "createdAt": actor.ValidatedAt(), "lastActivityAt": issued,
		"absoluteExpiresAt": issued + s.absoluteMs, "idleExpiresAt": expires, "accessTokenExpiresAt": accessExpires,
		"identityValidatedAt": actor.ValidatedAt(), "state": "active", "accessToken": tokens.AccessToken,
	}
	if tokens.RefreshToken != "" {
		payload["refreshToken"] = tokens.RefreshToken
	}
	value, err := json.Marshal(payload)
	if err != nil {
		return contract.ErrUnavailable
	}
	stored, err := s.store.CreateSession(ctx, key, value, expires)
	if err != nil {
		return err
	}
	if err = contract.CheckCancellation(ctx); err != nil {
		return err
	}
	if stored == nil || s.now() >= min(expires, accessExpires) {
		return contract.ErrUnauthenticated
	}
	w.Header().Set("Location", s.returnTo)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.SetCookie(w, result.ClearBindingCookie)
	http.SetCookie(w, &http.Cookie{Name: s.cookie, Value: opaque, MaxAge: int(s.absoluteMs / 1000),
		Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.login.callback.Scheme == "https"})
	w.WriteHeader(http.StatusSeeOther)
	return nil
}

func sessionKey(opaque string) string {
	digest := sha256.Sum256([]byte(opaque))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func (s *Sessions) requestKey(r *http.Request) (string, error) {
	header := strings.Join(r.Header.Values("Cookie"), "; ")
	if len(header) > 16384 {
		return "", contract.ErrUnauthenticated
	}
	var opaque string
	seen := false
	for _, item := range strings.Split(header, ";") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(item), s.cookie+"="); ok {
			if seen {
				return "", contract.ErrUnauthenticated
			}
			seen, opaque = true, value
		}
	}
	if !seen {
		return "", nil
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(opaque)
	if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != opaque {
		return "", contract.ErrUnauthenticated
	}
	return sessionKey(opaque), nil
}

func (s *Sessions) Authenticate(r *http.Request) (*identity.Principal, error) {
	ctx := r.Context()
	if err := contract.CheckCancellation(ctx); err != nil {
		return nil, err
	}
	if !s.login.sameHost(r.Host) {
		return nil, contract.ErrUnauthenticated
	}
	key, err := s.requestKey(r)
	if err != nil {
		return nil, err
	}
	if key == "" {
		return nil, contract.ErrUnauthenticated
	}
	stored, err := s.store.GetSession(ctx, key)
	if err != nil {
		return nil, err
	}
	if stored == nil {
		return nil, contract.ErrUnauthenticated
	}
	payload, record, access, err := readSession(stored)
	if err != nil {
		return nil, err
	}
	target := s.login.target
	scope := record.SessionScope
	scope.ProductID, scope.Environment, scope.Issuer = target.ProductID, target.Environment, target.Issuer
	status, err := contract.SessionStatus(record, scope, s.now(), 0)
	if err != nil {
		return nil, err
	}
	switch status {
	case "refreshing", "refresh_required":
		return nil, contract.ErrUnavailable
	case "reauth_required":
		return nil, contract.ErrUnauthenticated
	}
	authContext, err := identity.SessionIdentityContext(key, stored.Revision)
	if err != nil {
		return nil, err
	}
	actor, err := s.auth.Verify(ctx, access, authContext)
	if err != nil {
		return nil, err
	}
	product, environment, policy := actor.Scope()
	id := actor.Identity()
	if product != scope.ProductID || environment != scope.Environment || policy != scope.PolicyVersion ||
		id.Issuer != scope.Issuer || id.Subject != scope.Subject {
		return nil, contract.ErrUnauthenticated
	}
	at := s.now()
	expires := min(record.AbsoluteExpiresAt, at+s.idleMs)
	accessExpires := min(record.AccessTokenExpiresAt, actor.ExpiresAt())
	if at >= min(record.IdleExpiresAt, accessExpires) || at < max(record.LastActivityAt, actor.ValidatedAt()) {
		return nil, contract.ErrUnauthenticated
	}
	payload["lastActivityAt"], payload["idleExpiresAt"], payload["accessTokenExpiresAt"], payload["identityValidatedAt"] = at, expires, accessExpires, actor.ValidatedAt()
	value, err := json.Marshal(payload)
	if err != nil {
		return nil, contract.ErrUnavailable
	}
	updated, err := s.store.CompareAndSwapSession(ctx, key, stored.Revision, value, expires)
	if err != nil {
		return nil, err
	}
	if err = contract.CheckCancellation(ctx); err != nil {
		return nil, err
	}
	if updated == nil {
		current, err := s.store.GetSession(ctx, key)
		if err != nil {
			return nil, err
		}
		if current != nil {
			return nil, contract.ErrUnavailable
		}
		return nil, contract.ErrUnauthenticated
	}
	if s.now() >= min(expires, accessExpires) {
		return nil, contract.ErrUnauthenticated
	}
	return actor, nil
}

func (s *Sessions) Logout(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := contract.CheckCancellation(ctx); err != nil {
		return err
	}
	originHost, matchesScheme := strings.CutPrefix(r.Header.Get("Origin"), s.login.callback.Scheme+"://")
	if r.Method != http.MethodPost || !s.login.sameHost(r.Host) || len(r.Header.Values("Origin")) != 1 ||
		!matchesScheme || !s.login.sameHost(originHost) {
		return contract.ErrForbidden
	}
	key, err := s.requestKey(r)
	if err != nil {
		return err
	}
	if key != "" {
		if err := s.store.DeleteSession(ctx, key); err != nil {
			return err
		}
	}
	if err := contract.CheckCancellation(ctx); err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	http.SetCookie(w, &http.Cookie{Name: s.cookie, MaxAge: -1, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.login.callback.Scheme == "https"})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func readSession(stored *postgres.Record) (map[string]any, contract.SessionRecord, string, error) {
	var payload map[string]any
	var record contract.SessionRecord
	if !contract.ValidJSONUnicode(stored.Value) || json.Unmarshal(stored.Value, &payload) != nil {
		return nil, record, "", contract.ErrUnavailable
	}
	version, ok := payload["schemaVersion"].(float64)
	if !ok || version != 1 {
		return nil, record, "", contract.ErrUnavailable
	}
	var access string
	for key, out := range map[string]*string{
		"productId": &record.ProductID, "environment": &record.Environment, "issuer": &record.Issuer, "subject": &record.Subject,
		"policyVersion": &record.PolicyVersion, "state": &record.State, "accessToken": &access,
	} {
		field, ok := payload[key].(string)
		if !ok || field == "" {
			return nil, record, "", contract.ErrUnavailable
		}
		*out = field
	}
	if field, present := payload["refreshToken"]; present {
		token, ok := field.(string)
		if !ok || token == "" {
			return nil, record, "", contract.ErrUnavailable
		}
		record.RefreshToken = token
	}
	timestamp := func(field any) (int64, bool) {
		number, ok := field.(float64)
		return int64(number), ok && number >= 0 && number <= 9007199254740991 && float64(int64(number)) == number
	}
	for key, out := range map[string]*int64{
		"createdAt": &record.CreatedAt, "lastActivityAt": &record.LastActivityAt, "absoluteExpiresAt": &record.AbsoluteExpiresAt,
		"idleExpiresAt": &record.IdleExpiresAt, "accessTokenExpiresAt": &record.AccessTokenExpiresAt, "identityValidatedAt": &record.IdentityValidatedAt,
	} {
		field, ok := timestamp(payload[key])
		if !ok {
			return nil, record, "", contract.ErrUnavailable
		}
		*out = field
	}
	attempt, present := payload["refreshAttempt"]
	switch record.State {
	case "active", "reauth_required":
		if present {
			return nil, record, "", contract.ErrUnavailable
		}
	case "refreshing":
		fields, ok := attempt.(map[string]any)
		if !ok {
			return nil, record, "", contract.ErrUnavailable
		}
		id, ok := fields["id"].(string)
		expires, valid := timestamp(fields["expiresAt"])
		if !ok || id == "" || !valid {
			return nil, record, "", contract.ErrUnavailable
		}
		record.RefreshAttempt = &struct {
			ID        string
			ExpiresAt int64
		}{id, expires}
	default:
		return nil, record, "", contract.ErrUnavailable
	}
	if record.IdleExpiresAt != stored.ExpiresAt {
		return nil, record, "", contract.ErrUnavailable
	}
	return payload, record, access, nil
}
