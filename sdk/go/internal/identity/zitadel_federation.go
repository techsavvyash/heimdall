package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

var federationID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)

type FederationStart struct {
	AuthRequestID, IntentID, AuthorizationURL string
}

type FederationResult struct {
	Code, UpstreamSubject string
	Binding               FederatedSessionBinding
}

type ZitadelFederation struct {
	base       *url.URL
	client     *http.Client
	credential string
}

func (b *BrowserOIDC) Federation(credential string) (*ZitadelFederation, error) {
	base, err := url.Parse(b.config.Issuer)
	if err != nil || (base.Path != "" && base.Path != "/") || !visibleToken(credential, 4096) {
		return nil, contract.ErrInvalidArgument
	}
	return &ZitadelFederation{base, b.verifier.client, credential}, nil
}

func sameOrigin(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if u.Port() != "" {
			return u.Port()
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return a.Scheme == b.Scheme && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

func (f *ZitadelFederation) request(ctx context.Context, u *url.URL, authenticated bool, body any) (*http.Response, error) {
	if !sameOrigin(u, f.base) || u.User != nil || u.Fragment != "" {
		return nil, contract.ErrUnavailable
	}
	method := http.MethodGet
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, contract.ErrInvalidArgument
		}
		method, reader = http.MethodPost, bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return nil, contract.ErrUnavailable
	}
	if authenticated {
		req.Header.Set("Authorization", "Bearer "+f.credential)
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := f.client.Do(req)
	if err != nil {
		if cancellation := contract.CheckCancellation(ctx); cancellation != nil {
			return nil, cancellation
		}
		return nil, contract.ErrUnavailable
	}
	return resp, nil
}

func (f *ZitadelFederation) api(ctx context.Context, path string, body, result any) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := f.request(ctx, f.base.ResolveReference(&url.URL{Path: path}), true, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusBadRequest, http.StatusNotFound:
		return contract.ErrUnauthenticated
	case http.StatusOK, http.StatusCreated:
	default:
		return contract.ErrUnavailable
	}
	const limit = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if cancellation := contract.CheckCancellation(ctx); cancellation != nil {
		return cancellation
	}
	if err != nil || len(raw) > limit || !contract.ValidJSONUnicode(raw) || json.Unmarshal(raw, result) != nil {
		return contract.ErrUnavailable
	}
	return nil
}

func (f *ZitadelFederation) redirect(ctx context.Context, u *url.URL) (*url.URL, error) {
	resp, err := f.request(ctx, u, false, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 302, 303, 307, 308:
	default:
		return nil, contract.ErrUnavailable
	}
	if !visibleToken(resp.Header.Get("Location"), 16384) {
		return nil, contract.ErrUnavailable
	}
	location, err := resp.Location()
	if err != nil {
		return nil, contract.ErrUnavailable
	}
	return location, nil
}

func oneQuery(values url.Values, key string) string {
	if entries := values[key]; len(entries) == 1 {
		return entries[0]
	}
	return ""
}

func (f *ZitadelFederation) Start(ctx context.Context, authorization, callback, providerID, protocol string) (FederationStart, error) {
	if !federationID.MatchString(providerID) || (protocol != "saml" && protocol != "oidc") {
		return FederationStart{}, contract.ErrInvalidArgument
	}
	authorize, err := url.Parse(authorization)
	if err != nil {
		return FederationStart{}, contract.ErrInvalidArgument
	}
	login, err := f.redirect(ctx, authorize)
	if err != nil {
		return FederationStart{}, err
	}
	authRequestID := oneQuery(login.Query(), "authRequest")
	if !federationID.MatchString(authRequestID) || !strings.HasPrefix(authRequestID, "V2_") {
		return FederationStart{}, contract.ErrUnavailable
	}
	var result struct{ AuthURL string }
	err = f.api(ctx, "/v2/idp_intents", map[string]any{
		"idpId": providerID, "urls": map[string]string{"successUrl": callback, "failureUrl": callback},
	}, &result)
	if err != nil {
		return FederationStart{}, err
	}
	target, err := url.Parse(result.AuthURL)
	if err != nil || !visibleToken(result.AuthURL, 16384) || target.Scheme != "https" ||
		target.Hostname() == "" || target.User != nil || target.Fragment != "" {
		return FederationStart{}, contract.ErrUnavailable
	}
	parameter := "state"
	if protocol == "saml" {
		parameter = "RelayState"
	}
	intentID := oneQuery(target.Query(), parameter)
	if !federationID.MatchString(intentID) {
		return FederationStart{}, contract.ErrUnavailable
	}
	return FederationStart{authRequestID, intentID, result.AuthURL}, nil
}

