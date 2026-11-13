package browserlogin

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/identity"
	"github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

type LoginRecords interface {
	Start(context.Context, postgres.LoginTarget) (postgres.StartedLogin, error)
	Claim(context.Context, postgres.LoginTarget, string, string) (postgres.ClaimedLogin, error)
}

type federationLoginRecords interface {
	StartFederation(context.Context, postgres.LoginTarget, func(string, string, string, int64) (postgres.LoginFederation, string, error)) (postgres.StartedLogin, string, error)
	ClaimFederation(context.Context, postgres.LoginTarget, string, string, string) (postgres.ClaimedFederation, error)
}

type HTTP struct {
	oidc     *identity.BrowserOIDC
	records  LoginRecords
	callback *url.URL
	target   postgres.LoginTarget
	prefix   string
}

var scopeToken = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var projectToken = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var stateToken = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func NewHTTP(ctx context.Context, config identity.BrowserOIDCConfig, records LoginRecords, product, environment, returnToKey string) (*HTTP, error) {
	if ctx == nil || records == nil || !scopeToken.MatchString(product) || !scopeToken.MatchString(environment) || !scopeToken.MatchString(returnToKey) || !projectToken.MatchString(config.Audience) {
		return nil, contract.ErrInvalidArgument
	}
	oidc, err := identity.NewBrowserOIDC(ctx, config)
	if err != nil {
		return nil, err
	}
	callback, _ := url.Parse(config.RedirectURI)
	prefix := "heimdall-" + strconv.Itoa(len(product)) + "-" + product + "-" + environment + "-login-"
	if callback.Scheme == "https" {
		prefix = "__Host-" + prefix
	}
	target := postgres.LoginTarget{ProductID: product, Environment: environment, Issuer: config.Issuer, ClientID: config.ClientID, RedirectURI: config.RedirectURI, ReturnToKey: returnToKey}
	return &HTTP{oidc, records, callback, target, prefix}, nil
}

func (h *HTTP) bindingCookie(state, value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: h.prefix + state, Value: value, MaxAge: maxAge,
		Secure: h.callback.Scheme == "https", HttpOnly: true, Path: "/", SameSite: http.SameSiteLaxMode}
}

