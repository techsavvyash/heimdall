package identity

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

type BrowserOIDCConfig struct {
	Issuer, Profile, ClientID, Audience, ClientSecret, RedirectURI string
	ZitadelAPIAudience                                             string
}

type BrowserOIDC struct {
	tokenEndpoint, authorizationEndpoint string
	config                               BrowserOIDCConfig
	verifier                             *TokenVerifier
}

func visibleToken(value string, max int) bool {
	if len(value) == 0 || len(value) > max {
		return false
	}
	for i := range len(value) {
		if value[i] < 0x21 || value[i] > 0x7e {
			return false
		}
	}
	return true
}

func NewBrowserOIDC(ctx context.Context, config BrowserOIDCConfig) (*BrowserOIDC, error) {
	if contract.ValidateEndpoint(config.RedirectURI, config.Profile) != nil ||
		!visibleToken(config.ClientID, 128) || !visibleToken(config.ClientSecret, 4096) || (config.ZitadelAPIAudience != "" && !visibleToken(config.ZitadelAPIAudience, 128)) {
		return nil, contract.ErrInvalidArgument
	}
	metadata, err := DiscoverOIDC(ctx, config.Issuer, config.Profile)
	if err != nil {
		return nil, err
	}
	verifier, err := NewTokenVerifier(Config{Issuer: config.Issuer, Audience: config.Audience, JWKSURI: metadata.JWKSURI, Profile: config.Profile}, nil)
	if err != nil {
		return nil, err
	}
	return &BrowserOIDC{metadata.TokenEndpoint, metadata.AuthorizationEndpoint, config, verifier}, nil
}

func (b *BrowserOIDC) AuthorizationURL(state, nonce, pkceChallenge string) string {
	target, _ := url.Parse(b.authorizationEndpoint)
	scope := "openid profile offline_access urn:zitadel:iam:org:project:id:" + b.config.Audience + ":aud"
	if b.config.ZitadelAPIAudience != "" {
		scope += " urn:zitadel:iam:org:project:id:zitadel:aud"
	}
	target.RawQuery = url.Values{
		"client_id": {b.config.ClientID}, "redirect_uri": {b.config.RedirectURI}, "response_type": {"code"},
		"scope": {scope},
		"state": {state}, "nonce": {nonce}, "code_challenge": {pkceChallenge}, "code_challenge_method": {"S256"},
	}.Encode()
	return target.String()
}

type BrowserTokens struct {
	Identity                  *VerifiedToken
	AccessToken, RefreshToken string
	AccessExpiresAt           int64
}

// Exchange dispatches a single code request after the callback confirms its claim.
func (b *BrowserOIDC) Exchange(ctx context.Context, code string, claim func() (nonce, pkceVerifier string, err error)) (*BrowserTokens, error) {
	if err := contract.CheckCancellation(ctx); err != nil {
		return nil, err
	}
	if !visibleToken(code, 4096) {
		return nil, contract.ErrUnauthenticated
	}
	nonce, verifier, err := claim()
	if err != nil {
		return nil, err
	}
	tokens, err := b.requestTokens(ctx, url.Values{
		"grant_type": {"authorization_code"}, "code": {code},
		"redirect_uri": {b.config.RedirectURI}, "code_verifier": {verifier},
	})
	if err != nil {
		return nil, err
	}
	if !visibleToken(tokens.IDToken, 16384) {
		return nil, contract.ErrUnavailable
	}
	identity, err := b.verifier.verifyIDToken(ctx, tokens.IDToken, b.config.ClientID, nonce, tokens.AccessToken, b.config.ZitadelAPIAudience)
	if err != nil {
		return nil, err
	}
	access, err := b.verifier.Verify(ctx, tokens.AccessToken)
	if err != nil {
		return nil, err
	}
	if identity.Subject() != access.Subject() {
		return nil, contract.ErrUnauthenticated
	}
	return &BrowserTokens{identity, tokens.AccessToken, tokens.RefreshToken, access.ExpiresAt()}, nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
}

// Refresh returns rotating token material. The session layer verifies and audits
// the new access token against the original session identity before persisting it.
func (b *BrowserOIDC) Refresh(ctx context.Context, refreshToken string) (access, replacement string, err error) {
	if !visibleToken(refreshToken, 16384) {
		return "", "", contract.ErrUnauthenticated
	}
	tokens, err := b.requestTokens(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}})
	if err != nil {
		return "", "", err
	}
	if tokens.RefreshToken == "" || tokens.RefreshToken == refreshToken {
		return "", "", contract.ErrUnauthenticated
	}
	return tokens.AccessToken, tokens.RefreshToken, nil
}

func (b *BrowserOIDC) requestTokens(ctx context.Context, form url.Values) (tokenResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := contract.CheckCancellation(ctx); err != nil {
		return tokenResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, contract.ErrUnavailable
	}
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	formEncode := func(value string) string {
		return strings.NewReplacer("~", "%7E", "%2A", "*").Replace(url.QueryEscape(value))
	}
	req.SetBasicAuth(formEncode(b.config.ClientID), formEncode(b.config.ClientSecret))
	response, err := b.verifier.client.Do(req)
	if err != nil {
		if err := contract.CheckCancellation(ctx); err != nil {
			return tokenResponse{}, err
		}
		return tokenResponse{}, contract.ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusBadRequest {
		return tokenResponse{}, contract.ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if cancellation := contract.CheckCancellation(ctx); cancellation != nil {
		return tokenResponse{}, cancellation
	}
	var fields map[string]json.RawMessage
	if err != nil || len(raw) > 65536 || !contract.ValidJSONUnicode(raw) || json.Unmarshal(raw, &fields) != nil || fields == nil {
		return tokenResponse{}, contract.ErrUnavailable
	}
	if response.StatusCode == http.StatusBadRequest {
		var code string
		if json.Unmarshal(fields["error"], &code) == nil && code == "invalid_grant" {
			return tokenResponse{}, contract.ErrUnauthenticated
		}
	}
	var tokens tokenResponse
	if response.StatusCode != http.StatusOK || fields["error"] != nil ||
		json.Unmarshal(fields["token_type"], &tokens.TokenType) != nil || !strings.EqualFold(tokens.TokenType, "bearer") ||
		json.Unmarshal(fields["access_token"], &tokens.AccessToken) != nil || !visibleToken(tokens.AccessToken, 16384) ||
		(form.Get("grant_type") == "authorization_code" && json.Unmarshal(fields["id_token"], &tokens.IDToken) != nil) ||
		(fields["refresh_token"] != nil &&
			(json.Unmarshal(fields["refresh_token"], &tokens.RefreshToken) != nil || !visibleToken(tokens.RefreshToken, 16384))) {
		return tokenResponse{}, contract.ErrUnavailable
	}
	return tokens, nil
}
