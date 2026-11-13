package heimdall

import (
	"context"
	"errors"
	"github.com/techsavvyash/heimdall/sdk/go/internal/auditflow"
	"github.com/techsavvyash/heimdall/sdk/go/internal/browserlogin"
	"github.com/techsavvyash/heimdall/sdk/go/internal/configuration"
	"github.com/techsavvyash/heimdall/sdk/go/internal/contract"
	"github.com/techsavvyash/heimdall/sdk/go/internal/identity"
	"github.com/techsavvyash/heimdall/sdk/go/internal/modelbinding"
	"github.com/techsavvyash/heimdall/sdk/go/internal/postgres"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
)

type Identity = identity.Identity
type IdentityNegative = identity.Negative
type IdentityGate = identity.Gate
type AuditReceipt = contract.AuditReceipt
type Principal = identity.Principal
type Error = contract.Error

var (
	ErrInvalidArgument  = contract.ErrInvalidArgument
	ErrUnauthenticated  = contract.ErrUnauthenticated
	ErrForbidden        = contract.ErrForbidden
	ErrNotFound         = contract.ErrNotFound
	ErrConflict         = contract.ErrConflict
	ErrUnavailable      = contract.ErrUnavailable
	ErrDeadlineExceeded = contract.ErrDeadlineExceeded
	ErrCancelled        = contract.ErrCancelled
	ErrAuditUnavailable = contract.ErrAuditUnavailable
	ErrOutcomeUnknown   = contract.ErrOutcomeUnknown
)

type RuntimeStorage struct {
	ProductID, Environment string
	// Ready validates durable store readiness and scope, without migrations.
	Ready       func(context.Context) error
	Identity    IdentityGate
	AppendAudit func(context.Context, map[string]any) (AuditReceipt, error)
	Browser     *BrowserStorage
}
type Credentials struct {
	ClientSecret, OpenFGAKey func(context.Context) (string, error)
}
type Options struct {
	PolicyVersion       string
	IdentityFreshnessMs *int64
	Credentials         Credentials
	Storage             RuntimeStorage
}
type BrowserStorage struct {
	Login    *postgres.BrowserLoginRecords
	Sessions *postgres.SessionRecords
}

type SessionOptions struct {
	CallbackURL, ReturnTo, ZitadelAPIAudience string
	AbsoluteLifetime, IdleLifetime            time.Duration
}

type Sessions interface {
	Start(http.ResponseWriter, *http.Request) error
	Callback(http.ResponseWriter, *http.Request) error
	Authenticate(*http.Request) (*Principal, error)
	Logout(http.ResponseWriter, *http.Request) error
}

type Client struct {
	authentication *auditflow.Authentication
	createSessions func(context.Context, SessionOptions) (Sessions, error)
	tenantCheck    func(context.Context, string, *Principal, string) (bool, error)
}

func (c *Client) NewSessions(ctx context.Context, options SessionOptions) (Sessions, error) {
	if c == nil || ctx == nil || c.createSessions == nil {
		return nil, ErrInvalidArgument
	}
	return c.createSessions(ctx, options)
}

type Tokens struct{ authentication *auditflow.Authentication }

func (c *Client) Tokens() *Tokens {
	if c == nil {
		return &Tokens{}
	}
	return &Tokens{c.authentication}
}
func (t *Tokens) Verify(ctx context.Context, raw string) (*Principal, error) {
	if t == nil || t.authentication == nil || ctx == nil {
		return nil, ErrInvalidArgument
	}
	return t.authentication.Verify(ctx, raw, "bearer")
}

