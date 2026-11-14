-- Run with psql -v product_schema=ledger or -v product_schema=support.
CREATE TABLE :"product_schema".sso_candidates (
    environment text NOT NULL,
    workspace_id text NOT NULL,
    version text NOT NULL,
    verification_workspace text NOT NULL,
    organization_id text NOT NULL,
    protocol text NOT NULL CHECK (protocol IN ('oidc', 'saml')),
    settings_digest text NOT NULL,
    dispatched boolean NOT NULL DEFAULT false,
    provider_id text,
    tested_subject text,
    tested_at timestamptz,
    PRIMARY KEY (environment, workspace_id, version),
    UNIQUE (environment, verification_workspace)
);

-- Each row is both a policy publication and its durable product audit outbox entry.
CREATE TABLE :"product_schema".sso_publications (
    revision bigint GENERATED ALWAYS AS IDENTITY,
    environment text NOT NULL,
    workspace_id text NOT NULL,
    version text NOT NULL,
    previous_version text NOT NULL,
    event_id text NOT NULL UNIQUE,
    actor_issuer text NOT NULL,
    actor_subject text NOT NULL,
    recorded_at bigint NOT NULL,
    exported boolean NOT NULL DEFAULT false,
    PRIMARY KEY (environment, workspace_id, version),
    UNIQUE (environment, workspace_id, previous_version),
    FOREIGN KEY (environment, workspace_id, version)
      REFERENCES :"product_schema".sso_candidates (environment, workspace_id, version)
);

CREATE INDEX ON :"product_schema".sso_publications (environment, workspace_id, revision DESC);