type FederationCallback struct {
	IntentID, IntentToken, AuthRequestID, State, RedirectURI string
	ProviderID, Protocol, OrganizationID                     string
}

func (f *ZitadelFederation) Finish(ctx context.Context, in FederationCallback) (FederationResult, error) {
	if !federationID.MatchString(in.IntentID) || !visibleToken(in.IntentToken, 500) || !federationID.MatchString(in.AuthRequestID) {
		return FederationResult{}, contract.ErrUnauthenticated
	}
	var intent struct {
		UserID         string
		IDPInformation struct {
			IDPID, UserID string
			SAML          struct{ Assertion string }
			OAuth         struct{ IDToken string }
		}
	}
	err := f.api(ctx, "/v2/idp_intents/"+in.IntentID, map[string]string{"idpIntentToken": in.IntentToken}, &intent)
	if err != nil {
		return FederationResult{}, err
	}
	provider := intent.IDPInformation
	protocolMatches := (in.Protocol == "saml" && provider.SAML.Assertion != "") ||
		(in.Protocol == "oidc" && provider.OAuth.IDToken != "")
	if !federationID.MatchString(intent.UserID) || provider.IDPID != in.ProviderID || provider.UserID == "" ||
		utf8.RuneCountInString(provider.UserID) > 1024 || !protocolMatches {
		return FederationResult{}, contract.ErrUnauthenticated
	}
	var created struct{ SessionID, SessionToken string }
	err = f.api(ctx, "/v2/sessions", map[string]any{
		"checks": map[string]any{
			"user":      map[string]string{"userId": intent.UserID},
			"idpIntent": map[string]string{"idpIntentId": in.IntentID, "idpIntentToken": in.IntentToken},
		},
		"lifetime": "3600s",
	}, &created)
	if err != nil {
		return FederationResult{}, err
	}
	if !federationID.MatchString(created.SessionID) || len(created.SessionID) > 128 || !visibleToken(created.SessionToken, 500) {
		return FederationResult{}, contract.ErrUnavailable
	}
	var read struct {
		Session struct {
			ID      string
			Factors struct {
				User     struct{ ID, OrganizationID string }
				Intent   struct{ VerifiedAt time.Time }
				Password json.RawMessage
			}
		}
	}
	if err = f.api(ctx, "/v2/sessions/"+created.SessionID, nil, &read); err != nil {
		return FederationResult{}, err
	}
	factors := read.Session.Factors
	if read.Session.ID != created.SessionID || factors.User.ID != intent.UserID || factors.User.OrganizationID != in.OrganizationID ||
		factors.Intent.VerifiedAt.IsZero() || factors.Intent.VerifiedAt.UnixMilli() < 0 || factors.Password != nil {
		return FederationResult{}, contract.ErrUnauthenticated
	}
	code, err := f.productCode(ctx, in, created.SessionID, created.SessionToken)
	if err != nil {
		return FederationResult{}, err
	}
	return FederationResult{code, provider.UserID, FederatedSessionBinding{intent.UserID, created.SessionID, factors.Intent.VerifiedAt.UnixMilli()}}, nil
}

func (f *ZitadelFederation) productCode(ctx context.Context, in FederationCallback, sessionID, sessionToken string) (string, error) {
	var result struct{ CallbackURL string }
	err := f.api(ctx, "/v2/oidc/auth_requests/"+in.AuthRequestID, map[string]any{
		"session": map[string]string{"sessionId": sessionID, "sessionToken": sessionToken},
	}, &result)
	if err != nil {
		return "", err
	}
	location, err := url.Parse(result.CallbackURL)
	callback, callbackErr := url.Parse(in.RedirectURI)
	if err != nil || callbackErr != nil || !visibleToken(result.CallbackURL, 16384) {
		return "", contract.ErrUnavailable
	}
	target := func(u *url.URL) bool { return sameOrigin(u, callback) && u.EscapedPath() == callback.EscapedPath() }
	for hop := 0; hop < 4 && sameOrigin(location, f.base) && !target(location); hop++ {
		location, err = f.redirect(ctx, location)
		if err != nil {
			return "", err
		}
	}
	values, err := url.ParseQuery(location.RawQuery)
	code := oneQuery(values, "code")
	if err != nil || !target(location) || location.User != nil || location.Fragment != "" ||
		values.Has("error") || oneQuery(values, "state") != in.State || !visibleToken(code, 4096) {
		return "", contract.ErrUnauthenticated
	}
	return code, nil
}
