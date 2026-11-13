package browserlogin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"regexp"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/identity"
	"github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

type sessionAuth interface {
	Verify(context.Context, string, string) (*identity.Principal, error)
}

type sessionStore interface {
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
	digest := sha256.Sum256([]byte(opaque))
	key := base64.RawURLEncoding.EncodeToString(digest[:])
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
