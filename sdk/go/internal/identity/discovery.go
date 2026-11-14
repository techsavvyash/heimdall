package identity

import (
	"context"
	"encoding/json"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// OIDCMetadata contains only endpoints required by the selected backend profile.
// Values are copied; discovery does not enable other advertised algorithms.
type OIDCMetadata struct{ Issuer, JWKSURI, AuthorizationEndpoint, TokenEndpoint, IntrospectionEndpoint, RevocationEndpoint string }

func ValidateOIDCMetadata(raw []byte, issuer, profile string) (OIDCMetadata, error) {
	result := OIDCMetadata{}
	if contract.ValidateEndpoint(issuer, profile) != nil {
		return result, contract.ErrInvalidArgument
	}
	var data map[string]json.RawMessage
	if len(raw) > 65536 || !contract.ValidJSONUnicode(raw) || json.Unmarshal(raw, &data) != nil {
		return result, contract.ErrUnavailable
	}
	var actual string
	if json.Unmarshal(data["issuer"], &actual) != nil || actual != issuer {
		return result, contract.ErrUnavailable
	}
	result.Issuer = issuer
	origin, _ := url.Parse(issuer)
	for key, dest := range map[string]*string{"jwks_uri": &result.JWKSURI, "authorization_endpoint": &result.AuthorizationEndpoint, "token_endpoint": &result.TokenEndpoint, "introspection_endpoint": &result.IntrospectionEndpoint, "revocation_endpoint": &result.RevocationEndpoint} {
		if json.Unmarshal(data[key], dest) != nil || contract.ValidateEndpoint(*dest, profile) != nil {
			return OIDCMetadata{}, contract.ErrUnavailable
		}
		endpoint, _ := url.Parse(*dest)
		port := func(u *url.URL) string {
			if u.Port() != "" {
				return u.Port()
			}
			if u.Scheme == "https" {
				return "443"
			}
			return "80"
		}
		if origin.Scheme != endpoint.Scheme || origin.Hostname() != endpoint.Hostname() || port(origin) != port(endpoint) {
			return OIDCMetadata{}, contract.ErrUnavailable
		}
	}
	for key, required := range map[string][]string{"response_types_supported": {"code"}, "grant_types_supported": {"authorization_code", "refresh_token"}, "id_token_signing_alg_values_supported": {"RS256"}, "code_challenge_methods_supported": {"S256"}, "token_endpoint_auth_methods_supported": {"client_secret_basic"}, "introspection_endpoint_auth_methods_supported": {"client_secret_basic"}, "revocation_endpoint_auth_methods_supported": {"client_secret_basic"}} {
		var values []any
		if json.Unmarshal(data[key], &values) != nil {
			return OIDCMetadata{}, contract.ErrUnavailable
		}
		for _, value := range values {
			if _, ok := value.(string); !ok {
				return OIDCMetadata{}, contract.ErrUnavailable
			}
		}
		for _, want := range required {
			found := false
			for _, value := range values {
				if value == want {
					found = true
				}
			}
			if !found {
				return OIDCMetadata{}, contract.ErrUnavailable
			}
		}
	}
	return result, nil
}

func DiscoverOIDC(ctx context.Context, issuer, profile string) (OIDCMetadata, error) {
	if contract.ValidateEndpoint(issuer, profile) != nil {
		return OIDCMetadata{}, contract.ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := contract.CheckCancellation(ctx); err != nil {
		return OIDCMetadata{}, err
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(issuer, "/")+"/.well-known/openid-configuration", nil)
	req.Header.Set("Accept", "application/json")
	failure := func() (OIDCMetadata, error) {
		if err := contract.CheckCancellation(ctx); err != nil {
			return OIDCMetadata{}, err
		}
		return OIDCMetadata{}, contract.ErrUnavailable
	}
	resp, err := client.Do(req)
	if err != nil {
		return failure()
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return failure()
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil {
		return failure()
	}
	result, err := ValidateOIDCMetadata(raw, issuer, profile)
	if err != nil {
		return failure()
	}
	if err = contract.CheckCancellation(ctx); err != nil {
		return OIDCMetadata{}, err
	}
	return result, nil
}
