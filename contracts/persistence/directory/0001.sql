-- Install once in a deployment-owned transaction.
CREATE SCHEMA heimdall_directory;
REVOKE ALL ON SCHEMA heimdall_directory FROM PUBLIC;

CREATE TABLE heimdall_directory.deactivations (
    product_id text NOT NULL CHECK (product_id ~ '^[A-Za-z0-9_-]{1,64}$'),
    environment text NOT NULL CHECK (environment ~ '^[A-Za-z0-9_-]{1,64}$'),
    identity_hash bytea NOT NULL CHECK (octet_length(identity_hash) = 32),
    issuer text NOT NULL CHECK (octet_length(issuer) BETWEEN 1 AND 8192),
    subject text NOT NULL CHECK (subject ~ '^[A-Za-z0-9_-]{1,128}$'),
    organization_id text NOT NULL CHECK (organization_id ~ '^[A-Za-z0-9_-]{1,128}$'),
    observed_at bigint NOT NULL CHECK (observed_at BETWEEN 0 AND 9007199254740991),
    reason text NOT NULL CHECK (reason IN ('inactive', 'deleted')),
    PRIMARY KEY (product_id, environment, identity_hash)
);
