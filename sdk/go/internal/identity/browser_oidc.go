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
}

type BrowserOIDC struct {
	tokenEndpoint string
	config        BrowserOIDCConfig
	verifier      *TokenVerifier
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
		!visibleToken(config.ClientID, 128) || !visibleToken(config.ClientSecret, 4096) {
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
	return &BrowserOIDC{metadata.TokenEndpoint, config, verifier}, nil
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
	tokens, err := b.requestTokens(ctx, code, verifier)
	if err != nil {
		return nil, err
	}
	identity, err := b.verifier.verifyIDToken(ctx, tokens.IDToken, b.config.ClientID, nonce, tokens.AccessToken)
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

func (b *BrowserOIDC) requestTokens(ctx context.Context, code, verifier string) (tokenResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := contract.CheckCancellation(ctx); err != nil {
		return tokenResponse{}, err
	}
	body := url.Values{
		"grant_type": {"authorization_code"}, "code": {code},
		"redirect_uri": {b.config.RedirectURI}, "code_verifier": {verifier},
	}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.tokenEndpoint, strings.NewReader(body))
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
		json.Unmarshal(fields["id_token"], &tokens.IDToken) != nil || !visibleToken(tokens.IDToken, 16384) ||
		(fields["refresh_token"] != nil &&
			(json.Unmarshal(fields["refresh_token"], &tokens.RefreshToken) != nil || !visibleToken(tokens.RefreshToken, 16384))) {
		return tokenResponse{}, contract.ErrUnavailable
	}
	return tokens, nil
}
