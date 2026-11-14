package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"

	heimdall "github.com/techsavvyash/heimdall/sdk/go"
	"github.com/techsavvyash/heimdall/sdk/go/postgres"
	"github.com/techsavvyash/heimdall/sdk/go/provisioning"
)

const policyPublicationType = "product.sso.policy_published"
const policyColumns = "organization_id,provider_id,version,protocol,900000"

var providerIdentifier = regexp.MustCompile("^[A-Za-z0-9_-]{1,128}$")
var providerVersion = regexp.MustCompile("^[a-f0-9]{32}$")

type providerScope struct{ OrganizationID, TestSubject string }
type providerSettings struct {
	db                         *sql.DB
	environment, issuer, token string
	fallback                   heimdall.TenantSSOPolicySource
	mapping                    map[string]providerScope
}

func newProviderSettings(db *sql.DB, environment, issuer string, fallback heimdall.TenantSSOPolicySource, allowSettings bool) (*providerSettings, error) {
	raw := os.Getenv("HEIMDALL_PROVIDER_SETTINGS")
	p := &providerSettings{db: db, environment: environment, issuer: issuer, fallback: fallback, token: os.Getenv("HEIMDALL_PROVIDER_SETTINGS_TOKEN")}
	if raw == "" || !allowSettings {
		return p, nil
	}
	if json.Unmarshal([]byte(raw), &p.mapping) != nil || len(p.mapping) == 0 || p.token == "" {
		return nil, heimdall.ErrInvalidArgument
	}
	for id, scope := range p.mapping {
		if !providerIdentifier.MatchString(id) || strings.HasPrefix(id, "sso-test-") ||
			!providerIdentifier.MatchString(scope.OrganizationID) || !providerIdentifier.MatchString(scope.TestSubject) {
			return nil, heimdall.ErrInvalidArgument
		}
	}
	return p, nil
}

func scanPolicy(row *sql.Row) (*heimdall.TenantSSOPolicy, error) {
	var p heimdall.TenantSSOPolicy
	err := row.Scan(&p.OrganizationID, &p.ProviderID, &p.PolicyVersion, &p.Protocol, &p.MaxAgeMS)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &p, err
}

func (p *providerSettings) policy(ctx context.Context, workspace string) (*heimdall.TenantSSOPolicy, error) {
	if strings.HasPrefix(workspace, "sso-test-") {
		if len(p.mapping) == 0 {
			return nil, nil
		}
		return scanPolicy(p.db.QueryRowContext(ctx,
			"SELECT "+policyColumns+` FROM support.sso_candidates c
			 WHERE environment=$1 AND verification_workspace=$2 AND provider_id IS NOT NULL
			 AND NOT EXISTS (SELECT 1 FROM support.sso_publications p
			   WHERE (p.environment,p.workspace_id,p.version)=(c.environment,c.workspace_id,c.version))`,
			p.environment, workspace))
	}
	current, err := scanPolicy(p.db.QueryRowContext(ctx,
		"SELECT "+policyColumns+` FROM support.sso_publications p JOIN support.sso_candidates c
		 USING (environment,workspace_id,version)
		 WHERE environment=$1 AND workspace_id=$2 ORDER BY revision DESC LIMIT 1`,
		p.environment, workspace))
	if current != nil || err != nil || p.fallback == nil {
		return current, err
	}
	return p.fallback(ctx, workspace)
}

type providerInput struct {
	Version, Protocol, Issuer, ClientID, ClientSecret, MetadataXML, ExistingProviderID, ExpectedPolicyVersion string
}

