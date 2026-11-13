package browserlogin

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/identity"
	"github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
)

type loginRecords interface {
	Start(context.Context, postgres.LoginTarget) (postgres.StartedLogin, error)
	Claim(context.Context, postgres.LoginTarget, string, string) (postgres.ClaimedLogin, error)
}

type HTTP struct {
	oidc     *identity.BrowserOIDC
	records  loginRecords
	callback *url.URL
	target   postgres.LoginTarget
	prefix   string
}

var scopeToken = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var projectToken = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var stateToken = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func NewHTTP(ctx context.Context, config identity.BrowserOIDCConfig, records loginRecords, product, environment, returnToKey string) (*HTTP, error) {
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
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Location", h.oidc.AuthorizationURL(login.State, login.Nonce, login.PKCEChallenge))
	http.SetCookie(w, h.bindingCookie(login.State, login.BrowserBinding, 600))
	w.WriteHeader(http.StatusSeeOther)
	return nil
}

type CallbackResult struct {
	Tokens             *identity.BrowserTokens
	ReturnToKey        string
	ClearBindingCookie *http.Cookie
}

func (h *HTTP) Callback(r *http.Request) (*CallbackResult, error) {
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
	state, code, issuer := query["state"], query["code"], query["iss"]
	if err != nil || len(state) != 1 || !stateToken.MatchString(state[0]) || len(code) != 1 ||
		query.Has("error") || len(issuer) > 1 || (len(issuer) == 1 && issuer[0] != h.target.Issuer) {
		return nil, contract.ErrUnauthenticated
	}
	cookies := r.Header.Values("Cookie")
	if len(strings.Join(cookies, "; ")) > 16384 {
		return nil, contract.ErrUnauthenticated
	}
	name := h.prefix + state[0]
	var bindings []string
	for _, header := range cookies {
		for _, entry := range strings.Split(header, ";") {
			if value, ok := strings.CutPrefix(strings.TrimSpace(entry), name+"="); ok {
				bindings = append(bindings, value)
			}
		}
	}
	if len(bindings) != 1 {
		return nil, contract.ErrUnauthenticated
	}
	tokens, err := h.oidc.Exchange(r.Context(), code[0], func() (string, string, error) {
		login, err := h.records.Claim(r.Context(), h.target, state[0], bindings[0])
		return login.Nonce, login.PKCEVerifier, err
	})
	if err != nil {
		return nil, err
	}
	return &CallbackResult{tokens, h.target.ReturnToKey, h.bindingCookie(state[0], "", -1)}, nil
}
