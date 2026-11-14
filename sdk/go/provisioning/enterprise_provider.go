package provisioning

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
)

type OIDCProviderSettings struct {
	Issuer, ClientID, ClientSecret string
}

type EnterpriseProviderOptions struct {
	Issuer, OrganizationID, Name, Token, TransportProfile string
	OIDC                                                  *OIDCProviderSettings
	SAMLMetadataXML                                       []byte
	ExistingProviderID                                    string
	ClaimDispatch                                         func(context.Context) error
}

type EnterpriseProviderCandidate struct {
	ProviderID, OrganizationID, Protocol string
	CallbackURL, MetadataURL             string
}

var enterpriseScopes = []string{"email", "openid", "profile"}

const (
	samlBinding   = "SAML_BINDING_REDIRECT"
	samlSignature = "SAML_SIGNATURE_RSA_SHA256"
	samlNameID    = "SAML_NAME_ID_FORMAT_PERSISTENT"
)

// PrepareEnterpriseProvider provisions a candidate after product authorization or verifies a recovered ID.
func PrepareEnterpriseProvider(ctx context.Context, o EnterpriseProviderOptions) (EnterpriseProviderCandidate, error) {
	empty := EnterpriseProviderCandidate{}
	profile := o.TransportProfile
	if profile == "" {
		profile = "production"
	}
	if ctx == nil || contract.ValidateEndpoint(o.Issuer, profile) != nil ||
		!providerID.MatchString(o.OrganizationID) || !utf8.ValidString(o.Name) ||
		strings.TrimSpace(o.Name) == "" || utf8.RuneCountInString(o.Name) > 200 ||
		len(o.Token) > 4096 || !providerToken.MatchString(o.Token) ||
		(o.OIDC == nil) == (o.SAMLMetadataXML == nil) ||
		(o.ExistingProviderID == "" && o.ClaimDispatch == nil) ||
		(o.ExistingProviderID != "" && (!providerID.MatchString(o.ExistingProviderID) || o.ClaimDispatch != nil)) {
		return empty, contract.ErrInvalidArgument
	}
	base, _ := url.Parse(o.Issuer)
	if base.Path != "" && base.Path != "/" {
		return empty, contract.ErrInvalidArgument
	}
	path, kind, protocol := "saml", "IDP_TYPE_SAML", "saml"
	payload := map[string]any{
		"name":            o.Name,
		"providerOptions": map[string]bool{"isLinkingAllowed": false, "isCreationAllowed": false, "isAutoCreation": false, "isAutoUpdate": false},
	}
	if o.OIDC != nil {
		oidc := *o.OIDC
		o.OIDC = &oidc
		if contract.ValidateEndpoint(oidc.Issuer, "production") != nil ||
			!utf8.ValidString(oidc.ClientID) || strings.TrimSpace(oidc.ClientID) == "" || utf8.RuneCountInString(oidc.ClientID) > 200 ||
			(o.ExistingProviderID == "" && (oidc.ClientSecret == "" || !utf8.ValidString(oidc.ClientSecret) || len(oidc.ClientSecret) > 4096)) {
			return empty, contract.ErrInvalidArgument
		}
		path, kind, protocol = "generic_oidc", "IDP_TYPE_OIDC", "oidc"
		payload["issuer"], payload["clientId"], payload["clientSecret"] = oidc.Issuer, oidc.ClientID, oidc.ClientSecret
		payload["scopes"], payload["usePkce"] = []string{"openid", "profile", "email"}, true
	} else {
		if len(o.SAMLMetadataXML) == 0 || len(o.SAMLMetadataXML) > 262144 {
			return empty, contract.ErrInvalidArgument
		}
		o.SAMLMetadataXML = bytes.Clone(o.SAMLMetadataXML)
		payload["metadataXml"], payload["binding"] = o.SAMLMetadataXML, samlBinding
		payload["signatureAlgorithm"], payload["nameIdFormat"], payload["withSignedRequest"] = samlSignature, samlNameID, true
	}

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	id := o.ExistingProviderID
	if id == "" {
		if err := contract.CheckCancellation(ctx); err != nil {
			return empty, err
		}
		if err := runSetupCallback(ctx, o.ClaimDispatch); err != nil {
			var known *contract.Error
			if errors.As(err, &known) {
				return empty, contract.NewError(known.Code, "")
			}
			return empty, contract.ErrUnavailable
		}
		if err := contract.CheckCancellation(ctx); err != nil {
			return empty, err
		}
		var created struct{ ID string }
		found, err := providerCall(ctx, client, o.Issuer, o.Token, "/management/v1/idps/"+path, payload, &created, false, o.OrganizationID)
		if err != nil || !found || !providerID.MatchString(created.ID) {
			return empty, contract.ErrOutcomeUnknown
		}
		id = created.ID
	}

	var response struct {
		IDP *struct {
			ID, Name, State, Type string
			Details               struct{ ResourceOwner string }
			Config                struct {
				Options map[string]any
				OIDC    *struct {
					Issuer, ClientID string
					Scopes           []string
				}
				SAML *struct {
					MetadataXML                               []byte
					Binding, SignatureAlgorithm, NameIDFormat string
					WithSignedRequest                         bool
				}
			}
		}
	}
	found, err := providerCall(ctx, client, o.Issuer, o.Token, "/v2/idps/"+id, nil, &response, false, o.OrganizationID)
	if o.ExistingProviderID == "" && (err != nil || !found || response.IDP == nil) {
		return empty, contract.NewError(contract.OutcomeUnknown, id)
	}
	if err != nil {
		return empty, err
	}
	mismatch := contract.NewError(contract.Conflict, id)
	p := response.IDP
	if !found || p == nil || p.ID != id || p.Name != o.Name || p.State != "IDP_STATE_ACTIVE" ||
		p.Type != kind || p.Details.ResourceOwner != o.OrganizationID || p.Config.Options == nil {
		return empty, mismatch
	}
	for _, key := range []string{"isLinkingAllowed", "isCreationAllowed", "isAutoCreation", "isAutoUpdate"} {
		if value, present := p.Config.Options[key]; present && value != false {
			return empty, mismatch
		}
	}
	result := EnterpriseProviderCandidate{ProviderID: id, OrganizationID: o.OrganizationID, Protocol: protocol}
	base.Path = ""
	if o.OIDC != nil {
		oidc := p.Config.OIDC
		if oidc == nil || oidc.Issuer != o.OIDC.Issuer || oidc.ClientID != o.OIDC.ClientID {
			return empty, mismatch
		}
		slices.Sort(oidc.Scopes)
		if !slices.Equal(oidc.Scopes, enterpriseScopes) {
			return empty, mismatch
		}
		result.CallbackURL = base.String() + "/idps/callback"
	} else {
		saml := p.Config.SAML
		if saml == nil || !bytes.Equal(saml.MetadataXML, o.SAMLMetadataXML) || saml.Binding != samlBinding ||
			!saml.WithSignedRequest || saml.SignatureAlgorithm != samlSignature || saml.NameIDFormat != samlNameID {
			return empty, mismatch
		}
		result.MetadataURL = base.String() + "/idps/" + id + "/saml/metadata"
	}
	return result, nil
}