func parseProviderInput(fields map[string]json.RawMessage, action string) (providerInput, error) {
	var input providerInput
	for key, raw := range fields {
		var field *string
		switch key {
		case "version":
			field = &input.Version
		case "protocol":
			field = &input.Protocol
		case "issuer":
			field = &input.Issuer
		case "clientId":
			field = &input.ClientID
		case "clientSecret":
			field = &input.ClientSecret
		case "metadataXml":
			field = &input.MetadataXML
		case "existingProviderId":
			field = &input.ExistingProviderID
		case "expectedPolicyVersion":
			field = &input.ExpectedPolicyVersion
		}
		if field == nil || json.Unmarshal(raw, field) != nil ||
			(string(raw) == "null" && key != "expectedPolicyVersion") {
			return input, heimdall.ErrInvalidArgument
		}
	}
	valid := providerVersion.MatchString(input.Version)
	switch action {
	case "test":
		valid = valid && len(fields) == 1
	case "activate":
		raw, found := fields["expectedPolicyVersion"]
		valid = valid && len(fields) == 2 && found &&
			(string(raw) == "null" || providerIdentifier.MatchString(input.ExpectedPolicyVersion))
	case "prepare":
		allowed := map[string]bool{"version": true, "protocol": true, "existingProviderId": true}
		if input.Protocol == "oidc" {
			allowed["issuer"], allowed["clientId"], allowed["clientSecret"] = true, true, true
		} else if input.Protocol == "saml" {
			allowed["metadataXml"] = true
		} else {
			valid = false
		}
		for key := range fields {
			valid = valid && allowed[key]
		}
		if _, recovery := fields["existingProviderId"]; recovery {
			_, secret := fields["clientSecret"]
			valid = valid && providerIdentifier.MatchString(input.ExistingProviderID) && !secret
		}
	}
	if !valid {
		return input, heimdall.ErrInvalidArgument
	}
	return input, nil
}

func randomProviderID(size int) (string, error) {
	bytes := make([]byte, size)
	_, err := rand.Read(bytes)
	return hex.EncodeToString(bytes), err
}

func (p *providerSettings) route(auth *heimdall.Client, sessions heimdall.Sessions, origin, ownerRole string) func(http.ResponseWriter, *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error {
		workspace, action := r.PathValue("workspace"), r.PathValue("action")
		if action != "prepare" && action != "test" && action != "activate" {
			return heimdall.ErrNotFound
		}
		scope, allowed := p.mapping[workspace]
		if !allowed || r.Header.Get("Origin") != origin {
			return heimdall.ErrForbidden
		}
		actor, err := sessions.Authenticate(r)
		if err != nil {
			return err
		}
		ctx := r.Context()
		var activeVersion string
		if action == "test" {
			if actor.Identity().Subject != scope.TestSubject {
				return heimdall.ErrForbidden
			}
		} else {
			if action == "activate" {
				active, err := p.policy(ctx, workspace)
				if err != nil {
					return err
				}
				if active != nil {
					activeVersion = active.PolicyVersion
				}
			}
			roles, err := auth.Tenant(workspace).Roles(ctx, actor)
			if err != nil {
				return err
			}
			if !slices.Contains(roles, ownerRole) {
				return heimdall.ErrForbidden
			}
		}
		fields, err := readJSONInput(w, r, 352000)
		if err != nil {
			return err
		}
		input, err := parseProviderInput(fields, action)
		if err != nil {
			return err
		}
		switch action {
		case "prepare":
			result, err := p.prepare(ctx, workspace, scope, input)
			if err != nil {
				return err
			}
			writeJSON(w, 200, result)
		case "test":
			if err := p.test(ctx, auth, actor, workspace, scope, input.Version); err != nil {
				return err
			}
			writeJSON(w, 200, map[string]bool{"tested": true})
		case "activate":
			if activeVersion != input.ExpectedPolicyVersion || activeVersion == input.Version {
				return heimdall.ErrConflict
			}
			eventID, err := p.activate(ctx, actor, workspace, scope, input)
			if err != nil {
				return err
			}
			writeJSON(w, 200, map[string]string{"version": input.Version, "eventId": eventID})
		}
		return nil
	}
}

