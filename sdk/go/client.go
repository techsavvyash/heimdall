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
	"maps"
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
	Managed     *ManagedStorage
}
type Credentials struct {
	ClientSecret, OpenFGAKey func(context.Context) (string, error)
}
type Options struct {
	AuthorizeWorkspaceCreation WorkspaceCreationPolicy
	PolicyVersion              string
	IdentityFreshnessMs        *int64
	Credentials                Credentials
	Storage                    RuntimeStorage
	SessionStorage             *SessionStoreOptions
}
type ManagedStorage struct {
	Allows     func(context.Context, string) (bool, error)
	Source     func(Identity) WorkspaceSource
	Workspace  func(context.Context, string) (*postgres.ManagedWorkspace, error)
	Operations func(postgres.OperationBinding) (*postgres.OperationRecords, error)
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
	onboarding     *WorkspaceOnboarding
	membership     *membershipRuntime
	discovery      *discoveryRuntime
	authentication *auditflow.Authentication
	createSessions func(context.Context, SessionOptions) (Sessions, error)
	tenantCheck    func(context.Context, string, *Principal, string) (bool, error)
	tenantRoles    func(context.Context, string, *Principal) ([]string, error)
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
	configJSON, manifestJSON = append([]byte(nil), configJSON...), append([]byte(nil), manifestJSON...)
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
	var loginStore browserlogin.LoginRecords
	var sessions browserlogin.SessionStore
	if storage.Browser != nil && storage.Browser.Login != nil && storage.Browser.Sessions != nil {
		loginStore, sessions = storage.Browser.Login, storage.Browser.Sessions
	}
	gate := storage.Identity
	if options.SessionStorage != nil {
		if storage.Browser != nil {
			return nil, ErrInvalidArgument
		}
		custom, err := browserlogin.NewCustomSessionStore(cfg.ProductID, cfg.Environment, *options.SessionStorage)
		if err != nil {
			return nil, err
		}
		loginStore, sessions = postgres.NewBrowserLoginRecords(custom, nil), custom
		gate.Invalidate = func(ctx context.Context, negative IdentityNegative) error {
			session, err := identity.ParseIdentityContext(negative.Context)
			if err != nil {
				return err
			}
			if session != nil {
				if negative.Reason == "token_inactive" {
					if err := custom.InvalidateSessionVersion(ctx, session, negative.Identity); err != nil {
						return err
					}
				}
				negative.Context = "bearer"
			}
			return storage.Identity.Invalidate(ctx, negative)
		}
	}
	authentication, err := auditflow.NewAuthentication(identity.StatusConfig{Config: identity.Config{Issuer: cfg.Issuer, Audience: cfg.Audience, JWKSURI: metadata.JWKSURI, Profile: profile}, ProductID: cfg.ProductID, Environment: cfg.Environment, PolicyVersion: options.PolicyVersion, ClientID: cfg.ClientID, FreshnessMs: &freshness}, options.Credentials.ClientSecret, gate, storage.AppendAudit, nil)
	if err != nil {
		return nil, err
	}
	var managed *ManagedStorage
	if storage.Managed != nil {
		if storage.Managed.Allows == nil || storage.Managed.Source == nil {
			return nil, ErrInvalidArgument
		}
		snapshot := *storage.Managed
		managed = &snapshot
	}
	client := &Client{authentication: authentication, discovery: &discoveryRuntime{
		scope:   contract.CursorScope{ProductID: cfg.ProductID, Environment: cfg.Environment, ModelID: cfg.OpenFGA.ModelID},
		version: cfg.ManifestVersion, auth: authentication, model: model, managed: managed,
	}}
	if managed != nil && managed.Operations != nil {
		records, err := managed.Operations(postgres.OperationBinding{
			ProductID: cfg.ProductID, Environment: cfg.Environment, StoreID: cfg.OpenFGA.StoreID,
			ModelID: cfg.OpenFGA.ModelID, ModelHash: verified.ModelHash(), ManifestVersion: cfg.ManifestVersion,
		})
		if err != nil {
			return nil, err
		}
		if records == nil {
			return nil, ErrInvalidArgument
		}
		if managed.Workspace != nil {
			client.membership = &membershipRuntime{deployment.Manifest().OwnerRole, gate, model, managed.Workspace}
		}
		client.onboarding = &WorkspaceOnboarding{
			config: configJSON, manifest: manifestJSON,
			cfg: cfg, records: records, auth: authentication, policy: options.AuthorizeWorkspaceCreation,
			policyVersion: options.PolicyVersion, key: options.Credentials.OpenFGAKey,
		}
	} else if options.AuthorizeWorkspaceCreation != nil {
		return nil, ErrInvalidArgument
	}
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
			if managed != nil {
				allowed, err := managed.Allows(ctx, tenant)
				if err != nil || !allowed {
					return false, err
				}
			}
			return model.Check(ctx, user, tenant, permission)
		})
	}
	roleNames := slices.Sorted(maps.Keys(deployment.Manifest().Roles))
	client.tenantRoles = func(ctx context.Context, tenant string, actor *Principal) ([]string, error) {
		result := make([]string, 0)
		_, err := authentication.CheckLinkedPermission(ctx, actor, tenant, "roles.list", cfg.OpenFGA.ModelID, cfg.ManifestVersion, "", func(ctx context.Context, parent string) (bool, error) {
			if managed != nil {
				allowed, err := managed.Allows(ctx, tenant)
				if err != nil || !allowed {
					return false, err
				}
			}
			id := actor.Identity()
			user, err := contract.EncodeIdentity(id.Issuer, id.Subject)
			if err != nil {
				return false, err
			}
			for _, role := range roleNames {
				allowed, err := authentication.CheckLinkedPermission(ctx, actor, tenant, "role."+role, cfg.OpenFGA.ModelID, cfg.ManifestVersion, parent, func(ctx context.Context, _ string) (bool, error) {
					if managed != nil {
						allowed, err := managed.Allows(ctx, tenant)
						if err != nil || !allowed {
							return false, err
						}
					}
					return model.CheckRole(ctx, user, tenant, role)
				})
				if err != nil {
					return false, err
				}
				if allowed {
					result = append(result, role)
				}
			}
			if managed != nil && len(result) > 0 {
				allowed, err := managed.Allows(ctx, tenant)
				if err != nil {
					return false, err
				}
				if !allowed {
					result = result[:0]
				}
			}
			return len(result) > 0, nil
		})
		if err != nil {
			return nil, err
		}
		return result, nil
	}
	if loginStore != nil && sessions != nil {
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
			}, loginStore, cfg.ProductID, cfg.Environment, "default")
			if err != nil {
				return nil, err
			}
			return browserlogin.NewSessions(login, authentication, sessions, session.ReturnTo, session.AbsoluteLifetime, session.IdleLifetime, nil)
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

func (t *Tenant) Roles(ctx context.Context, actor *Principal) ([]string, error) {
	if ctx == nil || t == nil || t.client == nil || t.client.tenantRoles == nil || !tenantID.MatchString(t.id) {
		return nil, ErrInvalidArgument
	}
	return t.client.tenantRoles(ctx, t.id, actor)
}
