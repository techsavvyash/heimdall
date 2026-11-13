-- Install after 0007 in a deployment-owned transaction.
CREATE TABLE heimdall_directory.event_checkpoints (
    product_id text NOT NULL CHECK (product_id ~ '^[A-Za-z0-9_-]{1,64}$'),
    environment text NOT NULL CHECK (environment ~ '^[A-Za-z0-9_-]{1,64}$'),
    issuer text NOT NULL CHECK (octet_length(issuer) BETWEEN 1 AND 2048),
    organization_id text NOT NULL CHECK (organization_id ~ '^[A-Za-z0-9_-]{1,128}$'),
    tenant_id text NOT NULL CHECK (tenant_id ~ '^[A-Za-z0-9_-]{1,128}$'),
    mapping_digest text NOT NULL CHECK (mapping_digest ~ '^[a-f0-9]{64}$'),
    next_micros bigint NOT NULL CHECK (next_micros BETWEEN 1 AND 253402300799999999),
    PRIMARY KEY (product_id, environment, issuer, organization_id, tenant_id)
);