func (p *providerSettings) prepare(ctx context.Context, workspace string, scope providerScope, input providerInput) (map[string]string, error) {
	options := provisioning.EnterpriseProviderOptions{
		Issuer: p.issuer, OrganizationID: scope.OrganizationID, Token: p.token,
		Name: "heimdall-support-" + input.Version, ExistingProviderID: input.ExistingProviderID,
	}
	var material []string
	if input.Protocol == "oidc" {
		options.OIDC = &provisioning.OIDCProviderSettings{Issuer: input.Issuer, ClientID: input.ClientID, ClientSecret: input.ClientSecret}
		material = []string{input.Issuer, input.ClientID}
	} else {
		metadata, err := base64.StdEncoding.DecodeString(input.MetadataXML)
		if err != nil || base64.StdEncoding.EncodeToString(metadata) != input.MetadataXML {
			return nil, heimdall.ErrInvalidArgument
		}
		options.SAMLMetadataXML = metadata
		material = []string{input.MetadataXML}
	}
	raw, err := json.Marshal(material)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	fingerprint := hex.EncodeToString(digest[:])
	if options.ExistingProviderID == "" {
		verification, err := randomProviderID(24)
		if err != nil {
			return nil, err
		}
		_, err = p.db.ExecContext(ctx, `INSERT INTO support.sso_candidates
		 (environment,workspace_id,version,verification_workspace,organization_id,protocol,settings_digest)
		 VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (environment,workspace_id,version) DO NOTHING`,
			p.environment, workspace, input.Version, "sso-test-"+verification, scope.OrganizationID, input.Protocol, fingerprint)
		if err != nil {
			return nil, err
		}
		options.ClaimDispatch = func(ctx context.Context) error {
			return requireSettingsWrite(p.db.ExecContext(ctx,
				`UPDATE support.sso_candidates SET dispatched=true
				 WHERE environment=$1 AND workspace_id=$2 AND version=$3 AND NOT dispatched`,
				p.environment, workspace, input.Version))
		}
	}
	var verification string
	var dispatched bool
	var provider sql.NullString
	err = p.db.QueryRowContext(ctx, `SELECT verification_workspace,dispatched,provider_id FROM support.sso_candidates
	 WHERE environment=$1 AND workspace_id=$2 AND version=$3
	   AND organization_id=$4 AND protocol=$5 AND settings_digest=$6`,
		p.environment, workspace, input.Version, scope.OrganizationID, input.Protocol, fingerprint).Scan(&verification, &dispatched, &provider)
	if errors.Is(err, sql.ErrNoRows) || (options.ExistingProviderID != "" &&
		(!dispatched || (provider.Valid && provider.String != options.ExistingProviderID))) {
		return nil, heimdall.ErrConflict
	}
	if err != nil {
		return nil, err
	}
	result, err := provisioning.PrepareEnterpriseProvider(ctx, options)
	if err != nil {
		return nil, err
	}
	if err := requireSettingsWrite(p.db.ExecContext(ctx, `UPDATE support.sso_candidates SET provider_id=$4
	 WHERE environment=$1 AND workspace_id=$2 AND version=$3 AND dispatched AND (provider_id IS NULL OR provider_id=$4)`,
		p.environment, workspace, input.Version, result.ProviderID)); err != nil {
		return nil, err
	}
	response := map[string]string{"providerId": result.ProviderID, "organizationId": result.OrganizationID,
		"protocol": result.Protocol, "version": input.Version, "verificationWorkspaceId": verification}
	if result.Protocol == "oidc" {
		response["callbackUrl"] = result.CallbackURL
	} else {
		response["metadataUrl"] = result.MetadataURL
	}
	return response, nil
}

func (p *providerSettings) test(ctx context.Context, auth *heimdall.Client, actor *heimdall.Principal, workspace string, scope providerScope, version string) error {
	var verification string
	var candidate heimdall.TenantSSOPolicy
	err := p.db.QueryRowContext(ctx,
		"SELECT verification_workspace, "+policyColumns+` FROM support.sso_candidates
		 WHERE environment=$1 AND workspace_id=$2 AND version=$3 AND organization_id=$4 AND provider_id IS NOT NULL`,
		p.environment, workspace, version, scope.OrganizationID).Scan(
		&verification, &candidate.OrganizationID, &candidate.ProviderID, &candidate.PolicyVersion, &candidate.Protocol, &candidate.MaxAgeMS)
	if errors.Is(err, sql.ErrNoRows) {
		return heimdall.ErrConflict
	}
	if err != nil {
		return err
	}
	candidate.MaxAgeMS = 240000
	if err := auth.VerifyEnterpriseProviderTest(ctx, actor, verification, candidate); err != nil {
		return err
	}
	return requireSettingsWrite(p.db.ExecContext(ctx, `UPDATE support.sso_candidates c
	 SET tested_subject=$4,tested_at=clock_timestamp() WHERE environment=$1 AND workspace_id=$2 AND version=$3
	 AND NOT EXISTS (SELECT 1 FROM support.sso_publications p
	   WHERE (p.environment,p.workspace_id,p.version)=(c.environment,c.workspace_id,c.version))`,
		p.environment, workspace, version, actor.Identity().Subject))
}