func (h *HTTP) sameHost(host string) bool {
	candidate, err := url.Parse(h.callback.Scheme + "://" + host)
	if err != nil || candidate.User != nil || candidate.Path != "" || candidate.RawQuery != "" || candidate.Fragment != "" {
		return false
	}
	port := func(u *url.URL) string {
		if u.Port() != "" {
			return u.Port()
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return strings.EqualFold(candidate.Hostname(), h.callback.Hostname()) && port(candidate) == port(h.callback)
}

func (h *HTTP) Start(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodGet || !h.sameHost(r.Host) {
		return contract.ErrInvalidArgument
	}
	login, err := h.records.Start(r.Context(), h.target)
	if err != nil {
		return err
	}
	h.redirectToLogin(w, login, h.oidc.AuthorizationURL(login.State, login.Nonce, login.PKCEChallenge))
	return nil
}

func (h *HTTP) redirectToLogin(w http.ResponseWriter, login postgres.StartedLogin, location string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Location", location)
	http.SetCookie(w, h.bindingCookie(login.State, login.BrowserBinding, 600))
	w.WriteHeader(http.StatusSeeOther)
}

type CallbackResult struct {
	Tokens             *identity.BrowserTokens
	ReturnToKey        string
	ClearBindingCookie *http.Cookie
	EnterpriseEvidence *contract.EnterpriseEvidence
	ExpiresAt          int64
}

func (h *HTTP) Callback(r *http.Request) (*CallbackResult, error) {
	query, err := h.callbackQuery(r)
	if err != nil {
		return nil, err
	}
	state, code, issuer := query["state"], query["code"], query["iss"]
	if len(code) != 1 ||
		query.Has("error") || len(issuer) > 1 || (len(issuer) == 1 && issuer[0] != h.target.Issuer) {
		return nil, contract.ErrUnauthenticated
	}
	binding, err := h.browserBinding(r, state[0])
	if err != nil {
		return nil, err
	}
	tokens, err := h.oidc.Exchange(r.Context(), code[0], func() (string, string, error) {
		login, err := h.records.Claim(r.Context(), h.target, state[0], binding)
		return login.Nonce, login.PKCEVerifier, err
	})
	if err != nil {
		return nil, err
	}
	return &CallbackResult{Tokens: tokens, ReturnToKey: h.target.ReturnToKey, ClearBindingCookie: h.bindingCookie(state[0], "", -1)}, nil
}

func (h *HTTP) callbackQuery(r *http.Request) (url.Values, error) {
	if err := contract.CheckCancellation(r.Context()); err != nil {
		return nil, err
	}
	// The backend may sit behind a TLS proxy. Compare the configured route without
	// deriving the issuer, redirect URI or cookie flags from forwarded headers.
	path := h.callback.EscapedPath()
	if path == "" {
		path = "/"
	}
	if r.Method != http.MethodGet || !h.sameHost(r.Host) ||
		r.URL.EscapedPath() != path || len(r.URL.RawQuery) > 16383 {
		return nil, contract.ErrUnauthenticated
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	state := query["state"]
	if err != nil || len(state) != 1 || !stateToken.MatchString(state[0]) {
		return nil, contract.ErrUnauthenticated
	}
	return query, nil
}

func (h *HTTP) browserBinding(r *http.Request, state string) (string, error) {
	cookies := r.Header.Values("Cookie")
	if len(strings.Join(cookies, "; ")) > 16384 {
		return "", contract.ErrUnauthenticated
	}
	name := h.prefix + state
	var bindings []string
	for _, header := range cookies {
		for _, entry := range strings.Split(header, ";") {
			if value, ok := strings.CutPrefix(strings.TrimSpace(entry), name+"="); ok {
				bindings = append(bindings, value)
			}
		}
	}
	if len(bindings) != 1 {
		return "", contract.ErrUnauthenticated
	}
	return bindings[0], nil
}

func (h *HTTP) StartFederation(w http.ResponseWriter, r *http.Request, policy contract.LoginPolicy, credential string) error {
	records, supported := h.records.(federationLoginRecords)
	if !supported || r.Method != http.MethodGet || !h.sameHost(r.Host) {
		return contract.ErrInvalidArgument
	}
	federation, err := h.oidc.Federation(credential)
	if err != nil {
		return err
	}
	target := h.target
	target.TenantPolicy = &policy
	login, location, err := records.StartFederation(r.Context(), target, func(state, nonce, challenge string, _ int64) (postgres.LoginFederation, string, error) {
		authorization, err := h.oidc.FederatedAuthorizationURL(state, nonce, challenge, policy.OrganizationID)
		if err != nil {
			return postgres.LoginFederation{}, "", err
		}
		callback := *h.callback
		callback.RawQuery = url.Values{"state": {state}, "tenant": {policy.TenantID}}.Encode()
		prepared, err := federation.Start(r.Context(), authorization, callback.String(), policy.ProviderID, policy.Protocol)
		return postgres.LoginFederation{IntentID: prepared.IntentID, AuthRequestID: prepared.AuthRequestID}, prepared.AuthorizationURL, err
	})
	if err != nil {
		return err
	}
	h.redirectToLogin(w, login, location)
	return nil
}

func (h *HTTP) FederationCallback(r *http.Request, policy contract.LoginPolicy, maxAgeMS int64, credential string) (*CallbackResult, error) {
	query, err := h.callbackQuery(r)
	if err != nil {
		return nil, err
	}
	records, supported := h.records.(federationLoginRecords)
	if !supported || len(query["tenant"]) != 1 || query.Get("tenant") != policy.TenantID ||
		len(query["id"]) != 1 || len(query["token"]) != 1 || query.Has("error") {
		return nil, contract.ErrUnauthenticated
	}
	state := query.Get("state")
	binding, err := h.browserBinding(r, state)
	if err != nil {
		return nil, err
	}
	target := h.target
	target.TenantPolicy = &policy
	claimed, err := records.ClaimFederation(r.Context(), target, state, binding, query.Get("id"))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithDeadline(r.Context(), time.UnixMilli(claimed.ExpiresAt))
	defer cancel()
	federation, err := h.oidc.Federation(credential)
	if err != nil {
		return nil, err
	}
	verified, err := federation.Finish(ctx, identity.FederationCallback{
		IntentID: query.Get("id"), IntentToken: query.Get("token"), AuthRequestID: claimed.AuthRequestID,
		State: state, RedirectURI: target.RedirectURI, ProviderID: policy.ProviderID,
		Protocol: policy.Protocol, OrganizationID: policy.OrganizationID,
	})
	if err != nil {
		return nil, err
	}
	tokens, err := h.oidc.ExchangeFederated(ctx, verified.Code, claimed.Nonce, claimed.PKCEVerifier, verified.Binding)
	if err != nil {
		return nil, err
	}
	if err := contract.CheckCancellation(ctx); err != nil {
		return nil, err
	}
	scope := contract.EnterpriseScope{
		ProductID: target.ProductID, Environment: target.Environment, ClientID: target.ClientID, Issuer: target.Issuer,
		Subject: verified.Binding.Subject, TenantID: policy.TenantID, OrganizationID: policy.OrganizationID,
		PolicyVersion: policy.PolicyVersion, ProviderID: policy.ProviderID, Protocol: policy.Protocol,
	}
	evidence := &contract.EnterpriseEvidence{
		EnterpriseScope: scope, UpstreamSubject: verified.UpstreamSubject, ProviderSessionID: verified.Binding.SessionID,
		VerifiedAt: verified.Binding.VerifiedAt, AuthTime: verified.Binding.VerifiedAt / 1000 * 1000,
	}
	if err := contract.ValidateEnterpriseEvidence(evidence, scope, time.Now().UnixMilli(), maxAgeMS); err != nil {
		return nil, err
	}
	return &CallbackResult{
		Tokens: tokens, ReturnToKey: target.ReturnToKey, ClearBindingCookie: h.bindingCookie(state, "", -1),
		EnterpriseEvidence: evidence, ExpiresAt: claimed.ExpiresAt,
	}, nil
}