// New constructs a scoped core runtime. Storage ports are trusted durable integrations.
func New(ctx context.Context, configJSON, manifestJSON []byte, options Options) (*Client, error) {
	if ctx == nil {
		return nil, ErrInvalidArgument
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := contract.CheckCancellation(ctx); err != nil {
		return nil, err
	}
	deployment, err := configuration.Parse(configJSON, manifestJSON)
	if err != nil {
		return nil, err
	}
	cfg := deployment.Config()
	storage := options.Storage
	freshness := int64(60000)
	if options.IdentityFreshnessMs != nil {
		freshness = *options.IdentityFreshnessMs
	}
	if ctx == nil || !regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`).MatchString(options.PolicyVersion) || freshness < 0 || freshness > 60000 || storage.ProductID != cfg.ProductID || storage.Environment != cfg.Environment || storage.Ready == nil || storage.AppendAudit == nil || storage.Identity.Check == nil || storage.Identity.Invalidate == nil || options.Credentials.ClientSecret == nil || options.Credentials.OpenFGAKey == nil {
		return nil, ErrInvalidArgument
	}
	if err = contract.CheckCancellation(ctx); err != nil {
		return nil, err
	}
	ready := make(chan error, 1)
	go func() {
		if err := contract.CheckCancellation(ctx); err != nil {
			ready <- err
			return
		}
		ready <- storage.Ready(ctx)
	}()
	select {
	case <-ctx.Done():
		return nil, contract.CheckCancellation(ctx)
	case err = <-ready:
	}
	if err != nil {
		if cancellation := contract.CheckCancellation(ctx); cancellation != nil {
			return nil, cancellation
		}
		var typed *contract.Error
		if errors.As(err, &typed) {
			return nil, contract.NewError(typed.Code, "")
		}
		return nil, ErrUnavailable
	}
	profile := cfg.TransportProfile
	if profile == "" {
		profile = "production"
	}
	metadata, err := identity.DiscoverOIDC(ctx, cfg.Issuer, profile)
	if err != nil {
		return nil, err
	}
	if metadata.IntrospectionEndpoint != strings.TrimSuffix(cfg.Issuer, "/")+"/oauth/v2/introspect" {
		return nil, ErrUnavailable
	}
	model, err := modelbinding.New(modelbinding.Config{URL: cfg.OpenFGA.URL, StoreID: cfg.OpenFGA.StoreID, ModelID: cfg.OpenFGA.ModelID, Profile: profile}, deployment.Manifest(), contract.Binding{ProductID: cfg.ProductID, ManifestVersion: cfg.ManifestVersion, ClientKey: cfg.ClientKey, TransportProfile: profile}, deployment.Artifact(), options.Credentials.OpenFGAKey)
	if err != nil {
		return nil, err
	}
	verified, err := model.Verify(ctx)
	if err != nil {
		return nil, err
	}
	if err = contract.CheckCancellation(ctx); err != nil {
		return nil, err
	}
	authentication, err := auditflow.NewAuthentication(identity.StatusConfig{Config: identity.Config{Issuer: cfg.Issuer, Audience: cfg.Audience, JWKSURI: metadata.JWKSURI, Profile: profile}, ProductID: cfg.ProductID, Environment: cfg.Environment, PolicyVersion: options.PolicyVersion, ClientID: cfg.ClientID, FreshnessMs: &freshness}, options.Credentials.ClientSecret, storage.Identity, storage.AppendAudit, nil)
	if err != nil {
		return nil, err
	}
	client := &Client{authentication: authentication}
	client.tenantCheck = func(ctx context.Context, tenant string, actor *Principal, permission string) (bool, error) {
		if _, err := verified.Permission(permission); err != nil {
			return false, err
		}
		return authentication.CheckPermission(ctx, actor, tenant, permission, cfg.OpenFGA.ModelID, cfg.ManifestVersion, func(ctx context.Context) (bool, error) {
			id := actor.Identity()
			user, err := contract.EncodeIdentity(id.Issuer, id.Subject)
			if err != nil {
				return false, err
			}
			return model.Check(ctx, user, tenant, permission)
		})
	}
	if storage.Browser != nil && storage.Browser.Login != nil && storage.Browser.Sessions != nil {
		browser := *storage.Browser
		redirects := deployment.Manifest().Clients[cfg.ClientKey].RedirectURIs
		client.createSessions = func(ctx context.Context, session SessionOptions) (Sessions, error) {
			if !slices.Contains(redirects, session.CallbackURL) || session.ZitadelAPIAudience == "" {
				return nil, ErrInvalidArgument
			}
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			type secretResult struct {
				value string
				err   error
			}
			secret := make(chan secretResult, 1)
			if err := contract.CheckCancellation(ctx); err != nil {
				return nil, err
			}
			go func() { value, err := options.Credentials.ClientSecret(ctx); secret <- secretResult{value, err} }()
			var credential string
			select {
			case <-ctx.Done():
				return nil, contract.CheckCancellation(ctx)
			case result := <-secret:
				if result.err != nil {
					return nil, ErrUnavailable
				}
				credential = result.value
			}
			login, err := browserlogin.NewHTTP(ctx, identity.BrowserOIDCConfig{
				Issuer: cfg.Issuer, Profile: profile, Audience: cfg.Audience, ClientID: cfg.ClientID, ClientSecret: credential,
				RedirectURI: session.CallbackURL, ZitadelAPIAudience: session.ZitadelAPIAudience,
			}, browser.Login, cfg.ProductID, cfg.Environment, "default")
			if err != nil {
				return nil, err
			}
			return browserlogin.NewSessions(login, authentication, browser.Sessions, session.ReturnTo, session.AbsoluteLifetime, session.IdleLifetime, nil)
		}
	}
	return client, nil
}

// AuditEventDigest computes the portable digest for a custom durable audit implementation.
// It does not validate event semantics or prove persistence.
func AuditEventDigest(event map[string]any) (string, error) { return contract.AuditDigest(event) }

type Tenant struct {
	client *Client
	id     string
}

var tenantID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func (c *Client) Tenant(id string) *Tenant { return &Tenant{c, id} }
func (t *Tenant) Can(ctx context.Context, actor *Principal, permission string) (bool, error) {
	if ctx == nil || t == nil || t.client == nil || t.client.tenantCheck == nil || !tenantID.MatchString(t.id) {
		return false, ErrInvalidArgument
	}
	return t.client.tenantCheck(ctx, t.id, actor, permission)
}
func (t *Tenant) Require(ctx context.Context, actor *Principal, permission string) error {
	allowed, err := t.Can(ctx, actor, permission)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}