func (p *providerSettings) activate(ctx context.Context, actor *heimdall.Principal, workspace string, scope providerScope, input providerInput) (string, error) {
	var baseline string
	if p.fallback != nil {
		fallback, err := p.fallback(ctx, workspace)
		if err != nil {
			return "", err
		}
		if fallback != nil {
			baseline = fallback.PolicyVersion
		}
	}
	id, err := randomProviderID(16)
	if err != nil {
		return "", err
	}
	identity := actor.Identity()
	err = requireSettingsWrite(p.db.ExecContext(ctx, `INSERT INTO support.sso_publications
	 (environment,workspace_id,version,previous_version,event_id,actor_issuer,actor_subject,recorded_at)
	 SELECT $1,$2,$3,$4,$5,$6,$7,(extract(epoch FROM clock_timestamp())*1000)::bigint
	   FROM support.sso_candidates WHERE environment=$1 AND workspace_id=$2 AND version=$3
		 AND organization_id=$8 AND tested_subject=$9 AND tested_at > clock_timestamp()-interval '45 seconds'
		 AND $4=coalesce((SELECT version FROM support.sso_publications
		   WHERE environment=$1 AND workspace_id=$2 ORDER BY revision DESC LIMIT 1),$10)
	 ON CONFLICT DO NOTHING`, p.environment, workspace, input.Version, input.ExpectedPolicyVersion,
		id, identity.Issuer, identity.Subject, scope.OrganizationID, scope.TestSubject, baseline))
	return id, err
}

func requireSettingsWrite(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return heimdall.ErrConflict
	}
	return nil
}

func validatePolicyPublication(kind string, version int, attributes map[string]any) bool {
	next, nextOK := attributes["version"].(string)
	prev, prevOK := attributes["previousVersion"]
	previous, previousString := prev.(string)
	return kind == policyPublicationType && version == 1 && len(attributes) == 2 &&
		nextOK && providerVersion.MatchString(next) && prevOK &&
		(prev == nil || (previousString && providerIdentifier.MatchString(previous)))
}

func exportPolicyPublications(ctx context.Context, db, audit *sql.DB, product, environment, sink, tenant string) error {
	if tenant == "" {
		return heimdall.ErrInvalidArgument
	}
	writer, err := postgres.NewCustomAuditWriter(audit, postgres.CustomAuditOptions{
		ProductID: product, Environment: environment, AuditSinks: []string{sink}, ValidateCustom: validatePolicyPublication,
	})
	if err != nil {
		return err
	}
	rows, err := db.QueryContext(ctx, `SELECT event_id,version,previous_version,actor_issuer,actor_subject,recorded_at
	 FROM support.sso_publications WHERE environment=$1 AND workspace_id=$2 AND NOT exported ORDER BY revision LIMIT 20`,
		environment, tenant)
	if err != nil {
		return err
	}
	type publication struct {
		id, version, previous, issuer, subject string
		at                                     int64
	}
	var pending []publication
	for rows.Next() {
		var row publication
		if err := rows.Scan(&row.id, &row.version, &row.previous, &row.issuer, &row.subject, &row.at); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, row := range pending {
		var previous any
		if row.previous != "" {
			previous = row.previous
		}
		_, err := writer.Record(ctx, postgres.CustomAuditEvent{
			EventID: row.id, RequestID: row.id, EventType: policyPublicationType, EventVersion: 1,
			OccurredAt: row.at, RecordedAt: row.at, TenantID: &tenant,
			Actor:  map[string]any{"kind": "user", "issuer": row.issuer, "subject": row.subject},
			Target: map[string]any{"type": "workspace", "id": tenant},
			Action: "sso.policy.publish", Outcome: "succeeded",
			Attributes: map[string]any{"version": row.version, "previousVersion": previous},
		})
		if err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, "UPDATE support.sso_publications SET exported=true WHERE environment=$1 AND workspace_id=$2 AND event_id=$3",
			environment, tenant, row.id); err != nil {
			return err
		}
	}
	return nil
}
